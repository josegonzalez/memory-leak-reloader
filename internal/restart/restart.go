// Package restart resolves a pod's owning workload (Deployment, StatefulSet, or
// Argo Rollout), determines whether the pod belongs to the current revision and
// whether a rollout is already in progress, and dispatches a rollout restart
// using the correct mechanism per workload type. Argo Rollouts are handled via
// unstructured objects so the controller carries no hard dependency on the
// argo-rollouts module and degrades gracefully when the CRD is absent.
package restart

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

// Kind identifies the owning workload type.
type Kind string

const (
	KindDeployment  Kind = "Deployment"
	KindStatefulSet Kind = "StatefulSet"
	KindRollout     Kind = "Rollout"
)

// RolloutGVK is the Argo Rollouts Rollout GVK.
var RolloutGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout"}

// Kinds selects which workload types are eligible.
type Kinds struct {
	Deployments  bool
	StatefulSets bool
	Rollouts     bool
}

// Workload is a resolved owning workload plus the pod's ReplicaSet (for
// Deployment/Rollout). It exposes the revision/settle checks and the restart
// dispatch.
type Workload struct {
	Kind Kind
	Ref  types.NamespacedName

	dep     *appsv1.Deployment
	sts     *appsv1.StatefulSet
	rollout *unstructured.Unstructured
	rs      *appsv1.ReplicaSet // pod's ReplicaSet (Deployment/Rollout only)
	pod     *corev1.Pod
}

// ErrNoOwner indicates the pod has no eligible controlling workload.
var ErrNoOwner = fmt.Errorf("pod has no eligible owning workload")

// ErrNotAutoRestartable indicates the workload cannot be safely auto-restarted
// (e.g. a StatefulSet with updateStrategy OnDelete).
var ErrNotAutoRestartable = fmt.Errorf("workload is not auto-restartable")

// ResolveOwner walks ownerReferences from the pod to its controlling workload.
func ResolveOwner(ctx context.Context, c client.Client, pod *corev1.Pod, kinds Kinds) (*Workload, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil, ErrNoOwner
	}
	ns := pod.Namespace

	switch owner.Kind {
	case "StatefulSet":
		if !kinds.StatefulSets {
			return nil, ErrNoOwner
		}
		sts := &appsv1.StatefulSet{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: owner.Name}, sts); err != nil {
			return nil, err
		}
		return &Workload{Kind: KindStatefulSet, Ref: client.ObjectKeyFromObject(sts), sts: sts, pod: pod}, nil

	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: owner.Name}, rs); err != nil {
			return nil, err
		}
		rsOwner := metav1.GetControllerOf(rs)
		if rsOwner == nil {
			return nil, ErrNoOwner
		}
		switch {
		case rsOwner.Kind == "Deployment" && kinds.Deployments:
			dep := &appsv1.Deployment{}
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: rsOwner.Name}, dep); err != nil {
				return nil, err
			}
			return &Workload{Kind: KindDeployment, Ref: client.ObjectKeyFromObject(dep), dep: dep, rs: rs, pod: pod}, nil
		case rsOwner.Kind == "Rollout" && kinds.Rollouts:
			ro := &unstructured.Unstructured{}
			ro.SetGroupVersionKind(RolloutGVK)
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: rsOwner.Name}, ro); err != nil {
				return nil, err
			}
			return &Workload{Kind: KindRollout, Ref: types.NamespacedName{Namespace: ns, Name: rsOwner.Name}, rollout: ro, rs: rs, pod: pod}, nil
		}
		return nil, ErrNoOwner
	}
	return nil, ErrNoOwner
}

// GetWorkload fetches the live owning workload by kind/namespace/name and returns
// a Workload with only the kind object and Ref populated. That is enough for
// TemplateVersion and IsSettled (neither needs the pod or ReplicaSet); callers
// that need revision checks must use ResolveOwner instead. The API error
// (including NotFound) is returned so callers can distinguish a deleted workload
// from a transient failure.
func GetWorkload(ctx context.Context, c client.Client, kind Kind, namespace, name string) (*Workload, error) {
	ref := types.NamespacedName{Namespace: namespace, Name: name}
	switch kind {
	case KindDeployment:
		dep := &appsv1.Deployment{}
		if err := c.Get(ctx, ref, dep); err != nil {
			return nil, err
		}
		return &Workload{Kind: KindDeployment, Ref: ref, dep: dep}, nil
	case KindStatefulSet:
		sts := &appsv1.StatefulSet{}
		if err := c.Get(ctx, ref, sts); err != nil {
			return nil, err
		}
		return &Workload{Kind: KindStatefulSet, Ref: ref, sts: sts}, nil
	case KindRollout:
		ro := &unstructured.Unstructured{}
		ro.SetGroupVersionKind(RolloutGVK)
		if err := c.Get(ctx, ref, ro); err != nil {
			return nil, err
		}
		return &Workload{Kind: KindRollout, Ref: ref, rollout: ro}, nil
	}
	return nil, ErrNoOwner
}

// Key is the stable identity used for per-workload dedup/concurrency. Kind is
// prefixed so a Deployment and a Rollout of the same name never collide.
func (w *Workload) Key() string {
	return string(w.Kind) + "/" + w.Ref.String()
}

// WorkloadKey builds the same identity as Workload.Key() from raw fields, for
// callers (finalizer, expirer) that have only a workload reference.
func WorkloadKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// PodSelector returns the label selector matching the workload's pods, used to
// list the pods the controller samples and evaluates.
func (w *Workload) PodSelector() (labels.Selector, error) {
	switch w.Kind {
	case KindDeployment:
		return metav1.LabelSelectorAsSelector(w.dep.Spec.Selector)
	case KindStatefulSet:
		return metav1.LabelSelectorAsSelector(w.sts.Spec.Selector)
	case KindRollout:
		raw, found, err := unstructured.NestedMap(w.rollout.Object, "spec", "selector")
		if err != nil || !found {
			return nil, fmt.Errorf("rollout %s: spec.selector not found", w.Ref.Name)
		}
		ls := &metav1.LabelSelector{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, ls); err != nil {
			return nil, fmt.Errorf("rollout %s: parse selector: %w", w.Ref.Name, err)
		}
		return metav1.LabelSelectorAsSelector(ls)
	}
	return nil, ErrNoOwner
}

// IsCurrentRevision reports whether the pod belongs to the workload's current
// revision (not an old ReplicaSet being scaled down).
func (w *Workload) IsCurrentRevision() bool {
	switch w.Kind {
	case KindDeployment:
		return w.rs != nil && w.dep != nil &&
			w.rs.Annotations[config.AnnotationDeploymentRevision] != "" &&
			w.rs.Annotations[config.AnnotationDeploymentRevision] == w.dep.Annotations[config.AnnotationDeploymentRevision]
	case KindStatefulSet:
		return w.pod.Labels[config.LabelControllerRevHash] != "" &&
			w.pod.Labels[config.LabelControllerRevHash] == w.sts.Status.UpdateRevision
	case KindRollout:
		hash := w.rs.Labels[config.LabelRolloutPodTemplateHash]
		cur, _, _ := unstructured.NestedString(w.rollout.Object, "status", "currentPodHash")
		stable, _, _ := unstructured.NestedString(w.rollout.Object, "status", "stableRS")
		return hash != "" && (hash == cur || hash == stable)
	}
	return false
}

// IsSettled reports whether the workload is fully rolled out (no rollout in
// progress). Returns ErrNotAutoRestartable for OnDelete StatefulSets.
func (w *Workload) IsSettled() (bool, error) {
	switch w.Kind {
	case KindDeployment:
		return deploymentSettled(w.dep), nil
	case KindStatefulSet:
		if w.sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
			return false, ErrNotAutoRestartable
		}
		return statefulSetSettled(w.sts), nil
	case KindRollout:
		return rolloutSettled(w.rollout), nil
	}
	return false, ErrNoOwner
}

func deploymentSettled(d *appsv1.Deployment) bool {
	if d.Status.ObservedGeneration < d.Generation {
		return false
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return d.Status.UpdatedReplicas == desired &&
		d.Status.Replicas == desired &&
		d.Status.AvailableReplicas == desired &&
		d.Status.UnavailableReplicas == 0
}

func statefulSetSettled(s *appsv1.StatefulSet) bool {
	if s.Status.ObservedGeneration < s.Generation {
		return false
	}
	if s.Status.CurrentRevision != s.Status.UpdateRevision {
		return false
	}
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	return s.Status.UpdatedReplicas == desired &&
		s.Status.Replicas == desired &&
		s.Status.ReadyReplicas == desired
}

func rolloutSettled(ro *unstructured.Unstructured) bool {
	gen := ro.GetGeneration()
	observed, _, _ := unstructured.NestedInt64(ro.Object, "status", "observedGeneration")
	if observed != 0 && observed < gen {
		return false
	}
	phase, _, _ := unstructured.NestedString(ro.Object, "status", "phase")
	return phase == "Healthy"
}

// Dispatch triggers a rollout restart using the per-kind mechanism. The
// cooldown/circuit-breaker state lives in the MemoryLeakPolicy status, so
// Dispatch only stamps the restart trigger. now is the timestamp written into
// the restart annotation/field.
func (w *Workload) Dispatch(ctx context.Context, c client.Client, now time.Time) error {
	stamp := now.UTC().Format(time.RFC3339)
	switch w.Kind {
	case KindDeployment:
		patch := client.MergeFrom(w.dep.DeepCopy())
		setTemplateRestartedAt(&w.dep.Spec.Template.Annotations, stamp)
		return c.Patch(ctx, w.dep, patch)
	case KindStatefulSet:
		patch := client.MergeFrom(w.sts.DeepCopy())
		setTemplateRestartedAt(&w.sts.Spec.Template.Annotations, stamp)
		return c.Patch(ctx, w.sts, patch)
	case KindRollout:
		patch := client.MergeFrom(w.rollout.DeepCopy())
		if err := unstructured.SetNestedField(w.rollout.Object, stamp, "spec", "restartAt"); err != nil {
			return err
		}
		return c.Patch(ctx, w.rollout, patch)
	}
	return ErrNoOwner
}

func setTemplateRestartedAt(ann *map[string]string, stamp string) {
	if *ann == nil {
		*ann = map[string]string{}
	}
	(*ann)[config.AnnotationRestartedAt] = stamp
}

// Object returns the underlying workload object (for annotation read/write and
// Event recording).
func (w *Workload) Object() client.Object {
	switch w.Kind {
	case KindDeployment:
		return w.dep
	case KindStatefulSet:
		return w.sts
	case KindRollout:
		return w.rollout
	}
	return nil
}
