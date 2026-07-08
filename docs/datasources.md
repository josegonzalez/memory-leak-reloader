# Datasources

The datasource is selected **explicitly** with no fallback. At startup the
controller probes the selected source and fails fast (not ready, clear error) if
it is unreachable or lacks permission - it never silently samples nothing.

| Type | Signal | Notes |
|------|--------|-------|
| `metrics-server` (default) | `metrics.k8s.io` PodMetrics | Instantaneous; trend mode uses the controller's in-memory buffer (warms up after start/failover). |
| `prometheus` | `container_memory_working_set_bytes` | Set `datasource.prometheus.url`. |
| `datadog` | `kubernetes.memory.working_set` | Needs API + Application keys (see [credentials](credentials.md)). |

All three default to the **working-set** signal - the value the OOM killer acts
on - so thresholds are comparable. The metrics still differ subtly per backend,
so re-validate thresholds when switching sources.

```sh
# Prometheus
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set datasource.type=prometheus \
  --set datasource.prometheus.url=http://prometheus.monitoring:9090

# Datadog
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set datasource.type=datadog \
  --set datasource.datadog.existingSecret.name=dd-keys
```
