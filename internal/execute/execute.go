// Package execute runs a WavePlan one reconcile step at a time.
//
// Advance is idempotent and keeps all state in waveplan.Status, so the same
// code drives the CLI (status in a file) and the controller (status on the
// custom resource), and a crash at any point resumes cleanly.
//
// Forward: each wave promotes its steps' rollouts, waits until they are
// healthy, then bakes while the SLO gate watches the wave's watch set.
// Rollback: on a failed step or a breached gate, the failing wave and every
// earlier deployed step linked to it by ordering constraints roll back in
// reverse wave order. Irreversible steps halt the rollback for a human.
package execute

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// State is the actuator's view of one rollout.
type State string

// Rollout states.
const (
	// StateHeld: paused at an indefinite pause, waiting for a promote.
	StateHeld        State = "Held"
	StateProgressing State = "Progressing"
	// StateHealthy: fully rolled out, nothing in flight.
	StateHealthy  State = "Healthy"
	StateDegraded State = "Degraded"
	// StateAborted: an abort finished and the stable version serves traffic.
	StateAborted State = "Aborted"
)

// Observation is a point-in-time read of a rollout.
type Observation struct {
	State    State
	Revision string
	// Position identifies where the rollout is held (revision and step), so
	// the executor promotes each hold exactly once.
	Position string
	Message  string
}

// Actuator drives the deploy system. waveform ships an Argo Rollouts one.
type Actuator interface {
	Observe(ctx context.Context, s waveplan.Step) (Observation, error)
	// Promote releases a held rollout.
	Promote(ctx context.Context, s waveplan.Step) error
	// Abort stops an in-flight rollout and returns traffic to stable.
	Abort(ctx context.Context, s waveplan.Step) error
	// Undo rolls a completed rollout back to its previous revision.
	Undo(ctx context.Context, s waveplan.Step) error
}

// Gate measures SLO health of services.
type Gate interface {
	Check(ctx context.Context, services []string) ([]waveplan.Reading, error)
}

// Executor advances runs.
type Executor struct {
	Act  Actuator
	Gate Gate // nil: waves only bake
	Now  func() time.Time
	// Poll is how soon to requeue while something is in flight.
	Poll time.Duration
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Executor) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return 5 * time.Second
}

// Advance moves the run forward by at most one observation of each active
// step. It returns how long to wait before calling again (0 when terminal).
// spec must have defaults applied and be valid.
func (e *Executor) Advance(ctx context.Context, spec *waveplan.Spec, st *waveplan.Status) (time.Duration, error) {
	if st.Phase == "" {
		st.Phase = waveplan.PhaseRunning
		st.Wave = 1
		for _, w := range spec.Waves {
			for _, s := range w.Steps {
				st.Step(s.Service)
			}
		}
	}
	switch st.Phase {
	case waveplan.PhaseRunning:
		return e.forward(ctx, spec, st)
	case waveplan.PhaseRollingBack:
		return e.rollback(ctx, spec, st)
	}
	return 0, nil
}

func (e *Executor) forward(ctx context.Context, spec *waveplan.Spec, st *waveplan.Status) (time.Duration, error) {
	now := e.now()
	w := spec.Waves[st.Wave-1]
	if st.WaveStartedAt == nil {
		st.WaveStartedAt = &now
	}
	timedOut := now.Sub(*st.WaveStartedAt) > spec.StepTimeout.D()

	var failed, waiting []string
	allDone, started := true, false
	for _, step := range w.Steps {
		ss := st.Step(step.Service)
		switch ss.Phase {
		case waveplan.StepPending, waveplan.StepAwaitingApproval:
			allDone = false
			if step.Irreversible && !approved(spec, step.Service) {
				ss.Phase = waveplan.StepAwaitingApproval
				ss.Message = "irreversible step: add it to spec.approvals to start"
				waiting = append(waiting, step.Service)
				continue
			}
			obs, err := e.Act.Observe(ctx, step)
			switch {
			case err != nil:
				ss.Phase, ss.Message = waveplan.StepPending, err.Error()
			case obs.State == StateHeld:
				if err := e.Act.Promote(ctx, step); err != nil {
					ss.Phase, ss.Message = waveplan.StepPending, "promote: "+err.Error()
					break
				}
				ss.Phase = waveplan.StepProgressing
				ss.Message = "promoted"
				ss.StartedAt = &now
				ss.PromotedAt = obs.Position
				ss.Revision = obs.Revision
				started = true
				continue
			default:
				ss.Phase = waveplan.StepPending
				ss.Message = fmt.Sprintf("waiting for rollout to hold at a pause step (state %s); is the new revision applied?", obs.State)
			}
			if timedOut {
				ss.Phase = waveplan.StepFailed
				ss.Message = "timed out before start: " + ss.Message
				failed = append(failed, step.Service)
			}
		case waveplan.StepProgressing:
			started = true
			allDone = false
			obs, err := e.Act.Observe(ctx, step)
			switch {
			case err != nil:
				ss.Message = err.Error()
			case obs.State == StateHealthy:
				ss.Phase, ss.Message = waveplan.StepSucceeded, "healthy"
				ss.Revision = obs.Revision
				continue
			case obs.State == StateHeld:
				// A later indefinite pause in the rollout's own steps.
				if obs.Position == ss.PromotedAt {
					break
				}
				if err := e.Act.Promote(ctx, step); err != nil {
					ss.Message = "promote: " + err.Error()
					break
				}
				ss.PromotedAt = obs.Position
				ss.Message = "promoted " + obs.Position
			case obs.State == StateDegraded || obs.State == StateAborted:
				ss.Phase = waveplan.StepFailed
				ss.Message = fmt.Sprintf("rollout %s: %s", strings.ToLower(string(obs.State)), obs.Message)
				failed = append(failed, step.Service)
				continue
			default:
				ss.Message = obs.Message
			}
			if timedOut {
				ss.Phase = waveplan.StepFailed
				ss.Message = "timed out progressing: " + ss.Message
				failed = append(failed, step.Service)
			}
		case waveplan.StepSucceeded:
			started = true
		}
	}

	if len(failed) > 0 {
		e.startRollback(spec, st, fmt.Sprintf("wave %d failed: %s", st.Wave, strings.Join(failed, ", ")))
		return e.poll(), nil
	}

	gateOK := e.Gate == nil
	if e.Gate != nil && started && gateDue(st, spec, now) {
		st.LastGateCheck = &now
		readings, err := e.Gate.Check(ctx, w.Watch)
		if err != nil {
			st.Message = "gate check failed, retrying: " + err.Error()
		} else {
			st.Gate = readings
			var breached []string
			for _, r := range readings {
				if r.Breached {
					breached = append(breached, fmt.Sprintf("%s (burn %.2f > %.2f)", r.Service, r.BurnRate, spec.Gate.MaxBurnRate))
				}
			}
			if len(breached) > 0 {
				e.startRollback(spec, st, fmt.Sprintf("wave %d gate breached: %s", st.Wave, strings.Join(breached, ", ")))
				return e.poll(), nil
			}
			st.LastGateOK = &now
		}
	}

	if !allDone {
		switch {
		case len(waiting) > 0:
			st.Message = fmt.Sprintf("wave %d waiting for approval: %s", st.Wave, strings.Join(waiting, ", "))
		default:
			st.Message = fmt.Sprintf("wave %d rolling out", st.Wave)
		}
		return e.poll(), nil
	}

	// All steps healthy: bake, then require a passing gate check after the bake.
	if st.BakeStartedAt == nil {
		st.BakeStartedAt = &now
	}
	bakeEnd := st.BakeStartedAt.Add(spec.Gate.BakeD())
	if e.Gate != nil {
		gateOK = st.LastGateOK != nil && !st.LastGateOK.Before(bakeEnd)
	}
	if now.Before(bakeEnd) || !gateOK {
		st.Message = fmt.Sprintf("wave %d baking until %s", st.Wave, bakeEnd.Format(time.RFC3339))
		return e.poll(), nil
	}

	if st.Wave == len(spec.Waves) {
		st.Phase = waveplan.PhaseSucceeded
		st.Message = fmt.Sprintf("all %d waves rolled out", len(spec.Waves))
		return 0, nil
	}
	st.Wave++
	st.WaveStartedAt, st.BakeStartedAt, st.LastGateCheck, st.LastGateOK, st.Gate = nil, nil, nil, nil, nil
	st.Message = fmt.Sprintf("wave %d starting", st.Wave)
	return e.poll(), nil
}

func gateDue(st *waveplan.Status, spec *waveplan.Spec, now time.Time) bool {
	return st.LastGateCheck == nil || now.Sub(*st.LastGateCheck) >= spec.Gate.Interval.D()
}

func approved(spec *waveplan.Spec, svc string) bool {
	for _, a := range spec.Approvals {
		if a == svc {
			return true
		}
	}
	return false
}

// startRollback plans the rollback: started steps of the failing wave, plus
// earlier succeeded steps reachable from them over ordering constraints
// (either direction), grouped by wave, latest wave first.
func (e *Executor) startRollback(spec *waveplan.Spec, st *waveplan.Status, reason string) {
	waveOf := map[string]int{}
	links := map[string][]string{}
	for i, w := range spec.Waves {
		for _, s := range w.Steps {
			waveOf[s.Service] = i + 1
			for _, b := range s.After {
				links[s.Service] = append(links[s.Service], b)
				links[b] = append(links[b], s.Service)
			}
		}
	}
	// Only steps waveform actually promoted are rolled back: a step that
	// failed before its start never had its rollout touched.
	deployed := func(svc string) bool {
		ss := st.Step(svc)
		switch ss.Phase {
		case waveplan.StepProgressing, waveplan.StepSucceeded, waveplan.StepFailed:
			return ss.StartedAt != nil
		}
		return false
	}

	in := map[string]bool{}
	var queue []string
	for _, s := range spec.Waves[st.Wave-1].Steps {
		if deployed(s.Service) {
			in[s.Service] = true
			queue = append(queue, s.Service)
		}
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, u := range links[v] {
			if !in[u] && deployed(u) && waveOf[u] < st.Wave {
				in[u] = true
				queue = append(queue, u)
			}
		}
	}

	var groups [][]string
	for k := st.Wave; k >= 1; k-- {
		var g []string
		for _, s := range spec.Waves[k-1].Steps {
			if in[s.Service] {
				g = append(g, s.Service)
			}
		}
		if len(g) > 0 {
			sort.Strings(g)
			groups = append(groups, g)
		}
	}

	var kept []string
	for _, w := range spec.Waves {
		for _, s := range w.Steps {
			ss := st.Step(s.Service)
			switch {
			case in[s.Service]:
			case ss.Phase == waveplan.StepPending || ss.Phase == waveplan.StepAwaitingApproval:
				ss.Phase, ss.Message = waveplan.StepSkipped, "not started: run is rolling back"
			case ss.Phase == waveplan.StepSucceeded:
				kept = append(kept, s.Service)
			}
		}
	}

	st.Phase = waveplan.PhaseRollingBack
	st.FailedWave = st.Wave
	st.Rollback = groups
	st.Reason = reason
	if len(kept) > 0 {
		st.Reason += fmt.Sprintf("; keeping unrelated deployed steps: %s", strings.Join(kept, ", "))
	}
	st.Message = st.Reason
}

func (e *Executor) rollback(ctx context.Context, spec *waveplan.Spec, st *waveplan.Status) (time.Duration, error) {
	if len(st.Rollback) == 0 {
		st.Phase = waveplan.PhaseRolledBack
		st.Message = "rolled back: " + st.Reason
		return 0, nil
	}
	now := e.now()
	group := st.Rollback[0]
	done := true
	var manual []string
	for _, svc := range group {
		step := findStep(spec, svc)
		ss := st.Step(svc)
		switch ss.Phase {
		case waveplan.StepProgressing, waveplan.StepSucceeded, waveplan.StepFailed:
			if step.Irreversible {
				ss.Phase = waveplan.StepNeedsManual
				ss.Message = "irreversible: roll back by hand"
				manual = append(manual, svc)
				continue
			}
			done = false
			obs, err := e.Act.Observe(ctx, step)
			if err != nil {
				ss.Message = err.Error()
				continue
			}
			// A rollout that finished (Healthy) needs an undo; one still in
			// flight, held or degraded is aborted back to stable.
			if obs.State == StateHealthy {
				err = e.Act.Undo(ctx, step)
				ss.RollbackAction = "undo"
			} else if obs.State == StateAborted {
				ss.Phase, ss.Message, ss.RollbackAction = waveplan.StepRolledBack, "already aborted", "abort"
				continue
			} else {
				err = e.Act.Abort(ctx, step)
				ss.RollbackAction = "abort"
			}
			if err != nil {
				ss.Message = ss.RollbackAction + ": " + err.Error()
				continue
			}
			ss.Phase = waveplan.StepRollingBack
			ss.Revision = obs.Revision
			ss.PromotedAt = ""
			ss.StartedAt = &now
			ss.Message = ss.RollbackAction + " requested"
		case waveplan.StepRollingBack:
			obs, err := e.Act.Observe(ctx, step)
			if err != nil {
				done = false
				ss.Message = err.Error()
				continue
			}
			finished := false
			switch {
			case ss.RollbackAction == "abort":
				finished = obs.State == StateAborted || (obs.State == StateHealthy && obs.Revision != ss.Revision)
			case obs.State == StateHealthy:
				finished = obs.Revision != ss.Revision
			case obs.State == StateHeld && obs.Position != ss.PromotedAt:
				// The previous revision is held at the rollout's pause step.
				if err := e.Act.Promote(ctx, step); err != nil {
					ss.Message = "promote: " + err.Error()
				} else {
					ss.PromotedAt = obs.Position
					ss.Message = "promoted rollback revision"
				}
			case obs.State == StateDegraded:
				st.Phase = waveplan.PhaseHalted
				st.Message = fmt.Sprintf("rollback of %s degraded: %s", svc, obs.Message)
				ss.Message = "rollback degraded: " + obs.Message
				return 0, nil
			}
			if finished {
				ss.Phase, ss.Message = waveplan.StepRolledBack, "rolled back via "+ss.RollbackAction
				continue
			}
			done = false
			if ss.StartedAt != nil && now.Sub(*ss.StartedAt) > spec.StepTimeout.D() {
				st.Phase = waveplan.PhaseHalted
				st.Message = fmt.Sprintf("rollback of %s timed out", svc)
				return 0, nil
			}
		case waveplan.StepNeedsManual:
			manual = append(manual, svc)
		}
	}
	if !done {
		st.Message = fmt.Sprintf("rolling back %s", strings.Join(group, ", "))
		return e.poll(), nil
	}
	st.Rollback = st.Rollback[1:]
	if len(manual) > 0 {
		st.Phase = waveplan.PhaseHalted
		var rest []string
		for _, g := range st.Rollback {
			rest = append(rest, g...)
		}
		st.Message = fmt.Sprintf("irreversible step(s) %s need manual rollback", strings.Join(manual, ", "))
		if len(rest) > 0 {
			st.Message += fmt.Sprintf("; after that, roll back in order: %s", strings.Join(rest, ", "))
		}
		return 0, nil
	}
	return e.rollback(ctx, spec, st)
}

func findStep(spec *waveplan.Spec, svc string) waveplan.Step {
	for _, w := range spec.Waves {
		for _, s := range w.Steps {
			if s.Service == svc {
				return s
			}
		}
	}
	return waveplan.Step{Service: svc}
}
