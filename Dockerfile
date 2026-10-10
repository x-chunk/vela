# syntax=docker/dockerfile:1
#
# velad image: a static binary on distroless, running as the nonroot user
# (uid/gid 65532). The data directory is a bind mount of /srv/vela on the
# host, owned by 65532 (see the README).
#
# Keep the Go version in sync with the toolchain line in go.mod and the
# workflows in .github/workflows.

FROM golang:1.27.2-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w \
        -X go.xchunk.org/vela/internal/version.Version=${VERSION} \
        -X go.xchunk.org/vela/internal/version.Commit=${COMMIT} \
        -X go.xchunk.org/vela/internal/version.Date=${DATE}" \
      -o /out/velad ./cmd/velad \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="vela" \
      org.opencontainers.image.description="Vela: self-hosted file store server (velad)" \
      org.opencontainers.image.source="https://github.com/x-chunk/vela" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
# In PATH, so that "docker exec vela velad key create ..." works; distroless
# has no shell to resolve anything else.
COPY --from=build /out/velad /usr/local/bin/velad
# Owned by the runtime user, so the container also works with an anonymous
# volume instead of a bind mount.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV VELA_DATA_DIR=/data \
    VELA_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/velad", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/velad"]
CMD ["serve"]
