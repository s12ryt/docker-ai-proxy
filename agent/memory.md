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

### 找到的 6 個缺陷 + 修復

| # | 嚴重度 | 問題 | 修復 |
|---|---|---|---|
| 1 | 致命 | `go.mod` 沒 `require modernc.org/sqlite`，沒 `go.sum`，本機 build 必失敗。CI 靠 `go mod tidy` 線上補非常脆弱 | 加 `require modernc.org/sqlite v1.34.1`；go.sum 仍由 CI tidy 補 |
| 2 | 致命 | Dockerfile `USER nonroot` + `VOLUME /data`，但 `/data` 由 root 擁有，SQLite 寫入 EACCES | 加 `alpine:3.20 AS rootfs` 中介段：`mkdir /rootfs/data` + `chown 65532:65532`，然後 `COPY --from=rootfs --chown=65532:65532 /rootfs/data /data` 進 distroless |
| 3 | 中 | `config.Reload()` 若 caller 先呼叫 Reload 再 Get，會 nil deref `current.mu` | 兼容 `current==nil` 與 `current.mu==nil`，補新 RWMutex |
| 4 | 中 | `server.withLogging` 是空殼，沒任何輸出 | 改為真 access log，包 `loggingResponseWriter`（status 抓取 + Flush forward），跳過 /healthz、/style.css、/app.js、/dashboard.js |
| 5 | 中 | `proxy.ServeChatCompletions` 用 `io.Copy`，SSE 延遲到 stream 結束才送 | 32KB 緩衝迴圈 + 每塊 `flusher.Flush()`；正確處理 EOF/Canceled/ShortWrite；記錄 BytesOut |
| 6 | 低 | `index.html` GitHub 連結兩處是 `https://github.com`（空連結） | 改為 `https://github.com/s12ryt/docker-ai-proxy` |

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
