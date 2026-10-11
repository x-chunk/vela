package server_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/testutil"
)

func TestR2ProxyDelivery(t *testing.T) {
	cfg := testutil.S3Config(t)
	h := newHarness(t, testutil.WithStorage(cfg))
	obj := h.upload("data.txt", "0123456789", api.HeaderVisibility, "public")
	etag := `"sha256:` + obj.SHA256 + `"`
	for _, test := range []struct {
		rangeValue         string
		status             int
		body, contentRange string
	}{
		{"", 200, "0123456789", ""}, {"bytes=0-2", 206, "012", "bytes 0-2/10"}, {"bytes=3-5", 206, "345", "bytes 3-5/10"}, {"bytes=-3", 206, "789", "bytes 7-9/10"}, {"bytes=5-", 206, "56789", "bytes 5-9/10"}, {"bytes=10-", 416, "", "bytes */10"}, {"bytes=bad", 416, "", "bytes */10"}, {"bytes=+2-3", 416, "", "bytes */10"}, {"bytes=0-1,3-4", 200, "0123456789", ""},
	} {
		t.Run(test.rangeValue, func(t *testing.T) {
			r := h.do("GET", "/data.txt", "", nil, "Range", test.rangeValue)
			if r.status != test.status || string(r.body) != test.body || r.header.Get("Content-Range") != test.contentRange {
				t.Fatalf("response: %d %q %v", r.status, r.body, r.header)
			}
			if r.header.Get("ETag") != etag || r.header.Get("X-Accel-Redirect") != "" {
				t.Fatal("wrong backend headers")
			}
		})
	}
	// HEAD and 304 remain available even if the backend content is missing.
	resolved, err := h.ts.Service.ResolvePublic(context.Background(), "data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ts.Storage.Backend.Delete(context.Background(), resolved.Content.Object.BlobID); err != nil {
		t.Fatal(err)
	}
	r := h.do("HEAD", "/data.txt", "", nil)
	if r.status != 200 || r.header.Get("Content-Length") != "10" || len(r.body) != 0 {
		t.Fatalf("HEAD: %d %v", r.status, r.header)
	}
	expectStatus(t, h.do("GET", "/data.txt", "", nil, "If-None-Match", etag), 304)

}

func TestR2RedirectDelivery(t *testing.T) {
	cfg := testutil.S3Config(t)
	cfg.R2.Delivery = "redirect"
	h := newHarness(t, testutil.WithStorage(cfg))
	h.upload("private/data.txt", "contents", api.HeaderVisibility, "private")

	r := h.admin("GET", api.ContentRoute("private/data.txt"), "")
	if r.status != 302 || r.header.Get("Cache-Control") != "private, no-store" || r.header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("redirect: %d %v", r.status, r.header)
	}
	u, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("X-Amz-Expires") != "300" || u.Query().Get("response-content-disposition") == "" {
		t.Fatal("missing presign policy")
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal("invalid presigned request")
	}
	downloaded, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("presigned download failed")
	}
	data, readErr := io.ReadAll(downloaded.Body)
	_ = downloaded.Body.Close()
	if readErr != nil || downloaded.StatusCode != 200 || string(data) != "contents" || downloaded.Header.Get("Content-Disposition") != u.Query().Get("response-content-disposition") || downloaded.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("presigned response overrides or content differ")
	}
	r = h.admin("POST", api.RouteLinks, `{"path":"private/data.txt","ttl":"30s"}`)
	var l api.Link
	r.json(t, &l)
	signed, _ := url.Parse(*l.URL)
	r = h.do("GET", signed.RequestURI(), "", nil)
	u, _ = url.Parse(r.header.Get("Location"))
	if r.status != 302 || u.Query().Get("X-Amz-Expires") != "30" {
		t.Fatal("R2 grant outlives Vela link")
	}
	h.ts.Clock.Advance(31 * time.Second)
	expectStatus(t, h.do("GET", signed.RequestURI(), "", nil), 410)
}

func TestChecksumMismatchDetails(t *testing.T) {
	for _, backend := range []string{"fs", "r2"} {
		t.Run(backend, func(t *testing.T) {
			var opts []testutil.Option
			if backend == "r2" {
				opts = append(opts, testutil.WithStorage(testutil.S3Config(t)))
			}
			h := newHarness(t, opts...)
			expected := strings.Repeat("0", 64)
			r := h.admin("PUT", api.ContentRoute("bad.txt"), "x", api.HeaderSHA256, expected)
			expectStatus(t, r, 400)
			var response api.ErrorResponse
			r.json(t, &response)
			if response.Error.Code != api.CodeChecksumMismatch || response.Error.Details["expected"] != expected || response.Error.Details["actual"] != "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881" {
				t.Fatalf("checksum details: %+v", response.Error)
			}
		})
	}
}

func TestEmptyContentTypeReset(t *testing.T) {
	for _, backend := range []string{"fs", "r2"} {
		t.Run(backend, func(t *testing.T) {
			var opts []testutil.Option
			if backend == "r2" {
				opts = append(opts, testutil.WithStorage(testutil.S3Config(t)))
			}
			h := newHarness(t, opts...)
			h.upload("empty.txt", "", "Content-Type", "application/octet-stream")
			r := h.admin("PATCH", api.ObjectRoute("empty.txt"), `{"content_type":null}`)
			expectStatus(t, r, 200)
			var object api.Object
			r.json(t, &object)
			if object.ContentType != "text/plain; charset=utf-8" || object.Size != 0 {
				t.Fatalf("empty metadata reset: %+v", object)
			}
		})
	}
}
