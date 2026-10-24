// Package analyze runs the phase 1 pipeline: graph -> cycles -> blast radius
// -> wave plan, and renders the result.
package analyze

import (
	"fmt"
	"sort"

	"github.com/ChadiDridi/waveform/internal/blast"
	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/contract"
	"github.com/ChadiDridi/waveform/internal/graph"
	"github.com/ChadiDridi/waveform/internal/plan"
)

// Report is the full phase 1 output.
type Report struct {
	Changes    []changeset.Change   `json:"changes"`
	Graph      GraphSummary         `json:"graph"`
	Cycles     []graph.Cycle        `json:"cycles,omitempty"`
	Undeclared []graph.Edge         `json:"undeclared,omitempty"`
	Unobserved []graph.Dep          `json:"unobserved,omitempty"`
	Blast      blast.Result         `json:"blast"`
	Plan       plan.Plan            `json:"plan"`
	Contracts  []contract.Violation `json:"contractViolations,omitempty"`
	Warnings   []string             `json:"warnings,omitempty"`
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
	// Irreversible marks steps that need approval and manual rollback.
	Irreversible map[string]bool
	// MinExposure is the exposure at which a caller joins a wave's watch
	// set. Defaults to 0.05.
	MinExposure float64
	Contracts   map[string]contract.Contract
}

// Run computes the report. It fails when no valid wave order exists or the
// order breaks an API contract.
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
	if err != nil {
		return r, err
	}

	minExp := in.MinExposure
	if minExp <= 0 {
		minExp = 0.05
	}
	var waves [][]string
	for i := range r.Plan.Waves {
		w := &r.Plan.Waves[i]
		var names []string
		for j := range w.Steps {
			names = append(names, w.Steps[j].Service)
			w.Steps[j].Irreversible = in.Irreversible[w.Steps[j].Service]
		}
		waves = append(waves, names)
		res, err := blast.Compute(dag, names)
		if err != nil {
			return r, err
		}
		for _, imp := range res.Impacts {
			if imp.Exposure >= minExp {
				w.Watch = append(w.Watch, imp.Service)
			}
		}
		for _, u := range res.Unknown {
			w.Watch = append(w.Watch, u)
		}
		sort.Strings(w.Watch)
	}

	r.Contracts = contract.Check(in.Contracts, waves)
	if len(r.Contracts) > 0 {
		err = fmt.Errorf("wave order breaks %d API contract(s); adjust change kinds or split the change set", len(r.Contracts))
		r.Warnings = append(r.Warnings, err.Error())
	}
	return r, err
}
