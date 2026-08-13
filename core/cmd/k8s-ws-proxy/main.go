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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
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

	// mTLS client-certificate auth (#5991) — the alternative credential
	// presentation for callers that cannot set HTTP headers, i.e. guacd.
	// Built ONLY when an explicit subject allowlist is configured, so a
	// TLS-enabled proxy with no allowlist stays HMAC-only.
	var certVerifier *auth.CertVerifier
	if cfg.ClientCertAuthEnabled() {
		certVerifier, err = auth.NewCertVerifier(cfg.ClientCertSubjects)
		if err != nil {
			logger.Error("client-certificate verifier init failed", "err", err)
			os.Exit(exitConfigError)
		}
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("in-cluster REST config unavailable (run inside k8s)", "err", err)
		os.Exit(exitConfigError)
	}

	// Pod-alias resolution (#5991). nil unless POD_ALIAS_LABEL is set,
	// and nil means the pod segment is used verbatim with no apiserver
	// read — the pre-#5991 request path, unchanged.
	var resolver proxy.PodResolver
	if cfg.PodAliasLabel != "" {
		clientset, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			logger.Error("kubernetes clientset init failed (needed for POD_ALIAS_LABEL)", "err", err)
			os.Exit(exitConfigError)
		}
		resolver = proxy.NewLabelPodResolver(clientset, cfg.PodAliasLabel, cfg.NodeName)
	}

	h, err := proxy.New(proxy.HandlerOptions{
		Logger:            logger,
		Verifier:          verifier,
		CertVerifier:      certVerifier,
		PodResolver:       resolver,
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

	mux := newMux(h)

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
		"tlsListen", tlsListenLog(cfg),
		"clientCertAuth", cfg.ClientCertAuthEnabled(),
		"clientCertSubjects", cfg.ClientCertSubjects,
		"podAliasLabel", cfg.PodAliasLabel,
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

	// Second listener, same mux, TLS + optional client-certificate
	// verification (#5991). The plaintext listener above is untouched:
	// r.TLS is nil there, so the certificate leg can never authenticate
	// a plaintext request no matter how the allowlist is configured.
	var tlsSrv *http.Server
	if cfg.TLSEnabled() {
		tlsCfg, err := buildTLSConfig(cfg)
		if err != nil {
			logger.Error("tls config build failed", "err", err)
			os.Exit(exitConfigError)
		}
		tlsSrv = &http.Server{
			Addr:              cfg.TLSListenAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			TLSConfig:         tlsCfg,
		}
		go func() {
			// Certificate + key are already in TLSConfig.Certificates.
			if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received; draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("http shutdown returned error", "err", err)
		}
		if tlsSrv != nil {
			if err := tlsSrv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("https shutdown returned error", "err", err)
			}
		}
	case err := <-errCh:
		logger.Error("listen failed", "err", err)
		os.Exit(exitListenError)
	}
}

// newMux is the route table. Extracted from main so a test can pin
// WHICH paths reach the exec handler — the apiserver-shaped route
// (#5991) is useless if the handler understands it but nothing routes
// it there, which is the "helper tested, call site unpinned" shape this
// repo keeps re-learning.
func newMux(h http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/proxy/exec/", h)
	// guacd builds "/api/v1/namespaces/{ns}/pods/{pod}/exec" with a
	// literal snprintf and offers no way to change it, so the proxy
	// answers on the apiserver's own path shape. See
	// internal/proxy/exec.go parseAPIServerExecPath.
	mux.Handle("/api/v1/namespaces/", h)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// buildTLSConfig loads the server keypair and, when a client CA is
// configured, the pool that client certificates must chain to.
//
// ClientAuth is VerifyClientCertIfGiven, not RequireAndVerify: the same
// listener must keep serving HMAC-authenticated callers that present no
// certificate at all. "IfGiven" means a certificate that IS presented
// must verify — a certificate from an unknown CA fails the handshake
// and never reaches the handler — while a caller presenting none simply
// arrives with an empty r.TLS.PeerCertificates and falls through to the
// HMAC leg.
func buildTLSConfig(cfg wsruntime.Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	out := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if cfg.TLSClientCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read client CA %q: %w", cfg.TLSClientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("client CA %q contains no usable certificate", cfg.TLSClientCAFile)
		}
		out.ClientCAs = pool
		out.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return out, nil
}

func tlsListenLog(cfg wsruntime.Config) string {
	if !cfg.TLSEnabled() {
		return "disabled"
	}
	return cfg.TLSListenAddr
}
