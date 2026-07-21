// Package profile captures a pre-restart heap/memory profile from a container's
// pprof endpoint and writes it to a configured sink (object store, volume, or
// log). Capture is best-effort and time-boxed: it must never block or fail the
// restart it precedes. The stored blob is binary; only its URI is logged.
package profile

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result describes the outcome of a capture.
type Result struct {
	URI  string // where the profile was written (or "" if failed)
	Size int    // bytes captured
}

// Sink stores a captured profile blob and returns a reference URI.
type Sink interface {
	// Name identifies the sink for logging/metrics.
	Name() string
	// Put stores data under key and returns a reference URI.
	Put(ctx context.Context, key string, data []byte) (string, error)
}

// Capturer fetches a pprof profile over HTTP and writes it to a Sink.
type Capturer struct {
	HTTP    *http.Client
	Port    int           // pprof port on the pod (default 6060)
	Timeout time.Duration // overall capture budget
	Sink    Sink
}

// NewCapturer builds a Capturer with sane defaults.
func NewCapturer(sink Sink, port int, timeout time.Duration) *Capturer {
	if port == 0 {
		port = 6060
	}
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Capturer{
		HTTP:    &http.Client{Timeout: timeout},
		Port:    port,
		Timeout: timeout,
		Sink:    sink,
	}
}

// Capture fetches the profile at http://podIP:port<path> and stores it under
// key. All errors are returned for logging/metrics but are non-fatal to the
// caller.
func (c *Capturer) Capture(ctx context.Context, podIP, path, key string) (Result, error) {
	if podIP == "" {
		return Result{}, fmt.Errorf("pod has no IP yet")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s:%d%s", podIP, c.Port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("fetch pprof %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("pprof %s returned HTTP %d", url, resp.StatusCode)
	}
	// Bound the read so a misbehaving endpoint cannot exhaust memory; heap
	// profiles are small (sampled, well under 1 MB), so 32 MiB is generous.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read pprof body: %w", err)
	}
	uri, err := c.Sink.Put(ctx, key, data)
	if err != nil {
		return Result{Size: len(data)}, fmt.Errorf("write profile to %s: %w", c.Sink.Name(), err)
	}
	return Result{URI: uri, Size: len(data)}, nil
}
