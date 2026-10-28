package service

import (
	"context"
	"strings"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// Signed-in public views (docs/SPEC-web.md, "Prefix semantics"). A web
// session sees the anonymous view plus the full view of its key's prefix K:
//
//   - inside K every live object is listed and served whatever its
//     visibility, and directory policies are ignored;
//   - a strict ancestor of K is listed as for anonymous visitors, plus one
//     entrance: the child on the way to K, even if it is hidden or its parent
//     index is disabled. No other hidden sibling appears;
//   - everything else is exactly anonymous, status codes included.
//
// Prefixes match on whole segments ("test/" never covers "test2/") because
// K always ends in "/". The entrance exists only while K holds a live
// object; with an empty K the ancestors are plain anonymous pages.

// IndexFor lists a directory for a public index page as viewer sees it. A
// nil viewer is an anonymous visitor, exactly as Index.
func (s *Service) IndexFor(ctx context.Context, rawPrefix string, viewer *Principal) (IndexListing, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return IndexListing{}, notFoundf("not found")
	}
	if viewer != nil && (api.IsWithin(prefix, viewer.Prefix) || api.IsWithin(viewer.Prefix, prefix)) {
		return s.sessionIndex(ctx, prefix, viewer.Prefix)
	}
	listing, view, err := s.publicIndex(ctx, prefix)
	if err != nil || viewer == nil {
		return listing, err
	}
	// The page is anonymous; only navigation toward the prefix may differ,
	// because the ancestors of the prefix open for this session.
	live, err := s.sessionHasObjects(ctx, viewer.Prefix, viewer.Prefix, s.now())
	if err != nil {
		return IndexListing{}, err
	}
	listing.Policies = sessionPolicies{DirPolicies: view, prefix: viewer.Prefix, reachable: live}
	return listing, nil
}

// ResolvePublicFor resolves a direct URL as viewer sees it. A nil viewer is
// an anonymous visitor, exactly as ResolvePublic. Inside the key's prefix
// every live object is served; private objects elsewhere remain 404. A
// symlink inside the prefix is served only if its target is effectively
// non-private or also inside the prefix, so a symlink cannot open a private
// file elsewhere to the session.
func (s *Service) ResolvePublicFor(ctx context.Context, rawPath string, viewer *Principal) (PublicResult, error) {
	if viewer == nil {
		return s.ResolvePublic(ctx, rawPath)
	}
	path, err := api.CleanObjectPath(rawPath)
	if err != nil {
		return PublicResult{}, notFoundf("not found")
	}
	k, dir := viewer.Prefix, path+"/"
	inside := api.IsWithin(path, k)
	if !inside && !api.IsWithin(k, dir) {
		return s.ResolvePublic(ctx, path)
	}
	now := s.now()
	o, err := s.store.ObjectByPath(ctx, path)
	switch {
	case err == nil:
		// Inside the prefix every live object is served; a file beside the
		// way to it follows the anonymous rules.
		if !o.LiveAt(now) || (!inside && o.EffectiveVisibility() == api.Private) || !sessionMayFollow(o, k) {
			return PublicResult{}, notFoundf("not found")
		}
		return s.publicContent(ctx, o)
	case !isNotFound(err):
		return PublicResult{}, err
	}
	exists, err := s.sessionPageExists(ctx, dir, k, now)
	if err != nil {
		return PublicResult{}, err
	}
	if exists {
		return PublicResult{RedirectDir: dir}, nil
	}
	return PublicResult{}, notFoundf("not found")
}

// sessionPageExists reports whether the index page of a non-root directory
// inside or above the prefix k renders for the session, without building the
// tree. It must agree with sessionTree.
func (s *Service) sessionPageExists(ctx context.Context, dir, k string, now time.Time) (bool, error) {
	if api.IsWithin(dir, k) {
		return s.sessionHasObjects(ctx, dir, k, now)
	}
	live, err := s.sessionHasObjects(ctx, k, k, now)
	if err != nil || live {
		return live, err
	}
	return s.publicDirExists(ctx, dir, now)
}

// sessionHasObjects reports whether the session with prefix k sees a live
// object below prefix, which lies inside k. It must agree with the listing
// sessionTree makes, symlink rules included.
func (s *Service) sessionHasObjects(ctx context.Context, prefix, k string, now time.Time) (bool, error) {
	res, err := s.store.ListObjects(ctx, domain.ListQuery{Prefix: prefix, Recursive: true, LiveAt: now, TargetWithin: k, Limit: 1})
	return len(res.Objects) > 0, err
}

// sessionMayFollow reports whether a session with prefix k may be served o:
// a file always, a symlink only when its target is effectively non-private
// or lies inside k.
func sessionMayFollow(o domain.Object, k string) bool {
	return !o.IsSymlink() || o.EffectiveVisibility() != api.Private || targetVisibleTo(o, k)
}

// sessionPolicies makes the pages a session can open linkable in breadcrumbs
// and parent links, even where anonymous visitors find a disabled index:
// everything inside the prefix, and its ancestors while the prefix holds
// content. All other policy fields are unchanged.
type sessionPolicies struct {
	*DirPolicies
	prefix    string
	reachable bool
}

// Effective resolves a policy for navigation on a signed-in page.
func (v sessionPolicies) Effective(prefix string) api.EffectiveDirPolicy {
	out := v.DirPolicies.Effective(prefix)
	if api.IsWithin(prefix, v.prefix) || (v.reachable && api.IsWithin(v.prefix, prefix)) {
		out.Index = api.IndexOn
	}
	return out
}

func (s *Service) sessionIndex(ctx context.Context, prefix, k string) (IndexListing, error) {
	tree, view, err := s.sessionTree(ctx, prefix, k)
	if err != nil {
		return IndexListing{}, err
	}
	if !tree.renders {
		return IndexListing{}, notFoundf("not found")
	}
	out := IndexListing{Prefix: prefix, Files: tree.files, Policies: sessionPolicies{DirPolicies: view, prefix: k, reachable: tree.reachable}}
	for _, child := range tree.dirs {
		policy := view.Effective(child.prefix)
		count := child.count
		if !view.HasRules() {
			count = child.total
		}
		out.Dirs = append(out.Dirs, IndexDir{
			Path: child.prefix, Count: count, UpdatedAt: child.latest, Title: policy.Title, Description: policy.Description,
			Hidden: policy.Hidden, IndexOff: policy.Index == api.IndexOff,
		})
	}
	return out, nil
}

// sessionNode is a directory as one session sees it.
type sessionNode struct {
	prefix   string
	direct   []domain.Object
	children map[string]*sessionNode
	// public is set when a live public object lies below, which decides
	// whether the page exists for anonymous visitors.
	public bool
	// The page as the session sees it: listed files and child rows, their
	// number, the legacy file count and the latest visible change.
	files  []domain.Object
	dirs   []*sessionNode
	count  int
	total  int
	latest time.Time
}

// sessionTreeResult is the requested page plus what navigation needs.
type sessionTreeResult struct {
	*sessionNode
	renders bool
	// reachable is set when the prefix holds a live object, which opens its
	// ancestors to the session.
	reachable bool
}

// sessionTree loads every object the session can see below prefix, which lies
// inside or above k: inside k all live objects, above k the public ones plus
// all of k. Like publicTree it validates the policy generation after the
// complete listing, so a concurrent move cannot pair new paths with old rules.
func (s *Service) sessionTree(ctx context.Context, prefix, k string) (sessionTreeResult, *DirPolicies, error) {
	for {
		view, err := s.directoryPolicies(ctx)
		if err != nil {
			return sessionTreeResult{}, nil, err
		}
		now := s.now()
		within := api.IsWithin(prefix, k)
		scope := k
		if within {
			scope = prefix
		}
		own, err := s.store.ListObjects(ctx, domain.ListQuery{Prefix: scope, Recursive: true, LiveAt: now, TargetWithin: k})
		if err != nil {
			return sessionTreeResult{}, nil, err
		}
		objects := own.Objects
		if !within {
			public, err := s.store.ListObjects(ctx, domain.ListQuery{Prefix: prefix, Recursive: true, Visibilities: []api.Visibility{api.Public}, LiveAt: now})
			if err != nil {
				return sessionTreeResult{}, nil, err
			}
			// Public objects inside k are already part of its full listing.
			for _, o := range public.Objects {
				if !api.IsWithin(o.Path, k) {
					objects = append(objects, o)
				}
			}
		}
		version, err := s.store.DirPoliciesVersion(ctx)
		if err != nil {
			return sessionTreeResult{}, nil, err
		}
		if version != view.version {
			continue
		}
		root := buildSessionTree(prefix, objects)
		reachable := len(own.Objects) > 0
		summarizeSession(root, view, k, reachable)
		out := sessionTreeResult{sessionNode: root, reachable: reachable}
		if within {
			out.renders = prefix == "" || root.count > 0
		} else {
			out.renders = reachable || (view.Effective(prefix).Index == api.IndexOn && (prefix == "" || root.public))
		}
		return out, view, nil
	}
}

func buildSessionTree(prefix string, objects []domain.Object) *sessionNode {
	root := &sessionNode{prefix: prefix, children: map[string]*sessionNode{}}
	for _, o := range objects {
		node := root
		public := o.EffectiveVisibility() == api.Public
		node.public = node.public || public
		suffix := strings.TrimPrefix(api.ParentPrefix(o.Path), prefix)
		for _, segment := range strings.Split(strings.TrimSuffix(suffix, "/"), "/") {
			if segment == "" {
				continue
			}
			at := node.prefix + segment + "/"
			child := node.children[at]
			if child == nil {
				child = &sessionNode{prefix: at, children: map[string]*sessionNode{}}
				node.children[at] = child
			}
			node = child
			node.public = node.public || public
		}
		node.direct = append(node.direct, o)
	}
	return root
}

// summarizeSession computes every page of the tree for a session with prefix
// k. Subtrees outside k contain only public objects and follow the rules of
// summarizePublic exactly, so their rows match what anonymous visitors see.
func summarizeSession(node *sessionNode, view *DirPolicies, k string, reachable bool) {
	full := api.IsWithin(node.prefix, k)
	above := !full && api.IsWithin(k, node.prefix)
	// Above the prefix the anonymous listing appears only where anonymous
	// visitors could open the index; the entrance appears regardless.
	enabled := !above || view.Effective(node.prefix).Index == api.IndexOn
	entrance := ""
	if above {
		next, _, _ := strings.Cut(strings.TrimPrefix(k, node.prefix), "/")
		entrance = node.prefix + next + "/"
	}
	if enabled {
		node.files = node.direct
	}
	node.count, node.total = len(node.files), len(node.files)
	for _, o := range node.files {
		if o.UpdatedAt.After(node.latest) {
			node.latest = o.UpdatedAt
		}
	}
	for _, child := range node.children {
		summarizeSession(child, view, k, reachable)
		var listed bool
		switch {
		case full:
			listed = child.count > 0
		case above && child.prefix == entrance && reachable:
			listed = true
		case !enabled:
			listed = false
		default:
			policy := view.Effective(child.prefix)
			listed = !policy.Hidden && policy.Index == api.IndexOn && child.count > 0
		}
		// The legacy count of an anonymous row covers the whole subtree;
		// signed-in rows count what their page shows.
		if listed || (!full && !above) {
			node.total += child.total
		}
		if !listed {
			continue
		}
		node.dirs = append(node.dirs, child)
		node.count++
		if child.latest.After(node.latest) {
			node.latest = child.latest
		}
	}
}
