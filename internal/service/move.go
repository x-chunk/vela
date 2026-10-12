package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"go.xchunk.org/vela/api"
)

// maxReportedConflicts caps details.conflicts in a failed recursive move.
const maxReportedConflicts = 100

// Move renames or moves an object, or with Recursive every object under a
// prefix, in one transaction. Objects keep their IDs, so signed links keep
// working; public URLs change.
func (s *Service) Move(ctx context.Context, p Principal, req api.MoveRequest) (int, error) {
	out, err := s.MoveWithPolicies(ctx, p, req)
	return out.Moved, err
}

// MoveWithPolicies atomically renames objects and recursive directory rules.
func (s *Service) MoveWithPolicies(ctx context.Context, p Principal, req api.MoveRequest) (api.MoveResponse, error) {
	if err := p.require(api.ScopeRW); err != nil {
		return api.MoveResponse{}, err
	}
	if req.Recursive {
		return s.moveTree(ctx, p, req)
	}
	from, err := api.CleanObjectPath(req.From)
	if err != nil {
		return api.MoveResponse{}, pathError(err)
	}
	to, err := api.CleanObjectPath(req.To)
	if err != nil {
		return api.MoveResponse{}, pathError(err)
	}
	if err := p.checkPath(from); err != nil {
		return api.MoveResponse{}, err
	}
	if err := p.checkPath(to); err != nil {
		return api.MoveResponse{}, err
	}
	if from == to {
		return api.MoveResponse{}, invalidf("source and destination are the same: %s", from)
	}
	now := s.now()
	err = s.store.Update(ctx, func(w Writer) error {
		o, err := w.ObjectByPath(ctx, from)
		if isNotFound(err) {
			return notFoundf("object not found: %s", from)
		}
		if err != nil {
			return err
		}
		if err := s.clearTarget(ctx, w, p, to, req.Overwrite, now); err != nil {
			return err
		}
		// The source vacates its path, so moving "a" to "a/b" is legal.
		if err := checkConflicts(ctx, w, to, map[string]bool{from: true}); err != nil {
			return err
		}
		o.Path = to
		o.UpdatedAt = now
		o.Revision++
		return w.UpdateObject(ctx, o)
	})
	if err != nil {
		return api.MoveResponse{}, err
	}
	return api.MoveResponse{Moved: 1}, nil
}

func (s *Service) moveTree(ctx context.Context, p Principal, req api.MoveRequest) (api.MoveResponse, error) {
	from, err := api.CleanPrefix(req.From)
	if err != nil {
		return api.MoveResponse{}, pathError(err)
	}
	to, err := api.CleanPrefix(req.To)
	if err != nil {
		return api.MoveResponse{}, pathError(err)
	}
	if from == "" {
		return api.MoveResponse{}, invalidf("cannot move the root directory")
	}
	if from == to {
		return api.MoveResponse{}, invalidf("source and destination are the same: %s", from)
	}
	if api.IsWithin(to, from) {
		return api.MoveResponse{}, invalidf("cannot move %s into itself (%s)", from, to)
	}
	if err := p.checkPath(from); err != nil {
		return api.MoveResponse{}, err
	}
	if err := p.checkPath(to); err != nil {
		return api.MoveResponse{}, err
	}

	now := s.now()
	out := api.MoveResponse{}
	err = s.store.Update(ctx, func(w Writer) error {
		objs, err := w.ObjectsUnder(ctx, from)
		if err != nil {
			return err
		}
		policies, err := w.DirPolicies(ctx)
		if err != nil {
			return err
		}
		var movingPolicies []api.DirPolicy
		var destinationPolicies []string
		for _, rule := range policies {
			switch {
			case strings.HasPrefix(rule.Prefix, from):
				moved := rule
				moved.Prefix = to + strings.TrimPrefix(rule.Prefix, from)
				clean, err := api.CleanPrefix(moved.Prefix)
				if err != nil {
					return pathError(err)
				}
				if clean != moved.Prefix {
					return invalidf("destination policy prefix is not normalized")
				}
				if moved.Prefix == "" {
					if err := p.require(api.ScopeAdmin); err != nil {
						return err
					}
				}
				if err := validateDirPolicy(moved); err != nil {
					return err
				}
				movingPolicies = append(movingPolicies, moved)
			case api.IsWithin(rule.Prefix, to):
				destinationPolicies = append(destinationPolicies, rule.Prefix)
			}
		}
		if len(objs) == 0 && len(movingPolicies) == 0 {
			return notFoundf("no objects or directory policies under %s", from)
		}
		if len(destinationPolicies) > 0 && !req.Overwrite {
			return newErr(ErrConflict, map[string]any{"conflicting_policies": capList(destinationPolicies)}, "destination directory policies already exist; use overwrite")
		}
		moving := make(map[string]bool, len(objs))
		for _, o := range objs {
			moving[o.Path] = true
		}
		targets := make([]string, len(objs))
		var occupied, conflicts []string
		for i, o := range objs {
			np := to + o.Path[len(from):]
			// Re-validate: the new path may be too long or start with a
			// reserved name when moving to the root.
			if clean, err := api.CleanObjectPath(np); err != nil || clean != np {
				if err == nil {
					err = &api.PathError{Path: np, Reason: "not normalized"}
				}
				return pathError(err)
			}
			targets[i] = np
			if !moving[np] {
				if _, err := w.ObjectByPath(ctx, np); err == nil {
					occupied = append(occupied, np)
					continue
				} else if !isNotFound(err) {
					return err
				}
			}
			if err := checkConflicts(ctx, w, np, moving); err != nil {
				if errors.Is(err, ErrPathConflict) {
					conflicts = append(conflicts, np)
					continue
				}
				return err
			}
		}
		if len(conflicts) > 0 {
			return newErr(ErrPathConflict, map[string]any{"conflicts": capList(conflicts)},
				"%d destination path(s) conflict with existing files or directories", len(conflicts))
		}
		if len(occupied) > 0 && !req.Overwrite {
			return newErr(ErrAlreadyExists, map[string]any{"conflicts": capList(occupied)},
				"%d destination path(s) already exist; use overwrite", len(occupied))
		}
		for _, path := range occupied {
			o, err := w.ObjectByPath(ctx, path)
			if err != nil {
				return err
			}
			if _, err := s.trashObject(ctx, w, o, p.KeyID, now); err != nil {
				return err
			}
		}
		// Two phases, because a moved object's new path may equal another
		// moved object's old path (a/b/ -> a/ maps a/b/b/c onto a/b/c while
		// a/b/c itself moves to a/c). Parking every object on a unique
		// placeholder path first keeps the UNIQUE(path) index satisfied.
		for _, o := range objs {
			o.Path = "\x00moving/" + o.ID
			if err := w.UpdateObject(ctx, o); err != nil {
				return err
			}
		}
		for i, o := range objs {
			o.Path = targets[i]
			o.UpdatedAt = now
			o.Revision++
			if err := w.UpdateObject(ctx, o); err != nil {
				return err
			}
		}
		// Remove source names before insertion to support moves to an ancestor.
		for _, rule := range policies {
			if strings.HasPrefix(rule.Prefix, from) {
				if err := w.DeleteDirPolicy(ctx, rule.Prefix); err != nil {
					return err
				}
			}
		}
		if req.Overwrite {
			for _, prefix := range destinationPolicies {
				if err := w.DeleteDirPolicy(ctx, prefix); err != nil {
					return err
				}
			}
		}
		for _, rule := range movingPolicies {
			rule.Revision++
			rule.UpdatedAt = now
			if err := w.PutDirPolicy(ctx, rule); err != nil {
				return err
			}
		}
		out.Moved, out.PoliciesMoved = len(objs), len(movingPolicies)
		return nil
	})
	if err != nil {
		return api.MoveResponse{}, err
	}
	s.invalidateDirs()
	return out, nil
}

func capList(paths []string) []string {
	sort.Strings(paths)
	if len(paths) > maxReportedConflicts {
		return paths[:maxReportedConflicts]
	}
	return paths
}
