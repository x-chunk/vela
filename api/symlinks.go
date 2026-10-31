package api

import (
	"fmt"
	"time"
)

// ObjectKind tells a file from a symlink.
type ObjectKind string

// Object kinds.
const (
	// KindFile is a regular object with its own content.
	KindFile ObjectKind = "file"
	// KindSymlink refers to a file by its ID and has no content of its own.
	// It survives renames and moves of the file, follows overwrites, and
	// breaks when the file is deleted.
	KindSymlink ObjectKind = "symlink"
)

// Valid reports whether k is a known kind.
func (k ObjectKind) Valid() bool { return k == KindFile || k == KindSymlink }

// ParseObjectKind parses a kind name.
func ParseObjectKind(s string) (ObjectKind, error) {
	k := ObjectKind(s)
	if !k.Valid() {
		return "", fmt.Errorf("unknown kind %q (want file or symlink)", s)
	}
	return k, nil
}

// TargetState says where the target of a symlink is.
type TargetState string

// Target states.
const (
	// TargetOK means the target is a live object.
	TargetOK TargetState = "ok"
	// TargetTrashed means the target is in the trash; restoring it repairs
	// the symlink.
	TargetTrashed TargetState = "trashed"
	// TargetGone means the target was purged or lies outside the caller's
	// key prefix; the two are deliberately indistinguishable.
	TargetGone TargetState = "gone"
)

// SymlinkTarget describes the file a symlink refers to.
type SymlinkTarget struct {
	ID string `json:"id"`
	// Path is the target's current path, or its original path while it is
	// in the trash. It is nil when the target is gone or outside the key's
	// prefix.
	Path  *string     `json:"path"`
	State TargetState `json:"state"`
	// Visibility is the target's own visibility; nil unless State is ok.
	Visibility *Visibility `json:"visibility"`
	// Expired is true when the target's expires_at has passed.
	Expired bool `json:"expired"`
}

// MaxSymlinkTree caps the objects a recursive symlink request may create.
const MaxSymlinkTree = 10000

// CreateSymlinkRequest is the body of POST /api/v1/symlinks. With Recursive
// set, Source and Path are prefixes ending with a slash. Nil fields are not
// specified: new symlinks copy them from the source (unless CopyMeta is
// false) or take server defaults; retargeted ones keep their own.
type CreateSymlinkRequest struct {
	Source    string `json:"source"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	// Overwrite retargets an existing symlink in place, keeping its ID, or
	// moves an existing file to the trash.
	Overwrite bool `json:"overwrite"`
	// SkipSymlinks leaves the symlinks under Source out of a recursive
	// request instead of dereferencing them.
	SkipSymlinks bool `json:"skip_symlinks,omitempty"`
	// SkipConflicts makes a recursive request link the rest and report the
	// destinations it left out instead of failing: paths under a file or
	// holding a directory, and existing ones unless Overwrite is set.
	SkipConflicts bool `json:"skip_conflicts,omitempty"`
	// CopyMeta defaults to true for new symlinks and false for retargeted
	// ones.
	CopyMeta    *bool        `json:"copy_meta,omitempty"`
	Visibility  *Visibility  `json:"visibility,omitempty"`
	Title       *string      `json:"title,omitempty"`
	Description *string      `json:"description,omitempty"`
	Tags        *[]string    `json:"tags,omitempty"`
	Disposition *Disposition `json:"disposition,omitempty"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
}

// SymlinkTreeResponse is the response of a recursive POST /api/v1/symlinks.
type SymlinkTreeResponse struct {
	Created  int `json:"created"`
	Replaced int `json:"replaced"`
	// Skipped lists broken symlinks under the source that were not linked,
	// at most 100.
	Skipped []string `json:"skipped"`
	// SkippedConflicts lists the destinations left out by skip_conflicts,
	// at most 100.
	SkippedConflicts []string `json:"skipped_conflicts"`
}
