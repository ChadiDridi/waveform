package graph

import "sort"

// Dep is a declared dependency From consumer To provider.
type Dep struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// DeclaredDiff compares observed edges with declared dependencies.
// Undeclared: observed but not declared. Unobserved: declared but never seen
// in the window. Services with no declarations at all are skipped, so a
// partial config does not flag every edge.
func DeclaredDiff(g *Graph, declared map[string][]string) (undeclared []Edge, unobserved []Dep) {
	if len(declared) == 0 {
		return nil, nil
	}
	decl := map[[2]string]bool{}
	for from, tos := range declared {
		for _, to := range tos {
			decl[[2]string{from, to}] = true
		}
	}
	obs := map[[2]string]bool{}
	for _, e := range g.Edges {
		k := [2]string{e.From, e.To}
		obs[k] = true
		if _, known := declared[e.From]; known && !decl[k] {
			undeclared = append(undeclared, e)
		}
	}
	for k := range decl {
		if !obs[k] {
			unobserved = append(unobserved, Dep{From: k[0], To: k[1]})
		}
	}
	sort.Slice(unobserved, func(i, j int) bool {
		if unobserved[i].From != unobserved[j].From {
			return unobserved[i].From < unobserved[j].From
		}
		return unobserved[i].To < unobserved[j].To
	})
	return undeclared, unobserved
}
