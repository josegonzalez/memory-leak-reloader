package restart

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

func stateClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.MemoryLeakPolicy{}).
		WithObjects(objs...).Build()
}

func policy(name string, spec v1alpha1.MemoryLeakPolicySpec) *v1alpha1.MemoryLeakPolicy {
	return &v1alpha1.MemoryLeakPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       spec,
	}
}

func webPolicySpec() v1alpha1.MemoryLeakPolicySpec {
	return v1alpha1.MemoryLeakPolicySpec{WorkloadRef: v1alpha1.WorkloadRef{Kind: "Deployment", Name: "web"}}
}

func TestStorePersist(t *testing.T) {
	ctx := context.Background()
	p := policy("web", webPolicySpec())
	s := NewStore(stateClient(t, p))

	p.Status.RestartCount = 2
	if err := s.Persist(ctx, p); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got := &v1alpha1.MemoryLeakPolicy{}
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "web"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.RestartCount != 2 {
		t.Fatalf("persisted status lost: %d", got.Status.RestartCount)
	}
}

func liveDeployment(uid, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", UID: types.UID(uid)},
		Spec:       appsv1.DeploymentSpec{Template: imageTemplate(image)},
	}
}

func versionOf(dep *appsv1.Deployment) string {
	return (&Workload{Kind: KindDeployment, dep: dep}).TemplateVersion()
}

func inflightPolicy(uid, dispatchedVersion string, dispatchedAt metav1.Time) *v1alpha1.MemoryLeakPolicy {
	p := policy("web", webPolicySpec())
	p.Status = v1alpha1.MemoryLeakPolicyStatus{
		WorkloadUID: types.UID(uid),
		InFlight:    &v1alpha1.InFlightRollout{DispatchedAt: dispatchedAt, DispatchedVersion: dispatchedVersion},
		History:     []v1alpha1.RestartRecord{{TriggeredAt: dispatchedAt, Outcome: v1alpha1.OutcomeInProgress}},
	}
	return p
}

func TestStoreSweepInFlight(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recent := metav1.NewTime(now.Add(-time.Minute))
	old := metav1.NewTime(now.Add(-30 * time.Minute))
	timeout := 15 * time.Minute

	getPolicy := func(t *testing.T, s *Store) *v1alpha1.MemoryLeakPolicy {
		t.Helper()
		got := &v1alpha1.MemoryLeakPolicy{}
		if err := s.Client.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "web"}, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	mustSweep := func(t *testing.T, s *Store) []string {
		t.Helper()
		keys, err := s.SweepInFlight(ctx, now, timeout)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		return keys
	}
	assertReleased := func(t *testing.T, keys []string, outcome string, got *v1alpha1.MemoryLeakPolicy) {
		t.Helper()
		if len(keys) != 1 || keys[0] != "Deployment/ns/web" {
			t.Fatalf("released keys = %v", keys)
		}
		if got.Status.InFlight != nil {
			t.Error("in-flight not cleared")
		}
		if got.Status.History[0].Outcome != outcome || got.Status.History[0].CompletedAt == nil {
			t.Fatalf("history outcome = %+v want %q", got.Status.History[0], outcome)
		}
	}

	t.Run("version mismatch is superseded", func(t *testing.T) {
		dep := liveDeployment("u", "app:v2")
		s := NewStore(stateClient(t, dep, inflightPolicy("u", "stale-version", recent)))
		assertReleased(t, mustSweep(t, s), v1alpha1.OutcomeSuperseded, getPolicy(t, s))
	})

	t.Run("version match within timeout is left", func(t *testing.T) {
		dep := liveDeployment("u", "app:v1")
		s := NewStore(stateClient(t, dep, inflightPolicy("u", versionOf(dep), recent)))
		if keys := mustSweep(t, s); len(keys) != 0 {
			t.Fatalf("expected no release, got %v", keys)
		}
		if getPolicy(t, s).Status.InFlight == nil {
			t.Error("in-flight should be retained")
		}
	})

	t.Run("version match past timeout is timed out", func(t *testing.T) {
		dep := liveDeployment("u", "app:v1")
		s := NewStore(stateClient(t, dep, inflightPolicy("u", versionOf(dep), old)))
		assertReleased(t, mustSweep(t, s), v1alpha1.OutcomeTimedOut, getPolicy(t, s))
	})

	t.Run("missing workload is superseded", func(t *testing.T) {
		s := NewStore(stateClient(t, inflightPolicy("u", "v1", recent))) // no Deployment
		assertReleased(t, mustSweep(t, s), v1alpha1.OutcomeSuperseded, getPolicy(t, s))
	})

	t.Run("uid mismatch is superseded", func(t *testing.T) {
		dep := liveDeployment("u2", "app:v1") // recreated under a new UID
		s := NewStore(stateClient(t, dep, inflightPolicy("u", versionOf(dep), recent)))
		assertReleased(t, mustSweep(t, s), v1alpha1.OutcomeSuperseded, getPolicy(t, s))
	})
}
