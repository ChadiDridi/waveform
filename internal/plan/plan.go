// Package plan orders a change set into deploy waves.
//
// For every pair of changed services where consumer C (transitively) calls
// provider P, the provider's change kind decides the order:
//
//	P additive -> P before C (expand: ship the new API before its callers)
//	P removal  -> C before P (contract: stop calling before removing)
//
// Waves are Kahn layers of those constraints: everything in a wave can roll
// out in parallel once earlier waves are done.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/graph"
)

// Constraint says Before must finish rolling out before After starts.
type Constraint struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Reason string `json:"reason"`
}

// Step is one service deploy within a wave.
type Step struct {
	Service string         `json:"service"`
	Kind    changeset.Kind `json:"kind"`
	After   []string       `json:"after,omitempty"`
	// Irreversible steps need approval and are never rolled back automatically.
	Irreversible bool `json:"irreversible,omitempty"`
}

// Wave is a set of steps that can run in parallel.
type Wave struct {
	Index int    `json:"index"`
	Steps []Step `json:"steps"`
	// Watch lists services whose SLO gates this wave (filled by analyze).
	Watch []string `json:"watch,omitempty"`
}

// Plan is the ordered rollout.
type Plan struct {
	Waves       []Wave       `json:"waves"`
	Constraints []Constraint `json:"constraints"`
}

// Reasons attached to constraints.
const (
	ReasonExpand   = "provider first: additive change"
	ReasonContract = "consumer first: provider removal"
)

// Build computes waves for changes over dag, which must be acyclic.
func Build(dag *graph.Graph, changes []changeset.Change) (Plan, error) {
	kind := map[string]changeset.Kind{}
	for _, c := range changes {
		kind[c.Service] = c.Kind
	}
	adj := map[string][]string{}
	for _, e := range dag.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	var p Plan
	before := map[string][]string{} // after -> befores
	succ := map[string][]string{}   // before -> afters
	seen := map[[2]string]bool{}
	add := func(b, a, reason string) {
		k := [2]string{b, a}
		if seen[k] {
			return
		}
		seen[k] = true
		p.Constraints = append(p.Constraints, Constraint{Before: b, After: a, Reason: reason})
		before[a] = append(before[a], b)
		succ[b] = append(succ[b], a)
	}
	for _, c := range changes {
		for _, prov := range reachable(adj, c.Service) {
			pk, ok := kind[prov]
			if !ok {
				continue
			}
			if pk == changeset.Removal {
				add(c.Service, prov, ReasonContract)
			} else {
				add(prov, c.Service, ReasonExpand)
			}
		}
	}
	sort.Slice(p.Constraints, func(i, j int) bool {
		a, b := p.Constraints[i], p.Constraints[j]
		if a.Before != b.Before {
			return a.Before < b.Before
		}
		return a.After < b.After
	})

	indeg := map[string]int{}
	for _, c := range changes {
		indeg[c.Service] = len(before[c.Service])
	}
	var layer []string
	for _, c := range changes {
		if indeg[c.Service] == 0 {
			layer = append(layer, c.Service)
		}
	}
	placed := 0
	for len(layer) > 0 {
		sort.Strings(layer)
		w := Wave{Index: len(p.Waves) + 1}
		var nextLayer []string
		for _, s := range layer {
			after := append([]string(nil), before[s]...)
			sort.Strings(after)
			w.Steps = append(w.Steps, Step{Service: s, Kind: kind[s], After: after})
			placed++
			for _, t := range succ[s] {
				indeg[t]--
				if indeg[t] == 0 {
					nextLayer = append(nextLayer, t)
				}
			}
		}
		p.Waves = append(p.Waves, w)
		layer = nextLayer
	}
	if placed != len(changes) {
		var stuck []string
		for _, c := range changes {
			if indeg[c.Service] > 0 {
				stuck = append(stuck, c.Service)
			}
		}
		sort.Strings(stuck)
		return p, fmt.Errorf("no valid order: conflicting expand/contract constraints between %s; split the change set", strings.Join(stuck, ", "))
	}
	return p, nil
}

// reachable returns services reachable from s (excluding s), sorted.
func reachable(adj map[string][]string, s string) []string {
	seen := map[string]bool{s: true}
	stack := []string{s}
	var out []string
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, w := range adj[v] {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
				stack = append(stack, w)
			}
		}
	}
	sort.Strings(out)
	return out
}
