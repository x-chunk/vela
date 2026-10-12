package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// IndexMode selects an explicit directory setting or inheritance.
type IndexMode string

// Directory policy modes are shared by index and crawler settings.
const (
	IndexOn      IndexMode = "on"
	IndexOff     IndexMode = "off"
	IndexInherit IndexMode = "inherit"
)

// Valid reports whether m is a directory policy mode.
func (m IndexMode) Valid() bool { return m == IndexOn || m == IndexOff || m == IndexInherit }

// DirPolicy is an explicit rule, independent of the objects below Prefix.
type DirPolicy struct {
	Prefix      string    `json:"prefix"`
	Index       IndexMode `json:"index"`
	Hidden      bool      `json:"hidden"`
	NoIndex     IndexMode `json:"noindex"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Revision    int64     `json:"revision"`
}

// DirPolicyRequest fully replaces a rule; omitted fields take defaults.
type DirPolicyRequest struct {
	Index       IndexMode `json:"index,omitempty"`
	Hidden      bool      `json:"hidden"`
	NoIndex     IndexMode `json:"noindex,omitempty"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
}

// EffectiveDirPolicy records resolved settings and their sources.
type EffectiveDirPolicy struct {
	Index         IndexMode `json:"index"`
	IndexSource   string    `json:"index_source"`
	Hidden        bool      `json:"hidden"`
	NoIndex       IndexMode `json:"noindex"`
	NoIndexSource string    `json:"noindex_source"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
}

// EffectiveDir describes what an anonymous visitor can see at a prefix.
type EffectiveDir struct {
	Prefix         string             `json:"prefix"`
	Rule           *DirPolicy         `json:"rule"`
	Effective      EffectiveDirPolicy `json:"effective"`
	Exists         bool               `json:"exists"`
	URL            *string            `json:"url"`
	ListedInParent bool               `json:"listed_in_parent"`
}

// DirPolicyList is a page of explicitly stored rules.
type DirPolicyList struct {
	Policies   []DirPolicy `json:"policies"`
	NextCursor string      `json:"next_cursor"`
}

// DirPolicyPatch is a JSON Merge Patch; null resets a setting to its default.
type DirPolicyPatch struct {
	Index       Optional[IndexMode]
	Hidden      Optional[bool]
	NoIndex     Optional[IndexMode]
	Title       Optional[string]
	Description Optional[string]
}

// UnmarshalJSON rejects unknown fields and non-object patches.
func (p *DirPolicyPatch) UnmarshalJSON(b []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("directory patch must be a JSON object: %w", err)
	}
	if doc == nil {
		return fmt.Errorf("directory patch must be a JSON object")
	}
	*p = DirPolicyPatch{}
	fields := map[string]interface{ unmarshal(json.RawMessage) error }{"index": &p.Index, "hidden": &p.Hidden, "noindex": &p.NoIndex, "title": &p.Title, "description": &p.Description}
	names := make([]string, 0, len(doc))
	for name := range doc {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, ok := fields[name]
		if !ok {
			return fmt.Errorf("unknown directory policy field %q", name)
		}
		if err := f.unmarshal(doc[name]); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
	}
	return nil
}

// MarshalJSON includes only fields present in the patch.
func (p DirPolicyPatch) MarshalJSON() ([]byte, error) {
	doc := map[string]any{}
	if p.Index.Set {
		doc["index"] = p.Index.marshal()
	}
	if p.Hidden.Set {
		doc["hidden"] = p.Hidden.marshal()
	}
	if p.NoIndex.Set {
		doc["noindex"] = p.NoIndex.marshal()
	}
	if p.Title.Set {
		doc["title"] = p.Title.marshal()
	}
	if p.Description.Set {
		doc["description"] = p.Description.marshal()
	}
	return json.Marshal(doc)
}

// DirRoute returns the route of a normalized directory prefix, including root.
func DirRoute(prefix string) string { return RouteDirs + "/" + EscapePath(prefix) }
