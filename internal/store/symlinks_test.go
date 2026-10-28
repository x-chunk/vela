package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func newSymlink(path string, target domain.Object, vis api.Visibility) domain.Object {
	return domain.Object{
		ID: gen.New(), Path: path, Kind: domain.KindSymlink, TargetID: target.ID,
		Disposition: api.Inline, Visibility: vis, Tags: []string{}, CreatedAt: t0, UpdatedAt: t0, Revision: 1,
	}
}

func trashObject(t *testing.T, s *Store, o domain.Object) {
	t.Helper()
	ctx := context.Background()
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.InsertTrash(ctx, domain.TrashEntry{ID: gen.New(), Object: o, DeletedAt: t0}); err != nil {
			return err
		}
		return tx.DeleteObject(ctx, o.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkTargetResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	live := newObject("orig/live.txt", api.Unlisted)
	live.Size, live.SHA256, live.ContentType = 7, "abc", "image/png"
	trashed := newObject("orig/trashed.txt", api.Public)
	purged := newObject("orig/purged.txt", api.Public)
	insert(t, s, live, trashed)
	insert(t, s,
		newSymlink("l/live", live, api.Public),
		newSymlink("l/trashed", trashed, api.Public),
		newSymlink("l/purged", purged, api.Public))
	trashObject(t, s, trashed)

	got, err := s.ObjectByPath(ctx, "l/live")
	if err != nil {
		t.Fatal(err)
	}
	want := &domain.Target{ID: live.ID, State: domain.ObjectLive, Path: live.Path, BlobID: live.BlobID, Visibility: api.Unlisted}
	if !reflect.DeepEqual(got.Target, want) {
		t.Errorf("live target = %+v, want %+v", got.Target, want)
	}
	if got.Size != 7 || got.SHA256 != "abc" || got.ContentType != "image/png" || got.BlobID != "" {
		t.Errorf("symlink content fields = %d %q %q %q", got.Size, got.SHA256, got.ContentType, got.BlobID)
	}
	if got.EffectiveVisibility() != api.Unlisted {
		t.Errorf("effective visibility = %s", got.EffectiveVisibility())
	}

	got, err = s.ObjectByPath(ctx, "l/trashed")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.State != domain.ObjectTrashed || got.Target.Path != trashed.Path || got.Size != 0 || !got.Broken() {
		t.Errorf("trashed target = %+v (size %d)", got.Target, got.Size)
	}
	got, err = s.ObjectByPath(ctx, "l/purged")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.State != domain.ObjectGone || got.Target.Path != "" || got.EffectiveVisibility() != api.Private {
		t.Errorf("purged target = %+v", got.Target)
	}

	// Trashing a symlink keeps its target reference.
	sym, _ := s.ObjectByPath(ctx, "l/live")
	trashObject(t, s, sym)
	entries, err := s.ListTrash(ctx, domain.TrashQuery{Path: "l/live"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash = %v, %v", entries, err)
	}
	if e := entries[0].Object; !e.IsSymlink() || e.TargetID != live.ID || !e.Target.Live() {
		t.Errorf("trashed symlink = %+v", e)
	}

	// Files are never referenced through NULL blob IDs.
	if used, err := s.BlobReferenced(ctx, ""); err != nil || used {
		t.Errorf("BlobReferenced(\"\") = %v, %v", used, err)
	}
}

func TestListObjectsWithSymlinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	now := t0.Add(time.Hour)
	past := t0
	pub := newObject("orig/pub.txt", api.Public)
	unl := newObject("orig/unl.txt", api.Unlisted)
	priv := newObject("secret/priv.txt", api.Private)
	expired := newObject("orig/expired.txt", api.Public)
	expired.ExpiresAt = &past
	gone := newObject("orig/gone.txt", api.Public)
	insert(t, s, pub, unl, priv, expired)
	selfExpired := newSymlink("s/self-expired", pub, api.Public)
	selfExpired.ExpiresAt = &past
	insert(t, s,
		newSymlink("s/pub", pub, api.Public),
		newSymlink("s/pub-unlisted", pub, api.Unlisted),
		newSymlink("s/to-unl", unl, api.Public),
		newSymlink("s/to-priv", priv, api.Public),
		newSymlink("s/to-expired", expired, api.Public),
		newSymlink("s/to-gone", gone, api.Public),
		newSymlink("s/to-pub-again", pub, api.Private),
		selfExpired)

	list := func(lq domain.ListQuery) []string {
		t.Helper()
		lq.Prefix, lq.Recursive = "s/", true
		res, err := s.ListObjects(ctx, lq)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, o := range res.Objects {
			paths = append(paths, o.Path)
		}
		return paths
	}
	for name, tc := range map[string]struct {
		q    domain.ListQuery
		want []string
	}{
		"public live": {domain.ListQuery{Visibilities: []api.Visibility{api.Public}, LiveAt: now},
			[]string{"s/pub"}},
		"unlisted": {domain.ListQuery{Visibilities: []api.Visibility{api.Unlisted}},
			[]string{"s/pub-unlisted", "s/to-unl"}},
		"private": {domain.ListQuery{Visibilities: []api.Visibility{api.Private}},
			[]string{"s/to-gone", "s/to-priv", "s/to-pub-again"}},
		"live": {domain.ListQuery{LiveAt: now},
			[]string{"s/pub", "s/pub-unlisted", "s/to-priv", "s/to-pub-again", "s/to-unl"}},
		"broken": {domain.ListQuery{Broken: true}, []string{"s/to-gone"}},
		"target": {domain.ListQuery{TargetID: pub.ID},
			[]string{"s/pub", "s/pub-unlisted", "s/self-expired", "s/to-pub-again"}},
		"target within": {domain.ListQuery{LiveAt: now, TargetWithin: "orig/"},
			[]string{"s/pub", "s/pub-unlisted", "s/to-pub-again", "s/to-unl"}},
	} {
		if got := list(tc.q); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}

	res, err := s.ListObjects(ctx, domain.ListQuery{Recursive: true, Kind: domain.KindSymlink})
	if err != nil || len(res.Objects) != 8 {
		t.Errorf("kind=symlink: %d objects, %v", len(res.Objects), err)
	}
	res, err = s.ListObjects(ctx, domain.ListQuery{Recursive: true, Kind: domain.KindFile})
	if err != nil || len(res.Objects) != 4 {
		t.Errorf("kind=file: %d objects, %v", len(res.Objects), err)
	}

	if ok, err := s.HasPublicObjects(ctx, "s/", now); err != nil || !ok {
		t.Errorf("HasPublicObjects = %v, %v", ok, err)
	}
	n, _, err := s.DirStats(ctx, "s/", now)
	if err != nil || n != 1 {
		t.Errorf("DirStats = %d, %v", n, err)
	}
	if n, err := s.CountSymlinksTo(ctx, []string{pub.ID, priv.ID}, ""); err != nil || n != 5 {
		t.Errorf("CountSymlinksTo = %d, %v", n, err)
	}
	if n, err := s.CountSymlinksTo(ctx, []string{pub.ID}, "orig/"); err != nil || n != 0 {
		t.Errorf("CountSymlinksTo within = %d, %v", n, err)
	}
}

func TestPublicExistenceIgnoresHiddenSymlinkTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	priv := newObject("secret/a.txt", api.Private)
	insert(t, s, priv, newSymlink("pub/a.txt", priv, api.Public))
	if ok, err := s.HasPublicObjects(ctx, "pub/", t0); err != nil || ok {
		t.Errorf("HasPublicObjects = %v, %v; a public symlink to a private file must not count", ok, err)
	}
	if ok, err := s.HasLiveObjects(ctx, "pub/", t0); err != nil || !ok {
		t.Errorf("HasLiveObjects = %v, %v", ok, err)
	}
}
