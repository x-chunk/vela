package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// Symlinks (docs/SPEC-symlinks.md) refer to a file by ID, never by path, and
// resolve one level only: a symlink always points at a file. Two rules keep
// them from widening access:
//
//   - the key prefix must cover the symlink's path and its target's path,
//     both when the symlink is created and on every authenticated read, so a
//     symlink cannot carry a key outside its prefix;
//   - anonymous access uses the effective visibility, the stricter of the
//     symlink's and its target's, so a public symlink cannot publish a
//     private file.

// symlinkMeta is the validated metadata request of a symlink operation.
type symlinkMeta struct {
	meta metaInput
	// copyMeta, when set, overrides the default: copy for new symlinks, keep
	// for retargeted ones.
	copyMeta *bool
}

// copies reports whether the source's metadata is copied before meta applies.
func (sm symlinkMeta) copies(retarget bool) bool {
	if sm.copyMeta != nil {
		return *sm.copyMeta
	}
	return !retarget
}

func (s *Service) symlinkMetaFrom(req api.CreateSymlinkRequest) (symlinkMeta, error) {
	m := metaInput{
		visibility:  req.Visibility,
		title:       req.Title,
		description: req.Description,
		tags:        req.Tags,
		disposition: req.Disposition,
	}
	if req.ExpiresAt != nil {
		m.expires = api.Some(*req.ExpiresAt)
	}
	if req.Tags != nil {
		// Copy before validateMeta normalizes in place: the request belongs
		// to the caller.
		tags := slices.Clone(*req.Tags)
		m.tags = &tags
	}
	if err := s.validateMeta(&m); err != nil {
		return symlinkMeta{}, err
	}
	return symlinkMeta{meta: m, copyMeta: req.CopyMeta}, nil
}

// copyMetaFrom copies the metadata a symlink takes from its source as a
// snapshot. expires_at is deliberately not copied: it bounds the life of an
// entry, not a property of the content.
func copyMetaFrom(o *domain.Object, src domain.Object) {
	o.Title, o.Description, o.Disposition, o.Visibility = src.Title, src.Description, src.Disposition, src.Visibility
	o.Tags = slices.Clone(src.Tags)
	if o.Tags == nil {
		o.Tags = []string{}
	}
}

// symlinkSource resolves a source path to the object it names and the file a
// new symlink must point at. A symlink source is dereferenced, so chains never
// form. The target must lie inside the key's prefix: a source symlink whose
// target is outside it is as broken for this key as one whose target is gone.
func symlinkSource(ctx context.Context, r Reader, p Principal, path string) (src domain.Object, target domain.Object, err error) {
	src, err = r.ObjectByPath(ctx, path)
	if isNotFound(err) {
		first, err := r.FirstUnder(ctx, path+"/")
		if err != nil {
			return src, target, err
		}
		if first != "" {
			return src, target, invalidf("%s is a directory; link it with recursive=true (path %s/)", path, path)
		}
		return src, target, notFoundf("object not found: %s", path)
	}
	if err != nil {
		return src, target, err
	}
	if !src.IsSymlink() {
		return src, src, nil
	}
	if !targetVisibleTo(src, p.Prefix) {
		return src, target, notFoundf("%s is a broken symlink", path)
	}
	target, err = r.ObjectByID(ctx, src.TargetID)
	if isNotFound(err) {
		return src, target, notFoundf("%s is a broken symlink", path)
	}
	return src, target, err
}

// isSymlinkError refuses to write content to the path of a symlink. Content
// is never written through a symlink into its target: the target may lie
// outside the writer's key prefix, and an overwrite of someone else's file
// would surprise everyone who links to it.
func isSymlinkError(path string) error {
	return newErr(ErrIsSymlink, map[string]any{"path": path},
		"%s is a symlink; remove it or retarget it to write content there", path)
}

// symlinksAffected counts the symlinks within prefix that refer to any of
// objs, after the caller removed objs from the live objects. Counting only
// within the key's prefix keeps the number from disclosing symlinks the key
// cannot see.
func symlinksAffected(ctx context.Context, r Reader, objs []domain.Object, prefix string) (int, error) {
	var ids []string
	for _, o := range objs {
		if !o.IsSymlink() {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return r.CountSymlinksTo(ctx, ids, prefix)
}

// targetVisibleTo reports whether a symlink's target is live and inside the
// key prefix, i.e. whether a key with that prefix may follow it. Files are
// their own target.
func targetVisibleTo(o domain.Object, prefix string) bool {
	if !o.IsSymlink() {
		return true
	}
	return o.Target.Live() && api.IsWithin(o.Target.Path, prefix)
}

// CreateSymlink creates the symlink req.Path pointing at the file req.Source
// names, or with Overwrite retargets an existing symlink in place, keeping
// its ID so that its public URL and signed links follow the new target. An
// existing file at req.Path goes to the trash instead. It reports whether a
// new symlink was created.
func (s *Service) CreateSymlink(ctx context.Context, p Principal, req api.CreateSymlinkRequest) (api.Object, bool, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.Object{}, false, err
	}
	if req.Recursive {
		return api.Object{}, false, invalidf("recursive requests are handled by CreateSymlinkTree")
	}
	source, isDir, err := api.CleanPath(req.Source)
	if err != nil {
		return api.Object{}, false, pathError(err)
	}
	if isDir {
		return api.Object{}, false, invalidf("%s is a directory; link it with recursive=true", source)
	}
	path, err := api.CleanObjectPath(req.Path)
	if err != nil {
		return api.Object{}, false, pathError(err)
	}
	if err := p.checkPath(source); err != nil {
		return api.Object{}, false, err
	}
	if err := p.checkPath(path); err != nil {
		return api.Object{}, false, err
	}
	sm, err := s.symlinkMetaFrom(req)
	if err != nil {
		return api.Object{}, false, err
	}
	now := s.now()
	var (
		result  domain.Object
		created bool
	)
	err = s.store.Update(ctx, func(w Writer) error {
		src, target, err := symlinkSource(ctx, w, p, source)
		if err != nil {
			return err
		}
		if err := p.checkPath(target.Path); err != nil {
			return err
		}
		if path == target.Path {
			return invalidf("a symlink cannot replace its own target: %s", path)
		}
		existing, err := w.ObjectByPath(ctx, path)
		occupied := err == nil
		switch {
		case isNotFound(err):
			if err := checkConflicts(ctx, w, path, nil); err != nil {
				return err
			}
		case err != nil:
			return err
		case !req.Overwrite:
			return newErr(ErrAlreadyExists, map[string]any{"path": path}, "destination already exists: %s", path)
		}
		var id string
		if occupied && existing.IsSymlink() {
			id, err = s.retargetSymlink(ctx, w, existing, src, target, sm, now)
		} else {
			if occupied {
				// A file in the way goes to the trash, as with move --force.
				if _, err := s.trashObject(ctx, w, existing, p.KeyID, now); err != nil {
					return err
				}
			}
			id, err = s.insertSymlink(ctx, w, path, src, target, sm, now)
			created = true
		}
		if err != nil {
			return err
		}
		// Re-read to resolve the target exactly as later reads will.
		result, err = w.ObjectByID(ctx, id)
		return err
	})
	if err != nil {
		return api.Object{}, false, err
	}
	return s.objectDTO(result, p.Prefix, now), created, nil
}

// insertSymlink adds a new symlink at path to target. Metadata is copied from
// src, the object the caller named, which may itself be a symlink.
func (s *Service) insertSymlink(ctx context.Context, w Writer, path string, src, target domain.Object, sm symlinkMeta, now time.Time) (string, error) {
	o := domain.Object{
		ID:          s.ids.New(),
		Path:        path,
		Kind:        domain.KindSymlink,
		TargetID:    target.ID,
		Disposition: api.Inline,
		Visibility:  s.opts.DefaultVisibility,
		Tags:        []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
		Revision:    1,
	}
	if sm.copies(false) {
		copyMetaFrom(&o, src)
	}
	sm.meta.apply(&o)
	return o.ID, w.InsertObject(ctx, o)
}

// retargetSymlink points an existing symlink at a new target in place.
func (s *Service) retargetSymlink(ctx context.Context, w Writer, o, src, target domain.Object, sm symlinkMeta, now time.Time) (string, error) {
	o.TargetID = target.ID
	if sm.copies(true) {
		copyMetaFrom(&o, src)
	}
	sm.meta.apply(&o)
	o.UpdatedAt = now
	o.Revision++
	return o.ID, w.UpdateObject(ctx, o)
}

// symlinkTargetDTO describes the target of o as a key with prefix may see
// it. A target outside the prefix is reported exactly like a purged one, so a
// symlink cannot disclose anything about objects the key cannot reach.
func symlinkTargetDTO(o domain.Object, prefix string, now time.Time) *api.SymlinkTarget {
	t := o.Target
	out := &api.SymlinkTarget{ID: o.TargetID, State: api.TargetGone}
	if t == nil || t.State == domain.ObjectGone || !api.IsWithin(t.Path, prefix) {
		return out
	}
	path := t.Path
	out.Path = &path
	if t.State == domain.ObjectTrashed {
		out.State = api.TargetTrashed
		return out
	}
	vis := t.Visibility
	out.State, out.Visibility = api.TargetOK, &vis
	out.Expired = t.ExpiresAt != nil && now.After(*t.ExpiresAt)
	return out
}

// CreateSymlinkTree links every object under the prefix req.Source to the
// same relative path under the prefix req.Path, in one transaction: either
// every symlink is created or none is. It is a snapshot; objects added to the
// source later are not linked. A destination inside the source is excluded
// from it. Symlinks in the source are dereferenced, and
// broken ones are skipped and reported. Directory policies are not copied:
// this sorts content, it does not move it.
func (s *Service) CreateSymlinkTree(ctx context.Context, p Principal, req api.CreateSymlinkRequest) (api.SymlinkTreeResponse, error) {
	out := api.SymlinkTreeResponse{Skipped: []string{}}
	if err := p.require(api.ScopeRW); err != nil {
		return out, err
	}
	source, err := api.CleanPrefix(req.Source)
	if err != nil {
		return out, pathError(err)
	}
	path, err := api.CleanPrefix(req.Path)
	if err != nil {
		return out, pathError(err)
	}
	if path == source {
		return out, invalidf("cannot link %s into itself", source)
	}
	// A destination inside the source is left out of the walk: otherwise a
	// rerun would link the symlinks of the previous run one level deeper.
	nested := api.IsWithin(path, source)
	if err := p.checkPath(source); err != nil {
		return out, err
	}
	if err := p.checkPath(path); err != nil {
		return out, err
	}
	sm, err := s.symlinkMetaFrom(req)
	if err != nil {
		return out, err
	}
	now := s.now()
	err = s.store.Update(ctx, func(w Writer) error {
		objs, err := w.ObjectsUnder(ctx, source)
		if err != nil {
			return err
		}
		if nested {
			objs = slices.DeleteFunc(objs, func(o domain.Object) bool { return api.IsWithin(o.Path, path) })
		}
		if len(objs) == 0 {
			return notFoundf("no objects under %s", source)
		}
		if len(objs) > api.MaxSymlinkTree {
			return newErr(ErrInvalidRequest, map[string]any{"limit": api.MaxSymlinkTree},
				"%s holds %d objects; link at most %d at once (link its subdirectories separately)", source, len(objs), api.MaxSymlinkTree)
		}
		type plan struct {
			src      domain.Object
			targetID string
			path     string
			existing *domain.Object
		}
		var (
			plans                 []plan
			skipped               []string
			occupied, conflicting []string
		)
		for _, o := range objs {
			targetID, targetPath := o.ID, o.Path
			if o.IsSymlink() {
				if !targetVisibleTo(o, p.Prefix) {
					skipped = append(skipped, o.Path)
					continue
				}
				targetID, targetPath = o.TargetID, o.Target.Path
			}
			np := path + o.Path[len(source):]
			// Re-validate: the new path may be too long or start with a
			// reserved name when linking into the root.
			if clean, err := api.CleanObjectPath(np); err != nil || clean != np {
				if err == nil {
					err = &api.PathError{Path: np, Reason: "not normalized"}
				}
				return pathError(err)
			}
			if np == targetPath {
				return invalidf("a symlink cannot replace its own target: %s", np)
			}
			pl := plan{src: o, targetID: targetID, path: np}
			existing, err := w.ObjectByPath(ctx, np)
			switch {
			case err == nil:
				pl.existing = &existing
				occupied = append(occupied, np)
			case !isNotFound(err):
				return err
			default:
				if err := checkConflicts(ctx, w, np, nil); err != nil {
					if errors.Is(err, ErrPathConflict) {
						conflicting = append(conflicting, np)
						continue
					}
					return err
				}
			}
			plans = append(plans, pl)
		}
		if len(conflicting) > 0 {
			return newErr(ErrPathConflict, map[string]any{"conflicts": capList(conflicting)},
				"%d destination path(s) conflict with existing files or directories", len(conflicting))
		}
		if len(occupied) > 0 && !req.Overwrite {
			return newErr(ErrAlreadyExists, map[string]any{"conflicts": capList(occupied)},
				"%d destination path(s) already exist; use overwrite", len(occupied))
		}
		for _, pl := range plans {
			target := domain.Object{ID: pl.targetID}
			switch {
			case pl.existing == nil:
				_, err = s.insertSymlink(ctx, w, pl.path, pl.src, target, sm, now)
				out.Created++
			case pl.existing.IsSymlink():
				_, err = s.retargetSymlink(ctx, w, *pl.existing, pl.src, target, sm, now)
				out.Replaced++
			default:
				if _, err := s.trashObject(ctx, w, *pl.existing, p.KeyID, now); err != nil {
					return err
				}
				_, err = s.insertSymlink(ctx, w, pl.path, pl.src, target, sm, now)
				out.Replaced++
			}
			if err != nil {
				return err
			}
		}
		if len(skipped) > 0 {
			out.Skipped = capList(skipped)
		}
		return nil
	})
	if err != nil {
		return api.SymlinkTreeResponse{Skipped: []string{}}, err
	}
	return out, nil
}
