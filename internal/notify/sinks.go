package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func defaultClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// doPostJSON marshals body and POSTs it as JSON. The caller owns the response
// body (must close it). Shared by postJSON and the Slack bot sink.
func doPostJSON(ctx context.Context, client *http.Client, url string, body any, headers map[string]string) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

func postJSON(ctx context.Context, client *http.Client, url string, body any, headers map[string]string) error {
	resp, err := doPostJSON(ctx, client, url, body, headers)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s returned HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// SlackSink posts to a Slack incoming webhook.
type SlackSink struct {
	WebhookURL string
	Client     *http.Client
}

func (s SlackSink) Name() string { return "slack" }

func (s SlackSink) Send(ctx context.Context, e Event) error {
	blocks, fallback := buildSlackBlocks(e)
	payload := map[string]any{"text": fallback, "blocks": blocks}
	return postJSON(ctx, defaultClient(s.Client), s.WebhookURL, payload, nil)
}

// WebhookSink posts the full event as JSON to a generic endpoint, with an
// optional Authorization header.
type WebhookSink struct {
	URL        string
	AuthHeader string // value for the Authorization header; empty to omit
	Client     *http.Client
}

func (s WebhookSink) Name() string { return "webhook" }

func (s WebhookSink) Send(ctx context.Context, e Event) error {
	headers := map[string]string{}
	if s.AuthHeader != "" {
		headers["Authorization"] = s.AuthHeader
	}
	return postJSON(ctx, defaultClient(s.Client), s.URL, webhookPayloadFor(e), headers)
}

// webhookPayload is the stable JSON contract for the generic webhook sink. It is
// derived from the internal Event so the wire format is explicit (clean keys,
// both raw and human-readable sizes, no internal routing fields).
type webhookPayload struct {
	Type           string `json:"type"`
	WorkloadKind   string `json:"workloadKind"`
	Workload       string `json:"workload"`
	Namespace      string `json:"namespace"`
	Container      string `json:"container"`
	Mode           string `json:"mode"`
	ObservedBytes  int64  `json:"observedBytes"`
	ThresholdBytes int64  `json:"thresholdBytes"`
	Observed       string `json:"observed"`
	Threshold      string `json:"threshold"`
	Window         string `json:"window"`
	Reason         string `json:"reason"`
	DryRun         bool   `json:"dryRun"`
	Time           string `json:"time"`
}

func webhookPayloadFor(e Event) webhookPayload {
	p := webhookPayload{
		Type:           string(e.Type),
		WorkloadKind:   e.Kind,
		Workload:       e.Workload,
		Namespace:      e.Namespace,
		Container:      e.Container,
		Mode:           e.Mode,
		ObservedBytes:  e.Observed,
		ThresholdBytes: e.Threshold,
		Observed:       humanizeBytes(e.Observed),
		Threshold:      thresholdText(e.Threshold),
		Reason:         e.Reason,
		DryRun:         e.DryRun,
	}
	if e.Window > 0 {
		p.Window = e.Window.String()
	}
	if !e.Time.IsZero() {
		p.Time = e.Time.UTC().Format(time.RFC3339)
	}
	return p
}

// SlackBotSink posts via the Slack Web API chat.postMessage using a bot token,
// which (unlike a classic incoming webhook) allows targeting a channel per call.
// The per-pod channel comes from the Event; otherwise DefaultChannel is used.
type SlackBotSink struct {
	Token          string
	DefaultChannel string
	Client         *http.Client
}

func (s SlackBotSink) Name() string { return "slack" }

func (s SlackBotSink) Send(ctx context.Context, e Event) error {
	channel := e.SlackChannel
	if channel == "" {
		channel = s.DefaultChannel
	}
	if channel == "" {
		return fmt.Errorf("slack bot sink: no channel (set memreload.io/slack-channel or a default channel)")
	}
	blocks, fallback := buildSlackBlocks(e)
	payload := map[string]any{"channel": channel, "text": fallback, "blocks": blocks}
	return slackBotPost(ctx, defaultClient(s.Client), s.Token, payload)
}

// slackBotPost calls chat.postMessage. Slack returns HTTP 200 even on logical
// failures, so the response body's "ok" field must be checked.
func slackBotPost(ctx context.Context, client *http.Client, token string, body any) error {
	resp, err := doPostJSON(ctx, client, "https://slack.com/api/chat.postMessage", body,
		map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack chat.postMessage returned HTTP %d", resp.StatusCode)
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	if !r.OK {
		return fmt.Errorf("slack chat.postMessage error: %s", r.Error)
	}
	return nil
}

// DatadogEventSink posts to the Datadog Events API.
type DatadogEventSink struct {
	Site   string
	APIKey string
	Client *http.Client
}

func (s DatadogEventSink) Name() string { return "datadog" }

func (s DatadogEventSink) Send(ctx context.Context, e Event) error {
	site := s.Site
	if site == "" {
		site = "datadoghq.com"
	}
	alertType := "warning"
	if e.Type == EventCircuitBreakerTripped {
		alertType = "error"
	}
	payload := map[string]any{
		"title":      e.Title(),
		"text":       e.Body(),
		"alert_type": alertType,
		"tags": []string{
			"namespace:" + e.Namespace,
			"workload:" + e.Workload,
			"kind:" + e.Kind,
			"container:" + e.Container,
			"source:memory-leak-reloader",
		},
	}
	url := "https://api." + site + "/api/v1/events"
	return postJSON(ctx, defaultClient(s.Client), url, payload, map[string]string{"DD-API-KEY": s.APIKey})
}
