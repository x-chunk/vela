# Directory policy review

Reviewed the directory-policy feature (baseline `ab6735a`) on `codex/directory-policies`
against `AGENTS.md`, `docs/SPEC.md`, `docs/SPEC-dirs.md` (including the accepted
decisions in section 16), and `docs/directories.md` on 2026-10-12.

The review covered the changed API, client, CLI, configuration, service, store,
migration, public delivery and theme code, documentation, and associated tests.
Accepted compatibility decisions (legacy counts/HTML without rules, additive
JSON fields, and the string `Page.Parent`) are not findings. The new code
preserves the repository's layering and adds no dependencies. No additional
actionable architecture or comment-quality findings were identified.

## R-001 [severity: high] Recursive overwrite can remove the root policy without admin

- Where: `internal/service/move.go:220` (destination policy deletion), with the
  incomplete root authorization check at `internal/service/move.go:125`.
- Problem: The admin check applies only when a source policy maps onto the root.
  An unrestricted `rw` key can instead move a source subtree with no rule on its
  own prefix to root with `overwrite: true`. The existing root rule is included
  in `destinationPolicies` and deleted without checking admin scope. This
  violates `SPEC-dirs.md` section 9.3, which reserves root policy mutations for
  unrestricted admins. With the default `VELA_ROOT_INDEX=on`, deleting an
  admin-created `index: off` root rule immediately re-enables the public root
  index and can reveal its public directory/file listing.
- Reproduction: As admin, store a public object at `from/file` and set the root
  policy to `{"index":"off"}`; leave `from/` without a policy. As an unrestricted
  `rw` key, send `POST /api/v1/move` with
  `{"from":"from/","to":"","recursive":true,"overwrite":true}`. The operation
  succeeds, `GET /api/v1/dirs/` reports `rule: null` and effective `index: on`
  from `config`, and the root public index is enabled. A temporary Go test
  supplied through `go test -overlay` reproduced this against the real SQLite
  service: the move returned no error and removed the root rule.
- Suggestion: Check admin authorization whenever a recursive move will delete
  or replace an existing root policy, in addition to when a source policy maps
  onto root. Perform the check inside the same transaction before mutations.
  Add service and HTTP regression tests that require 403 and verify all object
  paths and rules remain unchanged, including when only descendant source
  policies move. Retain successful behavior for an unrestricted admin and for
  moves that do not mutate a root policy.
- Status: fixed (d02e66f)

## Resolution

Commit `d02e66f` requires admin scope whenever overwrite deletes an existing
root policy, independently of which source policies are moving. The check runs
inside the write transaction before any objects or rules change. Existing
prefix checks ensure the admin is unrestricted at root.

The coding agent added service tests for both vulnerable source shapes, intact
object/rule revisions after rejection, successful admin replacement, and an rw
move that does not mutate root. An HTTP test verifies 403 and the unchanged
public root index. These tests failed against the baseline before the fix.
Astra independently re-ran the original overlay reproducer and the new service
and HTTP tests after the fix; all passed. No findings remain open.

## Verification

The independent review ran the focused root-authorization reproducer through a
temporary Go overlay; it failed the expected authorization assertion as
described above. No implementation or test files were changed by the review.
`make check-neutrality` passed.
The coordinating coding agent completed:

| Check | Result |
| --- | --- |
| `make fmt` | Passed |
| `make test` (race detector) | Passed after R-001 fix |
| `make lint` | Passed, 0 issues |
| `make test-e2e` | Passed against real nginx after fix |
| `make test-s3` | Passed against isolated MinIO after fix |
| `make validate-examples` | Passed nginx and Caddy validation |
| `make check-neutrality` | Passed |
| `make build` | Both cgo-free binaries built |
| `git diff --check` | Passed |

Golden fixtures captured before implementation preserve the no-policy HTML
byte-for-byte. Existing public JSON fields are preserved while allowing the
new additive theme metadata. The separately maintained documentation website
was outside the repository scope; technical documentation and accepted
specification decisions are included in this branch.
