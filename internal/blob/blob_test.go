package blob

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.xchunk.org/vela/internal/ids"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestCommitOpenDelete(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	id := ids.NewULID(nil).New()
	p, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Abort()
	if _, err := io.WriteString(p, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(id); err != nil {
		t.Fatal(err)
	}
	p.Abort() // no-op after commit

	rel := s.RelPath(id)
	if rel != id[22:24]+"/"+id[24:26]+"/"+id {
		t.Fatalf("RelPath = %q", rel)
	}
	abs := filepath.Join(dir, "blobs", filepath.FromSlash(rel))
	if got := mode(t, abs); got != 0o644 {
		t.Errorf("blob mode = %o, want 644", got)
	}
	if got := mode(t, filepath.Dir(abs)); got != 0o755 {
		t.Errorf("shard mode = %o, want 755", got)
	}
	if got := mode(t, filepath.Join(dir, "blobs")); got != 0o755 {
		t.Errorf("blobs mode = %o, want 755", got)
	}
	if got := mode(t, filepath.Join(dir, "tmp")); got != 0o700 {
		t.Errorf("tmp mode = %o, want 700", got)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	if len(entries) != 0 {
		t.Errorf("tmp not empty after commit: %v", entries)
	}

	f, err := s.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "hello" {
		t.Fatalf("content = %q", b)
	}

	var walked []string
	if err := s.Walk(context.Background(), func(id string, _ time.Time) error {
		walked = append(walked, id)
		return nil
	}); err != nil || len(walked) != 1 || walked[0] != id {
		t.Fatalf("Walk = %v, %v", walked, err)
	}

	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := s.Open(id); err == nil {
		t.Fatal("Open after Delete succeeded")
	}
}

func TestAbortRemovesTmp(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	p, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(p, "partial")
	p.Abort()
	entries, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	if len(entries) != 0 {
		t.Fatalf("tmp not empty after abort: %v", entries)
	}
	if err := p.Commit(ids.NewULID(nil).New()); err == nil {
		t.Fatal("Commit after Abort succeeded")
	}
}

func TestInvalidIDs(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	for _, bad := range []string{"../../etc/passwd", "", "short"} {
		if _, err := s.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) succeeded", bad)
		}
	}
	p, _ := s.Create()
	if err := p.Commit("../x"); err == nil {
		t.Fatal("Commit with bad id succeeded")
	}
}

func TestCleanTmp(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	old, _ := s.Create()
	fresh, _ := s.Create()
	defer fresh.Abort()
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old.f.Name(), past, past); err != nil {
		t.Fatal(err)
	}
	n, err := s.CleanTmp(time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("CleanTmp = %d, %v", n, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	if len(entries) != 1 {
		t.Fatalf("tmp entries = %v", entries)
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
}
