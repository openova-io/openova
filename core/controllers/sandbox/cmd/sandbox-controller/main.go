// sandbox-controller — Wave 1 + Wave 8 of the Sandbox product
// (products/sandbox/docs/architecture.md §7).
//
// Production entry point. Reads configuration from environment vars,
// constructs the controller-runtime manager, and starts the Sandbox
// reconciler with leader election.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
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

	giteaURL := mustEnv("CATALYST_GITEA_URL", log)
	giteaToken := mustEnv("CATALYST_GITEA_TOKEN", log)
	hostCluster := mustEnv("CATALYST_HOST_CLUSTER", log)
	sovereignFQDN := mustEnv("CATALYST_SOVEREIGN_FQDN", log)

	branch := envOr("CATALYST_GITEA_BRANCH", "main")
	tenantRepo := envOr("CATALYST_TENANT_REPO_NAME", "catalyst-tenant")

	// Wave 8 runtime env — per-Sandbox pty-server / MCP / NEWAPI.
	ptyServerImage := mustEnv("SANDBOX_PTY_SERVER_IMAGE", log)
	mcpImage := mustEnv("SANDBOX_MCP_IMAGE", log)
	newapiURL := mustEnv("SANDBOX_NEWAPI_URL", log)
	llmGatewayTokenSecret := envOr("SANDBOX_LLM_GATEWAY_TOKEN_SECRET", "sandbox-tokens")
	byosSecretPrefix := envOr("SANDBOX_BYOS_SECRET_PREFIX", "sandbox-byos-claude-code")
	idleTimeoutMinutes := envOrInt("SANDBOX_IDLE_TIMEOUT_MINUTES", 30)

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

	r := &controller.Reconciler{
		Client:                mgr.GetClient(),
		Log:                   log.WithName("reconciler"),
		GiteaClient:           gitea.New(giteaURL, giteaToken),
		HostCluster:           hostCluster,
		SovereignFQDN:         sovereignFQDN,
		Branch:                branch,
		TenantRepoName:        tenantRepo,
		PtyServerImage:        ptyServerImage,
		MCPImage:              mcpImage,
		NewapiURL:             newapiURL,
		LLMGatewayTokenSecret: llmGatewayTokenSecret,
		BYOSSecretPrefix:      byosSecretPrefix,
		IdleTimeoutMinutes:    idleTimeoutMinutes,
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
		"pty_server_image", ptyServerImage,
		"mcp_image", mcpImage,
		"newapi_url", newapiURL,
		"llm_gateway_token_secret", llmGatewayTokenSecret,
		"byos_secret_prefix", byosSecretPrefix,
		"idle_timeout_minutes", idleTimeoutMinutes,
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

// envOrInt parses an integer env var; non-integer / empty returns the
// fallback. Used for SANDBOX_IDLE_TIMEOUT_MINUTES — operator drift
// (mistyped value) shouldn't crash the controller.
func envOrInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
