package gate

import (
	"testing"
	"time"
)

func TestGate_DedupeAndCap(t *testing.T) {
	now := time.Now()
	g := New(2)

	if got := g.TryAcquire("a", now); got != Acquired {
		t.Fatalf("acquire a = %v want acquired", got)
	}
	if got := g.TryAcquire("a", now); got != AlreadyInF {
		t.Fatalf("re-acquire a = %v want in_progress", got)
	}
	if got := g.TryAcquire("b", now); got != Acquired {
		t.Fatalf("acquire b = %v want acquired", got)
	}
	if got := g.TryAcquire("c", now); got != CapReached {
		t.Fatalf("acquire c = %v want cap", got)
	}
	g.Release("a")
	if got := g.TryAcquire("c", now); got != Acquired {
		t.Fatalf("acquire c after release = %v want acquired", got)
	}
	if g.Inflight() != 2 {
		t.Fatalf("inflight = %d want 2", g.Inflight())
	}
}

func TestGate_Unlimited(t *testing.T) {
	now := time.Now()
	g := New(0)
	for _, k := range []string{"a", "b", "c", "d"} {
		if got := g.TryAcquire(k, now); got != Acquired {
			t.Fatalf("acquire %s = %v want acquired", k, got)
		}
	}
}

func TestGate_ExpireOlderThan(t *testing.T) {
	base := time.Now()
	g := New(5)
	g.TryAcquire("old", base)
	g.TryAcquire("new", base.Add(9*time.Minute))

	released := g.ExpireOlderThan(base.Add(10*time.Minute), 5*time.Minute)
	if len(released) != 1 || released[0] != "old" {
		t.Fatalf("released = %v want [old]", released)
	}
	if g.Holds("old") {
		t.Fatal("old should be released")
	}
	if !g.Holds("new") {
		t.Fatal("new should still be held")
	}
}

func TestGate_Reconcile(t *testing.T) {
	now := time.Now()
	g := New(5)
	g.TryAcquire("stale", now)
	g.Reconcile(map[string]time.Time{"live": now})
	if g.Holds("stale") {
		t.Fatal("stale should be cleared by reconcile")
	}
	if !g.Holds("live") {
		t.Fatal("live should be present after reconcile")
	}
}
