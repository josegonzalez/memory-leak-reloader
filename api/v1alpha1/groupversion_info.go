// Package v1alpha1 defines the MemoryLeakPolicy custom resource: the
// user-authored opt-in and tuning for a single workload, carrying the
// controller's durable per-workload state (circuit-breaker bookkeeping,
// in-flight rollout tracking, a bounded restart history, observability fields,
// and diagnostic links) in its status.
// +kubebuilder:object:generate=true
// +groupName=memreload.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group/version for the MemoryLeakPolicy API.
var GroupVersion = schema.GroupVersion{Group: "memreload.io", Version: "v1alpha1"}

// SchemeBuilder registers the API types with a runtime.Scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds the MemoryLeakPolicy types to a scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &MemoryLeakPolicy{}, &MemoryLeakPolicyList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
