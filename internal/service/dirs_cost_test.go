package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
)

type publicReadCounts struct {
	service.Store
	listings, rows, stats, existence int
}

func (c *publicReadCounts) ListObjects(ctx context.Context, q domain.ListQuery) (domain.ListResult, error) {
	c.listings++
	result, err := c.Store.ListObjects(ctx, q)
	c.rows += len(result.Objects)
	return result, err
}

func (c *publicReadCounts) HasPublicObjects(ctx context.Context, prefix string, now time.Time) (bool, error) {
	c.existence++
	return c.Store.HasPublicObjects(ctx, prefix, now)
}

func (c *publicReadCounts) DirStats(ctx context.Context, prefix string, now time.Time) (int, time.Time, error) {
	c.stats++
	return c.Store.DirStats(ctx, prefix, now)
}

// R-003: rejection cost must not load descriptions or count a whole subtree.
func TestDisabledIndexUsesBoundedExistence(t *testing.T) {
	e := newEnv(t)
	for i := range 300 {
		e.put(fmt.Sprintf("disabled/item%03d", i), "public", api.Public)
	}
	if err := e.storage.DB.Update(e.ctx, func(tx *store.Tx) error {
		objects, err := tx.ObjectsUnder(e.ctx, "disabled/")
		if err != nil {
			return err
		}
		for _, o := range objects {
			o.Description = strings.Repeat("x", 4096)
			if err := tx.UpdateObject(e.ctx, o); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"disabled/", ""} {
		if _, err := e.svc.PatchDir(e.ctx, e.admin, prefix, api.DirPolicyPatch{Index: api.Some(api.IndexOff)}, service.DirWriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	counts := &publicReadCounts{}
	service.DecorateStore(e.svc, func(real service.Store) service.Store { counts.Store = real; return counts })
	for _, prefix := range []string{"disabled/", "absent/", ""} {
		counts.listings, counts.rows, counts.stats, counts.existence = 0, 0, 0, 0
		_, err := e.svc.Index(e.ctx, prefix)
		wantErr(t, err, service.ErrNotFound)
		if counts.listings != 0 || counts.rows != 0 || counts.stats != 0 || counts.existence != 1 {
			t.Fatalf("%q rejection scanned metadata: listings=%d rows=%d stats=%d existence=%d", prefix, counts.listings, counts.rows, counts.stats, counts.existence)
		}
	}
	for _, path := range []string{"disabled", "absent"} {
		counts.listings, counts.rows, counts.stats, counts.existence = 0, 0, 0, 0
		_, err := e.svc.ResolvePublic(e.ctx, path)
		wantErr(t, err, service.ErrNotFound)
		if counts.listings != 0 || counts.rows != 0 || counts.stats != 0 || counts.existence != 1 {
			t.Fatalf("%q redirect resolution scanned subtree: %+v", path, counts)
		}
	}
}
