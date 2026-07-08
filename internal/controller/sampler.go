package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/clock"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
	"github.com/josegonzalez/memory-leak-reloader/internal/datasource"
	"github.com/josegonzalez/memory-leak-reloader/internal/metrics"
	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

// Sampler periodically queries the datasource for container working-set memory,
// joins it with container limits from pod specs, feeds the per-container ring
// buffers, prunes stale series, and enqueues the policy whose workload's window
// is covered. It is leader-gated: only the active leader samples and acts.
type Sampler struct {
	Client     client.Client
	Source     datasource.Source
	Store      *sampling.Store
	Clock      clock.Clock
	Defaults   config.Defaults
	Kinds      restart.Kinds
	Namespaces []string // scope; empty means all (within cache scope)

	Interval time.Duration
	Events   chan<- event.GenericEvent
}

// NeedLeaderElection makes the sampler run only on the elected leader.
func (s *Sampler) NeedLeaderElection() bool { return true }

// Start runs the sampling loop until ctx is cancelled.
func (s *Sampler) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("sampler")
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	// Sample once immediately so we begin accumulating without an initial delay.
	s.sampleOnce(ctx, log)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.sampleOnce(ctx, log)
		}
	}
}

func (s *Sampler) sampleOnce(ctx context.Context, log logr.Logger) {
	policies, err := s.listPolicies(ctx)
	if err != nil {
		log.Error(err, "list policies")
		return
	}

	usage, err := s.Source.ListUsage(ctx, s.Namespaces)
	if err != nil {
		metrics.DatasourceErrors.WithLabelValues(s.Source.Name()).Inc()
		log.Error(err, "datasource list usage", "source", s.Source.Name())
		return
	}
	byKey := make(map[sampling.Key]int64, len(usage))
	for _, u := range usage {
		byKey[sampling.Key{Namespace: u.Namespace, Pod: u.Pod, Container: u.Container}] = u.WorkingSet
	}

	now := s.Clock.Now()
	live := make(map[sampling.Key]struct{})
	ignored := 0
	monitored := 0
	for i := range policies {
		p := &policies[i]
		wl, err := restart.GetWorkload(ctx, s.Client, restart.Kind(p.Spec.WorkloadRef.Kind), p.Namespace, p.Spec.WorkloadRef.Name)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "resolve workload", "policy", p.Name, "namespace", p.Namespace)
			}
			continue
		}
		pods, err := listWorkloadPods(ctx, s.Client, wl)
		if err != nil {
			log.Error(err, "list workload pods", "policy", p.Name, "namespace", p.Namespace)
			continue
		}
		podCfg := config.ResolvePolicy(s.Defaults, p.Spec)
		enqueue := false
		for j := range pods {
			pod := &pods[j]
			monitored++
			targets, dropped := SelectTargets(pod, podCfg)
			ignored += dropped
			for _, tg := range targets {
				key := sampling.Key{Namespace: pod.Namespace, Pod: pod.Name, Container: tg.Name}
				ws, ok := byKey[key]
				if !ok {
					continue
				}
				live[key] = struct{}{}
				s.Store.Observe(key, sampling.Sample{Time: now, WorkingSet: ws, Limit: tg.LimitBytes}, restartCount(pod, tg.Name))
				if s.Store.WindowCovered(key, tg.Det.Window, now) {
					enqueue = true
				}
			}
		}
		if enqueue && s.Events != nil {
			s.Events <- event.GenericEvent{Object: policyRef(p)}
		}
	}

	metrics.PodsMonitored.Set(float64(monitored))
	kept := s.Store.Prune(live)
	metrics.SampleBufferSeries.Set(float64(kept))
	metrics.ContainersIgnored.WithLabelValues("no_limit").Set(float64(ignored))
}

// listPolicies returns the MemoryLeakPolicy objects within scope.
func (s *Sampler) listPolicies(ctx context.Context) ([]v1alpha1.MemoryLeakPolicy, error) {
	namespaces := s.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{""} // all namespaces within cache scope
	}
	var out []v1alpha1.MemoryLeakPolicy
	for _, ns := range namespaces {
		list := &v1alpha1.MemoryLeakPolicyList{}
		var opts []client.ListOption
		if ns != "" {
			opts = append(opts, client.InNamespace(ns))
		}
		if err := s.Client.List(ctx, list, opts...); err != nil {
			return nil, err
		}
		out = append(out, list.Items...)
	}
	return out, nil
}

// restartCount returns the container's restart count, checking both regular and
// init (native sidecar) container statuses.
func restartCount(pod *corev1.Pod, container string) int32 {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == container {
			return pod.Status.ContainerStatuses[i].RestartCount
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == container {
			return pod.Status.InitContainerStatuses[i].RestartCount
		}
	}
	return 0
}

// policyRef returns a minimal MemoryLeakPolicy object carrying identity for enqueue.
func policyRef(p *v1alpha1.MemoryLeakPolicy) *v1alpha1.MemoryLeakPolicy {
	out := &v1alpha1.MemoryLeakPolicy{}
	out.Namespace = p.Namespace
	out.Name = p.Name
	return out
}
