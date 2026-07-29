# Notifications

Optional outbound alerts on restart decisions. Delivery is best-effort and
time-boxed; failures never block remediation and are recorded by
`memreload_notifications_total{sink,result}`. When a policy is in dry-run (the
default), notifications still fire but are clearly labeled "would restart"; the
label reflects each policy's effective mode.

Sinks: Slack (incoming webhook or bot token), generic webhook (JSON POST),
Datadog Events. Events fired default to `RestartTriggered` and
`CircuitBreakerTripped`; add `RestartDeferred` if desired.

Set the top-level `clusterName` value to stamp an operator-defined cluster
name onto every notification so receivers shared across clusters can tell
where an event came from. When unset (the default) it is omitted everywhere.

## Message formats

**Slack** (incoming webhook and bot token) posts a Block Kit message
(`{"text": <fallback>, "blocks": [...]}`): a header (`:recycle: Restart
triggered`, or `:mag: Would restart` in dry-run), a summary section, a
two-column fields section (Observed / Threshold / Mode / Window, with sizes
humanized to `Mi`/`Gi`), a context line (reason · source · timestamp), and a
divider. The `text` field is a plain-text fallback for notifications and
accessibility. With `clusterName` configured, the summary line reads
`*api* in *payments* on *prod-eu*  ·  Deployment`.

**Generic webhook** POSTs a stable JSON document:

```json
{
  "type": "RestartTriggered",
  "workloadKind": "Deployment",
  "workload": "api",
  "namespace": "payments",
  "container": "app",
  "mode": "sustained",
  "observedBytes": 943718400,
  "thresholdBytes": 901775360,
  "observed": "900Mi",
  "threshold": "860Mi",
  "window": "10m0s",
  "reason": "working set stayed above threshold for the full window",
  "dryRun": true,
  "time": "2026-01-01T12:00:05Z",
  "clusterName": "prod-eu"
}
```

Both raw byte counts and humanized strings are included; internal routing fields
are never sent. The `dryRun` field is the policy's effective mode (its
`spec.dryRun`, default `true`). `clusterName` is omitted when unset. An
optional `Authorization` header is added when configured.

**Datadog Events** are tagged with the agent's standard Kubernetes keys
(`kube_namespace`, `kube_container_name`, `kube_deployment` /
`kube_stateful_set` by kind, and `kube_cluster_name` when `clusterName` is
set) so they correlate with agent-tagged metrics and logs, plus kind-agnostic
`workload`/`kind` tags. The cluster also appears as a `[prod-eu]` suffix on
the event title.

```yaml
clusterName: prod-eu
notifications:
  events: [RestartTriggered, CircuitBreakerTripped]
  slack:
    enabled: true
    webhookUrl: https://hooks.slack.com/services/XXX   # or slack.existingSecret
  webhook:
    enabled: false
    url: https://example/hook
    authHeaderSecret: { name: hook-auth, key: authorization }
  datadogEvent:
    enabled: true   # reuses datasource.datadog credentials/site
```

Credentials follow the same same-namespace-secret pattern as the Datadog
datasource (see [credentials](credentials.md)).

## Per-pod routing

A leaking workload can direct its notification to a specific destination via
non-secret pod annotations. Targeting is **replace-with-fallback**: a pod that
sets a target uses it instead of the default sinks; pods that set nothing use the
defaults.

### Slack channel (bot-token mode)

A Slack **channel ID is not a secret**, so it lives on the policy. Configure a
single bot token centrally (enables `chat.postMessage`), then per workload:

```yaml
spec:
  slackChannel: "C0123ABC"   # bot posts here instead of the default channel
```

```sh
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set notifications.slack.enabled=true \
  --set notifications.slack.botToken=$SLACK_BOT_TOKEN \
  --set notifications.slack.botDefaultChannel=C0DEFAULT
```

The bot must be a member of the target channels. A classic incoming webhook
cannot override the channel - bot-token mode is what makes per-pod channels work.

### Named routes (webhooks)

A webhook URL can itself be a secret, so it never goes on the policy. Instead
register **named routes** centrally and reference them by name:

```sh
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set 'notifications.routes[0].name=team-payments' \
  --set 'notifications.routes[0].type=webhook' \
  --set 'notifications.routes[0].url=https://hooks.example/payments' \
  --set 'notifications.routes[1].name=sre-slack' \
  --set 'notifications.routes[1].type=slack-webhook' \
  --set 'notifications.routes[1].url=https://hooks.slack.com/services/...'
```

```yaml
spec:
  notifyRoutes: ["team-payments", "sre-slack"]
```

Route URLs/auth are rendered into a mounted `<release>-notify-routes` Secret
(`routes.json`) in the controller namespace - or reference an existing one via
`notifications.routesExistingSecret`. The policy references routes only by
non-secret **name**; unknown names are rejected (`InvalidConfig` Event) so a
policy can never point alerts at an arbitrary endpoint.

A route's `type` may be omitted (or set to `auto`) to infer the kind from the
URL: a `hooks.slack.com` host is treated as a `slack-webhook` (Slack Block Kit
payload), anything else as a generic `webhook`. An explicit `type` always wins:

```yaml
notifications:
  routes:
    - { name: sre-slack, url: "https://hooks.slack.com/services/..." }   # auto -> slack-webhook
    - { name: team-payments, url: "https://hooks.example/payments" }     # auto -> webhook
    - { name: forced, type: webhook, url: "https://hooks.slack.com/..." } # explicit override
```

### How this works with secrets

The only secrets are the Slack bot token and the route URLs/auth, both held
centrally in the controller's namespace and delivered by the kubelet (env
`secretKeyRef` for the token, a mounted Secret for `routes.json`). The per-pod
selectors are non-secret, there are no cross-namespace secret reads, and no
added RBAC.
