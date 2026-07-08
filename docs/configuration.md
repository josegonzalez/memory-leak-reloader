# Configuration

Opt-in and tuning are expressed by a namespaced `MemoryLeakPolicy` custom
resource (`memreload.io/v1alpha1`), one per managed workload. The controller
resolves the workload in the policy's own namespace.

Precedence: a policy's `spec` fields override the controller defaults (Helm
values / flags), which override the built-in defaults. Any unset spec field
inherits the default.

## MemoryLeakPolicy spec

| Field | Example | Meaning |
|-------|---------|---------|
| `workloadRef.kind` | `Deployment` | `Deployment` \| `StatefulSet` \| `Rollout`. Immutable. |
| `workloadRef.name` | `api` | Name of the workload in the policy's namespace. Immutable. |
| `detection.mode` | `combined` | `sustained` \| `trend` \| `combined`. |
| `detection.thresholdPercent` | `85` | Breach when usage ≥ this % of the memory limit (1-100). |
| `detection.thresholdAbsolute` | `500Mi` | Hard cap; precedence over percent. |
| `detection.window` | `10m` | Detection window. |
| `detection.trendMinGrowth` | `100Mi` | Min projected growth over a window (trend modes). |
| `cooldown` | `30m` | Per-workload cooldown. |
| `startupGrace` | `5m` | Ignore pods younger than this. |
| `containers` | `["app","worker"]` or `["*"]` | Container set to monitor. |
| `containerOverrides` | see below | Per-container detection overrides. |
| `profileCapture` | `true` | Enable/disable pre-restart capture for this workload. |
| `pprofPath` | `/debug/pprof/heap` | pprof path to capture. |
| `maintenanceWindows` | see below | Allowed restart windows. |
| `notifyRoutes` | `["team-payments"]` | Named notification routes to target (replaces default sinks). |
| `slackChannel` | `C0123ABC` | Per-workload Slack channel (bot-token mode); non-secret. See [notifications](notifications.md). |

All fields except `workloadRef` are optional. The spec is schema-validated
(enums, ranges, patterns) by the CRD's OpenAPI schema plus CEL rules, so
malformed values are rejected at apply time rather than surfaced as runtime
Events.

Container selection default: the `kubectl.kubernetes.io/default-container`
container, else the highest-memory-limit container (name-sort tiebreak). Native
sidecars (init containers with `restartPolicy: Always`) are in scope; plain init
containers are not. A container with no memory limit and no absolute cap is
ignored.

### Per-container overrides

`containerOverrides` replaces the former `mode.<container>` suffix annotations:

```yaml
spec:
  detection:
    thresholdPercent: 80        # policy-level base
  containerOverrides:
    - name: worker
      detection:
        thresholdPercent: 70    # override for the worker container
        mode: trend
```

### Maintenance windows

`maintenanceWindows` is a structured list (empty = always allowed). `start`/`end`
are `HH:MM` in 24-hour time with `start < end` (overnight windows unsupported);
`timezone` is an IANA name (empty means UTC). Everything except the timezone's
existence is schema-validated; an unknown timezone surfaces an `InvalidConfig`
Event at runtime.

```yaml
spec:
  maintenanceWindows:
    - days: [Mon, Tue, Wed, Thu, Fri]
      start: "09:00"
      end: "17:00"
      timezone: America/New_York
```

## Worked example

```yaml
apiVersion: memreload.io/v1alpha1
kind: MemoryLeakPolicy
metadata:
  name: api
  namespace: payments
spec:
  workloadRef:
    kind: Deployment
    name: api
  detection:
    mode: combined
    thresholdPercent: 80
    window: 15m
    trendMinGrowth: 200Mi
  containers: ["app"]
```

## Policy status (folded state)

The controller writes the durable per-workload state into the policy `status`
(there is no separate state object). It holds the circuit-breaker bookkeeping,
the observed workload UID (identity guard for delete + recreate), the in-flight
rollout marker, a bounded restart history (each entry stamped with its trigger
and completion time and an outcome of `Settled`, `Superseded` - an external
rollout replaced it - `TimedOut`, or `Failed`), and links to the last captured
profile and notification. High-frequency sampling data is deliberately kept out
of status and exposed only as Prometheus metrics.

Inspect it with `kubectl get mlp` (or `kubectl describe mlp <name>`):

```text
NAME   WORKLOAD     NAME   COUNT   BREAKER   LAST-RESTART   VERSION
api    Deployment   api    2       false     5m             1f3a9c2b8d4e6f01
```

The circuit-breaker window resets when the workload's pod-template changes, so a
new version starts with a fresh restart budget.

**Note:** status is written frequently and is reset if the policy object is
deleted and recreated (e.g. a GitOps rename/prune, `kubectl replace --force`, or
a backup restore) - which grants a fresh restart budget and drops any in-flight
marker. Prefer in-place `kubectl apply`/`kubectl patch` to reconfigure a policy.

The CRD is installed by the chart (`crds.install`, default `true`).

## Helm values

The full set of values is documented inline in
`charts/memory-leak-reloader/values.yaml`. Key groups: `crds`, `scope`, `dryRun`,
`log`, `detection`, `rollout`, `maintenanceWindows`, `profileCapture`,
`notifications`, `workloads`, `datasource`, `metrics`, `leaderElection`, `rbac`,
`serviceAccount`, `resources`. These set the controller-wide defaults that a
policy's spec overrides.
