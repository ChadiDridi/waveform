package analyze

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChadiDridi/waveform/internal/changeset"
)

// Marker identifies waveform's PR comment so it can be updated in place.
const Marker = "<!-- waveform:plan -->"

// JSON renders the report as indented JSON.
func (r Report) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Markdown renders the report for humans and PR comments.
func (r Report) Markdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("%s\n## waveform plan\n\n", Marker)
	if len(r.Changes) == 0 {
		w("No service changes detected.\n")
		r.writeGraphNotes(&b)
		return b.String()
	}
	w("**Blast radius: %s** (score %.2f, %d service(s) in radius, window %gs)\n\n",
		strings.ToUpper(r.Blast.Level), r.Blast.Score, r.Blast.Services, r.Graph.WindowSeconds)

	w("### Waves\n\n")
	if len(r.Plan.Waves) == 0 {
		w("_No valid order. See warnings._\n\n")
	}
	for _, wave := range r.Plan.Waves {
		var parts []string
		for _, s := range wave.Steps {
			p := fmt.Sprintf("`%s`", s.Service)
			if s.Kind == changeset.Removal {
				p += " (removal)"
			}
			if s.Irreversible {
				p += " ⚠ irreversible: needs approval, manual rollback"
			}
			parts = append(parts, p)
		}
		w("%d. %s\n", wave.Index, strings.Join(parts, ", "))
		if len(wave.Watch) > 0 {
			w("   - gate watches: %s\n", strings.Join(wave.Watch, ", "))
		}
	}
	w("\n")
	if len(r.Plan.Constraints) > 0 {
		w("<details><summary>Ordering constraints</summary>\n\n")
		for _, c := range r.Plan.Constraints {
			w("- `%s` before `%s`: %s\n", c.Before, c.After, c.Reason)
		}
		w("\n</details>\n\n")
	}

	w("### Blast radius\n\n")
	w("| Service | Changed | Exposure | RPS at risk | Confidence | Path |\n")
	w("|---|---|---:|---:|---:|---|\n")
	for _, i := range r.Blast.Impacts {
		changed := ""
		if i.Changed {
			changed = "yes"
		}
		w("| `%s` | %s | %.0f%% | %.2f | %.2f | %s |\n",
			i.Service, changed, i.Exposure*100, i.RPSAtRisk, i.Confidence, strings.Join(i.Via, " → "))
	}
	w("\n")
	r.writeGraphNotes(&b)
	return b.String()
}

func (r Report) writeGraphNotes(b *strings.Builder) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	if len(r.Contracts) > 0 {
		w("### API contract violations\n\n")
		for _, v := range r.Contracts {
			when := fmt.Sprintf("during wave %d", v.Wave)
			if v.Wave > len(r.Plan.Waves) {
				when = "after rollout"
			}
			w("- %s %s\n", when, v.Message)
		}
		w("\n")
	}
	if len(r.Undeclared) > 0 {
		w("### Undeclared dependencies\n\nObserved in traces, missing from `dependsOn`:\n\n")
		for _, e := range r.Undeclared {
			w("- `%s` → `%s` (%.2f rps, confidence %.2f)\n", e.From, e.To, e.RPS, e.Confidence)
		}
		w("\n")
	}
	if len(r.Unobserved) > 0 {
		w("### Declared but not observed\n\n")
		for _, d := range r.Unobserved {
			w("- `%s` → `%s`\n", d.From, d.To)
		}
		w("\n")
	}
	if len(r.Warnings) > 0 {
		w("### Warnings\n\n")
		for _, s := range r.Warnings {
			w("- %s\n", s)
		}
		w("\n")
	}
	w("<sub>Plan from observed traffic (%d services, %d edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>\n",
		r.Graph.Services, len(r.Graph.Edges))
}
