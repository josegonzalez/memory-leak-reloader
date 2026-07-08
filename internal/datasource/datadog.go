package datasource

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DatadogOptions configures the Datadog source.
type DatadogOptions struct {
	Site       string // e.g. datadoghq.com
	APIKey     string
	AppKey     string
	Metric     string // e.g. kubernetes.memory.working_set
	HTTPClient *http.Client
	nowFunc    func() time.Time // test hook
}

type datadogSource struct {
	opts   DatadogOptions
	client *http.Client
	query  string
}

func newDatadogSource(o DatadogOptions) (Source, error) {
	if strings.TrimSpace(o.APIKey) == "" || strings.TrimSpace(o.AppKey) == "" {
		return nil, fmt.Errorf("datadog source requires DD_API_KEY and DD_APP_KEY")
	}
	if strings.TrimSpace(o.Site) == "" {
		o.Site = "datadoghq.com"
	}
	if strings.TrimSpace(o.Metric) == "" {
		o.Metric = "kubernetes.memory.working_set"
	}
	if o.nowFunc == nil {
		o.nowFunc = time.Now
	}
	c := o.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	q := fmt.Sprintf("avg:%s{*} by {kube_namespace,pod_name,kube_container_name}", o.Metric)
	return &datadogSource{opts: o, client: c, query: q}, nil
}

func (s *datadogSource) Name() string { return string(TypeDatadog) }

func (s *datadogSource) Probe(ctx context.Context) error {
	if _, err := s.queryRange(ctx); err != nil {
		return fmt.Errorf("datadog probe failed for site %q: %w", s.opts.Site, err)
	}
	return nil
}

type ddResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Series []struct {
		TagSet    []string    `json:"tag_set"`
		PointList [][]float64 `json:"pointlist"`
	} `json:"series"`
}

func (s *datadogSource) queryRange(ctx context.Context) (*ddResponse, error) {
	now := s.opts.nowFunc()
	u, err := url.Parse("https://api." + s.opts.Site + "/api/v1/query")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("from", fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix()))
	q.Set("to", fmt.Sprintf("%d", now.Unix()))
	q.Set("query", s.query)
	u.RawQuery = q.Encode()

	headers := map[string]string{
		"DD-API-KEY":         s.opts.APIKey,
		"DD-APPLICATION-KEY": s.opts.AppKey,
	}
	var dr ddResponse
	if err := getJSON(ctx, s.client, u.String(), headers, &dr); err != nil {
		return nil, err
	}
	if dr.Status != "ok" && dr.Error != "" {
		return nil, fmt.Errorf("datadog query error: %s", dr.Error)
	}
	return &dr, nil
}

func (s *datadogSource) ListUsage(ctx context.Context, namespaces []string) ([]Usage, error) {
	dr, err := s.queryRange(ctx)
	if err != nil {
		return nil, err
	}
	want := namespaceSet(namespaces)
	var out []Usage
	for _, series := range dr.Series {
		ns, pod, container := tagsFromSet(series.TagSet)
		if container == "" || pod == "" {
			continue
		}
		if want != nil && !want[ns] {
			continue
		}
		val, ok := lastPoint(series.PointList)
		if !ok {
			continue
		}
		out = append(out, Usage{Namespace: ns, Pod: pod, Container: container, WorkingSet: int64(val)})
	}
	return out, nil
}

func tagsFromSet(tags []string) (ns, pod, container string) {
	for _, t := range tags {
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		switch k {
		case "kube_namespace":
			ns = v
		case "pod_name":
			pod = v
		case "kube_container_name":
			container = v
		}
	}
	return
}

// lastPoint returns the most recent non-null value in a Datadog pointlist.
func lastPoint(points [][]float64) (float64, bool) {
	for i := len(points) - 1; i >= 0; i-- {
		if len(points[i]) == 2 {
			return points[i][1], true
		}
	}
	return 0, false
}
