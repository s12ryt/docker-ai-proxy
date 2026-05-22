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
	"net/url"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/providers"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

// maxRequestBytes caps the incoming chat-completions payload. Generous enough
// for image_url / base64-encoded multimodal inputs while still keeping a hard
// ceiling against runaway uploads.
const maxRequestBytes = 32 << 20 // 32 MiB

// logCallTimeout bounds the background persistence of a CallRecord so a slow
// or stuck database cannot leak goroutines after the request has finished.
const logCallTimeout = 5 * time.Second

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
				MaxIdleConns:          200,
				MaxIdleConnsPerHost:   32,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				// ResponseHeaderTimeout guards against an upstream that
				// accepts the connection but never returns headers; the
				// per-request context still bounds the full body.
				ResponseHeaderTimeout: 60 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
	}
}

// ServeChatCompletions handles `/v1/chat/completions` requests in OpenAI format.
//
// The handler classifies the resolved upstream provider into one of three wire
// protocols (OpenAI / Anthropic / Gemini) and either streams the response
// verbatim (OpenAI-compatible upstreams) or runs a request/response
// translation through internal/protocol (Anthropic, Gemini).
//
// Streaming for Anthropic/Gemini also runs through the stream event translator
// so OpenAI clients can consume Claude/Gemini providers without changing SDKs.
func (p *Proxy) ServeChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
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
	requestedModel, _ := payload["model"].(string)
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		writeJSONError(w, http.StatusBadRequest, "missing 'model'")
		return
	}

	provider, upstreamModel, err := p.cfg.ProviderForModel(requestedModel)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	kind := providerKindOf(provider)
	stream, _ := payload["stream"].(bool)

	// Build the outbound payload. For OpenAI-style providers we keep the
	// original body shape but swap in the provider-native model name. For
	// Anthropic/Gemini we go through the IR translator.
	var outBody []byte
	switch kind {
	case kindOpenAI:
		payload["model"] = upstreamModel
		outBody, err = json.Marshal(payload)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "marshal payload: "+err.Error())
			return
		}
	case kindAnthropic, kindGemini:
		outBody, _, err = translateChatRequest(body, kind, upstreamModel)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	rec := store.CallRecord{
		Timestamp: time.Now(),
		Provider:  provider.Name,
		Model:     upstreamModel,
		Path:      r.URL.Path,
		BytesIn:   int64(len(body)),
		ClientIP:  clientIP(r),
	}
	start := time.Now()

	defer func() {
		rec.LatencyMS = time.Since(start).Milliseconds()
		if p.store == nil {
			return
		}
		// Use an independent context so the log write survives the request
		// being cancelled (client disconnect) and any server shutdown that
		// is currently draining active handlers. A short timeout keeps a
		// stuck DB from blocking the response.
		ctx, cancel := context.WithTimeout(context.Background(), logCallTimeout)
		defer cancel()
		_ = p.store.LogCall(ctx, rec)
	}()

	upstreamPath := upstreamPathForChat(kind, upstreamModel, stream)
	upstreamURL, headers, err := buildUpstreamRequest(provider, upstreamPath, p.picker.Pick(provider))
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

	if kind == kindAnthropic || kind == kindGemini {
		if stream {
			p.serveChatStreamAs(w, resp, kind, kindOpenAI, requestedModel, provider.Name, &rec)
			return
		}
		p.serveTranslatedChatResponse(w, resp, kind, requestedModel, provider.Name, &rec)
		return
	}

	// OpenAI / DeepSeek / any OAI-compatible upstream: stream through.
	p.serveStreamThrough(w, resp, provider.Name, stream, &rec)
}

// ServeEmbeddings handles OpenAI-compatible `/v1/embeddings` requests.
func (p *Proxy) ServeEmbeddings(w http.ResponseWriter, r *http.Request) {
	p.serveOpenAICompatibleEndpoint(w, r, "/v1/embeddings", false)
}

// ServeCompletions handles OpenAI-compatible legacy `/v1/completions` requests.
func (p *Proxy) ServeCompletions(w http.ResponseWriter, r *http.Request) {
	p.serveOpenAICompatibleEndpoint(w, r, "/v1/completions", true)
}

func (p *Proxy) serveOpenAICompatibleEndpoint(w http.ResponseWriter, r *http.Request, upstreamPath string, allowStream bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
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
	requestedModel, _ := payload["model"].(string)
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		writeJSONError(w, http.StatusBadRequest, "missing 'model'")
		return
	}

	provider, upstreamModel, err := p.cfg.ProviderForModel(requestedModel)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kind := providerKindOf(provider); kind != kindOpenAI {
		writeJSONError(w, http.StatusNotImplemented, fmt.Sprintf("%s is only supported for OpenAI-compatible providers", upstreamPath))
		return
	}

	payload["model"] = upstreamModel
	outBody, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "marshal payload: "+err.Error())
		return
	}
	stream, _ := payload["stream"].(bool)
	if !allowStream {
		stream = false
	}

	rec := store.CallRecord{
		Timestamp: time.Now(),
		Provider:  provider.Name,
		Model:     upstreamModel,
		Path:      r.URL.Path,
		BytesIn:   int64(len(body)),
		ClientIP:  clientIP(r),
	}
	start := time.Now()
	defer func() {
		rec.LatencyMS = time.Since(start).Milliseconds()
		if p.store == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), logCallTimeout)
		defer cancel()
		_ = p.store.LogCall(ctx, rec)
	}()

	upstreamURL, headers, err := buildUpstreamRequest(provider, upstreamPath, p.picker.Pick(provider))
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

	p.serveStreamThrough(w, resp, provider.Name, stream, &rec)
}

// ServeAnthropicMessages handles Anthropic-native `/v1/messages` requests.
func (p *Proxy) ServeAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	ir, err := decodeChatRequest(body, kindAnthropic)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.serveTranslatedChatRequest(w, r, kindAnthropic, ir.Model, body, ir.Stream)
}

// ServeGeminiGenerateContent handles Gemini-native generateContent requests.
func (p *Proxy) ServeGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestedModel, stream, ok := parseGeminiGenerateContentPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	if _, err := decodeChatRequest(body, kindGemini); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	p.serveTranslatedChatRequest(w, r, kindGemini, requestedModel, body, stream)
}

func (p *Proxy) serveTranslatedChatRequest(w http.ResponseWriter, r *http.Request, inKind providerKind, requestedModel string, body []byte, stream bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		writeJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	provider, upstreamModel, err := p.cfg.ProviderForModel(requestedModel)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	dstKind := providerKindOf(provider)

	outBody, _, err := translateChatRequestFromWithStream(body, inKind, dstKind, upstreamModel, &stream)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rec := store.CallRecord{
		Timestamp: time.Now(),
		Provider:  provider.Name,
		Model:     upstreamModel,
		Path:      r.URL.Path,
		BytesIn:   int64(len(body)),
		ClientIP:  clientIP(r),
	}
	start := time.Now()

	defer func() {
		rec.LatencyMS = time.Since(start).Milliseconds()
		if p.store == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), logCallTimeout)
		defer cancel()
		_ = p.store.LogCall(ctx, rec)
	}()

	upstreamPath := upstreamPathForChat(dstKind, upstreamModel, stream)
	upstreamURL, headers, err := buildUpstreamRequest(provider, upstreamPath, p.picker.Pick(provider))
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

	if stream {
		p.serveChatStreamAs(w, resp, dstKind, inKind, requestedModel, provider.Name, &rec)
		return
	}
	p.serveChatResponseAs(w, resp, dstKind, inKind, requestedModel, provider.Name, &rec)
}

func parseGeminiGenerateContentPath(path string) (model string, stream bool, ok bool) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	tail := strings.TrimPrefix(path, prefix)
	suffix := ":generateContent"
	if strings.HasSuffix(tail, ":streamGenerateContent") {
		stream = true
		suffix = ":streamGenerateContent"
	} else if !strings.HasSuffix(tail, suffix) {
		return "", false, false
	}
	modelPart := strings.TrimSuffix(tail, suffix)
	modelPart = strings.TrimSpace(modelPart)
	if modelPart == "" {
		return "", stream, false
	}
	decoded, err := url.PathUnescape(modelPart)
	if err != nil {
		return "", stream, false
	}
	return decoded, stream, true
}

// serveStreamThrough copies the upstream response to the client verbatim,
// flushing after every chunk. Used for OpenAI-compatible upstreams where the
// wire format is already what the client expects.
func (p *Proxy) serveStreamThrough(w http.ResponseWriter, resp *http.Response, providerName string, stream bool, rec *store.CallRecord) {
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-AI-Hub-Provider", providerName)

	// Detect SSE either from the upstream Content-Type or the client's
	// stream:true flag, and apply the anti-buffering headers that nginx /
	// Cloudflare / common reverse proxies look for. These must be set
	// before WriteHeader because the http.ResponseWriter freezes headers
	// on first write.
	if stream || isEventStream(resp.Header.Get("Content-Type")) {
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		// Replace any Content-Length the upstream might have sent — SSE
		// responses are length-unknown by definition.
		w.Header().Del("Content-Length")
	}

	w.WriteHeader(resp.StatusCode)
	rec.Status = resp.StatusCode

	// Stream-aware copy: flush after each chunk so SSE/NDJSON reaches
	// the client without buffering. Falls back to plain io.Copy when
	// the ResponseWriter does not expose http.Flusher.
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	var n int64
	for {
		nr, er := resp.Body.Read(buf)
		if nr > 0 {
			nw, ew := w.Write(buf[:nr])
			n += int64(nw)
			if flusher != nil {
				flusher.Flush()
			}
			if ew != nil {
				if !errors.Is(ew, context.Canceled) {
					rec.ErrMessage = ew.Error()
				}
				break
			}
			if nr != nw {
				rec.ErrMessage = io.ErrShortWrite.Error()
				break
			}
		}
		if er != nil {
			if er != io.EOF && !errors.Is(er, context.Canceled) {
				rec.ErrMessage = er.Error()
			}
			break
		}
	}
	rec.BytesOut = n
}

// serveTranslatedChatResponse buffers an Anthropic/Gemini response, runs it
// through the IR translator and emits an OpenAI-shape body. Upstream non-2xx
// responses are echoed verbatim with the original status code (so clients see
// vendor error messages); only 2xx bodies go through translation.
func (p *Proxy) serveTranslatedChatResponse(w http.ResponseWriter, resp *http.Response, kind providerKind, requestedModel, providerName string, rec *store.CallRecord) {
	p.serveChatResponseAs(w, resp, kind, kindOpenAI, requestedModel, providerName, rec)
}

func (p *Proxy) serveChatResponseAs(w http.ResponseWriter, resp *http.Response, srcKind, dstKind providerKind, requestedModel, providerName string, rec *store.CallRecord) {
	upstreamBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBytes))
	if err != nil {
		rec.Status = http.StatusBadGateway
		rec.ErrMessage = err.Error()
		writeJSONError(w, http.StatusBadGateway, "read upstream: "+err.Error())
		return
	}

	w.Header().Set("X-AI-Hub-Provider", providerName)

	// Non-2xx: pass through. Vendor error envelopes are usable to clients
	// even if they're not in OpenAI shape, and translating an error JSON
	// could mask the original signal. We still strip hop-by-hop headers.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		for k, vs := range resp.Header {
			if isHopByHop(k) {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		rec.Status = resp.StatusCode
		n, _ := w.Write(upstreamBody)
		rec.BytesOut = int64(n)
		return
	}

	translated, err := translateChatResponseTo(srcKind, dstKind, requestedModel, upstreamBody)
	if err != nil {
		rec.Status = http.StatusBadGateway
		rec.ErrMessage = err.Error()
		writeJSONError(w, http.StatusBadGateway, "translate response: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	rec.Status = http.StatusOK
	n, _ := w.Write(translated)
	rec.BytesOut = int64(n)
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
	case "anthropic", "claude":
		if apiKey == "" {
			return "", nil, errors.New("anthropic provider missing api key")
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		// If the caller passed an OpenAI-style path we still want to
		// land on /v1/messages — that's the only endpoint Anthropic
		// exposes for chat. Other Anthropic-style paths (e.g.
		// /v1/complete) are honoured verbatim.
		if path == "" || path == "/v1/chat/completions" {
			path = "/v1/messages"
		}
		return base + path, headers, nil
	case "gemini", "google", "googleai", "vertex":
		if apiKey == "" {
			return "", nil, errors.New("gemini provider missing api key")
		}
		headers["x-goog-api-key"] = apiKey
		// Gemini paths are computed by the caller (they embed the model
		// name). Fall through to base + path.
		return base + path, headers, nil
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

// clientIP picks the most plausible originating client address. Order:
//  1. The first non-empty hop in X-Forwarded-For (the closest the standard
//     gets to "the original client" — later hops are intermediate proxies).
//  2. X-Real-IP (often set by nginx ingress).
//  3. The TCP RemoteAddr, with the port stripped.
//
// IPv6 addresses inside any of the above are normalised by stripping a
// trailing `:port` and surrounding brackets where present.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			return normaliseIP(candidate)
		}
	}
	if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
		return normaliseIP(rip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// normaliseIP turns "[::1]:8080" / "1.2.3.4:5678" / "::1" / "1.2.3.4"
// into a bare IP literal. Pure IPv6 without brackets is left untouched.
func normaliseIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Bracketed form: [ipv6] or [ipv6]:port
	if strings.HasPrefix(s, "[") {
		if host, _, err := net.SplitHostPort(s); err == nil {
			return host
		}
		// Bracketed but no port — strip the brackets.
		if end := strings.LastIndexByte(s, ']'); end > 0 {
			return s[1:end]
		}
		return s
	}
	// Already a bare IPv6 (multiple colons, no brackets) — leave alone.
	if strings.Count(s, ":") >= 2 {
		return s
	}
	// IPv4 or hostname, possibly with a :port suffix.
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

func isEventStream(ct string) bool {
	if ct == "" {
		return false
	}
	// e.g. "text/event-stream; charset=utf-8"
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "text/event-stream")
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
