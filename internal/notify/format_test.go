package notify

import (
	"encoding/json"
	"strings"
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

func TestBuildSlackBlocks_WithSamples(t *testing.T) {
	e := sampleFull()
	base := time.Date(2026, 1, 1, 11, 45, 0, 0, time.UTC)
	for i := 0; i < 16; i++ {
		e.Samples = append(e.Samples, SamplePoint{Time: base.Add(time.Duration(i) * time.Minute), Bytes: int64(700+i*16) * 1024 * 1024})
	}
	blocks, fallback := buildSlackBlocks(e)
	if len(blocks) != 6 {
		t.Fatalf("want 6 blocks with a chart section, got %d", len(blocks))
	}
	if blocks[3]["type"] != "section" {
		t.Fatalf("chart block should be a section: %#v", blocks[3])
	}
	text := blocks[3]["text"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "```\n") || !strings.HasSuffix(text, "\n```") {
		t.Errorf("chart should be fenced as a code block: %q", text)
	}
	if blocks[4]["type"] != "context" || blocks[5]["type"] != "divider" {
		t.Fatalf("context/divider should follow the chart: %v / %v", blocks[4]["type"], blocks[5]["type"])
	}
	// The fallback stays short: no chart in the accessibility text.
	if strings.Contains(fallback, "┤") {
		t.Errorf("fallback should not carry the chart: %q", fallback)
	}
	if _, err := json.Marshal(map[string]any{"text": fallback, "blocks": blocks}); err != nil {
		t.Fatalf("blocks not JSON-marshalable: %v", err)
	}
}

func TestBuildSlackBlocks_ClusterName(t *testing.T) {
	e := sampleFull()
	e.ClusterName = "prod-eu"
	blocks, fallback := buildSlackBlocks(e)
	summary := blocks[1]["text"].(map[string]any)["text"].(string)
	if !strings.Contains(summary, "on *prod-eu*") {
		t.Errorf("summary should name the cluster: %q", summary)
	}
	if !strings.Contains(fallback, "[prod-eu]") {
		t.Errorf("fallback should carry the cluster suffix: %q", fallback)
	}
	// The cluster rides the summary line; the 2x2 fields grid must not grow.
	if fields, ok := blocks[2]["fields"].([]map[string]any); !ok || len(fields) != 4 {
		t.Fatalf("fields section changed shape: %#v", blocks[2])
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
	e := sampleFull()
	e.Samples = []SamplePoint{{Time: e.Time, Bytes: e.Observed}}
	p := webhookPayloadFor(e)
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
	// Confirm internal routing fields and the chart series are not present in
	// the wire format, and clusterName is omitted when unset.
	b, _ := json.Marshal(p)
	for _, leaked := range []string{"Routes", "SlackChannel", "routes", "slackChannel", "clusterName", "Samples", "samples"} {
		if containsKey(b, leaked) {
			t.Errorf("webhook payload leaked internal field %q: %s", leaked, b)
		}
	}
}

func TestWebhookPayload_ClusterName(t *testing.T) {
	e := sampleFull()
	e.ClusterName = "prod-eu"
	p := webhookPayloadFor(e)
	if p.ClusterName != "prod-eu" {
		t.Errorf("clusterName = %q want prod-eu", p.ClusterName)
	}
	b, _ := json.Marshal(p)
	if !containsKey(b, "clusterName") {
		t.Errorf("marshaled payload missing clusterName: %s", b)
	}
}

func TestTitle_ClusterSuffix(t *testing.T) {
	e := sampleFull()
	if got := e.Title(); strings.Contains(got, "[") {
		t.Errorf("title without cluster should have no suffix: %q", got)
	}
	e.ClusterName = "prod-eu"
	e.DryRun = true
	got := e.Title()
	if !strings.HasPrefix(got, "[dry-run]") || !strings.HasSuffix(got, "[prod-eu]") {
		t.Errorf("dry-run prefix and cluster suffix should coexist: %q", got)
	}
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func TestDatadogEventPayload_StandardTags(t *testing.T) {
	e := sampleFull()
	e.ClusterName = "prod-eu"
	tags := datadogEventPayloadFor(e)["tags"].([]string)
	// Standard agent keys plus the kind-agnostic workload/kind pair.
	for _, want := range []string{
		"kube_namespace:payments",
		"kube_container_name:app",
		"kube_deployment:api",
		"kube_cluster_name:prod-eu",
		"workload:api",
		"kind:Deployment",
	} {
		if !hasTag(tags, want) {
			t.Errorf("tags missing %q: %v", want, tags)
		}
	}

	for _, tag := range datadogEventPayloadFor(sampleFull())["tags"].([]string) {
		if strings.HasPrefix(tag, "kube_cluster_name:") {
			t.Errorf("unset cluster should add no tag, got %q", tag)
		}
	}
}

func TestDatadogEventPayload_KindTags(t *testing.T) {
	sts := sampleFull()
	sts.Kind = "StatefulSet"
	tags := datadogEventPayloadFor(sts)["tags"].([]string)
	if !hasTag(tags, "kube_stateful_set:api") {
		t.Errorf("tags missing kube_stateful_set:api: %v", tags)
	}

	ro := sampleFull()
	ro.Kind = "Rollout"
	for _, tag := range datadogEventPayloadFor(ro)["tags"].([]string) {
		if strings.HasPrefix(tag, "kube_deployment:") || strings.HasPrefix(tag, "kube_stateful_set:") {
			t.Errorf("rollout should get no kind-specific tag, got %q", tag)
		}
	}
}

func containsKey(jsonBytes []byte, key string) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(jsonBytes, &m)
	_, ok := m[key]
	return ok
}
