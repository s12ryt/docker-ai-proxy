# AI Hub · 多模型聚合分發網關

> 用 Go 撰寫的 **多協定 AI 模型聚合網關**。把 OpenAI、Anthropic、Gemini、DeepSeek 與任意 OpenAI 相容端點整合進**單一 API**；支援 OpenAI / Anthropic / Gemini chat 協定互轉、SSE 串流翻譯、OpenAI Responses/embeddings/completions/images/audio 端點轉發，內建密鑰池、SQLite/MySQL/PostgreSQL 觀測、漂亮的暗黑玻璃擬態控制台 — 單檔靜態二進制，Docker 鏡像 < 25 MB。

![License: MIT](https://img.shields.io/badge/license-MIT-22d3ee)
![Go](https://img.shields.io/badge/Go-1.22-7c3aed)
![Docker](https://img.shields.io/badge/docker-multi--arch-06b6d4)

[English README](./README.en.md) · [OpenAPI spec](./openapi.yaml)

---

## ✨ 特性

- **多協定 Chat API**：OpenAI `/v1/chat/completions`、Anthropic `/v1/messages`、Gemini `:generateContent` 可互相接入與轉出
- **OpenAI 常用端點**：`/v1/responses`、`/v1/embeddings`、`/v1/completions`、`/v1/images/*`、`/v1/audio/*` 對 OpenAI-compatible provider 透傳
- **多供應商**：OpenAI / Anthropic / Gemini / DeepSeek，以及任何 OpenAI 相容端點
- **流式 SSE 翻譯**：OpenAI chunk、Anthropic event stream、Gemini streamGenerateContent 文字 delta 雙向轉譯
- **密鑰池**：每個供應商可配置多把 Key，自動輪轉
- **訪問控制**：帳密登入 + HttpOnly 管理 Cookie，並保留客戶端訪問 Token 白名單
- **實時觀測**：SQLite / MySQL / PostgreSQL 三選一（純 Go 驅動，無 CGO）記錄每次呼叫，內建儀表板
- **單檔部署**：Go 靜態二進制，Docker 鏡像基於 `distroless/static` 約 20-25 MB
- **多架構鏡像**：`linux/amd64` + `linux/arm64`，自動發佈到 `ghcr.io`

## 🚀 快速開始

### Docker (推薦)

```bash
docker run -d --name ai-hub \
  -p 8080:8080 \
  -e ADMIN_TOKEN=please-change-me \
  -e AUTH_JWT_SECRET=please-change-long-random-session-secret \
  -e ACCESS_TOKENS=client-token-1 \
  -v $(pwd)/data:/data \
  -v $(pwd)/config.json:/app/config.json:ro \
  ghcr.io/s12ryt/docker-ai-proxy:latest
```

### Docker Compose

```bash
cp config.example.json config.json
# 編輯 config.json 填入你的 API Keys 並設 enabled=true
docker compose up -d
```

### 從原始碼

```bash
go mod tidy
go build -o ai-hub ./cmd/ai-hub
./ai-hub
```

打開瀏覽器訪問 `http://localhost:8080`,儀表板在 `/dashboard.html`。首次進入儀表板時可建立第一位管理員；之後以帳密登入，管理 session 會存放在 HttpOnly `ai_hub_session` Cookie。

## 🔧 配置

優先級:**環境變數 > config.json > 預設值**。

| 環境變數 | 預設 | 說明 |
| --- | --- | --- |
| `LISTEN` | `:8080` | 監聽地址 |
| `ADMIN_TOKEN` | `change-me-admin` | Legacy 管理 Token；仍可用於腳本存取 `/api/*` |
| `ACCESS_TOKENS` | (空) | Legacy 逗號分隔客戶端 Token；建議改用儀表板「Client 管理」建立具名稱/狀態/限制的 Client；未配置任何可用憑證時 `/v1/*` 會拒絕請求 |
| `AUTH_JWT_SECRET` | (空) | 管理登入 Cookie 簽章密鑰；生產環境務必設定長隨機字串 |
| `AUTH_COOKIE_SECURE` | `0` | 是否為登入 Cookie 加上 `Secure`；HTTPS 生產環境建議設 `1` |
| `AUTH_SESSION_TTL` | `24h` | 管理登入 session 有效期，Go duration 格式，例如 `8h`、`168h` |
| `TELEGRAM_USER_ID` | (空) | Telegram 使用者 ID（預留給通知/審計整合） |
| `TELEGRAM_BOT_ID` | (空) | Telegram Bot ID（預留給通知/審計整合） |
| `DB_DRIVER` | `sqlite` | 資料庫驅動,可選 `sqlite` / `mysql` / `postgres` |
| `DB_PATH` | `data/ai-hub.db` | SQLite 檔案路徑(僅 `DB_DRIVER=sqlite` 時生效) |
| `DB_DSN` | (空) | MySQL / PostgreSQL 連線字串,`mysql` 與 `postgres` 必填 |
| `DB_MAX_OPEN_CONNS` | sqlite=1, 其他=10 | 最大開啟連線數 |
| `DB_MAX_IDLE_CONNS` | sqlite=1, 其他=5 | 最大閒置連線數 |
| `DB_CONN_MAX_LIFETIME` | 雲端=`30m` | Go duration,例如 `15m`、`1h`。SQLite 留空 |
| `DB_RETENTION_DAYS` | `0` | 呼叫日誌保留天數；`0` 表示不自動刪除 |
| `CONFIG_PATH` | `config.json` | 配置檔路徑 |
| `ENABLE_METRICS` | `1` | 是否記錄統計 |

### ☁️ 切換到雲端 MySQL / PostgreSQL

預設使用內嵌的純 Go SQLite — 部署即用、不需任何外部服務。
若你想多副本部署、跨主機共享統計資料,或避免本機磁碟寫入,可直接改 `DB_DRIVER`:

```bash
# MySQL / MariaDB / PlanetScale / TiDB Cloud / AWS RDS for MySQL
docker run -d --name ai-hub \
  -p 8080:8080 \
  -e ADMIN_TOKEN=please-change-me \
  -e DB_DRIVER=mysql \
  -e DB_DSN="aihub:secret@tcp(mysql.internal:3306)/aihub?parseTime=true&charset=utf8mb4&loc=UTC" \
  ghcr.io/s12ryt/docker-ai-proxy:latest

# PostgreSQL / Neon / Supabase / AWS RDS for PostgreSQL
docker run -d --name ai-hub \
  -p 8080:8080 \
  -e ADMIN_TOKEN=please-change-me \
  -e DB_DRIVER=postgres \
  -e DB_DSN="postgres://aihub:secret@pg.internal:5432/aihub?sslmode=require" \
  ghcr.io/s12ryt/docker-ai-proxy:latest
```

DSN 格式:

| 驅動 | 範例 |
| --- | --- |
| `mysql` | `user:pass@tcp(host:3306)/dbname?parseTime=true&charset=utf8mb4&loc=UTC` |
| `postgres` | `postgres://user:pass@host:5432/dbname?sslmode=require` |

切換 driver 時:
- 啟動會自動 `CREATE TABLE IF NOT EXISTS` 與索引 — 無需手動 migration
- 啟動會做 10 秒 ping,連不上會 fail-fast 印出明確錯誤
- 雲端 DB 不需要 `-v ./data:/data` 掛載,docker-compose 中可刪除該行

### `config.json` 範本

```json
{
  "admin_token": "please-change",
  "access_tokens": ["client-token-1"],
  "clients": [
    {
      "name": "demo-client",
      "token": "client-token-1",
      "enabled": true,
      "daily_limit": 0,
      "allowed_models": [],
      "note": "daily_limit=0 表示不限制；空 allowed_models 表示可使用全部模型",
      "created_at": ""
    }
  ],
  "auth_jwt_secret": "change-me-long-random-session-secret",
  "auth_cookie_secure": false,
  "auth_session_ttl": "24h",
  "telegram_user_id": "123456789",
  "telegram_bot_id": "987654321",
  "providers": [
    {
      "name": "openai",
      "base_url": "https://api.openai.com",
      "api_keys": ["sk-xxx", "sk-yyy"],
      "models": ["gpt-4o", "gpt-4o-mini", "text-embedding-3-small", "dall-e-3", "whisper-1"],
      "enabled": true,
      "timeout_sec": 120
    },
    {
      "name": "anthropic",
      "base_url": "https://api.anthropic.com",
      "api_keys": ["ak-..."],
      "models": ["claude-3-5-sonnet-20240620"],
      "enabled": true,
      "timeout_sec": 120
    },
    {
      "name": "gemini",
      "base_url": "https://generativelanguage.googleapis.com",
      "api_keys": ["AIza..."],
      "models": ["gemini-1.5-pro", "gemini-1.5-flash"],
      "enabled": true,
      "timeout_sec": 120
    },
    {
      "name": "deepseek",
      "base_url": "https://api.deepseek.com",
      "api_keys": ["sk-..."],
      "models": ["deepseek-chat", "deepseek-reasoner"],
      "enabled": true
    }
  ]
}
```

完整範本見 [`config.example.json`](./config.example.json)。

## 📡 API

### 列出可用模型

```bash
curl -H "Authorization: Bearer $AI_HUB_TOKEN" \
  http://localhost:8080/v1/models
```

### 對話補全

OpenAI 相容入站：

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

Anthropic 原生入站：

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

Gemini 原生入站：

```bash
curl http://localhost:8080/v1beta/models/gemini-1.5-pro:generateContent \
  -H "Authorization: Bearer $AI_HUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role":"user","parts":[{"text":"Hello!"}]}]
  }'
```

以上三種 chat 入站都會依 `model` 自動選 provider，必要時在 OpenAI / Anthropic / Gemini 協定間轉換。串流也支援主路徑文字 delta 轉譯：OpenAI `stream:true`、Anthropic `stream:true`、Gemini `:streamGenerateContent`。

### OpenAI 常用端點

以下端點對 OpenAI-compatible provider（OpenAI、DeepSeek、或其他 OpenAI 相容服務）做原樣轉發，並會把 `model` 重寫成 provider 內部模型名：

| 端點 | 說明 |
| --- | --- |
| `POST /v1/responses` | OpenAI Responses API，支援 SSE pass-through |
| `POST /v1/embeddings` | Embeddings |
| `POST /v1/completions` | Legacy text completions，支援 SSE pass-through |
| `POST /v1/images/*` | Images，例如 `/v1/images/generations` |
| `POST /v1/audio/*` | Audio，例如 `/v1/audio/transcriptions`；支援 multipart/form-data |

非 OpenAI-compatible provider 對這些端點會明確回 `501 Not Implemented`，避免做語意不一致的假翻譯。

### 跨供應商路由

兩種寫法:
1. 直接用上游模型名 — `"model": "gpt-4o-mini"`(系統依 `models` 自動識別)
2. 顯式前綴 — `"model": "deepseek/my-fine-tuned-model"`

### 用 OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="client-token-1",  # 你的 ACCESS_TOKENS
)
resp = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hi"}],
)
print(resp.choices[0].message.content)
```

### 管理登入與端點

Dashboard 使用帳密登入與 HttpOnly `ai_hub_session` Cookie。第一次部署時可透過 `/dashboard.html` 建立第一位管理員；後續管理端點接受管理員 Cookie，並保留 legacy `ADMIN_TOKEN` Bearer / `?admin_token=` 給既有腳本使用。

| 端點 | 用途 |
| --- | --- |
| `GET /api/auth/bootstrap` | 檢查是否尚未建立第一位管理員 |
| `POST /api/auth/register` | 建立第一位管理員；已登入 admin 時可新增使用者 |
| `POST /api/auth/login` | 使用帳密登入並設定 `ai_hub_session` Cookie |
| `POST /api/auth/logout` | 清除登入 Cookie |
| `GET /api/auth/profile` | 取得目前登入使用者資訊（不含密碼雜湊） |
| `GET /api/summary?hours=24` | 總請求/錯誤/延遲/Token 聚合 |
| `GET /api/providers` | 供應商配置(不含 Keys) |
| `PUT /api/providers` | 更新供應商配置（需 JSON Content-Type；Cookie 登入需同源 Origin/Referer） |
| `GET /api/access-tokens` | 取得 legacy `/v1/*` 客戶端 Bearer Token 清單（僅管理端可用） |
| `PUT /api/access-tokens` | 建立、編輯或刪除 legacy Access Tokens（需 JSON Content-Type；空清單會讓 `/v1/*` 回 401，除非仍有啟用中的 Clients） |
| `GET /api/clients` | 取得具名稱、啟用狀態、每日限制與允許模型白名單的 Client 清單 |
| `PUT /api/clients` | 建立、編輯或刪除 Clients（需 JSON Content-Type；停用 Token 會被拒絕，`daily_limit` 與 `allowed_models` 會在 `/v1/*` 強制執行） |
| `GET /api/recent?limit=100` | 最近呼叫列表 |
| `GET /api/runtime` | Go 運行時資訊與 DB 連線池統計 |
| `POST /api/reload` | 熱重載 `config.json`（需 JSON Content-Type；Cookie 登入需同源 Origin/Referer） |
| `GET /healthz` | 公開健康檢查 |

## 🏗️ 專案結構

```
.
├── cmd/ai-hub/          # 主程式入口
├── internal/
│   ├── config/          # 配置載入(env + json)
│   ├── providers/       # 密鑰輪轉
│   ├── proxy/           # 上游請求路由與轉發
│   ├── store/           # 持久化:SQLite / MySQL / PostgreSQL 三選一(純 Go 驅動,無 CGO)
│   └── server/          # HTTP 路由、中間件、嵌入靜態頁
│       └── web/         # 玻璃擬態前端(landing + dashboard)
├── .github/workflows/
│   ├── ci.yml               # 測試 + 多架構 build 驗證
│   ├── docker-publish.yml   # 推送多架構鏡像到 ghcr.io
│   └── release.yml          # 建立 GitHub Release 並上傳跨平台二進制
├── scripts/dev.ps1      # 本機 fmt/test/vet/check 便利腳本
├── Dockerfile           # 多階段 + distroless,純靜態
├── docker-compose.yml
└── config.example.json
```

## 🧪 測試

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

CI 自動執行:`gofmt` 檢查 → `go vet` → `go test -race` → 多架構 Docker 構建驗證。

## 🐳 從 GHCR 發佈鏡像

推送到 `main`(或 `master`)或推送 `v*.*.*` 標籤,即會觸發
[`docker-publish.yml`](./.github/workflows/docker-publish.yml)
構建 **`linux/amd64` + `linux/arm64`** 鏡像並推到:

```
ghcr.io/s12ryt/docker-ai-proxy:latest
ghcr.io/s12ryt/docker-ai-proxy:v1.2.3
ghcr.io/s12ryt/docker-ai-proxy:sha-abc1234
```

並附帶 SBOM、provenance 證明。倉庫無需任何額外 secret — 內建 `GITHUB_TOKEN` 已具備寫入 `packages` 權限。

## 🔒 安全注意事項

- **務必設定** `AUTH_JWT_SECRET` 為長隨機字串；未設定時只會使用程序內暫時密鑰，重啟後既有登入 Cookie 會失效
- `ADMIN_TOKEN` 僅作為 legacy 腳本相容用途，仍應修改為強隨機值並避免外洩
- **發布到公網前務必透過儀表板「Client 管理」建立至少一個啟用中的 Client，或保留 legacy `ACCESS_TOKENS`**；若沒有任何可用憑證，`/v1/*` 會回 `401`，不會開放匿名模型代理呼叫；Client 的 `daily_limit` 以 UTC 當日呼叫次數強制限制，`allowed_models` 會限制可用模型並過濾 `/v1/models` 清單
- 生產環境建議在前面套一層 TLS(Caddy / Nginx / Cloudflare)，並設定 `AUTH_COOKIE_SECURE=1`
- Cookie 登入的 mutating `/api/*` 請求需 `Content-Type: application/json` 且具備同源 `Origin` 或 `Referer`，用來降低 CSRF 風險
- `data/ai-hub.db` 內含調用元數據(無 prompt 內容)與使用者帳號雜湊，可用 `DB_RETENTION_DAYS` 自動清理舊呼叫紀錄；設為 `0` 時請自行管理保留週期

## 📜 License

MIT
