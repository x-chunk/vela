package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/keys"
)

var keyNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CreatedKey is the result of CreateKey. Full is the only copy of the secret.
type CreatedKey struct {
	Key  domain.APIKey
	Full string
}

// CreateKey creates an API key. Keys are managed only through the velad
// command line inside the container, never over HTTP, so a leaked API key
// cannot mint more keys.
func (s *Service) CreateKey(ctx context.Context, name string, scope api.Scope, prefix string) (CreatedKey, error) {
	if !keyNameRE.MatchString(name) {
		return CreatedKey{}, invalidf("key name %q must be 1-64 characters of letters, digits, '.', '_' or '-'", name)
	}
	if !scope.Valid() {
		return CreatedKey{}, invalidf("invalid scope %q (want ro, rw or admin)", scope)
	}
	cleanPrefix, err := api.CleanPrefix(prefix)
	if err != nil {
		return CreatedKey{}, pathError(err)
	}
	now := s.now()
	// Key IDs have 50 random bits; a collision is unlikely but cheap to rule
	// out, and the transaction makes check-and-insert atomic.
	for attempt := 0; attempt < 5; attempt++ {
		id, err := keys.NewID()
		if err != nil {
			return CreatedKey{}, err
		}
		gen, err := keys.Generate(id)
		if err != nil {
			return CreatedKey{}, err
		}
		k := domain.APIKey{
			ID:         id,
			Name:       name,
			SecretHash: gen.SecretHash,
			Scope:      scope,
			Prefix:     cleanPrefix,
			CreatedAt:  now,
		}
		err = s.store.Update(ctx, func(w Writer) error {
			if _, err := w.APIKeyByID(ctx, id); err == nil {
				return errKeyIDTaken
			} else if !isNotFound(err) {
				return err
			}
			if err := w.InsertAPIKey(ctx, k); errors.Is(err, domain.ErrDuplicate) {
				return newErr(ErrAlreadyExists, nil, "an active key named %q already exists", name)
			} else if err != nil {
				return err
			}
			return nil
		})
		if errors.Is(err, errKeyIDTaken) {
			continue
		}
		if err != nil {
			return CreatedKey{}, err
		}
		return CreatedKey{Key: k, Full: gen.Full}, nil
	}
	return CreatedKey{}, fmt.Errorf("could not allocate a unique key id")
}

var errKeyIDTaken = errors.New("key id taken")

// unknownKeyHash stands in for the stored hash of a key ID that does not exist.
var unknownKeyHash [sha256.Size]byte

// ListKeys returns all API keys, including revoked ones.
func (s *Service) ListKeys(ctx context.Context) ([]domain.APIKey, error) {
	return s.store.ListAPIKeys(ctx)
}

// RevokeKey revokes an API key; requests with it fail immediately.
func (s *Service) RevokeKey(ctx context.Context, id string) error {
	err := s.store.Update(ctx, func(w Writer) error { return w.RevokeAPIKey(ctx, id, s.now()) })
	if isNotFound(err) {
		return notFoundf("no key with id %q", id)
	}
	return err
}

// Authenticate resolves a bearer token to a principal. Every failure is the
// same ErrUnauthorized so that a caller cannot learn whether a key ID exists,
// was revoked or had the wrong secret.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	unauthorized := newErr(ErrUnauthorized, nil, "invalid or revoked API key")
	id, hash, err := keys.Parse(token)
	if err != nil {
		return Principal{}, unauthorized
	}
	k, err := s.store.APIKeyByID(ctx, id)
	if isNotFound(err) {
		// Compare anyway, so an unknown key ID costs the same as a wrong
		// secret and the response time does not reveal which IDs exist.
		_ = keys.Verify(hash, unknownKeyHash[:])
		return Principal{}, unauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	if !keys.Verify(hash, k.SecretHash) || k.RevokedAt != nil {
		return Principal{}, unauthorized
	}
	now := s.now()
	if s.touch.due(k.ID, now) {
		if err := s.store.Update(ctx, func(w Writer) error { return w.TouchAPIKey(ctx, k.ID, now) }); err != nil {
			s.log.Warn("could not record key usage", "key_id", k.ID, "error", err)
		}
	}
	return Principal{KeyID: k.ID, Name: k.Name, Scope: k.Scope, Prefix: k.Prefix}, nil
}
