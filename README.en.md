# AI Hub · Multi-Protocol AI Gateway

> A Go-based **multi-protocol AI gateway** that aggregates OpenAI, Anthropic, Gemini, DeepSeek, and any OpenAI-compatible endpoint behind one API. It supports OpenAI / Anthropic / Gemini chat protocol translation, SSE stream translation, OpenAI Responses / embeddings / completions / images / audio pass-through, key rotation, SQLite/MySQL/PostgreSQL observability, and a built-in dashboard.

![License: MIT](https://img.shields.io/badge/license-MIT-22d3ee)
![Go](https://img.shields.io/badge/Go-1.22-7c3aed)
![Docker](https://img.shields.io/badge/docker-multi--arch-06b6d4)

[繁體中文 README](./README.md) · [OpenAPI spec](./openapi.yaml)

## Features

- **Multi-protocol chat API**: OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`, and Gemini `:generateContent` ingress can be translated to each other.
- **OpenAI-compatible endpoints**: `/v1/responses`, `/v1/embeddings`, `/v1/completions`, `/v1/images/*`, `/v1/audio/*` pass through to OpenAI-compatible providers.
- **Providers**: OpenAI, Anthropic, Gemini, DeepSeek, and other OpenAI-compatible services.
- **SSE stream translation**: OpenAI chunks, Anthropic event streams, and Gemini streamGenerateContent text deltas can be translated across chat protocols.
- **Key pool**: multiple API keys per provider with per-provider round-robin rotation.
- **Access control**: admin token for `/api/*`, optional client access-token allowlist for `/v1/*`.
- **Observability**: call logs, token counts, latency, status, recent calls, and summaries stored in SQLite/MySQL/PostgreSQL.
- **Retention**: `DB_RETENTION_DAYS` can automatically delete old call logs.
- **Small deployment artifact**: static Go binary with a distroless Docker image.

## Quick start

### Docker

```bash
docker run -d --name ai-hub \
  -p 8080:8080 \
  -e ADMIN_TOKEN=please-change-me \
  -e ACCESS_TOKENS=client-token-1 \
  -v $(pwd)/data:/data \
  -v $(pwd)/config.json:/app/config.json:ro \
  ghcr.io/s12ryt/docker-ai-proxy:latest
```

### Docker Compose

```bash
cp config.example.json config.json
# Edit config.json and fill provider API keys.
docker compose up -d
```

### From source

```bash
go mod tidy
go build -o ai-hub ./cmd/ai-hub
./ai-hub
```

Open `http://localhost:8080`; the dashboard is available at `/dashboard.html`.

## Configuration

Configuration priority: **environment variables > config.json > defaults**.

| Environment variable | Default | Description |
| --- | --- | --- |
| `LISTEN` | `:8080` | Listen address |
| `ADMIN_TOKEN` | `change-me-admin` | Admin token for dashboard and `/api/*` |
| `ACCESS_TOKENS` | empty | Legacy comma-separated client tokens; prefer dashboard Client management for named/enabled/limited clients; `/v1/*` is rejected when no usable credential is configured |
| `DB_DRIVER` | `sqlite` | `sqlite`, `mysql`, or `postgres` |
| `DB_PATH` | `data/ai-hub.db` | SQLite path |
| `DB_DSN` | empty | MySQL/PostgreSQL DSN |
| `DB_MAX_OPEN_CONNS` | sqlite=1, cloud=10 | `database/sql` max open connections |
| `DB_MAX_IDLE_CONNS` | sqlite=1, cloud=5 | `database/sql` max idle connections |
| `DB_CONN_MAX_LIFETIME` | cloud=`30m` | Go duration string |
| `DB_RETENTION_DAYS` | `0` | Call-log retention days; `0` disables automatic cleanup |
| `CONFIG_PATH` | `config.json` | JSON config path |
| `ENABLE_METRICS` | `1` | Whether call logs are written |

See [`config.example.json`](./config.example.json) for a complete example with OpenAI, Anthropic, Gemini, and DeepSeek providers.

## API

All `/v1/*` endpoints require a bearer token from an enabled dashboard Client or legacy `ACCESS_TOKENS`. If no usable credential is configured, model proxy requests fail closed with `401 Unauthorized` instead of being exposed anonymously.

### Models

```bash
curl -H "Authorization: Bearer $AI_HUB_TOKEN" \
  http://localhost:8080/v1/models
```

### Chat protocols

OpenAI-compatible ingress:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $AI_HUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role":"user","content":"Hello!"}],
    "stream": true
  }'
```

Anthropic-native ingress:

```bash
curl http://localhost:8080/v1/messages \
  -H "Authorization: Bearer $AI_HUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20240620",
    "max_tokens": 1024,
    "messages": [{"role":"user","content":[{"type":"text","text":"Hello!"}]}]
  }'
```

Gemini-native ingress:

```bash
curl http://localhost:8080/v1beta/models/gemini-1.5-pro:generateContent \
  -H "Authorization: Bearer $AI_HUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role":"user","parts":[{"text":"Hello!"}]}]
  }'
```

The gateway resolves the provider from `model` and translates requests/responses when the ingress and upstream protocols differ. The main text-delta streaming path is supported for OpenAI `stream:true`, Anthropic `stream:true`, and Gemini `:streamGenerateContent`.

### OpenAI-compatible endpoints

These endpoints pass through to OpenAI-compatible providers and rewrite `model` to the upstream model name:

| Endpoint | Notes |
| --- | --- |
| `POST /v1/responses` | OpenAI Responses API, including raw SSE pass-through |
| `POST /v1/embeddings` | Embeddings |
| `POST /v1/completions` | Legacy completions, including SSE pass-through |
| `POST /v1/images/*` | Images, e.g. `/v1/images/generations` |
| `POST /v1/audio/*` | Audio, e.g. `/v1/audio/transcriptions`; supports multipart/form-data |

Non-OpenAI-compatible providers return `501 Not Implemented` for these endpoints.

## Admin API

Admin endpoints require `ADMIN_TOKEN`.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/summary?hours=24` | Request/error/latency/token summary |
| `GET /api/providers` | Provider config without API keys |
| `GET /api/access-tokens` | List legacy `/v1/*` client Bearer tokens for the admin dashboard |
| `PUT /api/access-tokens` | Create, edit, or delete legacy Access Tokens; an empty list makes `/v1/*` return 401 unless enabled Clients still exist |
| `GET /api/clients` | List named Clients with enabled state, daily/RPM/concurrent limits, notes, and allowed model allowlists |
| `PUT /api/clients` | Create, edit, or delete Clients; disabled tokens are rejected, and `daily_limit` / `rpm_limit` / `concurrent_limit` / `allowed_models` are enforced on `/v1/*` |
| `GET /api/recent?limit=100` | Recent calls |
| `GET /api/runtime` | Go runtime and DB connection pool stats |
| `POST /api/reload` | Reload `config.json` |
| `GET /healthz` | Public health check |

## Development

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dev.ps1 -Task check
```

Available tasks: `fmt`, `test`, `vet`, `check`, `all`. The script uses `go` from PATH, or falls back to the portable Go path under `%TEMP%\opencode\go\bin\go.exe`.

## Security notes

- Change `ADMIN_TOKEN` before production use.
- Before exposing the service publicly, create at least one enabled dashboard Client or keep legacy `ACCESS_TOKENS`; with no usable credential, `/v1/*` returns `401` instead of allowing anonymous model proxy access. Client `daily_limit` is enforced per UTC day using logged calls, `rpm_limit` is enforced over the most recent minute, `concurrent_limit` caps in-flight requests/SSE streams, and `allowed_models` limits usable models plus filters `/v1/models`.
- Put the service behind TLS in production.
- Call logs contain metadata but not prompt content. Use `DB_RETENTION_DAYS` for automatic log cleanup, or keep it at `0` and manage retention manually.

## License

MIT
