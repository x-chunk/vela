package velad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"reflect"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/blob"
	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/r2"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/testutil"
	"go.xchunk.org/vela/internal/wire"
)

func localBackend(t *testing.T) content.Store {
	t.Helper()
	s, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return blob.Backend{Store: s}
}
func migrationFixture(t *testing.T, s content.Store) (string, blobRef) {
	t.Helper()
	id := ids.NewULID(nil).New()
	data := "immutable migration contents"
	sum := sha256.Sum256([]byte(data))
	ref := blobRef{size: int64(len(data)), sha256: hex.EncodeToString(sum[:])}
	_, err := s.Put(context.Background(), id, strings.NewReader(data), content.PutOptions{MaxSize: ref.size, SizeHint: ref.size, ExpectSHA256: ref.sha256})
	if err != nil {
		t.Fatal(err)
	}
	return id, ref
}

func TestMigrationVerificationAndDeletion(t *testing.T) {
	ctx := context.Background()
	source, target := localBackend(t), localBackend(t)
	id, ref := migrationFixture(t, source)
	refs := map[string]blobRef{id: ref}
	result, err := migrateBlobs(ctx, source, target, refs, true, false, io.Discard)
	if err != nil || result.copied != 0 {
		t.Fatal(result, err)
	}
	result, err = migrateBlobs(ctx, source, target, refs, false, false, io.Discard)
	if err != nil || result.copied != 1 {
		t.Fatal(result, err)
	}
	result, err = migrateBlobs(ctx, source, target, refs, false, true, io.Discard)
	if err != nil || result.skipped != 1 || result.deleted != 1 {
		t.Fatal(result, err)
	}
}

func TestMigrationResumesAfterSourceDeletion(t *testing.T) {
	ctx := context.Background()
	source, target := localBackend(t), localBackend(t)
	id, ref := migrationFixture(t, source)
	refs := map[string]blobRef{id: ref}
	for range 2 {
		result, err := migrateBlobs(ctx, source, target, refs, false, true, io.Discard)
		if err != nil || result.deleted != 1 {
			t.Fatal(result, err)
		}
	}
}

func TestMigrationCorruptDestinationPreservesSource(t *testing.T) {
	ctx := context.Background()
	source, target := localBackend(t), localBackend(t)
	id, ref := migrationFixture(t, source)
	_, err := target.Put(ctx, id, strings.NewReader("corrupt"), content.PutOptions{MaxSize: 100, SizeHint: -1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrateBlobs(ctx, source, target, map[string]blobRef{id: ref}, false, true, io.Discard)
	if err == nil || result.failed != 1 || result.deleted != 0 {
		t.Fatal(result, err)
	}
	if err := verifyStoredBlob(ctx, source, id, ref); err != nil {
		t.Fatal("good source lost", err)
	}
}

func TestS3MigrationRoundtrip(t *testing.T) {
	cfg := testutil.S3Config(t)
	remote, err := r2.New(cfg.R2)
	if err != nil {
		t.Fatal(err)
	}
	source, target := localBackend(t), localBackend(t)
	id, ref := migrationFixture(t, source)
	refs := map[string]blobRef{id: ref}
	ctx := context.Background()
	result, err := migrateBlobs(ctx, source, remote, refs, false, false, io.Discard)
	if err != nil || result.copied != 1 {
		t.Fatal(result, err)
	}
	result, err = migrateBlobs(ctx, source, remote, refs, false, false, io.Discard)
	if err != nil || result.skipped != 1 {
		t.Fatal(result, err)
	}
	result, err = migrateBlobs(ctx, remote, target, refs, false, true, io.Discard)
	if err != nil || result.copied != 1 || result.deleted != 1 {
		t.Fatal(result, err)
	}
	if err := verifyStoredBlob(ctx, target, id, ref); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCommandMigratesLiveAndTrash(t *testing.T) {
	cfg := testutil.S3Config(t)
	vars := newEnv(t)
	vars["VELA_STORAGE"] = "fs"
	vars["VELA_R2_ACCOUNT_ID"] = cfg.R2.AccountID
	vars["VELA_R2_ENDPOINT"] = cfg.R2.Endpoint
	vars["VELA_R2_BUCKET"] = cfg.R2.Bucket
	vars["VELA_R2_ACCESS_KEY_ID"] = cfg.R2.AccessKeyID
	vars["VELA_R2_SECRET_ACCESS_KEY"] = cfg.R2.SecretAccessKey
	ctx := context.Background()
	st, err := wire.OpenStorage(ctx, vars["VELA_DATA_DIR"], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	loaded, err := config.Load(nil, func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	svc, err := wire.NewService(st, nil, nil, migrationOptions(loaded))
	if err != nil {
		t.Fatal(err)
	}
	principal := service.Principal{KeyID: "admin", Scope: api.ScopeAdmin}
	for _, path := range []string{"live.txt", "trashed.txt"} {
		_, _, err := svc.Upload(ctx, principal, service.UploadInput{Path: path, Body: strings.NewReader(path), ContentLength: -1})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Delete(ctx, principal, service.DeleteParams{Path: "trashed.txt"}); err != nil {
		t.Fatal(err)
	}
	refs, count, err := referencedBlobs(ctx, st.DB)
	if err != nil || count != 2 {
		t.Fatal(count, err)
	}

	for _, args := range [][]string{
		{"storage", "migrate", "--from", "fs", "--to", "r2", "--dry-run"},
		{"storage", "migrate", "--from", "fs", "--to", "r2"},
		{"storage", "migrate", "--from", "fs", "--to", "r2", "--delete-source"},
		{"storage", "migrate", "--from", "fs", "--to", "r2", "--delete-source"},
		{"storage", "migrate", "--from", "r2", "--to", "fs"},
	} {
		result := execute(t, ctx, vars, nil, args...)
		if result.err != nil {
			t.Fatalf("command %v: %v %s", args, result.err, result.stderr)
		}
	}
	afterRefs, _, err := referencedBlobs(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, afterRefs) {
		t.Fatal("migration changed database references")
	}

	for id, ref := range refs {
		if err := verifyStoredBlob(ctx, blob.Backend{Store: st.Blobs}, id, ref); err != nil {
			t.Fatal(err)
		}
	}
}
