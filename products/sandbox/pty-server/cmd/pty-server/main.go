// pty-server entrypoint.
//
// Listens on :7681 (override with $PTY_SERVER_ADDR). Serves the HTTP
// + WebSocket surface defined in internal/server. Graceful shutdown on
// SIGTERM / SIGINT: stop accepting new connections, then close every
// live session (SIGTERM -> 5 s -> SIGKILL per session).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openova-io/openova/products/sandbox/pty-server/internal/server"
	"github.com/openova-io/openova/products/sandbox/pty-server/internal/session"
)

func main() {
	addr := os.Getenv("PTY_SERVER_ADDR")
	if addr == "" {
		addr = ":7681"
	}

	mgr := session.NewManager()
	h := server.New(mgr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("pty-server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-stop
	log.Printf("pty-server: shutdown requested")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	mgr.Shutdown()
	log.Printf("pty-server: stopped")
}
