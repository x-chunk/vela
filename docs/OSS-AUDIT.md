# Open-source preparation audit

Audit date: 2026-10-11. Scope: tracked source, hidden files, deployment examples,
embedded theme, design documents, 128 locally available commits, and local image
metadata. History is retained. No release or external account settings were
changed. The supplemental open-source specification was not present locally;
the supplied depersonalization document and existing specifications informed
this implementation.

| Original location | Category | Finding | Resolution |
| --- | --- | --- | --- |
| `AGENTS.md:7`, `docs/SPEC.md:9,19`, `docs/SPEC-storage.md:9` | Deployment identity | Project purpose described a particular domain, legacy tools, and server capacity | Neutral project description and storage rationale |
| `internal/config/config.go:100,101,109` | Defaults | nginx accel, Docker gateway trust, and index branding assumed one deployment | Direct delivery, loopback trust, and Vela title |
| `internal/cli/config/config.go:28`, `internal/cli/account.go:53` | CLI | Login offered a particular server URL | Explicit URL or saved profile; clear error for missing input |
| `deploy/nginx/`, `deploy/vela.env.example`, `README.md:40` | Examples | One nginx setup and moving image tag | Three neutral scenarios under `examples/`, explicit image versions, responsibility notes |
| `api/`, `client/`, service/store/CLI tests, e2e certificate fixture | Fixtures and documentation | Domain and education-specific examples; nonreserved test addresses | Reserved example domains, documentation addresses, generic paths and notes |
| `.github/workflows/release.yml` | Distribution | Image namespace embedded in workflow; amd64 only | Image variable, access-token secrets, amd64/arm64, OCI license/docs labels |
| Repository root | License and contributions | No license, contribution guide, or private security-reporting instructions | MIT chosen by maintainer; contribution guide and security policy |
| `internal/config/storage.go`, `internal/r2/r2.go` | Portability | Cloudflare account required even with explicit endpoint; signing region fixed | Optional account for explicit S3 endpoint and configurable signing region |
| CI | Regression protection | No neutral-source guard or secret/example checks | Source guard with negative tests; Gitleaks, ShellCheck, and nginx validation |

## Git history and credentials

Personal deployment references occur in 17 historical file paths. These are
nonsecret details; history was not rewritten. A separate credential-pattern
pass found no candidate credentials. Gitleaks v8.30.0 examined all available
history (125 content commits reported by the scanner) and initially flagged
three values: a known SHA-256 of `hello world`, a deterministic link ID in a
signature test, and a truncated example key in the original spec. Each was
reviewed as a false positive and excluded with an exact historical fingerprint
in `.gitleaksignore`; current tests use explicit `gitleaks:allow` comments.
The resulting history and current-source scans report no leaks. No broad
path exclusions or actual credentials were added to the allowlist.

## Images and external settings

Local cached images are development builds, not proof of current Docker Hub
tags. The source Dockerfile had neutral image ENV settings; former deployment
defaults were compiled into the server. The new image uses neutral defaults
and carries the MIT license. Public GitHub, Docker Hub, and documentation
metadata could not be retrieved through the web tool, and this checkout has no
Git remote configured. Anonymous lookup of `xchunk/vela:latest` also returned access denied (which
can indicate an absent repository/tag or a private image). Published tag
contents and external settings therefore remain unverified; confirm the image
namespace and visibility before publishing.

Before publishing, the maintainer should set `vars.DOCKERHUB_IMAGE=xchunk/vela`,
`secrets.DOCKERHUB_USERNAME`, and a repository-scoped `secrets.DOCKERHUB_TOKEN`.
Review GitHub description, website, topics and private vulnerability reporting;
review Docker Hub Overview, short description, and old tags. Update deployment,
configuration-default, and image-publication pages on the documentation site.
Choose the release version after reviewing the Unreleased changelog. Publishing
or deleting existing tags is outside this local change.

## Local validation

The race test suite, lint, nginx e2e, and S3 contracts/delivery/migration against
MinIO pass. All three nginx scenarios pass `nginx -t` in `nginx:stable`;
ShellCheck v0.10.0 passes for example and CI scripts. The source guard passes,
including synthetic negative tests for personal domains, private-registry text,
home directories, and nonreserved addresses. Docker builds succeed for amd64
and arm64 using native Go cross-compilation. An actual fs-direct example run
with isolated temporary data verified health, authentication, all visibilities,
byte comparisons, Range/HEAD/304, signed links, and query-free application logs.
Temporary test containers and data were removed. ARM64 was built, not executed.
GitHub-hosted CI and a production Cloudflare bucket were not exercised locally.

All six CLI release archives build and contain the MIT license; checksums
were verified. The host lacks `zip`, so Windows archive verification used a
temporary Python standard-library zip adapter. CI uses its installed zip tool.
