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
	if n > 0 {
		return PublicResult{RedirectDir: path + "/"}, nil
	}
	return PublicResult{}, notFoundf("not found")
}

// IndexDir is a sub-directory row of the HTML index.
type IndexDir struct {
	Path      string
	Count     int
	UpdatedAt time.Time
}

// IndexListing is the data behind one HTML index page: only public,
// unexpired objects and the directories that contain at least one of them.
type IndexListing struct {
	Prefix string
	Dirs   []IndexDir
	Files  []domain.Object
}

// Index lists a directory for the public HTML index. A directory without
// public content is ErrNotFound, except the root, which always renders.
func (s *Service) Index(ctx context.Context, rawPrefix string) (IndexListing, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return IndexListing{}, notFoundf("not found")
	}
	now := s.now()
	res, err := s.store.ListObjects(ctx, domain.ListQuery{
		Prefix:       prefix,
		Visibilities: []api.Visibility{api.Public},
		LiveAt:       now,
	})
	if err != nil {
		return IndexListing{}, err
	}
	if prefix != "" && len(res.Dirs) == 0 && len(res.Objects) == 0 {
		return IndexListing{}, notFoundf("not found")
	}
	out := IndexListing{Prefix: prefix, Files: res.Objects}
	for _, d := range res.Dirs {
		n, latest, err := s.store.DirStats(ctx, d, now)
		if err != nil {
			return IndexListing{}, err
		}
		out.Dirs = append(out.Dirs, IndexDir{Path: d, Count: n, UpdatedAt: latest})
	}
	return out, nil
}
