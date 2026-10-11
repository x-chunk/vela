package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	serverconfig "go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/r2"
)

// S3Config prepares an isolated MinIO bucket or skips without an integration endpoint.
func S3Config(t testing.TB) *serverconfig.Config {
	t.Helper()
	endpoint := os.Getenv("VELA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set VELA_TEST_S3_ENDPOINT for MinIO integration tests")
	}
	cfg := &serverconfig.Config{Storage: "r2", R2: serverconfig.R2Config{AccountID: "test", Endpoint: endpoint, Bucket: "vela-" + ids.NewULID(nil).New(), Prefix: "blobs/", AccessKeyID: "vela-test", SecretAccessKey: "vela-test-secret", Delivery: "proxy", PresignTTL: 5 * time.Minute, PartSize: 8 << 20, UploadConcurrency: 2, PathStyle: true}}
	client := s3.New(s3.Options{Region: "auto", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(cfg.R2.AccessKeyID, cfg.R2.SecretAccessKey, "")})
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(cfg.R2.Bucket)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend, err := r2.New(cfg.R2)
		if err == nil {
			_ = backend.Walk(context.Background(), func(info content.BlobInfo) error { return backend.Delete(context.Background(), info.ID) })
		}
		_, _ = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(cfg.R2.Bucket)})
	})
	return cfg
}
