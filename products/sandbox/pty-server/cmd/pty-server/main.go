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

	// TBD-V22 (#1986 F1, 2026-05-20) — apply SANDBOX_RING_BUFFER_BYTES
	// override before any Session.New runs. Empty / non-integer leaves
	// the package default (1 MiB) intact. Values above session.MaxRingBytes
	// are clamped and the clamp is logged so an operator-misconfigured
	// excessive value surfaces visibly instead of silently consuming Pod
	// memory.
	ringBytes, clamped := session.LoadDefaultRingBytesFromEnv()
	if clamped {
		log.Printf("pty-server: SANDBOX_RING_BUFFER_BYTES clamped to MaxRingBytes (%d) — operator-set value above the ceiling", session.MaxRingBytes)
	}
	log.Printf("pty-server: replay ring buffer default = %d bytes (~%d KiB)", ringBytes, ringBytes/1024)

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
