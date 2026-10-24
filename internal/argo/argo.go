// Package argo drives Argo Rollouts as waveform's actuator.
//
// waveform does not reimplement progressive delivery. Each service's Rollout
// starts its canary steps with an indefinite `pause: {}`; GitOps applies the
// new revision and the Rollout holds there. waveform promotes holds in wave
// order, aborts in-flight rollouts and undoes finished ones on rollback. The
// Rollout's own steps and analysis still run as configured.
//
// Patches mirror what `kubectl argo rollouts promote|abort|undo` send. Only
// the canary strategy with an inline pod template is supported.
package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// Resources used.
var (
	RolloutGVR    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	ReplicaSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
)

const (
	revisionAnnotation = "rollout.argoproj.io/revision"
	podHashLabel       = "rollouts-pod-template-hash"
	pauseReasonCanary  = "CanaryPauseStep"
)

// Actuator implements execute.Actuator against Argo Rollouts.
type Actuator struct {
	Client dynamic.Interface
	// Namespace is used for steps without rollout.namespace.
	Namespace string
}

var _ execute.Actuator = (*Actuator)(nil)

func (a *Actuator) ns(s waveplan.Step) string {
	if s.Rollout.Namespace != "" {
		return s.Rollout.Namespace
	}
	if a.Namespace != "" {
		return a.Namespace
	}
	return "default"
}

func (a *Actuator) get(ctx context.Context, s waveplan.Step) (*unstructured.Unstructured, error) {
	ro, err := a.Client.Resource(RolloutGVR).Namespace(a.ns(s)).Get(ctx, s.Rollout.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("rollout %s/%s: %w", a.ns(s), s.Rollout.Name, err)
	}
	return ro, nil
}

// Observe reads the rollout state.
func (a *Actuator) Observe(ctx context.Context, s waveplan.Step) (execute.Observation, error) {
	ro, err := a.get(ctx, s)
	if err != nil {
		return execute.Observation{}, err
	}
	return Observe(ro), nil
}

// Observe maps a Rollout object to an Observation.
func Observe(ro *unstructured.Unstructured) execute.Observation {
	obj := ro.Object
	rev := ro.GetAnnotations()[revisionAnnotation]
	idx, _, _ := unstructured.NestedInt64(obj, "status", "currentStepIndex")
	phase, _, _ := unstructured.NestedString(obj, "status", "phase")
	msg, _, _ := unstructured.NestedString(obj, "status", "message")
	obs := execute.Observation{Revision: rev, Position: fmt.Sprintf("%s/%d", rev, idx), Message: msg}

	// The controller has not caught up with the latest spec yet.
	observed, _, _ := unstructured.NestedFieldNoCopy(obj, "status", "observedGeneration")
	if fmt.Sprint(observed) != strconv.FormatInt(ro.GetGeneration(), 10) {
		obs.State = execute.StateProgressing
		obs.Message = "waiting for the rollouts controller to observe the latest spec"
		return obs
	}

	if aborted, _, _ := unstructured.NestedBool(obj, "status", "abort"); aborted {
		want, found, _ := unstructured.NestedInt64(obj, "spec", "replicas")
		if !found {
			want = 1
		}
		avail, _, _ := unstructured.NestedInt64(obj, "status", "availableReplicas")
		obs.State = execute.StateProgressing
		if avail >= want {
			obs.State = execute.StateAborted
		}
		return obs
	}

	switch phase {
	case "Healthy":
		obs.State = execute.StateHealthy
	case "Degraded":
		obs.State = execute.StateDegraded
	case "Paused":
		obs.State = execute.StateProgressing
		if heldAtIndefinitePause(obj, idx) {
			obs.State = execute.StateHeld
		}
	default:
		obs.State = execute.StateProgressing
	}
	return obs
}

func heldAtIndefinitePause(obj map[string]any, idx int64) bool {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "pauseConditions")
	canaryPause := false
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["reason"] == pauseReasonCanary {
			canaryPause = true
		}
	}
	if !canaryPause {
		return false
	}
	steps, _, _ := unstructured.NestedSlice(obj, "spec", "strategy", "canary", "steps")
	if idx < 0 || int(idx) >= len(steps) {
		return false
	}
	step, ok := steps[idx].(map[string]any)
	if !ok {
		return false
	}
	pause, ok := step["pause"].(map[string]any)
	if !ok {
		return false
	}
	_, timed := pause["duration"]
	return !timed
}

// Promote releases the current pause, like `kubectl argo rollouts promote`.
func (a *Actuator) Promote(ctx context.Context, s waveplan.Step) error {
	ro, err := a.get(ctx, s)
	if err != nil {
		return err
	}
	res := a.Client.Resource(RolloutGVR).Namespace(a.ns(s))
	if paused, _, _ := unstructured.NestedBool(ro.Object, "spec", "paused"); paused {
		if _, err := res.Patch(ctx, s.Rollout.Name, types.MergePatchType, []byte(`{"spec":{"paused":false}}`), metav1.PatchOptions{}); err != nil {
			return err
		}
	}
	_, err = res.Patch(ctx, s.Rollout.Name, types.MergePatchType, []byte(`{"status":{"pauseConditions":null}}`), metav1.PatchOptions{}, "status")
	return err
}

// Abort aborts the rollout, like `kubectl argo rollouts abort`.
func (a *Actuator) Abort(ctx context.Context, s waveplan.Step) error {
	_, err := a.Client.Resource(RolloutGVR).Namespace(a.ns(s)).Patch(ctx, s.Rollout.Name, types.MergePatchType,
		[]byte(`{"status":{"abort":true}}`), metav1.PatchOptions{}, "status")
	return err
}

// Undo restores the pod template of the previous revision, like
// `kubectl argo rollouts undo`. The restored revision then goes through the
// rollout's steps (unless Argo fast-tracks it), so the executor promotes it.
func (a *Actuator) Undo(ctx context.Context, s waveplan.Step) error {
	ro, err := a.get(ctx, s)
	if err != nil {
		return err
	}
	cur, err := strconv.Atoi(ro.GetAnnotations()[revisionAnnotation])
	if err != nil {
		return fmt.Errorf("rollout %s: no numeric %s annotation", s.Rollout.Name, revisionAnnotation)
	}
	list, err := a.Client.Resource(ReplicaSetGVR).Namespace(a.ns(s)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list replicasets: %w", err)
	}
	var prev *unstructured.Unstructured
	prevRev := 0
	for i := range list.Items {
		rs := &list.Items[i]
		if !ownedBy(rs, ro) {
			continue
		}
		r, err := strconv.Atoi(rs.GetAnnotations()[revisionAnnotation])
		if err != nil || r >= cur || r <= prevRev {
			continue
		}
		prev, prevRev = rs, r
	}
	if prev == nil {
		return fmt.Errorf("rollout %s: no replicaset for a revision before %d", s.Rollout.Name, cur)
	}
	tmpl, found, err := unstructured.NestedMap(prev.Object, "spec", "template")
	if err != nil || !found {
		return fmt.Errorf("replicaset %s: no pod template", prev.GetName())
	}
	unstructured.RemoveNestedField(tmpl, "metadata", "labels", podHashLabel)
	patch, err := json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/template", "value": tmpl}})
	if err != nil {
		return err
	}
	_, err = a.Client.Resource(RolloutGVR).Namespace(a.ns(s)).Patch(ctx, s.Rollout.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

func ownedBy(obj, owner *unstructured.Unstructured) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}
