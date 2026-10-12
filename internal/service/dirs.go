package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// DirPolicies is an immutable policy snapshot. Requests resolve every ancestor
// against one generation, so a concurrent edit cannot mix settings on a page.
type DirPolicies struct {
	rules       map[string]api.DirPolicy
	rootDefault api.IndexMode
}

// Effective resolves inheritance in O(path depth). Hidden only applies to the
// named directory; its crawler default applies to the entire hidden subtree.
func (v *DirPolicies) Effective(prefix string) api.EffectiveDirPolicy {
	out := api.EffectiveDirPolicy{Index: v.rootDefault, IndexSource: "config", NoIndex: api.IndexOff, NoIndexSource: "default"}
	if p, ok := v.rules[prefix]; ok {
		out.Hidden, out.Title, out.Description = p.Hidden, p.Title, p.Description
	}
	indexSet, robotsSet := false, false
	hiddenSource := ""
	for at := prefix; ; at = api.ParentPrefix(at) {
		if p, ok := v.rules[at]; ok {
			if !indexSet && p.Index != api.IndexInherit {
				out.Index, out.IndexSource, indexSet = p.Index, at, true
			}
			if !robotsSet && p.NoIndex != api.IndexInherit {
				out.NoIndex, out.NoIndexSource, robotsSet = p.NoIndex, at, true
			}
			if hiddenSource == "" && p.Hidden {
				hiddenSource = "hidden:" + at
			}
		}
		if at == "" {
			break
		}
	}
	if !robotsSet && hiddenSource != "" {
		out.NoIndex, out.NoIndexSource = api.IndexOn, hiddenSource
	}
	return out
}

// HasRules distinguishes the legacy index contract when no policies exist.
func (v *DirPolicies) HasRules() bool { return len(v.rules) != 0 }

type dirPolicyCache struct {
	mu      sync.Mutex
	version int64
	view    *DirPolicies
}

// directoryPolicies checks a persistent generation instead of data_version:
// data_version is connection-local and cannot safely use a pooled reader.
func (s *Service) directoryPolicies(ctx context.Context) (*DirPolicies, error) {
	s.dirs.mu.Lock()
	defer s.dirs.mu.Unlock()
	for {
		version, err := s.store.DirPoliciesVersion(ctx)
		if err != nil {
			return nil, err
		}
		if s.dirs.view != nil && version == s.dirs.version {
			return s.dirs.view, nil
		}
		rules, err := s.store.DirPolicies(ctx)
		if err != nil {
			return nil, err
		}
		after, err := s.store.DirPoliciesVersion(ctx)
		if err != nil {
			return nil, err
		}
		if after != version {
			continue
		}
		view := &DirPolicies{rules: make(map[string]api.DirPolicy, len(rules)), rootDefault: s.opts.RootIndex}
		for _, p := range rules {
			view.rules[p.Prefix] = p
		}
		s.dirs.view, s.dirs.version = view, version
		return view, nil
	}
}

func (s *Service) invalidateDirs() {
	s.dirs.mu.Lock()
	s.dirs.view = nil
	s.dirs.mu.Unlock()
}

func dirPolicyDTO(p api.DirPolicy) api.DirPolicy {
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Second)
	p.UpdatedAt = p.UpdatedAt.UTC().Truncate(time.Second)
	return p
}

// ShowDir returns the effective public behavior even for an absent rule.
func (s *Service) ShowDir(ctx context.Context, p Principal, rawPrefix string) (api.EffectiveDir, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return api.EffectiveDir{}, pathError(err)
	}
	if err := p.require(api.ScopeRO); err != nil {
		return api.EffectiveDir{}, err
	}
	if err := p.checkPath(prefix); err != nil {
		return api.EffectiveDir{}, err
	}
	view, err := s.directoryPolicies(ctx)
	if err != nil {
		return api.EffectiveDir{}, err
	}
	return s.describeDir(ctx, view, prefix)
}

func (s *Service) describeDir(ctx context.Context, view *DirPolicies, prefix string) (api.EffectiveDir, error) {
	out := api.EffectiveDir{Prefix: prefix, Effective: view.Effective(prefix)}
	if rule, ok := view.rules[prefix]; ok {
		dto := dirPolicyDTO(rule)
		out.Rule = &dto
	}
	// One public subtree query supplies existence and recursive visible entries.
	tree, err := s.publicTree(ctx, prefix, view)
	if err != nil {
		return out, err
	}
	out.Exists = tree.exists
	if out.Effective.Index == api.IndexOn && (prefix == "" || out.Exists) {
		u := api.PublicURL(s.opts.PublicURL, prefix)
		out.URL = &u
	}
	out.ListedInParent = prefix != "" && !out.Effective.Hidden && out.Effective.Index == api.IndexOn &&
		view.Effective(api.ParentPrefix(prefix)).Index == api.IndexOn && tree.count > 0
	return out, nil
}

// DirPolicyParams selects stored rules; listings are narrowed to a key prefix.
type DirPolicyParams struct {
	Prefix    string
	Recursive bool
	Orphaned  bool
	Limit     int
	Cursor    string
}

// ListDirPolicies returns a stable page ordered by normalized prefix.
func (s *Service) ListDirPolicies(ctx context.Context, p Principal, q DirPolicyParams) (api.DirPolicyList, error) {
	out := api.DirPolicyList{Policies: []api.DirPolicy{}}
	if err := p.require(api.ScopeRO); err != nil {
		return out, err
	}
	prefix, err := api.CleanPrefix(q.Prefix)
	if err != nil {
		return out, pathError(err)
	}
	within, err := p.scopeListing(prefix)
	if err != nil {
		return out, err
	}
	if within != "" {
		prefix = within
	}
	limit := q.Limit
	if limit == 0 {
		limit = 200
	}
	if limit < 1 || limit > 1000 {
		return out, invalidf("limit must be between 1 and 1000")
	}
	after, err := decodeCursor(q.Cursor)
	if err != nil {
		return out, err
	}
	// Prefix the cursor payload to distinguish the root rule from no cursor.
	if q.Cursor != "" {
		var ok bool
		after, ok = strings.CutPrefix(after, "dir:")
		if !ok {
			return out, invalidf("invalid directory policy cursor")
		}
	}
	view, err := s.directoryPolicies(ctx)
	if err != nil {
		return out, err
	}
	names := make([]string, 0, len(view.rules))
	for name := range view.rules {
		if !api.IsWithin(name, prefix) || (q.Cursor != "" && name <= after) {
			continue
		}
		if !q.Recursive && name != prefix && api.ParentPrefix(name) != prefix {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	now := s.now()
	for _, name := range names {
		if q.Orphaned {
			n, _, err := s.store.DirStats(ctx, name, now)
			if err != nil {
				return out, err
			}
			if n != 0 {
				continue
			}
		}
		if len(out.Policies) == limit {
			out.NextCursor = encodeCursor("dir:" + out.Policies[len(out.Policies)-1].Prefix)
			break
		}
		out.Policies = append(out.Policies, dirPolicyDTO(view.rules[name]))
	}
	return out, nil
}

// DirWriteOptions guards mutations with optional HTTP revision preconditions.
type DirWriteOptions struct {
	IfMatch     int64
	IfNoneMatch bool
}

func dirWritePrefix(p Principal, raw string) (string, error) {
	prefix, err := api.CleanPrefix(raw)
	if err != nil {
		return "", pathError(err)
	}
	if err := p.require(api.ScopeRW); err != nil {
		return "", err
	}
	if err := p.checkPath(prefix); err != nil {
		return "", err
	}
	if prefix == "" {
		if err := p.require(api.ScopeAdmin); err != nil {
			return "", err
		}
	}
	return prefix, nil
}

func defaultDirPolicy(prefix string) api.DirPolicy {
	return api.DirPolicy{Prefix: prefix, Index: api.IndexInherit, NoIndex: api.IndexInherit}
}

func validateDirPolicy(p api.DirPolicy) error {
	if !p.Index.Valid() {
		return invalidf("index must be on, off or inherit")
	}
	if !p.NoIndex.Valid() {
		return invalidf("noindex must be on, off or inherit")
	}
	if p.Prefix == "" && p.Hidden {
		return invalidf("the root directory cannot be hidden")
	}
	if err := api.ValidateTitle(p.Title); err != nil {
		return invalidf("%s", err)
	}
	if err := api.ValidateDescription(p.Description); err != nil {
		return invalidf("%s", err)
	}
	return nil
}

func checkDirPreconditions(rule api.DirPolicy, exists bool, opts DirWriteOptions) error {
	if opts.IfNoneMatch && exists {
		return newErr(ErrPreconditionFailed, nil, "directory policy already exists")
	}
	if opts.IfMatch != 0 && (!exists || rule.Revision != opts.IfMatch) {
		return newErr(ErrPreconditionFailed, nil, "directory policy revision does not match")
	}
	return nil
}

// PutDir completely replaces a policy; empty modes take inheritance defaults.
func (s *Service) PutDir(ctx context.Context, p Principal, raw string, req api.DirPolicyRequest, opts DirWriteOptions) (api.EffectiveDir, bool, error) {
	return s.writeDir(ctx, p, raw, opts, func(rule *api.DirPolicy) {
		rule.Index, rule.Hidden, rule.NoIndex, rule.Title, rule.Description = req.Index, req.Hidden, req.NoIndex, req.Title, req.Description
		if rule.Index == "" {
			rule.Index = api.IndexInherit
		}
		if rule.NoIndex == "" {
			rule.NoIndex = api.IndexInherit
		}
	})
}

// PatchDir changes selected fields, creating a default rule when necessary.
func (s *Service) PatchDir(ctx context.Context, p Principal, raw string, patch api.DirPolicyPatch, opts DirWriteOptions) (api.EffectiveDir, error) {
	out, _, err := s.writeDir(ctx, p, raw, opts, func(rule *api.DirPolicy) {
		applyDirField(&rule.Index, patch.Index, api.IndexInherit)
		applyDirField(&rule.Hidden, patch.Hidden, false)
		applyDirField(&rule.NoIndex, patch.NoIndex, api.IndexInherit)
		applyDirField(&rule.Title, patch.Title, "")
		applyDirField(&rule.Description, patch.Description, "")
	})
	return out, err
}

func applyDirField[T any](dst *T, patch api.Optional[T], def T) {
	if patch.Set {
		if patch.Null {
			*dst = def
		} else {
			*dst = patch.Value
		}
	}
}

func (s *Service) writeDir(ctx context.Context, p Principal, raw string, opts DirWriteOptions, change func(*api.DirPolicy)) (api.EffectiveDir, bool, error) {
	prefix, err := dirWritePrefix(p, raw)
	if err != nil {
		return api.EffectiveDir{}, false, err
	}
	created := false
	err = s.store.Update(ctx, func(w Writer) error {
		rule, err := w.DirPolicyByPrefix(ctx, prefix)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return err
		}
		if err := checkDirPreconditions(rule, exists, opts); err != nil {
			return err
		}
		if !exists {
			rule = defaultDirPolicy(prefix)
			rule.CreatedAt = s.now()
			created = true
		}
		change(&rule)
		if err := validateDirPolicy(rule); err != nil {
			return err
		}
		rule.Revision++
		rule.UpdatedAt = s.now()
		return w.PutDirPolicy(ctx, rule)
	})
	if err != nil {
		return api.EffectiveDir{}, false, err
	}
	s.invalidateDirs()
	out, err := s.ShowDir(ctx, p, prefix)
	return out, created, err
}

// DeleteDir removes only the explicit rule, preserving its objects and children.
func (s *Service) DeleteDir(ctx context.Context, p Principal, raw string, opts DirWriteOptions) error {
	prefix, err := dirWritePrefix(p, raw)
	if err != nil {
		return err
	}
	err = s.store.Update(ctx, func(w Writer) error {
		rule, err := w.DirPolicyByPrefix(ctx, prefix)
		if isNotFound(err) {
			if opts.IfMatch != 0 {
				return checkDirPreconditions(rule, false, opts)
			}
			return notFoundf("directory policy not found: %s", prefix)
		}
		if err != nil {
			return err
		}
		if err := checkDirPreconditions(rule, true, opts); err != nil {
			return err
		}
		return w.DeleteDirPolicy(ctx, prefix)
	})
	if err == nil {
		s.invalidateDirs()
	}
	return err
}

// publicNode summarizes the entries that can be reached through a directory.
type publicNode struct {
	prefix   string
	files    []domain.Object
	children map[string]*publicNode
	exists   bool
	count    int
	total    int
	latest   time.Time
}

func (s *Service) publicTree(ctx context.Context, prefix string, view *DirPolicies) (*publicNode, error) {
	res, err := s.store.ListObjects(ctx, domain.ListQuery{Prefix: prefix, Recursive: true, Visibilities: []api.Visibility{api.Public}, LiveAt: s.now()})
	if err != nil {
		return nil, err
	}
	root := &publicNode{prefix: prefix, children: map[string]*publicNode{}}
	for _, o := range res.Objects {
		node := root
		parent := api.ParentPrefix(o.Path)
		suffix := strings.TrimPrefix(parent, prefix)
		for _, segment := range strings.Split(strings.TrimSuffix(suffix, "/"), "/") {
			if segment == "" {
				continue
			}
			at := node.prefix + segment + "/"
			child := node.children[at]
			if child == nil {
				child = &publicNode{prefix: at, children: map[string]*publicNode{}}
				node.children[at] = child
			}
			node = child
		}
		node.files = append(node.files, o)
	}
	summarizePublic(root, view)
	return root, nil
}

func summarizePublic(node *publicNode, view *DirPolicies) {
	node.count, node.total = len(node.files), len(node.files)
	node.exists = len(node.files) > 0
	for _, o := range node.files {
		if o.UpdatedAt.After(node.latest) {
			node.latest = o.UpdatedAt
		}
	}
	for _, child := range node.children {
		summarizePublic(child, view)
		node.total += child.total
		node.exists = node.exists || child.exists
		policy := view.Effective(child.prefix)
		if policy.Hidden || policy.Index == api.IndexOff || child.count == 0 {
			continue
		}
		node.count++
		if child.latest.After(node.latest) {
			node.latest = child.latest
		}
	}
}

// RootDirPolicy exposes the startup root setting and its source without an
// object query; callers use it to explain the active configuration in logs.
func (s *Service) RootDirPolicy(ctx context.Context) (api.EffectiveDirPolicy, error) {
	view, err := s.directoryPolicies(ctx)
	if err != nil {
		return api.EffectiveDirPolicy{}, fmt.Errorf("load root directory policy: %w", err)
	}
	return view.Effective(""), nil
}
