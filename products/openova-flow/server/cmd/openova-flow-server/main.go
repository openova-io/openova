// openova-flow-server — stateless HTTP+SSE event router for
// OpenovaFlow. CNPG-backed persistence; in-memory fallback ONLY for
// development environments without a database (FLOW_SERVER_BACKEND=memory).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openova-io/openova/products/openova-flow/server/internal/api"
	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := envDefault("FLOW_SERVER_LISTEN_ADDR", ":8080")
	bufCap, err := strconv.Atoi(envDefault("FLOW_SERVER_RING_CAPACITY", "4096"))
	if err != nil || bufCap <= 0 {
		log.Warn("invalid FLOW_SERVER_RING_CAPACITY, falling back to 4096",
			"raw", os.Getenv("FLOW_SERVER_RING_CAPACITY"), "err", err)
		bufCap = 4096
	}

	backendKind := envDefault("FLOW_SERVER_BACKEND", "pg")
	var backend store.Backend
	switch backendKind {
	case "memory", "mem":
		log.Info("using in-memory backend (FLOW_SERVER_BACKEND=memory)")
		backend = store.NewMemBackend(bufCap)
	case "pg", "postgres":
		dsn := os.Getenv("FLOW_SERVER_PG_DSN")
		if dsn == "" {
			log.Error("FLOW_SERVER_PG_DSN is required when FLOW_SERVER_BACKEND=pg")
			os.Exit(1)
		}
		bootCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		pg, err := store.NewPGStore(bootCtx, dsn)
		cancel()
		if err != nil {
			log.Error("open PGStore failed", "err", err)
			os.Exit(1)
		}
		log.Info("PGStore ready", "dsn-masked", maskDSN(dsn))
		defer pg.Close()
		backend = pg
	default:
		log.Error("invalid FLOW_SERVER_BACKEND (want pg|memory)", "got", backendKind)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(backend),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		log.Info("openova-flow-server listening", "addr", addr, "backend", backendKind, "ringCapacity", bufCap)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown failed", "err", err)
	}
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// maskDSN hides the password segment of a libpq DSN for log lines.
func maskDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			if colon := strings.Index(rest[:at], ":"); colon >= 0 {
				return dsn[:i+3] + rest[:colon+1] + "***" + rest[at:]
			}
		}
	}
	return "***"
}
