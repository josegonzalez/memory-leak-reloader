package profile

import (
	"bytes"
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Uploader uploads profiles to S3. Credentials are resolved by the default
// AWS credential chain, which transparently supports EKS Pod Identity, IRSA,
// and static env credentials - so no key material is handled here.
type S3Uploader struct {
	client *s3.Client
}

// NewS3Uploader builds an S3 uploader. region may be empty to use the ambient
// region (env/region from the instance/identity).
func NewS3Uploader(ctx context.Context, region string) (*S3Uploader, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &S3Uploader{client: s3.NewFromConfig(cfg)}, nil
}

func (*S3Uploader) Scheme() string { return "s3" }

func (u *S3Uploader) Upload(ctx context.Context, bucket, key string, data []byte) error {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}
