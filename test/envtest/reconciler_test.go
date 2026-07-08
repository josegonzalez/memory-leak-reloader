// Package envtest exercises the reconciler against a real (test) apiserver via
// controller-runtime envtest. It is skipped automatically when the envtest
// binaries are not installed (KUBEBUILDER_ASSETS unset / setup-envtest absent).
package envtest

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/clock"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
	"github.com/josegonzalez/memory-leak-reloader/internal/controller"
	"github.com/josegonzalez/memory-leak-reloader/internal/gate"
	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

const mib = 1024 * 1024

func boolp(b bool) *bool    { return &b }
func int32p(i int32) *int32 { return &i }

func startEnv(t *testing.T) (client.Client, func()) {
	t.Helper()
	if err := v1alpha1.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../charts/memory-leak-reloader/files"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil || cfg == nil {
		t.Skipf("envtest unavailable (run `make envtest` to install binaries): %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		_ = env.Stop()
		t.Fatalf("client: %v", err)
	}
	return c, func() { _ = env.Stop() }
}

// seedLeakingDeployment creates a settled, leaking Deployment+ReplicaSet+Pod and
// a MemoryLeakPolicy targeting it, then returns a reconciler wired with the
// sampling store and policy-status store. The policy is created with the
// finalizer already present so the first Reconcile does real work rather than
// just adding the finalizer.
func seedLeakingDeployment(t *testing.T, c client.Client, ns string) *controller.Reconciler {
	t.Helper()
	ctx := context.Background()

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create ns: %v", err)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: ns, Generation: 1,
			Annotations: map[string]string{config.AnnotationDeploymentRevision: "1"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			},
		},
	}
	if err := c.Create(ctx, dep); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("deploy status: %v", err)
	}

	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-rs", Namespace: ns,
			Annotations:     map[string]string{config.AnnotationDeploymentRevision: "1"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: dep.UID, Controller: boolp(true)}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: dep.Spec.Template,
		},
	}
	if err := c.Create(ctx, rs); err != nil {
		t.Fatalf("create rs: %v", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-rs-1", Namespace: ns,
			Labels:          map[string]string{"app": "api"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: rs.UID, Controller: boolp(true)}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "app",
			Image: "busybox",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")},
			},
		}}},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	pod.Status = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		StartTime:  &metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
	}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatalf("pod status: %v", err)
	}

	policy := &v1alpha1.MemoryLeakPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: ns,
			Finalizers: []string{controller.PolicyFinalizer},
		},
		Spec: v1alpha1.MemoryLeakPolicySpec{
			WorkloadRef: v1alpha1.WorkloadRef{Kind: "Deployment", Name: "api"},
		},
	}
	if err := c.Create(ctx, policy); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// Seed the store with a leaking, window-covered series for the container.
	now := time.Now()
	store := sampling.NewStore(time.Hour, 1000)
	key := sampling.Key{Namespace: ns, Pod: "api-rs-1", Container: "app"}
	for i := 0; i <= 12; i++ {
		store.Observe(key, sampling.Sample{
			Time: now.Add(time.Duration(-12+i) * time.Minute), WorkingSet: int64(95 * mib), Limit: int64(100 * mib),
		}, 0)
	}

	return &controller.Reconciler{
		Client:               c,
		Clock:                clock.Real{},
		Store:                store,
		State:                restart.NewStore(c),
		Recorder:             events.NewFakeRecorder(64),
		Defaults:             config.Defaults{Detection: config.Detection{Mode: config.ModeSustained, ThresholdPercent: 85, Window: 10 * time.Minute}, StartupGrace: 5 * time.Minute, Cooldown: 30 * time.Minute},
		Kinds:                restart.Kinds{Deployments: true},
		Gate:                 gate.New(1),
		RestartWindow:        24 * time.Hour,
		MaxRestartsPerWindow: 3,
		RequeueAfter:         30 * time.Second,
	}
}

func policyRequest(ns string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "api"}}
}

func getState(t *testing.T, c client.Client, ns string) *v1alpha1.MemoryLeakPolicy {
	t.Helper()
	p := &v1alpha1.MemoryLeakPolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "api"}, p); err != nil {
		t.Fatalf("get MemoryLeakPolicy: %v", err)
	}
	return p
}

func TestReconcile_TriggersRestartOnLeak(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()
	const ns = "payments"

	r := seedLeakingDeployment(t, c, ns)

	res, err := r.Reconcile(ctx, policyRequest(ns))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected a settle-poll requeue after a restart")
	}

	got := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "api"}, got); err != nil {
		t.Fatalf("get deploy: %v", err)
	}
	if got.Spec.Template.Annotations[config.AnnotationRestartedAt] == "" {
		t.Fatalf("expected restartedAt annotation on the deployment pod template")
	}

	// The policy status records the restart: count 1, in-flight, one in-progress
	// history entry, and a stamped version.
	st := getState(t, c, ns).Status
	if st.RestartCount != 1 {
		t.Errorf("restartCount = %d want 1", st.RestartCount)
	}
	if st.InFlight == nil {
		t.Error("expected in-flight rollout recorded")
	}
	if st.Version == "" {
		t.Error("expected a stamped pod-template version")
	}
	if len(st.History) != 1 || st.History[0].Outcome != v1alpha1.OutcomeInProgress {
		t.Errorf("history = %+v", st.History)
	}
}

func TestReconcile_SettleCompletesHistory(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()
	const ns = "settle"

	r := seedLeakingDeployment(t, c, ns)

	// First pass triggers the restart (bumps the deployment generation).
	if _, err := r.Reconcile(ctx, policyRequest(ns)); err != nil {
		t.Fatalf("reconcile (trigger): %v", err)
	}

	// Simulate the deployment controller finishing the rollout: observedGeneration
	// catches up to the bumped generation.
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "api"}, dep); err != nil {
		t.Fatalf("get deploy: %v", err)
	}
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("settle deploy: %v", err)
	}

	// Second pass observes the settle, releases the slot, and completes history.
	if _, err := r.Reconcile(ctx, policyRequest(ns)); err != nil {
		t.Fatalf("reconcile (settle): %v", err)
	}

	st := getState(t, c, ns).Status
	if st.InFlight != nil {
		t.Error("in-flight should be cleared after settle")
	}
	if len(st.History) != 1 || st.History[0].Outcome != v1alpha1.OutcomeSettled || st.History[0].CompletedAt == nil {
		t.Fatalf("history not completed: %+v", st.History)
	}
}

// externalRollout simulates a deploy/ArgoCD sync by changing the pod-template
// image (which moves the fingerprint) and marking the deployment settled at its
// new generation.
func externalRollout(t *testing.T, c client.Client, ns, image string) {
	t.Helper()
	ctx := context.Background()
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "api"}, dep); err != nil {
		t.Fatalf("get deploy: %v", err)
	}
	dep.Spec.Template.Spec.Containers[0].Image = image
	if err := c.Update(ctx, dep); err != nil {
		t.Fatalf("external update: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "api"}, dep); err != nil {
		t.Fatalf("re-get deploy: %v", err)
	}
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("settle deploy: %v", err)
	}
}

func TestReconcile_SettleRecordsSupersededOnExternalRollout(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()
	const ns = "superseded"

	r := seedLeakingDeployment(t, c, ns)

	// First pass triggers our restart and holds the in-flight slot.
	if _, err := r.Reconcile(ctx, policyRequest(ns)); err != nil {
		t.Fatalf("reconcile (trigger): %v", err)
	}

	// An external rollout replaces the pod template before our restart settles.
	externalRollout(t, c, ns, "busybox:1.36")

	// Next pass observes a settled workload at a version other than the one we
	// dispatched: the in-flight is closed as Superseded, not Settled.
	if _, err := r.Reconcile(ctx, policyRequest(ns)); err != nil {
		t.Fatalf("reconcile (settle): %v", err)
	}

	st := getState(t, c, ns).Status
	if st.InFlight != nil {
		t.Error("in-flight should be cleared")
	}
	if len(st.History) != 1 || st.History[0].Outcome != v1alpha1.OutcomeSuperseded || st.History[0].CompletedAt == nil {
		t.Fatalf("history not marked superseded: %+v", st.History)
	}
	if r.Gate.Holds("Deployment/" + ns + "/api") {
		t.Error("slot should be released after the superseded rollout")
	}
}

func TestSweepInFlight_ReleasesSupersededSlot(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()
	const ns = "sweep"

	r := seedLeakingDeployment(t, c, ns)

	// Trigger a restart so a policy in-flight marker and a held slot exist.
	if _, err := r.Reconcile(ctx, policyRequest(ns)); err != nil {
		t.Fatalf("reconcile (trigger): %v", err)
	}
	wkey := "Deployment/" + ns + "/api"
	if !r.Gate.Holds(wkey) {
		t.Fatal("expected the in-flight slot to be held after trigger")
	}

	// An external rollout moves the pod-template version off the dispatched one.
	externalRollout(t, c, ns, "busybox:1.36")

	// A long timeout ensures only the version-mismatch path (not the age path) can
	// fire here.
	keys, err := r.State.SweepInFlight(ctx, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(keys) != 1 || keys[0] != wkey {
		t.Fatalf("released keys = %v want [%s]", keys, wkey)
	}

	st := getState(t, c, ns).Status
	if st.InFlight != nil {
		t.Error("in-flight should be cleared by the sweep")
	}
	if len(st.History) != 1 || st.History[0].Outcome != v1alpha1.OutcomeSuperseded {
		t.Fatalf("history not marked superseded: %+v", st.History)
	}
}
