# Installation

The controller installs via the Helm chart, published to the Helm repository
at `https://josediazgonzalez.com/memory-leak-reloader`:

```sh
helm repo add memreload https://josediazgonzalez.com/memory-leak-reloader
helm repo update
```

When developing against a local checkout, install from the chart path
(`./charts/memory-leak-reloader`) instead of `memreload/memory-leak-reloader`.

## Scopes

- `cluster` (default): watch `MemoryLeakPolicy` objects cluster-wide (ClusterRole).
- `namespaces`: restrict to a list of namespaces (Role per namespace).
- `single`: a single namespace.

```sh
# cluster-wide
helm install memreload memreload/memory-leak-reloader \
  -n memreload-system --create-namespace

# scoped to two namespaces
helm install memreload memreload/memory-leak-reloader \
  -n memreload-system --create-namespace \
  --set scope.mode=namespaces --set 'scope.namespaces={payments,checkout}'
```

When `scope.mode != cluster`, the controller's own namespace is always included
in the cache so the leader-election lease is reachable.

## Recommended rollout

1. Install with defaults.
2. Create a `MemoryLeakPolicy` for each workload you want managed (see
   [configuration](configuration.md)). Policies are dry-run by default: the
   controller samples, evaluates, and logs `WouldRestart` (and emits the same
   Events + metrics, plus clearly-labeled notifications if a sink is configured)
   but takes no action. Confirm the right workloads are flagged and thresholds
   are sane.
3. Enforce per workload by setting `spec.dryRun: false` on its policy, e.g.
   `kubectl -n <ns> patch mlp <name> --type=merge -p '{"spec":{"dryRun":false}}'`.
   There is no chart-level enforce switch.

## RBAC

`rbac.create=true` (default) renders the needed Role/ClusterRole. The controller
needs `get/list/watch` on `memreload.io/memoryleakpolicies` (plus `update/patch`
on it, its `/status`, and its `/finalizers`), `get/list/watch` on pods and
`metrics.k8s.io/pods`, `get/list/watch/patch` on Deployments/StatefulSets/
ReplicaSets and `argoproj.io/rollouts`, `create/patch` on events, and leases in
its own namespace for leader election.

## High availability

`replicaCount: 2` with leader election (default) keeps a warm standby; only the
leader samples and acts. Trend detection re-warms its in-memory buffers after a
leader change.
