package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/ids"
)

// Options are the tunables of the service, normally taken from the server
// configuration.
type Options struct {
	RootIndex         api.IndexMode
	PublicURL         string
	ServerVersion     string
	MaxUpload         int64
	MaxCreate         int64
	LinkDefaultTTL    time.Duration
	LinkMaxTTL        time.Duration
	LinkRetention     time.Duration
	TrashRetention    time.Duration
	DefaultVisibility api.Visibility
	// BlobGrace delays the physical deletion of replaced or purged content
	// (default DefaultBlobGrace); see gc.go.
	BlobGrace time.Duration
	// LockLease is the lease of cross-process locks (default
	// DefaultLockLease); holders renew it while they work.
	LockLease time.Duration
	// MaxConcurrentUploads bounds active upload buffers; excess uploads wait.
	MaxConcurrentUploads int
	// WebSessionIdle and WebSessionMax bound web sessions (defaults
	// DefaultWebSessionIdle and DefaultWebSessionMax); see web.go.
	WebSessionIdle time.Duration
	WebSessionMax  time.Duration
}

// Deps are the collaborators of the service.
type Deps struct {
	Store  Store
	Blobs  Blobs
	Clock  clock.Clock
	IDs    ids.Generator
	Logger *slog.Logger
}

// Service implements all use cases. It is safe for concurrent use.
type Service struct {
	store Store
	blobs Blobs
	clock clock.Clock
	ids   ids.Generator
	log   *slog.Logger
	opts  Options

	dirs      dirPolicyCache
	touch     touchThrottle
	janitorMu sync.Mutex
	gcMu      sync.Mutex
	backupMu  sync.Mutex
	uploads   chan struct{}
	// beforeJanitorTask is a test hook called before each janitor task.
	beforeJanitorTask func(task string)
	// owner identifies this process in cross-process advisory locks.
	owner string
}

// New validates the options and returns a service.
func New(d Deps, o Options) (*Service, error) {
	if d.Store == nil || d.Blobs == nil || d.Clock == nil || d.IDs == nil {
		return nil, errors.New("service: store, blobs, clock and ids are required")
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if !o.DefaultVisibility.Valid() {
		return nil, fmt.Errorf("service: invalid default visibility %q", o.DefaultVisibility)
	}
	if o.MaxUpload <= 0 || o.MaxCreate <= 0 || o.LinkDefaultTTL <= 0 || o.LinkMaxTTL < o.LinkDefaultTTL {
		return nil, errors.New("service: invalid size or TTL limits")
	}
	if o.RootIndex == "" {
		o.RootIndex = api.IndexOn
	}
	if o.RootIndex != api.IndexOn && o.RootIndex != api.IndexOff {
		return nil, errors.New("service: root index must be on or off")
	}
	o.PublicURL = strings.TrimSuffix(o.PublicURL, "/")
	if o.BlobGrace <= 0 {
		o.BlobGrace = DefaultBlobGrace
	}
	if o.LockLease <= 0 {
		o.LockLease = DefaultLockLease
	}
	if o.MaxConcurrentUploads <= 0 {
		o.MaxConcurrentUploads = 4
	}
	if o.WebSessionIdle <= 0 {
		o.WebSessionIdle = DefaultWebSessionIdle
	}
	if o.WebSessionMax <= 0 {
		o.WebSessionMax = DefaultWebSessionMax
	}
	host, _ := os.Hostname()
	svc := &Service{
		uploads: make(chan struct{}, o.MaxConcurrentUploads),
		store:   d.Store,
		blobs:   d.Blobs,
		clock:   d.Clock,
		ids:     d.IDs,
		log:     d.Logger,
		opts:    o,
		touch:   touchThrottle{last: map[string]time.Time{}},
		owner:   fmt.Sprintf("%s:%d:%s", host, os.Getpid(), randomToken()),
	}
	if _, err := svc.directoryPolicies(context.Background()); err != nil {
		return nil, fmt.Errorf("load directory policies: %w", err)
	}
	return svc, nil
}

func randomToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// now returns the current time truncated to milliseconds, the precision of
// the database, so values compare equal before and after a round trip.
func (s *Service) now() time.Time { return s.clock.Now().UTC().Truncate(time.Millisecond) }

// Options returns the effective options.
func (s *Service) Options() Options { return s.opts }

// Health checks the database and the blob store.
func (s *Service) Health(ctx context.Context) error {
	if err := s.store.Ping(ctx); err != nil {
		return err
	}
	return s.blobs.Check(ctx)
}

// objectDTO converts an object to its API representation for a key with
// prefix; see symlinkTargetDTO for what a prefix hides.
func (s *Service) objectDTO(o domain.Object, prefix string, now time.Time) api.Object {
	dto := api.Object{
		ID:                  o.ID,
		Path:                o.Path,
		Name:                api.BaseName(o.Path),
		Kind:                api.KindFile,
		Size:                o.Size,
		SHA256:              o.SHA256,
		ContentType:         o.ContentType,
		Disposition:         o.Disposition,
		Visibility:          o.Visibility,
		EffectiveVisibility: o.Visibility,
		Title:               o.Title,
		Description:         o.Description,
		Tags:                o.Tags,
		CreatedAt:           o.CreatedAt.UTC().Truncate(time.Second),
		UpdatedAt:           o.UpdatedAt.UTC().Truncate(time.Second),
		Expired:             o.ExpiredAt(now),
		Revision:            o.Revision,
	}
	if dto.Tags == nil {
		dto.Tags = []string{}
	}
	if o.ExpiresAt != nil {
		t := o.ExpiresAt.UTC().Truncate(time.Second)
		dto.ExpiresAt = &t
	}
	if o.IsSymlink() {
		dto.Kind = api.KindSymlink
		dto.EffectiveVisibility = o.EffectiveVisibility()
		dto.Target = symlinkTargetDTO(o, prefix, now)
		if dto.Target.State != api.TargetOK {
			// Content fields describe the target, which this key cannot see.
			dto.Size, dto.SHA256, dto.ContentType = 0, "", ""
		}
	}
	if dto.EffectiveVisibility != api.Private {
		u := api.PublicURL(s.opts.PublicURL, o.Path)
		dto.URL = &u
	}
	return dto
}

// Content locates the bytes of an object for delivery.
type Content struct {
	Object domain.Object
	// NoIndex applies the directory crawler policy to public delivery.
	NoIndex bool
	// BlobPath is the blob location relative to the blobs root.
	BlobPath string
	// DeliveryUntil caps external grants to the lifetime of a signed link.
	DeliveryUntil *time.Time
}

func (s *Service) content(o domain.Object) Content {
	return Content{Object: o, BlobPath: s.blobs.RelPath(o.BlobID)}
}

// OpenContent opens a full blob for administrative readers.
func (s *Service) OpenContent(c Content) (io.ReadCloser, error) {
	return s.OpenRange(context.Background(), c, 0, -1)
}

// OpenRange reads only the authorized content range and honors request cancellation.
func (s *Service) OpenRange(ctx context.Context, c Content, off, length int64) (io.ReadCloser, error) {
	f, err := s.blobs.Open(ctx, c.Object.BlobID, off, length)
	if err != nil {
		return nil, fmt.Errorf("open content of %s: %w", c.Object.ID, err)
	}
	return f, nil
}

// ContentURL requests an optional backend delivery grant after authorization.
func (s *Service) ContentURL(ctx context.Context, c Content, typ, disposition, cache string) (string, error) {
	backend, ok := s.blobs.(interface {
		ContentURL(context.Context, string, string, string, string, time.Duration) (string, error)
	})
	if !ok {
		return "", nil
	}
	ttl := time.Hour
	for _, until := range []*time.Time{c.Object.ExpiresAt, c.DeliveryUntil} {
		if until != nil {
			ttl = min(ttl, until.Sub(s.clock.Now()))
		}
	}
	return backend.ContentURL(ctx, c.Object.BlobID, typ, disposition, cache, ttl)
}

// blobInfo describes a committed blob.
type blobInfo struct {
	id     string
	size   int64
	sha256 string
	sniff  []byte
}

// writeBlob streams r into a new blob, enforcing limit and, when wantSHA256 is
// set, verifying the checksum before the database may reference it. The caller
// must delete the blob if the database transaction that references it fails.
func (s *Service) writeBlob(ctx context.Context, r io.Reader, limit int64, wantSHA256 string) (blobInfo, error) {
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	case <-ctx.Done():
		return blobInfo{}, newErr(ErrUploadInterrupted, nil, "upload interrupted")
	}
	sn := &sniffer{}
	lr := &limitReader{r: r, remaining: limit, ctx: ctx}
	id := s.ids.New()
	result, err := s.blobs.Put(ctx, id, io.TeeReader(lr, sn), content.PutOptions{MaxSize: limit, SizeHint: -1, ExpectSHA256: wantSHA256})
	switch {
	case lr.exceeded || errors.Is(err, content.ErrTooLarge):
		return blobInfo{}, newErr(ErrTooLarge, map[string]any{"limit_bytes": limit}, "content exceeds the limit of %d bytes", limit)
	case ctx.Err() != nil || errors.Is(err, errBodyRead):
		return blobInfo{}, newErr(ErrUploadInterrupted, nil, "upload interrupted")
	case errors.Is(err, content.ErrChecksumMismatch):
		return blobInfo{}, newErr(ErrChecksumMismatch, map[string]any{"expected": strings.ToLower(wantSHA256), "actual": result.SHA256}, "checksum mismatch: expected %s, got %s", strings.ToLower(wantSHA256), result.SHA256)
	case err != nil:
		return blobInfo{}, fmt.Errorf("write blob: %w", err)
	}
	return blobInfo{id: id, size: result.Size, sha256: result.SHA256, sniff: sn.buf}, nil
}

// deleteBlob removes a blob that no committed row has ever referenced (the
// transaction meant to reference it failed). Blobs that were referenced go
// through queueBlob instead. Failure only leaves an orphan, which the janitor
// collects, so it is logged rather than returned.
func (s *Service) deleteBlob(id string) {
	if err := s.blobs.Delete(context.Background(), id); err != nil {
		s.log.Warn("delete blob failed; the janitor will retry", "blob_id", id, "error", err)
	}
}

var errBodyRead = errors.New("read request body")

// limitReader stops reading as soon as more than remaining bytes arrive, so
// an oversized upload is cut off immediately instead of being written out.
type limitReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
	ctx       context.Context
}

func (l *limitReader) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	if l.remaining < 0 {
		l.exceeded = true
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		l.exceeded = true
		return n, ErrTooLarge
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("%w: %w", errBodyRead, err)
	}
	return n, err
}

// sniffer keeps the first 512 bytes for content type detection, the amount
// http.DetectContentType considers.
type sniffer struct{ buf []byte }

func (s *sniffer) Write(p []byte) (int, error) {
	if room := 512 - len(s.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		s.buf = append(s.buf, p[:room]...)
	}
	return len(p), nil
}

// checkConflicts enforces the file/directory rule from spec section 4.2 for
// a path about to be created: no ancestor may be a file and the path itself
// must not be a directory. Paths in ignore are about to move away and do not
// count.
func checkConflicts(ctx context.Context, r Reader, path string, ignore map[string]bool) error {
	for _, anc := range api.Ancestors(path) {
		if ignore[anc] {
			continue
		}
		p, err := r.FirstExisting(ctx, []string{anc})
		if err != nil {
			return err
		}
		if p != "" {
			return newErr(ErrPathConflict, map[string]any{"conflicting_path": p},
				"a file exists at a parent path: %s", p)
		}
	}
	if len(ignore) == 0 {
		p, err := r.FirstUnder(ctx, path+"/")
		if err != nil {
			return err
		}
		if p != "" {
			return newErr(ErrPathConflict, map[string]any{"conflicting_path": p},
				"%s is a directory (it contains %s)", path, p)
		}
		return nil
	}
	under, err := r.ObjectsUnder(ctx, path+"/")
	if err != nil {
		return err
	}
	for _, o := range under {
		if !ignore[o.Path] {
			return newErr(ErrPathConflict, map[string]any{"conflicting_path": o.Path},
				"%s is a directory (it contains %s)", path, o.Path)
		}
	}
	return nil
}

// trashObject moves an object into the trash inside a transaction.
func (s *Service) trashObject(ctx context.Context, w Writer, o domain.Object, by string, now time.Time) (string, error) {
	e := domain.TrashEntry{ID: s.ids.New(), Object: o, DeletedAt: now, DeletedBy: by}
	if err := w.InsertTrash(ctx, e); err != nil {
		return "", err
	}
	if err := w.DeleteObject(ctx, o.ID); err != nil {
		return "", err
	}
	return e.ID, nil
}

// clearTarget handles an occupied destination: an error without overwrite,
// otherwise the occupant goes to the trash.
func (s *Service) clearTarget(ctx context.Context, w Writer, p Principal, to string, overwrite bool, now time.Time) error {
	occupant, err := w.ObjectByPath(ctx, to)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !overwrite {
		return newErr(ErrAlreadyExists, map[string]any{"path": to}, "destination already exists: %s", to)
	}
	_, err = s.trashObject(ctx, w, occupant, p.KeyID, now)
	return err
}

// Cursors are opaque to clients; base64 keeps them URL-safe and discourages
// clients from parsing them.
func encodeCursor(key string) string {
	if key == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func decodeCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(b) == 0 {
		return "", invalidf("invalid cursor")
	}
	return string(b), nil
}

// touchThrottle limits last_used_at updates to one per key per minute, so
// authentication does not turn every request into a database write.
type touchThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (t *touchThrottle) due(id string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.last[id]; ok && now.Sub(prev) < time.Minute {
		return false
	}
	t.last[id] = now
	return true
}

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }
