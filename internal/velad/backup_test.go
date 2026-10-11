package velad

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
	"go.xchunk.org/vela/internal/wire"
)

type backupEnv struct {
	svc   *service.Service
	st    *wire.Storage
	clock *clock.Fake
	admin service.Principal
}

func newBackupEnv(t *testing.T) *backupEnv {
	t.Helper()
	st, err := wire.OpenStorage(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := clock.NewFake(time.Now())
	svc, err := wire.NewService(st, clk, nil, service.Options{
		PublicURL: "http://x", MaxUpload: 1 << 20, MaxCreate: 1 << 10,
		LinkDefaultTTL: time.Hour, LinkMaxTTL: time.Hour, TrashRetention: time.Hour,
		DefaultVisibility: api.Public, BlobGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &backupEnv{svc: svc, st: st, clock: clk, admin: service.Principal{KeyID: "k", Name: "a", Scope: api.ScopeAdmin}}
}

func (e *backupEnv) put(t *testing.T, path, content string) {
	t.Helper()
	_, _, err := e.svc.Upload(context.Background(), e.admin, service.UploadInput{Path: path, Body: strings.NewReader(content), ContentLength: -1})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBackupPinsBlobs covers R-001: overwrites, purges and blob collection
// between the snapshot and the copying must not make the backup lose content
// the snapshot references. Not parallel: it sets a package-level hook.
func TestBackupPinsBlobs(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "version one")
	e.put(t, "gone.txt", "purged during the backup")

	collected := -1
	backupAfterSnapshot = func() {
		e.put(t, "doc.txt", "version two")
		if _, err := e.svc.Delete(ctx, e.admin, service.DeleteParams{Path: "gone.txt", Purge: true}); err != nil {
			t.Error(err)
		}
		e.clock.Advance(2 * time.Second) // past the 1s grace period
		n, err := e.svc.CollectBlobs(ctx)
		if err != nil {
			t.Error(err)
		}
		collected = n
	}
	defer func() { backupAfterSnapshot = nil }()

	dest := filepath.Join(t.TempDir(), "backup")
	res, err := backup(ctx, e.svc, e.st, dest, true)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if collected != 0 {
		t.Fatalf("collector deleted %d blob(s) while the backup was running", collected)
	}
	if res.objects != 2 || res.blobs != 2 {
		t.Fatalf("backup result = %+v", res)
	}

	// Restore the backup into a fresh data directory and read everything.
	restored := t.TempDir()
	for _, name := range []string{"vela.db", "blobs"} {
		if err := os.Rename(filepath.Join(dest, name), filepath.Join(restored, name)); err != nil {
			t.Fatal(err)
		}
	}
	rst, err := wire.OpenStorage(ctx, restored, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rst.Close() }()
	rsvc, err := wire.NewService(rst, clock.System{}, nil, service.Options{
		PublicURL: "http://x", MaxUpload: 1, MaxCreate: 1, LinkDefaultTTL: time.Hour, LinkMaxTTL: time.Hour,
		DefaultVisibility: api.Public,
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"doc.txt": "version one", "gone.txt": "purged during the backup"} {
		c, err := rsvc.Download(ctx, e.admin, path)
		if err != nil {
			t.Fatalf("restored %s: %v", path, err)
		}
		f, err := rsvc.OpenContent(c)
		if err != nil {
			t.Fatalf("restored %s has no content: %v", path, err)
		}
		b := make([]byte, 100)
		n, _ := f.Read(b)
		_ = f.Close()
		if string(b[:n]) != want {
			t.Fatalf("restored %s = %q", path, b[:n])
		}
	}

	// After the backup the pin is released and the collector catches up.
	e.clock.Advance(2 * time.Second)
	if n, err := e.svc.CollectBlobs(ctx); err != nil || n != 2 {
		t.Fatalf("collection after the backup = %d, %v", n, err)
	}
}

func TestBackupFailsOnMissingBlob(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "content")
	c, _ := e.svc.Download(ctx, e.admin, "doc.txt")
	if err := os.Remove(filepath.Join(e.st.Blobs.Dir(), filepath.FromSlash(c.BlobPath))); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "backup")
	if _, err := backup(ctx, e.svc, e.st, dest, false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("backup with a missing blob = %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("failed backup left files behind: %v", entries)
	}
	// A second backup is not blocked by a stale lock.
	e.put(t, "doc.txt", "fixed")
	if _, err := backup(ctx, e.svc, e.st, dest, true); err != nil {
		t.Fatalf("backup after the failure: %v", err)
	}
}

func TestBackupDetectsCorruption(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "content")
	c, _ := e.svc.Download(ctx, e.admin, "doc.txt")
	if err := os.WriteFile(filepath.Join(e.st.Blobs.Dir(), filepath.FromSlash(c.BlobPath)), []byte("CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backup(ctx, e.svc, e.st, filepath.Join(t.TempDir(), "b"), true); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("verify missed a corrupted blob: %v", err)
	}
}

func TestConcurrentBackupsExclude(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "content")
	_, release, err := e.svc.HoldBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := backup(ctx, e.svc, e.st, filepath.Join(t.TempDir(), "b"), false); err == nil {
		t.Fatal("second backup ran concurrently")
	}
}

// Metadata backups remain possible while the configured bucket is unreachable.
func TestR2BackupIsMetadataOnly(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	e.put(t, "doc.txt", "stored in the bucket separately")
	vars := newEnv(t)
	vars["VELA_DATA_DIR"] = filepath.Dir(e.st.Blobs.Dir())
	vars["VELA_STORAGE"] = "r2"
	vars["VELA_R2_ACCOUNT_ID"] = "test"
	vars["VELA_R2_BUCKET"] = "test"
	vars["VELA_R2_ACCESS_KEY_ID"] = "test"
	vars["VELA_R2_SECRET_ACCESS_KEY"] = "test-secret"
	vars["VELA_R2_ENDPOINT"] = "http://127.0.0.1:1"
	// Only metadata is needed: neither local content nor a reachable bucket
	// may be required to take the snapshot during a storage outage.
	if err := os.RemoveAll(e.st.Blobs.Dir()); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	r := execute(t, ctx, vars, nil, "backup", dest)
	if r.err != nil || !strings.Contains(r.stdout, "Metadata-only R2 backup") || !strings.Contains(r.stdout, "1 object(s)") || !strings.Contains(r.stdout, "Bucket contents are not included") {
		t.Fatalf("R2 backup: %q %v", r.stdout, r.err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 || entries[0].Name() != wire.DatabaseFile {
		t.Fatalf("R2 backup files: %v %v", entries, err)
	}
	snap, err := store.Open(ctx, filepath.Join(dest, wire.DatabaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	refs, objects, err := referencedBlobs(ctx, snap)
	if err != nil || objects != 1 || len(refs) != 1 {
		t.Fatalf("R2 metadata snapshot: objects=%d refs=%d err=%v", objects, len(refs), err)
	}
	verifyDest := t.TempDir()
	r = execute(t, ctx, vars, nil, "backup", verifyDest, "--verify")
	if r.err == nil || !strings.Contains(r.err.Error(), "--verify is not supported") || r.stdout != "" {
		t.Fatalf("R2 backup --verify: %q %v", r.stdout, r.err)
	}
	if entries, err := os.ReadDir(verifyDest); err != nil || len(entries) != 0 {
		t.Fatalf("failed R2 backup left partial files: %v %v", entries, err)
	}
}
