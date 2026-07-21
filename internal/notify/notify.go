// Package notify delivers best-effort outbound notifications (Slack, generic
// webhook, Datadog Event) when the controller triggers, defers, or trips the
// circuit breaker on a restart. Delivery is time-boxed and never blocks
// remediation; failures are recorded as metrics.
package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/josegonzalez/memory-leak-reloader/internal/metrics"
)

// EventType is the kind of notification.
type EventType string

const (
	EventRestartTriggered      EventType = "RestartTriggered"
	EventCircuitBreakerTripped EventType = "CircuitBreakerTripped"
	EventRestartDeferred       EventType = "RestartDeferred"
)

// Event is the payload delivered to sinks.
type Event struct {
	Type      EventType
	Kind      string // workload kind
	Workload  string
	Namespace string
	Container string
	Mode      string
	Observed  int64
	Threshold int64
	Window    time.Duration
	Reason    string
	DryRun    bool
	Time      time.Time

	// Per-pod routing selectors (non-secret). Routes names a set of named routes
	// to target instead of the default sinks; SlackChannel overrides the channel
	// on a Slack bot sink in the target set.
	Routes       []string
	SlackChannel string
}

// Title renders a short human-readable headline.
func (e Event) Title() string {
	prefix := ""
	if e.DryRun {
		prefix = "[dry-run] would restart: "
	}
	return fmt.Sprintf("%s%s %s/%s (%s)", prefix, e.Type, e.Namespace, e.Workload, e.Kind)
}

// Body renders a detail string shared by all sinks.
func (e Event) Body() string {
	return fmt.Sprintf("container=%s mode=%s observed=%d threshold=%d reason=%q",
		e.Container, e.Mode, e.Observed, e.Threshold, e.Reason)
}

// Sink delivers a single Event.
type Sink interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// Notifier fans an Event out to the resolved target sinks. Targeting follows
// replace-with-fallback: a pod's named routes (or a Slack channel override)
// replace the default sinks for that pod; pods with neither use the defaults.
type Notifier struct {
	defaultSinks []Sink
	routes       map[string]Sink
	hasSlackBot  bool
	enabled      map[EventType]bool
	timeout      time.Duration
}

// New builds a Notifier from the default sink set and the named-route registry.
// events is the set of EventTypes that should fire; timeout bounds each delivery.
func New(defaultSinks []Sink, routes map[string]Sink, events []EventType, timeout time.Duration) *Notifier {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	en := make(map[EventType]bool, len(events))
	for _, e := range events {
		en[e] = true
	}
	hasBot := false
	for _, s := range defaultSinks {
		if _, ok := s.(SlackBotSink); ok {
			hasBot = true
		}
	}
	if routes == nil {
		routes = map[string]Sink{}
	}
	return &Notifier{defaultSinks: defaultSinks, routes: routes, hasSlackBot: hasBot, enabled: en, timeout: timeout}
}

// KnownRoute reports whether a route name is registered (callers Event/log on
// unknown names).
func (n *Notifier) KnownRoute(name string) bool {
	if n == nil {
		return false
	}
	_, ok := n.routes[name]
	return ok
}

// Notify delivers e to its resolved target sinks, best-effort. Delivery results
// are recorded in the notifications metric. Disabled event types are dropped.
func (n *Notifier) Notify(ctx context.Context, e Event) {
	if n == nil || !n.enabled[e.Type] {
		return
	}
	for _, s := range n.resolveTargets(e) {
		sctx, cancel := context.WithTimeout(ctx, n.timeout)
		err := s.Send(sctx, e)
		cancel()
		result := "success"
		if err != nil {
			result = "error"
		}
		metrics.Notifications.WithLabelValues(e.Namespace, e.Kind, e.Workload, s.Name(), result).Inc()
	}
}

// resolveTargets implements replace-with-fallback: named routes win; else a
// Slack channel override targets the Slack bot; else the default sinks.
func (n *Notifier) resolveTargets(e Event) []Sink {
	if len(e.Routes) > 0 {
		out := make([]Sink, 0, len(e.Routes))
		for _, name := range e.Routes {
			if s, ok := n.routes[name]; ok {
				out = append(out, s)
			}
		}
		return out
	}
	if e.SlackChannel != "" && n.hasSlackBot {
		for _, s := range n.defaultSinks {
			if _, ok := s.(SlackBotSink); ok {
				return []Sink{s}
			}
		}
	}
	return n.defaultSinks
}
