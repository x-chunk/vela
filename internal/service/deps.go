package service

import (
	"context"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/domain"
)

// Reader is the read side of the repository.
type Reader interface {
	DirPoliciesVersion(ctx context.Context) (int64, error)
	DirPolicies(ctx context.Context) ([]api.DirPolicy, error)
	DirPolicyByPrefix(ctx context.Context, prefix string) (api.DirPolicy, error)
	ObjectByPath(ctx context.Context, path string) (domain.Object, error)
	ObjectByID(ctx context.Context, id string) (domain.Object, error)
	FirstExisting(ctx context.Context, paths []string) (string, error)
	FirstUnder(ctx context.Context, prefix string) (string, error)
	ObjectsUnder(ctx context.Context, prefix string) ([]domain.Object, error)
	ListObjects(ctx context.Context, q domain.ListQuery) (domain.ListResult, error)
	HasPublicObjects(ctx context.Context, prefix string, now time.Time) (bool, error)
	DirStats(ctx context.Context, prefix string, now time.Time) (int, time.Time, error)
	ExpiredObjects(ctx context.Context, now time.Time, limit int) ([]domain.Object, error)
	BlobReferenced(ctx context.Context, blobID string) (bool, error)
	CountSymlinksTo(ctx context.Context, targetIDs []string, within string) (int, error)

	TrashByID(ctx context.Context, id string) (domain.TrashEntry, error)
	ListTrash(ctx context.Context, q domain.TrashQuery) ([]domain.TrashEntry, error)
	TrashDeletedBefore(ctx context.Context, cutoff time.Time, within string, limit int) ([]domain.TrashEntry, error)

	LinkByID(ctx context.Context, id string) (domain.LinkRecord, error)
	ListLinks(ctx context.Context, q domain.LinkQuery) ([]domain.LinkRecord, error)
	ActiveSigningKey(ctx context.Context) (domain.SigningKey, error)
	SigningKeys(ctx context.Context) ([]domain.SigningKey, error)

	APIKeyByID(ctx context.Context, id string) (domain.APIKey, error)
	ListAPIKeys(ctx context.Context) ([]domain.APIKey, error)

	WebSessionByHash(ctx context.Context, idHash []byte) (domain.WebSession, error)

	DueBlobDeletions(ctx context.Context, now time.Time, limit int) ([]string, error)
	LockHeld(ctx context.Context, name string, now time.Time) (bool, error)
}

// Writer is a write transaction; it can also read its own uncommitted state.
type Writer interface {
	Reader

	PutDirPolicy(ctx context.Context, p api.DirPolicy) error
	DeleteDirPolicy(ctx context.Context, prefix string) error

	InsertObject(ctx context.Context, o domain.Object) error
	UpdateObject(ctx context.Context, o domain.Object) error
	DeleteObject(ctx context.Context, id string) error

	InsertTrash(ctx context.Context, e domain.TrashEntry) error
	DeleteTrash(ctx context.Context, id string) error

	InsertLink(ctx context.Context, l domain.Link) error
	RevokeLink(ctx context.Context, id string, at time.Time) error
	RevokeLinksBySigningKey(ctx context.Context, keyID string, at time.Time) (int, error)
	DeleteLinksByObject(ctx context.Context, objectID string) (int, error)
	DeleteLinksBefore(ctx context.Context, cutoff time.Time) (int, error)

	InsertSigningKey(ctx context.Context, k domain.SigningKey) error
	RetireSigningKeys(ctx context.Context, at time.Time) ([]string, error)
	DeleteSigningKey(ctx context.Context, id string) error
	DeleteUnusedRetiredSigningKeys(ctx context.Context, now time.Time) (int, error)

	InsertAPIKey(ctx context.Context, k domain.APIKey) error
	RevokeAPIKey(ctx context.Context, id string, at time.Time) error
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	InsertWebSession(ctx context.Context, s domain.WebSession) error
	TouchWebSession(ctx context.Context, idHash []byte, at time.Time) error
	DeleteWebSession(ctx context.Context, idHash []byte) error
	DeleteWebSessions(ctx context.Context) (int, error)
	DeleteStaleWebSessions(ctx context.Context, now, idleCutoff time.Time) (int, error)

	EnqueueBlobDeletion(ctx context.Context, blobID string, notBefore time.Time) (bool, error)
	DeleteBlobDeletion(ctx context.Context, blobID string) error

	AcquireLock(ctx context.Context, name, owner string, now, until time.Time) (bool, error)
	ReleaseLock(ctx context.Context, name, owner string) error
}

// Store is the repository: reads outside transactions plus write
// transactions. Update commits when fn returns nil and rolls back otherwise.
type Store interface {
	Reader
	Update(ctx context.Context, fn func(Writer) error) error
	Ping(ctx context.Context) error
	// Checkpoint makes every committed transaction durable or fails; see
	// gc.go for why blob deletion depends on it.
	Checkpoint(ctx context.Context) error
}

// Blobs combines persistence with backend maintenance and local delivery paths.
type Blobs interface {
	content.Store
	RelPath(id string) string
	CleanTmp(cutoff time.Time) (int, error)
}
