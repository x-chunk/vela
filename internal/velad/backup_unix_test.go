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
