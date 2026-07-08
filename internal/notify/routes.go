package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Route is a named notification destination. Sensitive fields (URL, auth) come
// from a mounted Secret; the pod references a route only by its non-secret name.
type Route struct {
	Name string `json:"name"`
	// Type is "webhook", "slack-webhook"/"slack", or empty/"auto" to infer from
	// the URL (a hooks.slack.com host is treated as a Slack webhook).
	Type       string `json:"type"`
	URL        string `json:"url"`
	AuthHeader string `json:"authHeader,omitempty"`
}

// detectType infers a route type from its URL when none is given: a Slack
// incoming-webhook host maps to slack-webhook, everything else to a generic
// webhook.
func detectType(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "hooks.slack.com" {
			return "slack-webhook"
		}
	}
	return "webhook"
}

// BuildRoutes converts route definitions into a name->Sink registry. An empty
// or "auto" Type is resolved by detectType; an explicit Type always overrides.
func BuildRoutes(routes []Route) (map[string]Sink, error) {
	m := make(map[string]Sink, len(routes))
	for _, r := range routes {
		if r.Name == "" {
			return nil, fmt.Errorf("route with empty name")
		}
		if _, dup := m[r.Name]; dup {
			return nil, fmt.Errorf("duplicate route name %q", r.Name)
		}
		typ := strings.ToLower(strings.TrimSpace(r.Type))
		if typ == "" || typ == "auto" {
			typ = detectType(r.URL)
		}
		switch typ {
		case "webhook":
			m[r.Name] = WebhookSink{URL: r.URL, AuthHeader: r.AuthHeader}
		case "slack-webhook", "slack":
			m[r.Name] = SlackSink{WebhookURL: r.URL}
		default:
			return nil, fmt.Errorf("route %q: unknown type %q (want webhook|slack-webhook|auto)", r.Name, r.Type)
		}
	}
	return m, nil
}

// LoadRoutes reads and parses a routes.json file into a route registry. A
// missing file is not an error - it yields an empty registry (routing optional).
func LoadRoutes(path string) (map[string]Sink, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Sink{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]Sink{}, nil
	}
	var routes []Route
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, fmt.Errorf("parse routes file %s: %w", path, err)
	}
	return BuildRoutes(routes)
}
