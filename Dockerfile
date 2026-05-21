# syntax=docker/dockerfile:1.7
# ---------- build stage ----------
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# Copy everything so we have full source for module resolution.
COPY . .

ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=mod
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    set -eux; \
    go mod tidy; \
    go mod download; \
    go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/ai-hub ./cmd/ai-hub; \
    ls -lh /out/ai-hub

# ---------- runtime stage ----------
# Prepare a /data directory owned by the nonroot user (uid/gid 65532 in distroless).
# distroless has no shell so we do the chown in an intermediate alpine stage.
FROM alpine:3.20 AS rootfs
RUN mkdir -p /rootfs/data && chown -R 65532:65532 /rootfs/data

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="AI Hub" \
      org.opencontainers.image.description="Unified OpenAI-compatible gateway for aggregating multiple LLM providers." \
      org.opencontainers.image.source="https://github.com/s12ryt/docker-ai-proxy" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app
COPY --from=builder /out/ai-hub /app/ai-hub
COPY --from=rootfs --chown=65532:65532 /rootfs/data /data

ENV LISTEN=":8080" \
    DB_PATH="/data/ai-hub.db" \
    ENABLE_METRICS="1"

EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot

ENTRYPOINT ["/app/ai-hub"]
