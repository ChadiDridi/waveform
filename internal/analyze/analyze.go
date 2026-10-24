// Package analyze runs the phase 1 pipeline: graph -> cycles -> blast radius
// -> wave plan, and renders the result.
package analyze

import (
	"fmt"

	"github.com/ChadiDridi/waveform/internal/blast"
	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/graph"
	"github.com/ChadiDridi/waveform/internal/plan"
)

// Report is the full phase 1 output.
type Report struct {
	Changes    []changeset.Change `json:"changes"`
	Graph      GraphSummary       `json:"graph"`
	Cycles     []graph.Cycle      `json:"cycles,omitempty"`
	Undeclared []graph.Edge       `json:"undeclared,omitempty"`
	Unobserved []graph.Dep        `json:"unobserved,omitempty"`
	Blast      blast.Result       `json:"blast"`
	Plan       plan.Plan          `json:"plan"`
	Warnings   []string           `json:"warnings,omitempty"`
}

// GraphSummary describes the observed graph the plan was computed from.
type GraphSummary struct {
	WindowSeconds float64      `json:"windowSeconds"`
	Services      int          `json:"services"`
	Edges         []graph.Edge `json:"edges"`
}

// Input is everything the pipeline needs.
type Input struct {
	Graph    *graph.Graph
	Changes  []changeset.Change
	Declared map[string][]string
	Unmapped []string // changed files owned by no service
}

// Run computes the report. It fails only when no valid wave order exists.
func Run(in Input) (Report, error) {
	r := Report{
		Changes: in.Changes,
		Graph: GraphSummary{
			WindowSeconds: in.Graph.WindowSeconds,
			Services:      len(in.Graph.Services),
			Edges:         in.Graph.Edges,
		},
	}
	if r.Changes == nil {
		r.Changes = []changeset.Change{}
	}
	dag, cycles := in.Graph.BreakCycles()
	r.Cycles = cycles
	for _, c := range cycles {
		r.Warnings = append(r.Warnings, fmt.Sprintf("cycle %v: ignored lowest-traffic edge %s -> %s for ordering", c.Services, c.Broken.From, c.Broken.To))
	}
	r.Undeclared, r.Unobserved = graph.DeclaredDiff(in.Graph, in.Declared)
	for _, f := range in.Unmapped {
		r.Warnings = append(r.Warnings, "changed file not owned by any service: "+f)
	}

	names := make([]string, len(in.Changes))
	for i, c := range in.Changes {
		names[i] = c.Service
	}
	var err error
	if r.Blast, err = blast.Compute(dag, names); err != nil {
		return r, err
	}
	if r.Blast.Impacts == nil {
		r.Blast.Impacts = []blast.Impact{}
	}
	for _, u := range r.Blast.Unknown {
		r.Warnings = append(r.Warnings, "changed service not in observed graph, ordered without constraints: "+u)
	}
	r.Plan, err = plan.Build(dag, in.Changes)
	if err != nil {
		r.Plan.Waves = nil
		r.Warnings = append(r.Warnings, err.Error())
	}
	if r.Plan.Waves == nil {
		r.Plan.Waves = []plan.Wave{}
	}
	return r, err
}
