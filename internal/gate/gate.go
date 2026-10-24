// Package gate implements the Prometheus SLO burn-rate gate.
package gate

import (
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/ChadiDridi/waveform/internal/prom"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// Prometheus breaches when a watched service's burn rate exceeds
// MaxBurnRate, or when it has no data and OnNoData is "fail".
type Prometheus struct {
	Client *prom.Client
	Spec   waveplan.GateSpec
	tmpl   *template.Template
}

// NewPrometheus validates the query template. spec must have defaults applied.
func NewPrometheus(c *prom.Client, spec waveplan.GateSpec) (*Prometheus, error) {
	t, err := template.New("query").Option("missingkey=error").Parse(spec.Query)
	if err != nil {
		return nil, fmt.Errorf("gate query template: %w", err)
	}
	return &Prometheus{Client: c, Spec: spec, tmpl: t}, nil
}

// Query renders the query for svc.
func (p *Prometheus) Query(svc string) (string, error) {
	var b strings.Builder
	err := p.tmpl.Execute(&b, map[string]any{
		"Service":   svc,
		"Window":    promDuration(p.Spec.Window.D()),
		"Objective": p.Spec.Objective,
	})
	return b.String(), err
}

// Check reads the burn rate of each service.
func (p *Prometheus) Check(ctx context.Context, services []string) ([]waveplan.Reading, error) {
	out := make([]waveplan.Reading, 0, len(services))
	for _, svc := range services {
		q, err := p.Query(svc)
		if err != nil {
			return nil, err
		}
		samples, err := p.Client.Query(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", svc, err)
		}
		r := waveplan.Reading{Service: svc}
		if len(samples) == 0 {
			r.NoData = true
			r.Breached = p.Spec.OnNoData == "fail"
		} else {
			for _, s := range samples {
				r.BurnRate = max(r.BurnRate, s.Value)
			}
			r.Breached = r.BurnRate > p.Spec.MaxBurnRate
		}
		out = append(out, r)
	}
	return out, nil
}

// promDuration formats d as a PromQL range ("5m", "90s").
func promDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "1s"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
