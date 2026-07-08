package restart

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

func boolp(b bool) *bool    { return &b }
func int32p(i int32) *int32 { return &i }

func newClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
}

func deployment(rev string, gen int64, settled bool) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "ns", Generation: gen,
			Annotations: map[string]string{config.AnnotationDeploymentRevision: rev},
		},
		Spec: appsv1.DeploymentSpec{Replicas: int32p(1)},
	}
	if settled {
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: gen, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, UnavailableReplicas: 0,
		}
	} else {
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: gen, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1}
	}
	return d
}

func replicaSet(rev string, ownerDeploy string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc", Namespace: "ns",
			Annotations:     map[string]string{config.AnnotationDeploymentRevision: rev},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: ownerDeploy, Controller: boolp(true)}},
		},
	}
}

func podOwnedBy(kind, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc-1", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: name, Controller: boolp(true)}},
		},
	}
}

func TestGetWorkload(t *testing.T) {
	ctx := context.Background()

	// Deployment: the returned workload's TemplateVersion matches a directly-built
	// one, so the expirer can compare it against the dispatched version.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", UID: "u1"},
		Spec:       appsv1.DeploymentSpec{Template: imageTemplate("app:v1")},
	}
	wl, err := GetWorkload(ctx, newClient(dep), KindDeployment, "ns", "web")
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if wl.Kind != KindDeployment || wl.uid() != "u1" {
		t.Fatalf("workload = kind %s uid %s", wl.Kind, wl.uid())
	}
	if got, want := wl.TemplateVersion(), deployWorkload(imageTemplate("app:v1")).TemplateVersion(); got != want {
		t.Fatalf("version = %q want %q", got, want)
	}

	// StatefulSet resolves by kind too.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns"},
		Spec:       appsv1.StatefulSetSpec{Template: imageTemplate("db:v1")},
	}
	if wl, err := GetWorkload(ctx, newClient(sts), KindStatefulSet, "ns", "db"); err != nil || wl.Kind != KindStatefulSet {
		t.Fatalf("get statefulset: wl=%+v err=%v", wl, err)
	}

	// A missing workload surfaces NotFound so the caller can treat it as gone.
	if _, err := GetWorkload(ctx, newClient(), KindDeployment, "ns", "missing"); !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestWorkloadKey(t *testing.T) {
	dep := &Workload{Kind: KindDeployment, Ref: types.NamespacedName{Namespace: "ns", Name: "web"}}
	if got := dep.Key(); got != "Deployment/ns/web" {
		t.Errorf("Key() = %q want Deployment/ns/web", got)
	}
	// Kind prefix must keep a Deployment and a Rollout of the same name distinct.
	ro := &Workload{Kind: KindRollout, Ref: types.NamespacedName{Namespace: "ns", Name: "web"}}
	if dep.Key() == ro.Key() {
		t.Errorf("Deployment and Rollout keys collided: %q", dep.Key())
	}
}

// settledDeploymentFixture builds a settled Deployment+ReplicaSet+Pod on a fake
// client and resolves the owning workload (the common setup for the
// happy-path Deployment tests).
func settledDeploymentFixture(t *testing.T) (client.Client, *Workload) {
	t.Helper()
	dep := deployment("2", 1, true)
	rs := replicaSet("2", "web")
	pod := podOwnedBy("ReplicaSet", "web-abc")
	c := newClient(dep, rs, pod)
	wl, err := ResolveOwner(context.Background(), c, pod, Kinds{Deployments: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return c, wl
}

func TestResolveOwner_Deployment(t *testing.T) {
	_, wl := settledDeploymentFixture(t)
	if wl.Kind != KindDeployment || wl.Ref.Name != "web" {
		t.Fatalf("got kind=%s name=%s", wl.Kind, wl.Ref.Name)
	}
	if !wl.IsCurrentRevision() {
		t.Errorf("expected current revision (rs rev == deployment rev)")
	}
	settled, err := wl.IsSettled()
	if err != nil || !settled {
		t.Errorf("expected settled, got settled=%v err=%v", settled, err)
	}
}

func TestResolveOwner_OldRevisionNotCurrent(t *testing.T) {
	dep := deployment("3", 1, true)
	rs := replicaSet("2", "web") // older revision
	pod := podOwnedBy("ReplicaSet", "web-abc")
	c := newClient(dep, rs, pod)

	wl, err := ResolveOwner(context.Background(), c, pod, Kinds{Deployments: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if wl.IsCurrentRevision() {
		t.Errorf("pod from old RS revision should not be current")
	}
}

func TestDeploymentNotSettledWhenProgressing(t *testing.T) {
	dep := deployment("2", 1, false)
	rs := replicaSet("2", "web")
	pod := podOwnedBy("ReplicaSet", "web-abc")
	c := newClient(dep, rs, pod)
	wl, _ := ResolveOwner(context.Background(), c, pod, Kinds{Deployments: true})
	settled, _ := wl.IsSettled()
	if settled {
		t.Errorf("deployment mid-rollout should not be settled")
	}
}

func TestStatefulSet_OnDeleteNotAutoRestartable(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns", Generation: 1},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       int32p(1),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, CurrentRevision: "r1", UpdateRevision: "r1", Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1},
	}
	pod := podOwnedBy("StatefulSet", "db")
	pod.Labels = map[string]string{config.LabelControllerRevHash: "r1"}
	c := newClient(sts, pod)
	wl, err := ResolveOwner(context.Background(), c, pod, Kinds{StatefulSets: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := wl.IsSettled(); err != ErrNotAutoRestartable {
		t.Fatalf("OnDelete should be not-auto-restartable, got %v", err)
	}
}

func TestDispatch_TriggersRestart(t *testing.T) {
	c, wl := settledDeploymentFixture(t)
	ctx := context.Background()

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := wl.Dispatch(ctx, c, now); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	got := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "web"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Template.Annotations[config.AnnotationRestartedAt] == "" {
		t.Errorf("restartedAt not set on pod template")
	}
}

func TestPodSelector(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: imageTemplate("app:v1"),
		},
	}
	wl, err := GetWorkload(context.Background(), newClient(dep), KindDeployment, "ns", "web")
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	sel, err := wl.PodSelector()
	if err != nil {
		t.Fatalf("selector: %v", err)
	}
	if !sel.Matches(labels.Set{"app": "web"}) {
		t.Errorf("selector should match app=web")
	}
	if sel.Matches(labels.Set{"app": "other"}) {
		t.Errorf("selector should not match app=other")
	}
}
