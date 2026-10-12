# AGENTS.md

Instructions for agents that review or change this repository: the review agent (Astra) and coding agents. Humans are welcome too.

## What Vela is

Vela is a self-hosted file store with a Go server (`velad`) and CLI (`vela`). The server serves a REST API (`/api/v1`), a public HTML index, direct URLs, expiring HMAC-signed links, and a recoverable trash. Metadata lives in SQLite; contents live in the local filesystem or a private R2/S3 bucket. Go owns all path, access, visibility, and caching decisions. Files stream directly by default; nginx accel is an explicit filesystem optimization.

The specification is [`docs/SPEC.md`](docs/SPEC.md). Its section 21 records every deliberate deviation from the original text. If code and spec disagree, either the code is wrong or section 21 is missing an entry.

## Repository map

```
api/                public contract: DTOs, error codes, headers, routes, path rules, durations, merge patch
client/             public Go client for /api/v1 (retries, typed errors); importable by other modules
cmd/vela            CLI entrypoint (thin, calls internal/cli)
cmd/velad           server entrypoint (thin, calls internal/velad)
internal/archtest   test that enforces the import rules below
internal/content    backend-independent streaming blob contract
internal/blobtest   shared blob contract tests
internal/r2         private S3-compatible R2 backend
internal/blob       filesystem blob store: tmp/ -> fsync -> rename into blobs/<shard>/<id>
internal/cli        vela commands (cobra); talks to the server only through client/
internal/cli/config CLI profiles (TOML), 0600 enforcement, atomic writes
internal/cli/output human/JSON output, tables, colors, progress bar
internal/clock      injectable clock and a fake for tests
internal/config     velad configuration: env + flags, fail-fast validation
internal/domain     entities and query types shared by service and store
internal/ids        lower-case ULID generator
internal/index      HTML index rendering, embedded theme, VELA_THEME_DIR overrides
internal/keys       API key format: generate, parse, hash, constant-time verify
internal/mimetype   content type detection and normalization
internal/server     HTTP transport: routing, middleware, handlers, error mapping, delivery
internal/service    business rules: visibility, scopes/prefixes, uploads, moves, trash, links, janitor
internal/sign       HMAC-SHA256 signatures of signed links
internal/store      SQLite repositories (modernc.org/sqlite, no cgo), embedded migrations
internal/testutil   in-process velad for tests (test-only)
internal/units      byte sizes: parse and format
internal/velad      velad subcommands: serve, key, signing, janitor, backup, healthcheck, migrate
internal/version    build metadata from ldflags with a buildinfo fallback
internal/wire       assembles the service from store and blob (the only place that knows both)
examples/nginx      illustrative proxy configurations, exercised by the e2e test
examples/caddy      Caddy alternative for fs-direct and r2, checked by validate-examples
examples/           Docker and environment examples for fs-direct, fs-accel, and R2/S3
test/e2e            delivery through a real nginx in Docker (build tag e2e)
docs/SPEC.md        the specification
```

### Layering

```
server ──► service ──► interfaces declared in service (deps.go)
                          ▲               ▲
                 store (SQLite)     blob (filesystem)     ◄── wired together in internal/wire
```

- `internal/server` calls `internal/service` and never touches SQL or the filesystem.
- `internal/service` declares the interfaces it needs (`Reader`, `Writer`, `Store`, `Blobs`) and never imports `net/http`, `database/sql`, `internal/store` or `internal/blob`. Domain errors are typed (`service.ErrNotFound`, ...) and are mapped to HTTP status and API codes in exactly one place: `internal/server/errors.go`.
- `internal/wire` adapts the concrete store and blob store to the service interfaces. `cmd/velad` (via `internal/velad`) and `internal/testutil` build the same stack through it.
- `api` and `client` are public: they must not import anything under `internal/`. `internal/cli` uses `client`, never the service or store.

`internal/archtest` enforces these rules; extend it when you add a package.

## Commands

```bash
make build      # bin/vela and bin/velad with version ldflags (-trimpath, CGO_ENABLED=0)
make test       # go test -race ./... (cgo only for the race detector)
make lint       # golangci-lint v2.14.0, pinned and run via "go run"
make fmt        # gofmt + goimports
make test-e2e   # nginx end-to-end test (needs Docker, Linux host networking)
make dev        # velad on 127.0.0.1:8080 in direct mode with ./.dev-data; prints an admin key
make docker     # local image build
make dist       # CLI release archives for 6 platforms + checksums.txt (needs zip)
```

Before declaring work done: `make test` and `make lint` are green, and `make test-e2e` too if delivery, headers or the nginx config changed.

## Project neutrality

Use reserved example domains, documentation IP ranges, and neutral paths in source, fixtures, and docs. The public module `go.xchunk.org/vela`, documentation URL `https://docs.xchunk.org/vela`, source repository, and official Docker Hub image are project identity. Personal deployment values belong outside the repository. Run `make check-neutrality` during review; CI also scans secrets and validates proxy examples.

## Conventions

- **Comments** are American English, production style. Every package has a package comment and every exported identifier a doc comment (revive enforces both). Comments explain *why*, not *what*. Security-relevant decisions are commented where they are implemented.
- **Language**: identifiers, log messages, API and CLI messages are English.
- **Errors**: wrap with context (`fmt.Errorf("open blob: %w", err)`), compare with `errors.Is`/`errors.As`. Client-facing errors from the service are `*service.Error` with a kind, a safe message and optional details. Internal errors become `500 internal` with a generic message; details go to the log only.
- **Context**: pass `context.Context` to everything that does I/O.
- **Logging**: `log/slog`, one structured line per request (method, route, path, status, bytes, duration, request_id, ip, key_id). Never log API keys, the `Authorization` header, signatures or any query string. The access log deliberately has no query.
- **Time**: UTC everywhere; the database stores Unix milliseconds; the API shows RFC 3339 with seconds.
- **Tests**: the service is tested on real SQLite and a real filesystem in a temporary directory, using a fake clock. HTTP tests use `internal/testutil`. Prefer table tests for rules such as paths, durations and tags.
- **Dependencies** are kept minimal. Current ones and why:
  - `modernc.org/sqlite`: pure-Go SQLite, which keeps the build cgo-free.
  - `github.com/spf13/cobra`: command trees, help and shell completion for both binaries.
  - `github.com/oklog/ulid/v2`: ULIDs.
  - `golang.org/x/text`: Unicode NFC.
  - `golang.org/x/term`: no-echo key input and TTY detection.
  - `golang.org/x/sys`: Windows DACLs for the private CLI config (it already came in through `x/term`).
  - `github.com/pelletier/go-toml/v2`: the CLI config.

  - `github.com/aws/aws-sdk-go-v2` (`service/s3`, `feature/s3/manager`, `credentials`) and `github.com/aws/smithy-go`: R2/S3 streaming multipart, static credentials, presigning, and typed/redacted backend errors. Server-only; architecture tests exclude the SDK from the CLI.

  Justify any new dependency here.
- **Commits** are atomic: one logical change per commit, each building with green tests. Mainline branch is `master`.

## Security invariants (review checklist)

1. Secrets are compared in constant time: API key hashes (`keys.Verify`), link signatures (`sign.Verify` via `hmac.Equal`), upload checksums.
2. Private, expired, trashed and missing objects all answer **404** on the public side, never 403: existence is not disclosed. Signed-link failures are 404 too, except an expired `exp`, which is 410.
3. API keys are accepted **only** from `Authorization: Bearer`. There is no query parameter or cookie, so keys never reach URLs, logs or browser history.
4. Keys, the `Authorization` header, signatures and query strings are never logged. In nginx every server block (the HTTP redirect server too) uses the query-free `vela_noquery` format, and signed links (`/s/`, `/_blobs/s/`) have the error log disabled, because nginx's error log always records the full request line. `test/e2e` checks the actual logs.
5. Writes are atomic: content goes to `tmp/`, is size-limited and hashed while streaming, verified against `X-Vela-SHA256`, chmodded, fsynced and renamed into `blobs/`; every new directory and the shard are fsynced. Only then is the database row written. A failed transaction deletes the new blob. Content that loses its last reference (overwrite, purge, orphan) is never unlinked directly: the same transaction queues it in `blob_gc`, and `CollectBlobs` deletes it only after the grace period and a FULL WAL checkpoint (the durability barrier for `synchronous=NORMAL`), re-checking that nothing references it.
6. **Key prefixes are checked on every operation**: list (narrowed for ancestors, 403 for unrelated paths), stat, download, create, upload, patch, delete, the move source **and target**, the restore target, trash listing (trash entries outside the prefix are 404), links create/list/revoke. Scopes: `ro` < `rw` < `admin`; purge needs `admin`; link listing needs `rw` because it returns working URLs.
7. The `exp` in a signed URL must equal the stored `expires_at`; a link cannot be extended by editing `exp`. Expiry is checked before any database access.
8. Size limits: `VELA_MAX_UPLOAD` is enforced from `Content-Length` before reading and while streaming (chunked); `VELA_MAX_CREATE` for inline content; JSON bodies are capped; request bodies have a per-read idle deadline.
9. File permissions are set explicitly, not left to the umask: database 0600, `tmp/` 0700, `blobs/` dirs 0755 and files 0644 (nginx must read them), CLI config 0600 in a 0700 directory (on Windows: a protected DACL for the user and SYSTEM only). The CLI refuses a config readable by others on every platform.
10. `X-Real-IP`, `X-Forwarded-For` and `X-Request-ID` are trusted only from `VELA_TRUSTED_PROXIES`. Otherwise clients could dodge the auth rate limit or inject log IDs.
11. API keys are managed only by `velad key ...` inside the container, never over HTTP.
12. Paths are validated in one place (`api.CleanObjectPath`/`CleanPrefix`): NFC, no empty/dot segments, no control characters or backslashes, length limits, reserved first segments (`api`, `s`, `_vela`, `_blobs`, `healthz`, `robots.txt`, `favicon.ico`). Non-canonical API URLs are rejected rather than redirected.
13. In accel mode, velad never sends `Cache-Control` directly (nginx would forward it *and* add the `X-Vela-Cache-Control` copy); the e2e test checks that every header arrives exactly once and nothing internal leaks.

## What counts as a breaking change in API v1

Breaking (needs API v2, i.e. a new `APIVersion` and `/api/v2`):

- removing or renaming an endpoint, query parameter, header, JSON field or error code;
- changing a field's type, an endpoint's success status, or the meaning of an existing value;
- making an optional request field required, or rejecting input that used to be valid;
- changing the URL formats of public objects or signed links (old links in the wild must keep working);
- changing CLI exit codes or the shape of `--json` output.

Not breaking: new endpoints, new optional request fields or headers, new response fields, new error codes for new situations, and new CLI commands or flags.

## Do not

- Add docker compose, Kubernetes manifests or an external database.
- Introduce cgo (the race detector in tests is the only exception).
- Log query strings of signed links, or any key or signature.
- Accept API keys anywhere but the `Authorization` header.
- Let nginx make access decisions or serve `blobs/` outside the internal location.
- Answer 403 where existence must stay hidden (public side, signed links).
- Bypass `api` path validation or the service's prefix checks.
- Add an HTTP endpoint that manages API keys.
- Store blobs under logical paths, or move blob files on rename (renames are database-only).

## Review process

1. **Stage 2 (review):** Astra reads this file and `docs/SPEC.md` and writes `REVIEW.md` at the repository root. It covers conformance to the spec, security, correctness, architecture and comment quality. Suggested format, one section per finding:

   ```markdown
   ## R-001 [severity: high|medium|low] Short title
   - Where: path/to/file.go:123
   - Problem: what is wrong and why it matters (spec section if relevant)
   - Suggestion: how to fix it
   - Status: open
   ```

2. **Stage 3 (fixes):** a coding agent fixes the findings in atomic commits that reference the IDs (`fix(R-001): ...`). It sets each `Status:` to `fixed (<commit>)` or `rejected: <rationale>`. Tests and lint stay green.
3. **Stage 4:** a compact English `README.md` with basics, Getting Started,
   deployment entry points, and a link to `https://docs.xchunk.org/vela`.
   Repository documentation is technical: implementation decisions, contracts,
   limits, and verification. Follow [docs/README.md](docs/README.md); keep
   integration assumptions with `examples/`. The full user manual and website
   are maintained separately and are outside this repository's Stage 4 scope.
