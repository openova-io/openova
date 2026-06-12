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
//
// G92.1 #2660 vCluster placement env vars (optional; defaults match
// the loft-sh/vcluster + bp-<name>-vcluster slot 54/58/59 convention).
// Set these only when a Sovereign customised the bp-<name>-vcluster
// chart values away from the default (e.g. host namespace renamed):
//
//	VCLUSTER_PLACEMENT_DMZ_NS       host ns for the DMZ vCluster
//	                                (default: dmz)
//	VCLUSTER_PLACEMENT_DMZ_SECRET   admin kubeconfig Secret name
//	                                (default: vc-dmz)
//	VCLUSTER_PLACEMENT_MGMT_NS      (default: mgmt)
//	VCLUSTER_PLACEMENT_MGMT_SECRET  (default: vc-mgmt)
//	VCLUSTER_PLACEMENT_RTZ_NS       (default: rtz)
//	VCLUSTER_PLACEMENT_RTZ_SECRET   (default: vc-rtz)
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
	"strings"
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
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		leaderElectNS        string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", env("METRICS_ADDR", ":8080"), "Address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", env("HEALTH_ADDR", ":8081"), "Address the probe endpoint binds to.")
	// Chart contract: products/catalyst/chart/templates/controllers/application-controller-deployment.yaml
	// passes --leader-elect={{ .Values.controllers.application.leaderElection.enabled }}
	// (and the sibling controllers do the same). Define the flag so the
	// binary doesn't crash on parse. The application controller uses a
	// custom unstructured.Watch loop rather than controller-runtime's
	// Manager (see runProbes comment below for the rationale), so
	// leader-election is currently a no-op — a single replica is
	// authoritative. The chart defaults replicas: 1; if/when HA is
	// introduced for this controller, wire enableLeaderElection +
	// leaderElectNS into a Lease-based election here.
	flag.BoolVar(&enableLeaderElection, "leader-elect", envBool("LEADER_ELECT", true), "Enable leader election (currently no-op; single-replica only).")
	flag.StringVar(&leaderElectNS, "leader-elect-namespace", env("LEADER_ELECT_NS", podNamespace()), "Namespace for the leader-election Lease (currently no-op).")
	flag.Parse()

	level := parseLogLevel(env("LOG_LEVEL", "info"))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	// Log startup args verbatim so operators (and the qa-loop matrix
	// at TC-181) can confirm the flag set the binary actually parsed
	// matches the chart's args block. Per
	// docs/INVIOLABLE-PRINCIPLES.md #2 visible state beats inferred
	// state — the alternative ("read /proc/<pid>/cmdline") is racy on
	// pod restart.
	logger.Info("application-controller startup args parsed",
		"leader-elect", enableLeaderElection,
		"leader-elect-namespace", leaderElectNS,
		"metrics-bind-address", metricsAddr,
		"health-probe-bind-address", probeAddr)

	if enableLeaderElection {
		logger.Info("leader-elect requested but unimplemented for application-controller; running as single replica",
			"leaderElectNS", leaderElectNS)
	}

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
		// qa-loop iter-8 Fix #42 bug 3: host-side Flux bootstrap so the
		// per-Application manifests we commit to Gitea actually get
		// pulled by Flux on the host cluster. Without these the
		// Application reaches Provisioning + Ready=True but no Pod
		// is ever scheduled.
		HostFluxNamespace:       env("HOST_FLUX_NAMESPACE", "flux-system"),
		GiteaInClusterURL:       env("GITEA_IN_CLUSTER_URL", "http://gitea-http.gitea.svc.cluster.local:3000"),
		HostFluxIntervalSeconds: envInt("HOST_FLUX_INTERVAL_SECONDS", 60),
		FluxGiteaSecretRef:      env("FLUX_GITEA_SECRET_REF", ""),
		// G93.2 (Refs #2667) — Sovereign-wide BCP topology. Stamped
		// from cloud-init by the G93.1 (Refs #2666) chain:
		// var.bcp_topology → SOVEREIGN_BCP_TOPOLOGY (Kustomization
		// postBuild.substitute) → bp-catalyst-platform chart slot 13 →
		// catalyst-api Pod env. The application-controller reads this
		// once at startup; the value is Sovereign-wide invariant for
		// the life of the prov (changing it requires a wipe + fresh
		// prov per ADR-0001 §A4). Empty = unset, treated as
		// single-region by placement.EffectiveDefault.
		SovereignBcpTopology: env("SOVEREIGN_BCP_TOPOLOGY", ""),
		// G92.1 #2660 — vCluster placement map. Defaults match the
		// loft-sh convention (HostNamespace = name, Secret = "vc-"+name).
		// Operators override per Sovereign via env vars when the
		// bp-<name>-vcluster chart was customised.
		VClusterPlacements: map[string]controller.VClusterPlacement{
			"dmz": {
				HostNamespace:    env("VCLUSTER_PLACEMENT_DMZ_NS", "dmz"),
				KubeconfigSecret: env("VCLUSTER_PLACEMENT_DMZ_SECRET", "vc-dmz"),
			},
			"mgmt": {
				HostNamespace:    env("VCLUSTER_PLACEMENT_MGMT_NS", "mgmt"),
				KubeconfigSecret: env("VCLUSTER_PLACEMENT_MGMT_SECRET", "vc-mgmt"),
			},
			"rtz": {
				HostNamespace:    env("VCLUSTER_PLACEMENT_RTZ_NS", "rtz"),
				KubeconfigSecret: env("VCLUSTER_PLACEMENT_RTZ_SECRET", "vc-rtz"),
			},
		},
		// #3375 DoD-4 — the region suffix ("A"|"B") this controller runs
		// in, derived from the same SOVEREIGN_REGION_ROLE the cnpg-pair
		// bootstrap slot uses (primary→A, secondary→B). The cluster-ID
		// registry consults it to resolve declared cluster IDs to the
		// right kubeConfig Secret. Empty/unknown defaults to "A".
		LocalRegion: regionSuffixFromRole(env("SOVEREIGN_REGION_ROLE", "primary")),
	}
}

// regionSuffixFromRole maps the per-region SOVEREIGN_REGION_ROLE value
// (primary|secondary) to the canonical region suffix ("A"|"B") used in
// cluster IDs (mgmt-A / mgmt-B). Anything other than "secondary" maps to
// "A" (the safe primary default).
func regionSuffixFromRole(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "secondary") {
		return "B"
	}
	return "A"
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

// envBool reads a bool env var; the empty value or invalid input
// returns the fallback. Mirrors useraccess-controller for consistency.
func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

// podNamespace reads /var/run/secrets/kubernetes.io/serviceaccount/namespace
// (the in-cluster ServiceAccount projection). When run out of cluster
// (`go run ./cmd`) it falls back to "openova-system" — the canonical
// home for Catalyst control-plane components per
// docs/EPICS-1-6-unified-design.md §1.2.
func podNamespace() string {
	const path = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	b, err := os.ReadFile(path)
	if err != nil {
		return "openova-system"
	}
	return string(b)
}
