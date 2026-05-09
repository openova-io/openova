// application-controller — slice C4 of EPIC-0 #1095.
//
// Watches `Application.apps.openova.io/v1` CRs and reconciles each
// Application to:
//
//  1. Resolve parents (Environment + Organization).
//  2. Fetch the Blueprint at spec.blueprintRef.
//  3. Validate spec.parameters against Blueprint.spec.configSchema
//     using the canonical JSON Schema validator
//     (github.com/santhosh-tekuri/jsonschema/v5).
//  4. Resolve placement → per-region work plan.
//  5. Render + commit per-region kustomization.yaml + helmrelease.yaml
//     to the per-Org Gitea repo at <org>/<app> on the env-type-mapped
//     branch (develop|staging|main per NAMING §11.2).
//  6. Update Application.status with phase / primaryRegion /
//     regions[] / giteaRepo / installedBlueprint / conditions.
//
// Configuration is environment-only — per Inviolable Principle #4
// (never hardcode), nothing is built into the binary that an operator
// can't override per-Sovereign:
//
//	GITEA_API_URL          API base URL (default
//	                       http://gitea-http.gitea.svc.cluster.local:3000)
//	GITEA_TOKEN            personal-access token with repo + write:repository
//	GITEA_PUBLIC_URL       externally-visible Gitea base URL stamped on
//	                       Application.status.giteaRepo
//	COMMIT_AUTHOR_NAME     default: application-controller
//	COMMIT_AUTHOR_EMAIL    default: application-controller@openova.io
//	SOURCE_NAMESPACE       Flux source CR namespace inside vCluster
//	                       (default: flux-system)
//	HELMRELEASE_INTERVAL   per-HelmRelease reconcile interval seconds
//	                       (default: 600)
//	CATALOG_SOURCE_REF     Flux source ref name when Blueprint omits one
//	                       (default: openova-catalog)
//	REQUEUE_AFTER_SECONDS  background re-queue interval (default: 300)
//	METRICS_ADDR           default :8080
//	HEALTH_ADDR            default :8081
//	LEADER_ELECT           true|false (default true)
//	LEADER_ELECT_NS        default: in-cluster pod namespace; falls
//	                       back to "openova-system"
//	LOG_LEVEL              debug|info|warn|error (default info)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/core/controllers/application/internal/controller"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

func main() {
	var (
		metricsAddr string
		probeAddr   string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", env("METRICS_ADDR", ":8080"), "Address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", env("HEALTH_ADDR", ":8081"), "Address the probe endpoint binds to.")
	flag.Parse()

	level := parseLogLevel(env("LOG_LEVEL", "info"))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg := loadConfigFromEnv()
	logger.Info("starting application-controller",
		"giteaPublicURL", cfg.GiteaPublicURL,
		"sourceNamespace", cfg.SourceNamespace,
		"helmReleaseIntervalSeconds", cfg.HelmReleaseIntervalSeconds)

	restCfg, err := buildRestConfig()
	if err != nil {
		logger.Error("rest config", "err", err)
		os.Exit(1)
	}

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		logger.Error("dynamic client", "err", err)
		os.Exit(1)
	}

	giteaClient := gitea.New(env("GITEA_API_URL", "http://gitea-http.gitea.svc.cluster.local:3000"), os.Getenv("GITEA_TOKEN"))

	r := controller.New(dyn, giteaClient, giteaClassifier{}, cfg, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Lightweight HTTP server for /healthz, /readyz, /metrics. The
	// controller-runtime Manager would give us this for free, but slice
	// C3 (blueprint-controller) adopted the "watch loop directly"
	// pattern for the same reason — unstructured.Watch has no
	// dependency on a scheme.AddToScheme call, which simplifies
	// per-Sovereign deployment of a new CRD version.
	go runProbes(ctx, metricsAddr, probeAddr, logger)

	if err := r.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("controller exited", "err", err)
		os.Exit(1)
	}
}

func loadConfigFromEnv() controller.Config {
	return controller.Config{
		GiteaPublicURL:             os.Getenv("GITEA_PUBLIC_URL"),
		CommitAuthorName:           env("COMMIT_AUTHOR_NAME", "application-controller"),
		CommitAuthorEmail:          env("COMMIT_AUTHOR_EMAIL", "application-controller@openova.io"),
		SourceNamespace:            env("SOURCE_NAMESPACE", "flux-system"),
		HelmReleaseIntervalSeconds: envInt("HELMRELEASE_INTERVAL", 600),
		CatalogSourceRef:           env("CATALOG_SOURCE_REF", "openova-catalog"),
		RequeueAfter:               time.Duration(envInt("REQUEUE_AFTER_SECONDS", 300)) * time.Second,
	}
}

// giteaClassifier adapts the gitea package's typed errors to the
// Reconciler's GiteaErrorClassifier interface. Keeps the controller
// package transport-agnostic for testability.
type giteaClassifier struct{}

func (giteaClassifier) IsNotFound(err error) bool    { return gitea.IsNotFound(err) }
func (giteaClassifier) IsOrgNotFound(err error) bool { return errors.Is(err, gitea.ErrOrgNotFound) }

// runProbes runs a tiny http.Server with /healthz, /readyz, and a
// stub /metrics endpoint. Per Inviolable Principle #4 the addresses
// are configurable.
func runProbes(ctx context.Context, metricsAddr, probeAddr string, logger *slog.Logger) {
	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	probeMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		// Stub metrics endpoint — the slice ships health endpoints
		// for k8s probes; full Prometheus instrumentation lands in a
		// follow-up slice (slice OBS-1 per the EPIC-0 docs).
		_, _ = fmt.Fprintf(w, "# application-controller stub\n")
	})
	probeSrv := &http.Server{Addr: probeAddr, Handler: probeMux, ReadHeaderTimeout: 5 * time.Second}
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = probeSrv.Shutdown(shutdownCtx)
		_ = metricsSrv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := probeSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("probe server", "err", err)
		}
	}()
	if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Warn("metrics server", "err", err)
	}
}

func buildRestConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// Fallback for `go run` against a local k3s — picks up KUBECONFIG
	// or ~/.kube/config.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeconfig.ClientConfig()
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
