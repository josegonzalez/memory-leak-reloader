package config

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/josegonzalez/memory-leak-reloader/api/v1alpha1"
)

// Mode is the detection algorithm applied to a container's memory series.
type Mode string

const (
	ModeSustained Mode = "sustained"
	ModeTrend     Mode = "trend"
	ModeCombined  Mode = "combined"
)

// ParseMode validates a mode string (used for the controller-wide --mode flag;
// per-policy modes are enum-validated by the CRD schema).
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeSustained, ModeTrend, ModeCombined:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("invalid mode %q (want sustained|trend|combined)", s)
	}
}

// Detection holds the tunables that govern whether a single container is
// considered to be leaking. It exists both as the controller-wide default and,
// after resolution, as the effective per-container configuration.
type Detection struct {
	Mode              Mode
	ThresholdPercent  int                // percent of the container memory limit
	ThresholdAbsolute *resource.Quantity // optional hard cap; takes precedence over percent
	Window            time.Duration
	TrendMinGrowth    resource.Quantity // projected growth over one window for trend modes
}

// Defaults is the controller-wide configuration (from flags/Helm) used as the
// base for every workload before policy overrides are applied.
type Defaults struct {
	Detection
	SampleInterval time.Duration
	StartupGrace   time.Duration
	Cooldown       time.Duration
}

// PodConfig is the resolved configuration for a managed workload's pods:
// pod-scoped fields plus a base Detection that per-container resolution can
// override.
type PodConfig struct {
	Base         Detection
	StartupGrace time.Duration
	Cooldown     time.Duration

	// DryRun is the effective mode for the workload: true (the CRD default)
	// logs/notifies would-be restarts without acting.
	DryRun bool

	// Containers is the requested monitor set. Empty means "use default
	// selection"; the literal "*" element means all eligible containers.
	Containers []string

	// Overrides carry per-container detection overrides, applied by ForContainer.
	Overrides []v1alpha1.ContainerOverride

	// ProfileCapture is a tri-state override: nil = inherit controller
	// default, non-nil = explicit on/off.
	ProfileCapture *bool
	PprofPath      string

	// MaintenanceWindows is the policy's per-workload maintenance-window override
	// (built into maintenance.Windows by the caller to avoid an import cycle).
	MaintenanceWindows []v1alpha1.MaintenanceWindow

	// NotifyRoutes names notification routes to target for this workload (replaces
	// the default sinks). SlackChannel overrides the Slack channel (bot-token mode).
	NotifyRoutes []string
	SlackChannel string
}

// ResolvePolicy overlays a MemoryLeakPolicy spec on the controller defaults,
// returning the resolved PodConfig. Spec fields are schema-validated, so unlike
// the former annotation parsing this cannot fail: an unset field inherits the
// default.
func ResolvePolicy(d Defaults, spec v1alpha1.MemoryLeakPolicySpec) PodConfig {
	p := PodConfig{
		Base:               resolveDetection(d.Detection, spec.Detection),
		StartupGrace:       d.StartupGrace,
		Cooldown:           d.Cooldown,
		DryRun:             true,
		Containers:         spec.Containers,
		Overrides:          spec.ContainerOverrides,
		ProfileCapture:     spec.ProfileCapture,
		PprofPath:          spec.PprofPath,
		MaintenanceWindows: spec.MaintenanceWindows,
		NotifyRoutes:       spec.NotifyRoutes,
		SlackChannel:       spec.SlackChannel,
	}
	if spec.Cooldown != nil {
		p.Cooldown = spec.Cooldown.Duration
	}
	if spec.StartupGrace != nil {
		p.StartupGrace = spec.StartupGrace.Duration
	}
	// The API server defaults spec.dryRun to true; the nil check keeps
	// dry-run the fail-safe for objects built without server defaulting.
	if spec.DryRun != nil {
		p.DryRun = *spec.DryRun
	}
	return p
}

// ForContainer returns the effective Detection for a named container by applying
// any per-container override on top of the pod base.
func (p PodConfig) ForContainer(name string) Detection {
	for i := range p.Overrides {
		if p.Overrides[i].Name == name {
			return resolveDetection(p.Base, p.Overrides[i].Detection)
		}
	}
	return p.Base
}

// resolveDetection overlays a DetectionSpec on a base Detection; unset fields
// (zero value / nil pointer) inherit the base.
func resolveDetection(base Detection, s v1alpha1.DetectionSpec) Detection {
	d := base
	if s.Mode != "" {
		d.Mode = Mode(s.Mode)
	}
	if s.ThresholdPercent != 0 {
		d.ThresholdPercent = s.ThresholdPercent
	}
	if s.ThresholdAbsolute != nil {
		q := s.ThresholdAbsolute.DeepCopy()
		d.ThresholdAbsolute = &q
	}
	if s.Window != nil {
		d.Window = s.Window.Duration
	}
	if s.TrendMinGrowth != nil {
		d.TrendMinGrowth = s.TrendMinGrowth.DeepCopy()
	}
	return d
}
