package profile

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LogSink base64-embeds the (small) profile into its reference URI. Intended
// for quick local testing only - not for production.
type LogSink struct{}

func (LogSink) Name() string { return "log" }

func (LogSink) Put(_ context.Context, _ string, data []byte) (string, error) {
	return "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// VolumeSink writes the profile to a file under Dir and returns a file:// URI.
type VolumeSink struct {
	Dir string
}

func (s VolumeSink) Name() string { return "volume" }

func (s VolumeSink) Put(_ context.Context, key string, data []byte) (string, error) {
	path := filepath.Join(s.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file://" + path, nil
}

// Uploader stores a blob in an object store under (bucket, key).
type Uploader interface {
	// Scheme returns the URI scheme for stored objects (e.g. "s3").
	Scheme() string
	Upload(ctx context.Context, bucket, key string, data []byte) error
}

// ObjectStoreSink writes profiles to an object store via an Uploader. The
// concrete uploader (e.g. S3) is injected so this sink stays cloud-agnostic.
type ObjectStoreSink struct {
	Uploader Uploader
	Bucket   string
	Prefix   string
}

func (s ObjectStoreSink) Name() string { return "objectstore" }

func (s ObjectStoreSink) Put(ctx context.Context, key string, data []byte) (string, error) {
	if s.Uploader == nil {
		return "", fmt.Errorf("object store uploader not configured")
	}
	full := strings.TrimSuffix(s.Prefix, "/")
	if full != "" {
		full += "/"
	}
	full += key
	if err := s.Uploader.Upload(ctx, s.Bucket, full, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s://%s/%s", s.Uploader.Scheme(), s.Bucket, full), nil
}
