// Package gate provides the in-memory concurrency control for restarts: a
// per-workload in-flight set (so multiple leaking pods of one workload cause at
// most one restart) plus a global max-concurrent cap. The cluster's live
// rollout status remains the source of truth; this gate is a fast-path guard on
// the current leader and is reconciled from cluster state on leader start.
package gate

import (
	"sync"
	"time"
)

// Outcome explains an acquire attempt.
type Outcome string

const (
	Acquired   Outcome = "acquired"
	AlreadyInF Outcome = "in_progress" // this workload already has an in-flight restart
	CapReached Outcome = "cap"         // global max-concurrent reached
)

// Gate tracks in-flight restarts keyed by workload identity.
type Gate struct {
	mu       sync.Mutex
	cap      int
	inflight map[string]time.Time
}

// New creates a Gate with the given global cap (<=0 means unlimited).
func New(capacity int) *Gate {
	return &Gate{cap: capacity, inflight: make(map[string]time.Time)}
}

// TryAcquire reserves an in-flight slot for key. It returns Acquired on success;
// AlreadyInF if the workload already holds a slot; CapReached if the global cap
// is hit. The count-check-and-insert is atomic under the lock.
func (g *Gate) TryAcquire(key string, now time.Time) Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.inflight[key]; ok {
		return AlreadyInF
	}
	if g.cap > 0 && len(g.inflight) >= g.cap {
		return CapReached
	}
	g.inflight[key] = now
	return Acquired
}

// Release frees the slot held by key.
func (g *Gate) Release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.inflight, key)
}

// Holds reports whether key currently holds a slot.
func (g *Gate) Holds(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.inflight[key]
	return ok
}

// Inflight returns the number of slots currently held.
func (g *Gate) Inflight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inflight)
}

// ExpireOlderThan releases slots acquired before now-timeout, so a stuck or
// PDB-blocked rollout cannot permanently consume capacity. Returns released keys.
func (g *Gate) ExpireOlderThan(now time.Time, timeout time.Duration) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var released []string
	for k, t := range g.inflight {
		if now.Sub(t) >= timeout {
			delete(g.inflight, k)
			released = append(released, k)
		}
	}
	return released
}

// Reconcile resets the in-flight set to exactly the supplied active keys,
// preserving acquire times for keys already tracked. Used on leader start to
// rebuild state from live cluster rollouts.
func (g *Gate) Reconcile(active map[string]time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight = make(map[string]time.Time, len(active))
	for k, t := range active {
		g.inflight[k] = t
	}
}
