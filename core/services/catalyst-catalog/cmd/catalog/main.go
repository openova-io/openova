// catalyst-catalog HTTP REST service entrypoint.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs, regions, FQDNs)
// every config value is sourced from environment variables. See
// internal/config/config.go for the full schema.
//
// The Gitea client is the unified CC2 client at
// `core/controllers/pkg/gitea` (promoted from internal/ by EPIC-2 Slice L
// per the brief — Go internal/ rule blocks cross-module imports).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/config"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/handler"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/source"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("catalyst-catalog starting",
		"listen", cfg.ListenAddr,
		"public_org", cfg.PublicCatalogOrg,
		"sovereign_org", cfg.SovereignCatalogOrg,
		"private_repo", cfg.OrgPrivateRepoSuffix,
		"cache_ttl", cfg.CacheTTL,
		"cache_capacity", cfg.CacheCapacity,
		"sovereign_fqdn", cfg.SovereignFQDN,
		"anonymous_reads", cfg.AnonymousReads,
	)

	gc := gitea.NewWithHTTP(cfg.GiteaURL, cfg.GiteaToken, &http.Client{Timeout: 30 * time.Second})

	c := cache.New(cfg.CacheCapacity, cfg.CacheTTL)
	r := source.NewResolver(gc, cfg.PublicCatalogOrg, cfg.SovereignCatalogOrg, cfg.OrgPrivateRepoSuffix, c)
	h := handler.New(cfg, r, c, logger)

	mux := h.Routes()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           withLogging(logger, withRecover(logger, mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	idle := make(chan struct{})
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		logger.Info("shutdown initiated")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
		}
		close(idle)
	}()

	logger.Info("listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("ListenAndServe", "err", err)
		os.Exit(1)
	}
	<-idle
	logger.Info("stopped cleanly")
}

// withLogging is a tiny structured access logger. We log r.URL.Path
// (NEVER r.URL.RawQuery) because the access_token query param can carry
// the JWT — never let it land in logs.
func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// withRecover converts panics into 500s so a single bad code path
// can't take the process down.
func withRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic", "err", rec, "path", r.URL.Path)
				http.Error(w, `{"error":"internal","message":"panic"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
