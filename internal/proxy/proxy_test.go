package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

func TestBuildUpstreamRequest_OpenAI(t *testing.T) {
	url, hdrs, err := buildUpstreamRequest(config.Provider{
		Name: "openai", BaseURL: "https://api.openai.com",
	}, "/v1/chat/completions", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url=%s", url)
	}
	if hdrs["Authorization"] != "Bearer sk-test" {
		t.Fatalf("auth=%q", hdrs["Authorization"])
	}
}

func TestBuildUpstreamRequest_Anthropic(t *testing.T) {
	url, hdrs, err := buildUpstreamRequest(config.Provider{
		Name: "anthropic", BaseURL: "https://api.anthropic.com/",
	}, "/v1/chat/completions", "ak-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(url, "/v1/messages") {
		t.Fatalf("anthropic should remap path, got %s", url)
	}
	if hdrs["x-api-key"] != "ak-test" {
		t.Fatalf("missing x-api-key")
	}
	if hdrs["anthropic-version"] == "" {
		t.Fatalf("missing anthropic-version")
	}
}

func TestBuildUpstreamRequest_Anthropic_NoKey(t *testing.T) {
	if _, _, err := buildUpstreamRequest(config.Provider{
		Name: "anthropic", BaseURL: "https://api.anthropic.com",
	}, "/v1/chat/completions", ""); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestBuildUpstreamRequest_NoBase(t *testing.T) {
	if _, _, err := buildUpstreamRequest(config.Provider{
		Name: "openai", BaseURL: "",
	}, "/x", "k"); err == nil {
		t.Fatal("expected error for missing base_url")
	}
}

func TestServeChatCompletions_EndToEnd(t *testing.T) {
	// Fake upstream that echoes the model and returns a known body.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer up-key" {
			http.Error(w, "no auth", 401)
			return
		}
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "test-1",
			"object":  "chat.completion",
			"model":   got["model"],
			"choices": []any{},
		})
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "openai", Enabled: true,
			BaseURL: upstream.URL, APIKeys: []string{"up-key"},
			Models: []string{"gpt-4o-mini"}, TimeoutSec: 10,
		}},
	}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["model"] != "gpt-4o-mini" {
		t.Fatalf("expected model echo, got %v", resp["model"])
	}
	if got := rec.Header().Get("X-AI-Hub-Provider"); got != "openai" {
		t.Fatalf("expected provider header, got %q", got)
	}

	// And the store should have logged the call.
	rows, err := st.RecentCalls(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != 200 || rows[0].Provider != "openai" {
		t.Fatalf("bad log: %+v", rows)
	}
}

func TestServeChatCompletions_BadJSON(t *testing.T) {
	cfg := &config.Config{}
	p := New(cfg, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", rec.Code)
	}
}

func TestServeChatCompletions_MissingModel(t *testing.T) {
	cfg := &config.Config{}
	p := New(cfg, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", rec.Code)
	}
}

func TestServeModels(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "openai", Enabled: true, Models: []string{"gpt-4o", "gpt-4o-mini"}},
		{Name: "anthropic", Enabled: false, Models: []string{"claude"}},
	}}
	p := New(cfg, nil)
	rec := httptest.NewRecorder()
	p.ServeModels(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("expected 2 models (only enabled), got %d", len(out.Data))
	}
	for _, m := range out.Data {
		if m.OwnedBy != "openai" {
			t.Fatalf("unexpected owner %s", m.OwnedBy)
		}
	}
}

func TestServeChatCompletions_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"oops"}`, http.StatusBadGateway)
	}))
	defer upstream.Close()
	tmp := t.TempDir()
	st, _ := store.Open(filepath.Join(tmp, "t.db"))
	defer st.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true, BaseURL: upstream.URL,
		APIKeys: []string{"k"}, Models: []string{"m"}, TimeoutSec: 5,
	}}}
	p := New(cfg, st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected upstream status passthrough, got %d", rec.Code)
	}
	b, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(b), "oops") {
		t.Fatalf("body not passed through: %s", b)
	}
}
