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
	"sync"
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
	var mu sync.Mutex
	var capturedPath, capturedKey, capturedVersion string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedKey = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()
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
	mu.Lock()
	cPath, cKey, cVersion := capturedPath, capturedKey, capturedVersion
	cBody := capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/messages") {
		t.Fatalf("expected /v1/messages, got %s", cPath)
	}
	if cKey != "ak-test" {
		t.Fatalf("expected x-api-key=ak-test, got %q", cKey)
	}
	if cVersion == "" {
		t.Fatalf("missing anthropic-version header")
	}
	if cBody["model"] != "claude-3-5-sonnet-20240620" {
		t.Fatalf("expected model in upstream body, got %v", cBody["model"])
	}
	if cBody["system"] != "be brief" {
		t.Fatalf("expected system hoisted, got %v", cBody["system"])
	}
	msgs, ok := cBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected exactly one non-system message in Anthropic body, got %v", cBody["messages"])
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
	var mu sync.Mutex
	var capturedPath, capturedKey string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedKey = r.Header.Get("x-goog-api-key")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()
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
	mu.Lock()
	cPath, cKey := capturedPath, capturedKey
	cBody := capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1beta/models/gemini-1.5-pro:generateContent") {
		t.Fatalf("expected gemini generateContent path, got %s", cPath)
	}
	if cKey != "gk-test" {
		t.Fatalf("expected x-goog-api-key=gk-test, got %q", cKey)
	}
	sys, _ := cBody["systemInstruction"].(map[string]any)
	if sys == nil {
		t.Fatalf("expected systemInstruction in body, got %v", cBody)
	}
	if _, ok := cBody["contents"]; !ok {
		t.Fatalf("expected contents key in gemini body, got %v", cBody)
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

func TestServeChatCompletions_AnthropicStreamingTranslation(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedKey string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n")
		_, _ = io.WriteString(w, `data: {"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"claude-3-5-sonnet-20240620","content":[]}}`+"\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello stream claude"}}`+"\n\n")
		_, _ = io.WriteString(w, "event: message_delta\n")
		_, _ = io.WriteString(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`+"\n\n")
		_, _ = io.WriteString(w, "event: message_stop\n")
		_, _ = io.WriteString(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "stream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "anthropic", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"k"},
		Models:     []string{"claude-3-5-sonnet-20240620"},
		TimeoutSec: 5,
	}}}
	p := New(cfg, st)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"claude-3-5-sonnet-20240620","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	p.ServeChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	cPath, cKey, cBody := capturedPath, capturedKey, capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/messages") {
		t.Fatalf("expected anthropic messages path, got %s", cPath)
	}
	if cKey != "k" {
		t.Fatalf("expected x-api-key, got %q", cKey)
	}
	if cBody["stream"] != true {
		t.Fatalf("expected upstream stream=true, got %v", cBody["stream"])
	}
	out := rec.Body.String()
	if !strings.Contains(out, "chat.completion.chunk") || !strings.Contains(out, "hello stream claude") || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("expected OpenAI stream chunks, got %s", out)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Fatalf("expected no-cache header, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("expected X-Accel-Buffering=no, got %q", got)
	}

	rows, err := st.RecentCalls(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != 200 || rows[0].Provider != "anthropic" {
		t.Fatalf("bad log: %+v", rows)
	}
}

func TestServeAnthropicMessages_OpenAIUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-native-anthropic",
			"object":"chat.completion",
			"created":1710000000,
			"model":"gpt-4o-mini",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"hello anthropic client"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10}
		}`))
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "anthropic-in.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-up"},
		Models:     []string{"gpt-4o-mini"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{
		"model":"gpt-4o-mini",
		"max_tokens":64,
		"system":"be concise",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	rec := httptest.NewRecorder()
	p.ServeAnthropicMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	cPath, cAuth := capturedPath, capturedAuth
	cBody := capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/chat/completions") {
		t.Fatalf("expected OpenAI chat path, got %s", cPath)
	}
	if cAuth != "Bearer sk-up" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["model"] != "gpt-4o-mini" {
		t.Fatalf("expected upstream model, got %v", cBody["model"])
	}
	msgs, _ := cBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system + user messages upstream, got %v", cBody["messages"])
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["type"] != "message" {
		t.Fatalf("expected Anthropic message response, got %v", resp["type"])
	}
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("expected stop_reason=end_turn, got %v", resp["stop_reason"])
	}
	content, _ := resp["content"].([]any)
	if len(content) == 0 || !strings.Contains(toStringForTest(content), "hello anthropic client") {
		t.Fatalf("expected translated content, got %v", resp["content"])
	}
	if got := rec.Header().Get("X-AI-Hub-Provider"); got != "openai" {
		t.Fatalf("expected provider header openai, got %q", got)
	}

	rows, err := st.RecentCalls(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != 200 || rows[0].Provider != "openai" || rows[0].Path != "/v1/messages" {
		t.Fatalf("bad log: %+v", rows)
	}
}

func TestServeGeminiGenerateContent_OpenAIUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-native-gemini",
			"object":"chat.completion",
			"created":1710000000,
			"model":"gpt-4o-mini",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"hello gemini client"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10}
		}`))
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "gemini-in.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-up"},
		Models:     []string{"gpt-4o-mini"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{
		"systemInstruction":{"parts":[{"text":"be concise"}]},
		"contents":[{"role":"user","parts":[{"text":"hi"}]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o-mini:generateContent", body)
	rec := httptest.NewRecorder()
	p.ServeGeminiGenerateContent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	cPath, cAuth := capturedPath, capturedAuth
	cBody := capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/chat/completions") {
		t.Fatalf("expected OpenAI chat path, got %s", cPath)
	}
	if cAuth != "Bearer sk-up" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["model"] != "gpt-4o-mini" {
		t.Fatalf("expected upstream model, got %v", cBody["model"])
	}
	if _, ok := cBody["messages"]; !ok {
		t.Fatalf("expected OpenAI messages upstream, got %v", cBody)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	candidates, _ := resp["candidates"].([]any)
	if len(candidates) == 0 {
		t.Fatalf("expected Gemini candidates, got %v", resp)
	}
	first, _ := candidates[0].(map[string]any)
	if first["finishReason"] != "STOP" {
		t.Fatalf("expected finishReason=STOP, got %v", first["finishReason"])
	}
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) == 0 || !strings.Contains(toStringForTest(parts), "hello gemini client") {
		t.Fatalf("expected translated Gemini content, got %v", content)
	}
	usage, _ := resp["usageMetadata"].(map[string]any)
	if usage == nil || toIntForTest(usage["totalTokenCount"]) != 10 {
		t.Fatalf("expected totalTokenCount=10, got %v", resp["usageMetadata"])
	}
}

func TestServeAnthropicMessages_OpenAIStreamingUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hello anthropic stream"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "anthropic-stream-in.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-up"},
		Models:     []string{"gpt-4o-mini"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	body := bytes.NewBufferString(`{
		"model":"gpt-4o-mini",
		"max_tokens":64,
		"stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	rec := httptest.NewRecorder()
	p.ServeAnthropicMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	cPath, cAuth, cBody := capturedPath, capturedAuth, capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/chat/completions") {
		t.Fatalf("expected OpenAI chat path, got %s", cPath)
	}
	if cAuth != "Bearer sk-up" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["stream"] != true {
		t.Fatalf("expected upstream stream=true, got %v", cBody["stream"])
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: content_block_delta") || !strings.Contains(out, "hello anthropic stream") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("expected Anthropic stream events, got %s", out)
	}
}

func TestServeGeminiGenerateContent_OpenAIStreamingUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-gemini-stream","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hello gemini stream"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-gemini-stream","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "gemini-stream-in.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-up"},
		Models:     []string{"gpt-4o-mini"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o-mini:streamGenerateContent",
		bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	rec := httptest.NewRecorder()
	p.ServeGeminiGenerateContent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	cPath, cAuth, cBody := capturedPath, capturedAuth, capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/chat/completions") {
		t.Fatalf("expected OpenAI chat path, got %s", cPath)
	}
	if cAuth != "Bearer sk-up" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["stream"] != true {
		t.Fatalf("expected upstream stream=true, got %v", cBody["stream"])
	}
	out := rec.Body.String()
	if !strings.Contains(out, "hello gemini stream") || !strings.Contains(out, "\"finishReason\":\"STOP\"") {
		t.Fatalf("expected Gemini stream chunks, got %s", out)
	}
}

func TestServeEmbeddings_OpenAICompatibleUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []any{map[string]any{
				"object":    "embedding",
				"index":     0,
				"embedding": []float64{0.1, 0.2},
			}},
			"model": "text-embedding-3-small",
			"usage": map[string]any{"prompt_tokens": 2, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "embeddings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-emb"},
		Models:     []string{"text-embedding-3-small"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		bytes.NewBufferString(`{"model":"text-embedding-3-small","input":"hello"}`))
	rec := httptest.NewRecorder()
	p.ServeEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	cPath, cAuth, cBody := capturedPath, capturedAuth, capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/embeddings") {
		t.Fatalf("expected embeddings path, got %s", cPath)
	}
	if cAuth != "Bearer sk-emb" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["model"] != "text-embedding-3-small" || cBody["input"] != "hello" {
		t.Fatalf("unexpected upstream body: %v", cBody)
	}
	if got := rec.Header().Get("X-AI-Hub-Provider"); got != "openai" {
		t.Fatalf("expected provider header, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "embedding") {
		t.Fatalf("expected upstream response passthrough, got %s", rec.Body.String())
	}
	rows, err := st.RecentCalls(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != 200 || rows[0].Provider != "openai" || rows[0].Path != "/v1/embeddings" {
		t.Fatalf("bad log: %+v", rows)
	}
}

func TestServeCompletions_OpenAICompatibleUpstream(t *testing.T) {
	var mu sync.Mutex
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "cmpl-test",
			"object":  "text_completion",
			"model":   capturedBody["model"],
			"choices": []any{map[string]any{"text": "hello completion", "index": 0, "finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmp, "completions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk-cmpl"},
		Models:     []string{"gpt-3.5-turbo-instruct"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions",
		bytes.NewBufferString(`{"model":"gpt-3.5-turbo-instruct","prompt":"hello"}`))
	rec := httptest.NewRecorder()
	p.ServeCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	cPath, cAuth, cBody := capturedPath, capturedAuth, capturedBody
	mu.Unlock()
	if !strings.HasSuffix(cPath, "/v1/completions") {
		t.Fatalf("expected completions path, got %s", cPath)
	}
	if cAuth != "Bearer sk-cmpl" {
		t.Fatalf("expected bearer auth, got %q", cAuth)
	}
	if cBody["model"] != "gpt-3.5-turbo-instruct" || cBody["prompt"] != "hello" {
		t.Fatalf("unexpected upstream body: %v", cBody)
	}
	if !strings.Contains(rec.Body.String(), "hello completion") {
		t.Fatalf("expected upstream response passthrough, got %s", rec.Body.String())
	}
}

func TestServeCompletions_StreamPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: hello completions\\n\\n")
	}))
	defer upstream.Close()

	cfg := &config.Config{Providers: []config.Provider{{
		Name: "openai", Enabled: true,
		BaseURL:    upstream.URL,
		APIKeys:    []string{"sk"},
		Models:     []string{"gpt-3.5-turbo-instruct"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions",
		bytes.NewBufferString(`{"model":"gpt-3.5-turbo-instruct","prompt":"hello","stream":true}`))
	rec := httptest.NewRecorder()
	p.ServeCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: hello completions") {
		t.Fatalf("expected SSE passthrough, got %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("expected X-Accel-Buffering=no, got %q", got)
	}
}

func TestServeEmbeddings_NonOpenAIProviderRejected(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "anthropic", Enabled: true,
		BaseURL:    "https://api.anthropic.com",
		APIKeys:    []string{"ak"},
		Models:     []string{"claude-3-5-sonnet-20240620"},
		TimeoutSec: 10,
	}}}
	p := New(cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		bytes.NewBufferString(`{"model":"claude-3-5-sonnet-20240620","input":"hello"}`))
	rec := httptest.NewRecorder()
	p.ServeEmbeddings(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rec.Code, rec.Body.String())
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
