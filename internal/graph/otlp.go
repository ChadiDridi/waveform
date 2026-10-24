package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// ConfidenceK is the call count at which an edge reaches 0.5 confidence.
// Confidence is calls/(calls+K), halved for peer.service-only edges.
const ConfidenceK = 10

// OTLP span kinds (opentelemetry.proto.trace.v1.Span.SpanKind).
const (
	kindClient   = 3
	kindProducer = 4
)

const statusError = 2

// otlpTraces matches the OTLP/JSON TracesData encoding, as written by the
// collector's file exporter (one object per line) or a single JSON document.
type otlpTraces struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpKV `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpSpan struct {
	TraceID      string    `json:"traceId"`
	SpanID       string    `json:"spanId"`
	ParentSpanID string    `json:"parentSpanId"`
	Kind         otlpEnum  `json:"kind"`
	Start        otlpU64   `json:"startTimeUnixNano"`
	End          otlpU64   `json:"endTimeUnixNano"`
	Attributes   []otlpKV  `json:"attributes"`
	Status       otlpState `json:"status"`
}

type otlpState struct {
	Code otlpEnum `json:"code"`
}

type otlpKV struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string `json:"stringValue"`
	} `json:"value"`
}

// otlpEnum accepts both the numeric and the string form of a proto enum.
type otlpEnum int

var enumNames = map[string]int{
	"SPAN_KIND_UNSPECIFIED": 0, "SPAN_KIND_INTERNAL": 1, "SPAN_KIND_SERVER": 2,
	"SPAN_KIND_CLIENT": 3, "SPAN_KIND_PRODUCER": 4, "SPAN_KIND_CONSUMER": 5,
	"STATUS_CODE_UNSET": 0, "STATUS_CODE_OK": 1, "STATUS_CODE_ERROR": 2,
}

func (e *otlpEnum) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*e = otlpEnum(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n, ok := enumNames[s]
	if !ok {
		return fmt.Errorf("unknown enum %q", s)
	}
	*e = otlpEnum(n)
	return nil
}

// otlpU64 accepts a uint64 encoded as a JSON string (proto3 JSON) or number.
type otlpU64 uint64

func (u *otlpU64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	*u = otlpU64(n)
	return err
}

func attr(kvs []otlpKV, key string) string {
	for _, kv := range kvs {
		if kv.Key == key && kv.Value.StringValue != nil {
			return *kv.Value.StringValue
		}
	}
	return ""
}

type spanRef struct {
	service  string
	parent   string // trace/parentSpan key, "" for roots
	kind     otlpEnum
	peer     string
	err      bool
	seen     time.Time
	counted  bool // already attributed to an edge (as child, or via peer.service)
	resolved bool // some child span in another service points at this span
}

type edgeAcc struct {
	calls, errors int64
	traced        bool
}

// Builder accumulates OTLP spans into a Graph. It works in batch mode
// (files, then Graph) and incrementally (Add batches, Flush, Prune), which is
// what the live collector uses. Edge counts are kept separately from spans, so
// spans can be pruned once their pairs had time to arrive.
type Builder struct {
	spans          map[string]*spanRef
	edges          map[[2]string]*edgeAcc
	services       map[string]bool
	minNano, maxNs uint64
	now            func() time.Time
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{
		spans:    map[string]*spanRef{},
		edges:    map[[2]string]*edgeAcc{},
		services: map[string]bool{},
		minNano:  math.MaxUint64,
		now:      time.Now,
	}
}

// AddFile reads one OTLP/JSON file (single document or JSON lines).
func (b *Builder) AddFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := b.Add(f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Add reads a stream of OTLP/JSON TracesData objects.
func (b *Builder) Add(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var td otlpTraces
		err := dec.Decode(&td)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode OTLP JSON: %w", err)
		}
		for _, rs := range td.ResourceSpans {
			svc := attr(rs.Resource.Attributes, "service.name")
			if svc == "" {
				svc = "unknown_service"
			}
			b.services[svc] = true
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					b.addSpan(svc, sp)
				}
			}
		}
	}
}

func (b *Builder) addSpan(svc string, sp otlpSpan) {
	key := sp.TraceID + "/" + sp.SpanID
	if _, dup := b.spans[key]; dup {
		return
	}
	ref := &spanRef{
		service: svc,
		kind:    sp.Kind,
		peer:    attr(sp.Attributes, "peer.service"),
		err:     sp.Status.Code == statusError,
		seen:    b.now(),
	}
	if sp.ParentSpanID != "" {
		ref.parent = sp.TraceID + "/" + sp.ParentSpanID
	}
	b.spans[key] = ref
	if sp.Start > 0 {
		b.minNano = min(b.minNano, uint64(sp.Start))
	}
	b.maxNs = max(b.maxNs, uint64(sp.End), uint64(sp.Start))
}

func (b *Builder) edge(from, to string) *edgeAcc {
	k := [2]string{from, to}
	if b.edges[k] == nil {
		b.edges[k] = &edgeAcc{}
	}
	return b.edges[k]
}

// Flush attributes spans to edges. A cross-service parent/child span pair is
// one call. Client or producer spans whose callee never showed up fall back
// to their peer.service attribute once they are older than grace, so a late
// child still gets matched first. Batch callers use grace 0.
func (b *Builder) Flush(grace time.Duration) {
	for _, child := range b.spans {
		if child.counted || child.parent == "" {
			continue
		}
		parent, ok := b.spans[child.parent]
		if !ok {
			continue
		}
		child.counted = true
		if parent.service == child.service {
			continue
		}
		parent.resolved = true
		a := b.edge(parent.service, child.service)
		a.calls++
		a.traced = true
		if child.err {
			a.errors++
		}
	}
	cutoff := b.now().Add(-grace)
	for _, s := range b.spans {
		if s.resolved || s.peer == "" || s.peer == s.service || s.seen.After(cutoff) {
			continue
		}
		if s.kind != kindClient && s.kind != kindProducer {
			continue
		}
		s.resolved = true
		b.services[s.peer] = true
		a := b.edge(s.service, s.peer)
		a.calls++
		if s.err {
			a.errors++
		}
	}
}

// Prune forgets spans received before t. Their edges stay counted.
func (b *Builder) Prune(t time.Time) {
	for k, s := range b.spans {
		if s.seen.Before(t) {
			delete(b.spans, k)
		}
	}
}

// Spans returns how many spans are held in memory.
func (b *Builder) Spans() int { return len(b.spans) }

// Graph flushes all pending spans (grace 0) and builds the graph.
// windowSeconds overrides the window derived from span timestamps when > 0.
func (b *Builder) Graph(windowSeconds float64) *Graph {
	b.Flush(0)
	return b.Build(windowSeconds)
}

// Build returns the graph from edges attributed so far, without flushing.
func (b *Builder) Build(windowSeconds float64) *Graph {
	window := windowSeconds
	if window <= 0 && b.maxNs > b.minNano {
		window = float64(b.maxNs-b.minNano) / 1e9
	}
	if window <= 0 {
		window = 1
	}
	g := &Graph{WindowSeconds: Round(window, 3)}
	for s := range b.services {
		g.Services = append(g.Services, s)
	}
	for k, a := range b.edges {
		conf := float64(a.calls) / float64(a.calls+ConfidenceK)
		src := SourceTrace
		if !a.traced {
			conf /= 2
			src = SourcePeer
		}
		g.Edges = append(g.Edges, Edge{
			From:       k[0],
			To:         k[1],
			Calls:      a.calls,
			Errors:     a.errors,
			RPS:        Round(float64(a.calls)/window, 4),
			Confidence: Round(conf, 3),
			Source:     src,
		})
	}
	g.Normalize()
	return g
}
