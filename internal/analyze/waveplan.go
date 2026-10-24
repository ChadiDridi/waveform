package analyze

import (
	"fmt"

	"github.com/ChadiDridi/waveform/internal/config"
	"github.com/ChadiDridi/waveform/internal/waveplan"
)

// WavePlan turns the report's plan into an executable WavePlan resource.
// cfg supplies rollout names and gate settings; it may be nil.
func (r Report) WavePlan(name, namespace string, cfg *config.Config) (*waveplan.WavePlan, error) {
	if len(r.Plan.Waves) == 0 {
		return nil, fmt.Errorf("no waves to execute")
	}
	wp := &waveplan.WavePlan{
		APIVersion: waveplan.APIVersion,
		Kind:       waveplan.Kind,
		Metadata:   waveplan.Metadata{Name: name, Namespace: namespace},
	}
	if cfg != nil {
		wp.Spec.Gate = cfg.Gate
	}
	wp.Spec.Defaults()
	for _, w := range r.Plan.Waves {
		ww := waveplan.Wave{Index: w.Index, Watch: w.Watch}
		for _, s := range w.Steps {
			ww.Steps = append(ww.Steps, waveplan.Step{
				Service:      s.Service,
				Kind:         string(s.Kind),
				After:        s.After,
				Irreversible: s.Irreversible,
				Rollout:      cfg.RolloutFor(s.Service),
			})
		}
		wp.Spec.Waves = append(wp.Spec.Waves, ww)
	}
	return wp, wp.Spec.Validate()
}
