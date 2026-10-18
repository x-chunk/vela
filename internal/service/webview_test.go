package service_test

import (
	"sort"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func (e *env) dirRule(prefix string, patch api.DirPolicyPatch) {
	e.t.Helper()
	if _, err := e.svc.PatchDir(e.ctx, e.admin, prefix, patch, service.DirWriteOptions{}); err != nil {
		e.t.Fatalf("policy %s: %v", prefix, err)
	}
}

func (e *env) indexFor(prefix string, viewer *service.Principal) service.IndexListing {
	e.t.Helper()
	l, err := e.svc.IndexFor(e.ctx, prefix, viewer)
	if err != nil {
		e.t.Fatalf("index %q: %v", prefix, err)
	}
	return l
}

func dirPaths(l service.IndexListing) []string {
	out := []string{}
	for _, d := range l.Dirs {
		out = append(out, d.Path)
	}
	sort.Strings(out)
	return out
}

func filePaths(l service.IndexListing) []string {
	out := []string{}
	for _, o := range l.Files {
		out = append(out, o.Path)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findDir(t *testing.T, l service.IndexListing, path string) service.IndexDir {
	t.Helper()
	for _, d := range l.Dirs {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("no row %s in %v", path, dirPaths(l))
	return service.IndexDir{}
}

// Acceptance checks 2 and 4 of docs/SPEC-web.md.
func TestSessionPrefixView(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("pub.txt", "p", api.Public)
	e.put("test/a.txt", "a", api.Private)
	e.put("test/b.txt", "b", api.Unlisted)
	e.put("test/c.txt", "c", api.Public)
	e.put("test/sub/d.txt", "d", api.Private)
	e.put("test/raw/e.txt", "e", api.Private)
	e.put("secret/x.txt", "x", api.Private)
	e.put("open/y.txt", "y", api.Public)
	e.put("test2/z.txt", "z", api.Public)
	e.put("test2/w.txt", "w", api.Private)
	hidden := api.DirPolicyPatch{Hidden: api.Some(true)}
	e.dirRule("test/", hidden)
	e.dirRule("secret/", hidden)
	e.dirRule("open/", hidden)
	e.dirRule("test2/", hidden)
	e.dirRule("test/raw/", api.DirPolicyPatch{Index: api.Some(api.IndexOff)})
	viewer := &service.Principal{KeyID: "k", Scope: api.ScopeRO, Prefix: "test/"}

	root := e.indexFor("", viewer)
	if !equal(dirPaths(root), []string{"test/"}) || !equal(filePaths(root), []string{"pub.txt"}) {
		t.Fatalf("root = %v %v", dirPaths(root), filePaths(root))
	}
	entrance := findDir(t, root, "test/")
	// Five entries on test/: three files, sub/ and raw/.
	if !entrance.Hidden || entrance.IndexOff || entrance.Count != 5 {
		t.Fatalf("entrance row = %+v", entrance)
	}
	if anon := e.indexFor("", nil); len(anon.Dirs) != 0 {
		t.Fatalf("anonymous root lists %v", dirPaths(anon))
	}

	inside := e.indexFor("test/", viewer)
	if !equal(filePaths(inside), []string{"test/a.txt", "test/b.txt", "test/c.txt"}) || !equal(dirPaths(inside), []string{"test/raw/", "test/sub/"}) {
		t.Fatalf("test/ = %v %v", dirPaths(inside), filePaths(inside))
	}
	if raw := findDir(t, inside, "test/raw/"); !raw.IndexOff || raw.Hidden || raw.Count != 1 {
		t.Fatalf("raw row = %+v", raw)
	}
	if got := e.indexFor("test/raw/", viewer); !equal(filePaths(got), []string{"test/raw/e.txt"}) {
		t.Fatalf("test/raw/ = %v", filePaths(got))
	}
	for _, p := range []string{"test/a.txt", "test/sub/d.txt", "test/raw/e.txt"} {
		r, err := e.svc.ResolvePublicFor(e.ctx, p, viewer)
		if err != nil || r.Content == nil || r.Content.Object.Path != p {
			t.Fatalf("resolve %s = %+v, %v", p, r, err)
		}
	}
	for p, want := range map[string]string{"test": "test/", "test/sub": "test/sub/", "test/raw": "test/raw/"} {
		r, err := e.svc.ResolvePublicFor(e.ctx, p, viewer)
		if err != nil || r.RedirectDir != want {
			t.Fatalf("redirect %s = %+v, %v", p, r, err)
		}
	}

	// Outside the prefix the session is anonymous: the hidden sibling with
	// private content stays 404, a hidden public directory opens by URL as
	// it does for anybody, and test2/ is not covered by test/.
	for _, p := range []string{"secret/", "test2/sub/"} {
		_, err := e.svc.IndexFor(e.ctx, p, viewer)
		wantErr(t, err, service.ErrNotFound)
	}
	if got := e.indexFor("test2/", viewer); !equal(filePaths(got), []string{"test2/z.txt"}) {
		t.Fatalf("test2/ = %v", filePaths(got))
	}
	if got := e.indexFor("open/", viewer); !equal(filePaths(got), []string{"open/y.txt"}) {
		t.Fatalf("open/ = %v", filePaths(got))
	}
	for _, p := range []string{"secret/x.txt", "test2/w.txt", "secret", "missing", "test/missing"} {
		_, err := e.svc.ResolvePublicFor(e.ctx, p, viewer)
		wantErr(t, err, service.ErrNotFound)
	}
	if r, err := e.svc.ResolvePublicFor(e.ctx, "test2/z.txt", viewer); err != nil || r.Content == nil {
		t.Fatalf("public file outside the prefix = %+v, %v", r, err)
	}

	// Expired objects are gone for sessions too.
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "test/brief.txt", Body: stringsReader("x"),
		Visibility: ptr(api.Private), ExpiresAt: api.Some(t0.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Minute)
	_, err = e.svc.ResolvePublicFor(e.ctx, "test/brief.txt", viewer)
	wantErr(t, err, service.ErrNotFound)
	if got := e.indexFor("test/", viewer); len(got.Files) != 3 {
		t.Fatalf("expired file listed: %v", filePaths(got))
	}
}

// Acceptance check 3 of docs/SPEC-web.md.
func TestSessionEntranceThroughDisabledIndex(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("r.txt", "r", api.Public)
	e.put("a/pub.txt", "p", api.Public)
	e.put("a/c/x.txt", "x", api.Public)
	e.put("a/b/p.txt", "p", api.Private)
	e.dirRule("a/", api.DirPolicyPatch{Index: api.Some(api.IndexOff)})
	viewer := &service.Principal{KeyID: "k", Scope: api.ScopeRO, Prefix: "a/b/"}

	_, err := e.svc.Index(e.ctx, "a/")
	wantErr(t, err, service.ErrNotFound)
	a := e.indexFor("a/", viewer)
	if !equal(dirPaths(a), []string{"a/b/"}) || len(a.Files) != 0 {
		t.Fatalf("a/ = %v %v", dirPaths(a), filePaths(a))
	}
	if b := findDir(t, a, "a/b/"); !b.IndexOff || b.Count != 1 {
		t.Fatalf("b/ row = %+v", b)
	}
	root := e.indexFor("", viewer)
	if !equal(dirPaths(root), []string{"a/"}) || !equal(filePaths(root), []string{"r.txt"}) {
		t.Fatalf("root = %v %v", dirPaths(root), filePaths(root))
	}
	// Breadcrumbs and parent links reach the disabled ancestor.
	if a.Policies.Effective("a/").Index != api.IndexOn || root.Policies.Effective("a/").Index != api.IndexOn {
		t.Fatal("ancestor not linkable for the session")
	}
	if r, err := e.svc.ResolvePublicFor(e.ctx, "a", viewer); err != nil || r.RedirectDir != "a/" {
		t.Fatalf("redirect a = %+v, %v", r, err)
	}
	// a/c/ inherits the disabled index and stays 404.
	_, err = e.svc.IndexFor(e.ctx, "a/c/", viewer)
	wantErr(t, err, service.ErrNotFound)

	// Without content in the prefix there is no entrance.
	empty := &service.Principal{KeyID: "k", Scope: api.ScopeRO, Prefix: "a/none/"}
	_, err = e.svc.IndexFor(e.ctx, "a/", empty)
	wantErr(t, err, service.ErrNotFound)
	_, err = e.svc.ResolvePublicFor(e.ctx, "a", empty)
	wantErr(t, err, service.ErrNotFound)
	if root := e.indexFor("", empty); len(root.Dirs) != 0 || root.Policies.Effective("a/").Index != api.IndexOff {
		t.Fatalf("empty prefix root = %v", dirPaths(root))
	}
}

func TestSessionAncestorKeepsAnonymousRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("a/pub.txt", "p", api.Public)
	e.put("a/hid/h.txt", "h", api.Public)
	e.put("a/vis/v.txt", "v", api.Public)
	e.put("a/b/p.txt", "p", api.Private)
	e.dirRule("a/hid/", api.DirPolicyPatch{Hidden: api.Some(true)})
	viewer := &service.Principal{KeyID: "k", Scope: api.ScopeRO, Prefix: "a/b/"}
	anon := e.indexFor("a/", nil)
	got := e.indexFor("a/", viewer)
	if !equal(dirPaths(got), []string{"a/b/", "a/vis/"}) || !equal(filePaths(got), filePaths(anon)) {
		t.Fatalf("a/ = %v %v", dirPaths(got), filePaths(got))
	}
	if findDir(t, got, "a/vis/") != findDir(t, anon, "a/vis/") {
		t.Fatal("anonymous row differs for the session")
	}
}

func TestSessionWithoutPrefix(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, func(o *service.Options) { o.RootIndex = api.IndexOff })
	e.put("priv.txt", "p", api.Private)
	e.put("h/x.txt", "x", api.Unlisted)
	e.dirRule("h/", api.DirPolicyPatch{Hidden: api.Some(true)})
	viewer := &service.Principal{KeyID: "k", Scope: api.ScopeRO}
	_, err := e.svc.Index(e.ctx, "")
	wantErr(t, err, service.ErrNotFound)
	root := e.indexFor("", viewer)
	if !equal(dirPaths(root), []string{"h/"}) || !equal(filePaths(root), []string{"priv.txt"}) {
		t.Fatalf("root = %v %v", dirPaths(root), filePaths(root))
	}
	if h := findDir(t, root, "h/"); !h.Hidden || !h.IndexOff {
		t.Fatalf("h/ row = %+v", h)
	}
	if root.Policies.Effective("").Index != api.IndexOn {
		t.Fatal("root not linkable for the session")
	}
	// An empty store still renders the root for a session without prefix.
	empty := newEnvWith(t, func(o *service.Options) { o.RootIndex = api.IndexOff })
	if _, err := empty.svc.IndexFor(empty.ctx, "", viewer); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLegacyCounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("docs/a.txt", "a", api.Public)
	e.put("docs/sub/b.txt", "b", api.Private)
	e.put("docs/sub/c.txt", "c", api.Unlisted)
	viewer := &service.Principal{KeyID: "k", Scope: api.ScopeRO, Prefix: "docs/"}
	// Without rules counts are subtree file counts, as in the anonymous index.
	if d := findDir(t, e.indexFor("", nil), "docs/"); d.Count != 1 {
		t.Fatalf("anonymous docs/ = %+v", d)
	}
	if d := findDir(t, e.indexFor("", viewer), "docs/"); d.Count != 3 {
		t.Fatalf("signed-in docs/ = %+v", d)
	}
}
