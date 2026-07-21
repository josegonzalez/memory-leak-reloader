//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var h *harness

func TestMain(m *testing.M) {
	// The cluster is created lazily by the first test's setup(); tear it down
	// once here, after the whole suite, so it survives across tests.
	code := m.Run()
	if h != nil {
		h.teardown()
	}
	os.Exit(code)
}

// setup brings up the cluster once for the suite.
func setup(t *testing.T) *harness {
	if h == nil {
		h = newHarness(t)
		h.up(t)
	}
	return h
}

// TestDryRunThenEnforce opts a workload in (dry-run by default), confirms a
// "would restart" decision surfaces, then flips the policy to enforce and
// confirms exactly one rollout restart fires.
func TestDryRunThenEnforce(t *testing.T) {
	h := setup(t)

	// Namespace + a leaky workload: a container that grows memory under a small
	// limit, opted in with an aggressive (instantaneous-ish) sustained threshold.
	h.run(t, "kubectl", "create", "ns", h.namespace)
	t.Cleanup(func() { _, _ = h.tryRun("kubectl", "delete", "ns", h.namespace, "--wait=false") })
	manifest := withStdin(leakyDeploymentYAML())
	t.Cleanup(func() { _ = os.Remove(manifest) })
	h.run(t, "kubectl", "-n", h.namespace, "apply", "-f", manifest)

	h.run(t, "kubectl", "-n", h.namespace, "rollout", "status", "deploy/leaky", "--timeout=120s")

	// Install with a short window + low threshold so detection fires quickly.
	h.installChart(t, append(scopedDetectionArgs(h.namespace), "--set", "rollout.cooldown=10m")...)

	// Opt the workload in with a MemoryLeakPolicy (after the chart installs the
	// CRD). Policies are dry-run by default.
	applyPolicy(t, h, h.namespace, "")

	// In dry-run, a WouldRestart Event should appear and no restart annotation.
	got := waitFor(t, 4*time.Minute, func() bool {
		out, _ := h.tryRun("kubectl", "-n", h.namespace, "get", "events", "--field-selector", "reason=WouldRestart", "-o", "name")
		return strings.TrimSpace(out) != ""
	})
	if !got {
		t.Fatal("expected a WouldRestart event in dry-run")
	}
	if ann := restartedAtAnnotation(t, h); ann != "" {
		t.Fatalf("dry-run must not patch restartedAt, got %q", ann)
	}

	// Enforce: flip the policy itself; there is no chart-level switch.
	h.run(t, "kubectl", "-n", h.namespace, "patch", "mlp", "leaky",
		"--type=merge", "-p", `{"spec":{"dryRun":false}}`)

	if !waitFor(t, 4*time.Minute, func() bool { return restartedAtAnnotation(t, h) != "" }) {
		t.Fatal("expected a rollout restart (restartedAt annotation) after enforcing")
	}
}

// TestPerPodNotificationRouting verifies that a pod's memreload.io/notify-routes
// annotation directs its notification to the named route receiver and NOT to the
// default webhook receiver (replace-with-fallback). Runs in dry-run, where
// notifications still fire (labeled "would restart").
func TestPerPodNotificationRouting(t *testing.T) {
	h := setup(t)
	ns := "routing"
	h.run(t, "kubectl", "create", "ns", ns)
	t.Cleanup(func() { _, _ = h.tryRun("kubectl", "delete", "ns", ns, "--wait=false") })

	// Two HTTP echo receivers that log every request to stdout.
	for _, name := range []string{"default-recv", "route-recv"} {
		m := withStdin(echoReceiver(name))
		t.Cleanup(func() { _ = os.Remove(m) })
		h.run(t, "kubectl", "-n", ns, "apply", "-f", m)
		h.run(t, "kubectl", "-n", ns, "rollout", "status", "deploy/"+name, "--timeout=120s")
	}

	// Leaky workload; routing to the "team" route is set on the policy below.
	m := withStdin(leakyDeploymentYAML())
	t.Cleanup(func() { _ = os.Remove(m) })
	h.run(t, "kubectl", "-n", ns, "apply", "-f", m)
	h.run(t, "kubectl", "-n", ns, "rollout", "status", "deploy/leaky", "--timeout=120s")

	defaultURL := "http://default-recv." + ns + ".svc.cluster.local:8080/"
	routeURL := "http://route-recv." + ns + ".svc.cluster.local:8080/"
	h.installChart(t, append(scopedDetectionArgs(ns),
		"--set", "notifications.webhook.enabled=true",
		"--set", "notifications.webhook.url="+defaultURL,
		"--set", "notifications.routes[0].name=team",
		"--set", "notifications.routes[0].type=webhook",
		"--set", "notifications.routes[0].url="+routeURL,
	)...)

	// Opt in and route this workload's notifications to the "team" route.
	applyPolicy(t, h, ns, `  notifyRoutes: ["team"]`)

	gotRoute := waitFor(t, 4*time.Minute, func() bool {
		out, _ := h.tryRun("kubectl", "-n", ns, "logs", "deploy/route-recv")
		return strings.Contains(out, "POST")
	})
	if !gotRoute {
		t.Fatal("expected the route receiver to get the notification")
	}
	defaultOut, _ := h.tryRun("kubectl", "-n", ns, "logs", "deploy/default-recv")
	if strings.Contains(defaultOut, "POST") {
		t.Fatal("default receiver should NOT get a routed pod's notification (replace semantics)")
	}
}

func echoReceiver(name string) string {
	return strings.ReplaceAll(echoReceiverTmpl, "NAME", name)
}

func restartedAtAnnotation(t *testing.T, h *harness) string {
	out, _ := h.tryRun("kubectl", "-n", h.namespace, "get", "deploy", "leaky",
		"-o", `jsonpath={.spec.template.metadata.annotations.kubectl\.kubernetes\.io/restartedAt}`)
	return strings.TrimSpace(out)
}

// withStdin returns an exec stdin closure helper. kubectl apply -f - reads
// stdin; we shell out via a temp file to keep the harness simple.
func withStdin(content string) string {
	f, err := os.CreateTemp("", "manifest-*.yaml")
	if err != nil {
		panic(err)
	}
	_, _ = f.WriteString(content)
	_ = f.Close()
	return f.Name()
}

// echoReceiverTmpl is an HTTP echo Deployment+Service that logs each request to
// stdout (so the test can grep `kubectl logs`). NAME is substituted.
const echoReceiverTmpl = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: NAME
spec:
  replicas: 1
  selector:
    matchLabels: { app: NAME }
  template:
    metadata:
      labels: { app: NAME }
    spec:
      containers:
        - name: echo
          image: mendhak/http-https-echo:31
          env:
            - { name: HTTP_PORT, value: "8080" }
          ports:
            - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: NAME
spec:
  selector: { app: NAME }
  ports:
    - port: 8080
      targetPort: 8080
`

// memoryLeakPolicyYAML renders a MemoryLeakPolicy that opts the "leaky"
// Deployment into monitoring. notifyRoutesLine is an optional spec line (already
// indented two spaces), e.g. `  notifyRoutes: ["team"]`; pass "" for none.
func memoryLeakPolicyYAML(notifyRoutesLine string) string {
	return fmt.Sprintf(`
apiVersion: memreload.io/v1alpha1
kind: MemoryLeakPolicy
metadata:
  name: leaky
spec:
  workloadRef:
    kind: Deployment
    name: leaky
%s
`, notifyRoutesLine)
}

// applyPolicy applies the MemoryLeakPolicy, retrying briefly so it tolerates the
// CRD not yet being established right after the chart install.
func applyPolicy(t *testing.T, h *harness, ns, notifyRoutesLine string) {
	t.Helper()
	m := withStdin(memoryLeakPolicyYAML(notifyRoutesLine))
	t.Cleanup(func() { _ = os.Remove(m) })
	if !waitFor(t, 2*time.Minute, func() bool {
		_, err := h.tryRun("kubectl", "-n", ns, "apply", "-f", m)
		return err == nil
	}) {
		t.Fatal("failed to apply MemoryLeakPolicy (CRD not established?)")
	}
}

// leakyDeploymentYAML renders a Deployment whose container holds a sustained
// working set well above the detection threshold (10% of the 64Mi limit = ~6Mi)
// but comfortably under the limit, so the pod stays Ready and "leaking" for the
// whole test.
//
// The allocation is fixed rather than unbounded: an ever-growing leak hits the
// memory limit and crash-loops (RunContainerError / OOM) within a couple of
// minutes, leaving the pod NotReady so the reconciler skips it — which let the
// dry-run phase detect the leak but starved the later enforce phase of a Ready
// pod to restart.
func leakyDeploymentYAML() string {
	return `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: leaky
spec:
  replicas: 1
  selector:
    matchLabels: { app: leaky }
  template:
    metadata:
      labels:
        app: leaky
    spec:
      containers:
        - name: app
          image: busybox
          resources:
            limits:
              memory: 64Mi
          command: ["/bin/sh","-c"]
          args:
            - |
              dd if=/dev/zero of=/dev/shm/leak bs=1M count=40 2>/dev/null
              while true; do sleep 3600; done
`
}

// scopedDetectionArgs are the Helm --set flags shared by the e2e installs:
// single-namespace scope plus an aggressive detection config so a leak trips
// quickly. Callers append any feature-specific flags.
func scopedDetectionArgs(ns string) []string {
	return []string{
		"--set", "scope.mode=single",
		"--set", "scope.namespaces={" + ns + "}",
		"--set", "detection.thresholdPercent=10",
		"--set", "detection.window=1m",
		"--set", "detection.sampleInterval=10s",
		"--set", "detection.startupGracePeriod=10s",
	}
}
