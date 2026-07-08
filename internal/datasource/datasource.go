// Package datasource abstracts the source of container working-set memory. The
// active source is selected explicitly (no fallback) and validated at startup
// via Probe. Sources return instantaneous usage keyed by container; the caller
// joins usage with the container's memory limit (read from the pod spec) and
// feeds the per-container ring buffer used by the detectors.
package datasource

import (
	"context"
	"fmt"
)

// Type is the configured datasource selector.
type Type string

const (
	TypeMetricsServer Type = "metrics-server"
	TypePrometheus    Type = "prometheus"
	TypeDatadog       Type = "datadog"
)

// Usage is the current working-set memory for one container.
type Usage struct {
	Namespace  string
	Pod        string
	Container  string
	WorkingSet int64 // bytes
}

// Source supplies container working-set usage.
type Source interface {
	// Name identifies the source for logging/metrics.
	Name() string
	// Probe validates connectivity and permissions; it must fail fast and
	// clearly if the source is unusable, rather than silently returning nothing.
	Probe(ctx context.Context) error
	// ListUsage returns current usage for containers in the given namespaces.
	// An empty namespaces slice means cluster-wide.
	ListUsage(ctx context.Context, namespaces []string) ([]Usage, error)
}

// Options configures source construction.
type Options struct {
	Type Type

	// Namespaces is the controller's scope (empty means cluster-wide). The
	// metrics-server source probes within this scope so the startup probe needs
	// only the same RBAC that ListUsage does.
	Namespaces []string

	Prometheus PrometheusOptions
	Datadog    DatadogOptions
}

// New constructs the configured Source. metricsClient is required for the
// metrics-server source and may be nil otherwise.
func New(opts Options, metricsClient MetricsClient) (Source, error) {
	switch opts.Type {
	case TypeMetricsServer, "":
		if metricsClient == nil {
			return nil, fmt.Errorf("metrics-server source requires a metrics client")
		}
		return &metricsServerSource{client: metricsClient, namespaces: opts.Namespaces}, nil
	case TypePrometheus:
		return newPrometheusSource(opts.Prometheus)
	case TypeDatadog:
		return newDatadogSource(opts.Datadog)
	default:
		return nil, fmt.Errorf("unknown datasource type %q", opts.Type)
	}
}
