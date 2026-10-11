# Storage, delivery, and recovery

The backend-independent [`content.Store`](../internal/content/content.go)
contract stores immutable bytes under opaque IDs. SQLite holds logical paths,
SHA-256, metadata, keys, signed links, and trash. Every replacement gets a fresh
blob ID; renames change metadata only. One process selects one backend at startup.
Configuration defaults and ranges are in [configuration.md](configuration.md).

## Publication and collection

Filesystem uploads stream into `tmp/`, enforcing limits and hashing bytes.
The file is made readable, fsynced, and published exclusively under
`blobs/<shard>/<id>` with a hard link, then the temporary name is removed and
directories are synced. `tmp/` and `blobs/` must share a filesystem. New files
are 0644, blob directories 0755, temporary directories 0700; the database is
0600. A failed metadata transaction removes the newly published blob.

SQLite uses WAL and `synchronous=NORMAL`, one writer connection with
`BEGIN IMMEDIATE`, and four query-only reader connections. Embedded migrations
run at startup. See [`internal/store`](../internal/store/store.go) and the
[SQL migrations](../internal/store/migrations/) for the schema.

Overwrite, purge, and orphan cleanup queue unreferenced blobs in `blob_gc` in
the same transaction that removes their last reference. Physical deletion waits
for the 15-minute grace period and a FULL WAL checkpoint, then rechecks live and
trash references. This preserves content across rollback after power loss and
allows in-flight requests to finish. Failed deletions stay queued for retry.
The collector runs every five minutes and during janitor passes; janitor handles
object expiry, trash/link retention, and orphan discovery. Backup and janitor
leases renew during long work; losing a lease cancels the operation.

## R2/S3 backend and capacity

R2/S3 streams multipart uploads without staging full files locally. A private
bucket and bucket-scoped Object Read & Write credentials are required. Startup
checks HeadBucket; health probes cache this check for 45 seconds. HeadBucket
cannot establish write permission; an actual upload must verify it. Configure a
bucket lifecycle rule to abort incomplete multipart uploads after one day.
The janitor also aborts old managed uploads; the lifecycle rule covers crashes.

Requests use connection/header deadlines and at most three SDK attempts, with
no total streaming-body deadline. Failed multipart writes are aborted; completed
objects with bad checksums are deleted. Backend errors omit raw SDK messages and
signed URLs. Only metadata remains on local disk in R2 mode. Generic S3 endpoints
use the same backend and configurable signing region; MinIO is tested, while
other providers need their own compatibility verification.

The pinned SDK payload buffers are approximately:

```
part size × (part concurrency + 2) × active uploads
```

This includes the first part and a pool with read-ahead: 128 MiB at defaults.
First-part slice growth creates transient allocations. Reserve at least
`part size × (part concurrency + 4) × active uploads` (192 MiB at defaults),
plus headroom for garbage collection, TLS, SQLite, and caller buffers. This is a
capacity-planning allowance, not an RSS bound. See
[`buffering_test.go`](../internal/r2/buffering_test.go) for the pinned SDK check.

Part size is fixed and uploads are capped at 10,000 parts. Startup rejects
upload/create limits beyond `VELA_R2_PART_SIZE × 10000`; migration preflights
all referenced sizes too. At 8 MiB parts, the ceiling is 78.125 GiB; a 100 GiB
limit needs at least 11 MiB parts and a larger memory allowance.

## Delivery contracts

| Mode | Byte delivery | Required integration |
| --- | --- | --- |
| FS direct (default) | Go streams filesystem contents | Any proxy or none |
| FS accel | Go authorizes; nginx opens an immutable blob | Explicit prefix and matching internal locations with read access |
| R2 proxy (default for R2) | Go streams full content or one byte range | Proxy buffering/temp files disabled |
| R2 redirect | Go authorizes a short-lived presigned GET | Clients can reach the S3 origin |

HEAD and matching conditional responses use metadata without fetching contents.
SHA-256 ETag is the sole content validator; Last-Modified is omitted to avoid
false validation after same-second replacement. Shared public/unlisted downloads
can be cached for five minutes; signed and API downloads use `private, no-store`.
Invalid ranges return 416 with `no-store` and no ETag. R2 proxy supports single
ranges; multi-range requests fall back to a full 200 response.

Accel passes cache, ETag, robots, and referrer policy through internal X-Vela-*
headers. The supplied nginx locations re-emit them exactly once and hide internal
headers. Signed-link error logs are disabled and all access logs omit queries.
The actual example is exercised by [`test/e2e`](../test/e2e/).

Redirects set response type, disposition, and cache policy in the presigned URL;
the redirect itself uses `private, no-store` and `no-referrer`. Grants are capped
at object/link expiry. Near expiry, or for requests with conditional headers,
delivery falls back to proxy so Vela's validators apply. The client strips
Authorization and Referer on redirects. An already issued S3 grant can remain
valid after Vela revocation/visibility changes until its short TTL ends. Direct
presigned uploads and hybrid delivery are not implemented.

## Backup and restoration guarantees

`velad backup DEST_DIR` requires an absent or empty destination. It uses
`VACUUM INTO` for a SQLite snapshot and a renewable backup lease to prevent
content collection throughout the operation. Filesystem backups include every
referenced live/trash blob, using hard links or copies across filesystems.
Sizes are checked; `--verify` additionally checks SHA-256. Missing or damaged
blobs fail the backup, and partial output is removed. Protect backups: they
contain signing keys and private data; the destination is made 0700.

R2 backups contain only SQLite. Bucket contents are neither copied nor verified;
`--verify` is rejected. A consistent metadata/bucket pair requires the serving
process (including janitor/collector) to remain stopped while a one-shot backup
process takes the snapshot and a separate tool copies the matching private
bucket prefix. A metadata snapshot alone is insufficient for disaster recovery.

Restoration also requires Vela to be stopped. Restore `vela.db`, remove stale
`vela.db-wal` and `vela.db-shm`, and restore the matching filesystem blobs or
bucket prefix with the same backend configuration and permissions. Treat
hard-linked backups as immutable and retain backups independently of live data.
Implementation and failure tests live in
[`internal/velad/maintenance.go`](../internal/velad/maintenance.go) and
[`backup_test.go`](../internal/velad/backup_test.go).

## Backend migration invariants

`velad storage migrate --from fs|r2 --to fs|r2` copies referenced live/trash
contents while preserving IDs and SQLite metadata. The backup lease pins contents
and excludes other migrations/backups; it does **not** block uploads or metadata
writes. Operators must quiesce writes throughout migration and switching.
Configure both backends and dedicate the bucket prefix to one Vela store.

Destinations are checked first, including SHA-256 and size; missing copies are
uploaded and read back for verification. Reruns resume even after some source
copies were removed. `--dry-run` makes no content changes. `--delete-source`
cannot be combined with dry-run and starts only after every destination is
verified. Deletion failures may leave partial source cleanup; reruns resume it.
Never remove source contents while that backend still serves requests.

Switch configuration and recreate the container to reload Docker env values;
a restart alone does not reload `--env-file`. Keep source copies until delivery
is verified. Reverse migration follows the same rules and needs sufficient local
disk. See [`internal/velad/storage.go`](../internal/velad/storage.go) and
[`storage_test.go`](../internal/velad/storage_test.go).
