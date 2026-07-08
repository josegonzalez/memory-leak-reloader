# memory-leak-reloader

A Kubernetes controller that detects per-container memory leaks in opted-in pods
and triggers a safe rollout restart of the owning workload (Deployment,
StatefulSet, or Argo Rollout) before an OOM kill.

It is conservative by design: it acts only on healthy pods that belong to the
current revision, never stacks a restart on top of an in-progress rollout, and
serializes restarts (per-workload dedup plus a global concurrency cap) so a
fleet-wide leak cannot cause a restart storm. It is opt-in, observable, and safe
to trial via dry-run.

Opt-in and tuning are expressed by a namespaced `MemoryLeakPolicy` custom
resource, one per managed workload. The policy carries both the configuration
(its `spec`) and the controller's durable per-workload state (its `status`:
circuit-breaker bookkeeping, the in-flight rollout marker, and a bounded restart
history).

## How it works

1. For each `MemoryLeakPolicy`, a leader-gated sampler resolves the referenced
   workload, reads its pods' container working-set memory from the configured
   datasource (metrics-server by default) on an interval, and feeds a
   per-container ring buffer.
2. Three selectable detection modes evaluate each watched container: `sustained`
   (over threshold for the whole window), `trend` (upward slope via linear
   regression), and `combined` (both).
3. When a container leaks, the controller checks the safety gates (pod Ready and
   past startup grace, current revision, no ongoing rollout, cooldown, circuit
   breaker, maintenance window, concurrency cap), optionally captures a heap
   profile, then triggers a rollout restart and records it in the policy status.

## Quickstart (dry-run by default)

The controller ships in **dry-run by default** - it samples, evaluates, emits
`WouldRestart` Events/metrics (and clearly-labeled notifications if a sink is
configured) but takes no action until you opt into enforcement.

```sh
helm install memreload ./charts/memory-leak-reloader \
  --namespace memreload-system --create-namespace \
  --set scope.mode=single --set 'scope.namespaces={payments}'
```

Watch for `WouldRestart` log lines / Events, then enforce:

```sh
helm upgrade memreload ./charts/memory-leak-reloader \
  --namespace memreload-system --reuse-values --set dryRun=false
```

## Opt a workload in

Create a `MemoryLeakPolicy` referencing the workload. Only `workloadRef` is
required; every other field inherits the controller defaults (Helm values /
flags) when unset:

```yaml
apiVersion: memreload.io/v1alpha1
kind: MemoryLeakPolicy
metadata:
  name: api
  namespace: payments
spec:
  workloadRef:
    kind: Deployment       # Deployment | StatefulSet | Rollout
    name: api
  detection:
    mode: combined
    thresholdPercent: 80
    window: 15m
    trendMinGrowth: 200Mi
  containers: ["app"]
```

Inspect it with `kubectl -n payments get mlp` (state lives in the policy status).

A watched container with **no memory limit** (and no `detection.thresholdAbsolute`
cap) is ignored. If every watched container is unmonitorable, the controller
emits a `PodUnmonitorable` warning so the ineffective opt-in is visible.

Reconfigure with in-place `kubectl apply`/`kubectl patch`. Deleting and
recreating a policy resets its status (the circuit-breaker window and any
in-flight marker), so prefer editing in place.

## Documentation

See [`docs/`](docs/README.md): installation, configuration reference,
datasources, credentials, profiling, maintenance windows, notifications, and
troubleshooting.

## Development

```sh
make test        # unit tests (race)
make envtest     # controller tests against a test apiserver
make e2e         # kind-based end-to-end (isolated kubeconfig)
make helm-lint   # lint + template the chart
```

The e2e suite uses a dedicated kind cluster and an **isolated kubeconfig**; it
never touches your `~/.kube/config` or current context.
