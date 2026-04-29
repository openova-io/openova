// Command pdm — pool-domain-manager service entrypoint.
//
// Wires CNPG/Postgres (store), the Dynadot client, and the chi-based HTTP
// router. Starts the TTL-expiry sweeper as a goroutine. Handles SIGTERM by
// closing the listener gracefully so K8s rolling deploys finish in-flight
// requests before the pod terminates.
//
// All configuration is read from environment variables — per
// docs/INVIOLABLE-PRINCIPLES.md #4 nothing here is hardcoded:
//
//	PORT                       — listen port (default 8080)
//	PDM_DATABASE_URL           — postgres DSN, REQUIRED
//	DYNADOT_API_KEY            — dynadot api key, REQUIRED
//	DYNADOT_API_SECRET         — dynadot api secret, REQUIRED
//	DYNADOT_MANAGED_DOMAINS    — comma-separated managed pool list
//	DYNADOT_DOMAIN             — legacy single-domain fallback
//	PDM_RESERVATION_TTL        — go duration string, default "10m"
//	PDM_SWEEPER_INTERVAL       — go duration string, default "30s"
//	PDM_LOG_LEVEL              — debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/allocator"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/handler"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

func main() {
	log := newLogger(env("PDM_LOG_LEVEL", "info"))
	slog.SetDefault(log)

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()

	s, err := store.New(startCtx, cfg.DatabaseURL)
	if err != nil {
		log.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	dyn := dynadot.New(cfg.DynadotAPIKey, cfg.DynadotAPISecret)
	alloc := allocator.New(s, dyn, log, cfg.ReservationTTL)

	go alloc.RunSweeper(ctx, cfg.SweeperInterval)

	h := handler.New(alloc, s, log)

	root := chi.NewRouter()
	root.Use(middleware.RequestID)
	root.Use(middleware.RealIP)
	root.Use(middleware.Logger)
	root.Use(middleware.Recoverer)
	root.Mount("/", h.Routes())

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Surface the managed-domain list at startup so operators can grep logs
	// for misconfiguration (e.g. typo in the secret's `domains` key).
	log.Info("pool-domain-manager starting",
		"port", cfg.Port,
		"reservationTTL", cfg.ReservationTTL.String(),
		"sweeperInterval", cfg.SweeperInterval.String(),
		"managedDomains", dynadot.ManagedDomains(),
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received, draining")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

// config bundles the runtime configuration so loadConfig can return a single
// struct + error.
type config struct {
	Port             string
	DatabaseURL      string
	DynadotAPIKey    string
	DynadotAPISecret string
	ReservationTTL   time.Duration
	SweeperInterval  time.Duration
}

func loadConfig() (*config, error) {
	c := &config{
		Port: env("PORT", "8080"),
	}
	c.DatabaseURL = strings.TrimSpace(os.Getenv("PDM_DATABASE_URL"))
	if c.DatabaseURL == "" {
		return nil, errors.New("PDM_DATABASE_URL is required")
	}
	c.DynadotAPIKey = strings.TrimSpace(os.Getenv("DYNADOT_API_KEY"))
	if c.DynadotAPIKey == "" {
		return nil, errors.New("DYNADOT_API_KEY is required")
	}
	c.DynadotAPISecret = strings.TrimSpace(os.Getenv("DYNADOT_API_SECRET"))
	if c.DynadotAPISecret == "" {
		return nil, errors.New("DYNADOT_API_SECRET is required")
	}

	ttlStr := env("PDM_RESERVATION_TTL", "10m")
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		return nil, errors.New("PDM_RESERVATION_TTL is not a valid duration: " + err.Error())
	}
	c.ReservationTTL = ttl

	swStr := env("PDM_SWEEPER_INTERVAL", "30s")
	sw, err := time.ParseDuration(swStr)
	if err != nil {
		return nil, errors.New("PDM_SWEEPER_INTERVAL is not a valid duration: " + err.Error())
	}
	c.SweeperInterval = sw

	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
