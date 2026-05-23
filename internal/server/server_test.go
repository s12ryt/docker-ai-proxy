package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/reload?admin_token=test-admin", nil))
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
