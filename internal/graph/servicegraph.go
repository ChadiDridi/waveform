package graph

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ChadiDridi/waveform/internal/prom"
)

// SourceServiceGraph marks edges read from the OpenTelemetry Collector
// servicegraph connector's metrics.
const SourceServiceGraph = "servicegraph"

// ServiceGraphMetric is the counter the servicegraph connector exports. Its
// failed-request twin has the same name with "_failed" before "_total".
const ServiceGraphMetric = "traces_service_graph_request_total"

// FromServiceGraph builds a graph from servicegraph connector metrics over the
// trailing window, using the connector's "client" and "server" labels.
// selector is an optional extra label matcher, e.g. `env="prod"`.
func FromServiceGraph(ctx context.Context, c *prom.Client, window time.Duration, selector string) (*Graph, error) {
	if window <= 0 {
		return nil, fmt.Errorf("servicegraph window must be > 0")
	}
	rng := fmt.Sprintf("%ds", int(window.Seconds()))
	q := func(metric string) string {
		return fmt.Sprintf(`sum by (client, server) (increase(%s{%s}[%s]))`, metric, selector, rng)
	}
	total, err := c.Query(ctx, q(ServiceGraphMetric))
	if err != nil {
		return nil, err
	}
	failed, err := c.Query(ctx, q("traces_service_graph_request_failed_total"))
	if err != nil {
		return nil, err
	}
	errs := map[[2]string]float64{}
	for _, s := range failed {
		errs[[2]string{s.Labels["client"], s.Labels["server"]}] = s.Value
	}
	g := &Graph{WindowSeconds: window.Seconds()}
	for _, s := range total {
		from, to := s.Labels["client"], s.Labels["server"]
		if from == "" || to == "" || s.Value <= 0 {
			continue
		}
		calls := int64(math.Round(s.Value))
		g.Edges = append(g.Edges, Edge{
			From:       from,
			To:         to,
			Calls:      calls,
			Errors:     int64(math.Round(errs[[2]string{from, to}])),
			RPS:        Round(s.Value/window.Seconds(), 4),
			Confidence: Round(float64(calls)/float64(calls+ConfidenceK), 3),
			Source:     SourceServiceGraph,
		})
	}
	g.Normalize()
	return g, nil
}
