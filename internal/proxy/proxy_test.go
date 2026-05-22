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
	st, err := store.OpenSQLite(filepath.Join(tmp, "test.db"))
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
	st, _ := store.OpenSQLite(filepath.Join(tmp, "t.db"))
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

// TestServeChatCompletions_AnthropicTranslation verifies the OpenAI-in →
// Anthropic-out → OpenAI-back round-trip works against a fake Anthropic
// /v1/messages endpoint.
func TestServeChatCompletions_AnthropicTranslation(t *testing.T) {
	var capturedPath, capturedKey, capturedVersion string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedKey = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		// Reply with a minimal Anthropic /v1/messages response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"claude-3-5-sonnet-20240620",
			"content":[{"type":"text","text":"hello from claude"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":7,"output_tokens":3}
		}`))
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "anthropic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "anthropic", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"ak-test"},
		Models:     []string{"claude-3-5-sonnet-20240620"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{
		"model":"claude-3-5-sonnet-20240620",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"hi"}
		],
		"max_tokens": 64
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Upstream request shape — should be native Anthropic.
	if !strings.HasSuffix(capturedPath, "/v1/messages") {
		t.Fatalf("expected /v1/messages, got %s", capturedPath)
	}
	if capturedKey != "ak-test" {
		t.Fatalf("expected x-api-key=ak-test, got %q", capturedKey)
	}
	if capturedVersion == "" {
		t.Fatalf("missing anthropic-version header")
	}
	if capturedBody["model"] != "claude-3-5-sonnet-20240620" {
		t.Fatalf("expected model in upstream body, got %v", capturedBody["model"])
	}
	if capturedBody["system"] != "be brief" {
		t.Fatalf("expected system hoisted, got %v", capturedBody["system"])
	}
	msgs, ok := capturedBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected exactly one non-system message in Anthropic body, got %v", capturedBody["messages"])
	}

	// Downstream response shape — should be OpenAI-compatible.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["object"] != "chat.completion" {
		t.Fatalf("expected object=chat.completion, got %v", resp["object"])
	}
	if resp["model"] != "claude-3-5-sonnet-20240620" {
		t.Fatalf("expected model=claude-3-5-sonnet-20240620, got %v", resp["model"])
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected at least one choice, got %v", resp["choices"])
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	if !strings.Contains(toStringForTest(msg["content"]), "hello from claude") {
		t.Fatalf("expected translated content, got %v", msg["content"])
	}
	if first["finish_reason"] != "stop" {
		t.Fatalf("expected finish_reason=stop, got %v", first["finish_reason"])
	}

	if got := rec.Header().Get("X-AI-Hub-Provider"); got != "anthropic" {
		t.Fatalf("expected provider header anthropic, got %q", got)
	}

	rows, err := st.RecentCalls(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != 200 || rows[0].Provider != "anthropic" {
		t.Fatalf("bad log: %+v", rows)
	}
}

// TestServeChatCompletions_GeminiTranslation verifies the OpenAI-in →
// Gemini-out → OpenAI-back round-trip against a fake Gemini
// :generateContent endpoint.
func TestServeChatCompletions_GeminiTranslation(t *testing.T) {
	var capturedPath, capturedKey string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedKey = r.Header.Get("x-goog-api-key")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{"role":"model","parts":[{"text":"hello from gemini"}]},
				"finishReason":"STOP",
				"index":0
			}],
			"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"totalTokenCount":9}
		}`))
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "gemini.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "gemini", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"gk-test"},
		Models:     []string{"gemini-1.5-pro"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{
		"model":"gemini-1.5-pro",
		"messages":[
			{"role":"system","content":"act as poet"},
			{"role":"user","content":"hi"}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Upstream shape — native Gemini.
	if !strings.HasSuffix(capturedPath, "/v1beta/models/gemini-1.5-pro:generateContent") {
		t.Fatalf("expected gemini generateContent path, got %s", capturedPath)
	}
	if capturedKey != "gk-test" {
		t.Fatalf("expected x-goog-api-key=gk-test, got %q", capturedKey)
	}
	sys, _ := capturedBody["systemInstruction"].(map[string]any)
	if sys == nil {
		t.Fatalf("expected systemInstruction in body, got %v", capturedBody)
	}
	if _, ok := capturedBody["contents"]; !ok {
		t.Fatalf("expected contents key in gemini body, got %v", capturedBody)
	}

	// Downstream response shape — OpenAI compatible.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["object"] != "chat.completion" {
		t.Fatalf("expected object=chat.completion, got %v", resp["object"])
	}
	if resp["model"] != "gemini-1.5-pro" {
		t.Fatalf("expected model=gemini-1.5-pro, got %v", resp["model"])
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected at least one choice, got %v", resp["choices"])
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	if !strings.Contains(toStringForTest(msg["content"]), "hello from gemini") {
		t.Fatalf("expected translated content, got %v", msg["content"])
	}
	if first["finish_reason"] != "stop" {
		t.Fatalf("expected finish_reason=stop, got %v", first["finish_reason"])
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil || toIntForTest(usage["total_tokens"]) != 9 {
		t.Fatalf("expected usage.total_tokens=9, got %v", resp["usage"])
	}
}

// TestServeChatCompletions_AnthropicStreamingRejected ensures streaming for
// non-OpenAI providers is explicitly rejected for now (Stage 4 will lift it).
func TestServeChatCompletions_AnthropicStreamingRejected(t *testing.T) {
	tmp := t.TempDir()
	st, _ := store.OpenSQLite(filepath.Join(tmp, "stream.db"))
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "anthropic", Enabled: true,
		BaseURL:    "https://unused.invalid",
		APIKeys:    []string{"k"},
		Models:     []string{"claude-3-5-sonnet-20240620"},
		TimeoutSec: 5,
	}}}
	p := New(cfg, st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"claude-3-5-sonnet-20240620","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func toStringForTest(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []any:
		var b strings.Builder
		for _, p := range s {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func toIntForTest(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
