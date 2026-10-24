package collect

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReceiveAndSnapshot(t *testing.T) {
	c := New(0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	traces, err := os.ReadFile("../../testdata/cases/otlp/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	// First line plain, second line gzipped: spans of one trace arrive split.
	lines := bytes.SplitN(traces, []byte("\n"), 2)
	post := func(body []byte, gz bool, ct string) int {
		var buf bytes.Buffer
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", nil)
		if gz {
			zw := gzip.NewWriter(&buf)
			zw.Write(body)
			zw.Close()
			req.Header.Set("Content-Encoding", "gzip")
		} else {
			buf.Write(body)
		}
		req.Body = io.NopCloser(&buf)
		req.Header.Set("Content-Type", ct)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(lines[0], false, "application/json"); code != 200 {
		t.Fatalf("plain post = %d", code)
	}
	if code := post(lines[1], true, "application/json; charset=utf-8"); code != 200 {
		t.Fatalf("gzip post = %d", code)
	}
	if code := post(lines[0], false, "application/x-protobuf"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("protobuf post = %d", code)
	}

	out := filepath.Join(t.TempDir(), "graph.json")
	if err := c.WriteFile(out); err != nil {
		t.Fatal(err)
	}
	g := c.Snapshot()
	if len(g.Edges) != 5 {
		t.Fatalf("edges = %+v", g.Edges)
	}
	// Grace 0 prunes everything older than now: memory stays bounded and
	// edge counts survive.
	time.Sleep(time.Millisecond)
	c.Snapshot()
	if n := c.b.Spans(); n != 0 {
		t.Fatalf("spans held = %d, want 0 after prune", n)
	}
	if len(c.Snapshot().Edges) != 5 {
		t.Fatal("edges lost after prune")
	}
}
