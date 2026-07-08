package restart

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

// ConditionRolloutInProgress is set True while a controller-triggered rollout is
// in flight and False once it settles.
const ConditionRolloutInProgress = "RolloutInProgress"

// Cause describes why a restart is being triggered, recorded in the audit trail.
type Cause struct {
	Container string
	Observed  int64
	Threshold int64
	Mode      string
}

// TemplateVersion returns a fingerprint of the workload's pod-template version.
// A change rolls the circuit-breaker window over ("fresh version = fresh
// breaker"). The source differs per kind so the fingerprint never moves on the
// controller's own restart:
//   - Deployment/StatefulSet stamp restartedAt into spec.template, so we hash the
//     template ourselves with that annotation removed.
//   - Rollout restarts via spec.restartAt and never touches spec.template, so
//     Argo's status.currentPodHash is safe to use directly (and also covers
//     Rollouts that use spec.workloadRef instead of an inline template).
func (w *Workload) TemplateVersion() string {
	switch w.Kind {
	case KindDeployment:
		if w.dep == nil {
			return ""
		}
		return fingerprintTemplate(&w.dep.Spec.Template)
	case KindStatefulSet:
		if w.sts == nil {
			return ""
		}
		return fingerprintTemplate(&w.sts.Spec.Template)
	case KindRollout:
		if w.rollout == nil {
			return ""
		}
		hash, _, _ := unstructured.NestedString(w.rollout.Object, "status", "currentPodHash")
		return hash
	}
	return ""
}

func fingerprintTemplate(tmpl *corev1.PodTemplateSpec) string {
	t := tmpl.DeepCopy()
	delete(t.Annotations, config.AnnotationRestartedAt)
	b, err := json.Marshal(t)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// sameWindow reports whether the persisted breaker window is still active at
// `now`: it has not aged out and the pod-template version has not changed. An
// empty stored version (legacy seed) is treated as a match so it never triggers
// a spurious reset.
func sameWindow(st v1alpha1.MemoryLeakPolicyStatus, curVersion string, now time.Time, window time.Duration) (count int, start time.Time, same bool) {
	if st.WindowStart == nil {
		return st.RestartCount, time.Time{}, false
	}
	start = st.WindowStart.Time
	if now.Sub(start) >= window {
		return st.RestartCount, start, false
	}
	if st.Version != "" && st.Version != curVersion {
		return st.RestartCount, start, false
	}
	return st.RestartCount, start, true
}

// BreakerTripped reports whether triggering another restart at `now` would
// exceed maxPerWindow within the current window (a version change resets it).
func BreakerTripped(st v1alpha1.MemoryLeakPolicyStatus, curVersion string, now time.Time, window time.Duration, maxPerWindow int) bool {
	if maxPerWindow <= 0 {
		return false
	}
	count, _, same := sameWindow(st, curVersion, now, window)
	if !same {
		return false
	}
	return count >= maxPerWindow
}

// InCooldown reports whether the workload was restarted within `cooldown` of now.
func InCooldown(st v1alpha1.MemoryLeakPolicyStatus, now time.Time, cooldown time.Duration) bool {
	if st.LastRestartAt == nil {
		return false
	}
	return now.Sub(st.LastRestartAt.Time) < cooldown
}

// ApplyRestart records a restart at `now` into the status: it rolls or extends
// the breaker window, stamps the version, appends an in-progress history record
// (bounded to RestartHistoryLimit), and sets the in-flight marker. The matching
// CompletedAt/terminal Outcome are filled in later by CompleteInFlight.
func ApplyRestart(st *v1alpha1.MemoryLeakPolicyStatus, w *Workload, cause Cause, now time.Time, window time.Duration) {
	ver := w.TemplateVersion()
	count, start, same := sameWindow(*st, ver, now, window)
	nowT := metav1.NewTime(now)
	if !same {
		st.RestartCount = 1
		st.WindowStart = &nowT
	} else {
		st.RestartCount = count + 1
		startT := metav1.NewTime(start)
		st.WindowStart = &startT
	}
	st.LastRestartAt = &nowT
	st.Version = ver

	st.History = append(st.History, v1alpha1.RestartRecord{
		TriggeredAt:   nowT,
		Container:     cause.Container,
		Observed:      cause.Observed,
		Threshold:     cause.Threshold,
		DetectionMode: cause.Mode,
		Version:       ver,
		Outcome:       v1alpha1.OutcomeInProgress,
	})
	if len(st.History) > v1alpha1.RestartHistoryLimit {
		st.History = st.History[len(st.History)-v1alpha1.RestartHistoryLimit:]
	}

	st.InFlight = &v1alpha1.InFlightRollout{
		DispatchedAt:      nowT,
		DispatchedVersion: ver,
		Phase:             v1alpha1.PhaseDispatched,
	}
	setCondition(st, ConditionRolloutInProgress, metav1.ConditionTrue, "Dispatched",
		"restart dispatched for container "+cause.Container, now)
}

// CompleteInFlight closes out the current in-flight rollout: it stamps the last
// history record's CompletedAt/Outcome (if still open) and clears the marker.
func CompleteInFlight(st *v1alpha1.MemoryLeakPolicyStatus, now time.Time, outcome string) {
	if n := len(st.History); n > 0 {
		last := &st.History[n-1]
		if last.CompletedAt == nil {
			t := metav1.NewTime(now)
			last.CompletedAt = &t
			last.Outcome = outcome
		}
	}
	st.InFlight = nil
	setCondition(st, ConditionRolloutInProgress, metav1.ConditionFalse, outcome,
		"rollout "+outcome, now)
}

// RecomputeObservability refreshes the human-facing status fields surfaced via
// printer columns. RestartsRemaining is clamped to >= 0 and left zero when the
// breaker is disabled (maxPerWindow <= 0).
func RecomputeObservability(st *v1alpha1.MemoryLeakPolicyStatus, now time.Time, cooldown, window time.Duration, maxPerWindow int) {
	tripped := false
	remaining := 0
	if maxPerWindow > 0 {
		if st.WindowStart != nil && now.Sub(st.WindowStart.Time) < window {
			tripped = st.RestartCount >= maxPerWindow
			if d := maxPerWindow - st.RestartCount; d > 0 {
				remaining = d
			}
		} else {
			remaining = maxPerWindow
		}
	}
	st.BreakerTripped = tripped
	st.RestartsRemaining = remaining
	if st.LastRestartAt != nil {
		ne := metav1.NewTime(st.LastRestartAt.Add(cooldown))
		st.NextEligibleRestart = &ne
	}
}

func setCondition(st *v1alpha1.MemoryLeakPolicyStatus, condType string, status metav1.ConditionStatus, reason, msg string, now time.Time) {
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(now),
	})
}

// AdoptWorkload records the observed workload UID and, if it changed (the
// workload was deleted and recreated under the same name), drops the stale
// breaker bookkeeping and in-flight marker so the new workload starts fresh.
func AdoptWorkload(st *v1alpha1.MemoryLeakPolicyStatus, uid types.UID) {
	if st.WorkloadUID != "" && st.WorkloadUID != uid {
		st.LastRestartAt = nil
		st.RestartCount = 0
		st.WindowStart = nil
		st.Version = ""
		st.InFlight = nil
		st.History = nil
	}
	st.WorkloadUID = uid
}
