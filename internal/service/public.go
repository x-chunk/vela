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
// secret (spec section 3). A symlink is served by its effective visibility
// and only while it and its target are live, so a broken symlink and a
// public symlink to a private file look exactly like a missing path.
func (s *Service) ResolvePublic(ctx context.Context, rawPath string) (PublicResult, error) {
	path, err := api.CleanObjectPath(rawPath)
	if err != nil {
		return PublicResult{}, notFoundf("not found")
	}
	now := s.now()
	o, err := s.store.ObjectByPath(ctx, path)
	switch {
	case err == nil:
		if o.EffectiveVisibility() != api.Private && o.LiveAt(now) {
			return s.publicContent(ctx, o)
		}
		// A file cannot also be a directory, so there is nothing else here.
		return PublicResult{}, notFoundf("not found")
	case !isNotFound(err):
		return PublicResult{}, err
	}
	exists, err := s.publicDirExists(ctx, path+"/", now)
	if err != nil {
		return PublicResult{}, err
	}
	if exists {
		return PublicResult{RedirectDir: path + "/"}, nil
	}
	return PublicResult{}, notFoundf("not found")
}

// publicContent prepares an object that may be served for delivery, with the
// crawler policy of its directory.
func (s *Service) publicContent(ctx context.Context, o domain.Object) (PublicResult, error) {
	c := s.content(o)
	view, err := s.directoryPolicies(ctx)
	if err != nil {
		return PublicResult{}, err
	}
	c.NoIndex = view.Effective(api.ParentPrefix(o.Path)).NoIndex == api.IndexOn
	return PublicResult{Content: &c}, nil
}

// publicDirExists reports whether a non-root directory has an index page for
// anonymous visitors: an enabled index above at least one live public object.
// The lookup is bounded and validated against the policy generation, so a
// concurrent move cannot pair new paths with old rules.
func (s *Service) publicDirExists(ctx context.Context, dir string, now time.Time) (bool, error) {
	for {
		view, err := s.directoryPolicies(ctx)
		if err != nil {
			return false, err
		}
		exists, err := s.store.HasPublicObjects(ctx, dir, now)
		if err != nil {
			return false, err
		}
		version, err := s.store.DirPoliciesVersion(ctx)
		if err != nil {
			return false, err
		}
		if version == view.version {
			return exists && view.Effective(dir).Index == api.IndexOn, nil
		}
	}
}

// IndexDir is a sub-directory row of the HTML index.
type IndexDir struct {
	Path        string
	Title       string
	Description string
	Count       int
	UpdatedAt   time.Time
	// Hidden and IndexOff describe the directory's policy for anonymous
	// visitors. Only signed-in views can contain such rows.
	Hidden   bool
	IndexOff bool
}

// PolicyView resolves the directory policies that shape page navigation:
// breadcrumbs, the parent link and the page's own metadata.
type PolicyView interface {
	Effective(prefix string) api.EffectiveDirPolicy
	HasRules() bool
}

// IndexListing is the data behind one HTML index page. Anonymous listings
// hold only public, unexpired objects and the directories that lead to them;
// signed-in listings may add the key's own subtree (see IndexFor).
type IndexListing struct {
	Policies PolicyView
	Prefix   string
	Dirs     []IndexDir
	Files    []domain.Object
}

// Index lists a directory for the public HTML index. A directory without
// public content is ErrNotFound, except the root, which can render empty when enabled.
func (s *Service) Index(ctx context.Context, rawPrefix string) (IndexListing, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return IndexListing{}, notFoundf("not found")
	}
	listing, _, err := s.publicIndex(ctx, prefix)
	return listing, err
}

func (s *Service) publicIndex(ctx context.Context, prefix string) (IndexListing, *DirPolicies, error) {
	tree, view, err := s.publicTree(ctx, prefix, true)
	if err != nil {
		return IndexListing{}, nil, err
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
	return out, view, nil
}
