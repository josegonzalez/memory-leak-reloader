package notify

import (
	"fmt"
	"strings"
	"time"
)

// buildSlackBlocks renders an Event as Slack Block Kit: header + summary section
// + a 2-column fields section + context + divider. It also returns a plain-text
// fallback used as the message `text` (notifications/accessibility).
func buildSlackBlocks(e Event) ([]map[string]any, string) {
	emoji, verb := slackHeader(e)

	blocks := []map[string]any{
		{
			"type": "header",
			"text": map[string]any{"type": "plain_text", "text": emoji + " " + verb, "emoji": true},
		},
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": slackSummary(e)},
		},
		{
			"type": "section",
			"fields": []map[string]any{
				mrkdwnField("Observed", humanizeBytes(e.Observed)),
				mrkdwnField("Threshold", thresholdText(e.Threshold)),
				mrkdwnField("Mode", orDash(e.Mode)),
				mrkdwnField("Window", windowText(e.Window)),
			},
		},
		{
			"type":     "context",
			"elements": []map[string]any{{"type": "mrkdwn", "text": slackContext(e)}},
		},
		{"type": "divider"},
	}
	return blocks, e.Title() + " — " + e.Body()
}

func slackHeader(e Event) (emoji, verb string) {
	switch e.Type {
	case EventRestartTriggered:
		if e.DryRun {
			return ":mag:", "Would restart"
		}
		return ":recycle:", "Restart triggered"
	case EventCircuitBreakerTripped:
		return ":no_entry:", "Circuit breaker tripped"
	case EventRestartDeferred:
		return ":hourglass_flowing_sand:", "Restart deferred"
	default:
		return ":bell:", string(e.Type)
	}
}

func slackSummary(e Event) string {
	head := fmt.Sprintf("*%s* in *%s*  ·  %s", e.Workload, e.Namespace, e.Kind)
	var line string
	switch e.Type {
	case EventRestartDeferred:
		line = fmt.Sprintf("container *%s* is leaking; restart deferred to the next maintenance window", e.Container)
	case EventCircuitBreakerTripped:
		line = fmt.Sprintf("container *%s* keeps leaking; max restarts per window reached", e.Container)
	default:
		line = fmt.Sprintf("container *%s* crossed its memory threshold", e.Container)
	}
	return head + "\n" + line
}

func slackContext(e Event) string {
	parts := make([]string, 0, 3)
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	parts = append(parts, "memory-leak-reloader")
	if !e.Time.IsZero() {
		parts = append(parts, e.Time.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, "  ·  ")
}

func mrkdwnField(label, value string) map[string]any {
	return map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("*%s*\n%s", label, value)}
}

func thresholdText(t int64) string {
	if t <= 0 {
		return "—" // percent-of-limit with a varying limit; no single number
	}
	return humanizeBytes(t)
}

func windowText(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return d.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// humanizeBytes formats a byte count using IEC units in Kubernetes style
// (e.g. 943718400 -> "900Mi"), trimming a trailing ".0".
func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	s := fmt.Sprintf("%.1f", float64(n)/float64(div))
	s = strings.TrimSuffix(s, ".0")
	return s + string("KMGTPE"[exp]) + "i"
}
