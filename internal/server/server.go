package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/proxy"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

//go:embed web/*
var webFS embed.FS

// Server wires HTTP routes together.
type Server struct {
	cfg   *config.Config
	store *store.Store
	prx   *proxy.Proxy
	mux   *http.ServeMux
}

// New builds a configured Server.
func New(cfg *config.Config, st *store.Store, prx *proxy.Proxy) *Server {
	s := &Server{cfg: cfg, store: st, prx: prx, mux: http.NewServeMux()}
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

	s.mux.Handle("/api/summary", s.requireAdmin(http.HandlerFunc(s.handleSummary)))
	s.mux.Handle("/api/providers", s.requireAdmin(http.HandlerFunc(s.handleProviders)))
	s.mux.Handle("/api/recent", s.requireAdmin(http.HandlerFunc(s.handleRecent)))
	s.mux.Handle("/api/runtime", s.requireAdmin(http.HandlerFunc(s.handleRuntime)))
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
	snap := s.cfg.Snapshot()
	out := make([]map[string]any, 0, len(snap.Providers))
	for _, p := range snap.Providers {
		out = append(out, map[string]any{
			"name":         p.Name,
			"display_name": p.DisplayName,
			"base_url":     p.BaseURL,
			"models":       p.Models,
			"enabled":      p.Enabled,
			"weight":       p.Weight,
			"key_count":    len(p.APIKeys),
			"timeout_sec":  p.TimeoutSec,
		})
	}
	writeJSON(w, out)
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
	writeJSON(w, map[string]any{
		"go_version": runtime.Version(),
		"goroutines": runtime.NumGoroutine(),
		"heap_alloc": ms.HeapAlloc,
		"heap_sys":   ms.HeapSys,
		"num_gc":     ms.NumGC,
		"started_at": snap.StartedAt,
		"uptime":     time.Since(snap.StartedAt).Round(time.Second).String(),
		"providers":  len(snap.Providers),
	})
}

func (s *Server) requireAccessToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.cfg.Snapshot()
		if len(snap.AccessTokens) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		token := extractBearer(r)
		for _, t := range snap.AccessTokens {
			if t == token {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" {
			token = r.URL.Query().Get("admin_token")
		}
		snap := s.cfg.Snapshot()
		if token == "" || snap.AdminToken == "" || token != snap.AdminToken {
			http.Error(w, "admin token required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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

// Shutdown is a placeholder for graceful shutdown logic.
func (s *Server) Shutdown(_ context.Context) error { return nil }
