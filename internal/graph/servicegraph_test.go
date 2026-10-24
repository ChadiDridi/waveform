package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChadiDridi/waveform/internal/prom"
)

func TestFromServiceGraph(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		if strings.Contains(q, "failed") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"client":"web","server":"api"},"value":[0,"6"]}]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"client":"web","server":"api"},"value":[0,"600"]},
			{"metric":{"client":"api","server":"db"},"value":[0,"300"]},
			{"metric":{"client":"","server":"db"},"value":[0,"5"]}]}}`)
	}))
	defer srv.Close()

	g, err := FromServiceGraph(context.Background(), prom.New(srv.URL), time.Minute, `env="prod"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Edge{
		{From: "api", To: "db", Calls: 300, RPS: 5, Confidence: 0.968, Source: SourceServiceGraph},
		{From: "web", To: "api", Calls: 600, Errors: 6, RPS: 10, Confidence: 0.984, Source: SourceServiceGraph},
	}
	if fmt.Sprint(g.Edges) != fmt.Sprint(want) {
		t.Fatalf("edges =\n%v\nwant\n%v", g.Edges, want)
	}
	if !strings.Contains(queries[0], `traces_service_graph_request_total{env="prod"}[60s]`) {
		t.Fatalf("query = %s", queries[0])
	}
}
