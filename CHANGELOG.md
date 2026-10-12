# Changelog

## Unreleased

### Added

- Independent directory policies with inherited index/crawler settings, hidden
  entrances, titles/descriptions, `/api/v1/dirs`, and `vela dir` commands.
- `VELA_ROOT_INDEX` defaults to `on`; an explicit root database rule overrides it.
- Recursive moves atomically carry policies and report `policies_moved`.

- MIT license, contribution guide, and security reporting policy.
- MIT license included in the server image and CLI release archives.
- Separate deployment examples for filesystem direct delivery, explicit nginx
  accel, and private R2/S3 storage; configurable image, paths, and container name.
- `VELA_R2_REGION` (default `auto`) for other S3 signing regions. An explicit
  `VELA_R2_ENDPOINT` no longer requires a Cloudflare account ID.
- CI checks for neutral examples, secret leaks, shell scripts, and nginx syntax.
- Docker Hub release images for both linux/amd64 and linux/arm64, with MIT and
  documentation OCI labels.

### Changed

- **Breaking for custom themes:** breadcrumb URLs can be empty for disabled
  ancestors; render those names as text. Honor `Page.NoIndex` in the layout and
  empty `Page.Parent`. Directory rows add title/description and use visible-entry
  counts once a rule exists. Without rules, legacy HTML/counts remain unchanged.
- Directory `vela url` and `vela open` resolve public index availability on
  servers with directory policies and explain disabled or absent indexes.
  Older API v1 servers retain legacy URL/open behavior and long listings
  without policy annotations.

- **Breaking for existing deployments:** `VELA_ACCEL_PREFIX` now defaults to
  empty (Go streams file contents). Existing nginx accel deployments must set
  `VELA_ACCEL_PREFIX=/_blobs/` explicitly and retain matching internal locations.
- `VELA_TRUSTED_PROXIES` now defaults to loopback only. A host proxy reaching a
  Docker bridge container must have its actual gateway address added explicitly.
- `VELA_SITE_TITLE` now defaults to `Vela`. Set it explicitly to keep branding.
- `vela login` has no built-in server URL. Supply `--url`, `VELA_URL`, an existing
  profile URL, or an interactive answer.
- Deployment examples moved from `deploy/` to `examples/` and now describe the
  operator's responsibility for proxy and TLS configuration.
- Examples and design documents use neutral domains, addresses, and scenarios.
- Release publishing uses `vars.DOCKERHUB_IMAGE`, `secrets.DOCKERHUB_USERNAME`,
  and `secrets.DOCKERHUB_TOKEN`. Set the image variable to `xchunk/vela` for the
  official release; use a repository-scoped access token with Read & Write.

### Removed

- Obsolete private-registry instructions and personal deployment defaults.
