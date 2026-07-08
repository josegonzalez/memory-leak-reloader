package sampling

import (
	"sync"
	"time"
)

// Key identifies a single monitored container's series.
type Key struct {
	Namespace string
	Pod       string
	Container string
}

// series is one container's bounded sample history plus the last-seen restart
// count (a bump means the container was restarted - e.g. OOMKilled - so the
// pre-restart samples are stale and the series is reset).
type series struct {
	samples      []Sample
	lastRestarts int32
}

// Store holds per-container series, bounds their memory, and evicts series for
// containers that no longer exist. It is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	data      map[Key]*series
	retention time.Duration // how far back to keep samples (>= detection window)
	maxLen    int           // hard cap on samples per series
}

// NewStore creates a Store. retention bounds the time kept; maxLen caps samples
// per series so a misbehaving clock or tiny interval cannot grow unbounded.
func NewStore(retention time.Duration, maxLen int) *Store {
	if maxLen < 2 {
		maxLen = 2
	}
	return &Store{
		data:      make(map[Key]*series),
		retention: retention,
		maxLen:    maxLen,
	}
}

// Observe records a sample for key. If restartCount increased since the last
// observation, the series is reset first so post-restart data is not mixed with
// pre-restart data. Samples older than the retention window are trimmed.
func (s *Store) Observe(key Key, sample Sample, restartCount int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ser := s.data[key]
	if ser == nil {
		ser = &series{lastRestarts: restartCount}
		s.data[key] = ser
	}
	if restartCount > ser.lastRestarts {
		ser.samples = nil
	}
	ser.lastRestarts = restartCount

	ser.samples = append(ser.samples, sample)

	// Drop the larger of (samples older than the retention window) and
	// (overflow past the hard cap) from the front in a single shift.
	cutoff := sample.Time.Add(-s.retention)
	drop := 0
	for drop < len(ser.samples) && ser.samples[drop].Time.Before(cutoff) {
		drop++
	}
	if over := len(ser.samples) - s.maxLen; over > drop {
		drop = over
	}
	if drop > 0 {
		ser.samples = append(ser.samples[:0], ser.samples[drop:]...)
	}
}

// WindowCovered reports whether key's series spans at least window as of now,
// without copying the series (the hot-path check the sampler runs per
// container per tick).
func (s *Store) WindowCovered(key Key, window time.Duration, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ser := s.data[key]
	if ser == nil || len(ser.samples) < 2 {
		return false
	}
	return !ser.samples[0].Time.After(now.Add(-window))
}

// Samples returns a copy of the series for key (oldest first), or nil.
func (s *Store) Samples(key Key) []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	ser := s.data[key]
	if ser == nil {
		return nil
	}
	out := make([]Sample, len(ser.samples))
	copy(out, ser.samples)
	return out
}

// Reset drops the series for key (e.g. after the controller acts on it).
func (s *Store) Reset(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Prune evicts series whose keys are not in live. Returns the number kept,
// which callers export as a cardinality metric.
func (s *Store) Prune(live map[Key]struct{}) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.data {
		if _, ok := live[k]; !ok {
			delete(s.data, k)
		}
	}
	return len(s.data)
}

// Len returns the number of tracked series.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}
