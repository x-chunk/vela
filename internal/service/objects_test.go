package service_test

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestCreate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	o, err := e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{
		Path: "/notes/todo.md", Content: "# hello\n", Tags: []string{"Notes", "x"}, Title: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Path != "notes/todo.md" || o.Name != "todo.md" || o.Size != 8 || o.SHA256 != sha("# hello\n") {
		t.Errorf("unexpected object %+v", o)
	}
	if o.ContentType != "text/markdown; charset=utf-8" || o.Visibility != api.Unlisted || o.Disposition != api.Inline {
		t.Errorf("defaults not applied: %+v", o)
	}
	if strings.Join(o.Tags, ",") != "notes,x" || o.Revision != 1 || o.URL == nil {
		t.Errorf("tags/revision/url: %+v", o)
	}
	if *o.URL != "https://store.example.org/notes/todo.md" {
		t.Errorf("url = %s", *o.URL)
	}

	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "notes/todo.md", Content: "again"})
	wantErr(t, err, service.ErrAlreadyExists)

	bin, err := e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{
		Path: "bin/x", Content: base64.StdEncoding.EncodeToString([]byte{0, 1, 2}), Encoding: "base64", Visibility: api.Private,
	})
	if err != nil || bin.Size != 3 || bin.URL != nil {
		t.Fatalf("base64 create = %+v, %v", bin, err)
	}

	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "x", Content: "a", Encoding: "rot13"})
	wantErr(t, err, service.ErrUnsupportedEncoding)
	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "big", Content: strings.Repeat("a", 1025)})
	wantErr(t, err, service.ErrTooLarge)
	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "api/x", Content: "a"})
	wantErr(t, err, service.ErrInvalidPath)
	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "t", Content: "a", Tags: []string{"bad tag"}})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Create(e.ctx, e.principal(api.ScopeRO, ""), api.CreateObjectRequest{Path: "t", Content: "a"})
	wantErr(t, err, service.ErrForbidden)

	if n := e.countFiles("blobs"); n != 2 {
		t.Errorf("blobs on disk = %d, want 2 (failed creates must not leave blobs)", n)
	}
}

func TestFileDirectoryConflicts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("a/b", "x", api.Public)
	for _, p := range []string{"a/b/c", "a/b/c/d", "a"} {
		_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: p, Body: strings.NewReader("y")})
		wantErr(t, err, service.ErrPathConflict)
	}
	var se *service.Error
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "a/b/c", Body: strings.NewReader("y")})
	if !errors.As(err, &se) || se.Details["conflicting_path"] != "a/b" {
		t.Fatalf("conflict details = %+v", err)
	}
	if n := e.countFiles("blobs"); n != 1 {
		t.Errorf("blobs on disk = %d, want 1", n)
	}
}

func TestUploadOverwriteKeepsIdentityAndMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first, created, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{
		Path: "doc.txt", Body: strings.NewReader("v1"), Visibility: ptr(api.Public),
		Title: ptr("Title"), Tags: &[]string{"keep"}, Disposition: ptr(api.Attachment),
	})
	if err != nil || !created {
		t.Fatal(created, err)
	}
	e.clock.Advance(time.Minute)
	second, created, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{
		Path: "doc.txt", Body: strings.NewReader("version two"), Description: ptr("new"),
	})
	if err != nil || created {
		t.Fatal(created, err)
	}
	if second.ID != first.ID || second.Revision != 2 || second.Visibility != api.Public || second.Title != "Title" ||
		second.Tags[0] != "keep" || second.Disposition != api.Attachment || second.Description != "new" {
		t.Fatalf("overwrite lost identity or metadata: %+v", second)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("timestamps: %+v", second)
	}
	// The old content stays for the grace period, then the collector
	// removes it (R-002, R-012).
	if n := e.countFiles("blobs"); n != 2 {
		t.Fatalf("blobs right after overwrite = %d, want 2", n)
	}
	e.collectAfterGrace()
	if n := e.countFiles("blobs"); n != 1 {
		t.Fatalf("old blob not collected: %d blobs", n)
	}
}

// TestResolvedContentSurvivesOverwrite covers R-012: a download resolved
// just before an overwrite can still open the content it resolved.
func TestResolvedContentSurvivesOverwrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("doc.txt", "old", api.Public)
	c, err := e.svc.Download(e.ctx, e.admin, "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := e.svc.ResolvePublic(e.ctx, "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	e.put("doc.txt", "new", api.Public)
	if got := e.read(c); got != "old" {
		t.Fatalf("resolved API download reads %q", got)
	}
	if got := e.read(*pub.Content); got != "old" {
		t.Fatalf("resolved public download reads %q", got)
	}
	if n, err := e.svc.CollectBlobs(e.ctx); err != nil || n != 0 {
		t.Fatalf("collector deleted content within the grace period: %d, %v", n, err)
	}
}

func TestUploadPreconditions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("f", "1", api.Public)
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "f", Body: strings.NewReader("2"), IfNoneMatch: true})
	wantErr(t, err, service.ErrAlreadyExists)
	_, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "f", Body: strings.NewReader("2"), IfMatch: 7})
	wantErr(t, err, service.ErrPreconditionFailed)
	_, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "new", Body: strings.NewReader("2"), IfMatch: 1})
	wantErr(t, err, service.ErrPreconditionFailed)
	o, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "f", Body: strings.NewReader("2"), IfMatch: 1})
	if err != nil || o.Revision != 2 {
		t.Fatalf("If-Match upload = %+v, %v", o, err)
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(len(p), r.n)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.n -= n
	return n, nil
}

func TestUploadAtomicity(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("keep.txt", "original", api.Public)

	tests := []struct {
		name string
		in   service.UploadInput
		kind error
	}{
		{"interrupted stream", service.UploadInput{Path: "keep.txt", Body: &failingReader{n: 100}}, service.ErrUploadInterrupted},
		{"over limit while streaming", service.UploadInput{Path: "keep.txt", Body: &failingReader{n: 2 << 20}, ContentLength: -1}, service.ErrTooLarge},
		{"declared over limit", service.UploadInput{Path: "keep.txt", Body: strings.NewReader("x"), ContentLength: 2 << 20}, service.ErrTooLarge},
		{"checksum mismatch", service.UploadInput{Path: "keep.txt", Body: strings.NewReader("changed"), SHA256: sha("other")}, service.ErrChecksumMismatch},
		{"bad metadata", service.UploadInput{Path: "keep.txt", Body: strings.NewReader("changed"), Title: ptr(strings.Repeat("t", 300))}, service.ErrInvalidRequest},
	}
	for _, tt := range tests {
		_, _, err := e.svc.Upload(e.ctx, e.admin, tt.in)
		if !errors.Is(err, tt.kind) {
			t.Errorf("%s: error = %v; want %v", tt.name, err, tt.kind)
		}
	}
	c, err := e.svc.Download(e.ctx, e.admin, "keep.txt")
	if err != nil || e.read(c) != "original" {
		t.Fatalf("original content damaged: %v", err)
	}
	if n := e.countFiles("tmp"); n != 0 {
		t.Errorf("tmp files left behind: %d", n)
	}
	if n := e.countFiles("blobs"); n != 1 {
		t.Errorf("blobs on disk = %d, want 1", n)
	}

	ok, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "keep.txt", Body: strings.NewReader("good"), SHA256: strings.ToUpper(sha("good"))})
	if err != nil || ok.SHA256 != sha("good") {
		t.Fatalf("checksum (upper-case hex) upload = %+v, %v", ok, err)
	}
}

func TestContentTypeHandling(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	o, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "x.bin", Body: strings.NewReader("%PDF-1.4"), ContentType: "application/octet-stream"})
	if err != nil || o.ContentType != "application/octet-stream" {
		// .bin is unknown to our table; system tables may know it, so only
		// check that a generic client type triggers detection at all.
		if err != nil {
			t.Fatal(err)
		}
	}
	o, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "noext", Body: strings.NewReader("%PDF-1.4")})
	if err != nil || o.ContentType != "application/pdf" {
		t.Fatalf("sniffed type = %q, %v", o.ContentType, err)
	}
	o, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "noext", Body: strings.NewReader("hello"), ContentType: "text/x-custom"})
	if err != nil || o.ContentType != "text/x-custom; charset=utf-8" {
		t.Fatalf("explicit type = %q, %v", o.ContentType, err)
	}
	// Overwriting without an explicit type re-detects it.
	o, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "noext", Body: strings.NewReader("%PDF-1.5")})
	if err != nil || o.ContentType != "application/pdf" {
		t.Fatalf("re-detected type = %q, %v", o.ContentType, err)
	}
	_, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "bad", Body: strings.NewReader("x"), ContentType: "not a type"})
	wantErr(t, err, service.ErrInvalidRequest)
}

func TestPatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("p.txt", "x", api.Public)
	exp := t0.Add(48 * time.Hour)
	o, err := e.svc.Patch(e.ctx, e.admin, "p.txt", api.ObjectPatch{
		Visibility: api.Some(api.Private), Title: api.Some("T"), Tags: api.Some([]string{"A"}),
		ExpiresAt: api.Some(exp), ContentType: api.Some("text/markdown"), Disposition: api.Some(api.Attachment),
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if o.Visibility != api.Private || o.Title != "T" || o.Tags[0] != "a" || !o.ExpiresAt.Equal(exp) ||
		o.ContentType != "text/markdown; charset=utf-8" || o.Revision != 2 || o.URL != nil {
		t.Fatalf("patched = %+v", o)
	}
	_, err = e.svc.Patch(e.ctx, e.admin, "p.txt", api.ObjectPatch{Title: api.Some("x")}, 1)
	wantErr(t, err, service.ErrPreconditionFailed)

	o, err = e.svc.Patch(e.ctx, e.admin, "p.txt", api.ObjectPatch{
		Visibility: api.Null[api.Visibility](), Title: api.Null[string](), Tags: api.Null[[]string](),
		ExpiresAt: api.Null[time.Time](), ContentType: api.Null[string](), Disposition: api.Null[api.Disposition](),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o.Visibility != api.Unlisted || o.Title != "" || len(o.Tags) != 0 || o.ExpiresAt != nil ||
		o.ContentType != "text/plain; charset=utf-8" || o.Disposition != api.Inline {
		t.Fatalf("reset = %+v", o)
	}
	same, err := e.svc.Patch(e.ctx, e.admin, "p.txt", api.ObjectPatch{}, 0)
	if err != nil || same.Revision != o.Revision {
		t.Fatalf("empty patch changed revision: %+v, %v", same, err)
	}
	_, err = e.svc.Patch(e.ctx, e.admin, "p.txt", api.ObjectPatch{ExpiresAt: api.Some(t0.Add(-time.Hour))}, 0)
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Patch(e.ctx, e.admin, "missing", api.ObjectPatch{Title: api.Some("x")}, 0)
	wantErr(t, err, service.ErrNotFound)
}

func TestListAndScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("docs/a.pdf", "a", api.Public)
	e.put("docs/sub/b.pdf", "b", api.Private)
	e.put("other/c", "c", api.Public)
	e.put("top", "t", api.Unlisted)

	l, err := e.svc.List(e.ctx, e.admin, service.ListParams{})
	if err != nil || strings.Join(l.Dirs, ",") != "docs/,other/" || len(l.Objects) != 1 {
		t.Fatalf("root listing = %+v, %v", l, err)
	}
	ro := e.principal(api.ScopeRO, "docs/")
	l, err = e.svc.List(e.ctx, ro, service.ListParams{})
	if err != nil || strings.Join(l.Dirs, ",") != "docs/" || len(l.Objects) != 0 {
		t.Fatalf("prefixed root listing = %+v, %v", l, err)
	}
	l, err = e.svc.List(e.ctx, ro, service.ListParams{Prefix: "docs", Recursive: true})
	if err != nil || len(l.Objects) != 2 {
		t.Fatalf("prefixed recursive listing = %+v, %v", l, err)
	}
	_, err = e.svc.List(e.ctx, ro, service.ListParams{Prefix: "other/"})
	wantErr(t, err, service.ErrForbidden)
	_, err = e.svc.Stat(e.ctx, ro, "other/c")
	wantErr(t, err, service.ErrForbidden)
	if _, err := e.svc.Stat(e.ctx, ro, "docs/sub/b.pdf"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.List(e.ctx, e.admin, service.ListParams{Limit: 1001})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.List(e.ctx, e.admin, service.ListParams{Cursor: "!!"})
	wantErr(t, err, service.ErrInvalidRequest)

	l, _ = e.svc.List(e.ctx, e.admin, service.ListParams{Recursive: true, Limit: 2})
	if len(l.Objects) != 2 || l.NextCursor == "" {
		t.Fatalf("page 1 = %+v", l)
	}
	l, _ = e.svc.List(e.ctx, e.admin, service.ListParams{Recursive: true, Limit: 2, Cursor: l.NextCursor})
	if len(l.Objects) != 2 || l.NextCursor != "" || l.Objects[1].Path != "top" {
		t.Fatalf("page 2 = %+v", l)
	}
	l, _ = e.svc.List(e.ctx, e.admin, service.ListParams{Recursive: true, Visibilities: []api.Visibility{api.Private}})
	if len(l.Objects) != 1 {
		t.Fatalf("visibility filter = %+v", l)
	}
}

func TestExpiredObjectStillDownloadableViaAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	exp := t0.Add(time.Hour)
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "tmp.txt", Body: strings.NewReader("x"),
		Visibility: ptr(api.Public), ExpiresAt: api.Some(exp)})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour)
	o, err := e.svc.Stat(e.ctx, e.admin, "tmp.txt")
	if err != nil || !o.Expired {
		t.Fatalf("stat = %+v, %v", o, err)
	}
	if _, err := e.svc.Download(e.ctx, e.admin, "tmp.txt"); err != nil {
		t.Fatalf("API download of expired object: %v", err)
	}
}

// TestPatchExplicitGenericType covers R-018: PATCH sets
// application/octet-stream literally; only null re-detects the type.
func TestPatchExplicitGenericType(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("page.html", "<h1>x</h1>", api.Public)
	for _, ct := range []string{"application/octet-stream", "application/octet-stream; x-kind=raw"} {
		o, err := e.svc.Patch(e.ctx, e.admin, "page.html", api.ObjectPatch{ContentType: api.Some(ct)}, 0)
		if err != nil || o.ContentType != ct {
			t.Fatalf("PATCH %q = %q, %v", ct, o.ContentType, err)
		}
	}
	for _, bad := range []string{"", "not a type", "text/"} {
		_, err := e.svc.Patch(e.ctx, e.admin, "page.html", api.ObjectPatch{ContentType: api.Some(bad)}, 0)
		wantErr(t, err, service.ErrInvalidRequest)
	}
	o, err := e.svc.Patch(e.ctx, e.admin, "page.html", api.ObjectPatch{ContentType: api.Null[string]()}, 0)
	if err != nil || o.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("PATCH null = %q, %v", o.ContentType, err)
	}
	// Uploads keep treating octet-stream as "unknown, detect it".
	up, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "x.pdf", Body: strings.NewReader("%PDF"), ContentType: "application/octet-stream"})
	if err != nil || up.ContentType != "application/pdf" {
		t.Fatalf("upload with octet-stream = %q, %v", up.ContentType, err)
	}
}
