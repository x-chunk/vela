#!/usr/bin/env bash
# Validate every complete nginx scenario with temporary TLS certificates,
# and the Caddy example.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/certs" "$work/blobs"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj /CN=store.example.com \
    -keyout "$work/certs/privkey.pem" -out "$work/certs/fullchain.pem" >/dev/null 2>&1
for scenario in fs-direct fs-accel r2; do
    docker run --rm --network none \
        -v "$root/examples/nginx/$scenario.conf:/etc/nginx/conf.d/default.conf:ro" \
        -v "$root/examples/nginx:/etc/nginx/snippets:ro" \
        -v "$work/certs:/etc/letsencrypt/live/store.example.com:ro" \
        -v "$work/blobs:/srv/vela/blobs:ro" \
        nginx:stable nginx -t
done
docker run --rm --network none \
    -v "$root/examples/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" \
    caddy:2.10.2 caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
