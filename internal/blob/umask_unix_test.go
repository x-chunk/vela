//go:build unix

package blob

import (
	"io"
	"os"
	"path/filepath"
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
