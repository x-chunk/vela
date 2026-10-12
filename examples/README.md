# Deployment examples

These configurations are starting points, not supported deployments. Vela
supports `velad` and its `VELA_*` configuration; you are responsible for your
reverse proxy, TLS certificates, firewall, backups, and updates. Replace all
`CHANGE ME` values and validate the result for your own environment.

| Scenario | Environment | Docker script | nginx configuration | Choose when |
| --- | --- | --- | --- | --- |
| fs-direct | `env/fs.env.example` | `docker/run-fs.sh` | `nginx/fs-direct.conf` | Recommended start: Go streams contents; any proxy works |
| fs-accel | `env/fs-accel.env.example` | `docker/run-fs-accel.sh` | `nginx/fs-accel.conf` | nginx streams local blobs; it must be able to read the blob directory |
| r2 | `env/r2.env.example` | `docker/run-r2.sh` | `nginx/r2.conf` | Private cloud bucket; proxy or short-lived redirect delivery |

The fs-direct and R2 scenarios need no special proxy feature. The accel
scenario requires `VELA_ACCEL_PREFIX=/_blobs/` and the two internal locations
in `nginx/accel-locations.conf`. A mismatched setup can produce empty downloads
or proxy 404s. Startup logs report the active delivery mode. `velad doctor` is
not implemented; use the checks below.

## Prepare the container

Create `/srv/vela` owned by UID/GID 65532, and copy the selected env file to
`/etc/vela/vela.env` with mode 0600 in a private directory. Fill in the public
origin and, for R2, bucket and credentials. Secret fields are intentionally
empty. Do not commit your populated env file.

Set `VELA_IMAGE='xchunk/vela:<version>'` to a published release tag (replace
`<version>`), then run the selected script from the repository. The image is
public and supports amd64 and arm64. The scripts bind only
`127.0.0.1:8080:8080`; ensure a firewall and TLS proxy protect remote access.
Override `VELA_HOST_DATA_DIR`, `VELA_ENV_FILE`, `VELA_CONTAINER_NAME`, and
`VELA_HOST_PORT` (default 8080) as needed; a different host port must match the
proxy upstream address. The scripts set the selected backend and delivery prefix explicitly.
Use `docker logs vela` and `docker exec vela velad healthcheck` after startup.

By default only loopback may supply forwarding headers. With a host proxy and
Docker bridge networking, the peer seen inside the container is usually the
bridge gateway. Inspect it with `docker network inspect bridge`; explicitly
add that actual address to `VELA_TRUSTED_PROXIES`. `172.17.0.1/32` in the env
comments is an example, never a universal default. On Linux, adapting the
command to `--network host` (remove `-p`) and setting
`VELA_LISTEN=127.0.0.1:8080` keeps the proxy on loopback. Never trust an entire
shared Docker subnet merely to make forwarding headers work.

R2/S3 stores only metadata on local disk. Use a private bucket and a
bucket-scoped Object Read & Write credential. For another S3 service, supply
`VELA_R2_ENDPOINT` and its `VELA_R2_REGION`; the account ID may stay empty.
`VELA_R2_PATH_STYLE` controls bucket addressing. Test the service's multipart
and presigned-URL behavior before production use. See the
[storage reference](../docs/storage.md) for buffer sizing, lifecycle rules,
migration, and paired database/bucket backups.

## Adapt nginx

Install `nginx/common.conf` and, for accel, `nginx/accel-locations.conf` in
`/etc/nginx/snippets/`. Include exactly one scenario configuration in the
`http` context (for example as `/etc/nginx/conf.d/vela.conf`). The snippets
must not be included directly in `http` or copied into `conf.d` themselves.
Replace `store.example.com`, upstream address, certificate paths, and blob
path. The illustrated Let's Encrypt paths do not prescribe how to obtain
certificates. Prepare certificates using your chosen tooling before enabling
TLS. If using webroot challenges, create the chosen challenge directory.

Keep `client_max_body_size` synchronized with `VELA_MAX_UPLOAD`. Each example
streams API request bodies and file responses. Query-free access logging is
used in both HTTP and HTTPS servers. Signed-link error logs are disabled;
nginx's error log always includes the full request URI. Do not replace these
with a default log format or add a proxy layer that logs signed queries.
The HTTP redirect preserves signed queries so that links continue to work.

Run `nginx -t` before reloading. `make validate-examples` checks all three
unmodified examples in `nginx:stable` with temporary self-signed certificates;
`make test-e2e` verifies real accel delivery, headers, and logs.

## Or use Caddy

`caddy/Caddyfile` covers fs-direct and r2 (accel needs nginx). Caddy obtains
and renews certificates itself, so no certbot or certificate paths are
needed; ports 80 and 443 must reach it. Replace `store.example.com`, keep
`max_size` for `/api/*` in sync with `VELA_MAX_UPLOAD`, and adjust the upstream
if you changed `VELA_HOST_PORT`. The file filters queries and request headers
out of both access and handler error logs; keep those filters if you change
logging. Run `caddy validate` before reloading.

## Check after deployment

1. `curl -I https://store.example.com/healthz` returns 200.
2. Create a key: `docker exec vela velad key create --name workstation --scope admin`.
   Save it with `vela login --url https://store.example.com`, then run `vela whoami`.
3. Upload a small test file in each visibility with `vela put FILE PATH` and
   `--public`, `--unlisted`, or `--private`. Download and compare the bytes.
4. Inspect public and unlisted URLs with `curl -I`: verify Content-Type,
   Content-Disposition, one SHA-256 ETag, and X-Robots-Tag for unlisted files.
   Private direct URLs must return 404 without a key.
5. Create a short-lived private link with `vela share PATH --ttl 5m`. Check its
   bytes and `Cache-Control: private, no-store`; use Range and HEAD requests too.
   Treat the URL as a credential; do not paste it into logs or tickets.
6. Check that logs omit signatures and query strings and that the client IP is
   correct. For accel, verify no internal X-Vela-* headers reach clients and
   direct requests to `/_blobs/` return 404.
7. Delete the test files, confirm they are in the trash, and test restoration.
   Make and restore a backup before storing important data.
