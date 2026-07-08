package datasource

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// fakeMetricsClient records the namespace passed to PodMetricses so tests can
// assert which namespace a List/Probe targeted.
type fakeMetricsClient struct{ gotNamespace string }

func (f *fakeMetricsClient) PodMetricses(ns string) PodMetricsLister {
	f.gotNamespace = ns
	return fakeLister{}
}

type fakeLister struct{}

func (fakeLister) List(context.Context, metav1.ListOptions) (*metricsv1beta1.PodMetricsList, error) {
	return &metricsv1beta1.PodMetricsList{}, nil
}

func TestMetricsServerProbeRespectsScope(t *testing.T) {
	// Scoped: the probe must list within the first scoped namespace so it needs
	// only the namespaced RBAC ListUsage uses.
	scoped := &fakeMetricsClient{}
	s, err := New(Options{Type: TypeMetricsServer, Namespaces: []string{"payments", "ledger"}}, scoped)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if scoped.gotNamespace != "payments" {
		t.Fatalf("scoped probe namespace = %q, want %q", scoped.gotNamespace, "payments")
	}

	// Unscoped: the probe lists cluster-wide.
	cluster := &fakeMetricsClient{}
	s, err = New(Options{Type: TypeMetricsServer}, cluster)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if cluster.gotNamespace != metav1.NamespaceAll {
		t.Fatalf("cluster probe namespace = %q, want cluster-wide (empty)", cluster.gotNamespace)
	}
}

// rtFunc is a RoundTripper that returns a canned body for any request.
type rtFunc func(*http.Request) *http.Response

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func clientReturning(body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(_ *http.Request) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
	})}
}

func TestPrometheusListUsage(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"namespace":"ns","pod":"web-1","container":"app"},"value":[1700000000,"123456"]},
		{"metric":{"namespace":"ns","pod":"web-1","container":"POD"},"value":[1700000000,"1"]},
		{"metric":{"namespace":"other","pod":"x","container":"c"},"value":[1700000000,"999"]}
	]}}`
	s, err := newPrometheusSource(PrometheusOptions{URL: "http://prom", HTTPClient: clientReturning(body)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	usage, err := s.ListUsage(context.Background(), []string{"ns"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d usages want 1 (POD and other-ns filtered): %+v", len(usage), usage)
	}
	if usage[0].Container != "app" || usage[0].WorkingSet != 123456 {
		t.Fatalf("usage = %+v", usage[0])
	}
}

func TestDatadogListUsage(t *testing.T) {
	body := `{"status":"ok","series":[
		{"tag_set":["kube_namespace:ns","pod_name:web-1","kube_container_name:app"],"pointlist":[[1700000000000,100],[1700000030000,222]]}
	]}`
	s, err := newDatadogSource(DatadogOptions{Site: "datadoghq.com", APIKey: "k", AppKey: "a", HTTPClient: clientReturning(body)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	usage, err := s.ListUsage(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(usage) != 1 || usage[0].WorkingSet != 222 { // most recent point
		t.Fatalf("usage = %+v", usage)
	}
	if usage[0].Namespace != "ns" || usage[0].Pod != "web-1" || usage[0].Container != "app" {
		t.Fatalf("tags parsed wrong: %+v", usage[0])
	}
}

func TestDatadogRequiresKeys(t *testing.T) {
	if _, err := newDatadogSource(DatadogOptions{Site: "datadoghq.com"}); err == nil {
		t.Fatal("expected error without API/APP keys")
	}
}

func TestNewUnknownType(t *testing.T) {
	if _, err := New(Options{Type: "bogus"}, nil); err == nil {
		t.Fatal("expected error for unknown datasource type")
	}
}

// clientStatus returns an http.Client whose transport always responds with the
// given status code and an empty body.
func clientStatus(code int) *http.Client {
	return &http.Client{Transport: rtFunc(func(_ *http.Request) *http.Response {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
	})}
}

func TestGetJSON_NonOKErrors(t *testing.T) {
	// The shared getJSON helper must surface non-200 responses as errors for
	// every source (here exercised through the Prometheus and Datadog sources).
	ps, _ := newPrometheusSource(PrometheusOptions{URL: "http://prom", HTTPClient: clientStatus(503)})
	if _, err := ps.ListUsage(context.Background(), nil); err == nil {
		t.Error("prometheus ListUsage should error on HTTP 503")
	}
	ds, _ := newDatadogSource(DatadogOptions{Site: "datadoghq.com", APIKey: "k", AppKey: "a", HTTPClient: clientStatus(500)})
	if _, err := ds.ListUsage(context.Background(), nil); err == nil {
		t.Error("datadog ListUsage should error on HTTP 500")
	}
}
