package argo

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

func rollout(phase string, mutate func(map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]any{
			"name": "api", "namespace": "shop", "uid": "ro-uid", "generation": int64(3),
			"annotations": map[string]any{revisionAnnotation: "4"},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"strategy": map[string]any{"canary": map[string]any{"steps": []any{
				map[string]any{"pause": map[string]any{}},
				map[string]any{"setWeight": int64(50)},
				map[string]any{"pause": map[string]any{"duration": "10s"}},
			}}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "api"}}},
		},
		"status": map[string]any{"phase": phase, "observedGeneration": "3", "currentStepIndex": int64(0)},
	}
	if mutate != nil {
		mutate(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func held(obj map[string]any) {
	obj["status"].(map[string]any)["pauseConditions"] = []any{map[string]any{"reason": "CanaryPauseStep"}}
}

func TestObserve(t *testing.T) {
	cases := []struct {
		name string
		ro   *unstructured.Unstructured
		want execute.State
	}{
		{"healthy", rollout("Healthy", nil), execute.StateHealthy},
		{"degraded", rollout("Degraded", nil), execute.StateDegraded},
		{"held at pause {}", rollout("Paused", held), execute.StateHeld},
		{"timed pause is progressing", rollout("Paused", func(o map[string]any) {
			held(o)
			o["status"].(map[string]any)["currentStepIndex"] = int64(2)
		}), execute.StateProgressing},
		{"stale generation", rollout("Healthy", func(o map[string]any) {
			o["status"].(map[string]any)["observedGeneration"] = "2"
		}), execute.StateProgressing},
		{"abort in progress", rollout("Degraded", func(o map[string]any) {
			o["status"].(map[string]any)["abort"] = true
			o["status"].(map[string]any)["availableReplicas"] = int64(1)
		}), execute.StateProgressing},
		{"abort done", rollout("Degraded", func(o map[string]any) {
			o["status"].(map[string]any)["abort"] = true
			o["status"].(map[string]any)["availableReplicas"] = int64(2)
		}), execute.StateAborted},
	}
	for _, c := range cases {
		obs := Observe(c.ro)
		if obs.State != c.want {
			t.Errorf("%s: state = %s, want %s", c.name, obs.State, c.want)
		}
	}
	if p := Observe(rollout("Paused", held)).Position; p != "4/0" {
		t.Errorf("position = %s", p)
	}
}

func replicaSet(name, rev, owner string, env string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"name": name, "namespace": "shop",
			"annotations":     map[string]any{revisionAnnotation: rev},
			"ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "name": "api", "uid": owner}},
		},
		"spec": map[string]any{"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "api", podHashLabel: name}},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "api", "env": []any{map[string]any{"name": "V", "value": env}}}}},
		}},
	}}
}

func newFake(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		RolloutGVR:    "RolloutList",
		ReplicaSetGVR: "ReplicaSetList",
	}, objs...)
}

func TestUndoRestoresPreviousRevision(t *testing.T) {
	c := newFake(
		rollout("Healthy", nil),
		replicaSet("api-r2", "2", "ro-uid", "v2"),
		replicaSet("api-r3", "3", "ro-uid", "v3"),
		replicaSet("api-r4", "4", "ro-uid", "v4"),
		replicaSet("other-r3", "3", "someone-else", "x"),
	)
	a := &Actuator{Client: c}
	step := waveplan.Step{Service: "api", Rollout: waveplan.RolloutRef{Name: "api", Namespace: "shop"}}
	if err := a.Undo(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	ro, _ := a.get(context.Background(), step)
	containers, _, _ := unstructured.NestedSlice(ro.Object, "spec", "template", "spec", "containers")
	env := containers[0].(map[string]any)["env"].([]any)[0].(map[string]any)["value"]
	if env != "v3" {
		t.Fatalf("restored env = %v, want v3", env)
	}
	labels, _, _ := unstructured.NestedStringMap(ro.Object, "spec", "template", "metadata", "labels")
	if _, ok := labels[podHashLabel]; ok {
		t.Fatal("pod template hash label not stripped")
	}
}

func TestUndoWithoutHistoryFails(t *testing.T) {
	a := &Actuator{Client: newFake(rollout("Healthy", nil), replicaSet("api-r4", "4", "ro-uid", "v4"))}
	err := a.Undo(context.Background(), waveplan.Step{Rollout: waveplan.RolloutRef{Name: "api", Namespace: "shop"}})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPromoteAndAbortPatches(t *testing.T) {
	c := newFake(rollout("Paused", func(o map[string]any) {
		held(o)
		o["spec"].(map[string]any)["paused"] = true
	}))
	a := &Actuator{Client: c, Namespace: "shop"}
	step := waveplan.Step{Rollout: waveplan.RolloutRef{Name: "api"}}
	if err := a.Promote(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if err := a.Abort(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, act := range c.Actions() {
		if act.GetVerb() == "patch" {
			got = append(got, act.GetSubresource())
		}
	}
	// spec.paused cleared on the main resource, pause conditions and abort on status.
	if len(got) != 3 || got[0] != "" || got[1] != "status" || got[2] != "status" {
		t.Fatalf("patch subresources = %q", got)
	}
	ro, _ := a.get(context.Background(), step)
	if paused, _, _ := unstructured.NestedBool(ro.Object, "spec", "paused"); paused {
		t.Fatal("spec.paused still true")
	}
	if conds, found, _ := unstructured.NestedSlice(ro.Object, "status", "pauseConditions"); found && len(conds) > 0 {
		t.Fatal("pause conditions not cleared")
	}
}
