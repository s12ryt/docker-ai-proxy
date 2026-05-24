package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/proxy"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

//go:embed web/*
var webFS embed.FS

// Server wires HTTP routes together.
type Server struct {
	cfg                    *config.Config
	store                  *store.Store
	prx                    *proxy.Proxy
	mux                    *http.ServeMux
	ephemeralSessionSecret string
	activeMu              sync.Mutex
	activeClients         map[string]int
}

// New builds a configured Server.
func New(cfg *config.Config, st *store.Store, prx *proxy.Proxy) *Server {
	s := &Server{
		cfg:                    cfg,
		store:                  st,
		prx:                    prx,
		mux:                    http.NewServeMux(),
		ephemeralSessionSecret: newEphemeralSessionSecret(),
		activeClients:          make(map[string]int),
	}
	s.routes()
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.withRecover(s.withLogging(s.mux)) }

func (s *Server) routes() {
	sub, _ := fs.Sub(webFS, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		snap := s.cfg.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": "1.0.0",
			"uptime":  time.Since(snap.StartedAt).Round(time.Second).String(),
		})
	})

	s.mux.Handle("/v1/chat/completions", s.requireAccessToken(http.HandlerFunc(s.prx.ServeChatCompletions)))
	s.mux.Handle("/v1/responses", s.requireAccessToken(http.HandlerFunc(s.prx.ServeResponses)))
	s.mux.Handle("/v1/embeddings", s.requireAccessToken(http.HandlerFunc(s.prx.ServeEmbeddings)))
	s.mux.Handle("/v1/completions", s.requireAccessToken(http.HandlerFunc(s.prx.ServeCompletions)))
	s.mux.Handle("/v1/images/", s.requireAccessToken(http.HandlerFunc(s.prx.ServeImages)))
	s.mux.Handle("/v1/audio/", s.requireAccessToken(http.HandlerFunc(s.prx.ServeAudio)))
	s.mux.Handle("/v1/messages", s.requireAccessToken(http.HandlerFunc(s.prx.ServeAnthropicMessages)))
	s.mux.Handle("/v1beta/models/", s.requireAccessToken(http.HandlerFunc(s.prx.ServeGeminiGenerateContent)))
	s.mux.Handle("/v1/models", s.requireAccessToken(http.HandlerFunc(s.prx.ServeModels)))

	s.mux.Handle("/api/auth/bootstrap", http.HandlerFunc(s.handleBootstrapStatus))
	s.mux.Handle("/api/auth/login", s.requireSameOriginForMutating(http.HandlerFunc(s.handleLogin)))
	s.mux.Handle("/api/auth/logout", s.requireSameOriginForMutating(s.requireUser(http.HandlerFunc(s.handleLogout))))
	s.mux.Handle("/api/auth/profile", s.requireUser(http.HandlerFunc(s.handleProfile)))
	s.mux.Handle("/api/auth/register", s.requireSameOriginForMutating(s.attachOptionalUser(http.HandlerFunc(s.handleRegister))))

	s.mux.Handle("/api/summary", s.requireAdmin(http.HandlerFunc(s.handleSummary)))
	s.mux.Handle("/api/providers", s.requireAdmin(s.requireSameOriginForMutating(http.HandlerFunc(s.handleProviders))))
	s.mux.Handle("/api/access-tokens", s.requireAdmin(s.requireSameOriginForMutating(http.HandlerFunc(s.handleAccessTokens))))
	s.mux.Handle("/api/clients", s.requireAdmin(s.requireSameOriginForMutating(http.HandlerFunc(s.handleClients))))
	s.mux.Handle("/api/recent", s.requireAdmin(http.HandlerFunc(s.handleRecent)))
	s.mux.Handle("/api/runtime", s.requireAdmin(http.HandlerFunc(s.handleRuntime)))
	s.mux.Handle("/api/reload", s.requireAdmin(s.requireSameOriginForMutating(http.HandlerFunc(s.handleReload))))
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 24*30 {
			hours = n
		}
	}
	sum, err := s.store.Summarize(r.Context(), hours)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sum)
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snap := s.cfg.Snapshot()
		out := make([]map[string]any, 0, len(snap.Providers))
		for _, p := range snap.Providers {
			out = append(out, map[string]any{
				"name":         p.Name,
				"display_name": p.DisplayName,
				"base_url":     p.BaseURL,
				"api_keys":     p.APIKeys,
				"models":       p.Models,
				"enabled":      p.Enabled,
				"weight":       p.Weight,
				"key_count":    len(p.APIKeys),
				"timeout_sec":  p.TimeoutSec,
			})
		}
		writeJSON(w, out)
	case http.MethodPut:
		var req struct {
			Providers []config.Provider `json:"providers"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.SaveProviders(req.Providers); err != nil {
			if strings.Contains(err.Error(), "read ") || strings.Contains(err.Error(), "write ") || strings.Contains(err.Error(), "parse ") || strings.Contains(err.Error(), "marshal ") || strings.Contains(err.Error(), "create config dir") {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.cfg.ReplaceProviders(req.Providers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "config_path": config.ConfigPath()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAccessTokens(w http.ResponseWriter, r *http.Request) {
	envOverridden := os.Getenv("ACCESS_TOKENS") != ""
	switch r.Method {
	case http.MethodGet:
		snap := s.cfg.Snapshot()
		writeJSON(w, map[string]any{
			"tokens":         snap.AccessTokens,
			"count":          len(snap.AccessTokens),
			"config_path":    config.ConfigPath(),
			"env_overridden": envOverridden,
		})
	case http.MethodPut:
		var req struct {
			Tokens []string `json:"tokens"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.SaveAccessTokens(req.Tokens); err != nil {
			if isConfigFileError(err) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.cfg.ReplaceAccessTokens(req.Tokens); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		snap := s.cfg.Snapshot()
		writeJSON(w, map[string]any{
			"ok":             true,
			"count":          len(snap.AccessTokens),
			"config_path":    config.ConfigPath(),
			"env_overridden": envOverridden,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	envOverridden := os.Getenv("ACCESS_TOKENS") != ""
	switch r.Method {
	case http.MethodGet:
		snap := s.cfg.Snapshot()
		writeJSON(w, map[string]any{
			"clients":            snap.Clients,
			"count":              len(snap.Clients),
			"config_path":        config.ConfigPath(),
			"env_overridden":     envOverridden,
			"legacy_token_count": len(snap.AccessTokens),
		})
	case http.MethodPut:
		var req struct {
			Clients []config.Client `json:"clients"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.SaveClients(req.Clients); err != nil {
			if isConfigFileError(err) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.cfg.ReplaceClients(req.Clients); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		snap := s.cfg.Snapshot()
		writeJSON(w, map[string]any{
			"ok":                 true,
			"count":              len(snap.Clients),
			"config_path":        config.ConfigPath(),
			"env_overridden":     envOverridden,
			"legacy_token_count": len(snap.AccessTokens),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func isConfigFileError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "read ") || strings.Contains(msg, "write ") || strings.Contains(msg, "parse ") || strings.Contains(msg, "marshal ") || strings.Contains(msg, "create config dir")
}

func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := s.store.RecentCalls(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleRuntime(w http.ResponseWriter, _ *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	snap := s.cfg.Snapshot()
	out := map[string]any{
		"go_version": runtime.Version(),
		"goroutines": runtime.NumGoroutine(),
		"heap_alloc": ms.HeapAlloc,
		"heap_sys":   ms.HeapSys,
		"num_gc":     ms.NumGC,
		"started_at": snap.StartedAt,
		"uptime":     time.Since(snap.StartedAt).Round(time.Second).String(),
		"providers":  len(snap.Providers),
	}
	if s.store != nil && s.store.DB() != nil {
		stats := s.store.DB().Stats()
		out["db_stats"] = map[string]any{
			"driver":               s.store.Driver(),
			"max_open_connections": stats.MaxOpenConnections,
			"open_connections":     stats.OpenConnections,
			"in_use":               stats.InUse,
			"idle":                 stats.Idle,
			"wait_count":           stats.WaitCount,
			"wait_duration_ms":     stats.WaitDuration.Milliseconds(),
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	config.Reload()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) requireAccessToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.cfg.Snapshot()
		if !snap.HasClientCredentials() {
			http.Error(w, "access token is not configured", http.StatusUnauthorized)
			return
		}
		token := extractBearer(r)
		client, ok := snap.FindClientByToken(token)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.enforceClientPolicy(w, r, client) {
			return
		}
		release, ok := s.tryAcquireClient(client)
		if !ok {
			http.Error(w, "client concurrent limit exceeded", http.StatusTooManyRequests)
			return
		}
		defer release()
		r.Header.Set("X-AI-Hub-Client", client.Name)
		next.ServeHTTP(w, r)
	})
}

const maxClientPolicyBodyBytes = 32 << 20

func (s *Server) tryAcquireClient(client config.Client) (func(), bool) {
	name := strings.TrimSpace(client.Name)
	if client.ConcurrentLimit <= 0 || name == "" {
		return func() {}, true
	}

	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeClients == nil {
		s.activeClients = make(map[string]int)
	}
	if s.activeClients[name] >= client.ConcurrentLimit {
		return nil, false
	}
	s.activeClients[name]++
	return func() {
		s.activeMu.Lock()
		defer s.activeMu.Unlock()
		s.activeClients[name]--
		if s.activeClients[name] <= 0 {
			delete(s.activeClients, name)
		}
	}, true
}

func (s *Server) enforceClientPolicy(w http.ResponseWriter, r *http.Request, client config.Client) bool {
	if client.DailyLimit > 0 {
		if s.store == nil {
			http.Error(w, "usage store unavailable", http.StatusServiceUnavailable)
			return false
		}
		now := time.Now().UTC()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		used, err := s.store.CountClientCallsSince(r.Context(), client.Name, start)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return false
		}
		if used >= int64(client.DailyLimit) {
			http.Error(w, "client daily limit exceeded", http.StatusTooManyRequests)
			return false
		}
	}

	if client.RPMLimit > 0 {
		if s.store == nil {
			http.Error(w, "usage store unavailable", http.StatusServiceUnavailable)
			return false
		}
		used, err := s.store.CountClientCallsSince(r.Context(), client.Name, time.Now().UTC().Add(-time.Minute))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return false
		}
		if used >= int64(client.RPMLimit) {
			http.Error(w, "client rpm limit exceeded", http.StatusTooManyRequests)
			return false
		}
	}

	if len(client.AllowedModels) > 0 {
		model, err := requestedModelForPolicy(r)
		if err != nil {
			http.Error(w, "inspect model: "+err.Error(), http.StatusBadRequest)
			return false
		}
		if model != "" && !config.ClientAllowsModel(client, model) {
			http.Error(w, "model not allowed for client", http.StatusForbidden)
			return false
		}
	}
	return true
}

func requestedModelForPolicy(r *http.Request) (string, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		return "", nil
	}
	if model := geminiModelFromPolicyPath(r.URL.Path); model != "" {
		return model, nil
	}
	if r.Body == nil {
		return "", nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxClientPolicyBodyBytes))
	if err != nil {
		return "", err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return "", nil
	}

	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return "", nil
		}
		form, err := multipart.NewReader(bytes.NewReader(body), boundary).ReadForm(maxClientPolicyBodyBytes)
		if err != nil {
			return "", err
		}
		defer form.RemoveAll()
		if vals := form.Value["model"]; len(vals) > 0 {
			return strings.TrimSpace(vals[0]), nil
		}
		return "", nil
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil
	}
	return strings.TrimSpace(payload.Model), nil
}

func geminiModelFromPolicyPath(path string) string {
	if !strings.HasPrefix(path, "/v1beta/models/") {
		return ""
	}
	model := strings.TrimPrefix(path, "/v1beta/models/")
	for _, suffix := range []string{":generateContent", ":streamGenerateContent"} {
		if strings.HasSuffix(model, suffix) {
			model = strings.TrimSuffix(model, suffix)
			break
		}
	}
	if model == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(model); err == nil {
		model = decoded
	}
	return strings.TrimSpace(model)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		// Skip noisy static asset / health probes from access log.
		if r.URL.Path == "/healthz" ||
			strings.HasPrefix(r.URL.Path, "/style.css") ||
			strings.HasPrefix(r.URL.Path, "/app.js") ||
			strings.HasPrefix(r.URL.Path, "/dashboard.js") {
			return
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, lrw.status, time.Since(start).Round(time.Millisecond))
	})
}

// loggingResponseWriter captures the status code so withLogging can report it.
type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (l *loggingResponseWriter) WriteHeader(code int) {
	if l.wroteHeader {
		return
	}
	l.status = code
	l.wroteHeader = true
	l.ResponseWriter.WriteHeader(code)
}

func (l *loggingResponseWriter) Write(b []byte) (int, error) {
	if !l.wroteHeader {
		l.wroteHeader = true
	}
	return l.ResponseWriter.Write(b)
}

// ReadFrom forwards io.ReaderFrom when available so wrappers do not disable
// optimized io.Copy paths, while still marking the response as written.
func (l *loggingResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !l.wroteHeader {
		l.wroteHeader = true
	}
	if rf, ok := l.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(l.ResponseWriter, r)
}

// Flush forwards Flush() so the proxy's streaming path keeps working
// when the inner ResponseWriter is wrapped by loggingResponseWriter.
func (l *loggingResponseWriter) Flush() {
	if f, ok := l.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				// http.ErrAbortHandler is the documented way for a handler to
				// stop without logging; respect that contract.
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Printf("[panic] %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				// Best-effort: only write a response if the inner handler hasn't
				// already started one. The downstream loggingResponseWriter
				// silently no-ops repeated WriteHeader calls so this is safe.
				lrw, ok := w.(*loggingResponseWriter)
				if !ok || !lrw.wroteHeader {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"message": "internal server error",
							"type":    "ai_hub_panic",
							"code":    http.StatusInternalServerError,
						},
					})
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(h)
}

// Shutdown releases server-owned resources after the HTTP server stops.
func (s *Server) Shutdown(_ context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}
