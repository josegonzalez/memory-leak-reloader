package notify

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHumanizeBytes(t *testing.T) {
	cases := map[int64]string{
		512:                    "512B",
		900 * 1024 * 1024:      "900Mi",
		1024 * 1024:            "1Mi",
		2 * 1024 * 1024 * 1024: "2Gi",
		1536 * 1024:            "1.5Mi",
	}
	for in, want := range cases {
		if got := humanizeBytes(in); got != want {
			t.Errorf("humanizeBytes(%d) = %q want %q", in, got, want)
		}
	}
}

func sampleFull() Event {
	return Event{
		Type: EventRestartTriggered, Kind: "Deployment", Workload: "api", Namespace: "payments",
		Container: "app", Mode: "sustained", Observed: 943718400, Threshold: 901775360,
		Window: 10 * time.Minute, Reason: "working set stayed above threshold for the full window",
		Time: time.Date(2026, 1, 1, 12, 0, 5, 0, time.UTC),
	}
}

func TestBuildSlackBlocks_Structure(t *testing.T) {
	blocks, fallback := buildSlackBlocks(sampleFull())
	if fallback == "" {
		t.Fatal("fallback text should be non-empty")
	}
	if len(blocks) != 5 {
		t.Fatalf("want 5 blocks (header, section, fields, context, divider), got %d", len(blocks))
	}
	if blocks[0]["type"] != "header" || blocks[4]["type"] != "divider" {
		t.Fatalf("unexpected block ordering: %v / %v", blocks[0]["type"], blocks[4]["type"])
	}
	// Fields section should carry humanized Observed/Threshold.
	fields, ok := blocks[2]["fields"].([]map[string]any)
	if !ok || len(fields) != 4 {
		t.Fatalf("fields section malformed: %#v", blocks[2])
	}
	if fields[0]["text"] != "*Observed*\n900Mi" {
		t.Errorf("observed field = %q", fields[0]["text"])
	}
	// Whole thing must marshal to JSON (valid Slack payload shape).
	if _, err := json.Marshal(map[string]any{"text": fallback, "blocks": blocks}); err != nil {
		t.Fatalf("blocks not JSON-marshalable: %v", err)
	}
}

func TestBuildSlackBlocks_DryRunHeader(t *testing.T) {
	e := sampleFull()
	e.DryRun = true
	blocks, _ := buildSlackBlocks(e)
	hdr := blocks[0]["text"].(map[string]any)["text"].(string)
	if hdr != ":mag: Would restart" {
		t.Errorf("dry-run header = %q want ':mag: Would restart'", hdr)
	}
}

func TestThresholdDashWhenZero(t *testing.T) {
	if got := thresholdText(0); got != "—" {
		t.Errorf("thresholdText(0) = %q want em-dash", got)
	}
}

func TestWebhookPayload_Shape(t *testing.T) {
	p := webhookPayloadFor(sampleFull())
	if p.Type != "RestartTriggered" || p.WorkloadKind != "Deployment" || p.Workload != "api" {
		t.Errorf("payload identity wrong: %+v", p)
	}
	if p.ObservedBytes != 943718400 || p.Observed != "900Mi" {
		t.Errorf("payload should carry raw + human observed: %+v", p)
	}
	if p.Window != "10m0s" {
		t.Errorf("window = %q want 10m0s", p.Window)
	}
	if p.Time != "2026-01-01T12:00:05Z" {
		t.Errorf("time = %q", p.Time)
	}
	// Confirm internal routing fields are not present in the wire format.
	b, _ := json.Marshal(p)
	for _, leaked := range []string{"Routes", "SlackChannel", "routes", "slackChannel"} {
		if containsKey(b, leaked) {
			t.Errorf("webhook payload leaked internal field %q: %s", leaked, b)
		}
	}
}

func containsKey(jsonBytes []byte, key string) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(jsonBytes, &m)
	_, ok := m[key]
	return ok
}
