package main

import (
	"context"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/josegonzalez/memory-leak-reloader/internal/gate"
	appmetrics "github.com/josegonzalez/memory-leak-reloader/internal/metrics"
	"github.com/josegonzalez/memory-leak-reloader/internal/restart"
)

// expirer periodically releases in-flight rollout slots that should no longer be
// held, so a stuck rollout (or one an external deploy superseded) cannot
// permanently consume global capacity. Each pass sweeps the policies: an
// in-flight rollout whose pod-template version diverged from the dispatched one
// (or whose workload is gone) is closed as Superseded, while one held past the
// timeout is closed as TimedOut. It is leader-gated.
type expirer struct {
	g        *gate.Gate
	state    *restart.Store
	timeout  time.Duration
	interval time.Duration
}

func newExpirer(g *gate.Gate, state *restart.Store, timeout, interval time.Duration) *expirer {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &expirer{g: g, state: state, timeout: timeout, interval: interval}
}

func (e *expirer) NeedLeaderElection() bool { return true }

func (e *expirer) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("inflight-expirer")
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if released := e.g.ExpireOlderThan(now, e.timeout); len(released) > 0 {
				log.Info("released stuck in-flight rollout slots", "count", len(released), "keys", released)
				appmetrics.InflightRollouts.Set(float64(e.g.Inflight()))
			}
			if e.state != nil {
				keys, err := e.state.SweepInFlight(ctx, now, e.timeout)
				if err != nil {
					log.Info("sweep in-flight policy status failed (non-fatal)", "error", err.Error())
				}
				for _, k := range keys {
					e.g.Release(k)
				}
				if len(keys) > 0 {
					appmetrics.InflightRollouts.Set(float64(e.g.Inflight()))
				}
			}
		}
	}
}
