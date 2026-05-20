package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/providers"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

// Proxy forwards OpenAI-compatible requests to the appropriate upstream provider.
type Proxy struct {
	cfg    *config.Config
	store  *store.Store
	picker providers.KeyPicker
	client *http.Client
}

// New constructs a Proxy.
func New(cfg *config.Config, st *store.Store) *Proxy {
	return &Proxy{
		cfg:   cfg,
		store: st,
		client: &http.Client{
			Timeout: 0, // controlled per-request via context
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}
}

// ServeChatCompletions handles `/v1/chat/completions` requests in OpenAI format.
func (p *Proxy) ServeChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	modelRaw, _ := payload["model"].(string)
	modelRaw = strings.TrimSpace(modelRaw)
	if modelRaw == "" {
		writeJSONError(w, http.StatusBadRequest, "missing 'model'")
		return
	}

	provider, model, err := p.cfg.ProviderForModel(modelRaw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Replace model name with provider-native form before forwarding.
	payload["model"] = model
	outBody, _ := json.Marshal(payload)

	rec := store.CallRecord{
		Timestamp: time.Now(),
		Provider:  provider.Name,
		Model:     model,
		Path:      r.URL.Path,
		BytesIn:   int64(len(body)),
		ClientIP:  clientIP(r),
	}
	start := time.Now()

	defer func() {
		rec.LatencyMS = time.Since(start).Milliseconds()
		if p.store != nil {
			_ = p.store.LogCall(context.Background(), rec)
		}
	}()

	upstreamURL, headers, err := buildUpstreamRequest(provider, "/v1/chat/completions", p.picker.Pick(provider))
	if err != nil {
		rec.Status = http.StatusBadGateway
		rec.ErrMessage = err.Error()
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(provider.TimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(outBody))
	if err != nil {
		rec.Status = http.StatusInternalServerError
		rec.ErrMessage = err.Error()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		rec.Status = http.StatusBadGateway
		rec.ErrMessage = err.Error()
		writeJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-AI-Hub-Provider", provider.Name)
	w.WriteHeader(resp.StatusCode)
	rec.Status = resp.StatusCode

	n, copyErr := io.Copy(w, resp.Body)
	rec.BytesOut = n
	if copyErr != nil && !errors.Is(copyErr, context.Canceled) {
		rec.ErrMessage = copyErr.Error()
	}
}

// ServeModels lists all enabled models across providers in OpenAI format.
func (p *Proxy) ServeModels(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []entry `json:"data"`
	}{Object: "list"}

	snap := p.cfg.Snapshot()
	for _, prov := range snap.Providers {
		if !prov.Enabled {
			continue
		}
		for _, m := range prov.Models {
			out.Data = append(out.Data, entry{ID: m, Object: "model", OwnedBy: prov.Name})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func buildUpstreamRequest(p config.Provider, path, apiKey string) (string, map[string]string, error) {
	if p.BaseURL == "" {
		return "", nil, fmt.Errorf("provider %q has no base_url", p.Name)
	}
	headers := map[string]string{}
	base := strings.TrimRight(p.BaseURL, "/")

	switch strings.ToLower(p.Name) {
	case "anthropic":
		if apiKey == "" {
			return "", nil, errors.New("anthropic provider missing api key")
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		return base + "/v1/messages", headers, nil
	case "gemini":
		if apiKey == "" {
			return "", nil, errors.New("gemini provider missing api key")
		}
		headers["x-goog-api-key"] = apiKey
		return base + "/v1beta/openai/chat/completions", headers, nil
	default:
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		return base + path, headers, nil
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "ai_hub_error",
			"code":    code,
		},
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var hopByHop = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func isHopByHop(h string) bool {
	_, ok := hopByHop[strings.ToLower(h)]
	return ok
}
