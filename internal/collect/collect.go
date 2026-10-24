// Package collect receives spans over OTLP/HTTP (JSON encoding) and keeps a
// live service graph.
//
// Point an OpenTelemetry Collector at it with the otlphttp exporter and
// `encoding: json`. Protobuf requests get 415.
package collect

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ChadiDridi/waveform/internal/graph"
)

// MaxBody caps one export request (after decompression).
const MaxBody = 64 << 20

// Collector accumulates spans and periodically writes the graph.
type Collector struct {
	// Grace is how long a client span waits for its server span before
	// falling back to peer.service; spans older than 2*Grace are pruned.
	Grace time.Duration
	Log   *slog.Logger

	mu sync.Mutex
	b  *graph.Builder
}

// New returns a Collector.
func New(grace time.Duration, log *slog.Logger) *Collector {
	return &Collector{Grace: grace, Log: log, b: graph.NewBuilder()}
}

// ServeHTTP implements POST /v1/traces.
func (c *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		http.Error(w, "only OTLP/JSON is supported; set encoding: json on the otlphttp exporter", http.StatusUnsupportedMediaType)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "bad gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxBody+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(data) > MaxBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	c.mu.Lock()
	err = c.b.Add(bytes.NewReader(data))
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, "{}")
}

// Snapshot attributes settled spans, prunes old ones and returns the graph.
func (c *Collector) Snapshot() *graph.Graph {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.b.Flush(c.Grace)
	c.b.Prune(time.Now().Add(-2 * c.Grace))
	return c.b.Build(0)
}

// WriteFile writes the current graph to path atomically.
func (c *Collector) WriteFile(path string) error {
	g := c.Snapshot()
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".waveform-graph-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write graph: %w", err)
	}
	c.Log.Info("graph written", "path", path, "services", len(g.Services), "edges", len(g.Edges))
	return nil
}

// Handler serves /v1/traces, /graph (current graph JSON) and /healthz.
func (c *Collector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/traces", c)
	mux.HandleFunc("/graph", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(c.Snapshot())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	return mux
}
