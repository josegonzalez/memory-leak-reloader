//go:build e2e

// Package e2e drives a kind-based end-to-end test. It is guarded by the `e2e`
// build tag and only runs via `make e2e`, which sets an ISOLATED KUBECONFIG and
// a dedicated cluster name. The harness refuses to run against the developer's
// default kubeconfig and never switches the current context.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type harness struct {
	cluster    string
	kubeconfig string
	image      string
	namespace  string
	root       string
}

// repoRoot returns the repository root derived from this file's location
// (test/e2e/harness.go). `go test` runs the binary with its working directory
// set to the package dir, so relative paths to the Dockerfile and chart must be
// resolved from here rather than from the process working directory.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		return filepath.Join(filepath.Dir(file), "..", "..")
	}
	return root
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	cluster := envOr("KIND_CLUSTER", "memreload-e2e")
	image := envOr("IMG", "memory-leak-reloader:e2e")

	// Safety: never operate on the developer's real kubeconfig.
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG must be set to an isolated path for e2e (run via `make e2e`)")
	}
	root := repoRoot()
	// The Makefile passes a repo-root-relative KUBECONFIG, but `go test` runs in
	// the package dir, so anchor a relative path to the repo root to keep cluster
	// creation, kubectl, and teardown pointing at the same file.
	if !filepath.IsAbs(kubeconfig) {
		kubeconfig = filepath.Join(root, kubeconfig)
	}
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, ".kube", "config")
	if abs, _ := filepath.Abs(kubeconfig); abs == def {
		t.Fatalf("refusing to run e2e against the default kubeconfig %q", def)
	}
	return &harness{cluster: cluster, kubeconfig: kubeconfig, image: image, namespace: "payments", root: root}
}

// run executes a command with the isolated KUBECONFIG in the environment.
func (h *harness) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+h.kubeconfig)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}

func (h *harness) tryRun(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+h.kubeconfig)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *harness) up(t *testing.T) {
	t.Helper()
	// Create an isolated cluster; --kubeconfig keeps it out of ~/.kube/config.
	h.run(t, "kind", "create", "cluster", "--name", h.cluster, "--kubeconfig", h.kubeconfig, "--wait", "120s")

	// metrics-server (insecure kubelet TLS for kind).
	h.run(t, "kubectl", "apply", "-f", "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml")
	h.run(t, "kubectl", "-n", "kube-system", "patch", "deploy", "metrics-server", "--type=json",
		"-p", `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`)
	h.run(t, "kubectl", "-n", "kube-system", "rollout", "status", "deploy/metrics-server", "--timeout=180s")

	// Build and load the controller image into kind. The build context is the
	// repo root, not the test working directory.
	if _, err := h.tryRun("docker", "build", "-t", h.image, h.root); err != nil {
		t.Logf("docker build failed (continuing if image preloaded): %v", err)
	}
	h.run(t, "kind", "load", "docker-image", h.image, "--name", h.cluster)
}

func (h *harness) installChart(t *testing.T, extra ...string) {
	t.Helper()
	args := []string{
		"upgrade", "--install", "memreload", filepath.Join(h.root, "charts/memory-leak-reloader"),
		"-n", "memreload-system", "--create-namespace",
		"--set", "image.repository=" + strings.Split(h.image, ":")[0],
		"--set", "image.tag=" + imageTag(h.image),
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "replicaCount=1",
		"--wait", "--timeout", "180s",
	}
	args = append(args, extra...)
	h.run(t, "helm", args...)
}

// teardown deletes the cluster. It takes no *testing.T so it can run from
// TestMain after the whole suite completes (a t.Cleanup registered on the first
// test would instead fire between tests and tear the cluster down too early).
func (h *harness) teardown() {
	if os.Getenv("KEEP_CLUSTER") == "true" {
		return
	}
	_, _ = h.tryRun("kind", "delete", "cluster", "--name", h.cluster, "--kubeconfig", h.kubeconfig)
}

// waitFor polls fn until it returns true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Second)
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func imageTag(image string) string {
	if i := strings.LastIndex(image, ":"); i >= 0 {
		return image[i+1:]
	}
	return "latest"
}

func must(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

var _ = fmt.Sprintf
