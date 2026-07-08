package profile

import (
	"context"
	"fmt"
)

// SinkConfig selects and configures the profile sink.
type SinkConfig struct {
	Type        string // objectstore | volume | log
	VolumeDir   string
	ObjectStore ObjectStoreConfig
}

// ObjectStoreConfig configures the object-store sink.
type ObjectStoreConfig struct {
	Provider string // s3 | gcs | azblob
	Bucket   string
	Prefix   string
	Region   string
}

// NewSink constructs the configured Sink. GCS/azblob are recognized but not yet
// implemented; selecting them returns a clear error rather than silently
// degrading.
func NewSink(ctx context.Context, cfg SinkConfig) (Sink, error) {
	switch cfg.Type {
	case "log":
		return LogSink{}, nil
	case "volume":
		if cfg.VolumeDir == "" {
			return nil, fmt.Errorf("volume sink requires a mount path")
		}
		return VolumeSink{Dir: cfg.VolumeDir}, nil
	case "objectstore", "":
		if cfg.ObjectStore.Bucket == "" {
			return nil, fmt.Errorf("objectstore sink requires a bucket")
		}
		switch cfg.ObjectStore.Provider {
		case "s3", "":
			up, err := NewS3Uploader(ctx, cfg.ObjectStore.Region)
			if err != nil {
				return nil, err
			}
			return ObjectStoreSink{Uploader: up, Bucket: cfg.ObjectStore.Bucket, Prefix: cfg.ObjectStore.Prefix}, nil
		case "gcs", "azblob":
			return nil, fmt.Errorf("object store provider %q not yet implemented", cfg.ObjectStore.Provider)
		default:
			return nil, fmt.Errorf("unknown object store provider %q", cfg.ObjectStore.Provider)
		}
	default:
		return nil, fmt.Errorf("unknown profile sink type %q", cfg.Type)
	}
}
