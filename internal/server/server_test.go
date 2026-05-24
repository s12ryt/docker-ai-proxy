package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/proxy"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

func newTestServer(t *testing.T) (*Server, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		AdminToken: "test-admin",
		Providers: []config.Provider{{
			Name: "openai", Enabled: true,
			BaseURL: "http://127.0.0.1:1", Models: []string{"m"}, TimeoutSec: 1,
		}},
	}
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(cfg, st, proxy.New(cfg, st)), cfg
}

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, got %v", body)
	}
}

func TestAdminAuth_Required(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdminAuth_OK(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	req.Header.Set("Authorization", "Bearer test-admin")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminAuth_QueryParamFallback(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime?admin_token=test-admin", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRuntimeIncludesDBStats(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime?admin_token=test-admin", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	dbStats, ok := body["db_stats"].(map[string]any)
	if !ok {
		t.Fatalf("expected db_stats map, got %v", body["db_stats"])
	}
	if dbStats["driver"] != "sqlite" {
		t.Fatalf("expected sqlite driver, got %v", dbStats["driver"])
	}
	if _, ok := dbStats["open_connections"]; !ok {
		t.Fatalf("expected open_connections in db_stats, got %v", dbStats)
	}
}

func TestReload_MethodAndOK(t *testing.T) {
	s, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/reload?admin_token=test-admin", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reload?admin_token=test-admin", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, got %v", body)
	}
}

func TestProvidersPutPersistsAndUpdatesRuntimeConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CONFIG_PATH", configPath)

	s, cfg := newTestServer(t)
	payload := []byte(`{"providers":[{"name":" local-openai ","display_name":" Local OpenAI ","base_url":" https://api.openai.com/ ","api_keys":[" key-a ","","key-b"],"models":[" gpt-4o-mini ",""],"enabled":true,"weight":0,"timeout_sec":0}]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/providers?admin_token=test-admin", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	got, ok := cfg.FindProvider("local-openai")
	if !ok {
		t.Fatalf("expected updated provider in runtime config")
	}
	if got.BaseURL != "https://api.openai.com" || got.Weight != 1 || got.TimeoutSec != 120 {
		t.Fatalf("provider was not normalized: %+v", got)
	}
	if len(got.APIKeys) != 2 || got.APIKeys[0] != "key-a" || got.APIKeys[1] != "key-b" {
		t.Fatalf("api keys were not normalized: %+v", got.APIKeys)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Providers) != 1 || saved.Providers[0].Name != "local-openai" {
		t.Fatalf("provider was not persisted: %s", string(data))
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers?admin_token=test-admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	keys, ok := listed[0]["api_keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("expected api_keys in provider list, got %v", listed[0]["api_keys"])
	}
}

func TestProvidersPutRejectsInvalidProvider(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/providers?admin_token=test-admin", bytes.NewReader([]byte(`{"providers":[{"name":"bad/name","base_url":"https://example.com"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAccessTokensPutPersistsAndUpdatesRuntimeConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CONFIG_PATH", configPath)

	s, cfg := newTestServer(t)
	payload := []byte(`{"tokens":[" client-a ","","client-b"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/access-tokens?admin_token=test-admin", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	snap := cfg.Snapshot()
	if len(snap.AccessTokens) != 2 || snap.AccessTokens[0] != "client-a" || snap.AccessTokens[1] != "client-b" {
		t.Fatalf("access tokens were not normalized in runtime config: %+v", snap.AccessTokens)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.AccessTokens) != 2 || saved.AccessTokens[0] != "client-a" || saved.AccessTokens[1] != "client-b" {
		t.Fatalf("access tokens were not persisted: %s", string(data))
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/access-tokens?admin_token=test-admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	tokens, ok := listed["tokens"].([]any)
	if !ok || len(tokens) != 2 || tokens[0] != "client-a" || tokens[1] != "client-b" {
		t.Fatalf("expected access tokens in list, got %v", listed["tokens"])
	}
	if listed["count"] != float64(2) {
		t.Fatalf("expected count=2, got %v", listed["count"])
	}
}

func TestAccessTokensPutRejectsDuplicate(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/access-tokens?admin_token=test-admin", bytes.NewReader([]byte(`{"tokens":["dup","dup"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientsPutPersistsAndUpdatesRuntimeConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CONFIG_PATH", configPath)

	s, cfg := newTestServer(t)
	payload := []byte(`{"clients":[{"name":" Alice ","token":" alice-token ","enabled":true,"daily_limit":100,"rpm_limit":20,"concurrent_limit":3,"allowed_models":[" m ",""],"note":" test "}]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/clients?admin_token=test-admin", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	snap := cfg.Snapshot()
	if len(snap.Clients) != 1 {
		t.Fatalf("expected one client in runtime config, got %+v", snap.Clients)
	}
	client := snap.Clients[0]
	if client.Name != "Alice" || client.Token != "alice-token" || !client.Enabled || client.DailyLimit != 100 || client.RPMLimit != 20 || client.ConcurrentLimit != 3 || client.Note != "test" {
		t.Fatalf("client was not normalized in runtime config: %+v", client)
	}
	if len(client.AllowedModels) != 1 || client.AllowedModels[0] != "m" || client.CreatedAt == "" {
		t.Fatalf("client allowed models/created_at not normalized: %+v", client)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Clients) != 1 || saved.Clients[0].Name != "Alice" || saved.Clients[0].Token != "alice-token" || saved.Clients[0].RPMLimit != 20 || saved.Clients[0].ConcurrentLimit != 3 {
		t.Fatalf("client was not persisted: %s", string(data))
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients?admin_token=test-admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	clients, ok := listed["clients"].([]any)
	if !ok || len(clients) != 1 {
		t.Fatalf("expected clients in list, got %v", listed["clients"])
	}
	if listed["count"] != float64(1) {
		t.Fatalf("expected count=1, got %v", listed["count"])
	}
}

func TestClientToken_OK(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientToken_DisabledRejected(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: false}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientToken_AllowedModelsRejectsUnlisted(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, AllowedModels: []string{"allowed"}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"blocked","messages":[]}`)))
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model not allowed") {
		t.Fatalf("expected model policy error, got body=%s", rec.Body.String())
	}
}

func TestClientToken_AllowedModelsFiltersModels(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Providers[0].Models = []string{"m", "other"}
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, AllowedModels: []string{"m"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "m" {
		t.Fatalf("expected only allowed model m, got %+v", body.Data)
	}
}

func TestClientToken_DailyLimitExceeded(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, DailyLimit: 1}}
	if err := s.store.LogCall(context.Background(), store.CallRecord{ClientName: "alice", Status: http.StatusOK}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "daily limit") {
		t.Fatalf("expected daily limit error, got body=%s", rec.Body.String())
	}
}

func TestClientToken_DailyLimitAllowsBelowLimit(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, DailyLimit: 1}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientToken_RPMLimitExceeded(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, RPMLimit: 1}}
	if err := s.store.LogCall(context.Background(), store.CallRecord{ClientName: "alice", Status: http.StatusOK}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rpm limit") {
		t.Fatalf("expected rpm limit error, got body=%s", rec.Body.String())
	}
}

func TestClientToken_ConcurrentLimitExceeded(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, ConcurrentLimit: 1}}
	release, ok := s.tryAcquireClient(config.Client{Name: "alice", ConcurrentLimit: 1})
	if !ok {
		t.Fatal("expected initial concurrent slot")
	}
	defer release()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "concurrent limit") {
		t.Fatalf("expected concurrent limit error, got body=%s", rec.Body.String())
	}
}

func TestClientToken_ConcurrentLimitAllowsAfterRelease(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.Clients = []config.Client{{Name: "alice", Token: "client-token", Enabled: true, ConcurrentLimit: 1}}
	release, ok := s.tryAcquireClient(config.Client{Name: "alice", ConcurrentLimit: 1})
	if !ok {
		t.Fatal("expected initial concurrent slot")
	}
	release()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthBootstrapAndFirstAdminSession(t *testing.T) {
	s, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/bootstrap", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var bootstrap map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap["bootstrap_required"] != true {
		t.Fatalf("expected bootstrap_required=true, got %v", bootstrap)
	}

	body := []byte(`{"username":" AdminUser ","password":"super-secret","role":"user"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("response leaked password hash: %s", rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	if cookie.Name != sessionCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie not hardened: %+v", cookie)
	}
	var registered map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	user := registered["user"].(map[string]any)
	if user["username"] != "adminuser" || user["role"] != store.RoleAdmin {
		t.Fatalf("first user should be normalized admin, got %v", user)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/profile", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected profile 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected cookie admin summary 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected logout 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	cleared := rec.Result().Cookies()[0]
	if cleared.Name != sessionCookieName || cleared.MaxAge >= 0 {
		t.Fatalf("expected clearing cookie, got %+v", cleared)
	}
}

func TestAuthLoginRejectsWrongPassword(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte(`{"username":"missing","password":"bad-password"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMutatingAPIsRequireJSONContentType(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/reload?admin_token=test-admin", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAccessToken_RequiredWhenNoTokensConfigured(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "access token is not configured") {
		t.Fatalf("expected configuration error, got body=%s", rec.Body.String())
	}
}

func TestAccessToken_RequiredWhenConfigured(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.AccessTokens = []string{"client-tok"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAccessToken_OK(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.AccessTokens = []string{"client-tok"}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-tok")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestExtractBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":  "abc",
		"bearer xyz":  "xyz",
		"raw-token":   "raw-token",
		"":            "",
		"Bearer    s": "s",
	}
	for in, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if in != "" {
			r.Header.Set("Authorization", in)
		}
		if got := extractBearer(r); got != want {
			t.Errorf("extractBearer(%q) = %q, want %q", in, got, want)
		}
	}
}
