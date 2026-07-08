// Package metrics defines the controller's own Prometheus collectors. They are
// registered against a caller-supplied registerer (the controller-runtime
// metrics registry in production) so this package stays free of a
// controller-runtime import and is trivially testable.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// PodsMonitored is the number of opted-in, monitorable pods.
	PodsMonitored = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memreload_pods_monitored",
		Help: "Number of opted-in pods currently being monitored.",
	})

	// ThresholdBreaches counts detected leak conditions by detection mode.
	ThresholdBreaches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_threshold_breaches_total",
		Help: "Total leak conditions detected, by detection mode.",
	}, []string{"mode"})

	// RolloutsTriggered counts dispatched restarts by workload kind and result.
	RolloutsTriggered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_rollouts_triggered_total",
		Help: "Total rollout restarts triggered, by workload kind and result.",
	}, []string{"workload_kind", "result"})

	// RolloutsSkipped counts restarts that were gated out, by reason.
	RolloutsSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_rollouts_skipped_total",
		Help: "Total restarts skipped, by reason (in_progress, cooldown, cap, not_ready, old_revision, dry_run, circuit_breaker, superseded).",
	}, []string{"reason"})

	// RolloutsDeferred counts restarts deferred by a closed maintenance window.
	RolloutsDeferred = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "memreload_rollouts_deferred_total",
		Help: "Total restarts deferred because the maintenance window was closed.",
	})

	// ProfileCaptures counts pre-restart profile capture attempts by result.
	ProfileCaptures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_profile_captures_total",
		Help: "Total pre-restart profile captures, by result (success, error, skipped).",
	}, []string{"result"})

	// Notifications counts notification deliveries by sink and result.
	Notifications = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_notifications_total",
		Help: "Total notification deliveries, by sink and result.",
	}, []string{"sink", "result"})

	// ContainersIgnored is the number of watched containers ignored, by reason.
	ContainersIgnored = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "memreload_containers_ignored",
		Help: "Number of watched containers ignored, by reason (e.g. no_limit).",
	}, []string{"reason"})

	// InflightRollouts is the number of restarts currently in flight.
	InflightRollouts = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memreload_inflight_rollouts",
		Help: "Number of rollout restarts currently in flight.",
	})

	// GlobalCap exposes the configured global max-concurrent cap.
	GlobalCap = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memreload_global_cap",
		Help: "Configured global max-concurrent rollout cap.",
	})

	// SampleBufferSeries is the number of in-memory per-container series held.
	SampleBufferSeries = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memreload_sample_buffer_series",
		Help: "Number of in-memory per-container sample series held.",
	})

	// DatasourceErrors counts datasource query/probe errors by source.
	DatasourceErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "memreload_datasource_errors_total",
		Help: "Total datasource errors, by source.",
	}, []string{"source"})

	// DryRun is 1 when the controller is running in dry-run mode, else 0.
	DryRun = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memreload_dryrun",
		Help: "1 if the controller is running in dry-run mode, else 0.",
	})
)

// collectors lists every collector for bulk registration.
func collectors() []prometheus.Collector {
	return []prometheus.Collector{
		PodsMonitored, ThresholdBreaches, RolloutsTriggered, RolloutsSkipped,
		RolloutsDeferred, ProfileCaptures, Notifications, ContainersIgnored,
		InflightRollouts, GlobalCap, SampleBufferSeries, DatasourceErrors, DryRun,
	}
}

// Register registers all collectors with reg. Already-registered collectors are
// ignored, so it is safe to call more than once (e.g. across tests).
func Register(reg prometheus.Registerer) {
	for _, c := range collectors() {
		if err := reg.Register(c); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				panic(err)
			}
		}
	}
}
