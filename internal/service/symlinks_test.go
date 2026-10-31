package service_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// ln creates a symlink as p and fails the test on error.
func (e *env) ln(p service.Principal, source, path string) api.Object {
	e.t.Helper()
	o, _, err := e.svc.CreateSymlink(e.ctx, p, api.CreateSymlinkRequest{Source: source, Path: path})
	if err != nil {
		e.t.Fatalf("ln %s %s: %v", source, path, err)
	}
	return o
}

func TestCreateSymlinkCopiesMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	orig := e.put("orig/a.txt", "hello", api.Public)
	expires := t0.Add(48 * time.Hour)
	_, err := e.svc.Patch(e.ctx, e.admin, "orig/a.txt", api.ObjectPatch{
		Title: api.Some("Title"), Description: api.Some("Desc"), Tags: api.Some([]string{"x", "y"}),
		Disposition: api.Some(api.Attachment), ExpiresAt: api.Some(expires),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	l, created, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "sorted/a.txt"})
	if err != nil || !created {
		t.Fatalf("CreateSymlink = %v, %v", created, err)
	}
	if l.Kind != api.KindSymlink || l.Title != "Title" || l.Description != "Desc" || !reflect.DeepEqual(l.Tags, []string{"x", "y"}) ||
		l.Disposition != api.Attachment || l.Visibility != api.Public || l.ExpiresAt != nil {
		t.Errorf("copied metadata = %+v", l)
	}
	if l.Size != 5 || l.SHA256 != sha("hello") || l.ContentType != orig.ContentType || l.EffectiveVisibility != api.Public || l.URL == nil {
		t.Errorf("resolved content = %+v", l)
	}
	if l.Target == nil || l.Target.ID != orig.ID || *l.Target.Path != "orig/a.txt" || l.Target.State != api.TargetOK {
		t.Errorf("target = %+v", l.Target)
	}

	// copy_meta=false takes server defaults; explicit fields win over both.
	l, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{
		Source: "orig/a.txt", Path: "sorted/b.txt", CopyMeta: ptr(false), Tags: &[]string{"z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "" || l.Visibility != api.Unlisted || !reflect.DeepEqual(l.Tags, []string{"z"}) || l.Disposition != api.Inline {
		t.Errorf("defaults = %+v", l)
	}
	l, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{
		Source: "orig/a.txt", Path: "sorted/c.txt", Title: ptr("Own"), Visibility: ptr(api.Private), ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "Own" || l.Description != "Desc" || l.Visibility != api.Private || l.URL != nil || l.ExpiresAt == nil {
		t.Errorf("explicit fields = %+v", l)
	}

	// A symlink source is dereferenced; its own metadata is what gets copied.
	_, err = e.svc.Patch(e.ctx, e.admin, "sorted/c.txt", api.ObjectPatch{Title: api.Some("Via link")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	l = e.ln(e.admin, "sorted/c.txt", "sorted/d.txt")
	if l.Target.ID != orig.ID || l.Title != "Via link" || l.Visibility != api.Private {
		t.Errorf("dereferenced = %+v", l)
	}
}

func TestCreateSymlinkEffectiveVisibility(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/u.txt", "u", api.Unlisted)
	l, _, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/u.txt", Path: "ln/u.txt", Visibility: ptr(api.Public)})
	if err != nil {
		t.Fatal(err)
	}
	if l.Visibility != api.Public || l.EffectiveVisibility != api.Unlisted || *l.Target.Visibility != api.Unlisted {
		t.Errorf("symlink = %+v", l)
	}
}

func TestCreateSymlinkErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "a", api.Public)
	e.put("orig/sub/b.txt", "b", api.Public)
	e.put("taken.txt", "t", api.Public)
	e.put("secret/s.txt", "s", api.Private)
	e.ln(e.admin, "orig/a.txt", "orig/self-link")
	e.ln(e.admin, "secret/s.txt", "pub/to-secret")
	e.put("gone.txt", "g", api.Public)
	e.ln(e.admin, "gone.txt", "broken")
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "gone.txt", Purge: true}); err != nil {
		t.Fatal(err)
	}
	rw := e.principal(api.ScopeRW, "pub/")
	for name, tc := range map[string]struct {
		p    service.Principal
		req  api.CreateSymlinkRequest
		want error
	}{
		"read-only key":         {e.principal(api.ScopeRO, ""), api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "x"}, service.ErrForbidden},
		"missing source":        {e.admin, api.CreateSymlinkRequest{Source: "nope.txt", Path: "x"}, service.ErrNotFound},
		"directory with slash":  {e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "x"}, service.ErrInvalidRequest},
		"directory no slash":    {e.admin, api.CreateSymlinkRequest{Source: "orig/sub", Path: "x"}, service.ErrInvalidRequest},
		"broken source":         {e.admin, api.CreateSymlinkRequest{Source: "broken", Path: "x"}, service.ErrNotFound},
		"taken":                 {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "taken.txt"}, service.ErrAlreadyExists},
		"own target":            {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "orig/a.txt", Overwrite: true}, service.ErrInvalidRequest},
		"own target via link":   {e.admin, api.CreateSymlinkRequest{Source: "orig/self-link", Path: "orig/a.txt", Overwrite: true}, service.ErrInvalidRequest},
		"parent is a file":      {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "taken.txt/x"}, service.ErrPathConflict},
		"path is a directory":   {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "orig/sub"}, service.ErrPathConflict},
		"reserved path":         {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "api/x"}, service.ErrInvalidPath},
		"bad metadata":          {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "x", Visibility: ptr(api.Visibility("open"))}, service.ErrInvalidRequest},
		"source outside prefix": {rw, api.CreateSymlinkRequest{Source: "secret/s.txt", Path: "pub/x"}, service.ErrForbidden},
		"path outside prefix":   {e.principal(api.ScopeRW, "secret/"), api.CreateSymlinkRequest{Source: "secret/s.txt", Path: "pub/x"}, service.ErrForbidden},
		// The source lies in the prefix, but its target does not: for this
		// key the symlink is broken.
		"target outside prefix": {rw, api.CreateSymlinkRequest{Source: "pub/to-secret", Path: "pub/x"}, service.ErrNotFound},
	} {
		_, _, err := e.svc.CreateSymlink(e.ctx, tc.p, tc.req)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v; want %v", name, err, tc.want)
		}
	}
}

func TestCreateSymlinkOverwrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.put("orig/a.txt", "aaa", api.Public)
	b := e.put("orig/b.txt", "bb", api.Public)
	l := e.ln(e.admin, "orig/a.txt", "ln/x")
	_, err := e.svc.Patch(e.ctx, e.admin, "ln/x", api.ObjectPatch{Title: api.Some("Mine")}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Retargeting keeps the ID (and so signed links) and the metadata.
	r, created, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/b.txt", Path: "ln/x", Overwrite: true})
	if err != nil || created {
		t.Fatalf("retarget = %v, %v", created, err)
	}
	if r.ID != l.ID || r.Target.ID != b.ID || r.Title != "Mine" || r.Revision != l.Revision+2 || r.Size != 2 {
		t.Errorf("retargeted = %+v", r)
	}
	// copy_meta=true copies again on retarget.
	r, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/x", Overwrite: true, CopyMeta: ptr(true)})
	if err != nil || r.Title != "" || r.Target.ID != a.ID {
		t.Errorf("retarget with copy = %+v, %v", r, err)
	}

	// Overwriting a file moves it to the trash and creates a new symlink.
	f := e.put("ln/file.txt", "f", api.Public)
	r, created, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/file.txt", Overwrite: true})
	if err != nil || !created || r.ID == f.ID {
		t.Fatalf("replace file = %+v, %v, %v", r, created, err)
	}
	trash, err := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Path: "ln/file.txt"})
	if err != nil || len(trash.Entries) != 1 || trash.Entries[0].Object.ID != f.ID {
		t.Errorf("trash = %+v, %v", trash, err)
	}
}

// readMatrix sets up the cases of docs/SPEC-symlinks.md section 3.3: one
// symlink per target situation, all public themselves.
func readMatrix(t *testing.T) *env {
	e := newEnv(t)
	e.put("orig/pub.txt", "pub", api.Public)
	e.put("orig/unl.txt", "unl", api.Unlisted)
	e.put("orig/priv.txt", "priv", api.Private)
	e.put("orig/exp.txt", "exp", api.Public)
	e.put("orig/trash.txt", "trash", api.Public)
	e.put("orig/gone.txt", "gone", api.Public)
	e.put("other/priv.txt", "other", api.Private)
	for _, name := range []string{"pub", "unl", "priv", "exp", "trash", "gone"} {
		e.ln(e.admin, "orig/"+name+".txt", "ln/"+name+".txt")
	}
	e.ln(e.admin, "other/priv.txt", "ln/other.txt")
	e.ln(e.admin, "orig/pub.txt", "ln/own-private.txt")
	e.ln(e.admin, "orig/pub.txt", "ln/own-expired.txt")
	soon := t0.Add(time.Minute)
	for path, patch := range map[string]api.ObjectPatch{
		"orig/exp.txt":       {ExpiresAt: api.Some(soon)},
		"ln/own-expired.txt": {ExpiresAt: api.Some(soon)},
		"ln/own-private.txt": {Visibility: api.Some(api.Private)},
		"ln/priv.txt":        {Visibility: api.Some(api.Public)},
		"ln/other.txt":       {Visibility: api.Some(api.Public)},
	} {
		if _, err := e.svc.Patch(e.ctx, e.admin, path, patch, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "orig/trash.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "orig/gone.txt", Purge: true}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	return e
}

func TestSymlinkPublicAccess(t *testing.T) {
	t.Parallel()
	e := readMatrix(t)
	for path, want := range map[string]string{
		"ln/pub.txt": "pub", "ln/unl.txt": "unl",
		"ln/priv.txt": "", "ln/exp.txt": "", "ln/trash.txt": "", "ln/gone.txt": "",
		"ln/other.txt": "", "ln/own-private.txt": "", "ln/own-expired.txt": "",
	} {
		res, err := e.svc.ResolvePublic(e.ctx, path)
		if want == "" {
			if !errors.Is(err, service.ErrNotFound) {
				t.Errorf("%s: %+v, %v; want not found", path, res, err)
			}
			continue
		}
		if err != nil || res.Content == nil {
			t.Errorf("%s: %+v, %v", path, res, err)
			continue
		}
		c := *res.Content
		if got := e.read(c); got != want {
			t.Errorf("%s: content %q, want %q", path, got, want)
		}
		if c.Object.Path != path || c.Object.SHA256 != sha(want) || c.Object.Visibility == api.Public && want == "unl" {
			t.Errorf("%s: delivered object = %+v", path, c.Object)
		}
	}

	// The index lists only effectively public, live symlinks.
	listing, err := e.svc.Index(e.ctx, "ln/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range listing.Files {
		names = append(names, f.Path)
	}
	if !reflect.DeepEqual(names, []string{"ln/pub.txt"}) {
		t.Errorf("index = %v", names)
	}
	if listing.Files[0].Size != 3 {
		t.Errorf("index size = %d", listing.Files[0].Size)
	}
}

func TestSymlinkAPIAccess(t *testing.T) {
	t.Parallel()
	e := readMatrix(t)
	for path, want := range map[string]struct {
		content string
		state   api.TargetState
	}{
		"ln/pub.txt":   {"pub", api.TargetOK},
		"ln/priv.txt":  {"priv", api.TargetOK},
		"ln/exp.txt":   {"exp", api.TargetOK}, // expired, not yet trashed: still rescuable
		"ln/trash.txt": {"", api.TargetTrashed},
		"ln/gone.txt":  {"", api.TargetGone},
	} {
		o, err := e.svc.Stat(e.ctx, e.admin, path)
		if err != nil || o.Target == nil || o.Target.State != want.state {
			t.Errorf("stat %s = %+v, %v", path, o.Target, err)
		}
		c, err := e.svc.Download(e.ctx, e.admin, path)
		if want.content == "" {
			if !errors.Is(err, service.ErrNotFound) {
				t.Errorf("download %s: %v; want not found", path, err)
			}
			continue
		}
		if err != nil || e.read(c) != want.content {
			t.Errorf("download %s: %v", path, err)
		}
	}
	o, _ := e.svc.Stat(e.ctx, e.admin, "ln/exp.txt")
	if !o.Target.Expired {
		t.Errorf("expired target not reported: %+v", o.Target)
	}
	o, _ = e.svc.Stat(e.ctx, e.admin, "ln/trash.txt")
	if o.Target.Path == nil || *o.Target.Path != "orig/trash.txt" || o.Size != 0 || o.SHA256 != "" || o.URL != nil {
		t.Errorf("trashed target = %+v, object %+v", o.Target, o)
	}

	// A key limited to ln/ sees targets outside its prefix as gone and
	// cannot follow them, although an admin made the symlinks.
	k := e.principal(api.ScopeRW, "ln/")
	o, err := e.svc.Stat(e.ctx, k, "ln/pub.txt")
	if err != nil || o.Target.State != api.TargetGone || o.Target.Path != nil || o.Target.Visibility != nil || o.Size != 0 || o.SHA256 != "" {
		t.Errorf("stat outside prefix = %+v, %v", o, err)
	}
	if _, err := e.svc.Download(e.ctx, k, "ln/pub.txt"); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("download outside prefix: %v", err)
	}
	if _, err := e.svc.CreateLink(e.ctx, k, api.CreateLinkRequest{Path: "ln/pub.txt"}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("share outside prefix: %v", err)
	}
	if _, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "ln/priv.txt"}); err != nil {
		t.Fatal(err)
	}
	links, err := e.svc.ListLinks(e.ctx, k, service.LinkParams{})
	if err != nil || len(links.Links) != 0 {
		t.Errorf("links of a prefixed key = %+v, %v", links, err)
	}
	links, err = e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{})
	if err != nil || len(links.Links) != 1 {
		t.Errorf("links of admin = %+v, %v", links, err)
	}
}

func TestSymlinkListingFilters(t *testing.T) {
	t.Parallel()
	e := readMatrix(t)
	paths := func(lp service.ListParams) []string {
		t.Helper()
		lp.Recursive = true
		res, err := e.svc.List(e.ctx, e.admin, lp)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, o := range res.Objects {
			out = append(out, o.Path)
		}
		return out
	}
	if got := paths(service.ListParams{Prefix: "ln/", Broken: true}); !reflect.DeepEqual(got, []string{"ln/gone.txt", "ln/trash.txt"}) {
		t.Errorf("broken = %v", got)
	}
	if got := paths(service.ListParams{Kind: api.KindFile}); len(got) != 5 {
		t.Errorf("files = %v", got)
	}
	if got := paths(service.ListParams{Target: "orig/pub.txt"}); !reflect.DeepEqual(got, []string{"ln/own-expired.txt", "ln/own-private.txt", "ln/pub.txt"}) {
		t.Errorf("target = %v", got)
	}
	if got := paths(service.ListParams{Target: "orig/missing.txt"}); len(got) != 0 {
		t.Errorf("missing target = %v", got)
	}
	if got := paths(service.ListParams{Prefix: "ln/", Visibilities: []api.Visibility{api.Unlisted}}); !reflect.DeepEqual(got, []string{"ln/unl.txt"}) {
		t.Errorf("effective unlisted = %v", got)
	}
	if _, err := e.svc.List(e.ctx, e.principal(api.ScopeRO, "ln/"), service.ListParams{Prefix: "ln/", Target: "orig/pub.txt"}); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("target outside prefix: %v", err)
	}
	if _, err := e.svc.List(e.ctx, e.admin, service.ListParams{Kind: "dir"}); !errors.Is(err, service.ErrInvalidRequest) {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestSymlinkSignedLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "aaa", api.Private)
	e.put("orig/b.txt", "bb", api.Private)
	e.ln(e.admin, "orig/a.txt", "ln/x")
	link, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "ln/x"})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func() (string, error) {
		c, err := e.svc.ResolveLink(e.ctx, link.ID, queryParam(*link.URL, "exp"), queryParam(*link.URL, "sig"))
		if err != nil {
			return "", err
		}
		if c.Object.Path != "ln/x" {
			t.Errorf("delivered path = %s", c.Object.Path)
		}
		return e.read(c), nil
	}
	// Visibility does not matter for signed links.
	if got, err := resolve(); err != nil || got != "aaa" {
		t.Errorf("resolve = %q, %v", got, err)
	}
	if _, _, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/b.txt", Path: "ln/x", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(); err != nil || got != "bb" {
		t.Errorf("after retarget = %q, %v", got, err)
	}
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "orig/b.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("broken: %v", err)
	}
}

func TestSymlinkWebSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("k/own.txt", "own", api.Private)
	e.put("other/priv.txt", "priv", api.Private)
	e.put("other/pub.txt", "pub", api.Public)
	e.ln(e.admin, "k/own.txt", "k/to-own")
	e.ln(e.admin, "other/priv.txt", "k/to-priv")
	e.ln(e.admin, "other/pub.txt", "k/to-pub")
	e.ln(e.admin, "other/priv.txt", "elsewhere/to-priv")
	if _, err := e.svc.Patch(e.ctx, e.admin, "elsewhere/to-priv", api.ObjectPatch{Visibility: api.Some(api.Public)}, 0); err != nil {
		t.Fatal(err)
	}
	viewer := e.principal(api.ScopeRO, "k/")
	for path, ok := range map[string]bool{
		"k/to-own": true, "k/to-pub": true, "k/to-priv": false, "elsewhere/to-priv": false,
	} {
		res, err := e.svc.ResolvePublicFor(e.ctx, path, &viewer)
		if ok != (err == nil && res.Content != nil) {
			t.Errorf("%s: %+v, %v; want served=%v", path, res, err, ok)
		}
		if !ok && !errors.Is(err, service.ErrNotFound) {
			t.Errorf("%s: %v; want not found", path, err)
		}
	}
	listing, err := e.svc.IndexFor(e.ctx, "k/", &viewer)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range listing.Files {
		names = append(names, f.Path)
	}
	if !reflect.DeepEqual(names, []string{"k/own.txt", "k/to-own", "k/to-pub"}) {
		t.Errorf("session index = %v", names)
	}
}

func TestSymlinkWriteGuards(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "a", api.Public)
	e.ln(e.admin, "orig/a.txt", "ln/a")
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "ln/a", Body: stringsReader("x"), ContentLength: 1})
	wantErr(t, err, service.ErrIsSymlink)
	_, err = e.svc.Create(e.ctx, e.admin, api.CreateObjectRequest{Path: "ln/a", Content: "x"})
	wantErr(t, err, service.ErrIsSymlink)
	_, err = e.svc.Patch(e.ctx, e.admin, "ln/a", api.ObjectPatch{ContentType: api.Some("text/html")}, 0)
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Patch(e.ctx, e.admin, "ln/a", api.ObjectPatch{ContentType: api.Null[string]()}, 0)
	wantErr(t, err, service.ErrInvalidRequest)
	if o, err := e.svc.Patch(e.ctx, e.admin, "ln/a", api.ObjectPatch{Title: api.Some("T")}, 0); err != nil || o.Title != "T" {
		t.Errorf("patch title = %+v, %v", o, err)
	}
	// The target was not touched.
	if c, err := e.svc.Download(e.ctx, e.admin, "orig/a.txt"); err != nil || e.read(c) != "a" {
		t.Errorf("target = %v", err)
	}
}

func TestSymlinkTargetLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "one", api.Public)
	e.ln(e.admin, "orig/a.txt", "ln/a")
	e.ln(e.admin, "orig/a.txt", "ln/b")
	e.ln(e.admin, "orig/a.txt", "other/c")
	content := func(path string) string {
		t.Helper()
		res, err := e.svc.ResolvePublic(e.ctx, path)
		if err != nil {
			return ""
		}
		return e.read(*res.Content)
	}

	// Overwrite: same ID, new content and ETag source.
	e.put("orig/a.txt", "two", api.Public)
	if got := content("ln/a"); got != "two" {
		t.Errorf("after overwrite = %q", got)
	}
	// Move, also recursively.
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "orig/a.txt", To: "moved/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "moved/", To: "again/", Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if o, _ := e.svc.Stat(e.ctx, e.admin, "ln/a"); *o.Target.Path != "again/a.txt" || content("ln/a") != "two" {
		t.Errorf("after move = %+v", o.Target)
	}
	// Moving the symlinks leaves the target alone.
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "ln/", To: "sorted/", Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if content("sorted/a") != "two" {
		t.Error("moved symlink lost its target")
	}

	// Trash: the delete reports the symlinks in the key's prefix only.
	k := e.principal(api.ScopeRW, "again/")
	resp, err := e.svc.Delete(e.ctx, k, service.DeleteParams{Path: "again/a.txt"})
	if err != nil || resp.SymlinksAffected != 0 {
		t.Errorf("delete by prefixed key = %+v, %v", resp, err)
	}
	if content("sorted/a") != "" {
		t.Error("symlink to a trashed file still served")
	}
	// Restore to another path: same ID, working again.
	if _, err := e.svc.Restore(e.ctx, e.admin, resp.TrashIDs[0], api.RestoreRequest{Path: "back/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if content("sorted/a") != "two" {
		t.Error("restore did not repair the symlink")
	}
	resp, err = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "back/a.txt"})
	if err != nil || resp.SymlinksAffected != 3 {
		t.Errorf("delete by admin = %+v, %v", resp, err)
	}
	// Purge: gone for good, the symlinks stay broken.
	purge, err := e.svc.PurgeTrash(e.ctx, e.admin, nil)
	if err != nil || purge.Purged != 1 || purge.SymlinksAffected != 3 {
		t.Errorf("purge = %+v, %v", purge, err)
	}
	if o, err := e.svc.Stat(e.ctx, e.admin, "sorted/a"); err != nil || o.Target.State != api.TargetGone {
		t.Errorf("after purge = %+v, %v", o, err)
	}

	// Deleting a directory with a file and its symlinks counts only the
	// symlinks that stay behind.
	e.put("dir/f.txt", "f", api.Public)
	e.ln(e.admin, "dir/f.txt", "dir/l")
	e.ln(e.admin, "dir/f.txt", "keep/l")
	resp, err = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "dir/", Recursive: true, Purge: true})
	if err != nil || resp.SymlinksAffected != 1 || resp.Deleted != 2 {
		t.Errorf("recursive purge = %+v, %v", resp, err)
	}
}

func TestSymlinkPurgeAndExpiry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "a", api.Public)
	l := e.ln(e.admin, "orig/a.txt", "ln/a")
	soon := t0.Add(time.Minute)
	if _, err := e.svc.Patch(e.ctx, e.admin, "ln/a", api.ObjectPatch{ExpiresAt: api.Some(soon)}, 0); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	if _, err := e.svc.RunJanitor(e.ctx); err != nil {
		t.Fatal(err)
	}
	trash, err := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Path: "ln/a"})
	if err != nil || len(trash.Entries) != 1 || trash.Entries[0].Object.ID != l.ID || trash.Entries[0].Object.Kind != api.KindSymlink {
		t.Fatalf("janitor did not trash the expired symlink: %+v, %v", trash, err)
	}
	// Purging a symlink must never queue (or delete) its target's blob.
	if err := e.svc.PurgeTrashEntry(e.ctx, e.admin, trash.Entries[0].ID); err != nil {
		t.Fatal(err)
	}
	e.collectAfterGrace()
	if c, err := e.svc.Download(e.ctx, e.admin, "orig/a.txt"); err != nil || e.read(c) != "a" {
		t.Errorf("target content after purging its symlink: %v", err)
	}
	// A purged symlink via DELETE ?purge as well.
	e.ln(e.admin, "orig/a.txt", "ln/b")
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "ln/b", Purge: true}); err != nil {
		t.Fatal(err)
	}
	e.collectAfterGrace()
	if c, err := e.svc.Download(e.ctx, e.admin, "orig/a.txt"); err != nil || e.read(c) != "a" {
		t.Errorf("target content after purging a symlink directly: %v", err)
	}
}

func TestCreateSymlinkTree(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.put("orig/a.txt", "a", api.Public)
	e.put("orig/sub/b.txt", "b", api.Public)
	e.put("gone.txt", "g", api.Public)
	e.ln(e.admin, "gone.txt", "orig/broken")
	e.ln(e.admin, "orig/a.txt", "orig/sub/link-to-a")
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "gone.txt", Purge: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.PutDir(e.ctx, e.admin, "orig/sub/", api.DirPolicyRequest{Hidden: true}, service.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "sorted/", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 3 || res.Replaced != 0 || !reflect.DeepEqual(res.Skipped, []string{"orig/broken"}) {
		t.Errorf("tree = %+v", res)
	}
	o, err := e.svc.Stat(e.ctx, e.admin, "sorted/sub/link-to-a")
	if err != nil || o.Target.ID != a.ID {
		t.Errorf("dereferenced = %+v, %v", o, err)
	}
	if dir, err := e.svc.ShowDir(e.ctx, e.admin, "sorted/sub/"); err != nil || dir.Rule != nil {
		t.Errorf("directory policy was copied: %+v, %v", dir.Rule, err)
	}

	// Conflicts fail the whole request.
	e.put("other/a.txt", "x", api.Public)
	e.put("clash/sub", "file in the way", api.Public)
	_, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "other/", Recursive: true})
	wantErr(t, err, service.ErrAlreadyExists)
	if res, _ := e.svc.List(e.ctx, e.admin, service.ListParams{Prefix: "other/", Recursive: true}); len(res.Objects) != 1 {
		t.Errorf("partial tree created: %+v", res.Objects)
	}
	_, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "clash/", Recursive: true, Overwrite: true})
	wantErr(t, err, service.ErrPathConflict)

	// Overwrite retargets symlinks and trashes files.
	res, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "other/", Recursive: true, Overwrite: true})
	if err != nil || res.Created != 2 || res.Replaced != 1 {
		t.Errorf("overwrite = %+v, %v", res, err)
	}
	res, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "other/", Recursive: true, Overwrite: true})
	if err != nil || res.Created != 0 || res.Replaced != 3 {
		t.Errorf("second overwrite = %+v, %v", res, err)
	}

	for name, tc := range map[string]struct {
		p    service.Principal
		req  api.CreateSymlinkRequest
		want error
	}{
		"into itself":   {e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "orig/"}, service.ErrInvalidRequest},
		"empty source":  {e.admin, api.CreateSymlinkRequest{Source: "nothing/", Path: "x/"}, service.ErrNotFound},
		"source prefix": {e.principal(api.ScopeRW, "sorted/"), api.CreateSymlinkRequest{Source: "orig/", Path: "sorted/x/"}, service.ErrForbidden},
		"path prefix":   {e.principal(api.ScopeRW, "orig/"), api.CreateSymlinkRequest{Source: "orig/sub/", Path: "x/"}, service.ErrForbidden},
		"reserved path": {e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "api/"}, service.ErrInvalidPath},
	} {
		tc.req.Recursive = true
		if _, err := e.svc.CreateSymlinkTree(e.ctx, tc.p, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v; want %v", name, err, tc.want)
		}
	}

	// A prefixed key skips symlinks whose target is outside its prefix.
	e.put("secret/s.txt", "s", api.Private)
	e.ln(e.admin, "secret/s.txt", "k/src/to-secret")
	e.put("k/src/own.txt", "o", api.Private)
	res, err = e.svc.CreateSymlinkTree(e.ctx, e.principal(api.ScopeRW, "k/"), api.CreateSymlinkRequest{Source: "k/src/", Path: "k/dst/", Recursive: true})
	if err != nil || res.Created != 1 || !reflect.DeepEqual(res.Skipped, []string{"k/src/to-secret"}) {
		t.Errorf("prefixed tree = %+v, %v", res, err)
	}
}

func TestCreateSymlinkTreeNested(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("k/a.txt", "a", api.Public)
	e.put("k/sub/b.txt", "b", api.Public)
	req := api.CreateSymlinkRequest{Source: "k/", Path: "k/all/", Recursive: true}
	res, err := e.svc.CreateSymlinkTree(e.ctx, e.admin, req)
	if err != nil || res.Created != 2 {
		t.Fatalf("nested tree = %+v, %v", res, err)
	}
	// A rerun ignores what the first run created: no k/all/all/.
	req.Overwrite = true
	res, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, req)
	if err != nil || res.Created != 0 || res.Replaced != 2 {
		t.Fatalf("rerun = %+v, %v", res, err)
	}
	if l, _ := e.svc.List(e.ctx, e.admin, service.ListParams{Prefix: "k/all/all/", Recursive: true}); len(l.Objects) != 0 {
		t.Errorf("destination was walked: %+v", l.Objects)
	}
	// A source holding nothing but the destination is empty.
	e.ln(e.admin, "k/a.txt", "only/dst/a")
	_, err = e.svc.CreateSymlinkTree(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "only/", Path: "only/dst/", Recursive: true})
	wantErr(t, err, service.ErrNotFound)
}

func TestSymlinkIndexFlags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "a", api.Public)
	e.ln(e.admin, "orig/a.txt", "pub/a.txt")
	viewer := e.principal(api.ScopeRO, "")
	listing, err := e.svc.IndexFor(e.ctx, "pub/", &viewer)
	if err != nil || len(listing.Files) != 1 || !listing.Files[0].IsSymlink() {
		t.Fatalf("session index = %+v, %v", listing.Files, err)
	}
}
