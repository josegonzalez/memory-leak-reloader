# Credentials

All credentials reach the controller as environment variables sourced from a
Secret **in the controller's own namespace** via `secretKeyRef`. The kubelet
resolves the reference at pod start, so the controller needs no Secret RBAC and
never reads secret material over the API.

## Datadog (datasource and/or Datadog Event notifications)

Datadog's query API needs a **DD API key** and a **DD Application key**.

```sh
# (A) Inline - the chart creates the Secret (lands in the release; prefer B/SOPS in prod).
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set datasource.type=datadog \
  --set datasource.datadog.apiKey=$DD_API_KEY \
  --set datasource.datadog.appKey=$DD_APP_KEY

# (B) Reference an existing Secret (takes precedence over inline).
kubectl -n memreload-system create secret generic dd-keys \
  --from-literal=api-key=$DD_API_KEY --from-literal=app-key=$DD_APP_KEY
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set datasource.type=datadog \
  --set datasource.datadog.existingSecret.name=dd-keys
```

The same `datasource.datadog` credentials are reused by Datadog Event
notifications, even when the datasource is not Datadog.

## Prometheus / Slack / webhook

The same inline-or-existingSecret pattern applies: `datasource.prometheus.token`
or `prometheus.existingSecret` (`PROM_BEARER_TOKEN`), `notifications.slack.webhookUrl`
or `slack.existingSecret`, and `notifications.webhook.authHeaderSecret`.

### Slack bot token (per-workload channel routing)

`notifications.slack.botToken` (or `slack.existingSecret` with `botTokenKey`) is
injected as `SLACK_BOT_TOKEN` and enables `chat.postMessage`, so a policy can
target a channel via the non-secret `spec.slackChannel`. Bot-token mode takes
precedence over an incoming webhook when both are set.

### Notification routes

Named-route URLs/auth are sensitive and never appear on the policy. The chart
renders `notifications.routes` into a `<release>-notify-routes` Secret
(`routes.json`) mounted read-only at `/etc/memreload/routes`, or you can mount an
existing one via `notifications.routesExistingSecret`. A policy selects a route by
non-secret name (`spec.notifyRoutes`). No secret-read RBAC is needed - the kubelet
projects the mounted Secret.

## Object store (profile capture)

Auth precedence (no static keys preferred):

1. **EKS Pod Identity** (preferred on EKS): install the Pod Identity Agent add-on
   and create a Pod Identity Association mapping `{cluster, namespace,
   serviceAccount}` to an IAM role. Set `serviceAccount.name` explicitly so the
   association can target it; no SA annotation is needed.
2. **IRSA / GKE WI / Azure WI**: set `serviceAccount.annotations`
   (e.g. `eks.amazonaws.com/role-arn`).
3. **Static credentials**: a same-namespace Secret injected as the provider's
   standard env vars.

The controller code is identical across these; they differ only in setup.
