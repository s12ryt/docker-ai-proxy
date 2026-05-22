# deep_todos · docker-ai-proxy

> 完整 backlog。每項標註優先級與成本。完成後移至「已完成」並寫入 memory.md。

## 已完成 (本次 Ralph Loop)

- [x] **P0** go.mod 補 `require modernc.org/sqlite v1.34.1`
- [x] **P0** Dockerfile `/data` 預先 chown 65532:65532（解決 distroless nonroot 寫入 EACCES）
- [x] **P1** `config.Reload()` 防 nil pointer
- [x] **P1** `server.withLogging` 改為真 access log + loggingResponseWriter (status + Flush forward)
- [x] **P1** `proxy.ServeChatCompletions` 32KB 緩衝 + per-chunk flush 流式
- [x] **P2** index.html GitHub 連結指向 s12ryt/docker-ai-proxy
- [x] **P2** 建立 agent/ 目錄 (項目表 / memory / deep_todos)
- [x] **P1** 雲端 DB 支援：DB_DRIVER / DB_DSN / 池設定，支援 SQLite / MySQL / PostgreSQL 三選一
- [x] **P1** Telegram 參數變量化：`TELEGRAM_USER_ID` / `TELEGRAM_BOT_ID` 同步支援 env 與 config.json
- [x] **P1** 修正 config 載入優先級：env 最後覆蓋 config.json，避免部署環境被檔案設定吃掉
- [x] **P1** 修正測試相容性：`store.Open(path)` 舊呼叫改為 `OpenSQLite(path)`

## 已完成 (雲端 DB 增強 · 2026-05-21)

- [x] **P1** 新增 MySQL / PostgreSQL 雙 driver 支持（純 Go，無 CGO）
    - `go-sql-driver/mysql v1.8.1`、`jackc/pgx/v5 v5.7.1`（stdlib mode）
- [x] **P1** `internal/store/dialect.go`：抽象 sqlite / mysql / postgres 三套 schema 與 query rebind
- [x] **P1** `store.Open(cfg Config)` 重構，保留 `OpenSQLite(path)` 向後相容
- [x] **P1** `config.Config` 新增 6 欄位：DBDriver / DBDSN / DBMaxOpen / DBMaxIdle / DBConnMaxLife（DBPath 維持）
- [x] **P1** env / config.json 雙路徑可覆寫（含 `_db_examples` 文件範例區塊）
- [x] **P1** 連線預檢（10s `PingContext`）、SQLite 自動建父目錄、池預設值
- [x] **P1** `dialect_test.go` 含 9 + 6 + 3 + 1 + 2 + 1 + 1 個子測試覆蓋 driver alias / rebind / DSN 解析
- [x] **P1** docker-compose / README / config.example.json 範例與文件更新

## 已完成 (多協定接入 Stage 1 + Stage 2 · 2026-05-22)

使用者需求：「新增對各種 ai-api 協議（claude / google / openai）的接入及轉出」。
規劃 7 階段，本次完成前兩階段。

### Stage 1 · 中介 IR + 三家雙向轉換純函數（不改現有路徑）
- [x] `internal/protocol/types.go` — IR：`ChatRequest` / `Message` / `Part` / `Tool` / `ToolChoice` / `ResponseFormat` / `ChatResponse` 等；Part 涵蓋 text/image/audio/tool_use/tool_result；stop reason 抽象（stop/length/tool_calls/content_filter/error）
- [x] `internal/protocol/openai.go` — OpenAI ↔ IR；hoist system + developer 角色、tool_calls / tool 訊息互轉、image/audio dataURL 解析、N/Seed/User/FreqPen/PresPen/LogProbs 走 Extra
- [x] `internal/protocol/anthropic.go` — Anthropic /v1/messages ↔ IR；system string|array、tool_use/tool_result、tool_choice (auto/any/none/tool)、stop_reason (end_turn/max_tokens/tool_use)、MaxTokens 預設 4096
- [x] `internal/protocol/gemini.go` — Gemini :generateContent ↔ IR；functionCall/functionResponse with synthetic ID `call_<name>`、finish reason (STOP/MAX_TOKENS/SAFETY/RECITATION/BLOCKLIST/PROHIBITED_CONTENT/SPII)、`sanitiseGeminiSchema` 剔除 $schema/$id/definitions/$defs/additionalProperties
- [x] 三份 *_test.go — 全覆蓋 round-trip / system hoist / image / tool / finish 對應 / schema sanitiser
- commits: `5e54219` (feat) + `75bd1f5` (gofmt)

### Stage 2 · 重構 /v1/chat/completions 走 IR（OpenAI pass-through、Anthropic/Gemini 非流式翻譯）
- [x] `internal/proxy/translate.go` — `providerKindOf(p)` (name 主 / baseURL 次)、`upstreamPathForChat(kind, model, stream)` (anthropic→/v1/messages、gemini→/v1beta/models/{model}:generateContent[或 streamGenerateContent])、`translateChatRequest` (decode OpenAI → IR → encode dst native)、`translateChatResponse` (decode native → IR → 補 ID/Created/Model → encode OpenAI)、`fillDefaultsForOpenAIResponse` + `randomID`
- [x] `proxy.buildUpstreamRequest` 重構 — anthropic/claude 強制 path=/v1/messages、gemini/google/googleai/vertex 走 base+path（由 caller 帶入完整 `:generateContent` 路徑）
- [x] `proxy.ServeChatCompletions` 重構 — 依 providerKind 分支：OpenAI pass-through（payload["model"]=upstreamModel + json.Marshal）；Anthropic/Gemini 走 translateChatRequest 並全 buffer 上游回應後翻譯回 OpenAI；非 2xx 一律 pass-through 保留原廠 error envelope
- [x] 抽出 `serveStreamThrough`（沿用既有 32KB SSE 邏輯）與 `serveTranslatedChatResponse`（buffer + translate + write）
- [x] 暫拒非 OpenAI 串流請求：anthropic/gemini + stream=true → 501（Stage 4 補上）
- [x] 新增 e2e 測試 `TestServeChatCompletions_AnthropicTranslation` / `TestServeChatCompletions_GeminiTranslation` / `TestServeChatCompletions_AnthropicStreamingRejected` 用 fake upstream 驗證雙向翻譯與 header/path
- [x] CI 驗證：`5e41f15` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Stage 2 完成。

### Stage 3 · Anthropic/Gemini 原生入站路由（非串流）
- [x] `internal/proxy/translate.go` 泛化：新增 `translateChatRequestFrom` / `translateChatResponseTo`，支援 OpenAI / Anthropic / Gemini 任意來源與目的協定的非串流 request/response 翻譯；同協定 response 維持 raw pass-through。
- [x] `internal/proxy/proxy.go` 新增 `ServeAnthropicMessages`：支援 Anthropic-native `POST /v1/messages` 入站，解碼 Anthropic request → IR → 依目標 provider kind 編碼出站，非串流 response 轉回 Anthropic envelope；Stage 4 已補上 `stream:true`。
- [x] `internal/proxy/proxy.go` 新增 `ServeGeminiGenerateContent`：支援 Gemini-native `POST /v1beta/models/{model}:generateContent` 入站，從 URL path 解析 model，request/response 經 IR 翻譯；Stage 4 已補上 `:streamGenerateContent`。
- [x] `internal/server/server.go` 接上 `/v1/messages` 與 `/v1beta/models/`，沿用 `requireAccessToken`。
- [x] 新增 e2e 測試 `TestServeAnthropicMessages_OpenAIUpstream` / `TestServeGeminiGenerateContent_OpenAIUpstream`，用 fake OpenAI upstream 驗證原生入站 → OpenAI 出站 → 原生 response 回譯。
- [x] 本機 portable Go 驗證：`gofmt` + `go test -count=1 ./...` 全通過（Windows 無 CGO，`-race` 仍由 CI Linux runner 驗證）。

### Stage 4 · SSE 串流雙向翻譯
- [x] 新增 `internal/proxy/stream.go`：proxy-local SSE 事件轉換器，支援掃描 `event:` / `data:` SSE frame，解析 OpenAI `chat.completion.chunk`、Anthropic event stream、Gemini `streamGenerateContent` data chunk。
- [x] `serveChatStreamAs`：統一設定 `Content-Type: text/event-stream`、`Cache-Control: no-cache, no-transform`、`X-Accel-Buffering: no`，逐事件轉譯並 flush；非 2xx 仍原樣 pass-through。
- [x] 新增 OpenAI / Anthropic / Gemini 三種發射器：OpenAI `data: ...` + `[DONE]`、Anthropic `message_start` / `content_block_delta` / `message_delta` / `message_stop`、Gemini SSE data chunk with `candidates[].content.parts[].text` 與 `finishReason`。
- [x] `translateChatRequestFromWithStream` 支援由 handler 強制覆寫 `IR.Stream`，解決 Gemini 串流由 URL `:streamGenerateContent` 表達而非 body 欄位的差異。
- [x] 串流接入：OpenAI 入站 `stream:true` → Anthropic/Gemini provider；Anthropic 入站 `stream:true` → 目標 provider；Gemini 入站 `:streamGenerateContent` → 目標 provider。
- [x] 新增 fake upstream SSE e2e：`TestServeChatCompletions_AnthropicStreamingTranslation`、`TestServeAnthropicMessages_OpenAIStreamingUpstream`、`TestServeGeminiGenerateContent_OpenAIStreamingUpstream`。
- [x] 本機 portable Go 驗證：`gofmt` + `go test -count=1 ./...` + `go vet ./...` 全通過（Windows 無 CGO，`-race` 仍由 CI Linux runner 驗證）。

## 已完成 (第二輪 bug 排查 · 2026-05-22)

逐檔過完整 codebase，找出 18 個潛在 bug，修了 14 個（剩下 2 個 P2 + 1 個 P3 評估後不修）。詳見 memory.md 對應段落。

- [x] **P0** Bug 1+7 `config.Snapshot()` 改為真深拷貝（slice + 每個 Provider 內的 APIKeys/Models）
- [x] **P0** Bug 5 `requireAdmin` 改用 snapshot 並補空 token 防線
- [x] **P0** Bug 6 `handleRuntime` / `healthz` 改用 snapshot
- [x] **P0** Bug 4 `withRecover` 完整化：debug.Stack() 寫 log、特判 ErrAbortHandler、依 wroteHeader 決定是否回 500
- [x] **P0** Bug 2+11 proxy LogCall 改用獨立 `context.WithTimeout(Background, 5s)`，client cancel / shutdown 不影響日誌
- [x] **P1** Bug 3+17 SSE：複製 header 後設 Cache-Control: no-cache, no-transform / X-Accel-Buffering: no、刪 Content-Length；ShortWrite 路徑審視 + 註釋
- [x] **P1** Bug 8 `clientIP` 多 hop XFF Split + 共用 `normaliseIP` 處理 IPv4/IPv6 port
- [x] **P1** Bug 10 `json.Marshal(payload)` err 不再吞，改回 400
- [x] **P1** Bug 13 http.Client transport 補 TLSHandshakeTimeout / ExpectContinueTimeout / ResponseHeaderTimeout
- [x] **P1** Bug 15 body 上限 `maxRequestBytes` 8MB → 32MB（為 multimodal base64 預留）
- [x] **P1** Bug 9 KeyPicker 改為 per-provider cursor（map[string]*uint64 + RWMutex），新增 independence 測試
- [x] **P1** Bug 14 移除 `store.Store.mu` 全局 Mutex（SQLite MaxOpenConns=1 / 雲端 driver 自帶序列化）


## TODO · 待辦 backlog

### P0 · 阻塞性

- [ ] **Stage 4 CI 通過**：push 後到 https://github.com/s12ryt/docker-ai-proxy/actions 確認 `ci.yml` 與 `docker-publish.yml` 全綠。

### P1 · 重要（功能不完整或正確性問題）

- [ ] **token usage 計數**：proxy 從上游 response body 解析 `usage.{prompt,completion,total}_tokens` 寫入 `store.CallRecord`。
    - 注意：streaming 模式下 OpenAI 通常只在最後一個 chunk 帶 usage（且需 client 設 `stream_options.include_usage:true`）。可在 non-stream 路徑先做。
    - 成本：小–中。

- [ ] **graceful shutdown**：cmd/ai-hub/main.go 接 SIGINT/SIGTERM，呼叫 `http.Server.Shutdown(ctx)` 並 close store；server.Shutdown 從 placeholder 改為 inject `*http.Server`。
    - 成本：小。

### P2 · 增強

- [ ] **熱重載**：監聽 SIGHUP 或 `POST /api/reload` 走 `config.Reload()`。目前 Reload 已寫好但沒呼叫。
- [ ] **rate limit / per-token quota**：每個 access token 的 RPM/TPM 限制。
- [ ] **provider 健康檢查**：失敗計數 + 暫時冷卻（circuit breaker），讓 KeyPicker 跳過壞 key。
- [ ] **dashboard 顯示 token 統計**（依賴上面 token usage 計數）。
- [ ] **/v1/embeddings、/v1/completions** 等其他 OpenAI 路由的轉發。
- [ ] **DB schema migration 工具**：現在三套 schema 是 dialect 字串硬編碼，加欄位要三邊改。可導入 `golang-migrate` 或 `pressly/goose` 統一管理版本。
- [ ] **DB retention job**：背景定時 `DELETE FROM ai_calls WHERE created_at < NOW() - INTERVAL ?`（雲端 DB 必要）。sqlite 時代靠刪檔，現在切雲端後沒清理機制會無限長大。
- [ ] **DB 連線健康監控**：把 `db.Stats()`（open/idle/wait_count）暴露到 `/api/runtime`，方便診斷雲端連線池異常。
- [ ] **（觀察中）Bug 16 · `rebindPostgres` 註釋處理**：目前 query 全是手寫硬編無註釋，未來引入 query builder 時再補 `--` / `/* */` 略過邏輯。
- [ ] **（觀察中）Bug 12 · loggingResponseWriter 實作 `io.ReaderFrom`**：當前 proxy 路徑不走 sendfile fast path 沒影響；若未來改用 `io.Copy(w, src)` 模式且需 zero-copy，需補。

### P3 · 體驗與品質

- [ ] **本機開發**：建議補一份 `Makefile` 或 `scripts/dev.ps1`，方便沒 Go 環境用 Docker 跑測試（例如 `docker run --rm -v ${PWD}:/src -w /src golang:1.22 go test ./...`）。
- [ ] **更多 e2e 測試**：覆蓋 SSE 流式、X-Forwarded-For、Anthropic 路徑、超時。
- [ ] **dashboard.js 切換 provider enable/disable** 的 UI（目前只能改 config.json + 重啟）。
- [ ] **README 補英文版**（README.en.md）給國際用戶。
- [ ] **OpenAPI / Swagger** 描述 `/v1/*` 與 `/api/*`。
- [ ] **read replica / read-write 分流**：高流量場景需 `DB_READ_DSN` 之類設計（讀走 replica）。雲端 DB 才有意義，sqlite 用不到。
- [ ] **（觀察中）Bug 18 · `main.go -version` flag 大小寫不敏感**：影響微小，需要時補 `strings.ToLower`。
