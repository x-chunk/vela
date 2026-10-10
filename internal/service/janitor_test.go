package service_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
)

func TestJanitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// 1. An object that expires.
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "tmp/expiring", Body: stringsReader("x"),
		ExpiresAt: api.Some(t0.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	// 2. A trashed object that will age out.
	e.put("old", "o", api.Public)
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "old"}); err != nil {
		t.Fatal(err)
	}
	// 3. A link that will expire and age out.
	e.put("linked", "l", api.Private)
	if _, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "linked", TTL: "1h"}); err != nil {
		t.Fatal(err)
	}
	// 4. A retired signing key without live links.
	if _, err := e.svc.RotateSigningKey(e.ctx, false); err != nil {
		t.Fatal(err)
	}
	// 5. An abandoned tmp file and 6. an orphan blob, both old.
	stale := t0.Add(15 * api.Day).Add(-2 * time.Hour)
	tmpFile := filepath.Join(e.dataDir, "tmp", "upload-abandoned")
	orphanID := ids.NewULID(nil).New()
	orphan := filepath.Join(e.dataDir, "blobs", orphanID[22:24], orphanID[24:26], orphanID)
	for _, f := range []string{tmpFile, orphan} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	// Live blobs are old too, but referenced: they must survive.
	_ = filepath.WalkDir(filepath.Join(e.dataDir, "blobs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chtimes(p, stale, stale)
		}
		return nil
	})

	e.clock.Set(t0.Add(15 * api.Day))
	report, err := e.svc.RunJanitor(e.ctx)
	if err != nil || report.Skipped || report.Err() != nil {
		t.Fatalf("janitor = %+v, %v", report, err)
	}
	counts := map[string]int{}
	for _, task := range report.Tasks {
		counts[task.Name] = task.Count
	}
	want := map[string]int{
		"expired_links":   1,
		"expired_objects": 1,
		"trash":           1,
		"signing_keys":    1,
		"tmp_files":       1,
		"orphan_blobs":    1, // queued, not yet deleted
		"blob_gc":         0, // grace period not over
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("task %s count = %d; want %d (all: %v)", k, counts[k], v, counts)
		}
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Error("orphan deleted before its grace period")
	}
	// The expired object is in the trash now, restorable without its expiry.
	list, _ := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Path: "tmp/expiring"})
	if len(list.Entries) != 1 {
		t.Fatalf("expired object not in trash: %+v", list)
	}
	o, err := e.svc.Restore(e.ctx, e.admin, list.Entries[0].ID, api.RestoreRequest{})
	if err != nil || o.ExpiresAt != nil {
		t.Fatalf("restore of expired object = %+v, %v", o, err)
	}
	for _, p := range []string{"linked", "tmp/expiring"} {
		c, err := e.svc.Download(e.ctx, e.admin, p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		_ = e.read(c)
	}

	// After the grace period the collector removes the orphan and the
	// purged trash content, and keeps everything referenced.
	if n := e.collectAfterGrace(); n != 2 {
		t.Fatalf("collected %d blobs; want the orphan and the purged one", n)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan blob survived the collector")
	}

	// A second pass finds nothing.
	report, _ = e.svc.RunJanitor(e.ctx)
	for _, task := range report.Tasks {
		if task.Count != 0 {
			t.Errorf("second pass: task %s count = %d", task.Name, task.Count)
		}
	}
}

func TestJanitorLockAcrossProcesses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Another process (velad janitor run via docker exec) holds the lock.
	hold := func(owner string, until time.Time) {
		err := e.storage.DB.Update(e.ctx, func(tx *store.Tx) error {
			ok, err := tx.AcquireLock(e.ctx, "janitor", owner, e.clock.Now(), until)
			if err == nil && !ok {
				t.Fatal("could not take the lock")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	hold("other-process", t0.Add(time.Hour))
	report, err := e.svc.RunJanitor(e.ctx)
	if err != nil || !report.Skipped {
		t.Fatalf("pass ran despite a held lock: %+v, %v", report, err)
	}
	// A lock left by a crashed process expires.
	e.clock.Advance(2 * time.Hour)
	report, err = e.svc.RunJanitor(e.ctx)
	if err != nil || report.Skipped {
		t.Fatalf("pass skipped after lock expiry: %+v, %v", report, err)
	}
	// The pass released its lock, so another process can run now.
	hold("other-process", e.clock.Now().Add(time.Hour))
}
