package service

import (
	"context"
	"math"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// DeleteParams are the parameters of DELETE /api/v1/objects/{path}.
type DeleteParams struct {
	Path      string
	Recursive bool
	// Purge deletes permanently instead of moving to the trash (admin only).
	Purge   bool
	IfMatch int64
}

// Delete moves an object, or every object under a prefix, to the trash, or
// purges it permanently.
func (s *Service) Delete(ctx context.Context, p Principal, dp DeleteParams) (api.DeleteResponse, error) {
	need := api.ScopeRW
	if dp.Purge {
		need = api.ScopeAdmin
	}
	if err := p.require(need); err != nil {
		return api.DeleteResponse{}, err
	}
	path, isDir, err := api.CleanPath(dp.Path)
	if err != nil {
		return api.DeleteResponse{}, pathError(err)
	}
	if path == "" {
		return api.DeleteResponse{}, invalidf("refusing to delete the root directory")
	}
	if err := p.checkPath(path); err != nil {
		return api.DeleteResponse{}, err
	}
	if isDir && dp.IfMatch != 0 {
		return api.DeleteResponse{}, invalidf("If-Match applies to a single object, not a directory")
	}

	now := s.now()
	resp := api.DeleteResponse{TrashIDs: []string{}}
	err = s.store.Update(ctx, func(w Writer) error {
		var objs []domain.Object
		if !isDir {
			o, err := w.ObjectByPath(ctx, path)
			switch {
			case err == nil:
				if dp.IfMatch != 0 && dp.IfMatch != o.Revision {
					return newErr(ErrPreconditionFailed, map[string]any{"current_revision": o.Revision},
						"revision mismatch: object is at revision %d", o.Revision)
				}
				objs = []domain.Object{o}
			case isNotFound(err):
				// Not a file; maybe a directory addressed without the slash.
				first, err := w.FirstUnder(ctx, path+"/")
				if err != nil {
					return err
				}
				if first == "" {
					return notFoundf("object not found: %s", path)
				}
				if !dp.Recursive {
					return invalidf("%s is a directory; delete it with recursive=true (path %s/)", path, path)
				}
				isDir, path = true, path+"/"
			default:
				return err
			}
		} else if !dp.Recursive {
			return invalidf("%s is a directory; delete it with recursive=true", path)
		}
		if isDir {
			var err error
			if objs, err = w.ObjectsUnder(ctx, path); err != nil {
				return err
			}
			if len(objs) == 0 {
				return notFoundf("no objects under %s", path)
			}
		}
		for _, o := range objs {
			if dp.Purge {
				if err := w.DeleteObject(ctx, o.ID); err != nil {
					return err
				}
				if _, err := w.DeleteLinksByObject(ctx, o.ID); err != nil {
					return err
				}
				if err := s.queueBlob(ctx, w, o.BlobID, now); err != nil {
					return err
				}
				continue
			}
			id, err := s.trashObject(ctx, w, o, p.KeyID, now)
			if err != nil {
				return err
			}
			resp.TrashIDs = append(resp.TrashIDs, id)
		}
		resp.Deleted = len(objs)
		return nil
	})
	if err != nil {
		return api.DeleteResponse{}, err
	}
	return resp, nil
}

// TrashParams are the parameters of GET /api/v1/trash.
type TrashParams struct {
	Prefix string
	// Path selects entries with exactly this original path.
	Path   string
	Limit  int
	Cursor string
}

// ListTrash lists trash entries, newest first.
func (s *Service) ListTrash(ctx context.Context, p Principal, tp TrashParams) (api.TrashList, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.TrashList{}, err
	}
	q := domain.TrashQuery{}
	var err error
	if q.Prefix, err = api.CleanPrefix(tp.Prefix); err != nil {
		return api.TrashList{}, pathError(err)
	}
	if q.Within, err = p.scopeListing(q.Prefix); err != nil {
		return api.TrashList{}, err
	}
	if tp.Path != "" {
		if q.Path, err = api.CleanObjectPath(tp.Path); err != nil {
			return api.TrashList{}, pathError(err)
		}
		if err := p.checkPath(q.Path); err != nil {
			return api.TrashList{}, err
		}
	}
	limit := tp.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit {
		return api.TrashList{}, invalidf("limit must be between 1 and %d", MaxListLimit)
	}
	if q.Before, err = decodeCursor(tp.Cursor); err != nil {
		return api.TrashList{}, err
	}
	q.Limit = limit + 1
	entries, err := s.store.ListTrash(ctx, q)
	if err != nil {
		return api.TrashList{}, err
	}
	out := api.TrashList{Entries: make([]api.TrashEntry, 0, len(entries))}
	if len(entries) > limit {
		entries = entries[:limit]
		out.NextCursor = encodeCursor(entries[len(entries)-1].ID)
	}
	now := s.now()
	for _, e := range entries {
		out.Entries = append(out.Entries, s.trashDTO(e, now))
	}
	return out, nil
}

func (s *Service) trashDTO(e domain.TrashEntry, now time.Time) api.TrashEntry {
	obj := s.objectDTO(e.Object, now)
	obj.URL = nil // trashed objects are not served
	return api.TrashEntry{
		ID:           e.ID,
		OriginalPath: e.Object.Path,
		DeletedAt:    e.DeletedAt.UTC().Truncate(time.Second),
		PurgeAt:      e.DeletedAt.Add(s.opts.TrashRetention).UTC().Truncate(time.Second),
		Object:       obj,
	}
}

// trashEntryFor loads a trash entry visible to p. Entries outside the key's
// prefix are reported as missing, like everything else the key cannot list.
func (s *Service) trashEntryFor(ctx context.Context, r Reader, p Principal, id string) (domain.TrashEntry, error) {
	e, err := r.TrashByID(ctx, id)
	if isNotFound(err) || (err == nil && !api.IsWithin(e.Object.Path, p.Prefix)) {
		return domain.TrashEntry{}, notFoundf("trash entry not found: %s", id)
	}
	return e, err
}

// Restore moves a trash entry back, to its original path or to req.Path,
// under its original object ID so that its signed links work again.
func (s *Service) Restore(ctx context.Context, p Principal, id string, req api.RestoreRequest) (api.Object, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Object{}, err
	}
	var target string
	if req.Path != "" {
		var err error
		if target, err = api.CleanObjectPath(req.Path); err != nil {
			return api.Object{}, pathError(err)
		}
		if err := p.checkPath(target); err != nil {
			return api.Object{}, err
		}
	}
	now := s.now()
	var result domain.Object
	err := s.store.Update(ctx, func(w Writer) error {
		e, err := s.trashEntryFor(ctx, w, p, id)
		if err != nil {
			return err
		}
		o := e.Object
		if target != "" {
			o.Path = target
		}
		if err := s.clearTarget(ctx, w, p, o.Path, req.Overwrite, now); err != nil {
			return err
		}
		if err := checkConflicts(ctx, w, o.Path, nil); err != nil {
			return err
		}
		// An object trashed by the janitor for expiring would be trashed
		// again on the next pass; restoring it means keeping it.
		if o.ExpiredAt(now) {
			o.ExpiresAt = nil
		}
		o.UpdatedAt = now
		o.Revision++
		if err := w.DeleteTrash(ctx, e.ID); err != nil {
			return err
		}
		result = o
		return w.InsertObject(ctx, o)
	})
	if err != nil {
		return api.Object{}, err
	}
	return s.objectDTO(result, now), nil
}

// PurgeTrashEntry permanently deletes one trash entry (admin only).
func (s *Service) PurgeTrashEntry(ctx context.Context, p Principal, id string) error {
	if err := p.require(api.ScopeAdmin); err != nil {
		return err
	}
	now := s.now()
	return s.store.Update(ctx, func(w Writer) error {
		e, err := s.trashEntryFor(ctx, w, p, id)
		if err != nil {
			return err
		}
		return s.purgeEntry(ctx, w, e, now)
	})
}

// purgeEntry removes a trash entry and its links and queues its blob.
func (s *Service) purgeEntry(ctx context.Context, w Writer, e domain.TrashEntry, now time.Time) error {
	if err := w.DeleteTrash(ctx, e.ID); err != nil {
		return err
	}
	if _, err := w.DeleteLinksByObject(ctx, e.Object.ID); err != nil {
		return err
	}
	return s.queueBlob(ctx, w, e.Object.BlobID, now)
}

// PurgeTrash permanently deletes trash entries older than olderThan, or all
// entries when olderThan is nil (admin only). Entries outside the key's prefix
// are left alone.
func (s *Service) PurgeTrash(ctx context.Context, p Principal, olderThan *time.Duration) (int, error) {
	if err := p.require(api.ScopeAdmin); err != nil {
		return 0, err
	}
	cutoff := time.UnixMilli(math.MaxInt64)
	if olderThan != nil {
		if *olderThan < 0 {
			return 0, invalidf("older_than must not be negative")
		}
		cutoff = s.now().Add(-*olderThan)
	}
	return s.purgeTrashBefore(ctx, cutoff, p.Prefix)
}

// purgeTrashBefore deletes, in batches, trash entries deleted before cutoff.
// Each batch is its own transaction so that a large purge does not hold the
// write lock for long.
func (s *Service) purgeTrashBefore(ctx context.Context, cutoff time.Time, within string) (int, error) {
	const batch = 500
	total := 0
	for {
		entries, err := s.store.TrashDeletedBefore(ctx, cutoff, within, batch)
		if err != nil || len(entries) == 0 {
			return total, err
		}
		purged := 0
		now := s.now()
		err = s.store.Update(ctx, func(w Writer) error {
			for _, e := range entries {
				if err := s.purgeEntry(ctx, w, e, now); err != nil {
					if isNotFound(err) {
						continue // purged concurrently
					}
					return err
				}
				purged++
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += purged
		if len(entries) < batch || purged == 0 {
			return total, nil // done, or no progress: avoid spinning
		}
	}
}
