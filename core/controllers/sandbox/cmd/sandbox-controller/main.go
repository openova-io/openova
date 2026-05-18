// sandbox-controller — Wave 1 of the Sandbox product
// (products/sandbox/docs/architecture.md §7).
//
// Production entry point. Reads configuration from environment vars,
// constructs the controller-runtime manager, and starts the Sandbox
// reconciler with leader election. SIGTERM / SIGINT are honored via
// signals.SetupSignalHandler() so a Pod-eviction triggers a clean
// shutdown.
//
// Per Inviolable Principle #4 every knob is an env var — no hardcoded
// URLs / versions / regions in code. Shape mirrors
// core/controllers/organization/cmd/main.go.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/controller"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/newapi"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sandboxapi.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. Defaults to true so HA replicas don't double-write.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("sandbox-controller")

	// Required env.
	giteaURL := mustEnv("CATALYST_GITEA_URL", log)
	giteaToken := mustEnv("CATALYST_GITEA_TOKEN", log)
	hostCluster := mustEnv("CATALYST_HOST_CLUSTER", log)
	sovereignFQDN := mustEnv("CATALYST_SOVEREIGN_FQDN", log)

	branch := envOr("CATALYST_GITEA_BRANCH", "main")
	tenantRepo := envOr("CATALYST_TENANT_REPO_NAME", "catalyst-tenant")

	// NewAPI bridge wiring. Two env vars carry the bridge URL + admin
	// bearer (see platform/sandbox/chart/templates/deployment.yaml).
	// Both are REQUIRED in production — a sandbox-controller without
	// the bridge wired silently ships Sandboxes without an LLM
	// connection. Permit unset for compatibility with smoke tests
	// that exercise only the gitops path (env both unset ⇒ controller
	// runs without the token-mint path; log line announces it).
	newapiURL := strings.TrimSpace(os.Getenv("NEWAPI_BASE_URL"))
	newapiAdmin := strings.TrimSpace(os.Getenv("NEWAPI_ADMIN_SECRET"))
	defaultChannels := splitAndTrim(envOr("NEWAPI_DEFAULT_CHANNELS", ""), ",")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "sandbox-controller.sandbox.openova.io",
	})
	if err != nil {
		log.Error(err, "manager init")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "readyz")
		os.Exit(1)
	}

	var newapiClient newapi.Client
	if newapiURL != "" && newapiAdmin != "" {
		c, err := newapi.New(newapiURL, newapiAdmin, nil)
		if err != nil {
			log.Error(err, "newapi client init")
			os.Exit(1)
		}
		newapiClient = c
	} else {
		log.Info("newapi bridge not wired — sandbox-controller running in gitops-only mode",
			"newapi_base_url_set", newapiURL != "",
			"newapi_admin_secret_set", newapiAdmin != "",
		)
	}

	r := &controller.Reconciler{
		Client:          mgr.GetClient(),
		Log:             log.WithName("reconciler"),
		GiteaClient:     gitea.New(giteaURL, giteaToken),
		HostCluster:     hostCluster,
		SovereignFQDN:   sovereignFQDN,
		Branch:          branch,
		TenantRepoName:  tenantRepo,
		NewAPIClient:    newapiClient,
		DefaultChannels: defaultChannels,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "setup reconciler")
		os.Exit(1)
	}

	log.Info("starting manager",
		"host_cluster", hostCluster,
		"sovereign_fqdn", sovereignFQDN,
		"gitea_url", giteaURL,
		"tenant_repo", tenantRepo,
		"newapi_wired", newapiClient != nil,
		"default_channels", defaultChannels,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager start")
		os.Exit(1)
	}
}

func mustEnv(key string, log interface {
	Error(err error, msg string, kvs ...any)
},
) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Error(fmt.Errorf("missing env"), "required env var unset", "key", key)
		os.Exit(2)
	}
	return v
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

// splitAndTrim splits s on sep and returns the non-empty trimmed
// pieces. "qwen,vllm , " → ["qwen","vllm"]. Empty s returns nil so
// the caller's len()==0 check is unambiguous.
func splitAndTrim(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
