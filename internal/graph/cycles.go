package graph

import "sort"

// Cycle is a strongly connected component of more than one service, and the
// edge removed to break it.
type Cycle struct {
	Services []string `json:"services"`
	Broken   Edge     `json:"broken"`
}

// SCCs returns the strongly connected components with more than one service,
// each sorted, in deterministic order (Tarjan's algorithm).
func (g *Graph) SCCs() [][]string {
	adj := map[string][]string{}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	next := 0

	var visit func(v string)
	visit = func(v string) {
		index[v] = next
		low[v] = next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, ok := index[w]; !ok {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) > 1 {
			sort.Strings(comp)
			out = append(out, comp)
		}
	}
	for _, s := range g.Services {
		if _, ok := index[s]; !ok {
			visit(s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// BreakCycles returns an acyclic copy of g. Inside each cycle it repeatedly
// removes the lowest-traffic edge (ties broken by name) until no cycle
// remains. Every removed edge is reported.
func (g *Graph) BreakCycles() (*Graph, []Cycle) {
	dag := g.Clone()
	var cycles []Cycle
	for {
		comps := dag.SCCs()
		if len(comps) == 0 {
			return dag, cycles
		}
		for _, comp := range comps {
			in := map[string]bool{}
			for _, s := range comp {
				in[s] = true
			}
			victim := -1
			for i, e := range dag.Edges {
				if !in[e.From] || !in[e.To] {
					continue
				}
				if victim < 0 || weaker(e, dag.Edges[victim]) {
					victim = i
				}
			}
			cycles = append(cycles, Cycle{Services: comp, Broken: dag.Edges[victim]})
			dag.Edges = append(dag.Edges[:victim], dag.Edges[victim+1:]...)
		}
	}
}

func weaker(a, b Edge) bool {
	if a.RPS != b.RPS {
		return a.RPS < b.RPS
	}
	if a.Calls != b.Calls {
		return a.Calls < b.Calls
	}
	if a.From != b.From {
		return a.From < b.From
	}
	return a.To < b.To
}
