package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

func TestDirectoryPolicyLifecycleAndScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rw := e.principal(api.ScopeRW, "")
	scoped := e.principal(api.ScopeRW, "ci/")
	ro := e.principal(api.ScopeRO, "ci/")
	_, err := e.svc.PatchDir(e.ctx, rw, "", api.DirPolicyPatch{Index: api.Some(api.IndexOff)}, service.DirWriteOptions{})
	wantErr(t, err, service.ErrForbidden)
	_, err = e.svc.PatchDir(e.ctx, e.admin, "", api.DirPolicyPatch{Hidden: api.Some(true)}, service.DirWriteOptions{})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.PatchDir(e.ctx, ro, "ci/", api.DirPolicyPatch{}, service.DirWriteOptions{})
	wantErr(t, err, service.ErrForbidden)
	_, err = e.svc.ShowDir(e.ctx, ro, "else/")
	wantErr(t, err, service.ErrForbidden)
	dir, err := e.svc.PatchDir(e.ctx, scoped, "ci", api.DirPolicyPatch{Hidden: api.Some(true), Index: api.Some(api.IndexOn)}, service.DirWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dir.Rule.Revision != 1 || dir.Exists || dir.URL != nil || dir.Effective.NoIndex != api.IndexOn {
		t.Fatal(dir)
	}
	e.put("ci/a", "public", api.Public)
	dir, err = e.svc.ShowDir(e.ctx, ro, "ci/")
	if err != nil || !dir.Exists || dir.URL == nil || dir.ListedInParent {
		t.Fatalf("directory: %+v %v", dir, err)
	}
	_, err = e.svc.PatchDir(e.ctx, scoped, "ci/", api.DirPolicyPatch{Hidden: api.Null[bool]()}, service.DirWriteOptions{IfMatch: 9})
	wantErr(t, err, service.ErrPreconditionFailed)
	dir, err = e.svc.PatchDir(e.ctx, scoped, "ci/", api.DirPolicyPatch{Hidden: api.Null[bool]()}, service.DirWriteOptions{IfMatch: 1})
	if err != nil || !dir.ListedInParent || dir.Rule.Revision != 2 {
		t.Fatalf("directory: %+v %v", dir, err)
	}
	_, _, err = e.svc.PutDir(e.ctx, scoped, "ci/", api.DirPolicyRequest{}, service.DirWriteOptions{IfNoneMatch: true})
	wantErr(t, err, service.ErrPreconditionFailed)
	e.put("a", "file", api.Private)
	if _, _, err := e.svc.PutDir(e.ctx, rw, "a/", api.DirPolicyRequest{}, service.DirWriteOptions{}); err != nil {
		t.Fatal("file/policy conflict:", err)
	}
	deleted, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "ci/a"})
	if err != nil {
		t.Fatal(err)
	}
	orphaned, err := e.svc.ListDirPolicies(e.ctx, ro, service.DirPolicyParams{Recursive: true, Orphaned: true})
	if err != nil || len(orphaned.Policies) != 1 || orphaned.Policies[0].Prefix != "ci/" {
		t.Fatalf("orphaned: %+v %v", orphaned, err)
	}
	if _, err := e.svc.Restore(e.ctx, e.admin, deleted.TrashIDs[0], api.RestoreRequest{}); err != nil {
		t.Fatal(err)
	}
	dir, err = e.svc.ShowDir(e.ctx, ro, "ci/")
	if err != nil || !dir.Exists || dir.Rule.Revision != 2 {
		t.Fatalf("restore changed policy: %+v %v", dir, err)
	}
	if err := e.svc.DeleteDir(e.ctx, scoped, "ci/", service.DirWriteOptions{IfMatch: 2}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.svc.DeleteDir(e.ctx, scoped, "ci/", service.DirWriteOptions{}), service.ErrNotFound)
}

func TestDirectoryPoliciesAcrossProcesses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("a/file", "public", api.Public)
	// Independent pools emulate a maintenance process, not just another service.
	st, err := wire.OpenStorage(e.ctx, e.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	other, err := wire.NewService(st, e.clock, nil, testOptions(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Index(e.ctx, "a/"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.PatchDir(e.ctx, e.admin, "a/", api.DirPolicyPatch{Index: api.Some(api.IndexOff)}, service.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Index(e.ctx, "a/")
	wantErr(t, err, service.ErrNotFound)
	if err := other.DeleteDir(e.ctx, e.admin, "a/", service.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Index(e.ctx, "a/"); err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveMoveDirectoryPolicies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("from/sub/file", "original", api.Public)
	patch := api.DirPolicyPatch{Index: api.Some(api.IndexOn), Hidden: api.Some(true)}
	for _, prefix := range []string{"from/", "from/sub/", "to/unrelated/"} {
		if _, err := e.svc.PatchDir(e.ctx, e.admin, prefix, patch, service.DirWriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.svc.MoveWithPolicies(e.ctx, e.admin, api.MoveRequest{From: "from/", To: "to/", Recursive: true})
	wantErr(t, err, service.ErrConflict)
	var se *service.Error
	if !errors.As(err, &se) || len(se.Details["conflicting_policies"].([]string)) != 1 {
		t.Fatal(err)
	}
	if _, err := e.svc.Stat(e.ctx, e.admin, "from/sub/file"); err != nil {
		t.Fatal("failed move changed objects:", err)
	}
	out, err := e.svc.MoveWithPolicies(e.ctx, e.admin, api.MoveRequest{From: "from/", To: "to/", Recursive: true, Overwrite: true})
	if err != nil || out.Moved != 1 || out.PoliciesMoved != 2 {
		t.Fatalf("move: %+v %v", out, err)
	}
	list, err := e.svc.ListDirPolicies(e.ctx, e.admin, service.DirPolicyParams{Recursive: true})
	if err != nil || len(list.Policies) != 2 || list.Policies[0].Prefix != "to/" || list.Policies[1].Prefix != "to/sub/" || list.Policies[0].Revision != 2 {
		t.Fatalf("policies: %+v %v", list, err)
	}
	// Moving toward an ancestor must not mistake its own source rules for targets.
	out, err = e.svc.MoveWithPolicies(e.ctx, e.admin, api.MoveRequest{From: "to/sub/", To: "to/", Recursive: true, Overwrite: true})
	if err != nil || out.PoliciesMoved != 1 {
		t.Fatalf("ancestor move: %+v %v", out, err)
	}
	// A single file move leaves the directory rule where it was.
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "to/file", To: "file"}); err != nil {
		t.Fatal(err)
	}
	dir, err := e.svc.ShowDir(e.ctx, e.admin, "to/")
	if err != nil || dir.Rule == nil || dir.Exists {
		t.Fatalf("orphan: %+v %v", dir, err)
	}
}

func TestDirectoryPolicyConcurrentChanges(t *testing.T) {
	e := newEnv(t)
	e.put("a/file", "public", api.Public)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 25 {
				if i%2 == 0 {
					_, err := e.svc.PatchDir(context.Background(), e.admin, "a/", api.DirPolicyPatch{Hidden: api.Some(j%2 == 0)}, service.DirWriteOptions{})
					if err != nil {
						t.Error(err)
					}
				} else {
					if _, err := e.svc.Index(context.Background(), ""); err != nil {
						t.Error(err)
					}
				}
			}
		})
	}
	wg.Wait()
}

// R-001: overwrite may remove the admin-owned root policy even when no source
// policy maps onto root. Authorization must cover both kinds of root mutation.
func TestMoveCannotDeleteRootPolicyAsRW(t *testing.T) {
	for _, descendants := range []bool{false, true} {
		t.Run(fmt.Sprint(descendants), func(t *testing.T) {
			e := newEnv(t)
			original := e.put("from/file", "public", api.Public)
			if _, err := e.svc.PatchDir(e.ctx, e.admin, "", api.DirPolicyPatch{Index: api.Some(api.IndexOff)}, service.DirWriteOptions{}); err != nil {
				t.Fatal(err)
			}
			if descendants {
				if _, err := e.svc.PatchDir(e.ctx, e.admin, "from/sub/", api.DirPolicyPatch{Hidden: api.Some(true)}, service.DirWriteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			req := api.MoveRequest{From: "from/", To: "", Recursive: true, Overwrite: true}
			_, err := e.svc.MoveWithPolicies(e.ctx, e.principal(api.ScopeRW, ""), req)
			wantErr(t, err, service.ErrForbidden)
			got, err := e.svc.Stat(e.ctx, e.admin, "from/file")
			if err != nil || got.ID != original.ID || got.Revision != original.Revision {
				t.Fatalf("failed move changed object: %+v %v", got, err)
			}
			root, err := e.svc.ShowDir(e.ctx, e.admin, "")
			if err != nil || root.Rule == nil || root.Rule.Revision != 1 || root.Effective.Index != api.IndexOff {
				t.Fatalf("failed move changed root: %+v %v", root, err)
			}
			if descendants {
				sub, err := e.svc.ShowDir(e.ctx, e.admin, "from/sub/")
				if err != nil || sub.Rule == nil || sub.Rule.Revision != 1 {
					t.Fatalf("failed move changed descendant: %+v %v", sub, err)
				}
			}
			out, err := e.svc.MoveWithPolicies(e.ctx, e.admin, req)
			if err != nil || out.Moved != 1 {
				t.Fatalf("admin move: %+v %v", out, err)
			}
			root, err = e.svc.ShowDir(e.ctx, e.admin, "")
			if err != nil || root.Rule != nil || root.Effective.Index != api.IndexOn {
				t.Fatalf("admin replacement: %+v %v", root, err)
			}
		})
	}
	t.Run("no root mutation", func(t *testing.T) {
		e := newEnv(t)
		e.put("from/file", "public", api.Public)
		if _, err := e.svc.PatchDir(e.ctx, e.admin, "from/sub/", api.DirPolicyPatch{}, service.DirWriteOptions{}); err != nil {
			t.Fatal(err)
		}
		out, err := e.svc.MoveWithPolicies(e.ctx, e.principal(api.ScopeRW, ""), api.MoveRequest{From: "from/", To: "", Recursive: true, Overwrite: true})
		if err != nil || out.Moved != 1 || out.PoliciesMoved != 1 {
			t.Fatalf("rw without root mutation: %+v %v", out, err)
		}
	})
}
