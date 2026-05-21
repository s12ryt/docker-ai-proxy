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
