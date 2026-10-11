// Package blob stores file contents on the local filesystem.
//
// Blobs are named by random IDs that have nothing to do with logical paths,
// so renames and moves are pure database updates. Every blob is written to
// tmp/ first and published in blobs/ only when complete and verified; a crash
// can leave garbage in tmp/ or an unreferenced blob, both of which the janitor
// removes, but never a truncated file under a referenced name.
//
// Layout under the data directory:
//
//	blobs/      0755  readable by nginx (www-data) for X-Accel-Redirect
//	  ab/cd/ID  0644  shard dirs from the last four ID characters
//	tmp/        0700  in-flight uploads, private to velad
//
// Permissions are set explicitly with chmod after creation instead of relying
// on the process umask, which differs between hosts and container runtimes.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
)

const (
	blobsDirMode = 0o755
	blobFileMode = 0o644
	tmpDirMode   = 0o700
	tmpPattern   = "upload-*"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = content.ErrNotFound

// Store is a filesystem blob store rooted at a data directory.
type Store struct {
	blobsDir string
	tmpDir   string
}

// Open prepares blobs/ and tmp/ under dataDir and verifies that both are on the
// same filesystem, because exclusive blob publication uses a hard link.
// The rename probe rejects cross-device configurations before any upload.
func Open(dataDir string) (*Store, error) {
	s := &Store{
		blobsDir: filepath.Join(dataDir, "blobs"),
		tmpDir:   filepath.Join(dataDir, "tmp"),
	}
	if err := ensureDir(s.blobsDir, blobsDirMode); err != nil {
		return nil, err
	}
	if err := ensureDir(s.tmpDir, tmpDirMode); err != nil {
		return nil, err
	}
	if err := s.probeRename(); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureDir creates dir and any missing parents durably (see mkdirDurable)
// and sets its mode explicitly, also when it already existed.
func ensureDir(dir string, mode os.FileMode) error {
	parent := filepath.Dir(dir)
	if parent != dir {
		if _, err := os.Stat(parent); errors.Is(err, fs.ErrNotExist) {
			if err := ensureDir(parent, blobsDirMode); err != nil {
				return err
			}
		}
	}
	if err := mkdirDurable(dir, mode); err != nil {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// mkdirDurable creates one directory and, if it did not exist, fsyncs its
// parent. Creating a directory changes the parent's entries; until the
// parent is synced, a power loss can make the new directory, and every file
// renamed into it, disappear even though those files were synced themselves.
// An existing directory is not an error.
func mkdirDurable(dir string, mode os.FileMode) error {
	err := os.Mkdir(dir, mode)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return syncDir(filepath.Dir(dir))
}

func (s *Store) probeRename() error {
	f, err := os.CreateTemp(s.tmpDir, ".probe-*")
	if err != nil {
		return fmt.Errorf("tmp directory is not writable: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	dst := filepath.Join(s.blobsDir, filepath.Base(name))
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot rename from %s to %s (they must be on the same filesystem): %w", s.tmpDir, s.blobsDir, err)
	}
	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("remove probe file: %w", err)
	}
	return nil
}

// shard returns the relative directory of a blob. ULIDs start with a
// timestamp, so sharding on the leading characters would put everything
// uploaded within days into one directory; the trailing characters are random.
func shard(id string) string {
	n := len(id)
	return id[n-4:n-2] + "/" + id[n-2:]
}

// RelPath returns the blob path relative to blobs/, with forward slashes. It
// is the suffix of the X-Accel-Redirect URI.
func (s *Store) RelPath(id string) string {
	return shard(id) + "/" + id
}

func (s *Store) absPath(id string) (string, error) {
	// IDs come from our own database, but validating them here keeps a
	// corrupted row from turning into a path traversal.
	if !ids.Valid(id) {
		return "", fmt.Errorf("invalid blob id %q", id)
	}
	return filepath.Join(s.blobsDir, filepath.FromSlash(s.RelPath(id))), nil
}

// Pending is a blob being written. Exactly one of Commit or Abort must be
// called; Abort after Commit is a no-op, so "defer p.Abort()" is safe.
type Pending struct {
	s    *Store
	f    *os.File
	done bool
}

// Create starts a new blob in tmp/.
func (s *Store) Create() (*Pending, error) {
	f, err := os.CreateTemp(s.tmpDir, tmpPattern)
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	return &Pending{s: s, f: f}, nil
}

// Write appends to the pending blob.
func (p *Pending) Write(b []byte) (int, error) { return p.f.Write(b) }

// Commit makes the blob durable and visible under id. The order matters for
// crash safety: chmod it readable for nginx first and fsync afterwards, so
// that the final mode is on disk with the data; create missing shard
// directories, each followed by an fsync of its parent; publish an exclusive
// link in the shard; fsync the shard directory so that publication survives a
// power loss; unlink the temporary name. Only then may the database reference
// the blob.
func (p *Pending) Commit(id string) (err error) {
	if p.done {
		return errors.New("blob already committed or aborted")
	}
	p.done = true
	tmpName := p.f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	dst, err := p.s.absPath(id)
	if err != nil {
		_ = p.f.Close()
		return err
	}
	if err := p.f.Chmod(blobFileMode); err != nil {
		_ = p.f.Close()
		return fmt.Errorf("chmod blob: %w", err)
	}
	if err := syncFile(p.f); err != nil {
		_ = p.f.Close()
		return fmt.Errorf("fsync blob: %w", err)
	}
	if err := p.f.Close(); err != nil {
		return fmt.Errorf("close blob: %w", err)
	}
	if err := p.s.mkShard(id); err != nil {
		return err
	}
	// An exclusive link publishes complete bytes atomically and prevents an ID
	// collision from replacing an immutable blob. Both directories share a device.
	if err := os.Link(tmpName, dst); err != nil {
		return fmt.Errorf("commit blob: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		_ = os.Remove(dst)
		return err
	}
	_ = os.Remove(tmpName) // An abandoned temporary link is safe for the janitor.
	return nil
}

// Abort discards the pending blob.
func (p *Pending) Abort() {
	if p.done {
		return
	}
	p.done = true
	_ = p.f.Close()
	_ = os.Remove(p.f.Name())
}

func (s *Store) mkShard(id string) error {
	dir := s.blobsDir
	for _, part := range strings.Split(shard(id), "/") {
		dir = filepath.Join(dir, part)
		if err := mkdirDurable(dir, blobsDirMode); err != nil {
			return fmt.Errorf("create shard directory: %w", err)
		}
	}
	return nil
}

// syncObserver, when set by tests, sees every fsync with the path and the
// file mode at that moment.
var syncObserver func(path string, mode fs.FileMode)

func syncFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return err
	}
	if syncObserver != nil {
		if fi, err := f.Stat(); err == nil {
			syncObserver(f.Name(), fi.Mode().Perm())
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- dir is derived from a validated blob ID.
	if err != nil {
		return fmt.Errorf("open directory for fsync: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := syncFile(d); err != nil {
		return fmt.Errorf("fsync directory: %w", err)
	}
	return nil
}

// Open opens a blob for reading. The returned file supports Seek and Stat,
// which http.ServeContent needs for Range requests.
func (s *Store) Open(id string) (*os.File, error) {
	p, err := s.absPath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) // #nosec G304 -- path is derived from a validated blob ID.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return f, nil
}

// Delete removes a blob. Deleting a missing blob is not an error, which keeps
// purges and janitor retries idempotent.
func (s *Store) Delete(id string) error {
	p, err := s.absPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// Walk calls fn for every blob on disk with its modification time. Files whose
// names are not blob IDs (for example health-check probes) are skipped.
func (s *Store) Walk(ctx context.Context, fn func(id string, modTime time.Time) error) error {
	return filepath.WalkDir(s.blobsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !ids.Valid(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // deleted concurrently
		}
		if err != nil {
			return err
		}
		return fn(d.Name(), info.ModTime())
	})
}

// CleanTmp removes temporary files last modified before cutoff. Active uploads
// keep touching their file, so only abandoned ones are old enough to match.
func (s *Store) CleanTmp(cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(s.tmpDir)
	if err != nil {
		return 0, fmt.Errorf("read tmp directory: %w", err)
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.tmpDir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// Check verifies that blobs/ is writable; used by /healthz.
func (s *Store) Check() error {
	f, err := os.CreateTemp(s.blobsDir, ".healthz-*")
	if err != nil {
		return fmt.Errorf("blobs directory is not writable: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// Dir returns the blobs directory (used by backups).
func (s *Store) Dir() string { return s.blobsDir }
