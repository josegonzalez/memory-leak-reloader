package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
)

// listWorkloadPods lists the pods belonging to a workload using its pod
// selector. It reads through the given client, which is configured to bypass
// the cache for pods so only policy-referenced workloads' pods are fetched.
func listWorkloadPods(ctx context.Context, c client.Client, wl *restart.Workload) ([]corev1.Pod, error) {
	sel, err := wl.PodSelector()
	if err != nil {
		return nil, err
	}
	list := &corev1.PodList{}
	if err := c.List(ctx, list,
		client.InNamespace(wl.Ref.Namespace),
		client.MatchingLabelsSelector{Selector: sel},
	); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// podReady reports whether the pod's Ready condition is True.
func podReady(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podAge returns how long the pod has been running, preferring StartTime and
// falling back to the creation timestamp.
func (r *Reconciler) podAge(pod *corev1.Pod) time.Duration {
	start := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		start = pod.Status.StartTime.Time
	}
	if start.IsZero() {
		return 0
	}
	return r.Clock.Now().Sub(start)
}
