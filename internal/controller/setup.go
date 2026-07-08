package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

// SetupWithManager registers the reconciler. It watches MemoryLeakPolicy objects
// and a channel fed by the sampling runnable so detection runs as new samples
// arrive (the sampler enqueues the policy whose workload's window is covered).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, policyEvents <-chan event.GenericEvent) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("memory-leak-reloader").
		For(&v1alpha1.MemoryLeakPolicy{})

	if policyEvents != nil {
		b = b.WatchesRawSource(source.Channel(policyEvents, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}
