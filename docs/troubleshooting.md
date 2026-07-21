# Troubleshooting

## Events

`kubectl describe pod` / `kubectl describe deploy` shows the decision lifecycle:

| Event | Meaning |
|-------|---------|
| `BreachDetected` | A watched container crossed its threshold/trend. |
| `WouldRestart` | Dry-run (the default): a restart would have fired. |
| `RestartTriggered` | A rollout restart was dispatched. |
| `RestartSkipped` | A gate blocked the restart (see reason). |
| `RestartDeferred` | Outside the maintenance window; re-queued. |
| `ProfileCaptured` | A heap profile was stored (URI in the message). |
| `CircuitBreakerTripped` | Max restarts per window reached; investigate the leak. |
| `PodUnmonitorable` | Opted in, but no watched container has a memory limit or hard-cap. |
| `InvalidConfig` | A runtime config check failed: an unresolvable maintenance-window timezone or an unregistered notification route. |

## Skip reasons (`memreload_rollouts_skipped_total{reason}`)

`in_progress`, `cooldown`, `circuit_breaker`, `cap`, `not_autorestartable`,
`superseded` (an external rollout changed the pod-template version between the
settle check and the dispatch, so the restart was abandoned).

## Common situations

- **Nothing happens for a policy.** Check the referenced `workloadRef` matches an
  existing workload, the container has a memory limit (or an absolute cap), a pod
  is `Ready` and older than the startup grace, and the detection window has been
  covered (trend/sustained need a full window of samples after start or leader
  change).
- **`PodUnmonitorable`.** Add a memory limit or a `detection.thresholdAbsolute`
  on the policy.
- **Datasource errors / not ready.** The selected datasource is unreachable or
  RBAC is missing; the controller fails fast on purpose. Check
  `memreload_datasource_errors_total`.
- **Restarts not firing during business hours only.** A maintenance window is
  gating them; check `RestartDeferred` and `memreload_rollouts_deferred_total`.
- **Circuit breaker tripped repeatedly.** A restart only defers a real leak - fix
  the leak or raise the limit; the breaker is protecting you from a restart loop.

## Metrics

Scrape `:8080/metrics`. Per-workload series carry `workload_namespace`,
`workload_kind`, and `workload_name` labels identifying the policy's target
workload (prefixed so a scraper's own `namespace` target label cannot rename
them):
`memreload_pods_monitored`, `memreload_threshold_breaches_total{mode}`,
`memreload_rollouts_triggered_total{result}`,
`memreload_rollouts_skipped_total{reason}`,
`memreload_rollouts_deferred_total`, `memreload_containers_ignored{reason}`,
`memreload_profile_captures_total{result}`,
`memreload_notifications_total{sink,result}`. Process-global series:
`memreload_inflight_rollouts`, `memreload_global_cap`,
`memreload_sample_buffer_series`, `memreload_datasource_errors_total{source}`.
Configured policies are exported as
`memreload_policies{workload_namespace,dry_run}`; the effective per-policy
mode is shown in the `DRY-RUN` column of `kubectl get mlp`.

Breaking change note: the per-workload labels above re-key every series, and
`memreload_policy_dryrun{namespace,name}` was replaced by
`memreload_policies{workload_namespace,dry_run}`. Dashboards or monitors built
on the old unlabeled series need updating - in particular, `memreload_pods_monitored`
is now summed across workloads (`sum:` in Datadog, `sum()` in PromQL) rather
than read as a single gauge.

Note: dry-run keeps no persisted cooldown state, so a policy flipped from
dry-run to enforce may restart its workload immediately on the next detected
breach.
