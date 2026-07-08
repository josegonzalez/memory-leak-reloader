package sampling

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

const mib = 1024 * 1024

func mustQty(s string) resource.Quantity { return resource.MustParse(s) }

// build a series of samples ending at `end`, spaced `interval` apart, with the
// given working-set values (oldest first) and a fixed limit.
func makeSeries(end time.Time, interval time.Duration, limit int64, vals ...int64) []Sample {
	out := make([]Sample, len(vals))
	start := end.Add(-time.Duration(len(vals)-1) * interval)
	for i, v := range vals {
		out[i] = Sample{Time: start.Add(time.Duration(i) * interval), WorkingSet: v, Limit: limit}
	}
	return out
}

func TestDetect_Sustained(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	det := config.Detection{Mode: config.ModeSustained, ThresholdPercent: 80, Window: window}
	limit := int64(100 * mib)

	tests := []struct {
		name    string
		vals    []int64
		leaking bool
	}{
		{"all above threshold", []int64{85 * mib, 90 * mib, 95 * mib}, true},
		{"dips below once", []int64{85 * mib, 70 * mib, 95 * mib}, false},
		{"all below", []int64{50 * mib, 55 * mib, 60 * mib}, false},
		{"exactly at threshold", []int64{80 * mib, 80 * mib, 80 * mib}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := makeSeries(now, 5*time.Minute, limit, tc.vals...)
			got := Detect(s, det, now)
			if got.Leaking != tc.leaking {
				t.Fatalf("Leaking=%v want %v (reason=%q)", got.Leaking, tc.leaking, got.Reason)
			}
		})
	}
}

func TestDetect_WarmupGuard(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	det := config.Detection{Mode: config.ModeSustained, ThresholdPercent: 50, Window: 10 * time.Minute}
	// Only 2 minutes of history for a 10m window -> not covered -> never leaks.
	s := makeSeries(now, 1*time.Minute, 100*mib, 90*mib, 90*mib, 90*mib)
	if got := Detect(s, det, now); got.Leaking {
		t.Fatalf("expected no leak during warm-up, got reason=%q", got.Reason)
	}
}

func TestDetect_AbsoluteCapBeatsPercent(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cap := mustQty("500Mi")
	det := config.Detection{
		Mode:              config.ModeSustained,
		ThresholdPercent:  99, // would not trip on percent
		ThresholdAbsolute: &cap,
		Window:            10 * time.Minute,
	}
	// Limit is huge so percent never trips, but absolute cap (500Mi) is exceeded.
	s := makeSeries(now, 5*time.Minute, 10000*mib, 600*mib, 600*mib, 600*mib)
	if got := Detect(s, det, now); !got.Leaking {
		t.Fatalf("expected leak via absolute cap, got none")
	}
}

func TestDetect_Trend(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	growth := mustQty("100Mi")
	det := config.Detection{Mode: config.ModeTrend, Window: window, TrendMinGrowth: growth}
	limit := int64(1000 * mib)

	// Rising ~ 30Mi/min over 10 min -> ~300Mi/window >= 100Mi -> leak.
	rising := makeSeries(now, 1*time.Minute, limit,
		100*mib, 130*mib, 160*mib, 190*mib, 220*mib, 250*mib, 280*mib, 310*mib, 340*mib, 370*mib, 400*mib)
	if got := Detect(rising, det, now); !got.Leaking {
		t.Fatalf("expected rising trend to leak")
	}

	// Flat -> no leak.
	flat := makeSeries(now, 1*time.Minute, limit,
		200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib, 200*mib)
	if got := Detect(flat, det, now); got.Leaking {
		t.Fatalf("expected flat series not to leak")
	}

	// Gentle rise below the min growth -> no leak (~5Mi/min -> 50Mi/window < 100Mi).
	gentle := makeSeries(now, 1*time.Minute, limit,
		200*mib, 205*mib, 210*mib, 215*mib, 220*mib, 225*mib, 230*mib, 235*mib, 240*mib, 245*mib, 250*mib)
	if got := Detect(gentle, det, now); got.Leaking {
		t.Fatalf("expected gentle rise below min-growth not to leak")
	}
}

func TestDetect_Combined(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	growth := mustQty("100Mi")
	limit := int64(1000 * mib)
	det := config.Detection{Mode: config.ModeCombined, ThresholdPercent: 80, Window: window, TrendMinGrowth: growth}

	// Rising but never above 80% (limit 1000Mi -> 800Mi threshold): trend yes, sustained no -> no leak.
	risingLow := makeSeries(now, 1*time.Minute, limit,
		100*mib, 130*mib, 160*mib, 190*mib, 220*mib, 250*mib, 280*mib, 310*mib, 340*mib, 370*mib, 400*mib)
	if got := Detect(risingLow, det, now); got.Leaking {
		t.Fatalf("combined: should not leak when not sustained above threshold")
	}

	// Rising AND above 80% throughout -> leak.
	risingHigh := makeSeries(now, 1*time.Minute, limit,
		820*mib, 840*mib, 860*mib, 880*mib, 900*mib, 920*mib, 940*mib, 960*mib, 980*mib, 1000*mib, 1020*mib)
	if got := Detect(risingHigh, det, now); !got.Leaking {
		t.Fatalf("combined: expected leak when sustained and trending")
	}
}
