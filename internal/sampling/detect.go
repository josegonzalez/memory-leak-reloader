// Package sampling holds the in-memory per-container memory time series (used
// when the datasource is metrics-server, which has no history) and the leak
// detectors that run over a series: sustained-threshold, trend/slope, and the
// combination of both.
package sampling

import (
	"time"

	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

// Sample is one memory observation for a container.
type Sample struct {
	Time       time.Time
	WorkingSet int64 // bytes
	Limit      int64 // container memory limit in bytes at sample time; 0 if unset
}

// Result is the outcome of evaluating a series against a Detection config.
type Result struct {
	Leaking   bool
	Reason    string // human-readable explanation, used in Events/logs
	Observed  int64  // most recent working-set bytes
	Threshold int64  // effective threshold bytes (0 when threshold is percent-of-limit and limit varies)
}

// exceeds reports whether a single sample is over its threshold. An absolute
// hard-cap takes precedence; otherwise the percent-of-limit applies (and a
// sample with no limit can never exceed, since such containers are dropped
// upstream and this is a defensive guard).
func exceeds(s Sample, d config.Detection) bool {
	if d.ThresholdAbsolute != nil {
		return s.WorkingSet >= d.ThresholdAbsolute.Value()
	}
	if s.Limit > 0 {
		return s.WorkingSet*100 >= s.Limit*int64(d.ThresholdPercent)
	}
	return false
}

// thresholdBytes returns the effective threshold for the most recent sample,
// for reporting. Returns 0 when undefined.
func thresholdBytes(s Sample, d config.Detection) int64 {
	if d.ThresholdAbsolute != nil {
		return d.ThresholdAbsolute.Value()
	}
	if s.Limit > 0 {
		return s.Limit * int64(d.ThresholdPercent) / 100
	}
	return 0
}

// windowSamples returns the samples within [now-window, now], oldest first.
func windowSamples(samples []Sample, window time.Duration, now time.Time) []Sample {
	cutoff := now.Add(-window)
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if !s.Time.Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

// WindowCovered reports whether the samples span at least the full window,
// i.e. there is a sample at or before now-window. Detectors must not fire until
// the window is covered (the "warm-up" guard).
func WindowCovered(samples []Sample, window time.Duration, now time.Time) bool {
	if len(samples) < 2 {
		return false
	}
	return !samples[0].Time.After(now.Add(-window))
}

// Detect evaluates the series against the detection config as of now. It
// assumes samples are sorted oldest-first. A not-yet-warm series never leaks.
func Detect(samples []Sample, d config.Detection, now time.Time) Result {
	if len(samples) == 0 {
		return Result{}
	}
	last := samples[len(samples)-1]
	res := Result{Observed: last.WorkingSet, Threshold: thresholdBytes(last, d)}
	if !WindowCovered(samples, d.Window, now) {
		return res // warming up
	}
	win := windowSamples(samples, d.Window, now)
	if len(win) < 2 {
		return res
	}

	sustained := allExceed(win, d)
	leakingTrend, projected := trendLeaking(win, d)

	switch d.Mode {
	case config.ModeSustained:
		if sustained {
			res.Leaking = true
			res.Reason = "working set stayed above threshold for the full window"
		}
	case config.ModeTrend:
		if leakingTrend {
			res.Leaking = true
			res.Reason = trendReason(projected, d)
		}
	case config.ModeCombined:
		if sustained && leakingTrend {
			res.Leaking = true
			res.Reason = "working set sustained above threshold and trending upward"
		}
	}
	return res
}

func allExceed(win []Sample, d config.Detection) bool {
	for _, s := range win {
		if !exceeds(s, d) {
			return false
		}
	}
	return true
}

// trendLeaking runs an ordinary-least-squares regression of working set against
// time over the window and reports whether the projected growth across one
// window meets the configured minimum. Returns the projected growth in bytes.
func trendLeaking(win []Sample, d config.Detection) (bool, int64) {
	slope, ok := olsSlope(win)
	if !ok || slope <= 0 {
		return false, 0
	}
	projected := int64(slope * d.Window.Seconds())
	return projected >= d.TrendMinGrowth.Value(), projected
}

// olsSlope computes the least-squares slope (bytes per second) of working set
// over time. Returns false if the slope is undefined (zero time variance).
func olsSlope(win []Sample) (float64, bool) {
	n := float64(len(win))
	if n < 2 {
		return 0, false
	}
	t0 := win[0].Time
	var sumX, sumY, sumXY, sumXX float64
	for _, s := range win {
		x := s.Time.Sub(t0).Seconds()
		y := float64(s.WorkingSet)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0, false
	}
	return (n*sumXY - sumX*sumY) / denom, true
}

func trendReason(projected int64, d config.Detection) string {
	return "working set trending upward (projected growth over window exceeds " +
		d.TrendMinGrowth.String() + ")"
}
