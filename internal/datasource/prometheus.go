package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PrometheusOptions configures the Prometheus source.
type PrometheusOptions struct {
	URL         string
	Query       string // instant query returning a vector labeled namespace/pod/container
	BearerToken string // optional
	HTTPClient  *http.Client
}

type prometheusSource struct {
	opts   PrometheusOptions
	client *http.Client
}

func newPrometheusSource(o PrometheusOptions) (Source, error) {
	if strings.TrimSpace(o.URL) == "" {
		return nil, fmt.Errorf("prometheus source requires a URL")
	}
	if strings.TrimSpace(o.Query) == "" {
		o.Query = "container_memory_working_set_bytes"
	}
	c := o.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	return &prometheusSource{opts: o, client: c}, nil
}

func (s *prometheusSource) Name() string { return string(TypePrometheus) }

// Probe runs the configured query once to confirm reachability and that the
// metric exists.
func (s *prometheusSource) Probe(ctx context.Context) error {
	if _, err := s.query(ctx); err != nil {
		return fmt.Errorf("prometheus probe failed for %q: %w", s.opts.URL, err)
	}
	return nil
}

// promResponse models the instant-query JSON envelope.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (s *prometheusSource) query(ctx context.Context) (*promResponse, error) {
	u, err := url.Parse(strings.TrimRight(s.opts.URL, "/") + "/api/v1/query")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", s.opts.Query)
	u.RawQuery = q.Encode()

	var headers map[string]string
	if s.opts.BearerToken != "" {
		headers = map[string]string{"Authorization": "Bearer " + s.opts.BearerToken}
	}
	var pr promResponse
	if err := getJSON(ctx, s.client, u.String(), headers, &pr); err != nil {
		return nil, err
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus query error: %s", pr.Error)
	}
	return &pr, nil
}

func (s *prometheusSource) ListUsage(ctx context.Context, namespaces []string) ([]Usage, error) {
	pr, err := s.query(ctx)
	if err != nil {
		return nil, err
	}
	want := namespaceSet(namespaces)
	var out []Usage
	for _, r := range pr.Data.Result {
		ns := r.Metric["namespace"]
		pod := r.Metric["pod"]
		container := r.Metric["container"]
		if container == "" || container == "POD" || pod == "" {
			continue
		}
		if want != nil && !want[ns] {
			continue
		}
		val, err := promSampleValue(r.Value[1])
		if err != nil {
			continue
		}
		out = append(out, Usage{Namespace: ns, Pod: pod, Container: container, WorkingSet: val})
	}
	return out, nil
}

// promSampleValue parses the stringified float in a Prometheus sample value.
func promSampleValue(raw json.RawMessage) (int64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

// namespaceSet returns a lookup set, or nil for "all namespaces".
func namespaceSet(namespaces []string) map[string]bool {
	if len(namespaces) == 0 {
		return nil
	}
	m := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		m[ns] = true
	}
	return m
}
