#!/usr/bin/env bash
# Example configuration for Vela — not a supported deployment.
# Scenario: fs-direct
# Requires: Docker; prepared data directory; edited private env file
# Replace: store.example.com, certificate paths, /srv/vela, upstream address.
# You are responsible for securing and maintaining your reverse proxy.
set -euo pipefail
# CHANGE ME: export VELA_IMAGE=xchunk/vela:<version> using a published release tag.
: "${VELA_IMAGE:?Set VELA_IMAGE to xchunk/vela with an explicit release version}"
case "$VELA_IMAGE" in
    *:latest|*:dev) echo "Choose an explicit release version, not a moving tag" >&2; exit 1 ;;
    *@sha256:*|*:*) ;;
    *) echo "VELA_IMAGE must include a version tag or digest" >&2; exit 1 ;;
esac
# CHANGE ME if using other paths or a different container name.
data_dir=${VELA_HOST_DATA_DIR:-/srv/vela}
env_file=${VELA_ENV_FILE:-/etc/vela/vela.env}
container_name=${VELA_CONTAINER_NAME:-vela}
# Loopback port for the reverse proxy; the container always listens on 8080.
host_port=${VELA_HOST_PORT:-8080}
case "$host_port" in
    ''|*[!0-9]*) echo "VELA_HOST_PORT must be a port number" >&2; exit 1 ;;
esac
exec docker run -d --name "$container_name" --restart unless-stopped \
    --env-file "$env_file" \
    -p "127.0.0.1:$host_port:8080" --mount "type=bind,src=$data_dir,dst=/data" \
    -e VELA_STORAGE=fs -e VELA_ACCEL_PREFIX= \
    "$VELA_IMAGE"
