# Vela

Vela is a self-hosted file store with a Go server (`velad`), a CLI (`vela`), a
public index, private files, expiring signed links, and a recoverable trash.
SQLite owns metadata and access decisions. Immutable contents live either on
the local filesystem or in a private Cloudflare R2 bucket. One server image
supports both backends; the CLI and API v1 do not depend on the storage choice.

## Build and test

Use the Go toolchain from `go.mod`.

```sh
make build       # bin/vela and bin/velad
make test        # all tests with the race detector
make lint
make test-e2e    # actual nginx delivery and secret-free logs; requires Docker
make test-s3     # shared FS/S3 contracts, R2 HTTP delivery and migration
make dev        # local filesystem store, direct delivery, fresh admin key
```

`make test-s3` requires Docker, curl, and Go. It builds the pinned test-only
MinIO release from source and runs it in a temporary Docker container. This
avoids depending on the availability of MinIO's published images. Its buckets,
container, image, and temporary files are cleaned up. To reuse a previously
built static binary, set `MINIO_BINARY=/absolute/path/minio`. To use an existing
MinIO with credentials `vela-test` / `vela-test-secret`, run:

```sh
VELA_TEST_S3_ENDPOINT=http://127.0.0.1:9000 make test
```

Tests create isolated buckets. Never point these tests at a production bucket.
The shared contract checks full and ranged reads, empty files, immutable IDs,
size and checksum failures, reader failures, cancellation, and orphan-free
cleanup. HTTP tests cover full downloads, suffix ranges, 416, HEAD, ETag/304,
redirect policy, and signed-link lifetime. MinIO tests do not replace a real
R2 smoke test.

## Deploy the server

The server image runs as UID/GID 65532. nginx terminates TLS; bind the server
port only on loopback. Use `deploy/nginx/store.xchunk.org.conf` and its included
`vela.conf`, adjusting certificate paths, hostnames, and the upstream address.
The query-free access log and disabled signed-link error logs are necessary
because a signed URL is a credential.

```sh
sudo install -d -m 755 -o 65532 -g 65532 /srv/vela
sudo install -d -m 700 /etc/vela
sudo install -m 600 deploy/vela.env.example /etc/vela/vela.env
# Edit VELA_PUBLIC_URL and the backend settings in the env file.
docker run -d --name vela --restart unless-stopped \
  --env-file /etc/vela/vela.env \
  -p 127.0.0.1:8080:8080 -v /srv/vela:/data \
  xchunk/vela:latest

docker logs vela
docker exec vela velad healthcheck
docker exec vela velad key create --name workstation --scope admin
```

The final command prints the API key once, to stdout. Keep it private. Keys
can only be created or revoked by administrative `velad key` commands; there
is no HTTP endpoint for managing keys. To create a key limited to `edu/`, use
`velad key create --name edu-publish --scope rw --prefix edu/`.
Check `velad key --help` for listing and revoking keys.

In filesystem mode (`VELA_STORAGE=fs`, the default), mount `/srv/vela/blobs`
where nginx's internal `/_blobs/` locations expect it. The default
`VELA_ACCEL_PREFIX=/_blobs/` enables nginx delivery. Set it to an empty value
for direct delivery without nginx. Files are published durably and exclusively
under `blobs/<last-four-ID-characters-sharded>/<ID>`, never under logical paths.

## Core configuration

Core settings accept the corresponding `velad serve` flag, with precedence
flag → environment → default. Administrative commands also accept data-directory
and logging flags. An explicitly empty environment value counts as set.

| Variable | Default | Purpose |
| --- | --- | --- |
| `VELA_LISTEN` | `:8080` | Server listen address |
| `VELA_DATA_DIR` | `/data` | SQLite, temporary files, and FS blob directory |
| `VELA_PUBLIC_URL` | required to serve | Public HTTP(S) origin without a path |
| `VELA_ACCEL_PREFIX` | `/_blobs/` | nginx internal prefix; empty for direct/R2 delivery |
| `VELA_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128,172.17.0.1/32` | Proxies permitted to supply forwarded IP and request ID; match your Docker bridge |
| `VELA_MAX_UPLOAD` | `2GiB` | Streaming upload limit; match nginx `client_max_body_size` |
| `VELA_MAX_CREATE` | `1MiB` | Inline creation limit |
| `VELA_LINK_DEFAULT_TTL` | `24h` | Default signed-link TTL |
| `VELA_LINK_MAX_TTL` | `30d` | Maximum signed-link TTL |
| `VELA_LINK_RETENTION` | `7d` | Retention of expired/revoked link records |
| `VELA_TRASH_RETENTION` | `14d` | Trash retention |
| `VELA_JANITOR_INTERVAL` | `24h` | Cleanup interval |
| `VELA_SITE_TITLE` | `store.xchunk.org` | Public index title |
| `VELA_THEME_DIR` | empty | Optional index theme overrides |
| `VELA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `VELA_LOG_FORMAT` | `json` | `json` or `text` |
| `VELA_DEFAULT_VISIBILITY` | `unlisted` | Visibility when creation omits it |
| `VELA_ROBOTS` | `deny` | `deny` or `allow` for robots.txt |

Use `velad janitor run` for an immediate cleanup and `velad signing rotate` to
rotate the HMAC signing key while keeping existing links valid. Use
`velad signing rotate --revoke-all` for an emergency invalidation of every old
signing key. API keys and signing keys are different credentials.

## Configure Cloudflare R2

1. Create a bucket such as `vela-store`, using a location hint near your VPS.
2. Disable public access and `r2.dev`. Do not attach a custom domain to the
   bucket: access must pass through Vela.
3. Add a lifecycle rule to abort incomplete multipart uploads after one day.
4. In **R2 → Manage API tokens → Create**, choose **Object Read & Write** and
   restrict the token to this bucket. It needs no account-management rights.
5. Save the Access Key ID and Secret Access Key in `/etc/vela/vela.env` (0600).

A minimal R2 env file contains:

```dotenv
VELA_PUBLIC_URL=https://store.xchunk.org
VELA_STORAGE=r2
VELA_ACCEL_PREFIX=
VELA_R2_ACCOUNT_ID=your-account-id
VELA_R2_BUCKET=vela-store
VELA_R2_ACCESS_KEY_ID=your-access-key-id
VELA_R2_SECRET_ACCESS_KEY=your-secret-access-key
```

`VELA_ACCEL_PREFIX` must be explicitly empty in R2 mode; `velad serve` rejects
R2 with a nonempty accel prefix. Startup verifies bucket access with HeadBucket
and fails before listening if credentials, bucket, or network are wrong.
Health probes cache this check for 45 seconds. HeadBucket checks reachability
and bucket access; it cannot prove that the token has write permission. The
first smoke upload verifies the configured Object Read & Write permission.

R2 settings are environment-only. Credentials are static: the server never
reads `AWS_*`, `~/.aws`, or instance metadata. The region is `auto`. Ordinary
requests have connection/header timeouts and at most three SDK attempts; the
streaming body has no total timeout. Errors printed or logged by the backend
omit raw SDK messages and signed request URLs.

| Variable | Default | Purpose |
| --- | --- | --- |
| `VELA_STORAGE` | `fs` | `fs` or `r2` |
| `VELA_R2_ACCOUNT_ID` | required for R2 | Cloudflare account ID |
| `VELA_R2_ENDPOINT` | `https://<account>.r2.cloudflarestorage.com` | Bare S3 origin; set `https://<account>.eu.r2.cloudflarestorage.com` for EU jurisdiction |
| `VELA_R2_BUCKET` | required for R2 | Private bucket |
| `VELA_R2_PREFIX` | `blobs/` | Relative object prefix, ending in `/` |
| `VELA_R2_ACCESS_KEY_ID` | required for R2 | Bucket-scoped Access Key ID |
| `VELA_R2_SECRET_ACCESS_KEY` | required for R2 | Bucket-scoped secret |
| `VELA_R2_DELIVERY` | `proxy` | `proxy` or `redirect` |
| `VELA_R2_PRESIGN_TTL` | `5m` | Redirect grant lifetime, 1 second to 1 hour |
| `VELA_R2_PART_SIZE` | `8MiB` | Multipart part size, 5–64 MiB |
| `VELA_R2_UPLOAD_CONCURRENCY` | `2` | Parallel parts per upload, 1–16 |
| `VELA_MAX_CONCURRENT_UPLOADS` | `4` | Active uploads for either backend; excess requests wait with cancellation |
| `VELA_R2_PATH_STYLE` | `true` | Path-style addressing, also used by MinIO |

Uploads stream CLI → nginx → Go → R2, without a temporary disk file. The pinned
SDK buffers **part size × (part concurrency + 2) × active uploads** of payload:
one separate first part plus a pool of `concurrency + 1` parts per upload,
including read-ahead. At the defaults this is **128 MiB** across four uploads.
The first part uses `io.ReadAll`, whose growing slice has spare capacity and
transient old/new allocations. For capacity planning, reserve at least
**part size × (part concurrency + 4) × active uploads** for upload buffers
(**192 MiB** at the defaults), then add headroom for Go garbage collection,
TLS, requests, SQLite, and caller-owned input buffers. This is a planning
allowance, not a process RSS limit; freed slices may remain until GC runs.
Reduce `VELA_MAX_CONCURRENT_UPLOADS` on a small VPS. Size
limits and SHA-256 checks remain enforced. Failed multipart uploads are
aborted, including after request cancellation; completed objects with a bad
checksum are deleted. The daily janitor finds orphan blobs and aborts managed
multipart uploads older than 24 hours. The bucket lifecycle rule covers crashes.

R2 uploads use a fixed part size and at most
[10,000 parts](https://developers.cloudflare.com/r2/platform/limits/).
`VELA_MAX_UPLOAD` and `VELA_MAX_CREATE` must fit within
`VELA_R2_PART_SIZE × 10000`; startup rejects larger limits. With 8 MiB parts
the maximum supported size is **78.125 GiB**. For example, a 100 GiB upload
limit needs at least 11 MiB parts; increasing the part size also increases
the memory allowance above. Migration checks every destination blob size
before reading or copying any blob, including on `--dry-run`.

### Delivery modes

`proxy` streams the authorized byte range through Go. nginx disables response
buffering, sets `proxy_max_temp_file_size 0`, and permits 600-second upstream
idle periods on API, public, and signed-link routes, so downloads do not consume
VPS disk. Full downloads and single ranges work; multi-range requests receive
one complete 200 response. Invalid or unsatisfiable ranges receive 416.
Metadata, HEAD, and ETag/304 responses come from SQLite. SHA-256 ETag is the
sole validator: Last-Modified is deliberately omitted because its precision
can falsely validate a file replaced twice in one second.

`redirect` authorizes in Go and returns 302 to a short-lived presigned R2 GET.
The signed URL sets the response content type, disposition (including UTF-8
filename), and cache policy. The redirect itself uses `private, no-store` and
`no-referrer`. Grants never outlive the object expiry or the original Vela
signed link. Near expiry, delivery falls back to proxy.

The R2 hostname appears in downloads/browser navigation. An issued grant can
still work until its TTL after a visibility change or link revocation; keep
the TTL short. Public URLs also lose some browser caching efficiency through
the uncached redirect. Cloudflare's [presigned URL documentation](https://developers.cloudflare.com/r2/api/s3/presigned-urls/)
states that presigned URLs use the S3 endpoint and cannot use custom bucket
domains. Proxy is the default; hybrid delivery and direct presigned uploads
are outside this release.

## Migrate between backends

Configure both the local data directory and the R2 settings, regardless of
which backend currently serves traffic. Keep the bucket prefix dedicated to
one Vela store. Stop writes for the entire migration and switch; this command
keeps garbage collection paused but does not prevent uploads or metadata changes.

```sh
docker exec vela velad storage migrate --from fs --to r2 --dry-run
docker exec vela velad storage migrate --from fs --to r2
```

Migration includes live objects and trash, preserves blob IDs, and never
changes SQLite metadata. It verifies sources for missing copies, uploads them, then
reads back and verifies their SHA-256 and size. Existing destinations are also
verified before skipping, so reruns safely resume after interruption. A corrupt
destination causes failure without deleting the good source. Progress and
copied/skipped/failed counts are printed to stdout; any failure exits nonzero.

Then set `VELA_STORAGE=r2`, clear `VELA_ACCEL_PREFIX`, recreate the container
with the updated env file, and check downloads and `vela whoami`. Restarting
an existing container does **not** reload Docker's `--env-file` values. Keep
the original filesystem contents until the new deployment is verified. An
optional later pass removes the old source:

```sh
docker exec vela velad storage migrate --from fs --to r2 --delete-source
```

`--delete-source` starts deletion only after all destination blobs are verified;
keep writes stopped and never use it while the source still serves requests.
Deletion errors can leave some verified source copies already removed. Reruns
verify the existing destinations and safely resume source deletion. `--dry-run` and `--delete-source` cannot be combined.

The reverse direction is identical:

```sh
docker exec vela velad storage migrate --from r2 --to fs
```

Ensure the VPS has enough disk, switch to `VELA_STORAGE=fs`, restore the accel
prefix if using nginx, and verify before removing R2 source contents.

## Back up and restore

`docker exec vela velad backup /data-backup` writes a consistent SQLite snapshot
into an empty directory (mount it into the container). Filesystem mode also
links/copies every referenced live/trash blob; `--verify` checks SHA-256.
The backup lock pins contents against garbage collection for the whole pass.

In R2 mode the command backs up **only SQLite**, avoiding a full download to
the VPS. `--verify` is rejected for this metadata-only backup. Back up the
private bucket separately using a private destination and an R2 remote configured
according to Cloudflare's [rclone guide](https://developers.cloudflare.com/r2/examples/rclone/):

```sh
rclone copy r2:vela-store /secure-backups/vela-r2-bucket
```

For a consistent database/bucket pair, stop the Vela container. Run the same
image as a one-shot `docker run --rm ... backup /backup` command with the data
directory, env file, and an empty backup directory mounted; then copy the
bucket and start Vela again. Prepare the backup mount with owner 65532 and
mode 0700. Keeping the serving container stopped also stops its janitor and
collector. Prefer `copy` to a destructive
sync for retained backups. Protect rclone credentials and backup contents,
which include private files. To restore, stop Vela, restore `vela.db` (remove
old `vela.db-wal` and `vela.db-shm`), restore the bucket prefix or filesystem
blobs from the matching backup, and start with the same backend settings.

## CLI user manual

Install `bin/vela` on your workstation, then log in with the key from deployment:

```sh
vela login --url https://store.xchunk.org --profile personal
export VELA_PROFILE=personal
vela whoami
vela ls
vela put ./report.pdf docs/report.pdf --public
vela stat docs/report.pdf
vela get docs/report.pdf report-copy.pdf
vela mv docs/report.pdf docs/final.pdf
vela rm docs/final.pdf
vela trash ls
```

The login command reads the key without echo; a piped key is also supported.
Profiles are stored in an owner-only config, including protected Windows
permissions. Use `--profile` to select a store and `--json` for machine-readable
output. See `vela --help` and each command's `--help` for all flags.

`public` objects appear in the index and have direct URLs. `unlisted` objects
are reachable by URL but absent from the index. `private` objects require an
API key or signed link; missing, private, expired, and trashed files all give
404 on the public side. Moving a file changes only metadata; no blob is moved.
Deleting normally moves it to the trash, retained for 14 days by default.

```sh
vela put -r ./materials edu/ --public
vela new notes/hello.txt --content 'Hello' --private
vela share notes/hello.txt --ttl 2h
vela links ls
vela trash restore docs/final.pdf
```

Recursive upload places a directory's contents under the remote prefix. Links
are expiring grants to one object; revoking a Vela link takes effect on the
next authorization check, subject to an already issued R2 redirect grant's
remaining TTL. Purge is permanent and requires an admin key. The server defers
physical deletion of replaced/purged contents through a durability checkpoint
and a grace period, then collects them automatically.

## Real R2 smoke test

Use an isolated private bucket/prefix and a bucket-scoped token. Confirm startup
and `vela whoami`, upload a small file and a file larger than 8 MiB, and compare
downloaded SHA-256. Check proxy full/Range/HEAD/304 requests, interrupted uploads,
checksum mismatch, and janitor cleanup. Test redirect downloads with a Unicode
filename and a short link TTL; check the response overrides and expiration.
Finally migrate FS → R2 → FS, include a trash entry, and compare downloaded
hashes before deleting any source. Do not publish bucket access or log signed
URLs during this test.

The specifications are [docs/SPEC.md](docs/SPEC.md) and
[docs/SPEC-storage.md](docs/SPEC-storage.md). Deliberate implementation choices
and deviations are recorded in SPEC section 21.
