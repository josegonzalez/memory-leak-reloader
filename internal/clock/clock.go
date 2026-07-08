// Package clock provides a minimal injectable clock so time-dependent logic
// (sampling windows, cooldowns, maintenance windows, circuit breakers) can be
// tested deterministically without real sleeps.
package clock

import (
	"sync"
	"time"
)

// Clock is the subset of time used by the controller.
type Clock interface {
	Now() time.Time
}

// Real is the production clock backed by time.Now.
type Real struct{}

// Now returns the current time.
func (Real) Now() time.Time { return time.Now() }

// Fake is a controllable clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake returns a Fake clock set to t.
func NewFake(t time.Time) *Fake { return &Fake{t: t} }

// Now returns the fake clock's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Set moves the fake clock to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}
