// Package domain holds the entities shared by the service layer and the
// storage implementation. It contains plain data and invariants that need no
// I/O; behavior lives in internal/service.
package domain

import (
	"errors"
	"time"

	"go.xchunk.org/vela/api"
)

// Sentinel errors returned by repositories.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate means a uniqueness constraint was violated.
	ErrDuplicate = errors.New("duplicate")
)

// Kind tells a file from a symlink.
type Kind string

// Object kinds.
const (
	// KindFile is an object with its own blob.
	KindFile Kind = "file"
	// KindSymlink is an object without a blob that refers to a file by ID.
	KindSymlink Kind = "symlink"
)

// Object is a stored file: a logical path, metadata and a reference to the
// blob holding its bytes. A symlink has no blob; it refers to a file through
// TargetID, and its stored size, digest and content type are empty.
type Object struct {
	ID   string
	Path string
	// Kind is KindFile or KindSymlink; the empty value means a file.
	Kind Kind
	// TargetID is the ID of the file a symlink refers to, "" for files.
	TargetID string
	// Target is what the read that loaded a symlink found behind TargetID;
	// nil for files. For a live target the store also fills Size, SHA256
	// and ContentType from it, so readers see the content they would get.
	Target      *Target
	BlobID      string
	Size        int64
	SHA256      string
	ContentType string
	Disposition api.Disposition
	Visibility  api.Visibility
	Title       string
	Description string
	Tags        []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   *time.Time
	Revision    int64
}

// IsSymlink reports whether o is a symlink.
func (o *Object) IsSymlink() bool { return o.Kind == KindSymlink }

// Target describes the file behind a symlink as of the read that loaded it.
type Target struct {
	ID string
	// State is ObjectLive, ObjectTrashed or ObjectGone.
	State ObjectState
	// Path is the current path, or the original path while the target is in
	// the trash; "" when it is gone.
	Path string
	// The fields below are set only for a live target.
	BlobID     string
	Visibility api.Visibility
	ExpiresAt  *time.Time
}

// Live reports whether the target is in the objects table.
func (t *Target) Live() bool { return t != nil && t.State == ObjectLive }

// Broken reports whether o is a symlink whose target is not live.
func (o *Object) Broken() bool { return o.IsSymlink() && !o.Target.Live() }

// EffectiveVisibility is the visibility that governs anonymous access: the
// object's own for a file, and for a symlink the stricter of its own and its
// target's. A broken symlink is private. Using the stricter of the two keeps a
// public symlink from publishing a private file.
func (o *Object) EffectiveVisibility() api.Visibility {
	if !o.IsSymlink() {
		return o.Visibility
	}
	if !o.Target.Live() {
		return api.Private
	}
	return stricter(o.Visibility, o.Target.Visibility)
}

// visibilityRank orders visibilities from the most to the least open.
var visibilityRank = map[api.Visibility]int{api.Public: 0, api.Unlisted: 1, api.Private: 2}

func stricter(a, b api.Visibility) api.Visibility {
	if visibilityRank[b] > visibilityRank[a] {
		return b
	}
	return a
}

// LiveAt reports whether o can deliver content at now: it has not expired
// and, for a symlink, its target is live and has not expired either.
func (o *Object) LiveAt(now time.Time) bool {
	if o.ExpiredAt(now) {
		return false
	}
	if !o.IsSymlink() {
		return true
	}
	return o.Target.Live() && (o.Target.ExpiresAt == nil || !now.After(*o.Target.ExpiresAt))
}

// ExpiredAt reports whether the object's own TTL has passed at now. An object
// whose expires_at equals now is still alive; it expires strictly after.
func (o *Object) ExpiredAt(now time.Time) bool {
	return o.ExpiresAt != nil && now.After(*o.ExpiresAt)
}

// TrashEntry is a deleted object kept for restore. Object.Path is the
// original path; Object.ID is reused when the entry is restored, which is what
// makes signed links work again after a restore.
type TrashEntry struct {
	ID        string
	Object    Object
	DeletedAt time.Time
	DeletedBy string
	// Seq orders entries by deletion, strictly, even within one millisecond;
	// the store assigns it.
	Seq int64
}

// Link is a signed link record.
type Link struct {
	ID           string
	ObjectID     string
	SigningKeyID string
	Filename     string
	Note         string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	CreatedBy    string
}

// LinkRecord is a link joined with the data needed to verify and display it.
type LinkRecord struct {
	Link
	// KeySecret is the signing key, or nil if the key has been destroyed.
	KeySecret []byte
	// ObjectPath is the current path, or the original path if in the trash.
	ObjectPath string
	// ObjectState tells where the object is.
	ObjectState ObjectState
}

// ObjectState says where a link's object currently lives.
type ObjectState int

const (
	// ObjectGone means the object was purged.
	ObjectGone ObjectState = iota
	// ObjectLive means the object is in the objects table.
	ObjectLive
	// ObjectTrashed means the object is in the trash.
	ObjectTrashed
)

// SigningKey is an HMAC key for signed links. A retired key no longer signs
// new links but still verifies the ones it signed.
type SigningKey struct {
	ID        string
	Secret    []byte
	CreatedAt time.Time
	RetiredAt *time.Time
}

// APIKey is an API key record. Only the SHA-256 of the secret is stored.
type APIKey struct {
	ID         string
	Name       string
	SecretHash []byte
	Scope      api.Scope
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// WebSession is a browser session of the web sign-in. It stores the SHA-256
// of the random session ID, never the ID itself, and refers to the API key it
// was created with; the key decides what the session can see.
type WebSession struct {
	IDHash     []byte
	KeyID      string
	CreatedAt  time.Time
	LastSeenAt time.Time
	// ExpiresAt is the absolute end of the session; the idle timeout is
	// computed from LastSeenAt.
	ExpiresAt time.Time
}

// ListQuery selects objects for listings and the HTML index.
type ListQuery struct {
	// Prefix is the directory being listed ("" for root, else ends in "/").
	// Directory entries are computed relative to it.
	Prefix string
	// Within additionally restricts results to a key's prefix. Either Prefix
	// contains Within or the other way round; the caller ensures this.
	Within string
	// Recursive returns every object below Prefix instead of immediate
	// children plus sub-directories.
	Recursive bool
	// Visibilities filters by visibility; empty means all.
	Visibilities []api.Visibility
	// Visibilities and LiveAt apply to symlinks through their effective
	// visibility and their target (see Object.EffectiveVisibility, LiveAt).
	//
	// LiveAt, when non-zero, hides objects that are expired at that time.
	LiveAt time.Time
	// Kind, when set, selects only objects of that kind.
	Kind Kind
	// Broken selects only symlinks whose target is not live.
	Broken bool
	// TargetID selects only symlinks that refer to this object.
	TargetID string
	// Tags, when non-empty, selects objects whose own tags match it.
	Tags api.TagFilter
	// TargetWithin, when non-empty, hides symlinks that are effectively
	// private and whose target lies outside this prefix. Signed-in web views
	// use it so that a symlink inside the key's prefix cannot reveal a
	// private file outside it.
	TargetWithin string
	// After is the exclusive cursor: the key of the last entry already seen
	// (an object path, or a directory ending in "/").
	After string
	// Limit caps the number of entries (objects + dirs); 0 means no limit.
	Limit int
}

// ListResult is one page of a listing, in byte-wise path order.
type ListResult struct {
	Dirs    []string
	Objects []Object
	// More is true when entries remain after this page.
	More bool
	// Last is the key of the last returned entry, for the next cursor.
	Last string
}

// TrashQuery selects trash entries, newest first.
type TrashQuery struct {
	// Prefix restricts entries to original paths under a directory.
	Prefix string
	// Within restricts entries to a key's prefix.
	Within string
	// Path selects entries with exactly this original path.
	Path string
	// Tags, when non-empty, selects entries whose object's tags match it.
	Tags api.TagFilter
	// BeforeSeq is the exclusive cursor: entries with a smaller Seq follow.
	// Zero means from the newest.
	BeforeSeq int64
	// Limit caps the result size; 0 means no limit.
	Limit int
}

// LinkQuery selects links.
type LinkQuery struct {
	// ObjectID restricts links to one object.
	ObjectID string
	// Within restricts links to objects under a key's prefix.
	Within         string
	IncludeExpired bool
	IncludeRevoked bool
	Now            time.Time
}
