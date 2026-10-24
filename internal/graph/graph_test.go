package graph

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeMergesAndDropsSelfLoops(t *testing.T) {
	g := &Graph{Edges: []Edge{
		{From: "b", To: "c", Calls: 1, RPS: 1, Confidence: 0.1, Source: SourcePeer},
		{From: "a", To: "a", Calls: 9},
		{From: "b", To: "c", Calls: 2, RPS: 2, Confidence: 0.5, Source: SourceTrace},
	}}
	g.Normalize()
	want := []Edge{{From: "b", To: "c", Calls: 3, RPS: 3, Confidence: 0.5, Source: SourceTrace}}
	if !reflect.DeepEqual(g.Edges, want) {
		t.Fatalf("edges = %+v", g.Edges)
	}
	if !reflect.DeepEqual(g.Services, []string{"a", "b", "c"}) {
		t.Fatalf("services = %v", g.Services)
	}
}

func TestBreakCycles(t *testing.T) {
	g := &Graph{Edges: []Edge{
		{From: "a", To: "b", RPS: 10},
		{From: "b", To: "a", RPS: 1},
		{From: "c", To: "d", RPS: 5},
		{From: "d", To: "e", RPS: 4},
		{From: "e", To: "c", RPS: 3},
		{From: "e", To: "d", RPS: 2},
	}}
	g.Normalize()
	if got := g.SCCs(); !reflect.DeepEqual(got, [][]string{{"a", "b"}, {"c", "d", "e"}}) {
		t.Fatalf("SCCs = %v", got)
	}
	dag, cycles := g.BreakCycles()
	if _, err := dag.TopoOrder(); err != nil {
		t.Fatal(err)
	}
	var broken []string
	for _, c := range cycles {
		broken = append(broken, c.Broken.From+">"+c.Broken.To)
	}
	// c,d,e needs two cuts: e>d (2 rps) first, then e>c (3 rps).
	if want := []string{"b>a", "e>d", "e>c"}; !reflect.DeepEqual(broken, want) {
		t.Fatalf("broken = %v, want %v", broken, want)
	}
	if len(g.Edges) != 6 {
		t.Fatal("BreakCycles mutated the input graph")
	}
}

func TestOTLPEnumsAndPeerFallback(t *testing.T) {
	// Numeric and string enums, uint64 as number and string, two documents.
	in := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
  "scopeSpans":[{"spans":[
    {"traceId":"t1","spanId":"1","kind":"SPAN_KIND_SERVER","startTimeUnixNano":"1000000000","endTimeUnixNano":"1500000000"},
    {"traceId":"t1","spanId":"2","parentSpanId":"1","kind":3,"startTimeUnixNano":1000000000,"endTimeUnixNano":"1400000000"},
    {"traceId":"t1","spanId":"3","parentSpanId":"1","kind":3,"startTimeUnixNano":"1000000000","endTimeUnixNano":"1100000000",
     "attributes":[{"key":"peer.service","value":{"stringValue":"redis"}}],"status":{"code":2}}
  ]}]}]}
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"db"}}]},
  "scopeSpans":[{"spans":[
    {"traceId":"t1","spanId":"4","parentSpanId":"2","kind":2,"startTimeUnixNano":"1100000000","endTimeUnixNano":"3000000000","status":{"code":"STATUS_CODE_ERROR"}}
  ]}]}]}`
	b := NewBuilder()
	if err := b.Add(strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	g := b.Graph(0)
	if g.WindowSeconds != 2 {
		t.Errorf("window = %v, want 2", g.WindowSeconds)
	}
	want := []Edge{
		{From: "api", To: "db", Calls: 1, Errors: 1, RPS: 0.5, Confidence: 0.091, Source: SourceTrace},
		{From: "api", To: "redis", Calls: 1, Errors: 1, RPS: 0.5, Confidence: 0.045, Source: SourcePeer},
	}
	if !reflect.DeepEqual(g.Edges, want) {
		t.Fatalf("edges =\n%+v\nwant\n%+v", g.Edges, want)
	}
}

func TestDeclaredDiff(t *testing.T) {
	g := &Graph{Edges: []Edge{{From: "a", To: "b"}, {From: "a", To: "c"}, {From: "x", To: "y"}}}
	g.Normalize()
	und, unobs := DeclaredDiff(g, map[string][]string{"a": {"b", "d"}})
	if len(und) != 1 || und[0].To != "c" {
		t.Errorf("undeclared = %+v", und)
	}
	if !reflect.DeepEqual(unobs, []Dep{{From: "a", To: "d"}}) {
		t.Errorf("unobserved = %+v", unobs)
	}
}
