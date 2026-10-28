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
	TargetID    string
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
	// LiveAt, when non-zero, hides objects that are expired at that time.
	LiveAt time.Time
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
