package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

func ctr(name, limit string) corev1.Container {
	c := corev1.Container{Name: name}
	if limit != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	return c
}

func sidecar(name, limit string) corev1.Container {
	c := ctr(name, limit)
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

func podWith(ann map[string]string, containers []corev1.Container, inits []corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", Annotations: ann},
		Spec:       corev1.PodSpec{Containers: containers, InitContainers: inits},
	}
}

func baseCfg() config.PodConfig {
	d := config.Defaults{Detection: config.Detection{Mode: config.ModeSustained, ThresholdPercent: 85, Window: 10 * time.Minute}}
	return config.ResolvePolicy(d, v1alpha1.MemoryLeakPolicySpec{})
}

func TestSelect_DefaultContainerAnnotation(t *testing.T) {
	pod := podWith(map[string]string{config.AnnotationDefaultContainer: "app"},
		[]corev1.Container{ctr("sidecar", "200Mi"), ctr("app", "100Mi")}, nil)
	targets, dropped := SelectTargets(pod, baseCfg())
	if dropped != 0 || len(targets) != 1 || targets[0].Name != "app" {
		t.Fatalf("targets=%v dropped=%d want single app", targets, dropped)
	}
}

func TestSelect_HighestLimitFallback(t *testing.T) {
	pod := podWith(nil, []corev1.Container{ctr("a", "100Mi"), ctr("b", "300Mi"), ctr("c", "200Mi")}, nil)
	targets, _ := SelectTargets(pod, baseCfg())
	if len(targets) != 1 || targets[0].Name != "b" {
		t.Fatalf("targets=%v want highest-limit b", targets)
	}
}

func TestSelect_HighestLimitTiebreakByName(t *testing.T) {
	pod := podWith(nil, []corev1.Container{ctr("zeta", "200Mi"), ctr("alpha", "200Mi")}, nil)
	targets, _ := SelectTargets(pod, baseCfg())
	if len(targets) != 1 || targets[0].Name != "alpha" {
		t.Fatalf("targets=%v want alpha (name tiebreak)", targets)
	}
}

func TestSelect_AllAndNoLimitDropped(t *testing.T) {
	cfg := baseCfg()
	cfg.Containers = []string{"*"}
	pod := podWith(nil, []corev1.Container{ctr("withlimit", "100Mi"), ctr("nolimit", "")}, nil)
	targets, dropped := SelectTargets(pod, cfg)
	if len(targets) != 1 || targets[0].Name != "withlimit" {
		t.Fatalf("targets=%v want only withlimit", targets)
	}
	if dropped != 1 {
		t.Fatalf("dropped=%d want 1", dropped)
	}
}

func TestSelect_NativeSidecarIncludedPlainInitExcluded(t *testing.T) {
	cfg := baseCfg()
	cfg.Containers = []string{"*"}
	pod := podWith(nil,
		[]corev1.Container{ctr("app", "100Mi")},
		[]corev1.Container{sidecar("logger", "50Mi"), ctr("setup", "50Mi")}, // setup = plain init
	)
	targets, _ := SelectTargets(pod, cfg)
	names := map[string]bool{}
	for _, tg := range targets {
		names[tg.Name] = true
	}
	if !names["app"] || !names["logger"] {
		t.Fatalf("want app and native sidecar logger, got %v", names)
	}
	if names["setup"] {
		t.Fatalf("plain init container 'setup' should be excluded")
	}
}

func TestSelect_AbsoluteCapMakesNoLimitMonitorable(t *testing.T) {
	cfg := baseCfg()
	cfg.Containers = []string{"capped"}
	cap := resource.MustParse("500Mi")
	cfg.Overrides = []v1alpha1.ContainerOverride{
		{Name: "capped", Detection: v1alpha1.DetectionSpec{ThresholdAbsolute: &cap}},
	}
	pod := podWith(nil, []corev1.Container{ctr("capped", "")}, nil)
	targets, dropped := SelectTargets(pod, cfg)
	if len(targets) != 1 || dropped != 0 {
		t.Fatalf("absolute cap should make a no-limit container monitorable: targets=%v dropped=%d", targets, dropped)
	}
}

func TestSelect_UnmonitorablePod(t *testing.T) {
	pod := podWith(nil, []corev1.Container{ctr("a", ""), ctr("b", "")}, nil)
	targets, dropped := SelectTargets(pod, baseCfg())
	if len(targets) != 0 {
		t.Fatalf("expected no targets, got %v", targets)
	}
	_ = dropped // default selection finds no limited container; pod is unmonitorable
}
