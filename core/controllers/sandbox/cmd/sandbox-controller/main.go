// sandbox-controller — Wave 1 + Wave 8 + Wave 9 of the Sandbox product
// (products/sandbox/docs/architecture.md §7).
//
// Production entry point. Reads configuration from environment vars,
// constructs the controller-runtime manager, and starts the Sandbox
// reconciler with leader election.
package main

import (
	"context"
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/controller"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/idlescaler"
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

	giteaURL := mustEnv("CATALYST_GITEA_URL", log)
	giteaToken := mustEnv("CATALYST_GITEA_TOKEN", log)
	hostCluster := mustEnv("CATALYST_HOST_CLUSTER", log)
	sovereignFQDN := mustEnv("CATALYST_SOVEREIGN_FQDN", log)

	branch := envOr("CATALYST_GITEA_BRANCH", "main")
	tenantRepo := envOr("CATALYST_TENANT_REPO_NAME", "catalyst-tenant")

	// Wave 8 runtime env — per-Sandbox pty-server / MCP / NEWAPI for
	// the rendered Pod manifests.
	ptyServerImage := mustEnv("SANDBOX_PTY_SERVER_IMAGE", log)
	mcpImage := mustEnv("SANDBOX_MCP_IMAGE", log)
	sandboxNewapiURL := mustEnv("SANDBOX_NEWAPI_URL", log)
	llmGatewayTokenSecret := envOr("SANDBOX_LLM_GATEWAY_TOKEN_SECRET", "sandbox-tokens")
	byosSecretPrefix := envOr("SANDBOX_BYOS_SECRET_PREFIX", "sandbox-byos-claude-code")
	idleTimeoutMinutes := envOrInt("SANDBOX_IDLE_TIMEOUT_MINUTES", 30)

	// TBD-V22 #1986 F1 (2026-05-20) — replay ring buffer size in bytes.
	// 0 (the default when SANDBOX_RING_BUFFER_BYTES is unset / empty /
	// non-integer / non-positive) leaves the per-Sandbox pty-server
	// StatefulSet without the env var, so pty-server falls back to its
	// own session.DefaultRingBytes (1 MiB). Chart default in
	// platform/sandbox/chart/values.yaml::runtime.ringBufferBytes also
	// emits 1048576 explicitly so the operator-visible env var is set
	// out of the box.
	ringBufferBytes := envOrInt("SANDBOX_RING_BUFFER_BYTES", 0)

	// Wave 9 — NewAPI bridge wiring. Two env vars carry the bridge URL +
	// admin bearer used by the controller to call POST
	// /admin/tokens/sandbox (catalyst-api bridge handler, PR #1638).
	// Both are REQUIRED in production — a sandbox-controller without
	// the bridge wired silently ships Sandboxes without an LLM
	// connection. Permit unset for compatibility with smoke tests
	// that exercise only the gitops path (env both unset ⇒ controller
	// runs without the token-mint path; log line announces it).
	newapiBaseURL := strings.TrimSpace(os.Getenv("NEWAPI_BASE_URL"))
	newapiAdmin := strings.TrimSpace(os.Getenv("NEWAPI_ADMIN_SECRET"))
	defaultChannels := splitAndTrim(envOr("NEWAPI_DEFAULT_CHANNELS", ""), ",")

	// D31 active-hot-standby — Sovereign-level toggle + region pair the
	// controller threads into every per-Sandbox MCP Pod. The MCP
	// server's sandbox.db.provision handler reads these at call time
	// and, when valid, materialises a primary + replica Cluster.
	// postgresql.cnpg.io pair instead of a single Cluster (DoD D31).
	// Default-empty keeps every existing Sandbox on single-Cluster
	// CNPG (zero regression). Bootstrap-kit slot 61 wires these from
	// the per-Sovereign overlay's envsubst placeholders into the
	// bp-sandbox HelmRelease values.
	enableHotStandby := envOr("SOVEREIGN_ENABLE_HOT_STANDBY", "")
	primaryRegion := envOr("SOVEREIGN_PRIMARY_REGION", "")
	replicaRegion := envOr("SOVEREIGN_REPLICA_REGION", "")

	// TBD-P4 B4 — canonical SANDBOX_* env wiring for the MCP plugin
	// (products/sandbox/mcp-server/internal/tools/env.go). All have
	// in-cluster defaults; per-Sovereign overlays may override via
	// bp-sandbox HR values. Empty leaves the MCP's per-tool guard to
	// surface "not configured" at call time rather than crashing the
	// controller at startup.
	mcpGiteaBaseURL := envOr("SANDBOX_MCP_GITEA_BASE_URL", giteaURL)
	mcpGiteaTokenSecretName := envOr("SANDBOX_MCP_GITEA_TOKEN_SECRET_NAME", "catalyst-gitea-token")
	mcpGiteaTokenSecretKey := envOr("SANDBOX_MCP_GITEA_TOKEN_SECRET_KEY", "token")
	mcpDomainAPIURL := envOr("SANDBOX_MCP_DOMAIN_API_URL", "http://domain.org-services.svc.cluster.local:8086")
	mcpMarketplaceAPIURL := envOr("SANDBOX_MCP_MARKETPLACE_API_URL", "http://marketplace-api.marketplace.svc.cluster.local:8082")
	mcpStorageS3Endpoint := envOr("SANDBOX_MCP_STORAGE_S3_ENDPOINT", "http://seaweedfs.storage.svc.cluster.local:8333")
	mcpStorageS3Region := envOr("SANDBOX_MCP_STORAGE_S3_REGION", "us-east-1")
	mcpStorageS3UseTLS := envOr("SANDBOX_MCP_STORAGE_S3_USE_TLS", "false")
	mcpStorageS3CredsSecret := envOr("SANDBOX_MCP_STORAGE_S3_CREDS_SECRET_NAME", "")
	mcpStorageS3AccessKeyKey := envOr("SANDBOX_MCP_STORAGE_S3_ACCESS_KEY_KEY", "AWS_ACCESS_KEY_ID")
	mcpStorageS3SecretKeyKey := envOr("SANDBOX_MCP_STORAGE_S3_SECRET_KEY_KEY", "AWS_SECRET_ACCESS_KEY")
	mcpKeycloakAdminURL := envOr("SANDBOX_MCP_KEYCLOAK_ADMIN_URL", "http://keycloak.keycloak.svc.cluster.local:8080")
	mcpKeycloakParentRealm := envOr("SANDBOX_MCP_KEYCLOAK_PARENT_REALM", "master")
	mcpKeycloakAdminTokenSecret := envOr("SANDBOX_MCP_KEYCLOAK_ADMIN_TOKEN_SECRET_NAME", "")
	mcpKeycloakAdminTokenSecretKey := envOr("SANDBOX_MCP_KEYCLOAK_ADMIN_TOKEN_SECRET_KEY", "token")

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
	if newapiBaseURL != "" && newapiAdmin != "" {
		c, err := newapi.New(newapiBaseURL, newapiAdmin, nil)
		if err != nil {
			log.Error(err, "newapi client init")
			os.Exit(1)
		}
		newapiClient = c
	} else {
		log.Info("newapi bridge not wired — sandbox-controller running in gitops-only mode",
			"newapi_base_url_set", newapiBaseURL != "",
			"newapi_admin_secret_set", newapiAdmin != "",
		)
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
		NewapiURL:             sandboxNewapiURL,
		LLMGatewayTokenSecret: llmGatewayTokenSecret,
		BYOSSecretPrefix:      byosSecretPrefix,
		IdleTimeoutMinutes:    idleTimeoutMinutes,
		RingBufferBytes:       ringBufferBytes,
		NewAPIClient:          newapiClient,
		DefaultChannels:       defaultChannels,
		EnableHotStandby:      enableHotStandby,
		PrimaryRegion:         primaryRegion,
		ReplicaRegion:         replicaRegion,
		// TBD-P4 B4 — canonical SANDBOX_* env-var wiring for MCP plugin.
		GiteaBaseURL:                mcpGiteaBaseURL,
		GiteaTokenSecretName:        mcpGiteaTokenSecretName,
		GiteaTokenSecretKey:         mcpGiteaTokenSecretKey,
		DomainAPIURL:                mcpDomainAPIURL,
		MarketplaceAPIURL:           mcpMarketplaceAPIURL,
		StorageS3Endpoint:           mcpStorageS3Endpoint,
		StorageS3Region:             mcpStorageS3Region,
		StorageS3UseTLS:             mcpStorageS3UseTLS,
		StorageS3CredsSecretName:    mcpStorageS3CredsSecret,
		StorageS3AccessKeyKey:       mcpStorageS3AccessKeyKey,
		StorageS3SecretKeyKey:       mcpStorageS3SecretKeyKey,
		KeycloakAdminURL:            mcpKeycloakAdminURL,
		KeycloakParentRealm:         mcpKeycloakParentRealm,
		KeycloakAdminTokenSecret:    mcpKeycloakAdminTokenSecret,
		KeycloakAdminTokenSecretKey: mcpKeycloakAdminTokenSecretKey,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "setup reconciler")
		os.Exit(1)
	}

	// Wave 10 (PR #1641 follow-up) — IdleScaler reads the
	// `openova.io/sandbox-idle-timeout-minutes` annotation the
	// renderer writes on every pty-server StatefulSet, polls each
	// pty-server Service for live activity, and scales replicas to 0
	// once the idle window has elapsed. Leader-elected so HA
	// controller replicas don't race.
	scaler := idlescaler.New(mgr.GetClient(),
		log.WithName("idle-scaler"),
		idlescaler.Options{
			DefaultIdleTimeoutMinutes: idleTimeoutMinutes,
		})
	if err := mgr.Add(scaler); err != nil {
		log.Error(err, "add idle-scaler to manager")
		os.Exit(1)
	}

	// D35 consume-leg — subscribe to `catalyst.tenant.sandbox_requested`
	// so the publish from tenant-service nudges the matching Sandbox CR
	// into a fresh Reconcile within ~50ms. Same wiring shape as the
	// organization-controller's NATS bridge. Best-effort: NATS_URL
	// unset → log + continue (informer requeue fallback intact).
	natsURL := strings.TrimSpace(os.Getenv("NATS_URL"))
	sandboxNs := envOr("SANDBOX_NAMESPACE", "catalyst-system")
	if natsURL != "" {
		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			sub, err := natsbus.Connect(natsURL)
			if err != nil {
				log.Error(err, "natsbus: connect failed — D35 consume-leg disabled",
					"nats_url", natsURL)
				return nil
			}
			bridge := &controller.NATSBridge{
				Client:    mgr.GetClient(),
				Log:       log.WithName("natsbridge"),
				Namespace: sandboxNs,
			}
			if err := sub.Subscribe(ctx,
				natsbus.SubjectTenantSandboxRequested,
				"sandbox-controller-sandbox-requested",
				bridge.HandleSandboxRequested,
				natsbus.SubscribeOptions{},
			); err != nil {
				log.Error(err, "natsbus: subscribe tenant.sandbox_requested failed")
			}
			<-ctx.Done()
			sub.Close()
			return nil
		})); err != nil {
			log.Error(err, "natsbus: add runnable failed")
			os.Exit(1)
		}
		log.Info("natsbus: D35 consume-leg wired",
			"nats_url", natsURL,
			"subjects", []string{natsbus.SubjectTenantSandboxRequested},
			"sandbox_namespace", sandboxNs,
		)
	} else {
		log.Info("natsbus: NATS_URL unset — D35 consume-leg disabled (informer-requeue fallback only)")
	}

	log.Info("starting manager",
		"host_cluster", hostCluster,
		"sovereign_fqdn", sovereignFQDN,
		"gitea_url", giteaURL,
		"tenant_repo", tenantRepo,
		"pty_server_image", ptyServerImage,
		"mcp_image", mcpImage,
		"newapi_url", sandboxNewapiURL,
		"llm_gateway_token_secret", llmGatewayTokenSecret,
		"byos_secret_prefix", byosSecretPrefix,
		"idle_timeout_minutes", idleTimeoutMinutes,
		"ring_buffer_bytes", ringBufferBytes,
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
