// Package controller contains the workload-restart reconciler and the metrics
// sampling runnable. The reconciler is keyed on a MemoryLeakPolicy: it resolves
// the policy's workload, evaluates that workload's pods, and (if a pod is
// leaking and all gates pass) triggers a single rollout restart. Per-workload
// deduplication and the global concurrency cap are enforced by the gate.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/clock"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
	"github.com/josegonzalez/memory-leak-reloader/internal/gate"
	"github.com/josegonzalez/memory-leak-reloader/internal/maintenance"
	"github.com/josegonzalez/memory-leak-reloader/internal/metrics"
	"github.com/josegonzalez/memory-leak-reloader/internal/notify"
	"github.com/josegonzalez/memory-leak-reloader/internal/profile"
	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

// PolicyFinalizer lets the reconciler release the in-memory concurrency slot for
// a workload before its policy (and folded state) is deleted. Without it, a
// policy deleted mid-rollout would strand its slot until the controller restarts.
const PolicyFinalizer = "memreload.io/finalizer"

// Event reasons emitted on the pod or owning workload.
const (
	reasonBreachDetected   = "BreachDetected"
	reasonWouldRestart     = "WouldRestart"
	reasonRestartTriggered = "RestartTriggered"
	reasonRestartDeferred  = "RestartDeferred"
	reasonProfileCaptured  = "ProfileCaptured"
	reasonCircuitBreaker   = "CircuitBreakerTripped"
	reasonPodUnmonitorable = "PodUnmonitorable"
	reasonInvalidConfig    = "InvalidConfig"
)

// Reconciler implements the leak-driven rollout-restart control loop.
type Reconciler struct {
	Client   client.Client
	Clock    clock.Clock
	Store    *sampling.Store
	State    *restart.Store
	Recorder events.EventRecorder

	Defaults config.Defaults
	Kinds    restart.Kinds
	Gate     *gate.Gate
	Windows  maintenance.Windows

	Capturer       *profile.Capturer // nil when capture disabled
	ProfileEnabled bool

	Notifier *notify.Notifier

	DryRun               bool
	RestartWindow        time.Duration
	MaxRestartsPerWindow int
	RequeueAfter         time.Duration

	warnedUnmonitorable sync.Map // pod UID -> struct{}
	lastWouldRestart    sync.Map // workload key -> time.Time (dry-run notify throttle)
}

// Reconcile evaluates one MemoryLeakPolicy: it resolves the workload, polls any
// in-flight rollout to completion, and otherwise scans the workload's pods for a
// leak, triggering a rollout restart if all gates pass.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &v1alpha1.MemoryLeakPolicy{}
	if err := r.Client.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Finalizer: release the workload's in-memory slot before the policy (and its
	// folded state) is deleted.
	wkey := restart.WorkloadKey(policy.Spec.WorkloadRef.Kind, policy.Namespace, policy.Spec.WorkloadRef.Name)
	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(policy, PolicyFinalizer) {
			if r.Gate.Holds(wkey) {
				r.Gate.Release(wkey)
				metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
			}
			controllerutil.RemoveFinalizer(policy, PolicyFinalizer)
			if err := r.Client.Update(ctx, policy); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(policy, PolicyFinalizer) {
		if err := r.Client.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	podCfg := config.ResolvePolicy(r.Defaults, policy.Spec)

	// Resolve the managed workload.
	wl, err := restart.GetWorkload(ctx, r.Client, restart.Kind(policy.Spec.WorkloadRef.Kind), policy.Namespace, policy.Spec.WorkloadRef.Name)
	switch {
	case apierrors.IsNotFound(err):
		// The workload does not exist (yet); nothing to evaluate. Poll for it.
		return ctrl.Result{RequeueAfter: r.RequeueAfter}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	now := r.Clock.Now()
	curVersion := wl.TemplateVersion()

	// Identity guard: adopt the live workload UID, resetting stale bookkeeping if
	// the workload was deleted and recreated under the same name.
	prevUID := policy.Status.WorkloadUID
	restart.AdoptWorkload(&policy.Status, wl.UID())
	if prevUID != policy.Status.WorkloadUID {
		if err := r.State.Persist(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Settle / in-flight handling. When a rollout is in progress or we still hold
	// a slot, resolve it before looking for a new breach.
	settled, err := wl.IsSettled()
	switch {
	case errors.Is(err, restart.ErrNotAutoRestartable):
		return r.skip("not_autorestartable")
	case err != nil:
		return ctrl.Result{}, err
	}
	if !settled {
		if r.Gate.Holds(wkey) {
			return ctrl.Result{RequeueAfter: r.RequeueAfter}, nil
		}
		// Deterministic failover: if our status records that we dispatched this
		// exact version and it has not settled, this rollout is ours - resume
		// polling by re-acquiring the slot rather than treating it as someone else's.
		if policy.Status.InFlight != nil && policy.Status.InFlight.DispatchedVersion == curVersion {
			r.Gate.TryAcquire(wkey, policy.Status.InFlight.DispatchedAt.Time)
			metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
			return ctrl.Result{RequeueAfter: r.RequeueAfter}, nil
		}
		return r.skip("in_progress")
	}
	// Settled and we held a slot from our own prior restart: release it and record
	// the completion. If the workload settled at a version other than the one we
	// dispatched, an external rollout completed in our place - record Superseded.
	if r.Gate.Holds(wkey) {
		r.Gate.Release(wkey)
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		if policy.Status.InFlight != nil {
			outcome := v1alpha1.OutcomeSettled
			if policy.Status.InFlight.DispatchedVersion != curVersion {
				outcome = v1alpha1.OutcomeSuperseded
			}
			restart.CompleteInFlight(&policy.Status, now, outcome)
			restart.RecomputeObservability(&policy.Status, now, podCfg.Cooldown, r.RestartWindow, r.MaxRestartsPerWindow)
			if err := r.State.Persist(ctx, policy); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Detection: find the first leaking container on a Ready, current-revision pod.
	pods, err := listWorkloadPods(ctx, r.Client, wl)
	if err != nil {
		return ctrl.Result{}, err
	}
	var breach *Target
	var breachPod *corev1.Pod
	var result sampling.Result
	for i := range pods {
		pod := &pods[i]
		if !podReady(pod) || r.podAge(pod) < podCfg.StartupGrace {
			continue
		}
		owner, oerr := restart.ResolveOwner(ctx, r.Client, pod, r.Kinds)
		switch {
		case errors.Is(oerr, restart.ErrNoOwner):
			continue
		case oerr != nil:
			return ctrl.Result{}, oerr
		}
		if !owner.IsCurrentRevision() {
			continue
		}
		targets, dropped := SelectTargets(pod, podCfg)
		if len(targets) == 0 {
			if dropped > 0 {
				r.warnUnmonitorableOnce(pod, log)
			}
			continue
		}
		for j := range targets {
			key := sampling.Key{Namespace: pod.Namespace, Pod: pod.Name, Container: targets[j].Name}
			res := sampling.Detect(r.Store.Samples(key), targets[j].Det, now)
			if res.Leaking {
				breach = &targets[j]
				breachPod = pod
				result = res
				break
			}
		}
		if breach != nil {
			break
		}
	}
	if breach == nil {
		return ctrl.Result{}, nil
	}
	metrics.ThresholdBreaches.WithLabelValues(string(breach.Det.Mode)).Inc()
	r.Recorder.Eventf(breachPod, nil, corev1.EventTypeNormal, reasonBreachDetected, "Detect",
		"container %s leaking: %s (observed=%d threshold=%d)", breach.Name, result.Reason, result.Observed, result.Threshold)

	// Cooldown + circuit breaker (both read from the policy status).
	if restart.InCooldown(policy.Status, now, podCfg.Cooldown) {
		return r.skip("cooldown")
	}
	if restart.BreakerTripped(policy.Status, curVersion, now, r.RestartWindow, r.MaxRestartsPerWindow) {
		r.Recorder.Eventf(wl.Object(), nil, corev1.EventTypeWarning, reasonCircuitBreaker, "Restart",
			"max restarts per window reached; not restarting %s", wkey)
		r.notify(ctx, notify.EventCircuitBreakerTripped, wl, breach, result, now, podCfg)
		return r.skip("circuit_breaker")
	}

	// Maintenance window (policy override beats controller default).
	windows := r.Windows
	if len(podCfg.MaintenanceWindows) > 0 {
		if w, perr := maintenance.FromWindows(podCfg.MaintenanceWindows); perr != nil {
			r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, reasonInvalidConfig, "Resolve", "%s", perr.Error())
		} else {
			windows = w
		}
	}
	if !windows.IsAllowed(now) {
		metrics.RolloutsDeferred.Inc()
		r.Recorder.Eventf(wl.Object(), nil, corev1.EventTypeNormal, reasonRestartDeferred, "Restart",
			"restart deferred: outside maintenance window")
		r.notify(ctx, notify.EventRestartDeferred, wl, breach, result, now, podCfg)
		d := windows.NextOpening(now).Sub(now)
		if d <= 0 {
			d = r.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: d}, nil
	}

	// Acquire the in-flight slot (per-workload dedupe + global cap).
	switch r.Gate.TryAcquire(wkey, now) {
	case gate.AlreadyInF:
		return r.skip("in_progress")
	case gate.CapReached:
		return r.skipRequeue("cap")
	}
	metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))

	// Dispatch-time supersession guard: re-read the live version and bail if it
	// moved, so we don't stamp a restart (and bump the breaker) on top of an
	// external rollout that landed in the window since we resolved the workload.
	if fresh, ferr := restart.GetWorkload(ctx, r.Client, wl.Kind, wl.Ref.Namespace, wl.Ref.Name); ferr != nil {
		r.Gate.Release(wkey)
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		return ctrl.Result{}, ferr
	} else if fresh.TemplateVersion() != curVersion {
		r.Gate.Release(wkey)
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		return r.skip("superseded")
	}

	// Best-effort pre-restart profile capture (records the artifact on the policy).
	r.captureProfile(ctx, breachPod, podCfg, breach, now, &policy.Status, log)

	if r.DryRun {
		r.Gate.Release(wkey) // nothing actually in flight in dry-run
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		// Dry-run cannot write the cooldown state, so throttle the would-restart
		// Event + notification in memory to the cadence a real restart would have.
		if r.recentlyWouldRestart(wkey, now, podCfg.Cooldown) {
			return r.skip("cooldown")
		}
		r.lastWouldRestart.Store(wkey, now)
		r.Recorder.Eventf(wl.Object(), nil, corev1.EventTypeNormal, reasonWouldRestart, "Restart",
			"[dry-run] would restart %s due to container %s (observed=%d threshold=%d)", wkey, breach.Name, result.Observed, result.Threshold)
		metrics.RolloutsTriggered.WithLabelValues(string(wl.Kind), "dry_run").Inc()
		r.notify(ctx, notify.EventRestartTriggered, wl, breach, result, now, podCfg)
		return ctrl.Result{}, nil
	}

	// Record the restart in the policy status before triggering it. Persisting
	// first is the conservative order for a breaker: if the trigger then fails we
	// have over-counted (safe) rather than under-counted.
	restart.ApplyRestart(&policy.Status, wl, restart.Cause{
		Container: breach.Name, Observed: result.Observed, Threshold: result.Threshold, Mode: string(breach.Det.Mode),
	}, now, r.RestartWindow)
	policy.Status.LastNotification = &v1alpha1.NotificationRef{Event: string(notify.EventRestartTriggered), NotifiedAt: metav1.NewTime(now)}
	restart.RecomputeObservability(&policy.Status, now, podCfg.Cooldown, r.RestartWindow, r.MaxRestartsPerWindow)
	if err := r.State.Persist(ctx, policy); err != nil {
		r.Gate.Release(wkey)
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		metrics.RolloutsTriggered.WithLabelValues(string(wl.Kind), "error").Inc()
		return ctrl.Result{}, fmt.Errorf("persist restart state for %s: %w", wkey, err)
	}
	if err := wl.Dispatch(ctx, r.Client, now); err != nil {
		r.Gate.Release(wkey)
		metrics.InflightRollouts.Set(float64(r.Gate.Inflight()))
		metrics.RolloutsTriggered.WithLabelValues(string(wl.Kind), "error").Inc()
		return ctrl.Result{}, fmt.Errorf("dispatch restart for %s: %w", wkey, err)
	}
	metrics.RolloutsTriggered.WithLabelValues(string(wl.Kind), "success").Inc()
	r.Recorder.Eventf(wl.Object(), nil, corev1.EventTypeNormal, reasonRestartTriggered, "Restart",
		"restarted %s due to container %s (observed=%d threshold=%d, mode=%s)", wkey, breach.Name, result.Observed, result.Threshold, breach.Det.Mode)
	r.notify(ctx, notify.EventRestartTriggered, wl, breach, result, now, podCfg)

	// Poll until the rollout settles, then the slot is released.
	return ctrl.Result{RequeueAfter: r.RequeueAfter}, nil
}

// skip records a skipped restart by reason (the reason label is the source of
// truth for why; see the metric and the docs/troubleshooting table).
func (r *Reconciler) skip(reason string) (ctrl.Result, error) {
	metrics.RolloutsSkipped.WithLabelValues(reason).Inc()
	return ctrl.Result{}, nil
}

func (r *Reconciler) skipRequeue(reason string) (ctrl.Result, error) {
	metrics.RolloutsSkipped.WithLabelValues(reason).Inc()
	return ctrl.Result{RequeueAfter: r.RequeueAfter}, nil
}

func (r *Reconciler) warnUnmonitorableOnce(pod *corev1.Pod, log logr.Logger) {
	if _, loaded := r.warnedUnmonitorable.LoadOrStore(pod.UID, struct{}{}); loaded {
		return
	}
	log.Info("opted-in pod is unmonitorable: no watched container has a memory limit or hard-cap",
		"namespace", pod.Namespace, "pod", pod.Name)
	r.Recorder.Eventf(pod, nil, corev1.EventTypeWarning, reasonPodUnmonitorable, "Monitor",
		"no watched container has a memory limit or hard-cap; pod will not be monitored")
}

func (r *Reconciler) captureProfile(ctx context.Context, pod *corev1.Pod, podCfg config.PodConfig, breach *Target, now time.Time, st *v1alpha1.MemoryLeakPolicyStatus, log logr.Logger) {
	if r.Capturer == nil {
		return
	}
	enabled := r.ProfileEnabled
	if podCfg.ProfileCapture != nil {
		enabled = *podCfg.ProfileCapture
	}
	if !enabled {
		return
	}
	path := podCfg.PprofPath
	if path == "" {
		path = "/debug/pprof/heap"
	}
	key := fmt.Sprintf("%s/%s/%s/%s.pb.gz", pod.Namespace, pod.Name, breach.Name, now.UTC().Format("20060102T150405Z"))
	res, err := r.Capturer.Capture(ctx, pod.Status.PodIP, path, key)
	switch {
	case err != nil:
		metrics.ProfileCaptures.WithLabelValues("error").Inc()
		log.Info("profile capture failed (non-fatal)", "error", err.Error())
	case res.Skipped:
		metrics.ProfileCaptures.WithLabelValues("skipped").Inc()
	default:
		metrics.ProfileCaptures.WithLabelValues("success").Inc()
		if st != nil {
			st.LastProfile = &v1alpha1.ProfileRef{URL: res.URI, CapturedAt: metav1.NewTime(now)}
		}
		r.Recorder.Eventf(pod, nil, corev1.EventTypeNormal, reasonProfileCaptured, "CaptureProfile", "captured heap profile (%d bytes) to %s", res.Size, res.URI)
	}
}

func (r *Reconciler) notify(ctx context.Context, t notify.EventType, wl *restart.Workload, breach *Target, result sampling.Result, now time.Time, podCfg config.PodConfig) {
	if r.Notifier == nil {
		return
	}
	// Validate per-policy route names; unknown routes are surfaced and dropped so a
	// policy can only target pre-registered destinations.
	var routes []string
	for _, name := range podCfg.NotifyRoutes {
		if r.Notifier.KnownRoute(name) {
			routes = append(routes, name)
		} else {
			r.Recorder.Eventf(wl.Object(), nil, corev1.EventTypeWarning, reasonInvalidConfig, "Resolve",
				"unknown notification route %q (not registered in the controller)", name)
		}
	}
	r.Notifier.Notify(ctx, notify.Event{
		Type:         t,
		Kind:         string(wl.Kind),
		Workload:     wl.Ref.Name,
		Namespace:    wl.Ref.Namespace,
		Container:    breach.Name,
		Mode:         string(breach.Det.Mode),
		Observed:     result.Observed,
		Threshold:    result.Threshold,
		Window:       breach.Det.Window,
		Reason:       result.Reason,
		DryRun:       r.DryRun,
		Time:         now,
		Routes:       routes,
		SlackChannel: podCfg.SlackChannel,
	})
}

// recentlyWouldRestart reports whether a dry-run would-restart was already
// emitted for this workload within the cooldown window.
func (r *Reconciler) recentlyWouldRestart(wkey string, now time.Time, cooldown time.Duration) bool {
	v, ok := r.lastWouldRestart.Load(wkey)
	if !ok {
		return false
	}
	return now.Sub(v.(time.Time)) < cooldown
}
