// environment-controller — slice C2 of EPIC-0 #1095.
//
// Watches `Environment.catalyst.openova.io/v1` CRs (cluster-scoped) and
// reconciles each Environment to:
//
//  1. Verify the per-Org Gitea Org exists.
//  2. Render + idempotently commit the per-vCluster Flux GitRepository
//     manifest into the Org's Gitea repo.
//  3. Surface canonical branch + JetStream subject prefix on status.
//
// All configuration is read from environment variables at startup —
// Inviolable Principle #4 (never hardcode).
package main

import (
	"flag"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	envv1 "github.com/openova-io/openova/core/controllers/environment/api/v1"
	"github.com/openova-io/openova/core/controllers/environment/internal/controller"
	"github.com/openova-io/openova/core/controllers/environment/internal/gitea"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(envv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Bind address for /metrics.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Bind address for healthz/readyz.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election (one active replica). Required for HA deployments.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg := loadConfigFromEnv()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "environment-controller.catalyst.openova.io",
		LeaderElectionNamespace: getEnvDefault("LEADER_ELECTION_NAMESPACE", "catalyst-system"),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	giteaClient := gitea.NewClient(
		getEnvDefault("GITEA_API_URL", "http://gitea-http.gitea.svc.cluster.local:3000/api/v1"),
		os.Getenv("GITEA_TOKEN"),
	)

	if err := (&controller.EnvironmentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Gitea:  giteaClient,
		Cfg:    cfg,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up reconciler")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up readyz")
		os.Exit(1)
	}

	setupLog.Info("starting environment-controller",
		"giteaPublicURL", cfg.GiteaPublicURL,
		"fluxNamespace", cfg.FluxNamespace,
		"envRepoSuffix", cfg.EnvRepoSuffix,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

func loadConfigFromEnv() controller.Config {
	cfg := controller.Config{
		FluxNamespace:       getEnvDefault("FLUX_NAMESPACE", "flux-system"),
		FluxIntervalSeconds: getEnvIntDefault("FLUX_INTERVAL_SECONDS", 60),
		GiteaPublicURL:      os.Getenv("GITEA_PUBLIC_URL"),
		GiteaSecretRef:      getEnvDefault("GITEA_SECRET_REF", "gitea-flux-token"),
		CommitAuthorName:    getEnvDefault("COMMIT_AUTHOR_NAME", "environment-controller"),
		CommitAuthorEmail:   getEnvDefault("COMMIT_AUTHOR_EMAIL", "environment-controller@openova.io"),
		EnvRepoSuffix:       getEnvDefault("ENV_REPO_SUFFIX", "-environment"),
		RequeueAfter:        time.Duration(getEnvIntDefault("REQUEUE_AFTER_SECONDS", 300)) * time.Second,
	}
	return cfg.Defaults()
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
