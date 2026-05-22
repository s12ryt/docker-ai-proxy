# AI Hub · 多模型聚合分發網關

> 用 Go 撰寫的 **多協定 AI 模型聚合網關**。把 OpenAI、Anthropic、Gemini、DeepSeek 與任意 OpenAI 相容端點整合進**單一 API**；支援 OpenAI / Anthropic / Gemini chat 協定互轉、SSE 串流翻譯、OpenAI embeddings/completions/images/audio 端點轉發，內建密鑰池、SQLite/MySQL/PostgreSQL 觀測、漂亮的暗黑玻璃擬態控制台 — 單檔靜態二進制，Docker 鏡像 < 25 MB。

![License: MIT](https://img.shields.io/badge/license-MIT-22d3ee)
![Go](https://img.shields.io/badge/Go-1.22-7c3aed)
![Docker](https://img.shields.io/badge/docker-multi--arch-06b6d4)

---

## ✨ 特性

- **多協定 Chat API**：OpenAI `/v1/chat/completions`、Anthropic `/v1/messages`、Gemini `:generateContent` 可互相接入與轉出
- **OpenAI 常用端點**：`/v1/embeddings`、`/v1/completions`、`/v1/images/*`、`/v1/audio/*` 對 OpenAI-compatible provider 透傳
- **多供應商**：OpenAI / Anthropic / Gemini / DeepSeek，以及任何 OpenAI 相容端點
- **流式 SSE 翻譯**：OpenAI chunk、Anthropic event stream、Gemini streamGenerateContent 文字 delta 雙向轉譯
- **密鑰池**：每個供應商可配置多把 Key，自動輪轉
- **訪問控制**：管理員 Token + 客戶端訪問 Token 白名單
- **實時觀測**：SQLite / MySQL / PostgreSQL 三選一（純 Go 驅動，無 CGO）記錄每次呼叫，內建儀表板
- **單檔部署**：Go 靜態二進制，Docker 鏡像基於 `distroless/static` 約 20-25 MB
- **多架構鏡像**：`linux/amd64` + `linux/arm64`，自動發佈到 `ghcr.io`

## 🚀 快速開始

### Docker (推薦)

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
# 編輯 config.json 填入你的 API Keys 並設 enabled=true
docker compose up -d
```

### 從原始碼

```bash
go mod tidy
go build -o ai-hub ./cmd/ai-hub
./ai-hub
```

打開瀏覽器訪問 `http://localhost:8080`,儀表板在 `/dashboard.html`。

## 🔧 配置

優先級:**環境變數 > config.json > 預設值**。

| 環境變數 | 預設 | 說明 |
| --- | --- | --- |
| `LISTEN` | `:8080` | 監聽地址 |
| `ADMIN_TOKEN` | `change-me-admin` | 控制台與 `/api/*` 認證 Token |
| `ACCESS_TOKENS` | (空) | 逗號分隔的客戶端訪問 Token;空表示開放呼叫 |
| `TELEGRAM_USER_ID` | (空) | Telegram 使用者 ID（預留給通知/審計整合） |
| `TELEGRAM_BOT_ID` | (空) | Telegram Bot ID（預留給通知/審計整合） |
| `DB_DRIVER` | `sqlite` | 資料庫驅動,可選 `sqlite` / `mysql` / `postgres` |
| `DB_PATH` | `data/ai-hub.db` | SQLite 檔案路徑(僅 `DB_DRIVER=sqlite` 時生效) |
| `DB_DSN` | (空) | MySQL / PostgreSQL 連線字串,`mysql` 與 `postgres` 必填 |
| `DB_MAX_OPEN_CONNS` | sqlite=1, 其他=10 | 最大開啟連線數 |
| `DB_MAX_IDLE_CONNS` | sqlite=1, 其他=5 | 最大閒置連線數 |
| `DB_CONN_MAX_LIFETIME` | 雲端=`30m` | Go duration,例如 `15m`、`1h`。SQLite 留空 |
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

### 管理端點(需要 `ADMIN_TOKEN`)

| 端點 | 用途 |
| --- | --- |
| `GET /api/summary?hours=24` | 總請求/錯誤/延遲/Token 聚合 |
| `GET /api/providers` | 供應商配置(不含 Keys) |
| `GET /api/recent?limit=100` | 最近呼叫列表 |
| `GET /api/runtime` | Go 運行時資訊 |
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

- **務必修改** `ADMIN_TOKEN`,它能讀取所有調用日誌
- 生產環境建議在前面套一層 TLS(Caddy / Nginx / Cloudflare)
- `data/ai-hub.db` 內含調用元數據(無 prompt 內容),自行管理保留週期

## 📜 License

MIT
