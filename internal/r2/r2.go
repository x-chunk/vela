// Package r2 streams immutable blob contents to private S3-compatible storage.
// Explicit credentials and redacted errors prevent ambient AWS configuration
// and signed request URLs from entering Vela's behavior or logs.
package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
)

// Store owns a private bucket prefix and a bounded multipart uploader.
type Store struct {
	client    *s3.Client
	uploader  *manager.Uploader //nolint:staticcheck // The storage specification pins manager; replace after contract validation.
	presigner *s3.PresignClient
	cfg       config.R2Config
	checkMu   sync.Mutex
	checkedAt time.Time
	checkErr  error
}

// New creates an explicit client; Check must succeed before serving traffic.
func New(cfg config.R2Config) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	client := s3.New(s3.Options{
		Region: "auto", BaseEndpoint: aws.String(cfg.Endpoint), UsePathStyle: cfg.PathStyle,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
		HTTPClient:  &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Retryer:     retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 3 }),
		// R2 does not support every optional AWS checksum/trailer extension.
		// Vela always computes and verifies its own SHA-256 over the source stream.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	//nolint:staticcheck // Keep the specified multipart manager until transfermanager has equivalent contract coverage.
	uploader := manager.NewUploader(immutableClient{client}, func(u *manager.Uploader) {
		u.PartSize = cfg.PartSize
		u.Concurrency = cfg.UploadConcurrency
		u.LeavePartsOnError = false
	})
	return &Store{client: client, uploader: uploader, presigner: s3.NewPresignClient(client), cfg: cfg}, nil
}

func (s *Store) key(id string) (string, error) {
	if !ids.Valid(id) {
		return "", errors.New("invalid blob id")
	}
	return s.cfg.Prefix + s.RelPath(id), nil
}

// RelPath matches filesystem sharding so migrations retain every blob ID.
func (s *Store) RelPath(id string) string {
	n := len(id)
	if n < 4 {
		return ""
	}
	return id[n-4:n-2] + "/" + id[n-2:] + "/" + id
}

// Put never stages data on disk; the uploader buffers at most concurrency+1 parts.
func (s *Store) Put(ctx context.Context, id string, r io.Reader, opts content.PutOptions) (content.PutResult, error) {
	key, err := s.key(id)
	if err != nil {
		return content.PutResult{}, err
	}
	if opts.MaxSize < 0 || opts.SizeHint > opts.MaxSize {
		return content.PutResult{}, content.ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return content.PutResult{}, err
	}
	uploadToken := ids.NewULID(nil).New()
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	defer cancelUpload()
	stream := content.NewStream(uploadCtx, r, opts.MaxSize)
	body := cancelOnReadError{reader: stream, cancel: cancelUpload}
	//nolint:staticcheck // See the uploader construction rationale.
	_, err = s.uploader.Upload(uploadCtx, &s3.PutObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), Body: body, ContentType: optionalString(opts.ContentType), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"vela-upload": uploadToken}})
	if err != nil || ctx.Err() != nil || stream.Err() != nil {
		// SDK abort uses the request context. Once canceled it cannot reach R2,
		// so retry cleanup with an independent, short-lived context.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var multi manager.MultiUploadFailure
		if errors.As(err, &multi) {
			_, _ = s.client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(multi.UploadID())})
		}
		// An upload response can be lost after remote completion. The token
		// identifies our content even when a retry reports a precondition failure.
		deleteErr := s.cleanFailedObject(cleanupCtx, key, uploadToken)

		if stream.Err() != nil {
			err = stream.Err()
		} else if ctx.Err() != nil {
			err = ctx.Err()
		}
		return content.PutResult{}, errors.Join(safeError("upload blob", err), safeError("clean failed upload", deleteErr))
	}
	result := stream.Result()
	if !content.Matches(opts.ExpectSHA256, result.SHA256) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = s.Delete(cleanupCtx, id)
		return result, errors.Join(content.ErrChecksumMismatch, err)
	}
	return result, nil
}

// cleanFailedObject removes a completed upload whose response was lost, but
// never content belonging to a different writer that used the same ID.
func (s *Store) cleanFailedObject(ctx context.Context, key, token string) error {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if errors.Is(safeError("check failed upload", err), content.ErrNotFound) {
		return nil
	}
	if err != nil {
		return safeError("check failed upload", err)
	}
	if out.Metadata["vela-upload"] != token {
		return nil
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	return safeError("delete failed upload", err)
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return aws.String(v)
}

// Open asks S3 for only the requested range and keeps cancellation on its body.
func (s *Store) Open(ctx context.Context, id string, off, length int64) (io.ReadCloser, error) {
	key, err := s.key(id)
	if err != nil {
		return nil, err
	}
	if off < 0 {
		return nil, errors.New("negative blob offset")
	}
	in := &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)}
	if length == 0 {
		// A zero-length read still checks existence, without an invalid bytes=0--1.
		_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: in.Bucket, Key: in.Key})
		if err != nil {
			return nil, safeError("open blob", err)
		}
		return io.NopCloser(strings.NewReader("")), nil
	}
	if off > 0 || length >= 0 {
		value := fmt.Sprintf("bytes=%d-", off)
		if length > 0 {
			if off > int64(^uint64(0)>>1)-length {
				return nil, errors.New("blob range overflow")
			}
			value += fmt.Sprint(off + length - 1)
		}
		in.Range = aws.String(value)
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, safeError("open blob", err)
	}
	return &redactedReader{ReadCloser: out.Body}, nil
}

type redactedReader struct{ io.ReadCloser }

func (r *redactedReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = safeError("read blob", err)
	}
	return n, err
}

// Delete is idempotent, including a missing object.
func (s *Store) Delete(ctx context.Context, id string) error {
	key, err := s.key(id)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	return safeError("delete blob", err)
}

// Walk paginates only the managed prefix and ignores noncanonical object keys.
func (s *Store) Walk(ctx context.Context, fn func(content.BlobInfo) error) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(s.cfg.Prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return safeError("list blobs", err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			id := key[strings.LastIndex(key, "/")+1:]
			canonical, err := s.key(id)
			if err != nil || key != canonical {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(content.BlobInfo{ID: id, Size: aws.ToInt64(obj.Size), ModTime: aws.ToTime(obj.LastModified)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Check caches HeadBucket for 45 seconds; it checks access, not write permissions.
// Object Read & Write permissions are an operator requirement, verified on upload.
func (s *Store) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if time.Since(s.checkedAt) < 45*time.Second {
		return s.checkErr
	}
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.cfg.Bucket)})
	s.checkErr = safeError("check R2 bucket (credentials, bucket, or network)", err)
	// Do not cache cancellation caused by a disconnected health client.
	if ctx.Err() == nil {
		s.checkedAt = time.Now()
	}
	return s.checkErr
}

// CleanTmp is a no-op: uploads never use the local temporary directory.
func (*Store) CleanTmp(time.Time) (int, error) { return 0, nil }

// CleanMultipart aborts abandoned uploads with pagination under the managed prefix.
func (s *Store) CleanMultipart(ctx context.Context, cutoff time.Time) (int, error) {
	in := &s3.ListMultipartUploadsInput{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(s.cfg.Prefix)}
	total := 0
	for {
		page, err := s.client.ListMultipartUploads(ctx, in)
		if err != nil {
			return total, safeError("list multipart uploads", err)
		}
		for _, upload := range page.Uploads {
			key := aws.ToString(upload.Key)
			id := key[strings.LastIndex(key, "/")+1:]
			canonical, keyErr := s.key(id)
			if keyErr != nil || canonical != key || upload.Initiated == nil || !upload.Initiated.Before(cutoff) {
				continue
			}
			_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: in.Bucket, Key: upload.Key, UploadId: upload.UploadId})
			if err != nil {
				return total, safeError("abort multipart upload", err)
			}
			total++
		}
		if !aws.ToBool(page.IsTruncated) {
			return total, nil
		}
		in.KeyMarker = page.NextKeyMarker
		in.UploadIdMarker = page.NextUploadIdMarker
	}
}

// ContentURL optionally presigns a GET with the response policy chosen by Go.
// ttl caps the grant at the remaining lifetime of an object or Vela signed link.
func (s *Store) ContentURL(ctx context.Context, id, contentType, disposition, cache string, ttl time.Duration) (string, error) {
	if s.cfg.Delivery != "redirect" {
		return "", nil
	}
	key, err := s.key(id)
	if err != nil {
		return "", err
	}
	ttl = min(ttl, s.cfg.PresignTTL)
	if ttl < time.Second {
		return "", nil
	} // Proxy rather than outlive a nearly expired grant.
	out, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), ResponseContentType: aws.String(contentType), ResponseContentDisposition: aws.String(disposition), ResponseCacheControl: aws.String(cache)}, func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return "", safeError("presign blob", err)
	}
	return out.URL, nil
}

// backendError retains errors.Is/As while its printable form excludes credentials,
// raw SDK responses, request URLs, and provider-controlled error messages.
type backendError struct {
	operation string
	cause     error
}

func (e *backendError) Error() string {
	var apiErr smithy.APIError
	if errors.As(e.cause, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return e.operation + ": blob not found"
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return e.operation + ": access denied"
		case "NoSuchBucket":
			return e.operation + ": bucket not found"
		}
	}
	if errors.Is(e.cause, content.ErrTooLarge) {
		return e.operation + ": size limit exceeded"
	}
	if errors.Is(e.cause, context.Canceled) {
		return e.operation + ": canceled"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return e.operation + ": timed out"
	}
	return e.operation + ": backend request failed"
}
func (e *backendError) Unwrap() error { return e.cause }
func (e *backendError) Is(target error) bool {
	var apiErr smithy.APIError
	return target == content.ErrNotFound && errors.As(e.cause, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}
func safeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &backendError{operation: operation, cause: err}
}

var _ content.Store = (*Store)(nil)

// immutableClient propagates exclusive publication to multipart completion;
// manager otherwise copies only a subset of PutObject fields into that request.
type immutableClient struct{ *s3.Client }

func (c immutableClient) CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	in.IfNoneMatch = aws.String("*")
	return c.Client.CompleteMultipartUpload(ctx, in, opts...)
}

// cancelOnReadError interrupts part requests as soon as a size/read failure
// occurs. The original stream error remains available for domain error mapping.
type cancelOnReadError struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelOnReadError) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.cancel()
	}
	return n, err
}
