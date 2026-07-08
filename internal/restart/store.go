package restart

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

// Store reads and writes MemoryLeakPolicy status, the controller's durable
// per-workload state (circuit-breaker bookkeeping, in-flight rollout marker, and
// bounded restart history). The controller is the only writer of status.
type Store struct {
	Client client.Client
}

// NewStore returns a Store backed by c.
func NewStore(c client.Client) *Store { return &Store{Client: c} }

// Persist writes the policy's status subresource, retrying a status conflict
// once by re-reading and re-applying the desired status.
func (s *Store) Persist(ctx context.Context, p *v1alpha1.MemoryLeakPolicy) error {
	if err := s.Client.Status().Update(ctx, p); err != nil {
		if !apierrors.IsConflict(err) {
			return err
		}
		fresh := &v1alpha1.MemoryLeakPolicy{}
		if gerr := s.Client.Get(ctx, client.ObjectKeyFromObject(p), fresh); gerr != nil {
			return gerr
		}
		fresh.Status = p.Status
		return s.Client.Status().Update(ctx, fresh)
	}
	return nil
}

// SweepInFlight resolves every policy that has an in-flight rollout against the
// live workload and closes out the ones that no longer warrant holding a slot,
// returning the workload keys whose in-memory slots should be released:
//
//   - the live pod-template version diverges from the dispatched version (an
//     external rollout superseded ours), the workload is gone, or it was recreated
//     under a new UID -> recorded Superseded;
//   - the in-flight rollout was dispatched longer than timeout ago and is still
//     ours -> recorded TimedOut.
//
// A transient fetch error (e.g. the Argo Rollout CRD is absent) leaves the entry
// for the timeout path on a later pass rather than releasing it prematurely.
func (s *Store) SweepInFlight(ctx context.Context, now time.Time, timeout time.Duration) ([]string, error) {
	list := &v1alpha1.MemoryLeakPolicyList{}
	if err := s.Client.List(ctx, list); err != nil {
		return nil, err
	}
	var keys []string
	for i := range list.Items {
		p := &list.Items[i]
		if p.Status.InFlight == nil {
			continue
		}
		outcome, release := s.classifyInFlight(ctx, p, now, timeout)
		if !release {
			continue
		}
		CompleteInFlight(&p.Status, now, outcome)
		key := workloadKey(p.Spec.WorkloadRef.Kind, p.Namespace, p.Spec.WorkloadRef.Name)
		keys = append(keys, key)
		if err := s.Persist(ctx, p); err != nil {
			// Best-effort: the in-memory slot is still released so a status write
			// that races cannot pin global capacity.
			return keys, err
		}
	}
	return keys, nil
}

// classifyInFlight decides how (if at all) to close out an in-flight rollout. It
// reports the terminal outcome and whether the slot should be released.
func (s *Store) classifyInFlight(ctx context.Context, p *v1alpha1.MemoryLeakPolicy, now time.Time, timeout time.Duration) (outcome string, release bool) {
	ref := p.Spec.WorkloadRef
	wl, err := GetWorkload(ctx, s.Client, Kind(ref.Kind), p.Namespace, ref.Name)
	switch {
	case apierrors.IsNotFound(err):
		// The workload is gone; its dispatched rollout can never settle.
		return v1alpha1.OutcomeSuperseded, true
	case err != nil:
		// Transient (or CRD-absent) failure: fall back to the age-based path so a
		// genuinely stuck rollout is still eventually released.
		return s.timedOut(p, now, timeout)
	}
	if wl.uid() != p.Status.WorkloadUID {
		// Deleted and recreated under the same name: the old rollout is moot.
		return v1alpha1.OutcomeSuperseded, true
	}
	dispatched := p.Status.InFlight.DispatchedVersion
	if live := wl.TemplateVersion(); dispatched != "" && live != "" && live != dispatched {
		// An external rollout moved the pod-template version off the one we
		// dispatched; the controller's restart was superseded.
		return v1alpha1.OutcomeSuperseded, true
	}
	return s.timedOut(p, now, timeout)
}

func (s *Store) timedOut(p *v1alpha1.MemoryLeakPolicy, now time.Time, timeout time.Duration) (outcome string, release bool) {
	if now.Sub(p.Status.InFlight.DispatchedAt.Time) >= timeout {
		return v1alpha1.OutcomeTimedOut, true
	}
	return "", false
}

// workloadKey matches Workload.Key() (Kind/namespace/name).
func workloadKey(kind, namespace, name string) string {
	return WorkloadKey(kind, namespace, name)
}

// UID returns the workload object's UID (empty if unset).
func (w *Workload) UID() types.UID {
	if obj := w.Object(); obj != nil {
		return obj.GetUID()
	}
	return ""
}

func (w *Workload) uid() types.UID { return w.UID() }
