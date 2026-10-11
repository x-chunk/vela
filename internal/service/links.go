package service

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/sign"
)

// MaxLinkNoteRunes caps the free-text note on a signed link.
const MaxLinkNoteRunes = 1024

// linkURL builds the public URL of a signed link. The filename segment is
// cosmetic (spec section 8.1): it makes the URL readable and gives browsers a
// sensible download name, but verification ignores it.
func (s *Service) linkURL(l domain.Link, secret []byte) string {
	exp := l.ExpiresAt.Unix()
	return fmt.Sprintf("%s/s/%s/%s?exp=%d&sig=%s", s.opts.PublicURL, l.ID, api.EscapePath(l.Filename),
		exp, sign.Sign(secret, l.ID, exp))
}

func (s *Service) linkDTO(r domain.LinkRecord) api.Link {
	dto := api.Link{
		ID:        r.ID,
		ObjectID:  r.ObjectID,
		CreatedAt: r.CreatedAt.UTC().Truncate(time.Second),
		ExpiresAt: r.ExpiresAt.UTC(),
		Note:      r.Note,
	}
	if r.ObjectState == domain.ObjectLive {
		dto.Path = r.ObjectPath
	}
	if r.RevokedAt != nil {
		t := r.RevokedAt.UTC().Truncate(time.Second)
		dto.RevokedAt = &t
	}
	if r.KeySecret != nil {
		u := s.linkURL(r.Link, r.KeySecret)
		dto.URL = &u
	}
	return dto
}

// CreateLink issues a signed link to an object of any visibility.
func (s *Service) CreateLink(ctx context.Context, p Principal, req api.CreateLinkRequest) (api.Link, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Link{}, err
	}
	path, err := api.CleanObjectPath(req.Path)
	if err != nil {
		return api.Link{}, pathError(err)
	}
	if err := p.checkPath(path); err != nil {
		return api.Link{}, err
	}
	if n := utf8.RuneCountInString(req.Note); n > MaxLinkNoteRunes || !utf8.ValidString(req.Note) {
		return api.Link{}, invalidf("note must be valid UTF-8 of at most %d characters", MaxLinkNoteRunes)
	}
	now := s.now()
	// Link expiry is carried in the URL in whole seconds; keep the record
	// identical so the two can be compared exactly.
	var expires time.Time
	switch {
	case req.TTL != "" && req.ExpiresAt != nil:
		return api.Link{}, invalidf("set either ttl or expires_at, not both")
	case req.ExpiresAt != nil:
		expires = req.ExpiresAt.UTC().Truncate(time.Second)
	default:
		ttl := s.opts.LinkDefaultTTL
		if req.TTL != "" {
			if ttl, err = api.ParseDuration(req.TTL); err != nil {
				return api.Link{}, invalidf("%v", err)
			}
		}
		expires = now.Add(ttl).Truncate(time.Second)
	}
	if !expires.After(now) {
		return api.Link{}, invalidf("the link must expire in the future")
	}
	// Exceeding the maximum is an error, not silently clamped: the caller
	// asked for something the server will not grant (spec section 8.3).
	if expires.Sub(now) > s.opts.LinkMaxTTL {
		return api.Link{}, newErr(ErrInvalidRequest, map[string]any{"max_ttl": api.FormatDuration(s.opts.LinkMaxTTL)},
			"the link lifetime exceeds the maximum of %s", api.FormatDuration(s.opts.LinkMaxTTL))
	}

	var rec domain.LinkRecord
	err = s.store.Update(ctx, func(w Writer) error {
		o, err := w.ObjectByPath(ctx, path)
		if isNotFound(err) {
			return notFoundf("object not found: %s", path)
		}
		if err != nil {
			return err
		}
		key, err := s.activeSigningKey(ctx, w, now)
		if err != nil {
			return err
		}
		l := domain.Link{
			ID:           s.ids.New(),
			ObjectID:     o.ID,
			SigningKeyID: key.ID,
			Filename:     api.BaseName(o.Path),
			Note:         req.Note,
			CreatedAt:    now,
			ExpiresAt:    expires,
			CreatedBy:    p.KeyID,
		}
		if err := w.InsertLink(ctx, l); err != nil {
			return err
		}
		rec = domain.LinkRecord{Link: l, KeySecret: key.Secret, ObjectPath: o.Path, ObjectState: domain.ObjectLive}
		return nil
	})
	if err != nil {
		return api.Link{}, err
	}
	return s.linkDTO(rec), nil
}

// LinkParams are the parameters of GET /api/v1/links.
type LinkParams struct {
	Path           string
	IncludeExpired bool
	IncludeRevoked bool
}

// ListLinks lists signed links, newest first. It needs the rw scope because
// the listing contains working URLs, i.e. grants access by itself.
func (s *Service) ListLinks(ctx context.Context, p Principal, lp LinkParams) (api.LinkList, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.LinkList{}, err
	}
	q := domain.LinkQuery{
		Within:         p.Prefix,
		IncludeExpired: lp.IncludeExpired,
		IncludeRevoked: lp.IncludeRevoked,
		Now:            s.now(),
	}
	if lp.Path != "" {
		o, err := s.objectFor(ctx, p, api.ScopeRW, lp.Path)
		if err != nil {
			return api.LinkList{}, err
		}
		q.ObjectID = o.ID
	}
	recs, err := s.store.ListLinks(ctx, q)
	if err != nil {
		return api.LinkList{}, err
	}
	out := api.LinkList{Links: make([]api.Link, 0, len(recs))}
	for _, r := range recs {
		out.Links = append(out.Links, s.linkDTO(r))
	}
	return out, nil
}

// RevokeLink makes a link stop working immediately.
func (s *Service) RevokeLink(ctx context.Context, p Principal, id string) error {
	if err := p.require(api.ScopeRW); err != nil {
		return err
	}
	now := s.now()
	return s.store.Update(ctx, func(w Writer) error {
		r, err := w.LinkByID(ctx, id)
		// A prefixed key may only manage links to objects in its prefix; a
		// link whose object is gone has no path and is admin territory.
		if isNotFound(err) || (err == nil && p.Prefix != "" &&
			(r.ObjectState == domain.ObjectGone || !api.IsWithin(r.ObjectPath, p.Prefix))) {
			return notFoundf("link not found: %s", id)
		}
		if err != nil {
			return err
		}
		return w.RevokeLink(ctx, id, now)
	})
}

// ResolveLink verifies a signed link request and returns the content to
// serve. The order of checks follows spec section 8.2. Every failure except
// a passed expiry is reported as ErrNotFound, so a probe cannot tell a
// forged signature from a revoked link or a private object.
func (s *Service) ResolveLink(ctx context.Context, linkID, expParam, sig string) (Content, error) {
	exp, err := strconv.ParseInt(expParam, 10, 64)
	if err != nil {
		return Content{}, notFoundf("link not found")
	}
	now := s.now()
	// Expiry is decided from the URL alone, before touching the database:
	// stale links (the common case for old URLs) cost nothing.
	if now.Unix() > exp {
		return Content{}, newErr(ErrLinkExpired, nil, "this link has expired")
	}
	if !ids.Valid(linkID) {
		return Content{}, notFoundf("link not found")
	}
	r, err := s.store.LinkByID(ctx, linkID)
	if isNotFound(err) {
		return Content{}, notFoundf("link not found")
	}
	if err != nil {
		return Content{}, err
	}
	// The record's expiry must match the URL's: a valid signature over a
	// different exp cannot exist, but comparing also guards against a
	// record changed after issue.
	if r.KeySecret == nil || r.ExpiresAt.Unix() != exp || !sign.Verify(r.KeySecret, r.ID, exp, sig) {
		return Content{}, notFoundf("link not found")
	}
	if r.RevokedAt != nil || r.ObjectState != domain.ObjectLive {
		return Content{}, notFoundf("link not found")
	}
	o, err := s.store.ObjectByID(ctx, r.ObjectID)
	if isNotFound(err) {
		return Content{}, notFoundf("link not found")
	}
	if err != nil {
		return Content{}, err
	}
	if o.ExpiredAt(now) {
		return Content{}, notFoundf("link not found")
	}
	c := s.content(o)
	c.DeliveryUntil = &r.ExpiresAt
	return c, nil
}

// activeSigningKey returns the current signing key, creating one if none is
// active (first start, or every key retired by hand).
func (s *Service) activeSigningKey(ctx context.Context, w Writer, now time.Time) (domain.SigningKey, error) {
	k, err := w.ActiveSigningKey(ctx)
	if !isNotFound(err) {
		return k, err
	}
	secret, err := sign.NewKey()
	if err != nil {
		return domain.SigningKey{}, err
	}
	k = domain.SigningKey{ID: s.ids.New(), Secret: secret, CreatedAt: now}
	return k, w.InsertSigningKey(ctx, k)
}

// EnsureSigningKey creates a signing key if there is no active one. The
// server calls it at startup so the first link request does not have to.
func (s *Service) EnsureSigningKey(ctx context.Context) error {
	return s.store.Update(ctx, func(w Writer) error {
		_, err := s.activeSigningKey(ctx, w, s.now())
		return err
	})
}

// RotationResult reports a signing key rotation.
type RotationResult struct {
	NewKeyID     string
	RetiredKeys  []string
	DeletedKeys  []string
	RevokedLinks int
}

// RotateSigningKey activates a new signing key. Normally the previous key is
// retired and keeps verifying the links it signed until they expire. With
// revokeAll every older key is destroyed and all existing links stop working
// immediately, the emergency response to a leaked database.
func (s *Service) RotateSigningKey(ctx context.Context, revokeAll bool) (RotationResult, error) {
	var res RotationResult
	now := s.now()
	err := s.store.Update(ctx, func(w Writer) error {
		retired, err := w.RetireSigningKeys(ctx, now)
		if err != nil {
			return err
		}
		res.RetiredKeys = retired
		secret, err := sign.NewKey()
		if err != nil {
			return err
		}
		k := domain.SigningKey{ID: s.ids.New(), Secret: secret, CreatedAt: now}
		if err := w.InsertSigningKey(ctx, k); err != nil {
			return err
		}
		res.NewKeyID = k.ID
		if !revokeAll {
			return nil
		}
		all, err := w.SigningKeys(ctx)
		if err != nil {
			return err
		}
		for _, old := range all {
			if old.ID == k.ID {
				continue
			}
			n, err := w.RevokeLinksBySigningKey(ctx, old.ID, now)
			if err != nil {
				return err
			}
			res.RevokedLinks += n
			if err := w.DeleteSigningKey(ctx, old.ID); err != nil {
				return err
			}
			res.DeletedKeys = append(res.DeletedKeys, old.ID)
		}
		return nil
	})
	return res, err
}
