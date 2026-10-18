# Technical documentation

This directory describes the implementation and contracts maintained with the
code. User guides, CLI tutorials, and deployment walkthroughs belong on
[docs.xchunk.org/vela](https://docs.xchunk.org/vela); that website is maintained
separately. The root [README](../README.md) provides a short introduction and
Getting Started. [Deployment examples](../examples/README.md) document the
settings and assumptions needed to adapt the supplied configurations.

| Reference | Scope | Implementation |
| --- | --- | --- |
| [Architecture and HTTP contracts](architecture.md) | Layers, authorization, API surface, compatibility | `api/`, `client/`, `internal/server/`, `internal/service/` |
| [Directory policies](directories.md) | Inheritance, public filtering, API, cache, and move guarantees | `api/dirs.go`, `internal/service/dirs.go`, `internal/store/dirs.go` |
| [Configuration](configuration.md) | Defaults, precedence, validation, CLI profiles | `internal/config/`, `internal/cli/config/` |
| [Storage and recovery](storage.md) | Publication, collection, delivery, capacity, backup and migration guarantees | `internal/content/`, `internal/blob/`, `internal/r2/`, `internal/store/`, `internal/velad/` |
| [Theme contract](theme.md) | Template files, data, helpers, and browser constraints | `internal/index/`, `internal/server/public.go` |
| [Contributing](../CONTRIBUTING.md) | Toolchain, verification, and change process | `Makefile`, `.github/workflows/` |

## Specifications and historical records

[SPEC.md](SPEC.md) defines the original design and API v1; section 21 records
accepted changes. [SPEC-storage.md](SPEC-storage.md) describes the storage
extension and its implementation decisions. Read those decisions alongside the
original text: some initial proposals were deliberately superseded.
[SPEC-dirs.md](SPEC-dirs.md) describes directory policies and accepted compatibility
decisions. [SPEC-web.md](SPEC-web.md) describes the optional read-only web sign-in.
[OSS-AUDIT.md](OSS-AUDIT.md) is a dated preparation report, not a current release
or deployment status page. Release changes belong in [CHANGELOG.md](../CHANGELOG.md).

## Maintaining documentation

Keep the root README focused on basics and one working Getting Started path.
Add repository references for implementation decisions, extension contracts,
resource limits, and invariants that reviewers need. Link to source definitions
for complete DTOs, errors, flags, and schemas instead of maintaining copies.
Keep operational examples close to their files under `examples/`.

When changing behavior, update its technical reference and relevant examples in
the same change. Record deliberate spec changes in SPEC section 21; update the
changelog for compatibility or deployment changes. Keep links to the website at
its root until actual pages exist. Do not add site tooling or a second user
manual to this repository as part of documentation maintenance.
