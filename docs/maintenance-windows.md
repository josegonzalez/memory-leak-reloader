# Maintenance windows

Gate disruptive restarts to approved windows. A restart warranted outside every
window is **deferred** (re-queued to the next opening) rather than dropped, and a
`RestartDeferred` Event + `memreload_rollouts_deferred_total` metric are emitted.
Detection and sampling continue during closed windows; only the action is gated.
Empty configuration means restarts are always allowed.

Windows are same-day (`start < end`); overnight wrap is intentionally
unsupported.

## Controller-wide (Helm)

The Helm value accepts a `days` string (ranges `Mon-Fri`, lists `Mon,Wed,Fri`, or
`*`), rendered into the controller's `--maintenance-window` flag:

```yaml
maintenanceWindows:
  - days: Mon-Fri
    start: "09:00"
    end: "17:00"
    timezone: America/New_York
```

## Per workload (policy)

`spec.maintenanceWindows` on a `MemoryLeakPolicy` is a structured, schema-validated
list; `days` is an explicit enum list. A policy's windows override the
controller-wide configuration for that workload:

```yaml
spec:
  maintenanceWindows:
    - days: [Mon, Tue, Wed, Thu, Fri]
      start: "09:00"
      end: "17:00"
      timezone: America/New_York
```
