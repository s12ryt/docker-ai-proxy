# 項目任務清單 (deep_todos.md)

- [x] Stage 0: 準備與設計確認
    - [x] 完成規劃文檔撰寫
    - [x] 初始化管理檔案
- [x] Stage 1: 後端資料結構 (Store)
    - [x] 實作 `users` 資料表 schema (SQLite/MySQL/PostgreSQL)
    - [x] 新增 `internal/store/user.go` 與 `User` 模型
    - [x] 新增 Store CRUD 方法 (CreateUser, FindUserByUsername 等)
    - [x] 實作 `CreateInitialAdmin` (包含 `bootstrap_state` sentinel row 競態處理)
    - [x] 補齊 Store 與並發初始化測試
- [x] Stage 2: Auth Service 核心
    - [x] 實作密碼雜湊與驗證 (bcrypt)
    - [x] 實作登入 session 簽發與驗證 (標準庫 HMAC-SHA256 三段 JWT-like token)
    - [x] 實作 Cookie Helper (`ai_hub_session`, HttpOnly, SameSite=Lax)
    - [x] 更新 `internal/config/config.go` (`AUTH_JWT_SECRET`, `AUTH_COOKIE_SECURE`, `AUTH_SESSION_TTL`)
- [x] Stage 3: Auth API 與中間件
    - [x] 實作 `/api/auth/*` handler (`bootstrap`, `register`, `login`, `logout`, `profile`)
    - [x] 實作 `requireUser`, `requireAdmin`, `requireSameOriginForMutating` 中間件
- [x] Stage 4: Dashboard 改造
    - [x] 更新 Dashboard UI (登入/初始化表單)
    - [x] 移除 `localStorage` 依賴
    - [x] 調整前端 API 呼叫 (Cookie 憑證)
- [x] Stage 5: 測試與文件
    - [x] 補齊完整 Server auth 測試
    - [x] 更新 README 與 OpenAPI
    - [x] 更新 `config.example.json` 與 agent 狀態檔

## 驗證註記

- 目前工作環境找不到 `go` executable，且 `gopls` 未安裝，因此尚未能在本機執行 `gofmt` / `go test` / `go vet`。
- 已完成靜態人工檢查、補齊測試案例與文件；待 Go 環境可用後需立即執行完整測試。
