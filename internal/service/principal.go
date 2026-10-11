package service

import (
	"go.xchunk.org/vela/api"
)

// Principal is the authenticated API key on whose behalf an operation runs.
type Principal struct {
	KeyID  string
	Name   string
	Scope  api.Scope
	Prefix string // "" or a directory prefix ending in "/"
}

func (p Principal) require(need api.Scope) error {
	if !p.Scope.Allows(need) {
		return newErr(ErrForbidden, map[string]any{"required_scope": string(need)},
			"this operation requires the %q scope; key %q has %q", need, p.Name, p.Scope)
	}
	return nil
}

// checkPath enforces the key prefix for an object path or a directory
// prefix: it must lie inside the key's prefix.
func (p Principal) checkPath(path string) error {
	if !api.IsWithin(path, p.Prefix) {
		return newErr(ErrForbidden, map[string]any{"key_prefix": p.Prefix},
			"%q is outside the key's prefix %q", path, p.Prefix)
	}
	return nil
}

// scopeListing resolves a listing request for a directory against the key
// prefix. A directory inside the prefix is listed as is. A directory that
// contains the prefix (for example the root for a key limited to "docs/") is
// listed but narrowed to the prefix, so the key sees the path to its own
// subtree and nothing else. Anything else is forbidden.
func (p Principal) scopeListing(prefix string) (within string, err error) {
	switch {
	case api.IsWithin(prefix, p.Prefix):
		return "", nil
	case api.IsWithin(p.Prefix, prefix):
		return p.Prefix, nil
	default:
		return "", p.checkPath(prefix)
	}
}
