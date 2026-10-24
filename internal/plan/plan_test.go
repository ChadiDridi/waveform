package plan

import (
	"testing"

	"github.com/ChadiDridi/waveform/internal/changeset"
	"github.com/ChadiDridi/waveform/internal/graph"
)

func waves(p Plan) [][]string {
	var out [][]string
	for _, w := range p.Waves {
		var names []string
		for _, s := range w.Steps {
			names = append(names, s.Service)
		}
		out = append(out, names)
	}
	return out
}

func TestTransitiveThroughUnchanged(t *testing.T) {
	// a -> x -> b with x unchanged: b still goes before a.
	g := &graph.Graph{Edges: []graph.Edge{{From: "a", To: "x"}, {From: "x", To: "b"}}}
	g.Normalize()
	p, err := Build(g, []changeset.Change{{Service: "a", Kind: changeset.Additive}, {Service: "b", Kind: changeset.Additive}})
	if err != nil {
		t.Fatal(err)
	}
	if got := waves(p); len(got) != 2 || got[0][0] != "b" || got[1][0] != "a" {
		t.Fatalf("waves = %v", got)
	}
}

func TestRemovalConsumersFirst(t *testing.T) {
	g := &graph.Graph{Edges: []graph.Edge{{From: "web", To: "api"}, {From: "cli", To: "api"}}}
	g.Normalize()
	p, err := Build(g, []changeset.Change{
		{Service: "api", Kind: changeset.Removal},
		{Service: "cli", Kind: changeset.Additive},
		{Service: "web", Kind: changeset.Additive},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := waves(p)
	if len(got) != 2 || len(got[0]) != 2 || got[1][0] != "api" {
		t.Fatalf("waves = %v", got)
	}
	if after := p.Waves[1].Steps[0].After; len(after) != 2 {
		t.Fatalf("api after = %v", after)
	}
}

func TestIndependentChangesShareWave(t *testing.T) {
	g := &graph.Graph{Services: []string{"a", "b"}}
	g.Normalize()
	p, err := Build(g, []changeset.Change{{Service: "a"}, {Service: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := waves(p); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("waves = %v", got)
	}
}
