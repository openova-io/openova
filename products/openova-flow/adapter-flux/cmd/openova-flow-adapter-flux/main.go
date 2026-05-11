// openova-flow-adapter-flux — DaemonSet sidecar that watches
// HelmRelease + HelmChart CRs in the local cluster and POSTs
// FlowMessage envelopes to a configured openova-flow-server.
//
// See products/openova-flow/adapter-flux/README.md for the contract.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every parameter is env-driven.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/config"
	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/informer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	restCfg, err := loadRestConfig(log)
	if err != nil {
		log.Error("kubeconfig", "err", err)
		os.Exit(1)
	}
	// Match catalyst-api factory's QPS/Burst — the cold-LIST on a
	// fresh Sovereign is multi-kind and starves on client-go
	// defaults.
	restCfg.QPS = 25
	restCfg.Burst = 50

	rt, err := informer.NewRuntime(cfg, restCfg, log)
	if err != nil {
		log.Error("informer init", "err", err)
		os.Exit(1)
	}

	// Tiny health endpoint so the chart's livenessProbe has a target.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	healthAddr := envDefault("HEALTH_LISTEN_ADDR", ":8081")
	healthSrv := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("adapter-flux health listening", "addr", healthAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health listen", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := rt.Start(ctx); err != nil {
		log.Error("informer", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = healthSrv.Shutdown(shutdownCtx)
}

func loadRestConfig(log *slog.Logger) (*rest.Config, error) {
	// Prefer in-cluster (production DaemonSet); fall back to
	// $KUBECONFIG or ~/.kube/config for local development.
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kc := os.Getenv("KUBECONFIG")
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc != "" {
		rules.ExplicitPath = kc
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
