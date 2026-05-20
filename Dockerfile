# syntax=docker/dockerfile:1.7
# ---------- build stage ----------
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum* ./
RUN go mod download || true

# Source.
COPY . .

# Pure-Go SQLite driver (modernc.org/sqlite) means CGO_ENABLED=0 — clean cross compile.
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod tidy && \
    go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/ai-hub ./cmd/ai-hub

# ---------- runtime stage ----------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="AI Hub" \
      org.opencontainers.image.description="Unified OpenAI-compatible gateway for aggregating multiple LLM providers." \
      org.opencontainers.image.source="https://github.com/s12ryt/docker-ai-proxy" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app
COPY --from=builder /out/ai-hub /app/ai-hub

ENV LISTEN=":8080" \
    DB_PATH="/data/ai-hub.db" \
    ENABLE_METRICS="1"

EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot

ENTRYPOINT ["/app/ai-hub"]
