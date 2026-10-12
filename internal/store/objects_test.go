package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestObjectCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	o := newObject("docs/a.pdf", api.Unlisted)
	exp := t0.Add(time.Hour)
	o.ExpiresAt = &exp
	o.Title = "Контрольная"
	insert(t, s, o)

	got, err := s.ObjectByPath(ctx, "docs/a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, o) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, o)
	}
	if _, err := s.ObjectByID(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObjectByPath(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing object error = %v", err)
	}

	dup := newObject("docs/a.pdf", api.Public)
	err = s.Update(ctx, func(tx *Tx) error { return tx.InsertObject(ctx, dup) })
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Fatalf("duplicate path error = %v", err)
	}

	o.Path = "docs/b.pdf"
	o.Revision = 2
	o.ExpiresAt = nil
	if err := s.Update(ctx, func(tx *Tx) error { return tx.UpdateObject(ctx, o) }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ObjectByID(ctx, o.ID)
	if got.Path != "docs/b.pdf" || got.Revision != 2 || got.ExpiresAt != nil {
		t.Fatalf("after update: %+v", got)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.DeleteObject(ctx, o.ID) }); err != nil {
		t.Fatal(err)
	}
	err = s.Update(ctx, func(tx *Tx) error { return tx.DeleteObject(ctx, o.ID) })
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestConflictHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	insert(t, s, newObject("a/b", api.Public), newObject("x/y/z", api.Private))

	if p, err := s.FirstExisting(ctx, api.Ancestors("a/b/c/d")); err != nil || p != "a/b" {
		t.Fatalf("FirstExisting = %q, %v", p, err)
	}
	if p, err := s.FirstExisting(ctx, api.Ancestors("q/r")); err != nil || p != "" {
		t.Fatalf("FirstExisting(none) = %q, %v", p, err)
	}
	if p, err := s.FirstUnder(ctx, "x/"); err != nil || p != "x/y/z" {
		t.Fatalf("FirstUnder = %q, %v", p, err)
	}
	if p, err := s.FirstUnder(ctx, "x/y/z/"); err != nil || p != "" {
		t.Fatalf("FirstUnder(empty) = %q, %v", p, err)
	}
	objs, err := s.ObjectsUnder(ctx, "")
	if err != nil || len(objs) != 2 {
		t.Fatalf("ObjectsUnder(root) = %d, %v", len(objs), err)
	}
}

func paths(res domain.ListResult) string {
	var b []string
	b = append(b, res.Dirs...)
	for _, o := range res.Objects {
		b = append(b, o.Path)
	}
	return strings.Join(b, " ")
}

func TestListObjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	expired := newObject("docs/old.txt", api.Public)
	past := t0.Add(-time.Hour)
	expired.ExpiresAt = &past
	insert(t, s,
		newObject("a.txt", api.Public),
		newObject("b/1.txt", api.Public),
		newObject("b/2.txt", api.Private),
		newObject("b.txt", api.Public),
		newObject("b0", api.Public), // sorts right after "b/" subtree; must not be skipped
		newObject("docs/math/q1.pdf", api.Public),
		newObject("docs/math/q2.pdf", api.Unlisted),
		newObject("docs/phys/x.pdf", api.Private),
		newObject("docs/readme.md", api.Public),
		expired,
		newObject("z.txt", api.Unlisted),
	)

	tests := []struct {
		name string
		q    domain.ListQuery
		want string
		more bool
	}{
		{name: "root", q: domain.ListQuery{}, want: "b/ docs/ a.txt b.txt b0 z.txt"},
		{name: "dir", q: domain.ListQuery{Prefix: "docs/"}, want: "docs/math/ docs/phys/ docs/old.txt docs/readme.md"},
		{name: "recursive", q: domain.ListQuery{Prefix: "docs/", Recursive: true},
			want: "docs/math/q1.pdf docs/math/q2.pdf docs/old.txt docs/phys/x.pdf docs/readme.md"},
		{name: "public only, live", q: domain.ListQuery{Prefix: "docs/", Visibilities: []api.Visibility{api.Public}, LiveAt: t0},
			want: "docs/math/ docs/readme.md"},
		{name: "visibility filter hides dirs", q: domain.ListQuery{Visibilities: []api.Visibility{api.Private}},
			want: "b/ docs/"},
		{name: "within narrows root", q: domain.ListQuery{Within: "docs/math/"}, want: "docs/"},
		{name: "within narrows dir", q: domain.ListQuery{Prefix: "docs/", Within: "docs/math/"}, want: "docs/math/"},
		{name: "within recursive", q: domain.ListQuery{Within: "docs/math/", Recursive: true},
			want: "docs/math/q1.pdf docs/math/q2.pdf"},
		// Byte order: "b.txt" < "b/..." < "b0" because '.' < '/' < '0'.
		{name: "limit", q: domain.ListQuery{Limit: 2}, want: "a.txt b.txt", more: true},
		{name: "after object", q: domain.ListQuery{After: "a.txt", Limit: 2}, want: "b/ b.txt", more: true},
		{name: "after dir", q: domain.ListQuery{After: "b/", Limit: 10}, want: "docs/ b0 z.txt"},
		{name: "exact limit", q: domain.ListQuery{Prefix: "docs/math/", Limit: 2}, want: "docs/math/q1.pdf docs/math/q2.pdf"},
		{name: "empty", q: domain.ListQuery{Prefix: "nope/"}, want: ""},
	}
	for _, tt := range tests {
		res, err := s.ListObjects(ctx, tt.q)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := paths(res); got != tt.want || res.More != tt.more {
			t.Errorf("%s: got %q more=%v; want %q more=%v", tt.name, got, res.More, tt.want, tt.more)
		}
	}
}

func TestListObjectsPaginationIsComplete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	var objs []domain.Object
	for _, p := range []string{"a", "b/x", "b/y", "c", "d/e/f", "e", "f/g", "h", "i", "j/k"} {
		objs = append(objs, newObject(p, api.Public))
	}
	insert(t, s, objs...)
	for limit := 1; limit <= 4; limit++ {
		var all []string
		after := ""
		for page := 0; page < 20; page++ {
			res, err := s.ListObjects(ctx, domain.ListQuery{After: after, Limit: limit})
			if err != nil {
				t.Fatal(err)
			}
			if p := paths(res); p != "" {
				all = append(all, p)
			}
			if !res.More {
				break
			}
			after = res.Last
		}
		// b/x and b/y collapse into "b/", d/e/f into "d/", and so on.
		got := strings.Fields(strings.Join(all, " "))
		sort.Strings(got)
		if want := "a b/ c d/ e f/ h i j/"; strings.Join(got, " ") != want {
			t.Errorf("limit %d: got %q; want %q", limit, got, want)
		}
	}
}

func TestDirStatsAndExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	a := newObject("d/a", api.Public)
	b := newObject("d/sub/b", api.Public)
	b.UpdatedAt = t0.Add(time.Minute)
	c := newObject("d/c", api.Private)
	e := newObject("d/e", api.Public)
	past := t0.Add(-time.Second)
	e.ExpiresAt = &past
	insert(t, s, a, b, c, e)
	n, latest, err := s.DirStats(ctx, "d/", t0)
	if err != nil || n != 2 || !latest.Equal(t0.Add(time.Minute)) {
		t.Fatalf("DirStats = %d, %v, %v", n, latest, err)
	}
	if n, latest, _ := s.DirStats(ctx, "none/", t0); n != 0 || !latest.IsZero() {
		t.Fatalf("DirStats(empty) = %d, %v", n, latest)
	}
	exp, err := s.ExpiredObjects(ctx, t0, 10)
	if err != nil || len(exp) != 1 || exp[0].Path != "d/e" {
		t.Fatalf("ExpiredObjects = %v, %v", exp, err)
	}
	if ok, err := s.BlobReferenced(ctx, a.BlobID); err != nil || !ok {
		t.Fatalf("BlobReferenced(live) = %v, %v", ok, err)
	}
	if ok, err := s.BlobReferenced(ctx, gen.New()); err != nil || ok {
		t.Fatalf("BlobReferenced(orphan) = %v, %v", ok, err)
	}
}

func TestHasPublicObjects(t *testing.T) {
	t.Parallel()
	db := openTest(t)
	live := newObject("docs/file", api.Public)
	edge := newObject("edge/file", api.Public)
	edge.ExpiresAt = &t0
	expired := newObject("old/file", api.Public)
	past := t0.Add(-time.Millisecond)
	expired.ExpiresAt = &past
	insert(t, db, live, edge, expired, newObject("private/file", api.Private), newObject("unlisted/file", api.Unlisted), newObject("ab/file", api.Public))
	for _, tc := range []struct {
		prefix string
		want   bool
	}{{"", true}, {"docs/", true}, {"edge/", true}, {"old/", false}, {"private/", false}, {"unlisted/", false}, {"a/", false}, {"missing/", false}} {
		got, err := db.HasPublicObjects(context.Background(), tc.prefix, t0)
		if err != nil || got != tc.want {
			t.Fatalf("%q: exists=%t error=%v", tc.prefix, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.HasPublicObjects(ctx, "docs/", t0); err == nil {
		t.Fatal("cancellation ignored")
	}
}
