// Package config resolves the effective per-pod and per-container configuration
// from controller defaults (Helm values / flags) overlaid with a workload's
// MemoryLeakPolicy spec. Spec fields are typed and schema-validated, so
// resolution is a straightforward "use the spec value, else the default".
package config

// ContainersAll is the sentinel element in a policy's container list selecting
// every eligible container in the pod.
const ContainersAll = "*"

// Standard Kubernetes annotations the controller consumes or writes.
const (
	AnnotationDefaultContainer   = "kubectl.kubernetes.io/default-container"
	AnnotationRestartedAt        = "kubectl.kubernetes.io/restartedAt"
	AnnotationDeploymentRevision = "deployment.kubernetes.io/revision"
)

// Well-known pod-template-hash / revision label keys used in owner resolution.
const (
	LabelPodTemplateHash        = "pod-template-hash"
	LabelControllerRevHash      = "controller-revision-hash"
	LabelRolloutPodTemplateHash = "rollouts-pod-template-hash"
)
