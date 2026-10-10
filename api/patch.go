package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Optional is one field of a JSON Merge Patch (RFC 7396), which has three
// states: absent (leave unchanged), null (reset to the default) and a value.
type Optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// Some returns an Optional holding v.
func Some[T any](v T) Optional[T] { return Optional[T]{Set: true, Value: v} }

// Null returns an Optional that resets the field.
func Null[T any]() Optional[T] { return Optional[T]{Set: true, Null: true} }

// HasValue reports whether the field is present and not null.
func (o Optional[T]) HasValue() bool { return o.Set && !o.Null }

func (o *Optional[T]) unmarshal(raw json.RawMessage) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(raw, &o.Value)
}

func (o Optional[T]) marshal() any {
	if o.Null {
		return nil
	}
	return o.Value
}

// ObjectPatch is the body of PATCH /api/v1/objects/{path}. Only metadata can
// be patched; the path changes through POST /api/v1/move.
type ObjectPatch struct {
	Visibility  Optional[Visibility]
	Title       Optional[string]
	Description Optional[string]
	Tags        Optional[[]string]
	ContentType Optional[string]
	Disposition Optional[Disposition]
	ExpiresAt   Optional[time.Time]
}

// patchFields maps JSON names to fields. It is the single list of patchable
// fields: unmarshaling rejects everything else.
func (p *ObjectPatch) patchFields() map[string]interface {
	unmarshal(json.RawMessage) error
} {
	return map[string]interface {
		unmarshal(json.RawMessage) error
	}{
		"visibility":   &p.Visibility,
		"title":        &p.Title,
		"description":  &p.Description,
		"tags":         &p.Tags,
		"content_type": &p.ContentType,
		"disposition":  &p.Disposition,
		"expires_at":   &p.ExpiresAt,
	}
}

// UnmarshalJSON decodes a merge patch document. Unknown members are rejected
// rather than ignored so that a typo cannot silently turn into a no-op.
func (p *ObjectPatch) UnmarshalJSON(b []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("patch must be a JSON object: %w", err)
	}
	if doc == nil {
		return fmt.Errorf("patch must be a JSON object")
	}
	*p = ObjectPatch{}
	fields := p.patchFields()
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, ok := fields[k]
		if !ok {
			if k == "path" {
				return fmt.Errorf("the path cannot be changed with PATCH; use POST %s", RouteMove)
			}
			names := make([]string, 0, len(fields))
			for n := range fields {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf("unknown field %q (patchable: %s)", k, strings.Join(names, ", "))
		}
		if err := f.unmarshal(doc[k]); err != nil {
			return fmt.Errorf("field %q: %w", k, err)
		}
	}
	return nil
}

// MarshalJSON encodes only the fields that are set.
func (p ObjectPatch) MarshalJSON() ([]byte, error) {
	doc := map[string]any{}
	add := func(name string, set bool, v any) {
		if set {
			doc[name] = v
		}
	}
	add("visibility", p.Visibility.Set, p.Visibility.marshal())
	add("title", p.Title.Set, p.Title.marshal())
	add("description", p.Description.Set, p.Description.marshal())
	add("tags", p.Tags.Set, p.Tags.marshal())
	add("content_type", p.ContentType.Set, p.ContentType.marshal())
	add("disposition", p.Disposition.Set, p.Disposition.marshal())
	add("expires_at", p.ExpiresAt.Set, p.ExpiresAt.marshal())
	return json.Marshal(doc)
}

// IsEmpty reports whether the patch changes nothing.
func (p ObjectPatch) IsEmpty() bool {
	return !p.Visibility.Set && !p.Title.Set && !p.Description.Set && !p.Tags.Set &&
		!p.ContentType.Set && !p.Disposition.Set && !p.ExpiresAt.Set
}
