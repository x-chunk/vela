#!/usr/bin/env bash
# Build a pinned test-only MinIO server, run it in Docker, and clean up on exit.
set -euo pipefail
MINIO_VERSION=RELEASE.2025-09-07T16-13-09Z
work=$(mktemp -d)
container="vela-minio-test-$$"
image="vela-minio-test:$$"
cleanup() {
    docker rm -f "$container" >/dev/null 2>&1 || true
    docker image rm "$image" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT
if [[ -n ${MINIO_BINARY:-} ]]; then
    cp "$MINIO_BINARY" "$work/minio"
else
    # MinIO is a test tool, never a dependency of either Vela binary.
    CGO_ENABLED=0 GOBIN="$work" go install "github.com/minio/minio@$MINIO_VERSION"
fi
cat > "$work/Dockerfile" <<'DOCKERFILE'
FROM scratch
COPY minio /minio
ENTRYPOINT ["/minio"]
CMD ["server", "/data", "--console-address", ":9001"]
DOCKERFILE
docker build -q -t "$image" "$work" >/dev/null
docker run -d --rm --name "$container" --tmpfs /data:rw \
    -p 127.0.0.1::9000 \
    -e MINIO_ROOT_USER=vela-test -e MINIO_ROOT_PASSWORD=vela-test-secret \
    "$image" >/dev/null
address=$(docker port "$container" 9000/tcp)
export VELA_TEST_S3_ENDPOINT="http://$address"
ready=false
for ((i=0; i<60; i++)); do
    if curl -fsS "$VELA_TEST_S3_ENDPOINT/minio/health/ready" >/dev/null 2>&1; then
        ready=true
        break
    fi
    if [[ $(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true) != true ]]; then
        docker logs "$container" 2>/dev/null || true
        exit 1
    fi
    sleep 1
done
if [[ $ready != true ]]; then
    docker logs "$container"
    exit 1
fi
CGO_ENABLED=1 go test -race -count=1 ./internal/r2 ./internal/server ./internal/service ./internal/velad
