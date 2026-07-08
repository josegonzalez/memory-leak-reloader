package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countingSink records how many events it received.
type countingSink struct {
	name string
	n    int32
}

func (c *countingSink) Name() string                          { return c.name }
func (c *countingSink) Send(_ context.Context, _ Event) error { atomic.AddInt32(&c.n, 1); return nil }

func allEvents() []EventType {
	return []EventType{EventRestartTriggered, EventCircuitBreakerTripped, EventRestartDeferred}
}

func TestResolve_NoSelectorUsesDefaults(t *testing.T) {
	def := &countingSink{name: "default"}
	route := &countingSink{name: "route"}
	n := New([]Sink{def}, map[string]Sink{"team-a": route}, allEvents(), time.Second)

	n.Notify(context.Background(), sampleEvent(EventRestartTriggered))
	if def.n != 1 || route.n != 0 {
		t.Fatalf("no selector should hit default only: default=%d route=%d", def.n, route.n)
	}
}

func TestResolve_RoutesReplaceDefaults(t *testing.T) {
	def := &countingSink{name: "default"}
	a := &countingSink{name: "a"}
	b := &countingSink{name: "b"}
	n := New([]Sink{def}, map[string]Sink{"team-a": a, "team-b": b}, allEvents(), time.Second)

	e := sampleEvent(EventRestartTriggered)
	e.Routes = []string{"team-b"}
	n.Notify(context.Background(), e)
	if def.n != 0 || a.n != 0 || b.n != 1 {
		t.Fatalf("routes should replace defaults: default=%d a=%d b=%d", def.n, a.n, b.n)
	}
}

func TestResolve_UnknownRouteSkipped(t *testing.T) {
	def := &countingSink{name: "default"}
	a := &countingSink{name: "a"}
	n := New([]Sink{def}, map[string]Sink{"team-a": a}, allEvents(), time.Second)
	if n.KnownRoute("nope") {
		t.Fatal("KnownRoute should be false for unregistered name")
	}
	e := sampleEvent(EventRestartTriggered)
	e.Routes = []string{"nope"} // reconciler filters unknowns; here it resolves to no targets
	n.Notify(context.Background(), e)
	if def.n != 0 || a.n != 0 {
		t.Fatalf("unknown route should hit nothing: default=%d a=%d", def.n, a.n)
	}
}

func TestResolve_SlackChannelTargetsBot(t *testing.T) {
	// Default set has a webhook and a slack bot; a channel-only override should
	// target just the bot.
	var botHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&botHits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	// Use a bot sink whose HTTP client points at our stub via a rewriting transport.
	bot := SlackBotSink{Token: "t", DefaultChannel: "Cdefault", Client: rewriteClient(srv.URL)}
	webhook := &countingSink{name: "webhook"}
	n := New([]Sink{webhook, bot}, nil, allEvents(), time.Second)

	e := sampleEvent(EventRestartTriggered)
	e.SlackChannel = "Coverride"
	n.Notify(context.Background(), e)

	if botHits != 1 || webhook.n != 0 {
		t.Fatalf("channel override should hit bot only: bot=%d webhook=%d", botHits, webhook.n)
	}
}

func TestSlackBotSink_ChannelAndOkCheck(t *testing.T) {
	var gotChannel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if c, ok := body["channel"].(string); ok {
			gotChannel = c
		}
		// Simulate Slack returning HTTP 200 with ok:false.
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer srv.Close()

	s := SlackBotSink{Token: "xoxb-1", DefaultChannel: "Cdef", Client: rewriteClient(srv.URL)}
	e := sampleEvent(EventRestartTriggered)
	e.SlackChannel = "C999"
	err := s.Send(context.Background(), e)
	if err == nil {
		t.Fatal("expected error when Slack returns ok:false")
	}
	if gotChannel != "C999" {
		t.Errorf("channel = %q want C999 (per-pod override)", gotChannel)
	}
	if gotAuth != "Bearer xoxb-1" {
		t.Errorf("auth = %q want bearer token", gotAuth)
	}
}

func TestSlackBotSink_NoChannel(t *testing.T) {
	s := SlackBotSink{Token: "t"} // no default channel, event has none
	if err := s.Send(context.Background(), sampleEvent(EventRestartTriggered)); err == nil {
		t.Fatal("expected error when no channel is available")
	}
}

func TestLoadRoutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	content := `[
		{"name":"team-a","type":"webhook","url":"https://a/hook","authHeader":"Bearer x"},
		{"name":"sre","type":"slack-webhook","url":"https://hooks.slack.com/services/x"}
	]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	routes, err := LoadRoutes(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := routes["team-a"].(WebhookSink); !ok {
		t.Errorf("team-a should be a WebhookSink, got %T", routes["team-a"])
	}
	if _, ok := routes["sre"].(SlackSink); !ok {
		t.Errorf("sre should be a SlackSink, got %T", routes["sre"])
	}
}

func TestLoadRoutes_MissingFileIsEmpty(t *testing.T) {
	routes, err := LoadRoutes(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || len(routes) != 0 {
		t.Fatalf("missing file should yield empty registry, got routes=%v err=%v", routes, err)
	}
}

func TestBuildRoutes_Errors(t *testing.T) {
	if _, err := BuildRoutes([]Route{{Name: "x", Type: "carrier-pigeon"}}); err == nil {
		t.Error("unknown route type should error")
	}
	if _, err := BuildRoutes([]Route{{Name: "a", Type: "webhook"}, {Name: "a", Type: "webhook"}}); err == nil {
		t.Error("duplicate route name should error")
	}
}

// rewriteClient returns an http.Client whose transport sends every request to
// target instead of the real host (so we can exercise the Slack bot sink).
func rewriteClient(target string) *http.Client {
	return &http.Client{Transport: rewriteTransport{target: target}}
}

type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := r.URL.Parse(rt.target)
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}
