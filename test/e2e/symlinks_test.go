//go:build e2e

package e2e

import (
	"context"
	"net/url"
	"testing"

	"go.xchunk.org/vela/api"
	velaclient "go.xchunk.org/vela/client"
)

// TestSymlinksThroughNginx delivers symlinks in accel mode: nginx serves the
// target's blob under the symlink's name and headers, every header arrives
// exactly once, and refusals look like any missing path.
func TestSymlinksThroughNginx(t *testing.T) {
	_, n, c := setup(t)
	ctx := context.Background()
	orig := upload(t, c, "orig/report.pdf", "%PDF-1.4 symlinked", api.Public, api.Inline)
	upload(t, c, "orig/unlisted.txt", "unlisted target", api.Unlisted, "")
	upload(t, c, "orig/private.txt", "private target", api.Private, "")
	for _, req := range []api.CreateSymlinkRequest{
		{Source: "orig/report.pdf", Path: "sorted/copy.pdf", Disposition: ptr(api.Attachment)},
		{Source: "orig/unlisted.txt", Path: "sorted/unlisted.txt", Visibility: ptr(api.Public)},
		{Source: "orig/private.txt", Path: "sorted/private.txt", Visibility: ptr(api.Public)},
	} {
		if _, _, err := c.CreateSymlink(ctx, req); err != nil {
			t.Fatalf("symlink %s: %v", req.Path, err)
		}
	}

	r := n.get("GET", n.https+"/sorted/copy.pdf")
	if r.status != 200 || r.body != "%PDF-1.4 symlinked" {
		t.Fatalf("symlink: status %d body %q", r.status, r.body)
	}
	expectHeaders(t, "symlink", r, map[string]string{
		"Content-Type":        "application/pdf",
		"Content-Disposition": `attachment; filename="copy.pdf"; filename*=UTF-8''copy.pdf`,
		"Cache-Control":       "public, max-age=300",
		"ETag":                `"sha256:` + orig.SHA256 + `"`,
		"X-Robots-Tag":        "",
	})
	for _, h := range []string{"Content-Type", "Cache-Control", "ETag", "X-Content-Type-Options", "Strict-Transport-Security"} {
		if got := len(r.header.Values(h)); got != 1 {
			t.Errorf("%s appears %d times", h, got)
		}
	}
	if r := n.get("GET", n.https+"/sorted/copy.pdf", "If-None-Match", `"sha256:`+orig.SHA256+`"`); r.status != 304 {
		t.Errorf("revalidation: %d", r.status)
	}
	if r := n.get("GET", n.https+"/sorted/copy.pdf", "Range", "bytes=0-7"); r.status != 206 || r.body != "%PDF-1.4" {
		t.Errorf("range: %d %q", r.status, r.body)
	}

	r = n.get("GET", n.https+"/sorted/unlisted.txt")
	if r.status != 200 || r.body != "unlisted target" {
		t.Fatalf("unlisted target: %d %q", r.status, r.body)
	}
	expectHeaders(t, "unlisted target", r, map[string]string{"X-Robots-Tag": "noindex, nofollow"})

	missing := n.get("GET", n.https+"/sorted/none.txt")
	if r := n.get("GET", n.https+"/sorted/private.txt"); r.status != 404 || r.body != missing.body {
		t.Errorf("public symlink to a private file: %d", r.status)
	}

	// A signed link to the symlink serves the private target.
	l, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "sorted/private.txt", TTL: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(*l.URL)
	r = n.get("GET", n.https+u.RequestURI())
	if r.status != 200 || r.body != "private target" {
		t.Fatalf("signed link: %d %q", r.status, r.body)
	}
	expectHeaders(t, "signed link", r, map[string]string{"Cache-Control": "private, no-store", "Referrer-Policy": "no-referrer"})

	// A broken symlink is a plain 404.
	if _, err := c.Delete(ctx, "orig/report.pdf", velaclient.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if r := n.get("GET", n.https+"/sorted/copy.pdf"); r.status != 404 || r.body != missing.body {
		t.Errorf("broken symlink: %d", r.status)
	}
}

func ptr[T any](v T) *T { return &v }
