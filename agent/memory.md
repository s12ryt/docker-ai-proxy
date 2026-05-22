# memory · docker-ai-proxy

> 紀錄 review 過程的發現、決策、踩坑。讓下一次接手能直接跳過彎路。

## 2026-05-21 · Ralph Loop "自行檢查代碼中有無缺漏並提交至github"

### 環境

- Windows PowerShell 5.1，工作目錄 `D:\00\cloudflare\github\docker-ai-proxy`
- 本機**沒有安裝 Go**（`go.exe not found`），無法本地 build/test
- 一律靠 GitHub Actions CI 驗證

### 進場狀態

- branch=main 與 origin/main 同步
- 最新 commit: `b346ef0` "ci: add server+main tests, drop atomic covermode, harden Dockerfile build"
- 工作樹乾淨

### 找到的 7 個缺陷 + 修復

| # | 嚴重度 | 問題 | 修復 |
|---|---|---|---|
| 1 | 致命 | `go.mod` 沒 `require modernc.org/sqlite`，沒 `go.sum`，本機 build 必失敗。CI 靠 `go mod tidy` 線上補非常脆弱 | 加 `require modernc.org/sqlite v1.34.1`；go.sum 仍由 CI tidy 補 |
| 2 | 致命 | Dockerfile `USER nonroot` + `VOLUME /data`，但 `/data` 由 root 擁有，SQLite 寫入 EACCES | 加 `alpine:3.20 AS rootfs` 中介段：`mkdir /rootfs/data` + `chown 65532:65532`，然後 `COPY --from=rootfs --chown=65532:65532 /rootfs/data /data` 進 distroless |
| 3 | 中 | `config.Reload()` 若 caller 先呼叫 Reload 再 Get，會 nil deref `current.mu` | 兼容 `current==nil` 與 `current.mu==nil`，補新 RWMutex |
| 4 | 中 | `server.withLogging` 是空殼，沒任何輸出 | 改為真 access log，包 `loggingResponseWriter`（status 抓取 + Flush forward），跳過 /healthz、/style.css、/app.js、/dashboard.js |
| 5 | 中 | `proxy.ServeChatCompletions` 用 `io.Copy`，SSE 延遲到 stream 結束才送 | 32KB 緩衝迴圈 + 每塊 `flusher.Flush()`；正確處理 EOF/Canceled/ShortWrite；記錄 BytesOut |
| 6 | 低 | `index.html` GitHub 連結兩處是 `https://github.com`（空連結） | 改為 `https://github.com/s12ryt/docker-ai-proxy` |
| 7 | 中 | `store.Open` 簽名已改成 `Open(cfg Config)`，但測試還在呼叫舊 API，CI 會編譯失敗；另外 `config.load()` 原本是 config.json 蓋掉 env，與文件相反，會讓雲端 DB / Telegram env 被覆蓋 | 把測試改成 `OpenSQLite(...)`，並重構 config 載入為 `defaults → config.json → env`，讓 env 最後覆蓋 |

### 已知不修的限制（記錄供後續演進）

- **Anthropic body schema 與 OpenAI 不同**：目前 `buildUpstreamRequest` 對 anthropic 只改 URL→`/v1/messages` 並加 `x-api-key`/`anthropic-version`，但 body 仍是 OpenAI 格式 → Anthropic 會回 422。要真支援需做 OpenAI→Anthropic body 轉換 + 反向 SSE 翻譯。
- **token 計數恆為 0**：proxy 沒解析響應 body 的 `usage.{prompt,completion}_tokens`。dashboard 的 token 統計目前無實際資料。
- **`Server.Shutdown` 是 placeholder**：cmd/ai-hub/main.go 還沒接 signal handling 做 graceful shutdown。

### 設計重點 / 易踩坑

1. **`loggingResponseWriter.Flush` forward 是必要的**：proxy 對 `w.(http.Flusher)` type assert，如果 logging wrapper 沒實作 `Flush()`，外層的 32KB-flush 流式就會退化為「沒 flush」。已實作。
2. **`config.Reload()` 在還沒 `Get()` 過時被呼叫**：例如外部 SIGHUP 觸發 reload，要避免 panic。已兼容。
3. **distroless nonroot 寫 `/data`**：distroless 沒 shell，不能直接 `RUN chown`。標準解法就是 alpine stage 預備 rootfs。已套用。
4. **gofmt**：本機沒 Go 不能跑，純靠 CI `gofmt -l .` 步驟。新增程式碼以 tab 縮排，避免被擋。
5. **go.sum**：故意不寫 checked-in 版本。CI Dockerfile 的 `GOFLAGS=-mod=mod` + `go mod tidy` 線上補。代價：build 不可離線、不可完全鎖版本。可接受（單一依賴）。

### 操作備忘

- git push 觸發 CI 後到 GitHub Actions 看狀態：https://github.com/s12ryt/docker-ai-proxy/actions
- GHCR 鏡像：`ghcr.io/s12ryt/docker-ai-proxy:latest`
- 本機驗證 build：`docker build -t test .`（如果有 Docker）

---

## 2026-05-21 · 增強：雲端 MySQL / PostgreSQL 支持

### 需求

用戶任務：「幫我添加對雲端的MySQL或是PostgreSQL提供變量配置的支持」。

不只是「加 driver」，而是要在 env / config.json 兩條路徑都暴露 driver 與 DSN，並把 SQLite 切換為可選後端，且維持「純 Go、無 CGO」。

### 設計決策

1. **三 driver、純 Go**：
    - SQLite: `modernc.org/sqlite v1.34.1`（既有）
    - MySQL: `github.com/go-sql-driver/mysql v1.8.1`
    - PostgreSQL: `github.com/jackc/pgx/v5 v5.7.1`（用 `stdlib` 子包註冊為 `pgx` driver）
   全部不需 CGO → distroless 鏡像照樣能用。

2. **新增 6 個配置欄位**（env → JSON → 預設）：
    - `DB_DRIVER` / `db_driver`：`sqlite`（預設）/ `mysql` / `postgres`，alias 接受 `sqlite3` `mariadb` `postgresql` `pg` `pgx`
    - `DB_DSN` / `db_dsn`：mysql/pg 必填；sqlite 可選（留空走 `DB_PATH`）
    - `DB_PATH` / `db_path`：**僅 sqlite 生效**（向後相容）
    - `DB_MAX_OPEN_CONNS` / `db_max_open_conns`：池上限
    - `DB_MAX_IDLE_CONNS` / `db_max_idle_conns`：閒置上限
    - `DB_CONN_MAX_LIFETIME` / `db_conn_max_lifetime`：`time.Duration` 字串（如 `30m`、`1h`）

3. **dialect 抽象**（新檔 `internal/store/dialect.go`）：
    - `dialect{name, driverName, schema[], rebind}` 三家差異全部塞進這層
    - `rebindPostgres` 把 `?` 改成 `$N`，但要跳過字串字面值內的 `?`（處理 `''` escape）
    - sqlite/mysql 的 rebind 是 identity（直接回傳）

4. **`store.Open(cfg Config)` 簽名變更**：
    - 舊 `Open(path string)` → 新 `Open(cfg Config)` 一刀切，但保留 `OpenSQLite(path)` 包裝給既有 test
    - 新增 `Driver()` 回傳 dialect 名稱方便 main 印 log
    - `migrate()` 跑 `dialect.schema` slice（mysql 是單句、sqlite 三句、pg 三句）
    - `LogCall` / `Summarize` / `RecentCalls` 三個查詢全部包 `s.dialect.rebind(...)`

5. **行為差異補齊**：
    - `Summarize` 加 `COALESCE(SUM(...), 0)`，因為 mysql/pg 對「無 row」會回 NULL，sqlite 也補上以保持一致
    - `Open` 末段加 10s `PingContext`：雲端 DB 連線錯誤（DNS / SSL / 防火牆）提前曝露，比第一筆 LogCall 才炸要友善

6. **連線池預設**：
    - SQLite：`MaxOpenConns=1`（modernc.org/sqlite 多執行緒寫衝突會 SQLITE_BUSY，單連線最穩）
    - MySQL/PG：`MaxOpen=10`、`MaxIdle=5`、`Lifetime=30m`（雲端 DB 多半有閒置連線回收）
    - 全部可被 env / JSON 覆寫

### 踩坑與易混點

1. **`pgx/v5/stdlib` 的 driver 名是 `pgx`**，不是 `postgres` 或 `pq`。`resolveDialect` 把 `postgres/postgresql/pg/pgx` 都對到同一個 dialect 但 `driverName="pgx"`。
2. **mysql DSN 必須加 `parseTime=true`**：不然 `created_at` 讀出來是 `[]byte` 不是 `time.Time`。已寫進 README 與 `_db_examples`。
3. **`io.Copy` 與 SSE 的關係已經修過**，但雲端 DB 換 driver 不影響流式行為——LogCall 是 stream 結束後才呼叫，不在熱路徑上。
4. **`resolveDSN` 對 sqlite 仍自動 `os.MkdirAll(filepath.Dir(path))`**：把這層從 `cmd/ai-hub/main.go` 搬進 store，main 因此乾淨許多。
5. **Reload() 同步新欄位**：6 個 DB 欄位都要在 `Reload()` 與 `Snapshot()` 內複製，漏一個會造成「改 env / config 後沒生效」。
6. **`go.sum` 仍不入庫**：CI 的 `GOFLAGS=-mod=mod` + `go mod tidy` 自動拉三個新依賴。代價：build 不可離線。可接受。

### 已知不修的限制（雲端 DB 部分）

- **schema 是 dialect-specific 字串**，沒做 migrate 版本管理。增欄位時三份 schema 都要改。短期可接受，長期可換 `golang-migrate`。
- **沒做 retention job**：sqlite 時代靠手動刪資料庫檔；雲端 DB 必須加 `DELETE FROM ai_calls WHERE created_at < NOW() - INTERVAL ...` 排程。已寫入 deep_todos。
- **沒做 read replica 分流**：高流量場景可能需要 `db.SetReadOnlyDSN()` 之類設計。寫入 deep_todos P3。
- **MySQL `BIGINT AUTO_INCREMENT` vs sqlite `INTEGER PRIMARY KEY`**：跨 dialect 主鍵型別不同，dashboard 端 JSON 序列化沒問題（都走 `int64`）。注意未來如果加跨 DB migration 工具要留意。

### 操作備忘

- `config.example.json` 加了 `_db_examples` 區塊（底線開頭，被 `encoding/json` 解出但實際不映射到任何欄位 → 純文件用）
- README 新增「☁️ 切換到雲端 MySQL / PostgreSQL」一節，給兩個 docker run 範例
- docker-compose.yml 加註釋 env 範例，使用者只要解註釋就能切換

---

## 2026-05-22 · 第二輪 bug 排查 / 加固

### 範圍

用戶請求：「請你幫我排查還有無潛在bug」→「請你開始進行修復工作」。

逐檔過 `cmd/ai-hub`、`internal/config`、`internal/proxy`、`internal/providers`、`internal/server`、`internal/store`，列出 18 個潛在問題，依 P0–P3 分級。
本輪修了其中 14 個（P0/P1/P2 中與正確性、並發、resource 有關的），P3 與 1 個 P2 評估後不修並記錄理由。

### 修復清單（依檔案）

#### `internal/config/config.go`
- **Bug 1+7 · `Snapshot()` 真深拷貝**：之前 `out := *c` 是淺拷貝，slice header 共用底層陣列，外部讀 + Reload 並發改寫會 race。
  - 加 RLock；`AccessTokens` 拷貝；`Providers` 整 slice 重建，每個 Provider 的 `APIKeys` / `Models` 都 `append([]string(nil), ...)`。
  - 保留 `mu = nil` 在快照上（snapshot 是值類型，不需鎖）。

#### `internal/server/server.go`
- **Bug 5 · `requireAdmin` 用 snapshot**：之前直讀 `s.cfg.AdminToken`，現在 `snap := s.cfg.Snapshot()` 後比較 `snap.AdminToken`。
  - 多一道 `snap.AdminToken == ""` 防空 token（避免空 token 被當合法）。
- **Bug 6 · `handleRuntime` / `healthz` 用 snapshot**：`StartedAt`、`len(Providers)` 等改為從 snap 讀。
- **Bug 4 · `withRecover` 完整化**：
  - import 補 `runtime/debug`。
  - panic 時：先處理 `http.ErrAbortHandler` 交給上層；其他 panic 透過 `log.Printf("[panic] %s %s: %v\n%s", method, path, v, debug.Stack())` 寫完整 stack。
  - 檢查 `*loggingResponseWriter.wroteHeader`，若未寫才輸出 `Content-Type: application/json` + 500 + `{error:{message,type:"ai_hub_panic",code:500}}`。
  - 寫 header 之前就不能再蓋的設計避免「在 stream 中 panic 強寫 500」破壞已下發的 SSE。

#### `internal/proxy/proxy.go`
- **Bug 2+11 · LogCall 獨立 context**：原本 `defer s.store.LogCall(context.Background(), rec)` 雖然脫離 r.Context()，但沒 timeout。
  - 新增 `const logCallTimeout = 5 * time.Second`；defer 內 `ctx, cancel := context.WithTimeout(context.Background(), logCallTimeout); defer cancel()`；`LogCall(ctx, rec)`。
  - 解決：client cancel 不影響日誌、server shutdown 時也最多卡 5s。
- **Bug 3 · 流式 ShortWrite 錯誤路徑**：審視確認 `nw != nr` 後 `rec.ErrMessage = io.ErrShortWrite.Error()` 並 `break` 邏輯本身正確；補了註釋讓未來不被「優化」。
- **Bug 17 · SSE headers**：
  - 提前讀 `stream, _ := payload["stream"].(bool)`（在 outBody marshal 前）。
  - 在複製 upstream header 後、`WriteHeader` 前：若 `stream || isEventStream(resp.Header.Get("Content-Type"))`，設 `Cache-Control: no-cache, no-transform`、`X-Accel-Buffering: no`，並刪 `Content-Length`（SSE 不適用）。
  - 新增 helper `isEventStream(ct string) bool`，能解析 `text/event-stream; charset=utf-8` 之類帶參數。
  - 用途：nginx / cloudflare 反代預設會緩衝 text/event-stream，這兩個 header 是業界標準關閉緩衝的開關。
- **Bug 8 · `clientIP` 多 hop + IPv6**：
  - XFF 改用 `strings.Split(xff, ",")` 走第一個 trim 後非空項（之前 `IndexByte(',')` 對 `", 1.2.3.4"` 開頭逗號會回空字串）。
  - X-Real-IP 也走 `normaliseIP()`。
  - 新增 `normaliseIP(s string) string`：處理 `[ipv6]:port → ipv6`、`[ipv6] → ipv6`、裸 IPv6 不動、`1.2.3.4:port → 1.2.3.4` 四種情況。
- **Bug 10 · `json.Marshal(payload)` 不再吞 err**：改為 `outBody, err := json.Marshal(payload); if err != nil { writeJSONError(w, 400, ...); return }`。
- **Bug 13 · http.Client transport timeouts**：補 `TLSHandshakeTimeout: 10s`、`ExpectContinueTimeout: 1s`、`ResponseHeaderTimeout: 60s`。註釋說明「per-request 全長仍由 `context.WithTimeout(r.Context(), TimeoutSec)` 控」。
  - 避免「TLS 握手 hang 住吃連線」與「上游不回 header 但 keep-alive 不斷」這類資源洩漏。
- **Bug 15 · body 上限 8MB → 32MB**：`const maxRequestBytes = 32 << 20`；註釋說明 multimodal (vision) base64 圖片很容易破 8MB。

#### `internal/providers/keys.go`
- **Bug 9 · KeyPicker per-provider cursor**：之前 `cursor uint64` 全局共用 → openai 抽 1 把 cursor++，gemini 接下來抽會跳過第一個 key。
  - 重寫為 `KeyPicker{ mu sync.RWMutex; cursors map[string]*uint64 }`。
  - `Pick`：len==0 → ""; len==1 → APIKeys[0]; 其他走 `cursorFor(p.Name)` 拿 `*uint64`，再 `atomic.AddUint64(cur, 1) - 1` mod len(keys)。
  - `cursorFor` 用 RLock 快路徑（map 已存在）+ Lock 內 double-check 創建，hot path 多數時候只是 RLock。
- 測試：新增 `TestKeyPicker_PerProviderIndependence` 驗證 openai 敲 5 次後 gemini 第一次仍拿第一個 key。原 `TestKeyPicker_RoundRobin` 給 Provider 補 `Name: "test"` 以套用 map key 機制。

#### `internal/store/store.go`
- **Bug 14 · 移除全局 Mutex**：原本 `LogCall` 內 `s.mu.Lock()/Unlock()` 把所有 DB 寫入序列化。
  - 評估：SQLite 走 `MaxOpenConns=1` 已自動序列化；MySQL/PG 的 driver 本身就是 thread-safe（內部 connection pool 各自鎖）。app-level mutex 對雲端 DB 是純粹的瓶頸。
  - 動作：刪 `Store.mu` 欄位、刪 import `"sync"`、刪 LogCall 內鎖呼叫。doc comment 寫清楚為何不需要。

### 未修項目與理由

- **Bug 12 (P2) loggingResponseWriter 沒實作 `io.ReaderFrom`**：原本擔心 `io.Copy(w, r)` 走 sendfile fast path 會失效。實際路徑：proxy 用手寫 32KB read/write 迴圈，server 沒有走 `io.Copy(loggingW, ...)` 直接寫檔，sendfile fast path 本來就不會觸發。**不修**。
- **Bug 16 (P2) `rebindPostgres` 沒處理 `--` / `/* */` 註釋**：目前 schema 與 query 都是手寫硬編字串，沒有註釋。將來如果引入 query builder 才需要補。**不修**，留 deep_todos 觀察。
- **Bug 18 (P3) `main.go -version` flag 沒 `strings.ToLower`**：用戶輸入大小寫敏感無大影響。**不動**。

### 重點設計決策（避免下次回頭推翻）

1. **Snapshot 是值類型回傳**：呼叫端拿到 `Config` 值，不再持有指針 → 自動避免「拿到指針後底層被 Reload 改」。所有 handler 改成 `snap := s.cfg.Snapshot()`，**禁止直接 `s.cfg.AdminToken` 之類**。
2. **`http.ErrAbortHandler` 不算 panic**：標準庫用它讓 handler 主動放棄，必須往上拋給 net/http 內部處理。withRecover 要特判，否則會誤吞。
3. **SSE 三件套**：`Content-Type: text/event-stream`（upstream 帶來，原樣轉發）、`Cache-Control: no-cache, no-transform`、`X-Accel-Buffering: no` + 刪 Content-Length。漏一項在某些反代下就會壞。
4. **per-provider KeyPicker 必須鎖**：map 不是 thread-safe；用 sync.RWMutex 而不是 sync.Map 因為 hot path 只是讀 `*uint64`，原子操作不需要 sync.Map 的內部複雜性。
5. **`LogCall` 的 timeout 5s 是熱抉擇**：太短會掉日誌（雲端 DB tail latency 通常 100ms–2s），太長會卡 shutdown。5s 是大多數 cloud DB SLA 內 + shutdown 也能接受的點。
6. **`maxRequestBytes = 32MB`**：vision 模型 base64 一張 1080p PNG 約 5–8MB，多輪對話 + 多圖很容易破 8MB。32MB 是 Anthropic/OpenAI 實際接受上限附近。再大要走檔案上傳介面。

### 測試覆蓋預期

本機沒 Go，全靠 CI：
- `keys_test.go` 新增 `TestKeyPicker_PerProviderIndependence`；舊 `TestKeyPicker_RoundRobin` 補 `Name: "test"`。
- 其他既有測試應該全綠：snapshot 不改 API、SSE header 變更只影響 stream:true 路徑（測試裡 stream:false）、http.Client transport 變更不影響 httptest local 路徑、LogCall 移鎖只是內部變更、clientIP 修法只強化既有行為。

### 操作備忘

修完之後請：
1. `git status` 確認改了 6 個檔（config.go / server.go / proxy.go / keys.go / keys_test.go / store.go）。
2. 寫 commit：`fix: harden concurrency, SSE headers, client timeouts, per-provider key rotation`。
3. push 觸發 CI；到 GitHub Actions 看 `ci.yml` 結果。
4. 若 CI 失敗多半是 `gofmt -l .` 抓到 tab/space 不一致，本機沒 Go 沒辦法預跑，靠 CI 回報後修。


---

## 2026-05-22 · 多協定接入 Stage 1 + Stage 2(OpenAI/Anthropic/Gemini 雙向轉換)

### 需求

使用者:「請你幫我新增對各種ai-api協議(claude和google還有openai的各種協議)的接入及轉出」。

確認後範圍:
1. 3x3 入站 x 出站矩陣(OpenAI / Anthropic / Gemini)
2. 完整 SSE 雙向翻譯(Stage 4 才做)
3. OpenAI 全套端點:embeddings / images / audio / completions(Stage 5+6)

7-Stage 規劃:
- **Stage 1**:`internal/protocol/` IR + 3 家雙向轉換純函數 + 單元測試(不改現有路徑) [DONE]
- **Stage 2**:重構 `proxy.ServeChatCompletions` 走 IR;OpenAI/DeepSeek pass-through 快路徑 [DONE](本次未串流;串流 Stage 4)
- Stage 3:新增 `/v1/messages` 與 `/v1beta/models/{model}:generateContent` 入站路由
- Stage 4:SSE 統一事件流 + 三家解析器/發射器 + e2e 測試
- Stage 5:`/v1/embeddings` + `/v1/completions`
- Stage 6:`/v1/images/*` + `/v1/audio/*`(暫 OpenAI 出站)
- Stage 7:README + agent/* 文件更新

### Stage 1 internal/protocol/ IR(commit `5e54219` + style fix `75bd1f5`)

新增 7 個檔(全屬 package `protocol`):

**`types.go`** IR 結構:
- 常量族:`Role*`(system/user/assistant/tool)、`Part*`(text/image/audio/tool_use/tool_result)、`StopReason*`(stop/length/tool_calls/content_filter/error)
- `ChatRequest{Model, System, Messages[], MaxTokens, Temperature*float64, TopP*float64, Stop[]string, Stream, StreamUsage, Tools[], ToolChoice*, ResponseFmt*, Extra map[string]any}`
- `Message{Role, Name, Content []Part, ToolCallID, ToolCalls[]ToolCall}`
- `Part{Type, Text, URL, Data, MediaType, ToolUse*, ToolResult*}`
- `Tool{Name, Description, Parameters map[string]any}`、`ToolChoice{Mode, Name}`、`ResponseFormat{Type, Schema, SchemaName, Strict}`
- `ChatResponse{ID, Model, Created, Choices, Usage, StopReason}`、`Choice{Index, Message, StopReason, LogProbs, NativeFinish}`、`Usage{Prompt/Completion/TotalTokens}`
- helper:`TextPart(s)`、`FloatPtr(v)`、`MessageText(m)`、`HasNonTextContent(m)`

**`openai.go`** `DecodeOpenAIChat/EncodeOpenAIChat/DecodeOpenAIResponse/EncodeOpenAIResponse` + 共用 helper。
- 重點:system + developer role 都 hoist 到 IR.System(以 `\n` 串接);accepts both string 與 array content forms;tool role 對應 `PartToolResult`;N/Seed/User/FrequencyPenalty/PresencePenalty/LogProbs/TopLogProbs 存到 Extra 不丟失;空 choices 補一個避免 SDK 砸。

**`anthropic.go`** `DecodeAnthropicChat/EncodeAnthropicChat/DecodeAnthropicResponse/EncodeAnthropicResponse`。
- `MaxTokens` 預設 4096(Anthropic 必填);system 接受 string 或 blocks 陣列;image 同時支援 base64+media_type 與 url;tool_choice 對應 auto/any/none/tool;stop_reason 對應 end_turn/max_tokens/tool_use。`RoleTool` 訊息會展平為 user-with-tool_result 區塊。

**`gemini.go`** `DecodeGeminiChat/EncodeGeminiChat/DecodeGeminiResponse/EncodeGeminiResponse`。
- Role 映射:user<->user、assistant<->model、tool<->function
- `functionCall` / `functionResponse` 用 synthetic ID `call_<name>` 保留 ToolUseID 對應
- finish_reason:STOP/MAX_TOKENS/SAFETY/RECITATION/BLOCKLIST/PROHIBITED_CONTENT/SPII
- `sanitiseGeminiSchema` 遞迴剔除 `\`\`\`/`\`\`\`/`definitions`/`\`\`\`/`additionalProperties`(Gemini schema 不認)
- `responseMimeType=application/json` 對應 IR `ResponseFmt.json_object|json_schema`

**3 份 *_test.go** 共 30+ 子測試,覆蓋 round-trip / system hoist / data URL image / tool calls / tool choice 4 模式 / finish reason mapping / 空 choices padding / schema sanitiser 等。

### Stage 1 commit & gofmt 踩坑

- commit `5e54219` push 後 CI 在 `gofmt -l .` 失敗;CI 沒貼出哪些檔。
- 本機無 Go 無 Docker -> 用 **play.golang.org/fmt POST API** 線上批次格式化。
- **踩坑**:首次用 `Invoke-WebRequest` 造成 em-dash(0xE2 0x80 0x94)被誤認為 ISO-8859-1 寫壞檔。`git checkout --` 回滾後改用 `System.Net.Http.HttpClient` + `UTF8Encoding(false)` POST,並以 `[System.IO.File]::WriteAllText(..., UTF8Encoding(false))` 寫回。
- 結果:6 個檔被 gofmt 修(struct field 對齊,純 cosmetic,+34/-34 行),commit `75bd1f5` 「style: apply gofmt to internal/protocol」。CI 全綠。
- **教訓**:後續 Stage 2-7 新增/修改 Go 檔都該跑同一個批次線上 gofmt 再 push,避免兩段 commit。

### Stage 2 proxy.ServeChatCompletions 走 IR

新增檔 `internal/proxy/translate.go`(181 行)。重寫 `internal/proxy/proxy.go`(+144/-18)。

**`translate.go`** API:
- `type providerKind int`、`kindOpenAI / kindAnthropic / kindGemini`、`String()`
- `providerKindOf(p config.Provider) providerKind` 優先以 name(anthropic/claude/gemini/google/googleai/vertex)判斷,其次 baseURL(`anthropic.com` / `generativelanguage.googleapis.com` / `aiplatform.googleapis.com`),default openai
- `upstreamPathForChat(kind, model, stream)`:
  - anthropic -> `/v1/messages`
  - gemini -> `/v1beta/models/{model}:generateContent`(stream:true 改 `:streamGenerateContent`)
  - default -> `/v1/chat/completions`
- `translateChatRequest(srcBody []byte, dst providerKind, upstreamModel string) ([]byte, protocol.ChatRequest, error)` `DecodeOpenAIChat` -> 改 ir.Model -> 依 dst Encode
- `translateChatResponse(src providerKind, requestModel string, upstreamBody []byte) ([]byte, error)` anthropic/gemini decode -> `fillDefaultsForOpenAIResponse` -> `EncodeOpenAIResponse`;default 直接 pass-through bytes
- `fillDefaultsForOpenAIResponse`:補 `chatcmpl-<randomID(24)>`、`Created=time.Now().Unix()`、`Model=requestModel`
- `randomID(n)` base36 從 UnixNano 拼,不靠 `crypto/rand`(避免新依賴)

**`proxy.go` 重構**:
- `buildUpstreamRequest`:
  - case `anthropic`/`claude`:若 path 是 `""` 或 `/v1/chat/completions` 才覆寫為 `/v1/messages`;否則原樣(為 Stage 5/6 預留)。加 `x-api-key` + `anthropic-version`。
  - case `gemini`/`google`/`googleai`/`vertex`:加 `x-goog-api-key`,**刪除 hard-code 路徑**,改成 `base + path` 由 caller(`upstreamPathForChat`)控制。原本走 `/v1beta/openai/chat/completions` 的 OpenAI 相容 shim 不再使用。
  - default:不變(Bearer auth)
- `ServeChatCompletions` 分流:
  - kindOpenAI -> pass-through 快路徑:`payload["model"]=upstreamModel` + `json.Marshal` -> `serveStreamThrough`(維持串流低延遲)
  - kindAnthropic/kindGemini + stream=true -> `writeJSONError(501, "streaming for X providers is not yet supported, set stream=false")`
  - kindAnthropic/kindGemini + stream=false -> `translateChatRequest` -> 全 buffer 上游 -> `serveTranslatedChatResponse`
- 抽出兩個 helper:
  - `serveStreamThrough(w, resp, providerName, stream, *rec)`:複製 header(跳 hop-by-hop)、SSE 三件套、32KB 讀寫迴圈 + flusher、處理 ShortWrite/Canceled/EOF。
  - `serveTranslatedChatResponse(w, resp, kind, requestModel, providerName, *rec)`:buffer ReadAll(LimitReader maxRequestBytes);若非 2xx -> 原樣 pass-through(保留原廠 error envelope);2xx -> translate -> `Content-Type: application/json` + 200 + 譯後 body。

### Stage 2 e2e 測試(proxy_test.go 211 -> 479 行)

新增 3 個測試 + 2 個 helper:

1. **`TestServeChatCompletions_AnthropicTranslation`** fake upstream 收 path/x-api-key/anthropic-version + body,回 Anthropic `/v1/messages` minimal response。送 OpenAI 請求(system+user+max_tokens=64)。驗證:status=200、capturedPath 含 `/v1/messages`、headers 正確、body.model=claude...、body.system='be brief'、body.messages 只剩 user(system 被 hoist)、resp.object=`chat.completion`、resp.model=claude...、content 含 'hello from claude'、finish_reason=stop、`X-AI-Hub-Provider=anthropic`、`store.RecentCalls` 錄 1 條 status=200。
2. **`TestServeChatCompletions_GeminiTranslation`** fake upstream 收 path/x-goog-api-key + body,回 Gemini response(STOP + usageMetadata)。驗證:capturedPath 含 `/v1beta/models/gemini-1.5-pro:generateContent`、`x-goog-api-key=gk-test`、body.systemInstruction 非 nil、body.contents 存在、resp 經 OpenAI envelope 包裝、content 含 'hello from gemini'、finish_reason=stop、usage.total_tokens=9。
3. **`TestServeChatCompletions_AnthropicStreamingRejected`** stream=true + anthropic provider -> 501。
4. `toStringForTest`/`toIntForTest` helper 處理 `message.content` 可能是 string 或 `[{type:text,text:...}]` 陣列;number 處理 float64/int/int64。

### Stage 2 設計重點

1. **OpenAI/DeepSeek 走快路徑不過 IR**:測試已驗證 echo 後 `got["model"]=upstreamModel`;保留串流低延遲與 byte-perfect 透傳。避免「翻譯一次再翻譯回來」的無謂 round-trip。
2. **非串流路徑 buffer 整個 upstream body**:Anthropic/Gemini chat completion 非串流回應一般 < 1MB,buffer 風險可接受,換來簡單清晰的翻譯邏輯。串流 Stage 4 才會搬 SSE 事件解析。
3. **`upstreamPathForChat` 為何不放 `buildUpstreamRequest`**:路徑決策需要知道 model+stream,而 `buildUpstreamRequest` 是 path-agnostic(path 由 caller 傳入)。讓 ServeChatCompletions 控制路徑,後續新端點(messages/embeddings/images)能各自決定。
4. **fillDefaultsForOpenAIResponse 補 ID 是必要的**:Anthropic `id=msg_abc...`,Gemini 無 ID 欄位;OpenAI SDK 期待 `chatcmpl-...` 前綴,直接照原 ID 給的話有 SDK 會反射報錯。
5. **buildUpstreamRequest 對 anthropic path 的條件覆寫**:Stage 5 加 `/v1/messages` 入站時,proxy 要能透傳 anthropic 原生路徑,所以只有「OpenAI 預設路徑」才覆寫為 `/v1/messages`,其他路徑原樣。
6. **gemini 不再走 OpenAI 相容 shim**:之前 `/v1beta/openai/chat/completions` 是 Google 的 beta shim,有 model 名稱限制;改走 `:generateContent` 原生端點更全功能(支援 functionCalling、responseSchema、safetySettings 等)。

### 已知不修 / 留 Stage 4 解

- **Anthropic/Gemini 串流 SSE 翻譯**:目前 stream=true 直接 501。Stage 4 會做事件層級雙向翻譯(`message_delta` <-> `content.delta`)。
- **token usage 計數**:Anthropic 回 `input_tokens`/`output_tokens`,Gemini 回 `promptTokenCount`/`candidatesTokenCount`,目前 `translateChatResponse` 已經透過 IR 映射到 `Usage`,所以 `chat.completion` 回應內 `usage` 欄位是準的。但 `store.LogCall` 內 `BytesIn/BytesOut` 還沒換成 token,deep_todos P2 持續觀察。
- **deep_todos.md「Anthropic 真支援」項描述需更新**:非串流已 OK,只剩串流。可改寫為「串流 SSE 反向翻譯」。

---

## 2026-05-22 · Stage 2 CI 偵錯補記

- commit `8c3802c` 推上 main 後，CI `Run tests with race detector & coverage` 失敗，但 GitHub annotations 只有泛用 exit code 1，logs API 未授權無法抓詳細輸出。
- 第一次靜態推測：新增 fake upstream e2e test 在 handler goroutine 寫 captured vars、主 goroutine 讀斷言，可能被 `go test -race` 判 race；commit `5cd1da7` 對 Anthropic/Gemini 兩個測試加 `sync.Mutex` 保護 captured path/header/body 後再推，但 CI 仍失敗。
- 後續改用 portable Go 解法：下載 Go 1.22.12 到 `C:\Users\yoyo2\AppData\Local\Temp\opencode\go`，不污染專案；Windows 無 CGO toolchain 所以不能跑 `-race`，但可跑一般 `go test`。
- 本機重現 `go test -run TestServeChatCompletions_ -count=1 -v ./internal/proxy`，找到真正失敗：翻譯回 OpenAI envelope 時 `finish_reason` 仍是 vendor raw value：Anthropic `end_turn`、Gemini `STOP`，測試預期 OpenAI 標準 `stop`。
- 修復點：`internal/proxy/translate.go` 的 `fillDefaultsForOpenAIResponse` 會清空每個 choice 的 `NativeFinish`，讓 `protocol.EncodeOpenAIResponse` 依正常化後的 `StopReason` 輸出 OpenAI `finish_reason`。保留 protocol 層 IR 對 raw `NativeFinish` 的 fidelity，不改 Stage 1 純轉換函數語意。
- 驗證：
  - `go test -run TestServeChatCompletions_ -count=1 -v ./internal/proxy` 通過。
  - `go test -count=1 ./...` 全套通過。
- 注意：CI Linux runner 能跑 `-race`；Windows portable Go 本機只能覆蓋非 race assertion/build。若 CI 再紅，再依 CI 查下一個 race 或測試問題。

---

## 2026-05-22 · Stage 3 Anthropic/Gemini 原生入站路由

### 目標

延續 Stage 1/2 的 IR 與 OpenAI 入站翻譯能力，新增兩條原生入站協定：

- Anthropic-compatible：`POST /v1/messages`
- Gemini-compatible：`POST /v1beta/models/{model}:generateContent`

本階段只做非串流；Anthropic `stream:true` 與 Gemini `:streamGenerateContent` 都明確回 501，留給 Stage 4 的 SSE 事件層雙向翻譯。

### 主要變更

1. **`internal/proxy/translate.go` 泛化**
   - 新增 `translateChatRequestFrom(srcBody, srcKind, dstKind, upstreamModel)`：OpenAI / Anthropic / Gemini 任意來源 request → IR → 任意目的 request。
   - 新增 `translateChatResponseTo(srcKind, dstKind, requestModel, upstreamBody)`：任意 upstream response → IR → client 期望協定 response。
   - `srcKind == dstKind` 時 response 直接 raw pass-through，不做 decode/encode，避免破壞同協定原廠 response。
   - `encodeChatResponse` 對 OpenAI / Gemini 目的端會清空 `NativeFinish`，讓 encoder 使用正常化後的 `StopReason`；Anthropic encoder 本來就使用正常化 stop reason。

2. **`internal/proxy/proxy.go` 新增原生入站 handler**
   - `ServeAnthropicMessages`：處理 Anthropic-native `/v1/messages`，解碼 Anthropic request，依 model 解析 provider，轉成上游 provider kind，再把 response 轉回 Anthropic envelope。
   - `ServeGeminiGenerateContent`：處理 Gemini-native `/v1beta/models/{model}:generateContent`，從 URL path 解析 model，解碼 Gemini body，依 provider kind 出站，再把 response 轉回 Gemini response。
   - `parseGeminiGenerateContentPath` 支援 URL unescape，辨識 `:generateContent` 與 `:streamGenerateContent`。
   - 抽出 `serveTranslatedChatRequest`：共用 provider resolution、request 翻譯、upstream HTTP、獨立 5s `LogCall` timeout。
   - 抽出 `serveChatResponseAs`：共用非 2xx pass-through 與 2xx response 翻譯。原 Stage 2 `serveTranslatedChatResponse` 改成包裝 `dst=OpenAI`，保留既有 OpenAI 入站測試語意。

3. **`internal/server/server.go` 路由**
   - 新增 `/v1/messages` → `requireAccessToken` → `ServeAnthropicMessages`。
   - 新增 `/v1beta/models/` prefix → `requireAccessToken` → `ServeGeminiGenerateContent`。

### 測試與驗證

新增 `internal/proxy/proxy_test.go` e2e 測試：

- `TestServeAnthropicMessages_OpenAIUpstream`：Anthropic 入站 → fake OpenAI upstream → Anthropic response。驗證 Bearer auth、上游 `/v1/chat/completions`、OpenAI body model/messages、回譯 `type=message`、`stop_reason=end_turn`、內容文字、provider header 與 store log。
- `TestServeGeminiGenerateContent_OpenAIUpstream`：Gemini 入站 → fake OpenAI upstream → Gemini response。驗證 path model、OpenAI body、回譯 `candidates[].content.parts[].text`、`finishReason=STOP`、usage token 映射。
- `TestServeGeminiGenerateContent_StreamingRejected`：`:streamGenerateContent` 暫回 501。

本機 portable Go 驗證：

- `gofmt.exe -w internal/proxy/proxy.go internal/proxy/translate.go internal/server/server.go internal/proxy/proxy_test.go`
- `go.exe test -count=1 ./...` 全通過。

限制：Windows portable Go 仍無 CGO toolchain，不能跑 `-race`；CI Linux runner 會跑 race detector。

### 注意事項

- Anthropic 測試 request 的 `messages[].content` 必須是 Anthropic block array（例如 `[{"type":"text","text":"hi"}]`），不是 OpenAI 的純字串；一開始測試用字串會被 `DecodeAnthropicChat` 拒絕。
- Stage 3 完成後，非串流的 OpenAI / Anthropic / Gemini 入站與 OpenAI / Anthropic / Gemini 出站已形成 3×3 主路徑；串流仍是 Stage 4。

---

## 2026-05-22 · Stage 4 SSE 串流雙向翻譯

### 目標

補齊 Stage 2/3 暫拒的串流路徑，讓 OpenAI / Anthropic / Gemini 三種入站與三種出站在 SSE 文字增量主路徑上可以互轉。

### 主要變更

1. **新增 `internal/proxy/stream.go`**
   - `scanSSE` 解析標準 SSE frame，支援 `event:` / 多行 `data:` / comment line / EOF flush。
   - `parseOpenAIStreamDelta` 解析 OpenAI `chat.completion.chunk` 的 `delta.content`、`finish_reason`、`usage`。
   - `parseAnthropicStreamDelta` 解析 Anthropic `content_block_delta`、`message_delta`、`message_stop`。
   - `parseGeminiStreamDelta` 解析 Gemini `streamGenerateContent` data chunk 的 `candidates[].content.parts[].text`、`finishReason`、`usageMetadata`。
   - `streamEmitter` 可發射 OpenAI SSE (`data: ...` + `[DONE]`)、Anthropic event stream (`message_start` / `content_block_delta` / `message_delta` / `message_stop`)、Gemini SSE data chunk。

2. **proxy 串流接入**
   - `ServeChatCompletions`：OpenAI 入站 `stream:true` 且 provider 是 Anthropic/Gemini 時，不再 501；request 經 IR 編碼到上游原生 stream，response 經 `serveChatStreamAs` 翻回 OpenAI chunk。
   - `ServeAnthropicMessages`：Anthropic `stream:true` 進入 shared translated path。
   - `ServeGeminiGenerateContent`：`:streamGenerateContent` 進入 shared translated path。
   - `serveTranslatedChatRequest` 取消 stream 501，改用 `translateChatRequestFromWithStream` 強制覆寫 IR.Stream，並用 `upstreamPathForChat(dstKind, upstreamModel, stream)` 選擇 Gemini `generateContent` 或 `streamGenerateContent`。
   - `serveChatStreamAs` 統一設 `Content-Type: text/event-stream`、`Cache-Control: no-cache, no-transform`、`X-Accel-Buffering: no`，逐事件轉譯並 flush；非 2xx 仍 raw pass-through。

3. **測試**
   - `TestServeChatCompletions_AnthropicStreamingTranslation`：OpenAI stream 入站 → fake Anthropic SSE upstream → OpenAI chunk + `[DONE]`。
   - `TestServeAnthropicMessages_OpenAIStreamingUpstream`：Anthropic stream 入站 → fake OpenAI SSE upstream → Anthropic event stream。
   - `TestServeGeminiGenerateContent_OpenAIStreamingUpstream`：Gemini `:streamGenerateContent` 入站 → fake OpenAI SSE upstream → Gemini SSE chunks。
   - 舊的 `StreamingRejected` 測試已改為實際翻譯測試。

### 驗證

- `gofmt.exe -w internal/proxy/proxy.go internal/proxy/translate.go internal/proxy/stream.go internal/proxy/proxy_test.go`
- `go.exe test -count=1 ./...` 全通過。
- `go.exe vet ./...` 全通過。

限制：目前串流轉換覆蓋文字 delta、finish reason、usage 主路徑；tool call streaming 尚未完整抽象，若未來要支援 tool delta 細節需擴充 stream IR。

---

## 2026-05-22 · Stage 5 OpenAI embeddings/completions 端點

### 目標

補上 OpenAI 相容常用非 chat 端點：

- `POST /v1/embeddings`
- `POST /v1/completions`

本階段採 **OpenAI-compatible pass-through** 策略：只支援 OpenAI / DeepSeek / 任意 OpenAI 相容 provider；Anthropic/Gemini 原生 embedding/completion 沒有統一穩定對應，先明確回 501，避免假翻譯造成錯誤語意。

### 主要變更

1. **`internal/proxy/proxy.go`**
   - 新增 `ServeEmbeddings`：處理 `/v1/embeddings`，解析 `model`、依 `ProviderForModel` 選 provider，僅允許 `kindOpenAI`，替換成 upstream model 後轉發 `/v1/embeddings`。
   - 新增 `ServeCompletions`：處理 legacy `/v1/completions`，同樣僅支援 OpenAI-compatible provider；若 request `stream:true`，沿用 `serveStreamThrough` 做 SSE 原樣透傳。
   - 抽出 `serveOpenAICompatibleEndpoint` 共用 method/body/json/model/provider resolution、request 建立、header/auth、獨立 5s `LogCall` timeout、response pass-through。
   - 非 OpenAI-compatible provider（Anthropic/Gemini）對 embeddings/completions 回 `501 Not Implemented`。

2. **`internal/server/server.go`**
   - 新增 `/v1/embeddings` 與 `/v1/completions`，都沿用 `requireAccessToken`。

3. **測試**
   - `TestServeEmbeddings_OpenAICompatibleUpstream`：fake OpenAI upstream 驗證 `/v1/embeddings` path、Bearer auth、body model/input、response pass-through、provider header、store log。
   - `TestServeCompletions_OpenAICompatibleUpstream`：驗證 `/v1/completions` path、Bearer auth、prompt/model pass-through 與 response pass-through。
   - `TestServeCompletions_StreamPassThrough`：legacy completion streaming 使用 SSE 原樣透傳並補 `X-Accel-Buffering: no`。
   - `TestServeEmbeddings_NonOpenAIProviderRejected`：Anthropic provider 對 embeddings 明確回 501。

### 驗證

- `gofmt.exe -w internal/proxy/proxy.go internal/proxy/proxy_test.go internal/server/server.go`
- `go.exe test -count=1 ./...` 全通過。
- `go.exe vet ./...` 全通過。

限制：Stage 5 不做 Anthropic/Gemini 原生 embeddings/completions 翻譯；若未來供應商 API 有明確對應，可新增 IR 或 provider-specific 分支。
