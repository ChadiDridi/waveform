// Package blast computes the traffic-weighted blast radius of a change set.
//
// Exposure of a service S to a changed service C is the share of S's outbound
// traffic that (transitively) reaches C:
//
//	exposure(C) = 1                     for every changed C
//	exposure(S) = sum over edges S->D of rps(S->D)/rps_out(S) * exposure(D)
//
// where every changed service has exposure 1, so traffic is counted once even
// when one changed service sits behind another. The overall score is the
// throughput-weighted mean exposure across all services.
package blast

import (
	"sort"

	"github.com/ChadiDridi/waveform/internal/graph"
)

// Risk levels for the overall score.
const (
	LevelNone   = "none"
	LevelLow    = "low"
	LevelMedium = "medium"
	LevelHigh   = "high"
)

// Impact is one service inside the blast radius.
type Impact struct {
	Service string `json:"service"`
	Changed bool   `json:"changed"`
	// Exposure is the share of this service's traffic that reaches a
	// changed service, 0..1.
	Exposure float64 `json:"exposure"`
	// RPSAtRisk is Exposure times the service's throughput.
	RPSAtRisk float64 `json:"rpsAtRisk"`
	// Confidence is the product of edge confidences along Via.
	Confidence float64 `json:"confidence"`
	// Depth is hops to the nearest changed service.
	Depth int `json:"depth"`
	// Via is the highest-traffic path from this service to a changed one.
	Via []string `json:"via,omitempty"`
}

// Result is the blast radius of a change set.
type Result struct {
	Score    float64  `json:"score"`
	Level    string   `json:"level"`
	Impacts  []Impact `json:"impacts"`
	Unknown  []string `json:"unknown,omitempty"`
	Services int      `json:"servicesInRadius"`
}

// Compute returns the blast radius of changed over dag, which must be acyclic.
func Compute(dag *graph.Graph, changed []string) (Result, error) {
	order, err := dag.TopoOrder()
	if err != nil {
		return Result{}, err
	}
	var res Result
	var known []string
	for _, c := range changed {
		if dag.Has(c) {
			known = append(known, c)
		} else {
			res.Unknown = append(res.Unknown, c)
		}
	}

	out := map[string][]graph.Edge{}
	for _, s := range dag.Services {
		out[s] = dag.Out(s)
	}

	isChanged := map[string]bool{}
	for _, c := range known {
		isChanged[c] = true
	}
	exposure := exposureTo(order, out, isChanged)
	conf := map[string]float64{}
	depth := map[string]int{}
	next := map[string]string{}
	// Providers before consumers, so a service's providers are resolved first.
	for i := len(order) - 1; i >= 0; i-- {
		s := order[i]
		if isChanged[s] {
			conf[s] = 1
			continue
		}
		if exposure[s] <= 0 {
			continue
		}
		best := -1.0
		for _, e := range out[s] {
			if exposure[e.To] <= 0 {
				continue
			}
			w := e.RPS * exposure[e.To]
			if w > best || (w == best && e.To < next[s]) {
				best = w
				next[s] = e.To
				conf[s] = e.Confidence * conf[e.To]
				depth[s] = depth[e.To] + 1
			}
		}
	}

	var total, atRisk float64
	for _, s := range dag.Services {
		tp := dag.Throughput(s)
		total += tp
		if exposure[s] <= 0 {
			continue
		}
		atRisk += exposure[s] * tp
		imp := Impact{
			Service:    s,
			Changed:    isChanged[s],
			Exposure:   graph.Round(exposure[s], 4),
			RPSAtRisk:  graph.Round(exposure[s]*tp, 4),
			Confidence: graph.Round(conf[s], 3),
			Depth:      depth[s],
		}
		if !imp.Changed {
			for v := s; v != ""; v = next[v] {
				imp.Via = append(imp.Via, v)
			}
		}
		res.Impacts = append(res.Impacts, imp)
	}
	sort.Slice(res.Impacts, func(i, j int) bool {
		a, b := res.Impacts[i], res.Impacts[j]
		if a.Changed != b.Changed {
			return a.Changed
		}
		if a.RPSAtRisk != b.RPSAtRisk {
			return a.RPSAtRisk > b.RPSAtRisk
		}
		if a.Exposure != b.Exposure {
			return a.Exposure > b.Exposure
		}
		return a.Service < b.Service
	})
	res.Services = len(res.Impacts)
	if total > 0 {
		res.Score = graph.Round(atRisk/total, 4)
	}
	res.Level = level(res.Score, len(known))
	return res, nil
}

// exposureTo returns each service's exposure to the changed set.
func exposureTo(order []string, out map[string][]graph.Edge, changed map[string]bool) map[string]float64 {
	exp := map[string]float64{}
	for i := len(order) - 1; i >= 0; i-- {
		s := order[i]
		if changed[s] {
			exp[s] = 1
			continue
		}
		if len(out[s]) == 0 {
			continue
		}
		var sum float64
		for _, e := range out[s] {
			sum += e.RPS
		}
		var v float64
		for _, e := range out[s] {
			w := 1 / float64(len(out[s]))
			if sum > 0 {
				w = e.RPS / sum
			}
			v += w * exp[e.To]
		}
		if v > 0 {
			exp[s] = min(v, 1)
		}
	}
	return exp
}

func level(score float64, changed int) string {
	switch {
	case changed == 0:
		return LevelNone
	case score < 0.15:
		return LevelLow
	case score < 0.4:
		return LevelMedium
	default:
		return LevelHigh
	}
}
