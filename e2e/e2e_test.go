//go:build e2e

// End-to-end tests against a real cluster with Argo Rollouts installed and
// the WavePlan CRD applied. Run via hack/e2e.sh, which creates a throwaway
// kind cluster and points KUBECONFIG at it.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/ChadiDridi/waveform/internal/argo"
	"github.com/ChadiDridi/waveform/internal/controller"
	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/gate"
	"github.com/ChadiDridi/waveform/internal/kube"
	"github.com/ChadiDridi/waveform/internal/prom"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

const ns = "shop"

var services = []string{"db", "api", "web"}

func client(t *testing.T) dynamic.Interface {
	t.Helper()
	if os.Getenv("KUBECONFIG") == "" {
		t.Fatal("KUBECONFIG must point at the e2e cluster (see hack/e2e.sh)")
	}
	dc, _, err := kube.Dynamic("", "")
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func rolloutObj(name, version string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"replicas":             int64(2),
			"revisionHistoryLimit": int64(5),
			"selector":             map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": name}},
				"spec":     map[string]any{"containers": containers(version)},
			},
			"strategy": map[string]any{"canary": map[string]any{"steps": []any{
				map[string]any{"pause": map[string]any{}},
				map[string]any{"setWeight": int64(50)},
				map[string]any{"pause": map[string]any{"duration": "2s"}},
			}}},
		},
	}}
}

func containers(version string) []any {
	return []any{map[string]any{
		"name":  "app",
		"image": "registry.k8s.io/pause:3.10",
		"env":   []any{map[string]any{"name": "VERSION", "value": version}},
	}}
}

// reset recreates the three rollouts at v1 and waits until they are healthy.
func reset(t *testing.T, dc dynamic.Interface) {
	t.Helper()
	ctx := context.Background()
	res := dc.Resource(argo.RolloutGVR).Namespace(ns)
	for _, s := range services {
		_ = res.Delete(ctx, s, metav1.DeleteOptions{})
	}
	waitFor(t, 2*time.Minute, "rollouts deleted", func() bool {
		l, err := res.List(ctx, metav1.ListOptions{})
		return err == nil && len(l.Items) == 0
	})
	for _, s := range services {
		if _, err := res.Create(ctx, rolloutObj(s, "v1"), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range services {
		waitState(t, dc, s, execute.StateHealthy)
	}
}

func setVersion(t *testing.T, dc dynamic.Interface, svc, version string) {
	t.Helper()
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers(version)}}}})
	if _, err := dc.Resource(argo.RolloutGVR).Namespace(ns).Patch(context.Background(), svc, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
}

func observe(t *testing.T, dc dynamic.Interface, svc string) execute.Observation {
	t.Helper()
	a := &argo.Actuator{Client: dc, Namespace: ns}
	obs, err := a.Observe(context.Background(), waveplan.Step{Service: svc, Rollout: waveplan.RolloutRef{Name: svc}})
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

func waitState(t *testing.T, dc dynamic.Interface, svc string, want execute.State) {
	t.Helper()
	waitFor(t, 3*time.Minute, fmt.Sprintf("%s %s", svc, want), func() bool { return observe(t, dc, svc).State == want })
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// servingVersion returns VERSION of the pods behind the rollout's stable RS.
func servingVersion(t *testing.T, dc dynamic.Interface, svc string) string {
	t.Helper()
	ctx := context.Background()
	ro, err := dc.Resource(argo.RolloutGVR).Namespace(ns).Get(ctx, svc, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stable, _, _ := unstructured.NestedString(ro.Object, "status", "stableRS")
	rsl, err := dc.Resource(argo.ReplicaSetGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rs := range rsl.Items {
		if rs.GetLabels()["rollouts-pod-template-hash"] != stable {
			continue
		}
		cs, _, _ := unstructured.NestedSlice(rs.Object, "spec", "template", "spec", "containers")
		env := cs[0].(map[string]any)["env"].([]any)[0].(map[string]any)
		return env["value"].(string)
	}
	t.Fatalf("%s: stable replicaset %q not found", svc, stable)
	return ""
}

// fakeProm returns burn rate 9 for services in breach, 0.1 otherwise.
func fakeProm(breach ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		v := "0.1"
		for _, b := range breach {
			if strings.Contains(q, `"`+b+`"`) {
				v = "9"
			}
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"%s"]}]}}`, v)
	}))
}

// TestCLIPlanAndRun: plan from an observed graph, emit a WavePlan, execute it
// with `waveform run`, all waves succeed in dependency order.
func TestCLIPlanAndRun(t *testing.T) {
	dc := client(t)
	reset(t, dc)
	promSrv := fakeProm()
	defer promSrv.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "waveform")
	build := exec.Command("go", "build", "-o", bin, "../cmd/waveform")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	graphFile := write("graph.json", `{"windowSeconds":60,"edges":[
		{"from":"web","to":"api","calls":600,"rps":10,"confidence":0.98},
		{"from":"api","to":"db","calls":600,"rps":10,"confidence":0.98}]}`)
	cfg := write("waveform.yaml", fmt.Sprintf(`namespace: %s
gate:
  prometheusURL: %s
  bake: 2s
  interval: 1s
  window: 1m
`, ns, promSrv.URL))
	out, err := exec.Command(bin, "plan", "--graph", graphFile, "--config", cfg, "--change", "web,api,db", "--format", "waveplan", "--name", "e2e").Output()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	wpFile := write("waveplan.yaml", string(out))

	for _, s := range services {
		setVersion(t, dc, s, "v2")
	}
	for _, s := range services {
		waitState(t, dc, s, execute.StateHeld)
	}

	run := exec.Command(bin, "run", "-f", wpFile, "--poll", "1s")
	run.Env = os.Environ()
	var log strings.Builder
	run.Stdout, run.Stderr = &log, &log
	done := make(chan error, 1)
	go func() { done <- run.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v\n%s", err, log.String())
		}
	case <-time.After(5 * time.Minute):
		run.Process.Kill()
		t.Fatalf("run timed out\n%s", log.String())
	}
	t.Logf("waveform run:\n%s", log.String())

	for _, s := range services {
		if obs := observe(t, dc, s); obs.State != execute.StateHealthy {
			t.Errorf("%s state = %s", s, obs.State)
		}
		if v := servingVersion(t, dc, s); v != "v2" {
			t.Errorf("%s serves %s, want v2", s, v)
		}
	}
	// Wave order: db before api before web.
	l := log.String()
	if !(strings.Index(l, "wave 1/3") < strings.Index(l, "wave 2/3") && strings.Index(l, "wave 2/3") < strings.Index(l, "wave 3/3")) {
		t.Errorf("unexpected wave progression:\n%s", l)
	}
}

// TestControllerGateBreachRollsBack: a WavePlan custom resource reconciled by
// the controller; the downstream gate breaches during wave 2, so api (in
// flight) is aborted and db (finished, linked) is undone, in that order.
func TestControllerGateBreachRollsBack(t *testing.T) {
	dc := client(t)
	reset(t, dc)
	promSrv := fakeProm("web")
	defer promSrv.Close()

	for _, s := range services {
		setVersion(t, dc, s, "v2")
	}
	for _, s := range services {
		waitState(t, dc, s, execute.StateHeld)
	}

	ctx := context.Background()
	step := func(svc string, after ...string) map[string]any {
		m := map[string]any{"service": svc, "kind": "additive", "rollout": map[string]any{"name": svc, "namespace": ns}}
		if len(after) > 0 {
			m["after"] = after
		}
		return m
	}
	wp := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": waveplan.APIVersion,
		"kind":       waveplan.Kind,
		"metadata":   map[string]any{"name": "e2e-rollback", "namespace": ns},
		"spec": map[string]any{
			"gate": map[string]any{"prometheusURL": promSrv.URL, "bake": "2s", "interval": "1s", "window": "1m"},
			"waves": []any{
				map[string]any{"index": int64(1), "steps": []any{step("db")}, "watch": []any{"db"}},
				map[string]any{"index": int64(2), "steps": []any{step("api", "db")}, "watch": []any{"api", "web"}},
				map[string]any{"index": int64(3), "steps": []any{step("web", "api")}, "watch": []any{"web"}},
			},
		},
	}}
	res := dc.Resource(controller.WavePlanGVR).Namespace(ns)
	_ = res.Delete(ctx, "e2e-rollback", metav1.DeleteOptions{})
	if _, err := res.Create(ctx, wp, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	c := &controller.Controller{
		Client:    dc,
		Namespace: ns,
		Tick:      time.Second,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Executor: func(namespace string, spec *waveplan.Spec) (*execute.Executor, error) {
			g, err := gate.NewPrometheus(prom.New(spec.Gate.PrometheusURL), spec.Gate)
			if err != nil {
				return nil, err
			}
			return &execute.Executor{Act: &argo.Actuator{Client: dc, Namespace: namespace}, Gate: g, Poll: time.Second}, nil
		},
	}
	var status map[string]any
	waitFor(t, 5*time.Minute, "waveplan terminal", func() bool {
		if err := c.ReconcileAll(ctx); err != nil {
			t.Log(err)
		}
		u, err := res.Get(ctx, "e2e-rollback", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		status, _, _ = unstructured.NestedMap(u.Object, "status")
		phase, _ := status["phase"].(string)
		return phase == waveplan.PhaseRolledBack || phase == waveplan.PhaseHalted || phase == waveplan.PhaseSucceeded
	})
	pretty, _ := json.MarshalIndent(status, "", "  ")
	t.Logf("final status:\n%s", pretty)

	if status["phase"] != waveplan.PhaseRolledBack {
		t.Fatalf("phase = %v", status["phase"])
	}
	if obs := observe(t, dc, "api"); obs.State != execute.StateAborted {
		t.Errorf("api state = %s, want Aborted", obs.State)
	}
	if v := servingVersion(t, dc, "api"); v != "v1" {
		t.Errorf("api serves %s, want v1", v)
	}
	waitState(t, dc, "db", execute.StateHealthy)
	if v := servingVersion(t, dc, "db"); v != "v1" {
		t.Errorf("db serves %s after undo, want v1", v)
	}
	if obs := observe(t, dc, "web"); obs.State != execute.StateHeld {
		t.Errorf("web state = %s, want still Held (never started)", obs.State)
	}
}
