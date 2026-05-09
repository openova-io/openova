// Command k8s-ws-proxy is the per-node WebSocket exec-proxy that bridges
// HMAC-authenticated upstream callers (catalyst-api or Guacamole's
// k8s-shell adapter) onto the local kube-apiserver's
// /api/v1/.../pods/exec stream.
//
// The binary runs as a DaemonSet (one Pod per node) so that exec
// streams stay node-local, never crossing the cluster boundary at
// data-plane time. The HMAC layer (internal/auth/hmac.go) is the
// authn gate; the in-cluster ServiceAccount token is the authz gate
// against the apiserver.
//
// Why a DaemonSet rather than a Deployment + ClusterIP Service?
// kubelet exec sessions are sticky to one apiserver TCP connection;
// landing on the same node as the target Pod reduces session jitter
// and makes per-node NetworkPolicy effective. The chart's CC3-style
// gate ships the binary as a DaemonSet behind a ClusterIP Service
// with `internalTrafficPolicy: Local` (kube-proxy short-circuits
// onto the local DaemonSet pod) — see platform/k8s-ws-proxy/chart/.
//
// Configuration is environment-variable driven so a Sovereign overlay
// can retune the binary without rebuilding the image
// (INVIOLABLE-PRINCIPLES.md #4):
//
//	WS_PROXY_LISTEN_ADDR        — bind address; default :8080
//	SHARED_SECRET_FILE          — path to HMAC secret file (REQUIRED)
//	HMAC_SKEW_SECONDS           — clock-skew tolerance; default 300
//	TMUX_CASCADE                — wrap exec in shared tmux session;
//	                              default false
//	ALLOWED_NAMESPACES          — comma-separated allowlist; default
//	                              empty (all)
//	LOG_LEVEL                   — debug/info/warn/error; default info
//
// Restart policy: per the same DaemonSet contract, the binary exits
// with code 2 on configuration error (so kubelet stops retrying) and
// code 1 on listen failure (transient — kubelet retries). Healthy
// runtime exits via SIGTERM with code 0.
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

	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/auth"
	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/proxy"
	wsruntime "github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/runtime"
)

const (
	exitConfigError = 2
	exitListenError = 1
)

func main() {
	cfg, err := wsruntime.LoadFromEnv()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: wsruntime.ParseLogLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(exitConfigError)
	}

	secret, err := wsruntime.ReadSecret(cfg)
	if err != nil {
		logger.Error("shared secret unreadable", "err", err)
		os.Exit(exitConfigError)
	}
	verifier, err := auth.NewVerifier(secret, time.Duration(cfg.SkewSeconds)*time.Second)
	if err != nil {
		logger.Error("verifier init failed", "err", err)
		os.Exit(exitConfigError)
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("in-cluster REST config unavailable (run inside k8s)", "err", err)
		os.Exit(exitConfigError)
	}

	h, err := proxy.New(proxy.HandlerOptions{
		Logger:            logger,
		Verifier:          verifier,
		RESTConfig:        restCfg,
		TmuxCascade:       cfg.TmuxCascade,
		AllowedNamespaces: cfg.AllowedNamespaces,
		PingPeriod:        cfg.PingPeriod,
		HandshakeTimeout:  cfg.HandshakeTimeout,
	})
	if err != nil {
		logger.Error("proxy handler init failed", "err", err)
		os.Exit(exitConfigError)
	}

	mux := http.NewServeMux()
	mux.Handle("/proxy/exec/", h)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("k8s-ws-proxy starting",
		"listen", cfg.ListenAddr,
		"tmuxCascade", cfg.TmuxCascade,
		"allowedNamespaces", cfg.AllowedNamespaces,
		"skewSeconds", cfg.SkewSeconds,
	)

	// Graceful shutdown on SIGTERM / SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received; draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("http shutdown returned error", "err", err)
		}
	case err := <-errCh:
		logger.Error("listen failed", "err", err)
		os.Exit(exitListenError)
	}
}
