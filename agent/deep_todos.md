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


## TODO · 待辦 backlog

### P0 · 阻塞性

- [ ] **驗證 CI 通過**：push 後到 https://github.com/s12ryt/docker-ai-proxy/actions 確認 `ci.yml` 與 `docker-publish.yml` 全綠。

### P1 · 重要（功能不完整或正確性問題）

- [ ] **Anthropic 真支援**：buildUpstreamRequest 目前對 anthropic 只改 URL/header，body 仍是 OpenAI 格式 → 上游回 422。
    - 工作：在 ServeChatCompletions 內偵測 `provider.Name == "anthropic"`，把 messages 轉成 anthropic 的 `messages` + 提出 `system` 為頂層欄位、`max_tokens` 必填等。
    - 流式還要做 SSE 反向翻譯（anthropic event types → OpenAI delta）。
    - 成本：中–大（建議獨立 PR）。

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

### P3 · 體驗與品質

- [ ] **本機開發**：建議補一份 `Makefile` 或 `scripts/dev.ps1`，方便沒 Go 環境用 Docker 跑測試（例如 `docker run --rm -v ${PWD}:/src -w /src golang:1.22 go test ./...`）。
- [ ] **更多 e2e 測試**：覆蓋 SSE 流式、X-Forwarded-For、Anthropic 路徑、超時。
- [ ] **dashboard.js 切換 provider enable/disable** 的 UI（目前只能改 config.json + 重啟）。
- [ ] **README 補英文版**（README.en.md）給國際用戶。
- [ ] **OpenAPI / Swagger** 描述 `/v1/*` 與 `/api/*`。
- [ ] **read replica / read-write 分流**：高流量場景需 `DB_READ_DSN` 之類設計（讀走 replica）。雲端 DB 才有意義，sqlite 用不到。
