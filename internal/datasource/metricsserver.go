package datasource

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

// MetricsClient is the subset of the metrics-server client we use, narrowed to
// an interface so the source can be unit-tested with a fake.
type MetricsClient interface {
	PodMetricses(namespace string) PodMetricsLister
}

// PodMetricsLister lists PodMetrics in a namespace.
type PodMetricsLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*metricsv1beta1.PodMetricsList, error)
}

// realMetricsClient adapts the generated clientset to MetricsClient.
type realMetricsClient struct {
	c metricsclient.MetricsV1beta1Interface
}

// NewMetricsClient wraps a generated MetricsV1beta1 client.
func NewMetricsClient(c metricsclient.MetricsV1beta1Interface) MetricsClient {
	return &realMetricsClient{c: c}
}

func (r *realMetricsClient) PodMetricses(ns string) PodMetricsLister {
	return r.c.PodMetricses(ns)
}

type metricsServerSource struct {
	client     MetricsClient
	namespaces []string // controller scope; empty means cluster-wide
}

func (s *metricsServerSource) Name() string { return string(TypeMetricsServer) }

// Probe lists PodMetrics within the controller's scope to confirm the aggregated
// metrics.k8s.io API is registered and the controller has list permission. It
// probes the first scoped namespace (or cluster-wide when unscoped) so it needs
// only the same RBAC ListUsage does; a cluster-wide probe would require
// cluster-scoped access even for a single-namespace deployment.
func (s *metricsServerSource) Probe(ctx context.Context) error {
	ns := metav1.NamespaceAll
	if len(s.namespaces) > 0 {
		ns = s.namespaces[0]
	}
	if _, err := s.client.PodMetricses(ns).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("metrics-server probe failed (is metrics-server installed and RBAC granted on metrics.k8s.io?): %w", err)
	}
	return nil
}

// ListUsage lists PodMetrics for the given namespaces (or cluster-wide) and
// flattens to per-container working-set usage.
func (s *metricsServerSource) ListUsage(ctx context.Context, namespaces []string) ([]Usage, error) {
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	var out []Usage
	for _, ns := range namespaces {
		list, err := s.client.PodMetricses(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pod metrics in %q: %w", ns, err)
		}
		for i := range list.Items {
			pm := &list.Items[i]
			for j := range pm.Containers {
				c := &pm.Containers[j]
				mem := c.Usage.Memory()
				out = append(out, Usage{
					Namespace:  pm.Namespace,
					Pod:        pm.Name,
					Container:  c.Name,
					WorkingSet: mem.Value(),
				})
			}
		}
	}
	return out, nil
}
