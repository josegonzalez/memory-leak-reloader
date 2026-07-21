package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/clock"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
	"github.com/josegonzalez/memory-leak-reloader/internal/datasource"
	"github.com/josegonzalez/memory-leak-reloader/internal/metrics"
	"github.com/josegonzalez/memory-leak-reloader/internal/sampling"
)

// noUsageSource is a datasource stub returning no container usage; the sampler
// gauges under test are driven by policy and pod listing, not usage.
type noUsageSource struct{}

func (noUsageSource) Name() string                { return "fake" }
func (noUsageSource) Probe(context.Context) error { return nil }
func (noUsageSource) ListUsage(context.Context, []string) ([]datasource.Usage, error) {
	return nil, nil
}

func samplerDeployment(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			},
		},
	}
}

func samplerPod(ns, name, app string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{"app": app},
			// Name the default container explicitly so it is selected for
			// monitoring; its missing memory limit then counts it as ignored.
			Annotations: map[string]string{config.AnnotationDefaultContainer: "app"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
	}
}

func samplerPolicy(ns, name, workload string, dryRun *bool) *v1alpha1.MemoryLeakPolicy {
	return &v1alpha1.MemoryLeakPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.MemoryLeakPolicySpec{
			WorkloadRef: v1alpha1.WorkloadRef{Kind: "Deployment", Name: workload},
			DryRun:      dryRun,
		},
	}
}

// TestSampleOnce_PolicyAndWorkloadGauges verifies the sampler's reset-then-set
// contract: policy counts and per-workload pod counts are exported each tick
// and series for deleted policies disappear on the next one.
func TestSampleOnce_PolicyAndWorkloadGauges(t *testing.T) {
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}

	enforce := false
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(
		samplerDeployment("team-a", "api"),
		samplerPod("team-a", "api-1", "api"),
		samplerPod("team-a", "api-2", "api"),
		samplerPolicy("team-a", "api", "api", &enforce),
		samplerDeployment("team-b", "web"),
		samplerPod("team-b", "web-1", "web"),
		samplerPolicy("team-b", "web", "web", nil), // nil dryRun resolves to true
	).Build()

	s := &Sampler{
		Client:   c,
		Source:   noUsageSource{},
		Store:    sampling.NewStore(time.Hour, 1000),
		Clock:    clock.Real{},
		Defaults: config.Defaults{},
	}
	ctx := context.Background()
	s.sampleOnce(ctx, logr.Discard())

	if got := testutil.ToFloat64(metrics.Policies.WithLabelValues("team-a", "false")); got != 1 {
		t.Errorf("policies{team-a,false} = %v want 1", got)
	}
	if got := testutil.ToFloat64(metrics.Policies.WithLabelValues("team-b", "true")); got != 1 {
		t.Errorf("policies{team-b,true} = %v want 1", got)
	}
	if got := testutil.CollectAndCount(metrics.Policies); got != 2 {
		t.Errorf("policies series = %d want 2", got)
	}
	if got := testutil.ToFloat64(metrics.PodsMonitored.WithLabelValues("team-a", "Deployment", "api")); got != 2 {
		t.Errorf("pods monitored{team-a} = %v want 2", got)
	}
	if got := testutil.ToFloat64(metrics.PodsMonitored.WithLabelValues("team-b", "Deployment", "web")); got != 1 {
		t.Errorf("pods monitored{team-b} = %v want 1", got)
	}
	// The pods' containers have no memory limit, so every container is ignored.
	if got := testutil.ToFloat64(metrics.ContainersIgnored.WithLabelValues("team-a", "Deployment", "api", "no_limit")); got != 2 {
		t.Errorf("containers ignored{team-a} = %v want 2", got)
	}

	// Deleting a policy drops its series on the next tick.
	if err := c.Delete(ctx, samplerPolicy("team-b", "web", "web", nil)); err != nil {
		t.Fatalf("delete policy: %v", err)
	}
	s.sampleOnce(ctx, logr.Discard())

	if got := testutil.CollectAndCount(metrics.Policies); got != 1 {
		t.Errorf("policies series after delete = %d want 1", got)
	}
	if got := testutil.CollectAndCount(metrics.PodsMonitored); got != 1 {
		t.Errorf("pods monitored series after delete = %d want 1", got)
	}
	if got := testutil.ToFloat64(metrics.Policies.WithLabelValues("team-a", "false")); got != 1 {
		t.Errorf("policies{team-a,false} after delete = %v want 1", got)
	}
}
