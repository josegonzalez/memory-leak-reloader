package restart

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

func statusWindow(count int, start time.Time, version string) v1alpha1.MemoryLeakPolicyStatus {
	s := metav1.NewTime(start)
	return v1alpha1.MemoryLeakPolicyStatus{RestartCount: count, WindowStart: &s, Version: version}
}

func deployWorkload(tmpl corev1.PodTemplateSpec) *Workload {
	return &Workload{
		Kind: KindDeployment,
		Ref:  types.NamespacedName{Namespace: "ns", Name: "w"},
		dep:  &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tmpl}},
	}
}

func imageTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
	}
}

func TestInCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	last := metav1.NewTime(now.Add(-10 * time.Minute))
	st := v1alpha1.MemoryLeakPolicyStatus{LastRestartAt: &last}
	if !InCooldown(st, now, 30*time.Minute) {
		t.Error("should be in cooldown (10m < 30m)")
	}
	if InCooldown(st, now, 5*time.Minute) {
		t.Error("should not be in cooldown (10m > 5m)")
	}
	if InCooldown(v1alpha1.MemoryLeakPolicyStatus{}, now, 30*time.Minute) {
		t.Error("empty status should not be in cooldown")
	}
}

func TestBreakerTripped(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const ver = "v1"

	// 3 restarts in current 24h window, version matches -> tripped at max=3.
	st := statusWindow(3, now.Add(-time.Hour), ver)
	if !BreakerTripped(st, ver, now, 24*time.Hour, 3) {
		t.Error("expected breaker tripped at count==max")
	}
	// Window rolled over -> not tripped.
	expired := statusWindow(5, now.Add(-48*time.Hour), ver)
	if BreakerTripped(expired, ver, now, 24*time.Hour, 3) {
		t.Error("expired window should reset breaker")
	}
	// Version changed within window -> reset, not tripped.
	if BreakerTripped(st, "v2", now, 24*time.Hour, 3) {
		t.Error("version change should reset breaker even at count==max")
	}
	// Empty stored version (legacy seed) -> treated as match, still trips.
	legacy := statusWindow(3, now.Add(-time.Hour), "")
	if !BreakerTripped(legacy, "v2", now, 24*time.Hour, 3) {
		t.Error("empty stored version should not reset (legacy)")
	}
	// Disabled breaker (max<=0) never trips.
	if BreakerTripped(st, ver, now, 24*time.Hour, 0) {
		t.Error("max<=0 should disable breaker")
	}
}

func TestApplyRestart(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wl := deployWorkload(imageTemplate("app:v1"))
	ver := wl.TemplateVersion()
	cause := Cause{Container: "app", Observed: 100, Threshold: 80, Mode: "sustained"}

	// Fresh -> count 1, new window, version stamped, in-flight + history set.
	st := v1alpha1.MemoryLeakPolicyStatus{}
	ApplyRestart(&st, wl, cause, now, 24*time.Hour)
	if st.RestartCount != 1 || st.WindowStart == nil || !st.WindowStart.Time.Equal(now) {
		t.Fatalf("fresh apply = %+v", st)
	}
	if st.Version != ver {
		t.Fatalf("version = %q want %q", st.Version, ver)
	}
	if st.InFlight == nil || st.InFlight.DispatchedVersion != ver {
		t.Fatalf("in-flight = %+v", st.InFlight)
	}
	if len(st.History) != 1 || st.History[0].Outcome != v1alpha1.OutcomeInProgress || st.History[0].CompletedAt != nil {
		t.Fatalf("history = %+v", st.History)
	}

	// Within window, same version -> increments, keeps window start.
	start := st.WindowStart.Time
	ApplyRestart(&st, wl, cause, now.Add(time.Hour), 24*time.Hour)
	if st.RestartCount != 2 || !st.WindowStart.Time.Equal(start) {
		t.Fatalf("within-window apply count=%d start=%v", st.RestartCount, st.WindowStart)
	}

	// Version change -> resets to count 1, new window.
	wl2 := deployWorkload(imageTemplate("app:v2"))
	ApplyRestart(&st, wl2, cause, now.Add(2*time.Hour), 24*time.Hour)
	if st.RestartCount != 1 || !st.WindowStart.Time.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("version-change apply = %+v want reset", st)
	}
}

func TestCompleteInFlight(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wl := deployWorkload(imageTemplate("app:v1"))
	st := v1alpha1.MemoryLeakPolicyStatus{}
	ApplyRestart(&st, wl, Cause{Container: "app"}, now, 24*time.Hour)

	CompleteInFlight(&st, now.Add(time.Minute), v1alpha1.OutcomeSettled)
	if st.InFlight != nil {
		t.Error("in-flight should be cleared")
	}
	rec := st.History[len(st.History)-1]
	if rec.Outcome != v1alpha1.OutcomeSettled || rec.CompletedAt == nil || !rec.CompletedAt.Time.Equal(now.Add(time.Minute)) {
		t.Fatalf("completed record = %+v", rec)
	}
}

func TestCompleteInFlight_Superseded(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wl := deployWorkload(imageTemplate("app:v1"))
	st := v1alpha1.MemoryLeakPolicyStatus{}
	ApplyRestart(&st, wl, Cause{Container: "app"}, now, 24*time.Hour)

	CompleteInFlight(&st, now.Add(time.Minute), v1alpha1.OutcomeSuperseded)
	if st.InFlight != nil {
		t.Error("in-flight should be cleared")
	}
	rec := st.History[len(st.History)-1]
	if rec.Outcome != v1alpha1.OutcomeSuperseded || rec.CompletedAt == nil {
		t.Fatalf("completed record = %+v", rec)
	}
}

func TestHistoryBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wl := deployWorkload(imageTemplate("app:v1"))
	st := v1alpha1.MemoryLeakPolicyStatus{}
	for i := 0; i < v1alpha1.RestartHistoryLimit+5; i++ {
		ApplyRestart(&st, wl, Cause{Container: "app"}, now.Add(time.Duration(i)*time.Minute), 24*time.Hour)
	}
	if len(st.History) != v1alpha1.RestartHistoryLimit {
		t.Fatalf("history len = %d want %d", len(st.History), v1alpha1.RestartHistoryLimit)
	}
}

func TestAdoptWorkload(t *testing.T) {
	old := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// First observation records the UID and leaves bookkeeping untouched.
	st := v1alpha1.MemoryLeakPolicyStatus{RestartCount: 3, LastRestartAt: &old, Version: "v1"}
	AdoptWorkload(&st, "uid-1")
	if st.WorkloadUID != "uid-1" || st.RestartCount != 3 {
		t.Fatalf("first adopt should record UID and keep state: %+v", st)
	}

	// Same UID: no reset.
	AdoptWorkload(&st, "uid-1")
	if st.RestartCount != 3 {
		t.Fatalf("same UID should not reset: %+v", st)
	}

	// Changed UID (delete + recreate): stale bookkeeping is dropped.
	st.InFlight = &v1alpha1.InFlightRollout{DispatchedVersion: "v1"}
	AdoptWorkload(&st, "uid-2")
	if st.WorkloadUID != "uid-2" || st.RestartCount != 0 || st.LastRestartAt != nil || st.Version != "" || st.InFlight != nil {
		t.Fatalf("UID change should reset bookkeeping: %+v", st)
	}
}

func TestTemplateVersion(t *testing.T) {
	// Deployment fingerprint flips on image change.
	v1 := deployWorkload(imageTemplate("app:v1")).TemplateVersion()
	v2 := deployWorkload(imageTemplate("app:v2")).TemplateVersion()
	if v1 == "" || v1 == v2 {
		t.Fatalf("image change should change version: %q vs %q", v1, v2)
	}

	// A restartedAt stamp in the template must NOT change the fingerprint - else
	// the breaker would reset on its own restart.
	stamped := imageTemplate("app:v1")
	stamped.Annotations = map[string]string{config.AnnotationRestartedAt: "2026-01-01T00:00:00Z"}
	if got := deployWorkload(stamped).TemplateVersion(); got != v1 {
		t.Fatalf("restartedAt stamp changed version: %q vs %q", got, v1)
	}

	// Rollout uses Argo's status.currentPodHash directly.
	ro := &Workload{Kind: KindRollout, rollout: &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"currentPodHash": "abc123"},
	}}}
	if got := ro.TemplateVersion(); got != "abc123" {
		t.Fatalf("rollout version = %q want abc123", got)
	}
}
