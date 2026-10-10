package api

import "time"

// Object is the API representation of a stored file (spec section 10.2).
// Timestamps are UTC with second precision.
type Object struct {
	ID          string      `json:"id"`
	Path        string      `json:"path"`
	Name        string      `json:"name"`
	Size        int64       `json:"size"`
	SHA256      string      `json:"sha256"`
	ContentType string      `json:"content_type"`
	Disposition Disposition `json:"disposition"`
	Visibility  Visibility  `json:"visibility"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Tags        []string    `json:"tags"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	ExpiresAt   *time.Time  `json:"expires_at"`
	// Expired is true when ExpiresAt has passed but the janitor has not moved
	// the object to the trash yet. Expired objects are hidden from the public
	// side and from signed links but stay downloadable through the API.
	Expired  bool  `json:"expired"`
	Revision int64 `json:"revision"`
	// URL is the public URL, or nil for private objects, which have none.
	URL *string `json:"url"`
}

// Listing is the response of GET /api/v1/objects.
type Listing struct {
	Prefix string `json:"prefix"`
	// Dirs holds the immediate sub-directories (with a trailing slash) in
	// non-recursive mode and is always empty in recursive mode.
	Dirs       []string `json:"dirs"`
	Objects    []Object `json:"objects"`
	NextCursor string   `json:"next_cursor"`
}

// Content encodings accepted by POST /api/v1/objects.
const (
	EncodingUTF8   = "utf-8"
	EncodingBase64 = "base64"
)

// CreateObjectRequest is the body of POST /api/v1/objects, which creates a
// small object from inline content. Empty optional fields take server
// defaults.
type CreateObjectRequest struct {
	Path        string      `json:"path"`
	Content     string      `json:"content"`
	Encoding    string      `json:"encoding,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Visibility  Visibility  `json:"visibility,omitempty"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Disposition Disposition `json:"disposition,omitempty"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty"`
}

// MoveRequest is the body of POST /api/v1/move. With Recursive set, From and
// To are prefixes ending with a slash.
type MoveRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
	Recursive bool   `json:"recursive"`
}

// MoveResponse reports how many objects were moved.
type MoveResponse struct {
	Moved int `json:"moved"`
}

// DeleteResponse reports deleted objects. TrashIDs is empty for purges.
type DeleteResponse struct {
	Deleted  int      `json:"deleted"`
	TrashIDs []string `json:"trash_ids"`
}

// TrashEntry is an object in the trash. Object.Path equals OriginalPath and
// Object.URL is always nil because trashed objects are not served.
type TrashEntry struct {
	ID           string    `json:"id"`
	OriginalPath string    `json:"original_path"`
	DeletedAt    time.Time `json:"deleted_at"`
	PurgeAt      time.Time `json:"purge_at"`
	Object       Object    `json:"object"`
}

// TrashList is the response of GET /api/v1/trash.
type TrashList struct {
	Entries    []TrashEntry `json:"entries"`
	NextCursor string       `json:"next_cursor"`
}

// RestoreRequest is the body of POST /api/v1/trash/{id}/restore. An empty
// Path restores to the original location.
type RestoreRequest struct {
	Path      string `json:"path,omitempty"`
	Overwrite bool   `json:"overwrite"`
}

// PurgeResponse reports how many trash entries were permanently deleted.
type PurgeResponse struct {
	Purged int `json:"purged"`
}

// CreateLinkRequest is the body of POST /api/v1/links. Set at most one of TTL
// and ExpiresAt; with neither, the server default TTL applies.
type CreateLinkRequest struct {
	Path      string     `json:"path"`
	TTL       string     `json:"ttl,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// Link is a signed link record.
type Link struct {
	ID       string `json:"id"`
	ObjectID string `json:"object_id"`
	// Path is the object's current path. Links follow objects across moves, so
	// it can differ from the path the link was created for. It is empty when
	// the object is in the trash.
	Path string `json:"path"`
	// URL is nil when it can no longer be built because its signing key has
	// been destroyed (velad signing rotate --revoke-all).
	URL       *string    `json:"url"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	Note      string     `json:"note"`
}

// LinkList is the response of GET /api/v1/links.
type LinkList struct {
	Links []Link `json:"links"`
}

// KeyInfo describes the API key a request was authenticated with.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Scope  Scope  `json:"scope"`
	Prefix string `json:"prefix"`
}

// Limits reports server-side limits so that clients can fail early.
type Limits struct {
	MaxUploadBytes int64  `json:"max_upload_bytes"`
	MaxCreateBytes int64  `json:"max_create_bytes"`
	LinkDefaultTTL string `json:"link_default_ttl"`
	LinkMaxTTL     string `json:"link_max_ttl"`
}

// Info is the response of GET /api/v1/info.
type Info struct {
	ServerVersion string  `json:"server_version"`
	APIVersion    int     `json:"api_version"`
	PublicURL     string  `json:"public_url"`
	Key           KeyInfo `json:"key"`
	Limits        Limits  `json:"limits"`
}

// Health is the response of GET /healthz.
type Health struct {
	Status string `json:"status"`
}
