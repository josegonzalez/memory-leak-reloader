# Pre-restart profile capture

Optionally capture a heap profile from the leaking container immediately before
the restart, so engineers can root-cause the leak instead of losing the
evidence. Capture is an HTTP GET against the container's pprof path (no extra
RBAC; the controller reaches the pod over the network, not `pods/exec`), is
best-effort and time-boxed, and never blocks the restart.

The profile is a small sampled gzip protobuf (typically well under 1 MB) but
binary, so the default sink is an **object store**; the `ProfileCaptured` Event
and log record only the resulting URI, never the blob.

```sh
helm upgrade --install memreload ./charts/memory-leak-reloader \
  --set profileCapture.enabled=true \
  --set profileCapture.objectStore.bucket=my-memreload-profiles \
  --set profileCapture.objectStore.region=us-east-1 \
  --set serviceAccount.name=memreload   # for EKS Pod Identity association
```

Sinks:

- `objectstore` (default): `s3` implemented; `gcs`/`azblob` recognized but not
  yet implemented (selecting them errors rather than silently degrading).
- `volume`: writes to a mounted path; the chart mounts an `emptyDir` by default.
- `log`: base64-embeds the small blob into a log line - local testing only.

Per workload (on the policy): `spec.profileCapture: true|false` and
`spec.pprofPath: /debug/pprof/heap`. The controller must have
`profileCapture.enabled=true` for the capturer to exist; the policy field then
toggles it.

Metric: `memreload_profile_captures_total{result}` (success/error/skipped).
