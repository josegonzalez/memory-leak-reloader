package config

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

func defaults() Defaults {
	return Defaults{
		Detection: Detection{
			Mode:             ModeSustained,
			ThresholdPercent: 85,
			Window:           10 * time.Minute,
			TrendMinGrowth:   resource.MustParse("100Mi"),
		},
		SampleInterval: 30 * time.Second,
		StartupGrace:   5 * time.Minute,
		Cooldown:       30 * time.Minute,
	}
}

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func qty(s string) *resource.Quantity { q := resource.MustParse(s); return &q }

func TestResolvePolicy_Overrides(t *testing.T) {
	spec := v1alpha1.MemoryLeakPolicySpec{
		Detection: v1alpha1.DetectionSpec{
			Mode:              "combined",
			ThresholdPercent:  70,
			ThresholdAbsolute: qty("500Mi"),
			Window:            dur(15 * time.Minute),
			TrendMinGrowth:    qty("200Mi"),
		},
		Cooldown:       dur(time.Hour),
		StartupGrace:   dur(2 * time.Minute),
		Containers:     []string{"app", "worker"},
		ProfileCapture: func() *bool { b := true; return &b }(),
		PprofPath:      "/debug/pprof/heap",
	}
	p := ResolvePolicy(defaults(), spec)
	if p.Base.Mode != ModeCombined {
		t.Errorf("mode = %v want combined", p.Base.Mode)
	}
	if p.Base.ThresholdPercent != 70 {
		t.Errorf("percent = %d want 70", p.Base.ThresholdPercent)
	}
	if p.Base.ThresholdAbsolute == nil || p.Base.ThresholdAbsolute.String() != "500Mi" {
		t.Errorf("absolute = %v want 500Mi", p.Base.ThresholdAbsolute)
	}
	if p.Base.Window != 15*time.Minute {
		t.Errorf("window = %v want 15m", p.Base.Window)
	}
	if p.Cooldown != time.Hour {
		t.Errorf("cooldown = %v want 1h", p.Cooldown)
	}
	if p.StartupGrace != 2*time.Minute {
		t.Errorf("startupGrace = %v want 2m", p.StartupGrace)
	}
	if len(p.Containers) != 2 || p.Containers[0] != "app" || p.Containers[1] != "worker" {
		t.Errorf("containers = %v want [app worker]", p.Containers)
	}
	if p.ProfileCapture == nil || !*p.ProfileCapture {
		t.Errorf("profileCapture = %v want true", p.ProfileCapture)
	}
}

func TestResolvePolicy_Defaults(t *testing.T) {
	p := ResolvePolicy(defaults(), v1alpha1.MemoryLeakPolicySpec{})
	if p.Base.Mode != ModeSustained || p.Base.ThresholdPercent != 85 {
		t.Errorf("defaults not applied: %+v", p.Base)
	}
	if p.Base.ThresholdAbsolute != nil {
		t.Errorf("absolute should be unset by default")
	}
	if p.Cooldown != 30*time.Minute || p.StartupGrace != 5*time.Minute {
		t.Errorf("duration defaults not applied: cooldown=%v grace=%v", p.Cooldown, p.StartupGrace)
	}
}

func TestForContainer_PerContainerOverride(t *testing.T) {
	spec := v1alpha1.MemoryLeakPolicySpec{
		Detection: v1alpha1.DetectionSpec{ThresholdPercent: 80},
		ContainerOverrides: []v1alpha1.ContainerOverride{
			{Name: "worker", Detection: v1alpha1.DetectionSpec{ThresholdPercent: 70, Mode: "trend"}},
		},
	}
	p := ResolvePolicy(defaults(), spec)
	app := p.ForContainer("app")
	if app.ThresholdPercent != 80 {
		t.Errorf("app percent = %d want 80 (pod base)", app.ThresholdPercent)
	}
	worker := p.ForContainer("worker")
	if worker.ThresholdPercent != 70 {
		t.Errorf("worker percent = %d want 70 (override)", worker.ThresholdPercent)
	}
	if worker.Mode != ModeTrend {
		t.Errorf("worker mode = %v want trend (override)", worker.Mode)
	}
}

func TestResolvePolicy_NotificationRouting(t *testing.T) {
	spec := v1alpha1.MemoryLeakPolicySpec{
		NotifyRoutes: []string{"team-payments", "sre-slack"},
		SlackChannel: "C0123ABC",
	}
	p := ResolvePolicy(defaults(), spec)
	if len(p.NotifyRoutes) != 2 || p.NotifyRoutes[0] != "team-payments" || p.NotifyRoutes[1] != "sre-slack" {
		t.Errorf("notify routes = %v want [team-payments sre-slack]", p.NotifyRoutes)
	}
	if p.SlackChannel != "C0123ABC" {
		t.Errorf("slack channel = %q want C0123ABC", p.SlackChannel)
	}
}
