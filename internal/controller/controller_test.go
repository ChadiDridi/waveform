package controller

import (
	"context"
	"io"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// oneShot holds once, then is healthy after promotion.
type oneShot struct{ promoted map[string]bool }

func (a *oneShot) Observe(_ context.Context, s waveplan.Step) (execute.Observation, error) {
	if a.promoted[s.Service] {
		return execute.Observation{State: execute.StateHealthy, Revision: "2"}, nil
	}
	return execute.Observation{State: execute.StateHeld, Revision: "2", Position: "2/0"}, nil
}
func (a *oneShot) Promote(_ context.Context, s waveplan.Step) error {
	a.promoted[s.Service] = true
	return nil
}
func (a *oneShot) Abort(context.Context, waveplan.Step) error { return nil }
func (a *oneShot) Undo(context.Context, waveplan.Step) error  { return nil }

func wavePlan(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": waveplan.APIVersion,
		"kind":       waveplan.Kind,
		"metadata":   map[string]any{"name": name, "namespace": "ops", "resourceVersion": "1", "uid": name + "-uid"},
		"spec":       spec,
	}}
}

func TestReconcileToSuccess(t *testing.T) {
	spec := map[string]any{
		"gate": map[string]any{"bake": "0s"},
		"waves": []any{
			map[string]any{"index": int64(1), "steps": []any{map[string]any{"service": "db", "kind": "additive", "rollout": map[string]any{"name": "db"}}}},
			map[string]any{"index": int64(2), "steps": []any{map[string]any{"service": "api", "kind": "additive", "after": []any{"db"}, "rollout": map[string]any{"name": "api"}}}},
		},
	}
	client := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{WavePlanGVR: "WavePlanList"},
		wavePlan("pr-1", spec), wavePlan("broken", map[string]any{"waves": []any{}}))
	act := &oneShot{promoted: map[string]bool{}}
	c := &Controller{
		Client:   client,
		Executor: func(string, *waveplan.Spec) (*execute.Executor, error) { return &execute.Executor{Act: act}, nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		c.due = nil // ignore requeue delays in the test
		if err := c.ReconcileAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	get := func(name string) map[string]any {
		u, err := client.Resource(WavePlanGVR).Namespace("ops").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		st, _, _ := unstructured.NestedMap(u.Object, "status")
		return st
	}
	if st := get("pr-1"); st["phase"] != waveplan.PhaseSucceeded {
		t.Fatalf("pr-1 status = %v", st)
	}
	if st := get("broken"); st["phase"] != waveplan.PhaseHalted {
		t.Fatalf("broken status = %v", st)
	}
	if !act.promoted["db"] || !act.promoted["api"] {
		t.Fatalf("promoted = %v", act.promoted)
	}
}

func TestRecreatedPlanIsNotDelayed(t *testing.T) {
	spec := map[string]any{
		"gate":  map[string]any{"bake": "0s"},
		"waves": []any{map[string]any{"index": int64(1), "steps": []any{map[string]any{"service": "db", "rollout": map[string]any{"name": "db"}}}}},
	}
	client := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{WavePlanGVR: "WavePlanList"}, wavePlan("p", spec))
	act := &oneShot{promoted: map[string]bool{}}
	c := &Controller{
		Client:   client,
		Executor: func(string, *waveplan.Spec) (*execute.Executor, error) { return &execute.Executor{Act: act}, nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	res := client.Resource(WavePlanGVR).Namespace("ops")
	for i := 0; i < 3; i++ {
		c.ReconcileAll(ctx) // finishes; terminal plans requeue in an hour
	}
	res.Delete(ctx, "p", metav1.DeleteOptions{})
	again := wavePlan("p", spec)
	again.SetUID("p-uid-2")
	act.promoted = map[string]bool{}
	if _, err := res.Create(ctx, again, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.ReconcileAll(ctx)
	if !act.promoted["db"] {
		t.Fatal("re-created plan with the same name was not reconciled")
	}
}
