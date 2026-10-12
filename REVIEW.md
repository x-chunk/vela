# Review: directory policies against master

Reviewed on 2026-10-12:

- Branch: `codex/directory-policies`
- Head: `d20dee4418777c349febaf41e816dc54c7f63b51`
- Base: `master`, `3fa8548850b7e3b2b9760a40802f3bd7efa168e4` (also the merge base)
- Scope: the complete 56-file branch diff, including API/client/CLI contracts,
  service authorization and concurrency, SQLite migration/cache, public delivery,
  theme rendering, configuration, documentation, and tests.
- References: `AGENTS.md`, `docs/SPEC.md`, `docs/SPEC-dirs.md` (including the
  accepted decisions in section 16), and the affected technical references.

**All findings are fixed.** R-002, R-003, and R-004 were implemented in separate
commits; the earlier R-001 remains fixed and is retained below for continuity. No implementation or permanent test
files were changed during this review; only this report was updated.

Accepted decisions about legacy counts/HTML without policies, additive JSON
fields, the string `Page.Parent`, root defaults, and policy-only moves are not
findings. The changes preserve package layering and introduce no dependencies.
No additional actionable architecture or standalone comment-quality findings
were identified; R-003 also identifies an inaccurate query-cost comment.

## R-002 [severity: high] A concurrent move exposes a hidden directory in the public index

- Where: `internal/service/public.go:86-90`,
  `internal/service/dirs.go:395-419`.
- Problem: `Index` obtains a policy snapshot and then reads objects through a
  separate database operation. The policy generation is never checked again
  after the object read. A recursive move commits the new object paths and
  policy prefixes atomically, but an overlapping index request can combine
  the old policy map with the new paths. The destination then has no hidden
  rule in that map and appears in the public HTML/JSON index. This violates
  `SPEC-dirs.md` sections 1 and 5.2: a hidden entrance must stay out of its
  parent's index. It is possible even when the directory is hidden in every
  committed database state. The response can also be cached for 60 seconds.
- Reproduction: Create a public object at `hidden/file` and a rule
  `hidden/` with `hidden: true`. Start a root `Index` request and pause it after
  it obtains policies but before `ListObjects`. Complete a recursive move from
  `hidden/` to `renamed/`, then resume the read. A deterministic temporary
  overlay test using real SQLite and the real service returned
  `Dirs: [{Path: renamed/, Count: 1}]`; a subsequent root index returned no
  directories. The test failed the assertion that the overlapping index must
  also contain no directories.
- Suggestion: Read object metadata and policies from a consistent database
  snapshot, or validate the policy generation after the complete object read
  and retry the whole resolution when it changes. The latter must cover all
  batches of `ListObjects`, and must work across processes, not only behind a
  local mutex. Add a deterministic concurrent-move test for public HTML/JSON
  showing that neither hidden names nor their counts/dates become visible.
- Resolution: The service checks the persistent policy generation after the complete
  object listing, including every SQL batch, and retries on a change. Public
  HTML/JSON and the effective-directory API use the validated policy snapshot.
  Deterministic tests move a hidden subtree through independent SQLite pools
  before the read and between batches, verifying names, counts, and dates.
- Status: fixed (0da796a)

## R-003 [severity: medium] Disabled indexes scan the full subtree before returning 404

- Where: `internal/service/public.go:90-95`,
  `internal/service/dirs.go:395-419`; batching in
  `internal/store/objects.go:233-257`.
- Problem: The disabled-index check happens after `publicTree` has loaded all
  live public object metadata and built the complete subtree. A missing prefix
  produces an empty query, while a populated disabled prefix reads and
  allocates in proportion to its entire contents. `ListObjects` issues SELECTs
  in batches of 256, so the comment claiming the same query path does not imply
  the same number of queries. The resulting 404s are readily distinguishable
  by latency, contrary to `SPEC-dirs.md` section 5.1. With a disabled root,
  every anonymous request to `/` additionally forces a full-store metadata
  scan even though no index will be rendered.
- Reproduction: A temporary test inserted 5,001 live public objects under
  `disabled/`, with 4,096-character descriptions on 5,000 of them, then set
  `index: off`. Ten service requests per prefix averaged approximately
  **129 ms** for `disabled/` versus **0.44 ms** for an absent prefix on this
  machine. Both returned `ErrNotFound`. The disabled path fetches about 20 MiB
  of descriptions and builds the tree before discarding it; the absent path
  does neither. These measurements demonstrate the difference, not a fixed
  timing threshold for a regression test.
- Suggestion: Resolve enablement and use a bounded public-existence lookup
  before loading a tree. Keep missing and disabled rejection paths comparable,
  and build visible entries only for an index that can be returned. Add a test
  that counts reads/rows or otherwise proves a disabled index does not scan
  its full subtree; retain the existing identical-body/header assertions.
  Correct the related query-cost claims in comments and documentation.
- Resolution: Public index rejection uses a bounded existence query without loading
  object metadata or building a subtree. Disabled, absent, and disabled-root
  paths perform the same existence check; slashless directory URLs avoid full
  subtree statistics too. Read-count tests verify no metadata listings/rows
  or aggregate statistics, and the store tests cover visibility and expiry.
- Status: fixed (f843e93)

## R-004 [severity: medium] Existing CLI commands now fail against older API v1 servers

- Where: `internal/cli/browse.go:85-88` and
  `internal/cli/browse.go:248-250`.
- Problem: Directory annotations are now a mandatory request for `ls -l`, and
  directory URL resolution unconditionally requires the new `/api/v1/dirs`
  endpoint. A server built from `master` still advertises API version 1 but
  has no such endpoint. Updating only the CLI therefore breaks ordinary
  `ls -l` whenever its listing contains a directory, and breaks `url DIR/`
  and `open DIR/` for existing public directories. They exit with code 3 and
  `no such API endpoint`, although the requested objects/index exist. Ordinary
  `ls` and `ls -l --json` continue working, making the regression dependent on
  output format. There is no capability negotiation or legacy fallback.
- Reproduction: A temporary CLI overlay test used the real in-process server
  with a public `docs/file`, making the new directory requests take the same
  authenticated unknown-route fallback used by `master`. `ls` and
  `ls -l --json` succeeded; `ls -l`, `url docs/`, and `open docs/` all exited 3
  with `vela: no such API endpoint`. Inspection of `master` confirms its route
  table has no `/dirs`, while its existing CLI implements these commands
  through `/objects` and `/info`.
- Suggestion: Detect support for directory policies. When this additive
  endpoint is absent, retain the existing listing without annotations and the
  legacy URL resolution for existing commands. Preserve real authorization,
  transport, and disabled-index errors on servers that support policies.
  Add CLI compatibility tests against a server without `/dirs`; new `dir`
  commands can report that the server lacks the feature.
- Resolution: Long listings omit annotations only when the directory collection is
  absent. Directory URL/open requests verify collection support before using
  legacy /info resolution. Authorization, unavailable/transport, supported
  individual 404, and disabled-index errors remain errors. Compatibility
  tests exercise the authenticated unknown-route behavior of older API v1.
- Status: fixed (1df36e9)

## R-001 [severity: high] Recursive overwrite can remove the root policy without admin

- Where: `internal/service/move.go`, destination-policy deletion and its
  authorization checks.
- Original problem: An unrestricted `rw` key could move a source subtree to
  root with overwrite and delete an existing admin-created root policy even
  when no source policy mapped onto root. With the default root index enabled,
  this could re-enable an admin-disabled public root index.
- Resolution: Commit `d02e66f` checks admin scope when overwrite would remove an
  existing root policy, independently of which source policies are moving.
  The check runs inside the write transaction before any mutations. Existing
  prefix checks also prevent a restricted admin from operating on root.
  The service and HTTP regression tests passed again in this review, including
  unchanged objects/rules after rejection and permitted moves that do not
  mutate a root rule.
- Status: fixed (d02e66f)

## Verification

The following checks were run for the original review on `d20dee4`:

| Check | Result |
| --- | --- |
| `make test` | Passed, all packages with the race detector |
| `make lint` | Passed, 0 issues |
| `make test-e2e` | Passed against real nginx, including directory headers |
| `make test-s3` | Passed against isolated MinIO, including proxy/redirect crawler headers |
| `make check-neutrality` | Passed |
| `git diff --check master...HEAD` | Passed |
| New no-policy golden test run on an isolated archive of `master` | Passed; fixtures independently match the baseline |
| Concurrent hidden-move overlay reproducer | Failed the expected non-disclosure assertion; R-002 confirmed |
| Disabled-index cost overlay probe | Both errors were 404 equivalents; disproportionate work confirmed for R-003 |
| CLI without `/dirs` overlay reproducer | Failed the expected successful-command assertions; R-004 confirmed |

The initial sandboxed test attempts could not open local ports, and the initial
lint attempt could not reach the Go proxy. Authorized reruns outside the
sandbox passed; those initial environment failures are not code findings.
Temporary overlay tests and their fixtures were kept under `/tmp`, with no
changes to repository implementation or test files. Passing existing suites
does not cover or invalidate the three confirmed findings above.


## Fix verification

Each fix commit passed `make test` with the race detector and `make lint`.
Final verification after all three implementation commits also passed:

- `make test-e2e` against real nginx.
- `make test-s3` against isolated MinIO, including the new concurrency tests.
- `make validate-examples` for nginx/Caddy configurations.
- `make build` for both cgo-free binaries.
- `make check-neutrality` and `git diff --check master`.

The R-002 deterministic HTTP test failed against the original service with
hidden names/counts/dates in its output, then passed after the fix. The R-003
read-cost test first observed a full 300-object metadata listing before 404;
after the fix it observes one existence lookup and no metadata listings or
statistics. The R-004 legacy-server test first failed with an unknown endpoint,
then passed with the fallback; supporting-server error cases also pass.

No open findings remain. This closure is saved in Git history before the report
is removed in a separate commit at the user's request.
