# Vela

Vela is a self-hosted file store with a Go server (`velad`) and CLI (`vela`).
It provides a REST API, a public directory index, expiring signed links, and
recoverable trash. SQLite holds metadata; immutable file contents live on the
filesystem or in a private Cloudflare R2 / compatible S3 bucket. Both backends
use the same API and CLI.

The primary user documentation will live at
[docs.xchunk.org/vela](https://docs.xchunk.org/vela). Implementation contracts
and contributor references are maintained in [docs/](docs/README.md).

## Basics

| Visibility | Direct URL | Public index |
| --- | --- | --- |
| `public` | Accessible to anyone | Listed |
| `unlisted` (default) | Accessible to anyone who has the URL | Hidden |
| `private` | Unavailable; use the authenticated API or a signed link | Hidden |

**Unlisted is not access protection.** API keys have read-only, read/write, or
admin scopes and can be restricted to a path prefix. Keys are managed locally
with `velad key`, never through HTTP. Deletion moves files to trash for 14 days
by default; renaming changes direct URLs while keeping signed links attached
to the object.

## Getting Started

For a local filesystem store, install Git, Make, and the Go toolchain specified
in [go.mod](go.mod), then run:

```sh
git clone https://github.com/x-chunk/vela.git
cd vela
make dev
```

This builds both binaries, starts `velad` at `http://127.0.0.1:8080`, stores data
in `.dev-data/`, and prints a fresh admin key. In another terminal, from the
same checkout:

```sh
./bin/vela login --url http://127.0.0.1:8080 --profile dev
# Enter the printed key at the prompt.
./bin/vela --profile dev whoami
./bin/vela --profile dev new docs/hello.txt --content 'Hello from Vela' --public
```

Open [the local index](http://127.0.0.1:8080/) to see the file. Stop the server
with Ctrl+C; `.dev-data/` persists across runs. The CLI stores profiles and keys
in an owner-only configuration. `vela --help` and command-specific `--help`
describe the available operations; `--json` provides machine-readable output.

For a workstation installation, use a CLI archive from
[GitHub Releases](https://github.com/x-chunk/vela/releases), `make install`
from a checkout, or:

```sh
go install go.xchunk.org/vela/cmd/vela@latest
```

## Deployment

[Deployment examples](examples/README.md) cover filesystem direct delivery,
nginx accel, and R2/S3. Set your public origin, prepare the writable data mount
for container UID/GID 65532, and configure TLS and explicit proxy trust. The
default streams files through Go; nginx accel requires explicit configuration.
The release image is [xchunk/vela](https://hub.docker.com/r/xchunk/vela); choose
an explicit version tag, or build locally with `make docker`.

Consult the [configuration reference](docs/configuration.md) for defaults and
the [storage reference](docs/storage.md) for delivery, resource limits, backup,
and migration guarantees. The examples record their integration assumptions;
adapt them to your network, storage, and backup process.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development and verification,
[SECURITY.md](SECURITY.md) to report vulnerabilities, and the
[MIT license](LICENSE) for reuse.
