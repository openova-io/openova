// openova-flow-server — stateless HTTP+SSE event router for
// OpenovaFlow. See products/openova-flow/server/README.md for the
// wire contract.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every operational knob is env-
// driven; no hardcoded port, no hardcoded ring capacity.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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

	s := store.NewStore(bufCap)
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(s),
		ReadHeaderTimeout: 10 * time.Second,
		// SSE streams are long-lived; no overall write timeout.
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		log.Info("openova-flow-server listening",
			"addr", addr, "ringCapacity", bufCap)
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
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

