//go:build unix

package blob

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"go.xchunk.org/vela/internal/ids"
)

// TestModesIgnoreUmask checks that blob permissions do not depend on the
// process umask. It changes a process-wide setting and therefore must not run
// in parallel with other tests.
func TestModesIgnoreUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := ids.NewULID(nil).New()
	p, _ := s.Create()
	_, _ = io.WriteString(p, "x")
	if err := p.Commit(id); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "blobs", filepath.FromSlash(s.RelPath(id)))
	for path, want := range map[string]os.FileMode{
		abs:                             0o644,
		filepath.Dir(abs):               0o755,
		filepath.Dir(filepath.Dir(abs)): 0o755,
		filepath.Join(dir, "blobs"):     0o755,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

// TestCommitSyncs checks the fsync sequence that crash safety depends on:
// the blob is synced after its final chmod, and the first blob in a new
// shard syncs blobs/, blobs/ab/ and blobs/ab/cd/ (every directory whose
// entries changed). Not parallel: it sets a package-level observer.
func TestCommitSyncs(t *testing.T) {
	dir := t.TempDir()
	type synced struct {
		path string
		mode os.FileMode
	}
	var got []synced
	syncObserver = func(p string, m os.FileMode) { got = append(got, synced{p, m}) }
	defer func() { syncObserver = nil }()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(dir, "blobs")
	opened := map[string]bool{}
	for _, g := range got {
		opened[g.path] = true
	}
	if !opened[dir] {
		t.Errorf("creating blobs/ and tmp/ did not sync the data directory: %v", got)
	}

	got = nil
	id := ids.NewULID(nil).New()
	p, _ := s.Create()
	_, _ = io.WriteString(p, "x")
	if err := p.Commit(id); err != nil {
		t.Fatal(err)
	}
	shardA := filepath.Join(blobs, id[22:24])
	shardB := filepath.Join(shardA, id[24:26])
	want := []string{"tmp-file", blobs, shardA, shardB}
	if len(got) != len(want) {
		t.Fatalf("syncs = %v; want file, blobs/, shard, sub-shard", got)
	}
	if got[0].mode != 0o644 || !strings.Contains(got[0].path, "upload-") {
		t.Errorf("blob synced before its final chmod: %+v", got[0])
	}
	for i, w := range want[1:] {
		if got[i+1].path != w {
			t.Errorf("sync %d = %s; want %s", i+1, got[i+1].path, w)
		}
	}

	// A second blob in an existing shard only syncs the file and its shard.
	got = nil
	// Same shard as id: same last four characters.
	other := ids.NewULID(nil).New()
	other = other[:22] + id[22:26]
	p, _ = s.Create()
	_, _ = io.WriteString(p, "y")
	if err := p.Commit(other); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].path != shardB {
		t.Fatalf("syncs for an existing shard = %v", got)
	}
}
