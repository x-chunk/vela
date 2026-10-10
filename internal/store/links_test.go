package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestLinksAndSigningKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	live := newObject("edu/live.pdf", api.Private)
	trashed := newObject("edu/trashed.pdf", api.Private)
	insert(t, s, live)
	key := domain.SigningKey{ID: gen.New(), Secret: []byte("0123456789abcdef0123456789abcdef"), CreatedAt: t0}
	mk := func(objID string, exp time.Duration) domain.Link {
		return domain.Link{ID: gen.New(), ObjectID: objID, SigningKeyID: key.ID, Filename: "f.pdf",
			CreatedAt: t0, ExpiresAt: t0.Add(exp), CreatedBy: "k1"}
	}
	l1, l2, l3 := mk(live.ID, time.Hour), mk(trashed.ID, time.Hour), mk(gen.New(), -time.Hour)
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.InsertSigningKey(ctx, key); err != nil {
			return err
		}
		if err := tx.InsertTrash(ctx, domain.TrashEntry{ID: gen.New(), Object: trashed, DeletedAt: t0}); err != nil {
			return err
		}
		for _, l := range []domain.Link{l1, l2, l3} {
			if err := tx.InsertLink(ctx, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	active, err := s.ActiveSigningKey(ctx)
	if err != nil || active.ID != key.ID {
		t.Fatalf("ActiveSigningKey = %+v, %v", active, err)
	}

	r, err := s.LinkByID(ctx, l1.ID)
	if err != nil || r.ObjectState != domain.ObjectLive || r.ObjectPath != "edu/live.pdf" || string(r.KeySecret) != string(key.Secret) {
		t.Fatalf("LinkByID(live) = %+v, %v", r, err)
	}
	r, _ = s.LinkByID(ctx, l2.ID)
	if r.ObjectState != domain.ObjectTrashed || r.ObjectPath != "edu/trashed.pdf" {
		t.Fatalf("LinkByID(trashed) = %+v", r)
	}
	r, _ = s.LinkByID(ctx, l3.ID)
	if r.ObjectState != domain.ObjectGone {
		t.Fatalf("LinkByID(gone) = %+v", r)
	}

	list, _ := s.ListLinks(ctx, domain.LinkQuery{Now: t0})
	if len(list) != 2 {
		t.Fatalf("active links = %d", len(list))
	}
	list, _ = s.ListLinks(ctx, domain.LinkQuery{Now: t0, IncludeExpired: true})
	if len(list) != 3 {
		t.Fatalf("all links = %d", len(list))
	}
	list, _ = s.ListLinks(ctx, domain.LinkQuery{Now: t0, ObjectID: live.ID})
	if len(list) != 1 {
		t.Fatalf("links by object = %d", len(list))
	}
	list, _ = s.ListLinks(ctx, domain.LinkQuery{Now: t0, Within: "other/"})
	if len(list) != 0 {
		t.Fatalf("links within other/ = %d", len(list))
	}

	// Revoke, then rotate: the old key retires but stays while l2 is live.
	err = s.Update(ctx, func(tx *Tx) error {
		if err := tx.RevokeLink(ctx, l1.ID, t0); err != nil {
			return err
		}
		retired, err := tx.RetireSigningKeys(ctx, t0)
		if err != nil || len(retired) != 1 {
			return errors.Join(err, errors.New("retire count"))
		}
		n, err := tx.DeleteUnusedRetiredSigningKeys(ctx, t0)
		if err != nil || n != 0 {
			return errors.Join(err, errors.New("deleted a key still in use"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveSigningKey(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("active key after retire: %v", err)
	}
	list, _ = s.ListLinks(ctx, domain.LinkQuery{Now: t0})
	if len(list) != 1 || list[0].ID != l2.ID {
		t.Fatalf("after revoke: %+v", list)
	}

	err = s.Update(ctx, func(tx *Tx) error {
		if n, err := tx.RevokeLinksBySigningKey(ctx, key.ID, t0); err != nil || n != 2 {
			return errors.Join(err, errors.New("revoke-by-key count"))
		}
		if n, err := tx.DeleteUnusedRetiredSigningKeys(ctx, t0); err != nil || n != 1 {
			return errors.Join(err, errors.New("unused key not deleted"))
		}
		if n, err := tx.DeleteLinksByObject(ctx, live.ID); err != nil || n != 1 {
			return errors.Join(err, errors.New("delete by object count"))
		}
		n, err := tx.DeleteLinksBefore(ctx, t0.Add(time.Minute))
		if err != nil || n != 2 {
			return errors.Join(err, errors.New("delete before count"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
