package r2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"go.xchunk.org/vela/internal/blobtest"
	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
)

func testConfig(endpoint, bucket string) config.R2Config {
	return config.R2Config{AccountID: "test", Endpoint: endpoint, Bucket: bucket, Prefix: "blobs/", AccessKeyID: "vela-test", SecretAccessKey: "vela-test-secret", Delivery: "proxy", PresignTTL: 5 * time.Minute, PartSize: 8 << 20, UploadConcurrency: 2, PathStyle: true}
}

func TestS3Contract(t *testing.T) {
	endpoint := os.Getenv("VELA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set VELA_TEST_S3_ENDPOINT to run the shared contract against MinIO")
	}
	blobtest.Run(t, func(t *testing.T) content.Store {
		cfg := testConfig(endpoint, "vela-"+ids.NewULID(nil).New())
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = s.Walk(context.Background(), func(i content.BlobInfo) error { return s.Delete(context.Background(), i.ID) })
			_, _ = s.client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(cfg.Bucket)})
		})
		return s
	})
}

func TestPresignPolicy(t *testing.T) {
	cfg := testConfig("https://example.r2.cloudflarestorage.com", "vela-test")
	cfg.Delivery = "redirect"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := ids.NewULID(nil).New()
	raw, err := s.ContentURL(context.Background(), id, "text/plain", "attachment; filename*=UTF-8''report.txt", "private, no-store", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("X-Amz-Expires") != "30" || q.Get("response-content-type") != "text/plain" || q.Get("response-content-disposition") == "" || q.Get("response-cache-control") != "private, no-store" {
		t.Fatalf("presign response policy missing")
	}
	raw, err = s.ContentURL(context.Background(), id, "text/plain", "inline", "public", time.Millisecond)
	if raw != "" || err != nil {
		t.Fatal("near-expiry grant must use proxy")
	}
}

func TestErrorsRedactURLs(t *testing.T) {
	err := safeError("upload", fmt.Errorf("request https://secret@example.com/?X-Amz-Signature=secret failed"))
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https") {
		t.Fatal(err)
	}
}

func TestCheckCaches(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer ts.Close()
	s, err := New(testConfig(ts.URL, "test"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("HeadBucket calls=%d", calls)
	}
}

// Cancellation after multipart creation must abort with a usable context.
func TestCanceledMultipartAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aborted := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Query().Has("uploads"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><Bucket>test</Bucket><Key>blob</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == "PUT":
			cancel()
			w.WriteHeader(503)
		case r.Method == "DELETE" && r.URL.Query().Get("uploadId") != "":
			aborted = true
			w.WriteHeader(204)
		case r.Method == "HEAD":
			w.WriteHeader(404)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.Method)
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()
	s, err := New(testConfig(ts.URL, "test"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Put(ctx, ids.NewULID(nil).New(), strings.NewReader(strings.Repeat("x", 9<<20)), content.PutOptions{MaxSize: 10 << 20, SizeHint: -1})
	if err == nil || !aborted {
		t.Fatalf("error=%v abort=%v", err, aborted)
	}
}

func TestOpenCancellationClosesRemoteRequest(t *testing.T) {
	stopped := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = io.WriteString(w, "first bytes")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer ts.Close()
	s, err := New(testConfig(ts.URL, "test"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	body, err := s.Open(ctx, ids.NewULID(nil).New(), 0, -1)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err := io.ReadFull(body, b); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = body.Close()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("remote request did not stop")
	}
}

func TestMultipartJanitorPagination(t *testing.T) {
	id := ids.NewULID(nil).New()
	key := "blobs/" + id[22:24] + "/" + id[24:] + "/" + id
	pages, aborts := 0, 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			aborts++
			w.WriteHeader(204)
			return
		}
		pages++
		w.Header().Set("Content-Type", "application/xml")
		if pages == 1 {
			_, _ = fmt.Fprintf(w, `<ListMultipartUploadsResult><IsTruncated>true</IsTruncated><NextKeyMarker>%s</NextKeyMarker><NextUploadIdMarker>u1</NextUploadIdMarker><Upload><Key>%s</Key><UploadId>u1</UploadId><Initiated>2020-01-01T00:00:00Z</Initiated></Upload></ListMultipartUploadsResult>`, key, key)
		} else {
			if r.URL.Query().Get("key-marker") != key || r.URL.Query().Get("upload-id-marker") != "u1" {
				t.Error("multipart marker not forwarded")
			}
			_, _ = fmt.Fprintf(w, `<ListMultipartUploadsResult><IsTruncated>false</IsTruncated><Upload><Key>%s</Key><UploadId>u2</UploadId><Initiated>2099-01-01T00:00:00Z</Initiated></Upload><Upload><Key>blobs/foreign</Key><UploadId>foreign</UploadId><Initiated>2020-01-01T00:00:00Z</Initiated></Upload></ListMultipartUploadsResult>`, key)
		}
	}))
	defer ts.Close()
	s, err := New(testConfig(ts.URL, "test"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CleanMultipart(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil || n != 1 || pages != 2 || aborts != 1 {
		t.Fatalf("janitor: %d %v pages=%d aborts=%d", n, err, pages, aborts)
	}
}
