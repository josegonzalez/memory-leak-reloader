package sampling

import (
	"testing"
	"time"
)

func TestStore_RestartCountResetsSeries(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewStore(time.Hour, 100)
	k := Key{"ns", "pod", "c"}

	s.Observe(k, Sample{Time: now, WorkingSet: 100, Limit: 1000}, 0)
	s.Observe(k, Sample{Time: now.Add(time.Minute), WorkingSet: 200, Limit: 1000}, 0)
	if got := len(s.Samples(k)); got != 2 {
		t.Fatalf("len = %d want 2", got)
	}
	// Restart count bump -> series resets, only the new sample remains.
	s.Observe(k, Sample{Time: now.Add(2 * time.Minute), WorkingSet: 50, Limit: 1000}, 1)
	got := s.Samples(k)
	if len(got) != 1 || got[0].WorkingSet != 50 {
		t.Fatalf("after restart bump = %+v want single 50-byte sample", got)
	}
}

func TestStore_TrimsByRetention(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewStore(10*time.Minute, 100)
	k := Key{"ns", "pod", "c"}
	for i := 0; i < 20; i++ {
		s.Observe(k, Sample{Time: now.Add(time.Duration(i) * time.Minute), WorkingSet: int64(i), Limit: 1000}, 0)
	}
	got := s.Samples(k)
	// Last sample at +19m, retention 10m -> keep samples with Time >= +9m => 11 samples.
	if len(got) != 11 {
		t.Fatalf("retained %d samples want 11", len(got))
	}
	if got[0].WorkingSet != 9 {
		t.Fatalf("oldest retained = %d want 9", got[0].WorkingSet)
	}
}

func TestStore_MaxLenCap(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewStore(time.Hour, 5)
	k := Key{"ns", "pod", "c"}
	for i := 0; i < 20; i++ {
		s.Observe(k, Sample{Time: now.Add(time.Duration(i) * time.Second), WorkingSet: int64(i), Limit: 1000}, 0)
	}
	if got := len(s.Samples(k)); got != 5 {
		t.Fatalf("len = %d want 5 (capped)", got)
	}
}

func TestStore_WindowCovered(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewStore(time.Hour, 100)
	k := Key{"ns", "pod", "c"}
	window := 10 * time.Minute

	// Empty / single-sample series are never covered.
	if s.WindowCovered(k, window, now) {
		t.Fatal("empty series should not be covered")
	}
	s.Observe(k, Sample{Time: now, WorkingSet: 1, Limit: 10}, 0)
	if s.WindowCovered(k, window, now) {
		t.Fatal("single sample should not be covered")
	}

	// Two samples that do not yet span the window.
	s.Observe(k, Sample{Time: now.Add(2 * time.Minute), WorkingSet: 1, Limit: 10}, 0)
	if s.WindowCovered(k, window, now.Add(2*time.Minute)) {
		t.Fatal("2 minutes of history should not cover a 10m window")
	}

	// An old enough oldest sample covers the window.
	s.Observe(k, Sample{Time: now.Add(12 * time.Minute), WorkingSet: 1, Limit: 10}, 0)
	if !s.WindowCovered(k, window, now.Add(12*time.Minute)) {
		t.Fatal("oldest sample older than now-window should be covered")
	}
}

func TestStore_RespectsBothBoundsTogether(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Tight cap and a retention window that are both active.
	s := NewStore(4*time.Minute, 3)
	k := Key{"ns", "pod", "c"}
	for i := 0; i < 10; i++ {
		s.Observe(k, Sample{Time: now.Add(time.Duration(i) * time.Minute), WorkingSet: int64(i), Limit: 100}, 0)
	}
	got := s.Samples(k)
	// Cap bounds length...
	if len(got) > 3 {
		t.Fatalf("len = %d, exceeds maxLen 3", len(got))
	}
	// ...and nothing older than the retention window survives.
	cutoff := now.Add(9 * time.Minute).Add(-4 * time.Minute)
	if got[0].Time.Before(cutoff) {
		t.Fatalf("oldest sample %v is older than retention cutoff %v", got[0].Time, cutoff)
	}
}

func TestStore_Prune(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewStore(time.Hour, 100)
	keep := Key{"ns", "pod", "keep"}
	drop := Key{"ns", "pod", "drop"}
	s.Observe(keep, Sample{Time: now, WorkingSet: 1, Limit: 10}, 0)
	s.Observe(drop, Sample{Time: now, WorkingSet: 1, Limit: 10}, 0)
	kept := s.Prune(map[Key]struct{}{keep: {}})
	if kept != 1 {
		t.Fatalf("kept = %d want 1", kept)
	}
	if s.Samples(drop) != nil {
		t.Fatalf("drop series should be evicted")
	}
}
