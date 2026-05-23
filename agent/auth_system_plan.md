# 多用戶身分認證系統規劃

## 1. 核心願景

為 `AI Hub` 引入帳號登入系統，將目前 Dashboard / `/api/*` 管理端點使用的 `ADMIN_TOKEN + localStorage` 模式，升級為「帳密登入 + HttpOnly JWT Cookie」模式，降低瀏覽器端管理 Token 外洩風險，並為後續多用戶協作、RBAC（Role-Based Access Control）、審計與配額管理打下基礎。

本階段仍需維持專案既有定位：

- 單檔部署、輕量化、無外部認證服務依賴。
- 支援目前既有的 SQLite / MySQL / PostgreSQL 三種 Store dialect。
- 不破壞既有 `/v1/*` OpenAI-compatible / Anthropic / Gemini API 使用方式。
- 保留既有 `ADMIN_TOKEN` 作為相容與緊急維護入口。
- 優先解決 Dashboard / `/api/*` 的瀏覽器管理登入問題；普通使用者、多租戶、per-user API key、quota、rate limit 留到下一階段。

---

## 2. 範圍邊界

### 2.1 本階段要做

- 新增 `users` 資料表。
- 新增帳密登入、登出、登入狀態查詢、初始化註冊 API。
- 新增 bcrypt 密碼雜湊。
- 新增 JWT 簽發 / 驗證。
- 使用 HttpOnly Cookie 儲存 Dashboard session。
- 改造 Dashboard：移除對 `localStorage` 儲存 `ADMIN_TOKEN` 的依賴，改為登入 / 初始化註冊表單。
- 後端中間件支援：
  - `requireUser`：已登入使用者。
  - `requireAdmin`：admin role 或 legacy `ADMIN_TOKEN`。
  - `requireAccessToken`：維持既有 `/v1/*` access token 白名單邏輯。
  - `requireSameOriginForMutating`：Cookie auth 下的基本 CSRF 防護。
- `/api/*` 管理端點逐步改為依角色授權；第一版管理能力保守地只開給 admin。
- 文件與測試同步更新。

### 2.2 本階段不做

- 不把 `/v1/*` 模型代理 API 改成 Cookie 認證。
- 不移除 `ACCESS_TOKENS`。
- 不移除 `ADMIN_TOKEN`。
- 不做完整多租戶資料隔離。
- 不做 per-user API key / quota / rate limit；這應與既有 backlog 的 `rate limit / per-token quota` 一起另行設計。
- 不引入大型 migration framework；仍沿用目前 `internal/store/dialect.go` 的 `CREATE TABLE IF NOT EXISTS` 方式。
- 不在第一版開放普通 `user` role 查看管理資料；`user` role 先保留在 schema 與 JWT claim 中，供後續擴充。

---

## 3. 認證模型

### 3.1 Dashboard / 管理 API

Dashboard 與 `/api/*` 管理端點使用帳號系統：

1. 使用者透過 `/api/auth/login` 輸入帳號密碼。
2. 後端驗證 bcrypt 密碼。
3. 後端簽發 JWT。
4. JWT 透過 HttpOnly Cookie 寫入瀏覽器。
5. 前端後續呼叫 `/api/*` 時使用 `credentials: "same-origin"` 自動帶 Cookie。
6. 後端每次受保護請求都解析 Cookie、驗證 JWT，並用 `sub` 回查 DB，確認使用者仍存在、未 disabled，且角色符合要求。

> 重要：JWT 裡的 `role` 可作為快速判斷與前端顯示資訊，但後端授權不可只相信 JWT claim。`requireUser` / `requireAdmin` 必須以 DB 目前狀態為準，避免使用者被停用或降權後舊 Cookie 仍可使用。

### 3.2 模型代理 API

`/v1/*` 保留既有 `ACCESS_TOKENS`：

```http
Authorization: Bearer <ACCESS_TOKEN>
```

原因：

- `/v1/*` 主要給 SDK、curl、服務端程式使用，不應依賴瀏覽器 Cookie。
- 現有 OpenAI SDK 用法需要繼續相容。
- 未來若要支援「每個使用者有自己的 API key」，應新增 `api_keys` 或 token/quota 表，而不是用 Dashboard Cookie 取代 `ACCESS_TOKENS`。

### 3.3 Legacy `ADMIN_TOKEN` 相容策略

`ADMIN_TOKEN` 不立即移除。`requireAdmin` 第一版應同時接受：

1. 有效 JWT Cookie，且 DB 中對應使用者 `role = admin`、未 disabled。
2. `Authorization: Bearer <ADMIN_TOKEN>`。
3. 既有 `?admin_token=<ADMIN_TOKEN>` 查詢參數。

建議定位：

- `ADMIN_TOKEN`：bootstrap / emergency admin token / legacy automation。
- 帳密登入：Dashboard 日常管理入口。

注意：`?admin_token=` 容易出現在瀏覽器歷史、反向代理 access log、server access log、Referer 中，文件需標記為 **deprecated**，只保留給相容與緊急用途，不建議瀏覽器日常使用。

---

## 4. 資料庫設計（Store）

新增 `users` 資料表。由於專案目前支援 SQLite / MySQL / PostgreSQL，需在三個 dialect 中各自加入對應 schema。

### 4.1 Username 正規化規則

為避免三種 DB 的 collation / unique 行為不一致，第一版必須明確定義 username 規則：

- 儲存前一律 `strings.TrimSpace`。
- 儲存前一律 `strings.ToLower`。
- 登入查詢前也一律套用相同 normalization。
- 允許字元建議限制為：`[a-zA-Z0-9._-]`。
- 長度建議：3 到 64 字元。
- 不另外支援 display name；若未來需要顯示名稱，再新增 `display_name` 欄位。

這能避免 SQLite / PostgreSQL 允許 `Admin` 與 `admin` 共存，而 MySQL 因預設 collation 不允許的跨 DB 行為差異。

### 4.2 欄位設計

| 欄位 | 建議型態 | 說明 |
| :--- | :--- | :--- |
| `id` | INTEGER / BIGINT | 主鍵，自增 |
| `username` | TEXT / VARCHAR(64) | 唯一帳號，需 trim + lower-case 後儲存 |
| `password_hash` | TEXT | bcrypt 加密後的密碼 |
| `role` | TEXT / VARCHAR(16) | `admin` 或 `user` |
| `disabled` | BOOLEAN / INTEGER | 是否停用帳號，預設 false |
| `created_at` | BIGINT | 建立時間，Unix milliseconds |
| `updated_at` | BIGINT | 更新時間，Unix milliseconds |
| `last_login_at` | BIGINT NULL | 最後登入時間，未登入為 NULL |

### 4.3 索引與約束

最低要求：

- `username` 必須唯一。
- `role` 僅允許 `admin` / `user`。
- `disabled` 預設 false。
- `created_at` / `updated_at` 不可為 NULL。

建議 schema 方向：

```sql
CREATE TABLE IF NOT EXISTS users (...);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);
```

各 dialect 注意事項：

- SQLite 可用 `INTEGER NOT NULL DEFAULT 0` 表示 boolean。
- MySQL 可用 `BOOLEAN NOT NULL DEFAULT FALSE` 或 `TINYINT(1) NOT NULL DEFAULT 0`。
- PostgreSQL 可用 `BOOLEAN NOT NULL DEFAULT FALSE`。
- 若要加 `CHECK (role IN ('admin', 'user'))`，需確認三個 dialect 行為一致；不一致時至少在 Go validation 層強制檢查。

### 4.4 Store User model

建議在 `internal/store/user.go` 新增：

```go
type User struct {
    ID            int64
    Username      string
    PasswordHash  string
    Role          string
    Disabled      bool
    CreatedAt     time.Time
    UpdatedAt     time.Time
    LastLoginAt   *time.Time
}
```

`last_login_at` 在 DB 是 nullable BIGINT，讀取時用 `sql.NullInt64` 再轉 `*time.Time`。不要用裸 `time.Time` 表示 nullable 欄位，避免把「從未登入」和「zero time」混在一起。

### 4.5 Store 方法

建議新增：

```go
func (s *Store) CreateUser(ctx context.Context, u User) (User, error)
func (s *Store) CreateInitialAdmin(ctx context.Context, u User) (User, error)
func (s *Store) CountUsers(ctx context.Context) (int64, error)
func (s *Store) FindUserByUsername(ctx context.Context, username string) (User, error)
func (s *Store) FindUserByID(ctx context.Context, id int64) (User, error)
func (s *Store) UpdateUserLastLogin(ctx context.Context, id int64) error
```

注意：

- `CreateUser` 必須依賴 DB unique constraint 防止同 username 重複。
- `CreateInitialAdmin` 必須專門處理初始化競態，不可只在 handler 中做 `CountUsers() == 0` 後再 `CreateUser()`。
- 不要把明文密碼寫入任何 log 或 DB。
- 不要回傳 `password_hash` 到 API response。

### 4.6 初始化 admin 競態處理（P0）

第一個 admin 建立流程不能只靠：

```go
if CountUsers() == 0 {
    CreateUser(admin)
}
```

此流程在併發請求下會出現：A 和 B 同時看到 `CountUsers() == 0`，然後建立兩個不同 username 的 admin。`username` unique constraint 無法阻止不同 username 的雙 admin 競態。

必須新增專用方法：

```go
func (s *Store) CreateInitialAdmin(ctx context.Context, u User) (User, error)
```

可行設計方向：

1. **交易 + dialect-specific lock**
   - SQLite：`BEGIN IMMEDIATE` 後查 count 再 insert。
   - PostgreSQL：transaction + advisory lock 或 lock table。
   - MySQL：transaction + table lock / named lock。
2. **bootstrap sentinel row**
   - 新增輕量 `settings` / `bootstrap_state` 表。
   - 用固定 key，例如 `initial_admin_created`，以 unique insert 搶初始化權。
   - 只有成功插入 sentinel 的請求可以建立第一個 admin。

第一版建議採用「bootstrap sentinel row」或「交易 + dialect lock」其中一種，並寫測試覆蓋並發初始化。

---

## 5. 密碼與 JWT 設計

### 5.1 密碼處理

使用：

```go
golang.org/x/crypto/bcrypt
```

建議：

- 使用 `bcrypt.DefaultCost`。
- 密碼最小長度至少 8，建議 12。
- 密碼最大長度建議限制，例如 256，避免過大的 request body 造成 bcrypt 計算與記憶體風險。
- 登入失敗回應統一為「帳號或密碼錯誤」，不要透露 username 是否存在。
- 註冊與登入都不要 log 明文密碼。

### 5.2 JWT library 選型

建議使用：

```go
github.com/golang-jwt/jwt/v5
```

理由：

- 認證安全邏輯不適合自行重造。
- 只新增一個成熟依賴，成本可接受。
- 可明確限制 signing method，降低 `alg=none` 或 alg confusion 類問題。

實作要求：

- 僅接受 HMAC SHA-256（HS256）。
- 驗證時明確檢查 `token.Method == jwt.SigningMethodHS256`。
- 驗證 `exp`、`iat`。
- 驗證 `sub` 可解析為 user id。
- 解析成功後仍需用 `sub` 回查 DB，確認使用者存在、未 disabled。

### 5.3 JWT Claims

建議 Claims：

```json
{
  "sub": "<user_id>",
  "username": "admin",
  "role": "admin",
  "iat": 1710000000,
  "exp": 1710086400
}
```

必要欄位：

- `sub`：user id。
- `username`：顯示用。
- `role`：前端顯示 / 快速判斷用；後端授權仍以 DB 狀態為準。
- `iat`：簽發時間。
- `exp`：過期時間。

建議 session 有效期：

- 預設 12h 或 24h。
- 透過 `AUTH_SESSION_TTL` 設定，例如 `24h`。

### 5.4 JWT Secret 設定

新增設定：

| 環境變數 | JSON 欄位 | 說明 |
| :--- | :--- | :--- |
| `AUTH_JWT_SECRET` | `auth_jwt_secret` | JWT 簽章 secret，正式環境強烈建議必填 |
| `AUTH_COOKIE_SECURE` | `auth_cookie_secure` | 是否加上 Cookie `Secure` |
| `AUTH_SESSION_TTL` | `auth_session_ttl` | Session 有效期，例如 `24h` |

建議規則：

- 若 `AUTH_JWT_SECRET` 有設定，長度必須至少 32 bytes。
- 若未設定：
  - 啟動時產生 random secret。
  - log 明確警告：`AUTH_JWT_SECRET is not set; sessions will be invalidated on restart`。
  - 允許啟動，避免破壞輕量部署與本機開發體驗。
- 不使用 `ADMIN_TOKEN` 當 JWT secret。
- 文件需明確警告正式環境務必設定 `AUTH_JWT_SECRET`。

此規則比「生產環境必填」更容易落地，因為目前專案沒有明確 `APP_ENV=production` 概念。

---

## 6. Cookie 與 CSRF 策略

### 6.1 Cookie 屬性

登入成功後設定：

```http
Set-Cookie: ai_hub_session=<jwt>; Path=/; HttpOnly; SameSite=Lax
```

正式 HTTPS 部署應加：

```http
Secure
```

建議固定 cookie name：

```txt
ai_hub_session
```

登出時清除：

```http
Set-Cookie: ai_hub_session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0
```

若 `AUTH_COOKIE_SECURE=true`，登入與登出清 cookie 都應一致加上 `Secure`。

注意：

- 本機 `http://localhost:8080` 若強制 `Secure`，瀏覽器不會保存 Cookie。
- 因此 `AUTH_COOKIE_SECURE` 需可配置，預設建議 `false`。
- 若使用 Cloudflare / Caddy / Nginx 終止 TLS，正式部署應設為 `true`。

### 6.2 CSRF 防護

HttpOnly Cookie 可防止 XSS 直接讀取 JWT，但會引入 CSRF 風險。第一版最低要求：

- Cookie 使用 `SameSite=Lax`。
- 對會改狀態的 API 僅接受 JSON body。
- 對 `POST` / `PUT` / `PATCH` / `DELETE` 檢查 `Origin` 或 `Referer` 是否與服務端 Host 同源。

建議新增 middleware：

```go
func (s *Server) requireSameOriginForMutating(next http.Handler) http.Handler
```

規則：

- 只對 `POST` / `PUT` / `PATCH` / `DELETE` 生效。
- 只套用在 Cookie auth 的 `/api/*` 管理端點，不套用 `/v1/*`。
- `Origin` 優先於 `Referer`。
- 同源檢查第一版以 `r.Host` 為準。
- 若部署在反向代理後，文件需提醒保留原 Host 或正確設定 forwarded headers。
- `Content-Type` 不可用字串完全等於 `application/json`，需使用 `mime.ParseMediaType`，接受 `application/json; charset=utf-8`。

後續若要更完整，可新增 double submit CSRF token：

- 登入時回傳 CSRF token 或設置非 HttpOnly CSRF Cookie。
- 前端對 mutating request 加 `X-CSRF-Token`。
- 後端比對 token。

---

## 7. API 變更規劃

### 7.1 認證 API (`/api/auth`)

| 方法 | 路徑 | 說明 |
| :--- | :--- | :--- |
| `POST` | `/api/auth/login` | 輸入帳號密碼，成功後設定 Cookie 並回傳使用者資訊 |
| `POST` | `/api/auth/logout` | 清除 Cookie |
| `GET` | `/api/auth/profile` | 回傳目前登入使用者資訊；未登入回 401 |
| `POST` | `/api/auth/register` | 建立使用者；初始化時可建立第一個 admin，之後僅 admin 可呼叫 |

### 7.2 初始化註冊規則

當 `users` 表為空時：

- `POST /api/auth/register` 可建立第一個使用者。
- 第一個使用者強制 `role = admin`。
- request body 即使帶 `role=user` 也必須忽略或覆蓋為 `admin`。
- 必須透過 `Store.CreateInitialAdmin` 處理競態，不可只用 handler 層 `CountUsers` 判斷。

當 `users` 表非空時：

- `POST /api/auth/register` 需要 admin 權限。
- 只有 admin 可以建立其他 admin 或 user。

建議 request：

```json
{
  "username": "admin",
  "password": "change-this-password",
  "role": "admin"
}
```

建議 response：

```json
{
  "id": 1,
  "username": "admin",
  "role": "admin",
  "disabled": false,
  "created_at": "2026-05-23T00:00:00Z",
  "last_login_at": null
}
```

不得回傳：

- `password`
- `password_hash`
- JWT 本體（JWT 只放 HttpOnly Cookie）

### 7.3 權限控制 API

新增或調整中間件：

```go
requireUser(next http.Handler) http.Handler
requireAdmin(next http.Handler) http.Handler
requireAccessToken(next http.Handler) http.Handler
requireSameOriginForMutating(next http.Handler) http.Handler
```

職責：

- `requireUser`：解析 JWT Cookie，檢查過期、使用者存在、未 disabled。
- `requireAdmin`：允許 admin JWT 或 legacy `ADMIN_TOKEN`。
- `requireAccessToken`：維持既有 `/v1/*` token 白名單邏輯，不受 Cookie 登入影響。
- `requireSameOriginForMutating`：保護 Cookie auth 的 mutating 管理 API。

`requireAdmin` 建議流程：

1. 嘗試從 Cookie 解析 JWT。
2. 若 JWT 有效，回查 DB，確認使用者存在、未 disabled、role 是 `admin`。
3. 若 Cookie 不存在或無效，再 fallback legacy `Authorization: Bearer <ADMIN_TOKEN>`。
4. 若仍無效，再 fallback deprecated `?admin_token=<ADMIN_TOKEN>`。
5. 全部失敗回 401。

---

## 8. RBAC 權限矩陣

第一版保守處理：`user` role 先保留，但主要管理能力僅 admin 可用。

| API | admin | user | legacy `ADMIN_TOKEN` |
| :--- | :---: | :---: | :---: |
| `GET /api/auth/profile` | yes | yes | no |
| `POST /api/auth/register` | yes | no | yes |
| `GET /api/summary` | yes | no | yes |
| `GET /api/recent` | yes | no | yes |
| `GET /api/runtime` | yes | no | yes |
| `GET /api/providers` | yes | no | yes |
| `PUT /api/providers` | yes | no | yes |
| `POST /api/reload` | yes | no | yes |
| `/v1/*` | access token | access token | access token |

建議第一版：

- Dashboard 管理功能全部要求 admin。
- `user` 可以登入，但暫不開放供應商管理、runtime、summary、recent。
- 若登入者是 `user`，Dashboard 可顯示「此帳號目前沒有管理權限」。
- 若未來要讓 user 查看 summary / recent，需先確認是否會洩漏供應商名稱、模型名稱、來源 IP、token usage 等資訊。

---

## 9. 前端變更規劃

### 9.1 `dashboard.html`

新增：

- `login-container`：登入表單。
- `bootstrap-container`：初始化第一個 admin 表單。
- `dashboard-container`：原本 dashboard 內容。
- 角色提示區：普通 `user` 登入後顯示無管理權限提示。

移除或隱藏：

- 現有 `admin-token` 輸入框。

### 9.2 `dashboard.js`

調整啟動流程：

1. 頁面載入時呼叫：

```js
GET /api/auth/profile
credentials: "same-origin"
```

2. 若 200：

- 顯示 Dashboard。
- 根據 `role` 決定 UI 可見性。
- 第一版只有 `admin` 顯示管理功能。

3. 若 401：

- 顯示登入表單。
- 若後端提供「尚無使用者」狀態，可切換到初始化 admin 表單。

4. 登入：

```js
POST /api/auth/login
body: { username, password }
credentials: "same-origin"
```

5. 登出：

```js
POST /api/auth/logout
credentials: "same-origin"
```

6. 所有管理 API fetch 改為：

```js
fetch(path, {
  ...options,
  credentials: "same-origin",
  headers: {
    "Content-Type": "application/json"
  }
})
```

7. 不再將 admin token 存入 `localStorage`。

### 9.3 `index.html` / `app.js`

目前 landing page 會嘗試讀 `localStorage` 的 admin token 查 summary/runtime。導入 Cookie 登入後需調整：

- 移除 `localStorage.getItem("ai-hub-admin-token")`。
- 若已登入，可用 `fetch(..., { credentials: "same-origin" })` 嘗試讀取 summary/runtime。
- 若未登入，landing page 不顯示管理數據或顯示「登入後查看」。

---

## 10. 建議檔案切分

可能新增：

```txt
internal/auth/auth.go        # Claims、Service、Cookie helper
internal/auth/password.go    # bcrypt hash / verify
internal/auth/jwt.go         # JWT issue / parse
internal/auth/auth_test.go
internal/store/user.go       # User model + CRUD + initial admin creation
```

可能修改：

```txt
go.mod                          # 新增 github.com/golang-jwt/jwt/v5
internal/config/config.go        # AUTH_JWT_SECRET / AUTH_COOKIE_SECURE / AUTH_SESSION_TTL
internal/store/dialect.go        # users table schema；如採 sentinel，也加入 bootstrap/settings schema
internal/store/store.go          # migrate 跑新 schema；必要時掛 user helpers
internal/store/store_test.go     # user CRUD / initial admin 競態測試
internal/server/server.go        # /api/auth/* routes + requireUser/requireAdmin/CSRF 調整
internal/server/server_test.go   # auth / RBAC / legacy admin token 測試
internal/server/web/dashboard.html
internal/server/web/dashboard.js
internal/server/web/app.js
README.md
README.en.md
openapi.yaml
agent/deep_todos.md
agent/項目表.md
agent/memory.md
```

---

## 11. 演進步驟

### Stage 0：安全與資料模型定稿

1. 確認 username normalization 規則。
2. 確認 JWT library：`github.com/golang-jwt/jwt/v5`。
3. 確認 cookie name：`ai_hub_session`。
4. 確認 `AUTH_JWT_SECRET` fallback 規則。
5. 確認 CSRF middleware 規則。
6. 確認 initial admin 競態解法。
7. 確認 `User.LastLoginAt` 使用 `*time.Time`。

### Stage 1：後端資料結構

1. 在三個 dialect 新增 `users` schema。
2. 若採 sentinel 設計，新增 `settings` / `bootstrap_state` schema。
3. 新增 `store.User` 與 CRUD 方法。
4. 新增 `CreateInitialAdmin`，處理併發初始化。
5. 補 SQLite 為主的 store 測試；dialect schema 至少要能通過既有 migrate。
6. 補並發初始化測試：同時註冊兩個不同 username，最多只能成功一個。

### Stage 2：Auth Service

1. 新增 bcrypt hash / verify。
2. 新增 JWT issue / parse。
3. 新增 Cookie helper。
4. 新增 config 欄位與 env / JSON 載入。
5. 未設定 `AUTH_JWT_SECRET` 時產生 random secret 並 log warning。

### Stage 3：Auth API 與中間件

1. 新增 `/api/auth/login`。
2. 新增 `/api/auth/logout`。
3. 新增 `/api/auth/profile`。
4. 新增 `/api/auth/register`。
5. 新增 `requireUser`。
6. 調整 `requireAdmin`：支援 admin JWT + legacy `ADMIN_TOKEN`。
7. 新增 `requireSameOriginForMutating`。
8. 保持 `requireAccessToken` 不變。

### Stage 4：Dashboard 改造

1. 新增登入 UI。
2. 新增初始化第一個 admin UI。
3. 移除 `localStorage` 儲存 admin token。
4. 所有 `/api/*` fetch 改用 Cookie credentials。
5. 根據 role 隱藏或停用非授權 UI。
6. 調整 landing page 的 summary/runtime 讀取邏輯。

### Stage 5：文件與測試

1. README / README.en 補帳號系統、初始化 admin、Cookie secure 設定、`AUTH_JWT_SECRET` 說明。
2. OpenAPI 補 `/api/auth/*`。
3. 補 server auth 測試：
   - 無 user 時可建立第一個 admin。
   - 並發初始化只能成功一個 admin。
   - 有 user 後 register 需 admin。
   - login 成功設定 `HttpOnly` / `SameSite=Lax` cookie。
   - `AUTH_COOKIE_SECURE=true` 時 cookie 有 `Secure`。
   - logout 成功清除 cookie。
   - disabled user 無法登入。
   - disabled user 即使持有舊 cookie 也不能呼叫 profile/admin API。
   - user 不能呼叫 admin-only API。
   - legacy `Authorization: Bearer <ADMIN_TOKEN>` 仍可呼叫 admin API。
   - legacy `?admin_token=` 仍可呼叫 admin API，但文件標 deprecated。
   - `/v1/*` 仍只使用 `ACCESS_TOKENS`，不受 Dashboard Cookie 影響。
   - mutating `/api/*` 缺少合法 Origin / Referer 時被拒絕。

---

## 12. 風險與注意事項

1. **初始化流程要防競態**：第一個 admin 建立必須處理併發請求，這是 P0。
2. **JWT secret 是 P0 安全要求**：正式環境務必設定 `AUTH_JWT_SECRET`；未設定只能靠啟動時 random secret，重啟後 session 全失效。
3. **Cookie Secure 不能無腦開啟**：本機 HTTP 會無法登入，需要設定控制。
4. **CSRF 不可忽略**：使用 Cookie 後至少要有 SameSite 與 Origin / Referer 檢查。
5. **不要破壞 SDK 使用方式**：`/v1/*` 必須保留 Authorization Bearer token。
6. **不要只靠前端隱藏 UI 做授權**：所有 admin-only 行為必須後端檢查。
7. **DB schema 第一版要設計完整**：目前沒有正式 migration framework，後續 ALTER TABLE 成本較高。
8. **不要回傳 password_hash**：任何 auth API 都不能把 hash 傳到前端。
9. **username 大小寫必須正規化**：避免 SQLite / MySQL / PostgreSQL unique 行為不一致。
10. **provider API keys 暴露風險仍存在**：目前 `/api/providers` 會回傳 `api_keys` 供 Dashboard 編輯；HttpOnly Cookie 只解決 ADMIN_TOKEN localStorage 風險，不代表 XSS 後完全拿不到 provider keys。若要更安全，後續需設計 masked key / write-only key update。
11. **legacy query token 風險高**：`?admin_token=` 會進 log 和 history，必須標 deprecated。

---

## 13. 建議結論

此方案可行，但應採漸進式落地，並先補齊初始化競態、username normalization、JWT library/secret 策略、CSRF middleware、nullable time 欄位等 P0/P1 細節。

第一版優先把 Dashboard / `/api/*` 從 `ADMIN_TOKEN + localStorage` 升級為帳密登入與 HttpOnly JWT Cookie，同時保留 `ADMIN_TOKEN` 作為相容與緊急入口。`/v1/*` 模型代理 API 繼續使用既有 `ACCESS_TOKENS`，避免破壞 OpenAI SDK、curl 與服務端整合。

`user` role 先納入 schema，但管理功能以 admin 為主。真正的普通用戶權限、per-user API key、quota、rate limit，建議在下一階段與既有 `rate limit / per-token quota` backlog 合併設計。
