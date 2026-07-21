# memory-leak-reloader

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

A Kubernetes controller that detects per-container memory leaks in opted-in pods and triggers a safe rollout restart of the owning workload.

**Homepage:** <https://github.com/josegonzalez/memory-leak-reloader>

## Prerequisites

- Kubernetes with metrics-server (the default datasource), or Prometheus / Datadog credentials
- Helm 3

## Installing the chart

Every `MemoryLeakPolicy` is dry-run by default - the controller logs what it
would restart but takes no action until a policy sets `spec.dryRun: false`.
Enforcement is opt-in per workload; there is no chart-level switch.

> **Breaking change**: earlier chart versions had a `dryRun` value that toggled
> the whole installation. It has been removed and is ignored if still set (for
> example via `--reuse-values`), so a fleet that had `dryRun=false` reverts to
> dry-run until each policy sets `spec.dryRun: false`.

```sh
helm repo add memreload https://josediazgonzalez.com/memory-leak-reloader
helm install memreload memreload/memory-leak-reloader \
  --namespace memreload-system --create-namespace
```

## Documentation

See the repository [docs](../../docs/README.md) for installation, configuration,
datasources, credentials, notifications, and troubleshooting guides.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| image.repository | string | `"ghcr.io/josegonzalez/memory-leak-reloader"` | Controller image repository, published to the GitHub Container Registry (`ghcr.io/<owner>/<repo>`). |
| image.tag | string | `""` | Image tag; defaults to the chart appVersion when empty. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| imagePullSecrets | list | `[]` | Image pull secrets for the controller pod. |
| replicaCount | int | `2` | Controller replicas; 2+ only meaningful with leaderElection.enabled (only the leader samples and acts). |
| crds.install | bool | `true` | Install the MemoryLeakPolicy CRD (set false if managed out-of-band). |
| scope.mode | string | `"cluster"` | Watch scope: one of cluster, namespaces, single. |
| scope.namespaces | list | `[]` | Required when mode != cluster; drives which namespaces' policies are watched and Role vs ClusterRole. |
| log.format | string | `"json"` | Log format: one of logfmt (local), json (Datadog). |
| log.level | string | `"info"` | Log level. |
| detection.mode | string | `"sustained"` | Default detection mode: one of sustained, trend, combined. |
| detection.thresholdPercent | int | `85` | Breach when usage >= this percent of the container memory limit. |
| detection.window | string | `"10m"` | Detection window. |
| detection.sampleInterval | string | `"30s"` | Memory sampling interval. |
| detection.trendMinGrowth | string | `"100Mi"` | Minimum projected growth over one window before trend modes flag a leak. |
| detection.startupGracePeriod | string | `"5m"` | Ignore pods younger than this. |
| rollout.cooldown | string | `"30m"` | Per-workload cooldown between restarts. |
| rollout.globalMaxConcurrent | int | `1` | Global cap on concurrent restarts across all workloads. |
| rollout.maxRestartsPerWindow | int | `3` | Circuit breaker: restarts allowed per workload within restartWindow. |
| rollout.restartWindow | string | `"24h"` | Window the circuit breaker counts restarts over. |
| rollout.inflightTimeout | string | `"15m"` | A rollout still unsettled after this ages out with outcome TimedOut. |
| rollout.requeueAfter | string | `"30s"` | Requeue interval while polling an in-flight rollout to settle. |
| maintenanceWindows | list | `[]` | Allowed restart windows; empty = always allowed. Each item: `{ days: Mon-Fri, start: "09:00", end: "17:00", timezone: America/New_York }`. |
| profileCapture.enabled | bool | `false` | Capture a heap profile from the leaking container immediately before the restart. |
| profileCapture.pprofPath | string | `"/debug/pprof/heap"` | pprof path to capture. |
| profileCapture.pprofPort | int | `6060` | Port the target container serves pprof on. |
| profileCapture.timeout | string | `"10s"` | Time box for the capture request; capture is best-effort and never blocks the restart. |
| profileCapture.sink | string | `"objectstore"` | Profile sink: one of objectstore, volume, log. |
| profileCapture.objectStore.provider | string | `"s3"` | Object store provider (used when sink=objectstore): one of s3, gcs, azblob. |
| profileCapture.objectStore.bucket | string | `""` | Bucket profiles are uploaded to (used when sink=objectstore). |
| profileCapture.objectStore.prefix | string | `"memreload-profiles/"` | Key prefix for uploaded profiles. |
| profileCapture.objectStore.region | string | `""` | Object store region. |
| profileCapture.volume.mountPath | string | `"/var/run/memreload/profiles"` | Path profiles are written to (used when sink=volume); the chart mounts an emptyDir by default. |
| notifications.events | list | `["RestartTriggered","CircuitBreakerTripped"]` | Event kinds that notify; also available: RestartDeferred. |
| notifications.slack.enabled | bool | `false` | Enable Slack notifications. |
| notifications.slack.botToken | string | `""` | Bot token (inline -> chart Secret -> SLACK_BOT_TOKEN env). Bot-token mode enables per-pod channel override via `memreload.io/slack-channel`. |
| notifications.slack.botDefaultChannel | string | `""` | Channel used when a pod sets none (bot-token mode). |
| notifications.slack.webhookUrl | string | `""` | Incoming-webhook URL (inline -> chart Secret); fixed channel, no per-pod override. Bot token wins if both set. |
| notifications.slack.existingSecret | object | `{"botTokenKey":"bot-token","name":"","webhookKey":"webhook-url"}` | Reference an existing Secret instead of inline values (takes precedence). |
| notifications.webhook.enabled | bool | `false` | Enable generic webhook notifications. |
| notifications.webhook.url | string | `""` | Webhook URL. |
| notifications.webhook.authHeaderSecret | object | `{"key":"","name":""}` | Optional Secret reference rendered into the Authorization header. |
| notifications.datadogEvent.enabled | bool | `false` | Send Datadog Events; reuses datasource.datadog credentials/site. |
| notifications.routes | list | `[]` | Named routes for per-pod targeting via `memreload.io/notify-routes`. URLs/auth are sensitive and are rendered into a mounted `<release>-notify-routes` Secret (routes.json), or reference an existing Secret; pods reference routes by non-secret name only. type may be omitted (or "auto") to infer from the URL: a hooks.slack.com host becomes slack-webhook (Block Kit), anything else a generic webhook. Explicit type wins. |
| notifications.routesExistingSecret | string | `""` | Name of an existing Secret holding key routes.json (instead of inline routes). |
| workloads.deployments | bool | `true` | Deployments are eligible owner kinds. |
| workloads.statefulSets | bool | `true` | StatefulSets are eligible owner kinds. |
| workloads.argoRollouts | bool | `true` | Argo Rollouts are eligible owner kinds; auto-disabled at startup if the argoproj.io Rollout CRD isn't installed. |
| datasource.type | string | `"metrics-server"` | Datasource: one of metrics-server, prometheus, datadog (no fallback). |
| datasource.prometheus.url | string | `""` | Prometheus base URL. |
| datasource.prometheus.query | string | `"container_memory_working_set_bytes"` | Metric queried for container working-set memory. |
| datasource.prometheus.token | string | `""` | Inline bearer token; the chart creates a Secret. |
| datasource.prometheus.existingSecret | object | `{"name":"","tokenKey":"token"}` | Reference an existing Secret holding the bearer token; takes precedence over the inline token. |
| datasource.datadog.site | string | `"datadoghq.com"` | Datadog site. |
| datasource.datadog.metric | string | `"kubernetes.memory.working_set"` | Metric queried for container working-set memory. |
| datasource.datadog.apiKey | string | `""` | Inline API key; the chart creates Secret `<release>-datadog`. |
| datasource.datadog.appKey | string | `""` | Inline application key. |
| datasource.datadog.existingSecret | object | `{"apiKeyKey":"api-key","appKeyKey":"app-key","name":""}` | Reference an existing Secret holding the keys; takes precedence over inline keys. |
| metrics.enabled | bool | `true` | Expose controller Prometheus metrics. |
| metrics.bindAddress | string | `":8080"` | Metrics listen address. |
| metrics.service | object | `{"enabled":true,"port":8080}` | Metrics Service. |
| metrics.serviceMonitor | object | `{"enabled":false}` | ServiceMonitor for Prometheus Operator scraping. |
| metrics.prometheusRule | object | `{"enabled":false}` | PrometheusRule shipping the chart's alert rules. |
| leaderElection.enabled | bool | `true` | Enable leader election; only the leader samples and acts. |
| leaderElection.id | string | `"memory-leak-reloader"` | Leader-election lease name. |
| rbac.create | bool | `true` | Create the Role/ClusterRole and bindings the controller needs. |
| serviceAccount.create | bool | `true` | Create the controller ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; set explicitly when using EKS Pod Identity. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations, e.g. IRSA (eks.amazonaws.com/role-arn) / GKE WI / Azure WI. |
| healthProbeBindAddress | string | `":8081"` | Health probe listen address. |
| resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"100m","memory":"128Mi"}}` | Controller pod resources. |
| podAnnotations | object | `{}` | Extra pod annotations. |
| podLabels | object | `{}` | Extra pod labels. |
| nodeSelector | object | `{}` | Node selector for the controller pod. |
| tolerations | list | `[]` | Tolerations for the controller pod. |
| affinity | object | `{}` | Affinity rules for the controller pod. |
| securityContext | object | restricted: runAsNonRoot, uid 65532, RuntimeDefault seccomp | Pod security context. |
| containerSecurityContext | object | no privilege escalation, read-only root filesystem, all capabilities dropped | Container security context. |

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| josegonzalez |  |  |

## Source Code

* <https://github.com/josegonzalez/memory-leak-reloader>

