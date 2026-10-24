// Package prom is a minimal Prometheus HTTP API client (instant queries only).
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client queries one Prometheus-compatible endpoint.
type Client struct {
	HTTP    *http.Client
	BaseURL string            // e.g. http://prometheus:9090
	Headers map[string]string // e.g. Authorization, X-Scope-OrgID
}

// Sample is one series of an instant vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// New returns a client for baseURL using http.DefaultClient.
func New(baseURL string) *Client {
	return &Client{HTTP: http.DefaultClient, BaseURL: strings.TrimRight(baseURL, "/")}
}

type response struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// Query runs an instant query. Scalars come back as one sample without
// labels. NaN and Inf samples are dropped, so "0/0" reads as no data.
func (c *Client) Query(ctx context.Context, q string) ([]Sample, error) {
	u := c.BaseURL + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("prometheus %s: %s", resp.Status, truncate(body))
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s: %s", r.ErrorType, r.Error)
	}
	switch r.Data.ResultType {
	case "vector":
		var vec []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
			return nil, err
		}
		var out []Sample
		for _, v := range vec {
			if f, ok := parseValue(v.Value[1]); ok {
				out = append(out, Sample{Labels: v.Metric, Value: f})
			}
		}
		return out, nil
	case "scalar":
		var sc [2]any
		if err := json.Unmarshal(r.Data.Result, &sc); err != nil {
			return nil, err
		}
		if f, ok := parseValue(sc[1]); ok {
			return []Sample{{Value: f}}, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported result type %q (want vector or scalar)", r.Data.ResultType)
}

func parseValue(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func truncate(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return strings.TrimSpace(string(b))
}
