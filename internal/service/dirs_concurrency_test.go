package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

type pausedPublicStore struct {
	service.Store
	once           sync.Once
	betweenBatches bool
	started        chan struct{}
	resume         chan struct{}
}

func (p *pausedPublicStore) ListObjects(ctx context.Context, q domain.ListQuery) (domain.ListResult, error) {
	var err error
	var first domain.ListResult
	p.once.Do(func() {
		if p.betweenBatches {
			batch := q
			batch.Limit = 256
			first, err = p.Store.ListObjects(ctx, batch)
			if err != nil {
				return
			}
		}
		close(p.started)
		select {
		case <-p.resume:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	if err != nil {
		return domain.ListResult{}, err
	}
	if len(first.Objects) > 0 {
		q.After = first.Last
	}
	rest, err := p.Store.ListObjects(ctx, q)
	if err != nil {
		return domain.ListResult{}, err
	}
	rest.Objects = append(first.Objects, rest.Objects...)
	return rest, nil
}

// R-002: a root response must not combine new paths with the old hidden rule.
// Separate SQLite pools ensure the fix also works across process boundaries.
func TestPublicIndexDuringHiddenMove(t *testing.T) {
	for _, between := range []bool{false, true} {
		for _, format := range []string{"html", "json"} {
			t.Run(fmt.Sprintf("between_batches=%t/%s", between, format), func(t *testing.T) {
				e := newEnv(t)
				e.put("visible/file", "visible", api.Public)
				e.clock.Advance(time.Hour)
				hidden := e.put("hidden/file", "hidden", api.Public)
				base, err := e.storage.DB.ObjectByID(e.ctx, hidden.ID)
				if err != nil {
					t.Fatal(err)
				}
				for i := range 300 {
					e.put(fmt.Sprintf("hidden/item%03d", i), "hidden", api.Public)
				}
				if _, err := e.svc.PatchDir(e.ctx, e.admin, "hidden/", api.DirPolicyPatch{Hidden: api.Some(true)}, service.DirWriteOptions{}); err != nil {
					t.Fatal(err)
				}
				st, err := wire.OpenStorage(e.ctx, e.dataDir, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = st.Close() }()
				other, err := wire.NewService(st, e.clock, nil, testOptions(nil))
				if err != nil {
					t.Fatal(err)
				}
				paused := &pausedPublicStore{betweenBatches: between, started: make(chan struct{}), resume: make(chan struct{})}
				service.DecorateStore(e.svc, func(real service.Store) service.Store { paused.Store = real; return paused })
				theme, err := index.Load("")
				if err != nil {
					t.Fatal(err)
				}
				handler := server.New(e.svc, theme, nil, server.Options{Clock: e.clock, SiteTitle: "Test"}).Handler()
				ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
				defer cancel()
				url := "/"
				if format == "json" {
					url += "?format=json"
				}
				req := httptest.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { r := httptest.NewRecorder(); handler.ServeHTTP(r, req); result <- r }()
				select {
				case <-paused.started:
				case <-ctx.Done():
					t.Fatal("index did not reach repository barrier")
				}
				e.clock.Advance(time.Hour)
				_, moveErr := other.MoveWithPolicies(ctx, e.admin, api.MoveRequest{From: "hidden/", To: "renamed/", Recursive: true})
				close(paused.resume)
				if moveErr != nil {
					t.Fatal(moveErr)
				}
				var r *httptest.ResponseRecorder
				select {
				case r = <-result:
				case <-ctx.Done():
					t.Fatal("index did not finish")
				}
				if r.Code != http.StatusOK {
					t.Fatalf("index status %d: %s", r.Code, r.Body.String())
				}
				for _, name := range []string{"hidden/", "renamed/"} {
					if strings.Contains(r.Body.String(), name) {
						t.Fatalf("hidden name leaked: %s", r.Body.String())
					}
				}
				if format == "json" {
					var page index.Page
					if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					if len(page.Dirs) != 1 || page.Dirs[0].Count != 1 || !page.Dirs[0].UpdatedAt.Equal(t0) {
						t.Fatalf("hidden counts or dates leaked: %+v", page.Dirs)
					}
				} else if !strings.Contains(r.Body.String(), `datetime="`+t0.Format(time.RFC3339)+`"`) || strings.Contains(r.Body.String(), base.UpdatedAt.Format(time.RFC3339)) {
					t.Fatal("visible date changed or hidden date leaked")
				}
			})
		}
	}
}
