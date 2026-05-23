package server

import (
	"bytes"
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

func TestAccessToken_Required(t *testing.T) {
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
