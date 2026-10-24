// Package controller reconciles WavePlan custom resources.
//
// It lists WavePlans on a fixed tick and advances each due, non-terminal plan
// by one executor step, writing status through the status subresource with a
// resourceVersion precondition. Run a single replica; there is no leader
// election yet.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/ChadiDridi/waveform/internal/execute"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// WavePlanGVR is the WavePlan resource.
var WavePlanGVR = schema.GroupVersionResource{Group: waveplan.Group, Version: waveplan.Version, Resource: waveplan.Resource}

// Controller reconciles WavePlans.
type Controller struct {
	Client dynamic.Interface
	// Namespace limits the watch; empty means all namespaces.
	Namespace string
	// Executor builds the executor for one plan (gate settings are per plan).
	// namespace is the WavePlan's, the default for its rollouts.
	Executor func(namespace string, spec *waveplan.Spec) (*execute.Executor, error)
	Tick     time.Duration
	Log      *slog.Logger

	due map[string]time.Time
}

// Run reconciles until ctx is done.
func (c *Controller) Run(ctx context.Context) error {
	if c.Tick <= 0 {
		c.Tick = 5 * time.Second
	}
	t := time.NewTicker(c.Tick)
	defer t.Stop()
	for {
		if err := c.ReconcileAll(ctx); err != nil {
			c.Log.Error("list waveplans", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// ReconcileAll advances every due plan once.
func (c *Controller) ReconcileAll(ctx context.Context) error {
	if c.due == nil {
		c.due = map[string]time.Time{}
	}
	list, err := c.Client.Resource(WavePlanGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	now := time.Now()
	seen := map[string]bool{}
	for i := range list.Items {
		u := &list.Items[i]
		// Keyed by UID: a plan re-created under the same name starts fresh.
		key := string(u.GetUID())
		seen[key] = true
		// Half a tick of slack, so a requeue equal to the tick runs next tick
		// instead of the one after.
		if now.Add(c.Tick / 2).Before(c.due[key]) {
			continue
		}
		requeue, err := c.Reconcile(ctx, u)
		if err != nil {
			c.Log.Error("reconcile", "waveplan", u.GetNamespace()+"/"+u.GetName(), "err", err)
			requeue = c.Tick
		}
		if requeue == 0 {
			requeue = time.Hour // terminal; re-checked rarely
		}
		c.due[key] = now.Add(requeue)
	}
	for k := range c.due {
		if !seen[k] {
			delete(c.due, k)
		}
	}
	return nil
}

// Reconcile advances one plan and persists its status if it changed.
func (c *Controller) Reconcile(ctx context.Context, u *unstructured.Unstructured) (time.Duration, error) {
	var wp waveplan.WavePlan
	b, err := json.Marshal(u.Object)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(b, &wp); err != nil {
		return 0, c.halt(ctx, u, wp.Status, fmt.Sprintf("invalid WavePlan: %v", err))
	}
	if wp.Status.Terminal() {
		return 0, nil
	}
	spec := wp.Spec
	spec.Defaults()
	if err := spec.Validate(); err != nil {
		return 0, c.halt(ctx, u, wp.Status, "invalid spec: "+err.Error())
	}
	ex, err := c.Executor(u.GetNamespace(), &spec)
	if err != nil {
		return 0, c.halt(ctx, u, wp.Status, err.Error())
	}

	before := wp.Status
	before.Steps = append([]waveplan.StepStatus(nil), wp.Status.Steps...)
	requeue, err := ex.Advance(ctx, &spec, &wp.Status)
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(before, wp.Status) {
		if before.Phase != wp.Status.Phase || before.Wave != wp.Status.Wave {
			c.Log.Info("waveplan", "name", u.GetNamespace()+"/"+u.GetName(), "phase", wp.Status.Phase, "wave", wp.Status.Wave, "message", wp.Status.Message)
		}
		if err := c.writeStatus(ctx, u, wp.Status); err != nil {
			return 0, err
		}
	}
	return requeue, nil
}

func (c *Controller) halt(ctx context.Context, u *unstructured.Unstructured, st waveplan.Status, msg string) error {
	st.Phase, st.Message = waveplan.PhaseHalted, msg
	return c.writeStatus(ctx, u, st)
}

// writeStatus replaces .status, failing if the object changed since read.
func (c *Controller) writeStatus(ctx context.Context, u *unstructured.Unstructured, st waveplan.Status) error {
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": u.GetResourceVersion()},
		{"op": "add", "path": "/status", "value": st},
	})
	if err != nil {
		return err
	}
	_, err = c.Client.Resource(WavePlanGVR).Namespace(u.GetNamespace()).Patch(ctx, u.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{}, "status")
	return err
}
