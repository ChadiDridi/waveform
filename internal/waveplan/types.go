// Package waveplan defines the WavePlan resource: a wave plan plus the gate
// and actuator settings needed to execute it, and the execution status.
//
// The same document is used by `waveform run` (status kept in a local state
// file) and by `waveform controller` (status kept on the custom resource).
package waveplan

import (
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// API coordinates of the WavePlan custom resource.
const (
	Group      = "waveform.chadidridi.github.io"
	Version    = "v1alpha1"
	Kind       = "WavePlan"
	Resource   = "waveplans"
	APIVersion = Group + "/" + Version
)

// WavePlan is the custom resource.
type WavePlan struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	Status     Status   `json:"status,omitempty"`
}

// Metadata is the subset of ObjectMeta waveform reads and writes.
type Metadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Spec is the desired rollout.
type Spec struct {
	Waves []Wave   `json:"waves"`
	Gate  GateSpec `json:"gate"`
	// StepTimeout bounds how long a step may stay pending or progressing,
	// measured from the start of its wave. Approval waits do not count.
	StepTimeout Duration `json:"stepTimeout,omitempty"`
	// Approvals lists irreversible steps a human has approved.
	Approvals []string `json:"approvals,omitempty"`
}

// Wave is a set of steps that roll out in parallel.
type Wave struct {
	Index int    `json:"index"`
	Steps []Step `json:"steps"`
	// Watch lists services whose SLO burn rate gates this wave: the wave's
	// own services plus callers inside its blast radius.
	Watch []string `json:"watch,omitempty"`
}

// Step deploys one service.
type Step struct {
	Service string   `json:"service"`
	Kind    string   `json:"kind"` // additive or removal
	After   []string `json:"after,omitempty"`
	// Irreversible steps (schema migrations, data backfills) need an approval
	// to start and are never rolled back automatically.
	Irreversible bool       `json:"irreversible,omitempty"`
	Rollout      RolloutRef `json:"rollout"`
}

// RolloutRef points at the Argo Rollout that deploys a service.
type RolloutRef struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace"`
}

// GateSpec configures the SLO burn-rate gate.
type GateSpec struct {
	// PrometheusURL disables gating when empty; waves then only bake.
	PrometheusURL string `json:"prometheusURL,omitempty" yaml:"prometheusURL"`
	// Query is a Go template returning the burn rate of one service. Fields:
	// .Service, .Window, .Objective.
	Query       string   `json:"query,omitempty" yaml:"query"`
	Objective   float64  `json:"objective,omitempty" yaml:"objective"`
	MaxBurnRate float64  `json:"maxBurnRate,omitempty" yaml:"maxBurnRate"`
	Window      Duration `json:"window,omitempty" yaml:"window"`
	// Bake is how long a healthy wave must stay healthy. Explicit 0 skips it.
	Bake     *Duration `json:"bake,omitempty" yaml:"bake"`
	Interval Duration  `json:"interval,omitempty" yaml:"interval"`
	// OnNoData is "pass" (default) or "fail".
	OnNoData string `json:"onNoData,omitempty" yaml:"onNoData"`
	// MinExposure is the exposure at which a caller joins a wave's watch set.
	// Used at planning time.
	MinExposure float64 `json:"minExposure,omitempty" yaml:"minExposure"`
}

// DefaultQuery assumes OTel HTTP server metrics in Prometheus with the
// service name in the job label (the default for Prometheus' OTLP receiver
// and the collector's prometheus exporter when service.namespace is unset).
const DefaultQuery = `(sum(rate(http_server_request_duration_seconds_count{job="{{.Service}}",http_response_status_code=~"5.."}[{{.Window}}])) / sum(rate(http_server_request_duration_seconds_count{job="{{.Service}}"}[{{.Window}}]))) / (1 - {{.Objective}})`

// BakeD returns the bake duration (0 if unset).
func (g *GateSpec) BakeD() time.Duration {
	if g.Bake == nil {
		return 0
	}
	return g.Bake.D()
}

// Defaults fills unset fields.
func (g *GateSpec) Defaults() {
	if g.Query == "" {
		g.Query = DefaultQuery
	}
	if g.Objective == 0 {
		g.Objective = 0.999
	}
	if g.MaxBurnRate == 0 {
		g.MaxBurnRate = 2
	}
	if g.Window == 0 {
		g.Window = Duration(5 * time.Minute)
	}
	if g.Bake == nil {
		d := Duration(5 * time.Minute)
		g.Bake = &d
	}
	if g.Interval == 0 {
		g.Interval = Duration(30 * time.Second)
	}
	if g.OnNoData == "" {
		g.OnNoData = "pass"
	}
	if g.MinExposure == 0 {
		g.MinExposure = 0.05
	}
}

// Defaults fills unset spec fields.
func (s *Spec) Defaults() {
	s.Gate.Defaults()
	if s.StepTimeout == 0 {
		s.StepTimeout = Duration(30 * time.Minute)
	}
}

// Validate checks the spec is executable.
func (s *Spec) Validate() error {
	if len(s.Waves) == 0 {
		return fmt.Errorf("spec.waves is empty")
	}
	seen := map[string]bool{}
	for i, w := range s.Waves {
		if len(w.Steps) == 0 {
			return fmt.Errorf("wave %d has no steps", i+1)
		}
		for _, st := range w.Steps {
			if st.Service == "" || st.Rollout.Name == "" {
				return fmt.Errorf("wave %d: step needs service and rollout.name", i+1)
			}
			if seen[st.Service] {
				return fmt.Errorf("service %s appears in more than one step", st.Service)
			}
			seen[st.Service] = true
		}
	}
	if s.Gate.OnNoData != "pass" && s.Gate.OnNoData != "fail" {
		return fmt.Errorf("gate.onNoData must be pass or fail")
	}
	return nil
}

// Run phases.
const (
	PhaseRunning     = "Running"
	PhaseSucceeded   = "Succeeded"
	PhaseRollingBack = "RollingBack"
	PhaseRolledBack  = "RolledBack"
	// PhaseHalted needs a human: an irreversible step blocks rollback, or a
	// rollback itself failed.
	PhaseHalted = "Halted"
)

// Step phases.
const (
	StepPending          = "Pending"
	StepAwaitingApproval = "AwaitingApproval"
	StepProgressing      = "Progressing"
	StepSucceeded        = "Succeeded"
	StepFailed           = "Failed"
	StepSkipped          = "Skipped"
	StepRollingBack      = "RollingBack"
	StepRolledBack       = "RolledBack"
	StepNeedsManual      = "NeedsManualRollback"
)

// Status is the observed execution state.
type Status struct {
	Phase   string       `json:"phase,omitempty"`
	Wave    int          `json:"wave,omitempty"`
	Message string       `json:"message,omitempty"`
	Steps   []StepStatus `json:"steps,omitempty"`

	WaveStartedAt *time.Time `json:"waveStartedAt,omitempty"`
	BakeStartedAt *time.Time `json:"bakeStartedAt,omitempty"`
	LastGateCheck *time.Time `json:"lastGateCheck,omitempty"`
	LastGateOK    *time.Time `json:"lastGateOK,omitempty"`
	Gate          []Reading  `json:"gate,omitempty"`

	FailedWave int `json:"failedWave,omitempty"`
	// Reason says why the run is rolling back.
	Reason string `json:"reason,omitempty"`
	// Rollback holds the remaining rollback groups, first group next. Each
	// group rolls back in parallel.
	Rollback [][]string `json:"rollback,omitempty"`
}

// StepStatus tracks one step.
type StepStatus struct {
	Service   string     `json:"service"`
	Phase     string     `json:"phase"`
	Message   string     `json:"message,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// PromotedAt is the rollout position (revision/step) last promoted, so a
	// stale read never triggers a second promotion.
	PromotedAt string `json:"promotedAt,omitempty"`
	// Revision is the rollout revision this step deployed.
	Revision       string `json:"revision,omitempty"`
	RollbackAction string `json:"rollbackAction,omitempty"`
}

// Reading is one gate measurement.
type Reading struct {
	Service  string  `json:"service"`
	BurnRate float64 `json:"burnRate"`
	NoData   bool    `json:"noData,omitempty"`
	Breached bool    `json:"breached,omitempty"`
}

// Terminal reports whether the run is finished.
func (s *Status) Terminal() bool {
	switch s.Phase {
	case PhaseSucceeded, PhaseRolledBack, PhaseHalted:
		return true
	}
	return false
}

// Step returns the status entry for svc, creating it if needed.
func (s *Status) Step(svc string) *StepStatus {
	for i := range s.Steps {
		if s.Steps[i].Service == svc {
			return &s.Steps[i]
		}
	}
	s.Steps = append(s.Steps, StepStatus{Service: svc, Phase: StepPending})
	return &s.Steps[len(s.Steps)-1]
}

// Duration is a time.Duration encoded as a Go duration string ("5m").
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON encodes as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON accepts "5m" or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var secs float64
		if err := json.Unmarshal(b, &secs); err != nil {
			return fmt.Errorf("duration: %s", b)
		}
		*d = Duration(secs * float64(time.Second))
		return nil
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

// UnmarshalYAML accepts "5m".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	*d = Duration(v)
	return err
}
