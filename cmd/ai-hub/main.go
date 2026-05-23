package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/proxy"
	"github.com/s12ryt/docker-ai-proxy/internal/server"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		arg := strings.ToLower(os.Args[1])
		if arg == "-version" || arg == "--version" {
			log.Printf("ai-hub %s (%s)", version, commit)
			return
		}
	}
	cfg := config.Get()

	stCfg := store.Config{
		Driver:       cfg.DBDriver,
		DSN:          cfg.DBDSN,
		Path:         cfg.DBPath,
		MaxOpenConns: cfg.DBMaxOpen,
		MaxIdleConns: cfg.DBMaxIdle,
	}
	if cfg.DBConnMaxLife != "" {
		if d, err := time.ParseDuration(cfg.DBConnMaxLife); err == nil {
			stCfg.ConnMaxLifetime = d
		} else {
			log.Printf("[main] invalid DB_CONN_MAX_LIFETIME=%q: %v (ignored)", cfg.DBConnMaxLife, err)
		}
	}

	st, err := store.Open(stCfg)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	log.Printf("ai-hub: store driver=%s", st.Driver())

	prx := proxy.New(cfg, st)
	srv := server.New(cfg, st, prx)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("ai-hub listening on %s", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Printf("shutting down…")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	_ = srv.Shutdown(ctx)
}
