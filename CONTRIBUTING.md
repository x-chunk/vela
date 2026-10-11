# Contributing to Vela

Issues and pull requests are welcome at https://github.com/x-chunk/vela.
Describe the scenario, expected behavior, and a minimal reproduction. Remove
API keys, signed URLs, credentials, and personal deployment information.
Report security vulnerabilities as described in [SECURITY.md](SECURITY.md).

Use the Go toolchain in `go.mod`. Start with `make dev` for an isolated local
filesystem store. Read [AGENTS.md](AGENTS.md) for layering and security rules,
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

Use one logical change per commit and describe the problem, resulting behavior,
and validation in the pull request. Document deliberate spec changes in §21
of the main spec. Deployment examples are illustrative; do not introduce
operator-specific addresses, paths, networks, or credentials into defaults.

Contributions are licensed under the project's [MIT license](LICENSE).
