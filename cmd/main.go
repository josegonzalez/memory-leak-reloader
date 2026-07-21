// Command memory-leak-reloader runs the controller: for each MemoryLeakPolicy it
// samples the referenced workload's container memory, detects leak conditions,
// and triggers a rollout restart of that workload (Deployment, StatefulSet, or
// Argo Rollout) when it is safe.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	corev1 "k8s.io/api/core/v1"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/clock"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
	"github.com/josegonzalez/memory-leak-reloader/internal/controller"
	"github.com/josegonzalez/memory-leak-reloader/internal/datasource"
	"github.com/josegonzalez/memory-leak-reloader/internal/gate"
	"github.com/josegonzalez/memory-leak-reloader/internal/logging"
	"github.com/josegonzalez/memory-leak-reloader/internal/maintenance"
	appmetrics "github.com/josegonzalez/memory-leak-reloader/internal/metrics"
	"github.com/josegonzalez/memory-leak-reloader/internal/notify"
	"github.com/josegonzalez/memory-leak-reloader/internal/profile"
	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

// stringSlice is a repeatable string flag (e.g. --maintenance-window).
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ";") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		metricsAddr = flag.String("metrics-bind-address", ":8080", "metrics endpoint address")
		probeAddr   = flag.String("health-probe-bind-address", ":8081", "health probe address")
		leaderElect = flag.Bool("leader-elect", true, "enable leader election")
		leaderID    = flag.String("leader-election-id", "memory-leak-reloader", "leader election lock name")
		logFormat   = flag.String("log-format", "logfmt", "log format: logfmt|json")
		logLevel    = flag.String("log-level", "info", "log level: debug|info|warn|error")
		scopeMode   = flag.String("scope-mode", "cluster", "cluster|namespaces|single")
		dsType      = flag.String("datasource", "metrics-server", "metrics-server|prometheus|datadog")

		mode          = flag.String("mode", "sustained", "default detection mode: sustained|trend|combined")
		thresholdPct  = flag.Int("threshold-percent", 85, "default threshold percent of memory limit")
		windowFlag    = flag.Duration("window", 10*time.Minute, "default detection window")
		sampleEvery   = flag.Duration("sample-interval", 30*time.Second, "metrics sampling interval")
		trendGrowth   = flag.String("trend-min-growth", "100Mi", "default trend min growth per window")
		startupGrace  = flag.Duration("startup-grace", 5*time.Minute, "ignore pods younger than this")
		cooldown      = flag.Duration("cooldown", 30*time.Minute, "per-workload cooldown")
		globalMax     = flag.Int("global-max-concurrent", 1, "max concurrent rollouts (0=unlimited)")
		maxPerWindow  = flag.Int("max-restarts-per-window", 3, "circuit-breaker max restarts per window")
		restartWindow = flag.Duration("restart-window", 24*time.Hour, "circuit-breaker window")
		inflightTO    = flag.Duration("inflight-timeout", 15*time.Minute, "release stuck in-flight slots after")
		requeueAfter  = flag.Duration("requeue-after", 30*time.Second, "settle-poll requeue interval")

		enableDeploy  = flag.Bool("enable-deployments", true, "manage Deployments")
		enableSts     = flag.Bool("enable-statefulsets", true, "manage StatefulSets")
		enableRollout = flag.Bool("enable-rollouts", true, "manage Argo Rollouts (auto-disabled if CRD absent)")

		promURL   = flag.String("prometheus-url", "", "Prometheus base URL")
		promQuery = flag.String("prometheus-query", "container_memory_working_set_bytes", "Prometheus working-set query")
		ddSite    = flag.String("datadog-site", "datadoghq.com", "Datadog site")
		ddMetric  = flag.String("datadog-metric", "kubernetes.memory.working_set", "Datadog working-set metric")

		profileEnabled  = flag.Bool("profile-enabled", false, "capture a heap profile before restart")
		profilePort     = flag.Int("profile-pprof-port", 6060, "pprof port on target pods")
		profileTimeout  = flag.Duration("profile-timeout", 10*time.Second, "profile capture timeout")
		profileSink     = flag.String("profile-sink", "objectstore", "objectstore|volume|log")
		profileBucket   = flag.String("profile-objectstore-bucket", "", "object store bucket")
		profilePrefix   = flag.String("profile-objectstore-prefix", "memreload-profiles/", "object store key prefix")
		profileRegion   = flag.String("profile-objectstore-region", "", "object store region")
		profileProvider = flag.String("profile-objectstore-provider", "s3", "s3|gcs|azblob")
		profileVolume   = flag.String("profile-volume-dir", "/var/run/memreload/profiles", "volume sink dir")

		notifyEvents        = flag.String("notify-events", "RestartTriggered,CircuitBreakerTripped", "comma-separated event types to notify")
		slackDefaultChannel = flag.String("slack-default-channel", "", "default Slack channel (bot-token mode) when a pod sets none")
		notifyRoutesFile    = flag.String("notify-routes-file", "/etc/memreload/routes/routes.json", "path to the named-route registry (routes.json)")
	)
	var namespaces stringSlice
	flag.Var(&namespaces, "namespace", "namespace to scope to (repeatable; required for namespaces/single scope)")
	var windowSpecs stringSlice
	flag.Var(&windowSpecs, "maintenance-window", "allowed restart window, e.g. 'Mon-Fri 09:00-17:00 UTC' (repeatable)")
	flag.Parse()

	logger := logging.New(*logFormat, *logLevel)
	ctrl.SetLogger(logger)
	// client-go (leader election, reflectors) logs via klog, not the
	// controller-runtime logger; route it through the same handler so it
	// honors --log-format and --log-level.
	klog.SetLogger(logger)

	growth, err := resource.ParseQuantity(*trendGrowth)
	if err != nil {
		return fmt.Errorf("invalid --trend-min-growth: %w", err)
	}
	detMode, err := config.ParseMode(*mode)
	if err != nil {
		return err
	}

	ownNamespace := envOr("POD_NAMESPACE", "default")
	restCfg := ctrl.GetConfigOrDie()

	// Cache scoping. Policies (and their workloads) are cached; pods are not - we
	// read only the pods of policy-referenced workloads, on demand, so the client
	// bypasses the cache for pods rather than starting a cluster-wide pod informer.
	cacheOpts := cache.Options{}
	if *scopeMode != "cluster" {
		if len(namespaces) == 0 {
			return fmt.Errorf("--namespace is required when --scope-mode=%s", *scopeMode)
		}
		m := map[string]cache.Config{ownNamespace: {}} // include own ns for the lease
		for _, ns := range namespaces {
			m[ns] = cache.Config{}
		}
		cacheOpts.DefaultNamespaces = m
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                        scheme,
		Cache:                         cacheOpts,
		Client:                        client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Pod{}}}},
		Metrics:                       metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress:        *probeAddr,
		LeaderElection:                *leaderElect,
		LeaderElectionID:              *leaderID,
		LeaderElectionNamespace:       ownNamespace,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	appmetrics.Register(ctrlmetrics.Registry)
	appmetrics.GlobalCap.Set(float64(*globalMax))

	// Datasource (explicit; no fallback).
	var metricsClient datasource.MetricsClient
	if datasource.Type(*dsType) == datasource.TypeMetricsServer {
		mc, err := metricsv.NewForConfig(restCfg)
		if err != nil {
			return fmt.Errorf("metrics client: %w", err)
		}
		metricsClient = datasource.NewMetricsClient(mc.MetricsV1beta1())
	}
	src, err := datasource.New(datasource.Options{
		Type:       datasource.Type(*dsType),
		Namespaces: namespaces,
		Prometheus: datasource.PrometheusOptions{
			URL: *promURL, Query: *promQuery, BearerToken: os.Getenv("PROM_BEARER_TOKEN"),
		},
		Datadog: datasource.DatadogOptions{
			Site: *ddSite, Metric: *ddMetric,
			APIKey: os.Getenv("DD_API_KEY"), AppKey: os.Getenv("DD_APP_KEY"),
		},
	}, metricsClient)
	if err != nil {
		return fmt.Errorf("build datasource: %w", err)
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := src.Probe(probeCtx); err != nil {
		return fmt.Errorf("datasource probe (%s): %w", src.Name(), err)
	}
	logger.Info("datasource ready", "source", src.Name())

	windows, err := maintenance.ParseString(strings.Join(windowSpecs, ";"))
	if err != nil {
		return fmt.Errorf("invalid --maintenance-window: %w", err)
	}

	defaults := config.Defaults{
		Detection: config.Detection{
			Mode: detMode, ThresholdPercent: *thresholdPct, Window: *windowFlag, TrendMinGrowth: growth,
		},
		SampleInterval: *sampleEvery,
		StartupGrace:   *startupGrace,
		Cooldown:       *cooldown,
	}

	// Profile capturer (optional).
	var capturer *profile.Capturer
	if *profileEnabled {
		sink, err := profile.NewSink(context.Background(), profile.SinkConfig{
			Type:      *profileSink,
			VolumeDir: *profileVolume,
			ObjectStore: profile.ObjectStoreConfig{
				Provider: *profileProvider, Bucket: *profileBucket, Prefix: *profilePrefix, Region: *profileRegion,
			},
		})
		if err != nil {
			return fmt.Errorf("build profile sink: %w", err)
		}
		capturer = profile.NewCapturer(sink, *profilePort, *profileTimeout)
	}

	notifier, err := buildNotifier(*notifyEvents, *ddSite, *slackDefaultChannel, *notifyRoutesFile)
	if err != nil {
		return err
	}

	retention := *windowFlag*2 + *sampleEvery
	maxLen := int(*windowFlag/(*sampleEvery)) + 8
	store := sampling.NewStore(retention, maxLen)

	policyEvents := make(chan event.GenericEvent, 1024)

	kinds := restart.Kinds{Deployments: *enableDeploy, StatefulSets: *enableSts, Rollouts: *enableRollout}

	state := restart.NewStore(mgr.GetClient())
	reconciler := &controller.Reconciler{
		Client:               mgr.GetClient(),
		Clock:                clock.Real{},
		Store:                store,
		State:                state,
		Recorder:             mgr.GetEventRecorder("memory-leak-reloader"),
		Defaults:             defaults,
		Kinds:                kinds,
		Gate:                 gate.New(*globalMax),
		Windows:              windows,
		Capturer:             capturer,
		ProfileEnabled:       *profileEnabled,
		Notifier:             notifier,
		RestartWindow:        *restartWindow,
		MaxRestartsPerWindow: *maxPerWindow,
		RequeueAfter:         *requeueAfter,
	}
	if err := reconciler.SetupWithManager(mgr, policyEvents); err != nil {
		return fmt.Errorf("setup reconciler: %w", err)
	}

	sampler := &controller.Sampler{
		Client:     mgr.GetClient(),
		Source:     src,
		Store:      store,
		Clock:      clock.Real{},
		Defaults:   defaults,
		Kinds:      kinds,
		Namespaces: namespaces,
		Interval:   *sampleEvery,
		Events:     policyEvents,
	}
	if err := mgr.Add(sampler); err != nil {
		return fmt.Errorf("add sampler: %w", err)
	}

	// Periodic in-flight slot expiry (leader-gated alongside reconcile). Also
	// clears stale in-flight markers on MemoryLeakPolicy status.
	if err := mgr.Add(newExpirer(reconciler.Gate, state, *inflightTO, *requeueAfter)); err != nil {
		return fmt.Errorf("add expirer: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	logger.Info("starting controller",
		"scope", *scopeMode,
		"datasource", src.Name(), "mode", *mode, "sampleInterval", sampleEvery.String(),
		"maintenanceWindows", windows.String())
	return mgr.Start(ctrl.SetupSignalHandler())
}

func buildNotifier(events, ddSite, slackDefaultChannel, routesFile string) (*notify.Notifier, error) {
	var sinks []notify.Sink
	// Slack: prefer the bot token (enables per-pod channel) over an incoming webhook.
	if tok := os.Getenv("SLACK_BOT_TOKEN"); tok != "" {
		sinks = append(sinks, notify.SlackBotSink{Token: tok, DefaultChannel: slackDefaultChannel})
	} else if u := os.Getenv("SLACK_WEBHOOK_URL"); u != "" {
		sinks = append(sinks, notify.SlackSink{WebhookURL: u})
	}
	if u := os.Getenv("WEBHOOK_URL"); u != "" {
		sinks = append(sinks, notify.WebhookSink{URL: u, AuthHeader: os.Getenv("WEBHOOK_AUTH_HEADER")})
	}
	if os.Getenv("DATADOG_EVENT_ENABLED") == "true" {
		sinks = append(sinks, notify.DatadogEventSink{Site: ddSite, APIKey: os.Getenv("DD_API_KEY")})
	}

	routes, err := notify.LoadRoutes(routesFile)
	if err != nil {
		return nil, fmt.Errorf("load notification routes: %w", err)
	}

	// No default sinks and no named routes => notifications disabled.
	if len(sinks) == 0 && len(routes) == 0 {
		return nil, nil
	}
	var types []notify.EventType
	for _, e := range strings.Split(events, ",") {
		if e = strings.TrimSpace(e); e != "" {
			types = append(types, notify.EventType(e))
		}
	}
	return notify.New(sinks, routes, types, 5*time.Second), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
