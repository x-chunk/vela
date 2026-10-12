package service

import (
	"context"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// PublicResult is the outcome of a request for a public path: either content
// to serve or a directory to redirect to.
type PublicResult struct {
	Content *Content
	// RedirectDir is set when the path names a directory with public content;
	// the client is redirected to the same path with a trailing slash.
	RedirectDir string
}

// ResolvePublic resolves a direct URL. Public and unlisted objects are
// served; private, expired, trashed and missing ones are all ErrNotFound.
// Answering 404 rather than 403 for private objects keeps their existence
// secret (spec section 3).
func (s *Service) ResolvePublic(ctx context.Context, rawPath string) (PublicResult, error) {
	path, err := api.CleanObjectPath(rawPath)
	if err != nil {
		return PublicResult{}, notFoundf("not found")
	}
	now := s.now()
	o, err := s.store.ObjectByPath(ctx, path)
	switch {
	case err == nil:
		if o.Visibility != api.Private && !o.ExpiredAt(now) {
			c := s.content(o)
			view, err := s.directoryPolicies(ctx)
			if err != nil {
				return PublicResult{}, err
			}
			c.NoIndex = view.Effective(api.ParentPrefix(path)).NoIndex == api.IndexOn
			return PublicResult{Content: &c}, nil
		}
		// A file cannot also be a directory, so there is nothing else here.
		return PublicResult{}, notFoundf("not found")
	case !isNotFound(err):
		return PublicResult{}, err
	}
	n, _, err := s.store.DirStats(ctx, path+"/", now)
	if err != nil {
		return PublicResult{}, err
	}
	view, err := s.directoryPolicies(ctx)
	if err != nil {
		return PublicResult{}, err
	}
	if n > 0 && view.Effective(path+"/").Index == api.IndexOn {
		return PublicResult{RedirectDir: path + "/"}, nil
	}
	return PublicResult{}, notFoundf("not found")
}

// IndexDir is a sub-directory row of the HTML index.
type IndexDir struct {
	Path        string
	Title       string
	Description string
	Count       int
	UpdatedAt   time.Time
}

// IndexListing is the data behind one HTML index page: only public,
// unexpired objects and the directories that contain at least one of them.
type IndexListing struct {
	Policies *DirPolicies
	Prefix   string
	Dirs     []IndexDir
	Files    []domain.Object
}

// Index lists a directory for the public HTML index. A directory without
// public content is ErrNotFound, except the root, which always renders.
func (s *Service) Index(ctx context.Context, rawPrefix string) (IndexListing, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return IndexListing{}, notFoundf("not found")
	}
	tree, view, err := s.publicTree(ctx, prefix)
	if err != nil {
		return IndexListing{}, err
	}
	// Both disabled and absent directories take the same query and error path.
	if view.Effective(prefix).Index == api.IndexOff || (prefix != "" && !tree.exists) {
		return IndexListing{}, notFoundf("not found")
	}
	out := IndexListing{Prefix: prefix, Files: tree.files, Policies: view}
	for _, child := range tree.children {
		policy := view.Effective(child.prefix)
		if policy.Hidden || policy.Index == api.IndexOff || child.count == 0 {
			continue
		}
		count := child.count
		if !view.HasRules() {
			count = child.total
		}
		out.Dirs = append(out.Dirs, IndexDir{Path: child.prefix, Count: count, UpdatedAt: child.latest, Title: policy.Title, Description: policy.Description})
	}
	return out, nil
}
