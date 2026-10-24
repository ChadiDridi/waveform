// Package graph models the observed service-to-service call graph.
//
// Edges point from consumer (caller) to provider (callee).
package graph

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Edge sources.
const (
	SourceTrace = "trace"        // parent span in caller, child span in callee
	SourcePeer  = "peer.service" // client span attribute only, callee not instrumented
)

// Edge is an observed call path From consumer To provider.
type Edge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Calls      int64   `json:"calls"`
	Errors     int64   `json:"errors,omitempty"`
	RPS        float64 `json:"rps"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source,omitempty"`
}

// Graph is a set of services and the edges observed between them over a window.
type Graph struct {
	WindowSeconds float64  `json:"windowSeconds"`
	Services      []string `json:"services"`
	Edges         []Edge   `json:"edges"`
}

// Load reads a graph JSON file.
func Load(path string) (*Graph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(f)
}

// Decode reads graph JSON and normalizes it.
func Decode(r io.Reader) (*Graph, error) {
	var g Graph
	if err := json.NewDecoder(r).Decode(&g); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	for _, e := range g.Edges {
		if e.From == "" || e.To == "" {
			return nil, fmt.Errorf("edge with empty endpoint: %+v", e)
		}
	}
	g.Normalize()
	return &g, nil
}

// Normalize drops self-loops, merges duplicate edges, adds edge endpoints to
// Services, and sorts everything for deterministic output.
func (g *Graph) Normalize() {
	seen := map[string]bool{}
	for _, s := range g.Services {
		seen[s] = true
	}
	merged := map[[2]string]*Edge{}
	var order [][2]string
	for _, e := range g.Edges {
		seen[e.From] = true
		seen[e.To] = true
		if e.From == e.To {
			continue
		}
		k := [2]string{e.From, e.To}
		if m, ok := merged[k]; ok {
			m.Calls += e.Calls
			m.Errors += e.Errors
			m.RPS += e.RPS
			m.Confidence = math.Max(m.Confidence, e.Confidence)
			if e.Source == SourceTrace {
				m.Source = SourceTrace
			}
			continue
		}
		ec := e
		merged[k] = &ec
		order = append(order, k)
	}
	g.Edges = g.Edges[:0]
	for _, k := range order {
		g.Edges = append(g.Edges, *merged[k])
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	g.Services = g.Services[:0]
	for s := range seen {
		g.Services = append(g.Services, s)
	}
	sort.Strings(g.Services)
}

// Has reports whether svc is a known service.
func (g *Graph) Has(svc string) bool {
	i := sort.SearchStrings(g.Services, svc)
	return i < len(g.Services) && g.Services[i] == svc
}

// Out returns edges leaving svc, sorted by provider.
func (g *Graph) Out(svc string) []Edge {
	var out []Edge
	for _, e := range g.Edges {
		if e.From == svc {
			out = append(out, e)
		}
	}
	return out
}

// Throughput estimates a service's request rate as the larger of its inbound
// and outbound edge RPS.
func (g *Graph) Throughput(svc string) float64 {
	var in, out float64
	for _, e := range g.Edges {
		if e.To == svc {
			in += e.RPS
		}
		if e.From == svc {
			out += e.RPS
		}
	}
	return math.Max(in, out)
}

// Clone returns a deep copy.
func (g *Graph) Clone() *Graph {
	c := &Graph{WindowSeconds: g.WindowSeconds}
	c.Services = append([]string(nil), g.Services...)
	c.Edges = append([]Edge(nil), g.Edges...)
	return c
}

// TopoOrder returns services ordered so every consumer precedes its providers.
// It fails if the graph has a cycle.
func (g *Graph) TopoOrder() ([]string, error) {
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, s := range g.Services {
		indeg[s] = 0
	}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}
	var queue, order []string
	for _, s := range g.Services {
		if indeg[s] == 0 {
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		order = append(order, s)
		for _, t := range adj[s] {
			indeg[t]--
			if indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	if len(order) != len(g.Services) {
		return nil, fmt.Errorf("graph has a cycle")
	}
	return order, nil
}

// Round rounds x to n decimal places. Used to keep JSON output stable.
func Round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
