//go:build unix

package velad

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestBackupIsPrivate covers R-004: with the most permissive umask and a
// pre-created world-readable directory, the snapshot is 0600 from the start
// (VACUUM INTO writes into a file created 0600 and does not change its
// mode) and the backup directory becomes 0700. Not parallel: umask is
// process-wide.
func TestBackupIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "content")
	dest := filepath.Join(t.TempDir(), "backup")
	if err := os.Mkdir(dest, 0o777); err != nil {
		t.Fatal(err)
	}
	var modeDuringBackup os.FileMode
	backupAfterSnapshot = func() {
		fi, err := os.Stat(filepath.Join(dest, "vela.db"))
		if err == nil {
			modeDuringBackup = fi.Mode().Perm()
		}
	}
	defer func() { backupAfterSnapshot = nil }()
	if _, err := backup(context.Background(), e.svc, e.st, dest, false); err != nil {
		t.Fatal(err)
	}
	if modeDuringBackup != 0o600 {
		t.Fatalf("snapshot mode during the backup = %o", modeDuringBackup)
	}
	for path, want := range map[string]os.FileMode{dest: 0o700, filepath.Join(dest, "vela.db"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %o; want %o", path, fi.Mode().Perm(), want)
		}
	}
}

// TestBackupModesForNginx covers R-022: under umask 077 the blob tree of a
// backup is still 0755 directories and 0644 files, for both hard links and
// cross-filesystem copies, so a restored data directory works with nginx.
func TestBackupModesForNginx(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, forceCopy := range []bool{false, true} {
		backupForceCopy = forceCopy
		e := newBackupEnv(t)
		e.put(t, "doc.txt", "content")
		dest := filepath.Join(t.TempDir(), "backup")
		res, err := backup(context.Background(), e.svc, e.st, dest, true)
		backupForceCopy = false
		if err != nil {
			t.Fatal(err)
		}
		if forceCopy && res.copied != 1 || !forceCopy && res.linked != 1 {
			t.Fatalf("copy=%v: result %+v", forceCopy, res)
		}
		err = filepath.WalkDir(filepath.Join(dest, "blobs"), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, _ := d.Info()
			want := os.FileMode(0o644)
			if d.IsDir() {
				want = 0o755
			}
			if fi.Mode().Perm() != want {
				t.Errorf("copy=%v: %s mode = %o; want %o", forceCopy, p, fi.Mode().Perm(), want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
