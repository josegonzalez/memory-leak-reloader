# Design

## Architecture

The controller is **policy-driven**: a `MemoryLeakPolicy` (one per workload) is
the unit of reconciliation. Two signals on two cadences:

1. **Policy watch** - a controller-runtime informer over `MemoryLeakPolicy`
   objects. Policy events (and a channel fed by the sampler) trigger
   reconciliation. Pods are **not** cached: the controller reads only the pods of
   policy-referenced workloads, on demand, through a client configured to bypass
   the cache for pods (no cluster-wide pod informer).
2. **Sampling runnable** - a leader-gated goroutine that, on an interval,
   iterates policies, resolves each workload, lists its pods by the workload's
   selector, queries the datasource for working-set memory, joins it with
   container limits from the pod spec, feeds per-container ring buffers, prunes
   stale series, and enqueues the policy whose detection window is covered.

`metrics.k8s.io` is not watchable, so it is never placed in the cache; the
sampler queries it directly.

## Reconcile pipeline (per policy)

resolve effective config from spec → finalizer bookkeeping → resolve owning
workload → adopt workload UID (reset stale state on delete+recreate) →
settle-state gate (poll/complete any in-flight rollout) → list workload pods →
per pod: health gate (Ready + past startup grace) → current-revision gate →
select containers → detection (sustained/trend/combined) → cooldown → circuit
breaker → maintenance window → concurrency gate (per-workload dedup + global
cap) → profile capture → dispatch (or, when the policy's `spec.dryRun` is true -
the default - a would-restart log) → record in policy status →
requeue to poll settle.

The policy carries a finalizer so the reconciler can release the workload's
in-memory concurrency slot before the object (and its folded state) is deleted;
without it a policy deleted mid-rollout would strand its slot until the
controller restarts. Because the in-memory gate is leader-local, a failover or
restart already clears orphaned slots, so the finalizer only needs to cover a
same-leader mid-rollout deletion.

Per-workload deduplication and the global cap are enforced by an in-memory gate
on the leader; the cluster's live rollout status is the source of truth. On
leader failover the gate is re-acquired deterministically: if a policy's status
records an in-flight rollout for the current pod-template version that has not yet
settled, the reconciler resumes polling it rather than treating it as someone
else's rollout.

The controller never lets an external rollout (an image/spec change from ArgoCD,
a deploy, or `kubectl apply`) corrupt its own-rollout tracking, because such a
change always moves the pod-template version while its own restart never does.
Just before dispatching it re-reads the live version and abandons the restart
(`skip("superseded")`) if it moved in the interim. Once a restart is in flight,
each expirer pass resolves every in-flight policy against the live
workload: if the version has diverged from the dispatched one (or the workload is
gone, or was recreated under a new UID) the slot is released immediately and the
history outcome recorded as `Superseded`, rather than holding capacity until the
`inflightTimeout`. A genuinely stuck rollout still ages out after
`inflightTimeout`, recording the outcome as `TimedOut`.

## Persistent state (folded into policy status)

Per-workload controller state lives in the `MemoryLeakPolicy` **status**
subresource (there is no separate state object). It holds the circuit-breaker
bookkeeping (last restart, window count/start, and the pod-template version the
window was opened against), the observed workload UID, the in-flight rollout
marker, a bounded restart history (each entry stamped with its trigger and
completion time and outcome), computed observability fields surfaced via
`kubectl get mlp` printer columns, and links to the last captured profile and
notification. High-frequency sampling data is deliberately kept out of the CRD
and exposed only as Prometheus metrics. The controller is the only writer of
status.

**Identity guard.** The policy references its workload by kind/name; the observed
workload UID is recorded in status. If the UID changes (the workload was deleted
and recreated under the same name) the controller drops the stale breaker
bookkeeping so the new workload starts fresh.

**Version-aware breaker reset.** The breaker window resets when the workload's
pod-template version changes ("fresh version = fresh breaker"). The version is
sourced per kind so it never moves on the controller's own restart: for
Deployments/StatefulSets it is a hash of the pod template with the
`restartedAt` stamp removed; for Rollouts it is Argo's `status.currentPodHash`
(which the `spec.restartAt` restart never touches).

**State lifecycle caveat.** Because state is folded into the policy, deleting and
recreating the policy object (a GitOps rename/prune, `kubectl replace --force`, or
a backup restore) resets its status - granting a fresh restart budget and
dropping any in-flight marker. Ordinary `kubectl apply`/`patch` preserves status
(the status subresource is untouched by spec writes), so reconfigure in place.

## Owner resolution & gates

- **Deployment**: Pod → ReplicaSet → Deployment. Current revision = RS revision
  annotation equals the Deployment's. Settled when observedGeneration is current
  and updated/available replicas match desired.
- **StatefulSet**: Pod → StatefulSet. Current = pod's `controller-revision-hash`
  equals `status.updateRevision`. `OnDelete` strategy is treated as
  not-auto-restartable.
- **Argo Rollout** (unstructured; no hard dependency): Pod → ReplicaSet →
  Rollout. Restart via `spec.restartAt`. Auto-disabled if the CRD is absent.

Deployments/StatefulSets restart via the `kubectl.kubernetes.io/restartedAt`
pod-template annotation (idempotent), exactly like `kubectl rollout restart`.

## Detection

- **Sustained**: working set over threshold for the full window (per-sample
  limit, so VPA/in-place resize is handled).
- **Trend**: OLS slope over the window; leaks when projected growth across one
  window meets `trend-min-growth`.
- **Combined**: both.

A restart-count bump (e.g. OOMKilled) resets the series so pre/post-restart data
are not mixed.

## Logging & observability

`log/slog` bridged to logr: logfmt (local) or Datadog-compatible JSON. Prometheus
metrics on `:8080`, native Kubernetes Events for every decision, and Datadog +
Grafana dashboards plus an optional PrometheusRule.

## Validation

The `MemoryLeakPolicy` CRD's OpenAPI schema plus CEL rules
(`x-kubernetes-validations`) validate the spec at apply time: enum fields
(`mode`, `workloadRef.kind`, maintenance-window `days`), numeric ranges
(`thresholdPercent`), quantity and `HH:MM` patterns, `workloadRef` immutability,
and `start < end` for maintenance windows. The only checks that remain at runtime
are those the schema cannot express - a maintenance-window timezone that does not
resolve (no tzdata in CEL) and a notification route name that is not registered
in the controller - which surface as `InvalidConfig` Events.
