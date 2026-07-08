package profile

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestVolumeSink(t *testing.T) {
	dir := t.TempDir()
	s := VolumeSink{Dir: dir}
	uri, err := s.Put(context.Background(), "ns/pod/c/p.pb.gz", []byte("hello"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("uri = %q want file://", uri)
	}
	data, err := os.ReadFile(strings.TrimPrefix(uri, "file://"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q", data)
	}
}

func TestLogSink(t *testing.T) {
	uri, err := LogSink{}.Put(context.Background(), "k", []byte("x"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasPrefix(uri, "data:application/octet-stream;base64,") {
		t.Fatalf("uri = %q", uri)
	}
}

type fakeUploader struct {
	bucket, key string
	data        []byte
}

func (f *fakeUploader) Scheme() string { return "s3" }
func (f *fakeUploader) Upload(_ context.Context, bucket, key string, data []byte) error {
	f.bucket, f.key, f.data = bucket, key, data
	return nil
}

func TestObjectStoreSink(t *testing.T) {
	up := &fakeUploader{}
	s := ObjectStoreSink{Uploader: up, Bucket: "b", Prefix: "memreload-profiles/"}
	uri, err := s.Put(context.Background(), "ns/pod/c/p.pb.gz", []byte("data"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if up.bucket != "b" || up.key != "memreload-profiles/ns/pod/c/p.pb.gz" {
		t.Fatalf("uploaded to %s/%s", up.bucket, up.key)
	}
	if uri != "s3://b/memreload-profiles/ns/pod/c/p.pb.gz" {
		t.Fatalf("uri = %q", uri)
	}
}

func TestObjectStoreSink_NoUploader(t *testing.T) {
	s := ObjectStoreSink{Bucket: "b"}
	if _, err := s.Put(context.Background(), "k", []byte("x")); err == nil {
		t.Fatal("expected error when uploader is nil")
	}
}
