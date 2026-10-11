# Contributing to Vela

Issues and pull requests are welcome at https://github.com/x-chunk/vela.
Describe the scenario, expected behavior, and a minimal reproduction. Remove
API keys, signed URLs, credentials, and personal deployment information.
Report security vulnerabilities as described in [SECURITY.md](SECURITY.md).

Use the Go toolchain in `go.mod`. Start with `make dev` for an isolated local
filesystem store. Read [AGENTS.md](AGENTS.md) for layering and security rules,
[technical references](docs/README.md) for maintained implementation contracts,
and [docs/SPEC.md](docs/SPEC.md) for API v1. Keep dependencies minimal and
preserve the API and CLI JSON contracts. New optional configuration must have
neutral defaults, validation, documentation, and relevant regression coverage.

Before submitting, run:

```sh
make fmt
make test
make lint
make check-neutrality # Python 3; no external Python packages
```

Changes to delivery, headers, or nginx examples also require `make test-e2e`
and `make validate-examples` (Docker and OpenSSL). Changes to R2/S3 require
`make test-s3`. Run `shellcheck examples/docker/*.sh scripts/*.sh` when editing
scripts. CI checks secrets across Git history; never commit real credentials.

## Verification boundaries

| Suite | Coverage | Prerequisites |
| --- | --- | --- |
| `make test` | Race detector; API/client/CLI, real SQLite/filesystem services, HTTP, import rules | Go and a C compiler for the race detector |
| `make lint` | Formatting and static analysis with the pinned golangci-lint | Go |
| `make test-e2e` | Real nginx accel/proxy delivery, validators, headers, cache, and query-free logs | Docker on Linux with host networking |
| `make test-s3` | Shared blob contract, R2 HTTP delivery, migration, and failure cleanup | Docker, curl, and Go |
| `make validate-examples` | Syntax and integration of all supplied nginx scenarios | Docker and OpenSSL |

`make test-s3` builds the pinned test-only MinIO release from source and runs
it in a temporary Docker container. Buckets, container, image, and temporary
files are cleaned up. Set `MINIO_BINARY=/absolute/path/minio` to reuse a static
binary. Alternatively, `VELA_TEST_S3_ENDPOINT=http://127.0.0.1:9000 make test`
uses an existing MinIO with test credentials `vela-test` / `vela-test-secret`.
Tests create isolated buckets; use a dedicated test service, never production.

The shared contract checks full/ranged reads, empty files, immutable IDs, size
and checksum failures, cancellation, and cleanup. HTTP tests cover HEAD, ranges,
416, ETag/304, conditional redirect policy, and signed-link lifetime. MinIO does
not establish compatibility with every S3 provider. For backend changes, also
smoke-test an isolated real R2 bucket/prefix: small and multipart uploads,
downloaded SHA-256, proxy/redirect conditions and expiry, interrupted/checksum
failures, janitor cleanup, and FS → R2 → FS migration including trash. Keep
credentials and signed URLs out of diagnostics.

## Distribution and documentation

`make build` produces cgo-free binaries; `make dist` creates CLI archives for
six platforms with checksums (requires zip). The public module is
`go.xchunk.org/vela`. Its vanity host must serve a `go-import` meta tag pointing
to `https://github.com/x-chunk/vela` for `?go-get=1`, including command subpaths.
That host is maintained outside this repository; see [SPEC §15.4](docs/SPEC.md#154-vanity-import).

Keep the README short and technical contracts with their code references in
`docs/`. User guides belong on [docs.xchunk.org/vela](https://docs.xchunk.org/vela),
maintained separately. Follow the [documentation policy](docs/README.md#maintaining-documentation)
when adding or relocating material.

Use one logical change per commit and describe the problem, resulting behavior,
and validation in the pull request. Document deliberate spec changes in §21
of the main spec. Deployment examples are illustrative; do not introduce
operator-specific addresses, paths, networks, or credentials into defaults.

Contributions are licensed under the project's [MIT license](LICENSE).
