package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// scheme registration lives in groupversion_info.go (addKnownTypes).

// RestartHistoryLimit bounds the number of RestartRecord entries kept in status.
const RestartHistoryLimit = 10

// Restart outcomes recorded in RestartRecord.Outcome.
const (
	OutcomeInProgress = "InProgress"
	OutcomeSettled    = "Settled"
	OutcomeTimedOut   = "TimedOut"
	OutcomeFailed     = "Failed"
	// OutcomeSuperseded marks a controller-dispatched rollout that an external
	// rollout (a new image/spec from ArgoCD, a deploy, or kubectl apply) replaced
	// before it settled. The pod-template version diverging from the dispatched
	// version is the unambiguous signal, since the controller's own restart never
	// moves that version.
	OutcomeSuperseded = "Superseded"
)

// In-flight rollout phases.
const (
	PhaseDispatched = "Dispatched"
	PhaseSettling   = "Settling"
)

// Detection modes accepted in MemoryLeakPolicy detection config.
const (
	ModeSustained = "sustained"
	ModeTrend     = "trend"
	ModeCombined  = "combined"
)

// Day is a weekday abbreviation used in maintenance windows.
//
// +kubebuilder:validation:Enum=Mon;Tue;Wed;Thu;Fri;Sat;Sun
type Day string

// WorkloadRef identifies the workload a policy manages. The controller resolves
// it in the policy's own namespace. It is immutable: retarget by creating a new
// policy rather than repointing an existing one (which would strand its state).
type WorkloadRef struct {
	// +kubebuilder:validation:Enum=Deployment;StatefulSet;Rollout
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// DetectionSpec is the detection tuning for a policy or a per-container override.
// Unset fields inherit the controller defaults (Helm values / flags).
type DetectionSpec struct {
	// +kubebuilder:validation:Enum=sustained;trend;combined
	// +optional
	Mode string `json:"mode,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ThresholdPercent int `json:"thresholdPercent,omitempty"`
	// +optional
	ThresholdAbsolute *resource.Quantity `json:"thresholdAbsolute,omitempty"`
	// +optional
	Window *metav1.Duration `json:"window,omitempty"`
	// +optional
	TrendMinGrowth *resource.Quantity `json:"trendMinGrowth,omitempty"`
}

// ContainerOverride applies detection overrides to a single named container.
type ContainerOverride struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Detection DetectionSpec `json:"detection,omitempty"`
}

// MaintenanceWindow is a recurring same-day allowed interval. Start and End are
// "HH:MM" (Start < End; overnight windows are unsupported). Timezone is an IANA
// name (empty means UTC); its shape is validated by schema, but its existence is
// confirmed at runtime since OpenAPI/CEL have no timezone database.
//
// +kubebuilder:validation:XValidation:rule="self.start < self.end",message="start must be before end (overnight windows unsupported)"
type MaintenanceWindow struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=7
	// +listType=set
	Days []Day `json:"days"`
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +kubebuilder:validation:MaxLength=5
	Start string `json:"start"`
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +kubebuilder:validation:MaxLength=5
	End string `json:"end"`
	// +kubebuilder:validation:Pattern=`^(UTC|[A-Za-z]+(?:/[A-Za-z0-9_+-]+)+)$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// InFlightRollout records a restart the controller has dispatched but not yet
// observed settle. Persisting it makes leader failover deterministic.
type InFlightRollout struct {
	DispatchedAt      metav1.Time `json:"dispatchedAt"`
	DispatchedVersion string      `json:"dispatchedVersion"`
	// +optional
	Phase string `json:"phase,omitempty"` // Dispatched|Settling
}

// RestartRecord is one entry in the bounded restart audit trail.
type RestartRecord struct {
	TriggeredAt metav1.Time `json:"triggeredAt"`
	// CompletedAt is set when the rollout settles, fails, or times out.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Container   string       `json:"container"`
	// +optional
	Observed int64 `json:"observed,omitempty"` // observed value, in the detection mode's units
	// +optional
	Threshold int64 `json:"threshold,omitempty"` // threshold, same units as observed
	// +optional
	DetectionMode string `json:"detectionMode,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Outcome string `json:"outcome,omitempty"` // InProgress|Settled|TimedOut|Failed|Superseded
}

// ProfileRef links to the last heap profile captured before a restart.
type ProfileRef struct {
	// +optional
	URL        string      `json:"url,omitempty"`
	CapturedAt metav1.Time `json:"capturedAt"`
}

// NotificationRef records the last notification emitted, for failover-safe dedup.
type NotificationRef struct {
	// +optional
	Event      string      `json:"event,omitempty"`
	NotifiedAt metav1.Time `json:"notifiedAt"`
}

// MemoryLeakPolicySpec is the user-authored opt-in and tuning for one workload.
type MemoryLeakPolicySpec struct {
	// WorkloadRef is the managed workload (resolved in the policy's namespace).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="workloadRef is immutable"
	WorkloadRef WorkloadRef `json:"workloadRef"`

	// Detection is the policy-level detection tuning (per-container overrides in
	// ContainerOverrides take precedence).
	// +optional
	Detection DetectionSpec `json:"detection,omitempty"`

	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`
	// +optional
	StartupGrace *metav1.Duration `json:"startupGrace,omitempty"`

	// Containers is the requested monitor set. Empty means default selection; the
	// literal "*" element means all eligible containers.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Containers []string `json:"containers,omitempty"`

	// ContainerOverrides tune detection per named container.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	ContainerOverrides []ContainerOverride `json:"containerOverrides,omitempty"`

	// ProfileCapture is a tri-state override: nil = inherit the controller default.
	// +optional
	ProfileCapture *bool `json:"profileCapture,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	PprofPath string `json:"pprofPath,omitempty"`

	// MaintenanceWindows restricts restarts to these windows (empty = always).
	// +optional
	// +kubebuilder:validation:MaxItems=24
	MaintenanceWindows []MaintenanceWindow `json:"maintenanceWindows,omitempty"`

	// NotifyRoutes names notification routes to target (replaces the default
	// sinks). SlackChannel overrides the Slack channel (bot-token mode).
	// +optional
	// +kubebuilder:validation:MaxItems=32
	NotifyRoutes []string `json:"notifyRoutes,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	SlackChannel string `json:"slackChannel,omitempty"`
}

// MemoryLeakPolicyStatus is the controller-managed durable state (folded from the
// former RestartState). The controller is the only writer.
type MemoryLeakPolicyStatus struct {
	// WorkloadUID is the observed UID of the managed workload; a change (delete +
	// recreate under the same name) resets the persisted breaker bookkeeping.
	// +optional
	WorkloadUID types.UID `json:"workloadUID,omitempty"`

	// Circuit-breaker bookkeeping.
	// +optional
	LastRestartAt *metav1.Time `json:"lastRestartAt,omitempty"`
	// +optional
	RestartCount int `json:"restartCount,omitempty"` // within the current window
	// +optional
	WindowStart *metav1.Time `json:"windowStart,omitempty"`
	// +optional
	Version string `json:"version,omitempty"` // pod-template version at window start

	// In-flight rollout (deterministic leader failover).
	// +optional
	InFlight *InFlightRollout `json:"inFlight,omitempty"`

	// Computed observability, surfaced via printer columns.
	// +optional
	BreakerTripped bool `json:"breakerTripped,omitempty"`
	// +optional
	NextEligibleRestart *metav1.Time `json:"nextEligibleRestart,omitempty"`
	// +optional
	RestartsRemaining int `json:"restartsRemaining,omitempty"`

	// Diagnostic links.
	// +optional
	LastProfile *ProfileRef `json:"lastProfile,omitempty"`
	// +optional
	LastNotification *NotificationRef `json:"lastNotification,omitempty"`

	// Bounded audit trail, newest last, capped at RestartHistoryLimit.
	// +optional
	History []RestartRecord `json:"history,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// MemoryLeakPolicy opts a single workload into memory-leak detection and carries
// the controller's durable per-workload state in its status.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mlp
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.kind`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Count",type=integer,JSONPath=`.status.restartCount`
// +kubebuilder:printcolumn:name="Breaker",type=boolean,JSONPath=`.status.breakerTripped`
// +kubebuilder:printcolumn:name="Last-Restart",type=date,JSONPath=`.status.lastRestartAt`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
type MemoryLeakPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MemoryLeakPolicySpec   `json:"spec,omitempty"`
	Status MemoryLeakPolicyStatus `json:"status,omitempty"`
}

// MemoryLeakPolicyList is a list of MemoryLeakPolicy objects.
//
// +kubebuilder:object:root=true
type MemoryLeakPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MemoryLeakPolicy `json:"items"`
}
