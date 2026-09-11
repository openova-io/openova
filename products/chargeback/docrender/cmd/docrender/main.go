// docrender is the chargeback BSS document renderer (EPIC #6867): a
// stateless HTTP service that turns a structured document — invoice, credit
// note, statement or quote — into a PDF.
//
// It is IN-CLUSTER ONLY. The chart gives it a ClusterIP Service, a
// NetworkPolicy that admits the chargeback pods and nothing else, and an
// egress policy that allows DNS and nothing else. It opens no database,
// writes no file and makes no outbound call, so there is nothing for it to
// reach even if something asked it to.
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

	"github.com/openova-io/openova/products/chargeback/docrender"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/server"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})))

	opt := server.Options{
		Assets:        docrender.Assets(),
		Token:         strings.TrimSpace(os.Getenv("RENDER_TOKEN")),
		MaxBodyBytes:  int64(intEnv("MAX_BODY_BYTES", server.DefaultMaxBodyBytes)),
		RenderTimeout: durEnv("RENDER_TIMEOUT", server.DefaultRenderTimeout),
		FontFile:      strings.TrimSpace(os.Getenv("FONT_FILE")),
		Version:       version,
	}
	srv, err := server.New(opt)
	if err != nil {
		// A renderer that cannot render its own sample document must not
		// take traffic: fail at start rather than 500 on the first invoice.
		slog.Error("start", "error", err)
		os.Exit(1)
	}
	if opt.Token == "" {
		slog.Info("RENDER_TOKEN is empty: the shared-secret header is not required; the NetworkPolicy is the only admission control")
	}

	addr := getenv("LISTEN_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("docrender listening",
		"addr", addr, "version", version,
		"render_timeout", opt.RenderTimeout, "max_body_bytes", opt.MaxBodyBytes,
		"token_required", opt.Token != "")
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
	slog.Info("docrender stopped")
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func logLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func intEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("ignoring unusable integer env, using default", "env", key, "value", raw, "default", fallback)
		return fallback
	}
	return n
}

func durEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("ignoring unusable duration env, using default", "env", key, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
