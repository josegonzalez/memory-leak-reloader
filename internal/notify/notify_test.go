package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func sampleEvent(t EventType) Event {
	return Event{
		Type: t, Kind: "Deployment", Workload: "web", Namespace: "ns",
		Container: "app", Mode: "sustained", Observed: 900, Threshold: 800, Reason: "leak",
	}
}

// recordingSink counts deliveries.
type recordingSink struct {
	name  string
	count int32
}

func (r *recordingSink) Name() string { return r.name }
func (r *recordingSink) Send(_ context.Context, _ Event) error {
	atomic.AddInt32(&r.count, 1)
	return nil
}

func TestNotifier_OnlyFiresEnabledTypes(t *testing.T) {
	sink := &recordingSink{name: "rec"}
	n := New([]Sink{sink}, nil, []EventType{EventRestartTriggered}, time.Second)

	n.Notify(context.Background(), sampleEvent(EventRestartTriggered))
	n.Notify(context.Background(), sampleEvent(EventRestartDeferred)) // not enabled
	if got := atomic.LoadInt32(&sink.count); got != 1 {
		t.Fatalf("deliveries = %d want 1 (only enabled type fires)", got)
	}
}

func TestNotifier_NilSafe(t *testing.T) {
	var n *Notifier
	n.Notify(context.Background(), sampleEvent(EventRestartTriggered)) // must not panic
}

func TestWebhookSink_PostsJSON(t *testing.T) {
	var gotAuth string
	var gotBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody = r.Header.Get("Content-Type") == "application/json"
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := WebhookSink{URL: srv.URL, AuthHeader: "Bearer t"}
	if err := s.Send(context.Background(), sampleEvent(EventRestartTriggered)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotAuth != "Bearer t" || !gotBody {
		t.Fatalf("auth=%q jsonContentType=%v", gotAuth, gotBody)
	}
}

func TestWebhookSink_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := WebhookSink{URL: srv.URL}
	if err := s.Send(context.Background(), sampleEvent(EventRestartTriggered)); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestEventTitleDryRun(t *testing.T) {
	e := sampleEvent(EventRestartTriggered)
	e.DryRun = true
	if got := e.Title(); got == "" || got[:9] != "[dry-run]" {
		t.Fatalf("dry-run title = %q", got)
	}
}
