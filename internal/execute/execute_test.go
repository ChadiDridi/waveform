package execute

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// fakeRollout mimics an Argo Rollout with steps [pause: {}, ...]: a new
// revision holds until promoted, then needs `ticks` observations to finish.
type fakeRollout struct {
	rev      int
	state    State
	left     int
	degrade  bool // fail instead of finishing
	promotes int
}

type fakeAct struct {
	ro    map[string]*fakeRollout
	ticks int
	log   []string
}

func newFakeAct(ticks int, svcs ...string) *fakeAct {
	a := &fakeAct{ro: map[string]*fakeRollout{}, ticks: ticks}
	for _, s := range svcs {
		a.ro[s] = &fakeRollout{rev: 2, state: StateHeld}
	}
	return a
}

func (a *fakeAct) Observe(_ context.Context, s waveplan.Step) (Observation, error) {
	r, ok := a.ro[s.Service]
	if !ok {
		return Observation{}, fmt.Errorf("rollout %s not found", s.Rollout.Name)
	}
	if r.state == StateProgressing {
		if r.left--; r.left <= 0 {
			r.state = StateHealthy
			if r.degrade {
				r.state = StateDegraded
			}
		}
	}
	return Observation{State: r.state, Revision: fmt.Sprint(r.rev), Position: fmt.Sprintf("%d/0", r.rev)}, nil
}

func (a *fakeAct) Promote(_ context.Context, s waveplan.Step) error {
	a.log = append(a.log, "promote "+s.Service)
	r := a.ro[s.Service]
	r.promotes++
	r.state, r.left = StateProgressing, a.ticks
	return nil
}

func (a *fakeAct) Abort(_ context.Context, s waveplan.Step) error {
	a.log = append(a.log, "abort "+s.Service)
	a.ro[s.Service].state = StateAborted
	return nil
}

func (a *fakeAct) Undo(_ context.Context, s waveplan.Step) error {
	a.log = append(a.log, "undo "+s.Service)
	r := a.ro[s.Service]
	r.rev++
	r.state = StateHeld
	return nil
}

type fakeGate struct {
	breach map[string]bool
	calls  [][]string
}

func (g *fakeGate) Check(_ context.Context, svcs []string) ([]waveplan.Reading, error) {
	g.calls = append(g.calls, svcs)
	var out []waveplan.Reading
	for _, s := range svcs {
		r := waveplan.Reading{Service: s, BurnRate: 0.5}
		if g.breach[s] {
			r.BurnRate, r.Breached = 9, true
		}
		out = append(out, r)
	}
	return out, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// spec: wave 1 = db, cache; wave 2 = api (after db); wave 3 = web (after api).
func testSpec() *waveplan.Spec {
	step := func(svc string, after ...string) waveplan.Step {
		return waveplan.Step{Service: svc, Kind: "additive", After: after, Rollout: waveplan.RolloutRef{Name: svc}}
	}
	s := &waveplan.Spec{Waves: []waveplan.Wave{
		{Index: 1, Steps: []waveplan.Step{step("cache"), step("db")}, Watch: []string{"api", "db", "web"}},
		{Index: 2, Steps: []waveplan.Step{step("api", "db")}, Watch: []string{"api", "web"}},
		{Index: 3, Steps: []waveplan.Step{step("web", "api")}, Watch: []string{"web"}},
	}}
	bake := waveplan.Duration(time.Minute)
	s.Gate.Bake = &bake
	s.Gate.Interval = waveplan.Duration(10 * time.Second)
	s.Defaults()
	return s
}

// drive calls Advance until terminal, moving the clock by the requeue.
func drive(t *testing.T, e *Executor, spec *waveplan.Spec, st *waveplan.Status, hook func(*waveplan.Status)) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	e.Now = c.now
	for i := 0; i < 1000; i++ {
		if hook != nil {
			hook(st)
		}
		d, err := e.Advance(context.Background(), spec, st)
		if err != nil {
			t.Fatal(err)
		}
		if d == 0 {
			return
		}
		c.t = c.t.Add(d)
	}
	t.Fatalf("did not terminate: %+v", st)
}

func TestHappyPathRespectsWaveOrderAndBake(t *testing.T) {
	spec := testSpec()
	act := newFakeAct(3, "cache", "db", "api", "web")
	gate := &fakeGate{}
	st := &waveplan.Status{}
	drive(t, &Executor{Act: act, Gate: gate}, spec, st, nil)

	if st.Phase != waveplan.PhaseSucceeded {
		t.Fatalf("phase = %s (%s)", st.Phase, st.Message)
	}
	want := "promote cache,promote db,promote api,promote web"
	if got := strings.Join(act.log, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	for _, r := range act.ro {
		if r.promotes != 1 {
			t.Fatalf("rollout promoted %d times, want 1", r.promotes)
		}
	}
	if len(gate.calls) == 0 || strings.Join(gate.calls[0], ",") != "api,db,web" {
		t.Fatalf("gate calls = %v", gate.calls)
	}
}

func TestGateBreachRollsBackLinkedWavesInReverse(t *testing.T) {
	spec := testSpec()
	act := newFakeAct(3, "cache", "db", "api", "web")
	gate := &fakeGate{breach: map[string]bool{}}
	st := &waveplan.Status{}
	drive(t, &Executor{Act: act, Gate: gate}, spec, st, func(st *waveplan.Status) {
		// Downstream web starts burning once api is fully out.
		if st.Step("api").Phase == waveplan.StepSucceeded {
			gate.breach["web"] = true
		}
	})

	if st.Phase != waveplan.PhaseRolledBack {
		t.Fatalf("phase = %s (%s)", st.Phase, st.Message)
	}
	// api (wave 2, finished) undone first, then its linked provider db.
	// cache is unrelated and stays; web never started.
	want := "promote cache,promote db,promote api,undo api,promote api,undo db,promote db"
	if got := strings.Join(act.log, ","); got != want {
		t.Fatalf("calls =\n%s\nwant\n%s", got, want)
	}
	phases := map[string]string{}
	for _, s := range st.Steps {
		phases[s.Service] = s.Phase
	}
	wantPhases := map[string]string{"cache": "Succeeded", "db": "RolledBack", "api": "RolledBack", "web": "Skipped"}
	for k, v := range wantPhases {
		if phases[k] != v {
			t.Errorf("%s = %s, want %s", k, phases[k], v)
		}
	}
	if !strings.Contains(st.Message, "gate breached: web") || !strings.Contains(st.Message, "keeping unrelated deployed steps: cache") {
		t.Errorf("message = %s", st.Message)
	}
}

func TestDegradedRolloutIsAborted(t *testing.T) {
	spec := testSpec()
	act := newFakeAct(2, "cache", "db", "api", "web")
	act.ro["api"].degrade = true
	st := &waveplan.Status{}
	drive(t, &Executor{Act: act}, spec, st, nil)
	if st.Phase != waveplan.PhaseRolledBack {
		t.Fatalf("phase = %s (%s)", st.Phase, st.Message)
	}
	if !strings.Contains(strings.Join(act.log, ","), "abort api,undo db") {
		t.Fatalf("calls = %v", act.log)
	}
}

func TestIrreversibleNeedsApprovalAndHaltsRollback(t *testing.T) {
	spec := testSpec()
	spec.Waves[0].Steps[1].Irreversible = true // db migration
	act := newFakeAct(1, "cache", "db", "api", "web")
	gate := &fakeGate{breach: map[string]bool{}}
	st := &waveplan.Status{}
	ticks := 0
	drive(t, &Executor{Act: act, Gate: gate}, spec, st, func(st *waveplan.Status) {
		ticks++
		if ticks == 5 {
			if st.Step("db").Phase != waveplan.StepAwaitingApproval {
				t.Fatalf("db = %s, want AwaitingApproval", st.Step("db").Phase)
			}
			spec.Approvals = []string{"db"}
		}
		if st.Step("api").Phase == waveplan.StepSucceeded {
			gate.breach["api"] = true
		}
	})
	if st.Phase != waveplan.PhaseHalted {
		t.Fatalf("phase = %s (%s)", st.Phase, st.Message)
	}
	if st.Step("db").Phase != waveplan.StepNeedsManual || st.Step("api").Phase != waveplan.StepRolledBack {
		t.Fatalf("steps = %+v", st.Steps)
	}
	if strings.Contains(strings.Join(act.log, ","), "undo db") {
		t.Fatal("irreversible step was rolled back automatically")
	}
}

func TestStartTimeoutFailsWave(t *testing.T) {
	spec := testSpec()
	act := newFakeAct(1, "cache", "db", "api", "web")
	act.ro["api"].state = StateHealthy // new revision never applied
	st := &waveplan.Status{}
	drive(t, &Executor{Act: act}, spec, st, nil)
	if st.Phase != waveplan.PhaseRolledBack || st.Step("api").Phase != waveplan.StepFailed {
		t.Fatalf("phase = %s, api = %+v", st.Phase, *st.Step("api"))
	}
	if strings.Contains(strings.Join(act.log, ","), "undo api") {
		t.Fatal("rolled back a rollout waveform never promoted")
	}
	if !strings.Contains(st.Step("api").Message, "timed out before start") {
		t.Fatalf("api message = %s", st.Step("api").Message)
	}
}

func TestGateErrorsDoNotCompleteBake(t *testing.T) {
	spec := testSpec()
	spec.Waves = spec.Waves[:1]
	act := newFakeAct(1, "cache", "db")
	st := &waveplan.Status{}
	e := &Executor{Act: act, Gate: errGate{}}
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	e.Now = c.now
	for i := 0; i < 100; i++ {
		d, _ := e.Advance(context.Background(), spec, st)
		c.t = c.t.Add(d)
	}
	if st.Phase != waveplan.PhaseRunning || !strings.Contains(st.Message, "baking") {
		t.Fatalf("phase = %s (%s), want still baking", st.Phase, st.Message)
	}
}

type errGate struct{}

func (errGate) Check(context.Context, []string) ([]waveplan.Reading, error) {
	return nil, fmt.Errorf("prometheus down")
}
