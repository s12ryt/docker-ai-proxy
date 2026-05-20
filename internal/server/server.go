package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"runtime"
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": "1.0.0",
			"uptime":  time.Since(s.cfg.StartedAt).Round(time.Second).String(),
		})
	})

	s.mux.Handle("/v1/chat/completions", s.requireAccessToken(http.HandlerFunc(s.prx.ServeChatCompletions)))
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
	writeJSON(w, map[string]any{
		"go_version": runtime.Version(),
		"goroutines": runtime.NumGoroutine(),
		"heap_alloc": ms.HeapAlloc,
		"heap_sys":   ms.HeapSys,
		"num_gc":     ms.NumGC,
		"started_at": s.cfg.StartedAt,
		"uptime":     time.Since(s.cfg.StartedAt).Round(time.Second).String(),
		"providers":  len(s.cfg.Snapshot().Providers),
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
		if token == "" || token != s.cfg.AdminToken {
			http.Error(w, "admin token required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "internal"})
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
