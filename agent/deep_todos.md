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
- [x] CI 驗證：`d86a6c7` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Stage 4 完成。

### Stage 5 · OpenAI embeddings/completions 端點
- [x] `internal/proxy/proxy.go` 新增 `ServeEmbeddings`：OpenAI-compatible provider pass-through 到 `/v1/embeddings`；Anthropic/Gemini 等非 OpenAI-compatible provider 明確回 501。
- [x] `internal/proxy/proxy.go` 新增 `ServeCompletions`：OpenAI-compatible provider pass-through 到 legacy `/v1/completions`；`stream:true` 沿用 SSE 原樣透傳。
- [x] 抽出 `serveOpenAICompatibleEndpoint`：共用 JSON/model/provider resolution、upstream request、Bearer/header、response pass-through 與獨立 5s `LogCall` timeout。
- [x] `internal/server/server.go` 接上 `/v1/embeddings` 與 `/v1/completions`，沿用 `requireAccessToken`。
- [x] 新增 e2e 測試：`TestServeEmbeddings_OpenAICompatibleUpstream`、`TestServeCompletions_OpenAICompatibleUpstream`、`TestServeCompletions_StreamPassThrough`、`TestServeEmbeddings_NonOpenAIProviderRejected`。
- [x] 本機 portable Go 驗證：`gofmt` + `go test -count=1 ./...` + `go vet ./...` 全通過（Windows 無 CGO，`-race` 仍由 CI Linux runner 驗證）。
- [x] CI 驗證：`cd12b6f` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Stage 5 完成。

### Stage 6 · OpenAI images/audio 端點
- [x] `internal/proxy/proxy.go` 新增 `ServeImages`：OpenAI-compatible provider pass-through 到 `/v1/images/*` 子路徑；JSON body 會重寫 `model` 為 upstream model；非 OpenAI-compatible provider 明確回 501。
- [x] `internal/proxy/proxy.go` 新增 `ServeAudio`：OpenAI-compatible provider pass-through 到 `/v1/audio/*` 子路徑；multipart/form-data 會重寫 `model` part 並保留 `file` 等其他 part。
- [x] 抽出 `serveOpenAICompatibleMediaEndpoint` / `readMediaRequest` / `rewriteRequestModel` / `rewriteMultipartModel` / `forwardOpenAICompatible`，共用 JSON/multipart model extraction、request rewrite、Bearer/header、response pass-through 與獨立 5s `LogCall` timeout。
- [x] `internal/server/server.go` 接上 `/v1/images/` 與 `/v1/audio/` prefix，沿用 `requireAccessToken`。
- [x] 新增 e2e 測試：`TestServeImages_OpenAICompatibleUpstream`、`TestServeAudioTranscriptions_OpenAICompatibleMultipartUpstream`、`TestServeImages_NonOpenAIProviderRejected`、`TestServeImages_RequiresSubpath`。
- [x] 本機 portable Go 驗證：`gofmt` + `go test -count=1 ./...` + `go vet ./...` + `git diff --check` 全通過（僅 autocrlf warning）。
- [x] CI 驗證：`c68068a` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Stage 6 完成。

### Stage 7 · README 與最終文件整理
- [x] `README.md` 更新為多協定 AI gateway 說明：OpenAI / Anthropic / Gemini chat 入站、協定互轉、SSE 串流翻譯、OpenAI embeddings/completions/images/audio pass-through。
- [x] README API 範例補齊 `/v1/chat/completions`、`/v1/messages`、Gemini `:generateContent`、OpenAI 常用端點表與非 OpenAI-compatible provider 的 501 行為。
- [x] `config.example.json` 補 Gemini provider，更新 OpenAI/Anthropic 範例 models，涵蓋 embeddings/images/audio 常用模型。
- [x] `agent/memory.md`、`agent/deep_todos.md`、`agent/項目表.md` 同步最終功能面與 backlog 狀態。
- [x] CI 驗證：`cc817ad` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Stage 7 完成。

### OpenAI Responses API · 2026-05-23
- [x] `internal/proxy/proxy.go` 新增 `ServeResponses`：OpenAI-compatible provider pass-through 到 `/v1/responses`；`stream:true` 沿用 SSE raw pass-through。
- [x] `internal/server/server.go` 接上 `/v1/responses`，沿用 `requireAccessToken`。
- [x] 新增 e2e 測試：`TestServeResponses_OpenAICompatibleUpstream`、`TestServeResponses_StreamPassThrough`、`TestServeResponses_NonOpenAIProviderRejected`。
- [x] README / agent 文件補 OpenAI Responses API 支援說明；非 OpenAI-compatible provider 明確回 501。
- [x] CI 驗證：`5cee816` 後 `ci.yml` 與 `docker-publish.yml` 全綠；Responses API 完成。

### P1–P3 backlog 實作切片 · 2026-05-23
- [x] **P1 token usage 計數**：新增 `internal/proxy/usage.go`，解析 OpenAI `usage.prompt_tokens/completion_tokens/total_tokens`、Anthropic `usage.input_tokens/output_tokens`、Gemini `usageMetadata.*TokenCount`，寫入 `store.CallRecord.TokensIn/TokensOut`。
- [x] token usage 覆蓋三條路徑：OpenAI-compatible raw pass-through 非串流 response、跨協定非串流翻譯 response、SSE stream final usage chunk；新增/補強 e2e 斷言讓 store recent calls 驗證 token 欄位。
- [x] **P1 graceful shutdown**：`Server.Shutdown` 從 placeholder 改為釋放 server-owned store；`cmd/ai-hub/main.go` 保留 SIGINT/SIGTERM → `http.Server.Shutdown(ctx)`，並改由 `srv.Shutdown(ctx)` 關閉 store。
- [x] **P2 熱重載**：新增 admin-only `POST /api/reload`，呼叫既有 `config.Reload()`；非 POST 回 405。
- [x] **P2 DB 連線健康監控**：`/api/runtime` 新增 `db_stats`，輸出 driver、open/in-use/idle connections、wait_count、wait_duration_ms 等 `database/sql` pool 指標。
- [x] **P2 Bug 12**：`loggingResponseWriter` 補 `io.ReaderFrom`，避免 wrapper 關閉未來 `io.Copy` / sendfile fast path。
- [x] **P3 Bug 18**：`main.go` 的 `-version` / `--version` 參數改為大小寫不敏感。
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

- [ ] **P1–P3 backlog 實作切片 CI 通過**：push 後到 https://github.com/s12ryt/docker-ai-proxy/actions 確認 `ci.yml` 與 `docker-publish.yml` 全綠。

### P1 · 重要（功能不完整或正確性問題）

- [x] **token usage 計數**：proxy 從上游 response body / SSE final usage 解析 token usage，寫入 `store.CallRecord.TokensIn/TokensOut`。
    - 已覆蓋 OpenAI-compatible raw pass-through、跨協定非串流翻譯、SSE stream delta usage 三條路徑。
    - dashboard 既有 summary/recent token 欄位可直接吃到資料。

- [x] **graceful shutdown**：cmd/ai-hub/main.go 已接 SIGINT/SIGTERM 呼叫 `http.Server.Shutdown(ctx)`；`server.Shutdown` 從 placeholder 改為釋放 server-owned store，main 不再重複 defer close。

### P2 · 增強

- [x] **熱重載**：新增 admin-only `POST /api/reload` 走既有 `config.Reload()`；非 POST 回 405。
- [ ] **rate limit / per-token quota**：每個 access token 的 RPM/TPM 限制。
- [ ] **provider 健康檢查**：失敗計數 + 暫時冷卻（circuit breaker），讓 KeyPicker 跳過壞 key。
- [x] **dashboard 顯示 token 統計**：token usage 寫入後，既有 dashboard summary / recent calls token 欄位可顯示真實資料。
- [ ] **DB schema migration 工具**：現在三套 schema 是 dialect 字串硬編碼，加欄位要三邊改。可導入 `golang-migrate` 或 `pressly/goose` 統一管理版本。
- [ ] **DB retention job**：背景定時 `DELETE FROM ai_calls WHERE created_at < NOW() - INTERVAL ?`（雲端 DB 必要）。sqlite 時代靠刪檔，現在切雲端後沒清理機制會無限長大。
- [x] **DB 連線健康監控**：`/api/runtime` 暴露 `db_stats`（driver/open/in_use/idle/wait_count/wait_duration_ms），方便診斷雲端連線池異常。
- [ ] **（觀察中）Bug 16 · `rebindPostgres` 註釋處理**：目前 query 全是手寫硬編無註釋，未來引入 query builder 時再補 `--` / `/* */` 略過邏輯。
- [x] **Bug 12 · loggingResponseWriter 實作 `io.ReaderFrom`**：已補 `ReadFrom`，避免 wrapper 關閉未來 `io.Copy` / sendfile fast path。

### P3 · 體驗與品質

- [ ] **本機開發**：建議補一份 `Makefile` 或 `scripts/dev.ps1`，方便沒 Go 環境用 Docker 跑測試（例如 `docker run --rm -v ${PWD}:/src -w /src golang:1.22 go test ./...`）。
- [x] **更多 e2e 測試**：補強 token usage、SSE stream usage、runtime db_stats、reload method/status 測試；先前多協定階段已覆蓋 SSE 流式與 Anthropic/Gemini 路徑。
- [ ] **dashboard.js 切換 provider enable/disable** 的 UI（目前只能改 config.json + 重啟）。
- [ ] **README 補英文版**（README.en.md）給國際用戶。
- [ ] **OpenAPI / Swagger** 描述 `/v1/*` 與 `/api/*`。
- [ ] **read replica / read-write 分流**：高流量場景需 `DB_READ_DSN` 之類設計（讀走 replica）。雲端 DB 才有意義，sqlite 用不到。
- [x] **Bug 18 · `main.go -version` flag 大小寫不敏感**：已補 `strings.ToLower`。
