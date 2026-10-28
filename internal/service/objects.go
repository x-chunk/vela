package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/mimetype"
)

// Info describes the server and the calling key.
func (s *Service) Info(p Principal) api.Info {
	return api.Info{
		ServerVersion: s.opts.ServerVersion,
		APIVersion:    api.APIVersion,
		PublicURL:     s.opts.PublicURL,
		Key:           api.KeyInfo{ID: p.KeyID, Name: p.Name, Scope: p.Scope, Prefix: p.Prefix},
		Limits: api.Limits{
			MaxUploadBytes: s.opts.MaxUpload,
			MaxCreateBytes: s.opts.MaxCreate,
			LinkDefaultTTL: api.FormatDuration(s.opts.LinkDefaultTTL),
			LinkMaxTTL:     api.FormatDuration(s.opts.LinkMaxTTL),
		},
	}
}

// ListParams are the query parameters of GET /api/v1/objects.
type ListParams struct {
	Prefix       string
	Recursive    bool
	Visibilities []api.Visibility
	Limit        int
	Cursor       string
}

// Listing limits from spec section 10.4.
const (
	DefaultListLimit = 200
	MaxListLimit     = 1000
)

// List returns one page of a directory listing.
func (s *Service) List(ctx context.Context, p Principal, lp ListParams) (api.Listing, error) {
	if err := p.require(api.ScopeRO); err != nil {
		return api.Listing{}, err
	}
	prefix, err := api.CleanPrefix(lp.Prefix)
	if err != nil {
		return api.Listing{}, pathError(err)
	}
	within, err := p.scopeListing(prefix)
	if err != nil {
		return api.Listing{}, err
	}
	limit := lp.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit {
		return api.Listing{}, invalidf("limit must be between 1 and %d", MaxListLimit)
	}
	after, err := decodeCursor(lp.Cursor)
	if err != nil {
		return api.Listing{}, err
	}
	res, err := s.store.ListObjects(ctx, domain.ListQuery{
		Prefix:       prefix,
		Within:       within,
		Recursive:    lp.Recursive,
		Visibilities: lp.Visibilities,
		After:        after,
		Limit:        limit,
	})
	if err != nil {
		return api.Listing{}, err
	}
	now := s.now()
	out := api.Listing{Prefix: prefix, Dirs: res.Dirs, Objects: make([]api.Object, 0, len(res.Objects))}
	if out.Dirs == nil {
		out.Dirs = []string{}
	}
	for _, o := range res.Objects {
		out.Objects = append(out.Objects, s.objectDTO(o, p.Prefix, now))
	}
	if res.More {
		out.NextCursor = encodeCursor(res.Last)
	}
	return out, nil
}

// objectFor resolves an object path for a principal, enforcing scope and
// prefix.
func (s *Service) objectFor(ctx context.Context, p Principal, need api.Scope, rawPath string) (domain.Object, error) {
	if err := p.require(need); err != nil {
		return domain.Object{}, err
	}
	path, err := api.CleanObjectPath(rawPath)
	if err != nil {
		return domain.Object{}, pathError(err)
	}
	if err := p.checkPath(path); err != nil {
		return domain.Object{}, err
	}
	o, err := s.store.ObjectByPath(ctx, path)
	if isNotFound(err) {
		return domain.Object{}, notFoundf("object not found: %s", path)
	}
	return o, err
}

// Stat returns an object's metadata.
func (s *Service) Stat(ctx context.Context, p Principal, path string) (api.Object, error) {
	o, err := s.objectFor(ctx, p, api.ScopeRO, path)
	if err != nil {
		return api.Object{}, err
	}
	return s.objectDTO(o, p.Prefix, s.now()), nil
}

// Download locates an object's content for an API download. Unlike public
// delivery it serves every visibility and, deliberately, expired objects that
// the janitor has not trashed yet, so they can still be rescued.
func (s *Service) Download(ctx context.Context, p Principal, path string) (Content, error) {
	o, err := s.objectFor(ctx, p, api.ScopeRO, path)
	if err != nil {
		return Content{}, err
	}
	return s.content(o), nil
}

// metaInput carries optional metadata for create and upload. Nil pointers
// mean "not specified".
type metaInput struct {
	visibility  *api.Visibility
	title       *string
	description *string
	tags        *[]string
	disposition *api.Disposition
	// contentType is the client's type. For uploads and creates, "" or
	// application/octet-stream means "detect it" (a client that does not
	// know the type sends one of those); with exactType it is taken
	// literally, as PATCH does, where null asks for detection instead.
	contentType string
	exactType   bool
	expires     api.Optional[time.Time]
}

// validate normalizes the input and checks every limit before any byte of
// content is read, so bad metadata fails fast.
func (s *Service) validateMeta(m *metaInput) error {
	if m.visibility != nil && !m.visibility.Valid() {
		return invalidf("invalid visibility %q", *m.visibility)
	}
	if m.disposition != nil && !m.disposition.Valid() {
		return invalidf("invalid disposition %q", *m.disposition)
	}
	if m.title != nil {
		if err := api.ValidateTitle(*m.title); err != nil {
			return invalidf("%v", err)
		}
	}
	if m.description != nil {
		if err := api.ValidateDescription(*m.description); err != nil {
			return invalidf("%v", err)
		}
	}
	if m.tags != nil {
		tags, err := api.NormalizeTags(*m.tags)
		if err != nil {
			return invalidf("%v", err)
		}
		*m.tags = tags
	}
	if m.exactType || !mimetype.IsGeneric(m.contentType) {
		ct, err := mimetype.Normalize(m.contentType)
		if err != nil {
			return invalidf("%v", err)
		}
		m.contentType = ct
	} else {
		m.contentType = ""
	}
	if m.expires.HasValue() {
		t := m.expires.Value.UTC().Truncate(time.Second)
		if !t.After(s.clock.Now()) {
			return invalidf("expires_at must be in the future")
		}
		m.expires.Value = t
	}
	return nil
}

// apply copies the specified fields onto o.
func (m *metaInput) apply(o *domain.Object) {
	if m.visibility != nil {
		o.Visibility = *m.visibility
	}
	if m.title != nil {
		o.Title = *m.title
	}
	if m.description != nil {
		o.Description = *m.description
	}
	if m.tags != nil {
		o.Tags = *m.tags
	}
	if m.disposition != nil {
		o.Disposition = *m.disposition
	}
	if m.expires.Set {
		if m.expires.Null {
			o.ExpiresAt = nil
		} else {
			t := m.expires.Value
			o.ExpiresAt = &t
		}
	}
}

// newObjectFrom builds a new object with defaults for unspecified metadata.
func (s *Service) newObjectFrom(path string, b blobInfo, m *metaInput, now time.Time) domain.Object {
	o := domain.Object{
		ID:          s.ids.New(),
		Path:        path,
		Kind:        domain.KindFile,
		BlobID:      b.id,
		Size:        b.size,
		SHA256:      b.sha256,
		Disposition: api.Inline,
		Visibility:  s.opts.DefaultVisibility,
		Tags:        []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
		Revision:    1,
	}
	m.apply(&o)
	o.ContentType = m.contentType
	if o.ContentType == "" {
		o.ContentType = mimetype.Detect(path, b.sniff)
	}
	return o
}

// Create stores a new object from inline content (POST /api/v1/objects). It
// never overwrites.
func (s *Service) Create(ctx context.Context, p Principal, req api.CreateObjectRequest) (api.Object, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Object{}, err
	}
	path, err := api.CleanObjectPath(req.Path)
	if err != nil {
		return api.Object{}, pathError(err)
	}
	if err := p.checkPath(path); err != nil {
		return api.Object{}, err
	}
	var data []byte
	switch req.Encoding {
	case "", api.EncodingUTF8:
		if !utf8.ValidString(req.Content) {
			return api.Object{}, invalidf("content is not valid UTF-8; use the base64 encoding")
		}
		data = []byte(req.Content)
	case api.EncodingBase64:
		data, err = base64.StdEncoding.DecodeString(req.Content)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(req.Content)
		}
		if err != nil {
			return api.Object{}, invalidf("content is not valid base64: %v", err)
		}
	default:
		return api.Object{}, newErr(ErrUnsupportedEncoding, map[string]any{"supported": []string{api.EncodingUTF8, api.EncodingBase64}},
			"unsupported encoding %q (use %q or %q)", req.Encoding, api.EncodingUTF8, api.EncodingBase64)
	}
	if int64(len(data)) > s.opts.MaxCreate {
		return api.Object{}, newErr(ErrTooLarge, map[string]any{"limit_bytes": s.opts.MaxCreate},
			"content is %d bytes; inline creation is limited to %d bytes (upload larger files with PUT %s)",
			len(data), s.opts.MaxCreate, api.RouteCont+"/{path}")
	}
	m := metaInput{
		title:       &req.Title,
		description: &req.Description,
		tags:        &req.Tags,
		contentType: req.ContentType,
	}
	if req.Tags == nil {
		m.tags = nil
	}
	if req.Visibility != "" {
		m.visibility = &req.Visibility
	}
	if req.Disposition != "" {
		m.disposition = &req.Disposition
	}
	if req.ExpiresAt != nil {
		m.expires = api.Some(*req.ExpiresAt)
	}
	if err := s.validateMeta(&m); err != nil {
		return api.Object{}, err
	}
	// Fail before writing anything if the path is obviously taken.
	if err := s.checkFree(ctx, s.store, path); err != nil {
		return api.Object{}, err
	}

	b, err := s.writeBlob(ctx, bytes.NewReader(data), s.opts.MaxCreate, "")
	if err != nil {
		return api.Object{}, err
	}
	now := s.now()
	o := s.newObjectFrom(path, b, &m, now)
	err = s.store.Update(ctx, func(w Writer) error {
		if err := s.checkFree(ctx, w, path); err != nil {
			return err
		}
		return w.InsertObject(ctx, o)
	})
	if err != nil {
		s.deleteBlob(b.id)
		return api.Object{}, err
	}
	return s.objectDTO(o, p.Prefix, now), nil
}

// checkFree fails if path is taken by an object or conflicts with one.
func (s *Service) checkFree(ctx context.Context, r Reader, path string) error {
	_, err := r.ObjectByPath(ctx, path)
	switch {
	case err == nil:
		return newErr(ErrAlreadyExists, map[string]any{"path": path}, "object already exists: %s", path)
	case !isNotFound(err):
		return err
	}
	return checkConflicts(ctx, r, path, nil)
}

// UploadInput describes PUT /api/v1/content/{path}.
type UploadInput struct {
	Path string
	Body io.Reader
	// ContentLength is the declared body size, or -1 if unknown (chunked).
	ContentLength int64
	ContentType   string
	Visibility    *api.Visibility
	Title         *string
	Description   *string
	Tags          *[]string
	Disposition   *api.Disposition
	// ExpiresAt: absent leaves the expiry unchanged, Null clears it.
	ExpiresAt api.Optional[time.Time]
	// SHA256 is the expected lower-case hex digest, or "".
	SHA256 string
	// IfNoneMatch ("*") only allows creating a new object.
	IfNoneMatch bool
	// IfMatch, when non-zero, only allows overwriting this revision.
	IfMatch int64
}

// Upload streams content into a new or existing object. Overwriting keeps
// the object's ID (so signed links keep working) and every metadata field the
// request does not set. It reports whether a new object was created.
func (s *Service) Upload(ctx context.Context, p Principal, in UploadInput) (api.Object, bool, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Object{}, false, err
	}
	path, err := api.CleanObjectPath(in.Path)
	if err != nil {
		return api.Object{}, false, pathError(err)
	}
	if err := p.checkPath(path); err != nil {
		return api.Object{}, false, err
	}
	if in.ContentLength > s.opts.MaxUpload {
		return api.Object{}, false, newErr(ErrTooLarge, map[string]any{"limit_bytes": s.opts.MaxUpload},
			"content is %d bytes; the upload limit is %d bytes", in.ContentLength, s.opts.MaxUpload)
	}
	m := metaInput{
		visibility:  in.Visibility,
		title:       in.Title,
		description: in.Description,
		tags:        in.Tags,
		disposition: in.Disposition,
		contentType: in.ContentType,
		expires:     in.ExpiresAt,
	}
	if err := s.validateMeta(&m); err != nil {
		return api.Object{}, false, err
	}
	// Check preconditions before streaming possibly gigabytes; they are
	// checked again inside the transaction, which is authoritative.
	if _, err := s.uploadTarget(ctx, s.store, path, in); err != nil {
		return api.Object{}, false, err
	}

	b, err := s.writeBlob(ctx, in.Body, s.opts.MaxUpload, in.SHA256)
	if err != nil {
		return api.Object{}, false, err
	}
	now := s.now()
	var (
		result  domain.Object
		created bool
	)
	err = s.store.Update(ctx, func(w Writer) error {
		existing, err := s.uploadTarget(ctx, w, path, in)
		if err != nil {
			return err
		}
		if existing == nil {
			result = s.newObjectFrom(path, b, &m, now)
			created = true
			return w.InsertObject(ctx, result)
		}
		o := *existing
		// The old content is deleted later, not now: see gc.go.
		if err := s.queueBlob(ctx, w, o.BlobID, now); err != nil {
			return err
		}
		o.BlobID, o.Size, o.SHA256 = b.id, b.size, b.sha256
		m.apply(&o)
		o.ContentType = m.contentType
		if o.ContentType == "" {
			o.ContentType = mimetype.Detect(path, b.sniff)
		}
		o.UpdatedAt = now
		o.Revision++
		result = o
		return w.UpdateObject(ctx, o)
	})
	if err != nil {
		// The new blob was never referenced by a committed row.
		s.deleteBlob(b.id)
		return api.Object{}, false, err
	}
	return s.objectDTO(result, p.Prefix, now), created, nil
}

// uploadTarget evaluates the upload preconditions and returns the object to
// overwrite, or nil when a new object will be created.
func (s *Service) uploadTarget(ctx context.Context, r Reader, path string, in UploadInput) (*domain.Object, error) {
	o, err := r.ObjectByPath(ctx, path)
	if isNotFound(err) {
		if in.IfMatch != 0 {
			return nil, newErr(ErrPreconditionFailed, nil, "object does not exist: %s", path)
		}
		return nil, checkConflicts(ctx, r, path, nil)
	}
	if err != nil {
		return nil, err
	}
	if in.IfNoneMatch {
		return nil, newErr(ErrAlreadyExists, map[string]any{"path": path}, "object already exists: %s", path)
	}
	if in.IfMatch != 0 && in.IfMatch != o.Revision {
		return nil, newErr(ErrPreconditionFailed, map[string]any{"current_revision": o.Revision},
			"revision mismatch: object is at revision %d", o.Revision)
	}
	return &o, nil
}

// Patch updates metadata with a JSON merge patch. ifMatch, when non-zero,
// must equal the current revision.
func (s *Service) Patch(ctx context.Context, p Principal, rawPath string, patch api.ObjectPatch, ifMatch int64) (api.Object, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Object{}, err
	}
	path, err := api.CleanObjectPath(rawPath)
	if err != nil {
		return api.Object{}, pathError(err)
	}
	if err := p.checkPath(path); err != nil {
		return api.Object{}, err
	}
	m := metaInput{expires: patch.ExpiresAt}
	if patch.Visibility.HasValue() {
		m.visibility = &patch.Visibility.Value
	}
	if patch.Title.HasValue() {
		m.title = &patch.Title.Value
	}
	if patch.Description.HasValue() {
		m.description = &patch.Description.Value
	}
	if patch.Tags.HasValue() {
		tags := patch.Tags.Value
		if tags == nil {
			tags = []string{}
		}
		m.tags = &tags
	}
	if patch.Disposition.HasValue() {
		m.disposition = &patch.Disposition.Value
	}
	if patch.ContentType.HasValue() {
		m.contentType, m.exactType = patch.ContentType.Value, true
	}
	if err := s.validateMeta(&m); err != nil {
		return api.Object{}, err
	}

	// Metadata detection may make a network request. Bound the entire reset,
	// including retries, and never hold SQLite's writer while reading a blob.
	if patch.ContentType.Null {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	readObject := func(reader Reader) (domain.Object, error) {
		o, err := reader.ObjectByPath(ctx, path)
		if isNotFound(err) {
			return domain.Object{}, notFoundf("object not found: %s", path)
		}
		if err != nil {
			return domain.Object{}, err
		}
		if ifMatch != 0 && ifMatch != o.Revision {
			return domain.Object{}, newErr(ErrPreconditionFailed, map[string]any{"current_revision": o.Revision},
				"revision mismatch: object is at revision %d", o.Revision)
		}
		return o, nil
	}
	changed := errors.New("object changed during content type detection")
	for {
		var snapshot domain.Object
		var sniff []byte
		if patch.ContentType.Null {
			snapshot, err = readObject(s.store)
			if err != nil {
				return api.Object{}, err
			}
			sniff, err = s.sniffBlob(ctx, snapshot.BlobID, snapshot.Size)
			if err != nil {
				return api.Object{}, err
			}
		}
		now := s.now()
		var result domain.Object
		err = s.store.Update(ctx, func(w Writer) error {
			o, err := readObject(w)
			if err != nil {
				return err
			}
			// An overwrite or replacement at this path must not receive a type
			// detected from the previous version. Metadata revisions are checked
			// too, so the reset is applied to precisely the inspected object.
			if patch.ContentType.Null && (o.ID != snapshot.ID || o.BlobID != snapshot.BlobID || o.Revision != snapshot.Revision) {
				return changed
			}
			if patch.IsEmpty() {
				result = o
				return nil
			}
			m.apply(&o)
			// null resets a field to its default (RFC 7396 semantics as defined
			// for this resource in spec section 10.4).
			if patch.Visibility.Null {
				o.Visibility = s.opts.DefaultVisibility
			}
			if patch.Title.Null {
				o.Title = ""
			}
			if patch.Description.Null {
				o.Description = ""
			}
			if patch.Tags.Null {
				o.Tags = []string{}
			}
			if patch.Disposition.Null {
				o.Disposition = api.Inline
			}
			switch {
			case m.contentType != "":
				o.ContentType = m.contentType
			case patch.ContentType.Null:
				o.ContentType = mimetype.Detect(o.Path, sniff)
			}
			o.UpdatedAt = now
			o.Revision++
			result = o
			return w.UpdateObject(ctx, o)
		})
		if errors.Is(err, changed) {
			continue
		}
		if err != nil {
			return api.Object{}, err
		}
		return s.objectDTO(result, p.Prefix, now), nil
	}
}

func (s *Service) sniffBlob(ctx context.Context, id string, size int64) ([]byte, error) {
	// Empty contents need no read. S3 rejects any nonempty range on an
	// empty object, whereas filesystem reads would simply return EOF.
	if size == 0 {
		return nil, nil
	}
	f, err := s.blobs.Open(ctx, id, 0, min(size, 512))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
