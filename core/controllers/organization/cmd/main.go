// organization-controller — slice C1 of EPIC-0 #1095.
//
// Production entry point. Reads configuration from environment vars,
// constructs the controller-runtime manager, and starts the
// Organization reconciler with leader election. SIGTERM / SIGINT are
// honored via signals.SetupSignalHandler() so a Pod-eviction triggers
// a clean shutdown of the reconciler queue.
//
// Per Inviolable Principle #4 every knob is an env var — no
// hardcoded URLs/versions/regions in code.
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

	"github.com/openova-io/openova/core/controllers/organization/internal/controller"
	"github.com/openova-io/openova/core/controllers/organization/internal/iacbootstrap"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(orgapi.AddToScheme(scheme))
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
	log := ctrl.Log.WithName("organization-controller")

	// Required env.
	kcAddr := mustEnv("CATALYST_KC_ADDR", log)
	kcRealm := envOr("CATALYST_KC_REALM", "sovereign")
	kcSAID := mustEnv("CATALYST_KC_SA_CLIENT_ID", log)
	kcSASecret := mustEnv("CATALYST_KC_SA_CLIENT_SECRET", log)

	giteaURL := mustEnv("CATALYST_GITEA_URL", log)
	giteaToken := mustEnv("CATALYST_GITEA_TOKEN", log)

	// #3084 Part 2 — per-Org Keycloak realm auto-wiring. Defaults to
	// DISABLED (opt-in) — this path is FATAL-on-failure on the Org-creation
	// (Pillar-1 onboarding) path, so a KC-admin-API hiccup would break Org
	// creation. It ships dormant; an operator enables it by setting
	// CATALYST_PER_ORG_REALM_ENABLED=true AFTER validating on a live
	// Sovereign (hw101 + a tenant Org). See PR #3101 merge-readiness review.
	perOrgRealmEnabled := envBoolDefaultFalse("CATALYST_PER_ORG_REALM_ENABLED")

	hostCluster := mustEnv("CATALYST_HOST_CLUSTER", log)
	// Issue #4077 — seed values for the single region of the Environment CR
	// the controller auto-ensures per Org. CRD-valid defaults
	// (huawei/mee/rtz); the real host cluster is bound via the explicit
	// hostCluster override (= CATALYST_HOST_CLUSTER), and a canonical
	// `{provider}-{region}-{bb}-{envType}` host-cluster name (Hetzner)
	// overrides these at runtime. Per Inviolable Principle #4, env-driven.
	envRegionProvider := envOr("CATALYST_ENV_REGION_PROVIDER", "huawei")
	envRegionCode := envOr("CATALYST_ENV_REGION_CODE", "mee")
	envRegionBuildingBlock := envOr("CATALYST_ENV_REGION_BUILDING_BLOCK", "rtz")
	chartVer := envOr("CATALYST_VCLUSTER_CHART_VERSION", "0.33.*")
	helmRepoName := envOr("CATALYST_VCLUSTER_HELMREPO_NAME", "loft")
	helmRepoNs := envOr("CATALYST_VCLUSTER_HELMREPO_NAMESPACE", "vcluster-system")
	// #3760 (Refs #3376 #3754): the Sovereign-local Harbor host the per-Org
	// vCluster images pull through (proxy-cache). Default harbor.openova.io;
	// cutover Step-04 flips it to harbor.<sovereign-fqdn>. Without proxying,
	// the harbor-proxy-pull Kyverno ClusterPolicy DENIES the StatefulSet.
	vclusterImageRegistry := envOr("CATALYST_VCLUSTER_IMAGE_REGISTRY", "harbor.openova.io")
	branch := envOr("CATALYST_GITEA_BRANCH", "main")
	// PR #3700 §4.3 — per-Org vCluster Flux loop. The in-cluster Gitea URL
	// the per-Org Flux GitRepository clones from. Defaults to the same
	// in-cluster Service the application-controller's GiteaInClusterURL +
	// the chart's Flux sources use. When this resolves empty the loop is
	// skipped (vclusterReadiness keeps reporting Pending) — but the default
	// ensures a fresh prov wires it automatically with zero operator action.
	giteaInClusterURL := envOr("CATALYST_GITEA_INCLUSTER_URL", "http://gitea-http.gitea.svc.cluster.local:3000")
	fluxNamespace := envOr("CATALYST_FLUX_NAMESPACE", "flux-system")
	fluxIntervalSeconds := envIntOr("CATALYST_FLUX_INTERVAL_SECONDS", 60)
	// Flux basic-auth Secret (in fluxNamespace) holding the Gitea PAT the
	// per-Org GitRepository uses to clone (bp-gitea REQUIRE_SIGNIN_VIEW=true
	// → anonymous clone 401s). Empty == anonymous, matching the
	// application-controller's FLUX_GITEA_SECRET_REF default.
	fluxGiteaSecretRef := envOr("CATALYST_FLUX_GITEA_SECRET_REF", "")
	// Slice F2 (#1098): namespace where federation client-secret K8s
	// Secrets live. Defaults to the controller's own namespace so the
	// ClusterRole `secrets:get` rule + cache scope stay minimal.
	fedSecretNs := envOr("CATALYST_FEDERATION_SECRET_NAMESPACE", "catalyst-controllers")
	// Namespace where per-Org UserAccess Claim CRs are written. Defaults
	// to `catalyst-system` per the qa-fixtures convention. Per Inviolable
	// Principle #4 the deployment env can override this for non-canonical
	// installs.
	uaNs := envOr("CATALYST_USERACCESS_NAMESPACE", "catalyst-system")

	// #4075 — per-pool console TLS. The dedicated console Cilium Gateway
	// (#4053) the per-Org console HTTPRoute attaches to + the per-pool
	// `*.<parentDomain>` wildcard listener/Certificate are appended to.
	// Defaults match clusters/_template/sovereign-tls/cilium-gateway-
	// console.yaml + the DNS-01 ClusterIssuer every Sovereign installs
	// (bp-cert-manager-powerdns-webhook). Per Inviolable Principle #4 all
	// four knobs are env-overridable for non-canonical installs.
	consoleGatewayName := envOr("CATALYST_CONSOLE_GATEWAY_NAME", "cilium-gateway-console")
	consoleGatewayNs := envOr("CATALYST_CONSOLE_GATEWAY_NAMESPACE", "kube-system")
	consoleTLSIssuer := envOr("CATALYST_CONSOLE_TLS_CLUSTER_ISSUER", "letsencrypt-dns01-prod-powerdns")
	consoleTLSCertNs := envOr("CATALYST_CONSOLE_TLS_CERT_NAMESPACE", "kube-system")

	// #4186 — per-Org console HTTPRoute backends. The console host
	// (console.<slug>.<pool>) is served by catalyst-ui + catalyst-api in
	// catalyst-system; the route + backendRefs share that namespace so they
	// resolve without a ReferenceGrant. Defaults match the catalyst chart
	// Service names/ports; env-overridable per Inviolable Principle #4.
	consoleRouteNs := envOr("CATALYST_CONSOLE_ROUTE_NAMESPACE", "catalyst-system")
	consoleAPISvc := envOr("CATALYST_CONSOLE_API_SERVICE", "catalyst-api")
	consoleUISvc := envOr("CATALYST_CONSOLE_UI_SERVICE", "catalyst-ui")
	consoleAPIPort := int32(envIntOr("CATALYST_CONSOLE_API_PORT", 8080))
	consoleUIPort := int32(envIntOr("CATALYST_CONSOLE_UI_PORT", 80))

	// #4236 (the #4179 final layer) — per-Org pool-DNS A-record write. The
	// org-controller writes `console.<slug>.<parentDomain>` + the per-Org
	// wildcard to the CENTRAL PowerDNS (pdns.openova.io) so a fresh
	// marketplace-funnel signup's console host resolves. #4222 wired this only
	// into the catalyst-api BSS pipeline (a door the marketplace funnel never
	// runs); the org-controller reconciles every Org CR so it covers both doors.
	//
	// poolPowerDNSURL / poolPowerDNSAPIKey — the CENTRAL pool pdns endpoint + key.
	// The key is the same central credential #4218 reflects into catalyst-system
	// as `pool-powerdns-api-credentials` (this controller runs in catalyst-system
	// too). Empty → the pool-DNS write degrades to a logged no-op (single-pdns /
	// Catalyst-Zero), never a wedge. Per Inviolable Principle #4, env-driven.
	poolPowerDNSURL := envOr("CATALYST_POOL_POWERDNS_API_URL", "")
	poolPowerDNSAPIKey := strings.TrimSpace(os.Getenv("CATALYST_POOL_POWERDNS_API_KEY"))
	// #4290/#4179 SELF-HEAL — the env above is a `secretKeyRef ... optional:true`
	// bound ONCE at Pod start; on a fresh prov the Pod almost always starts before
	// bp-reflector fills the bridged Secret, so the env is frozen empty and the
	// pool A-record write never fires (have_key:false forever). These two name the
	// Secret so the reconciler can READ THE KEY LIVE each reconcile and self-heal
	// the moment reflection lands — no Pod roll. Defaults match the #4218 chart
	// PULL-stub (Secret `pool-powerdns-api-credentials` in the controller's own
	// namespace). Per Inviolable Principle #4, both env-overridable.
	poolPowerDNSSecretName := envOr("CATALYST_POOL_POWERDNS_SECRET_NAME", "pool-powerdns-api-credentials")
	poolPowerDNSSecretNS := strings.TrimSpace(os.Getenv("CATALYST_POOL_POWERDNS_SECRET_NAMESPACE"))
	if poolPowerDNSSecretNS == "" {
		// Default to the controller's own namespace (where the #4218 bridge
		// reflects the key). POD_NAMESPACE is injected via the downward API in
		// the chart; fall back to catalyst-system to match targetNamespace.
		poolPowerDNSSecretNS = strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
		if poolPowerDNSSecretNS == "" {
			poolPowerDNSSecretNS = "catalyst-system"
		}
	}
	// #4475 §2 SELF-HEAL — the reflector SOURCE secret the bridged destination
	// above is filled FROM. bp-reflector only re-emits into the destination on a
	// reconcile cycle AFTER the source exists; on a fresh prov the source is
	// created LATE (cert-manager DNS-01 solver) and the reflector can fail to
	// re-scan after the late source-create, leaving the bridged destination empty
	// INDEFINITELY. These two name the SOURCE so the reconciler can read the key
	// DIRECTLY from it as a last-resort fallback — sidestepping a stuck reflector.
	// Defaults match the #4218 chart PULL-stub source
	// (`cert-manager/powerdns-api-credentials`). Per Inviolable Principle #4, both
	// env-overridable.
	poolPowerDNSSourceSecretName := envOr("CATALYST_POOL_POWERDNS_SOURCE_SECRET_NAME", "powerdns-api-credentials")
	poolPowerDNSSourceSecretNS := envOr("CATALYST_POOL_POWERDNS_SOURCE_SECRET_NAMESPACE", "cert-manager")
	// tenantConsoleLBIPv4 — the dedicated console-ELB EIP (#4053) the per-Org
	// console host resolves to. tenantPrimaryLBIPv4 — the primary/shared LB IP
	// fallback for a single-LB / pre-#4053 Sovereign. Both empty → the write is
	// skipped (logged), never a hard error.
	tenantConsoleLBIPv4 := strings.TrimSpace(os.Getenv("CATALYST_TENANT_CONSOLE_LB_IPV4"))
	tenantPrimaryLBIPv4 := strings.TrimSpace(os.Getenv("CATALYST_OTECH_INGRESS_IPV4"))

	// G117.3 W2.C3 — IaC repo bootstrap (ADR-0009).
	// OpenBao seam for per-Org Gitea robot-token storage. Optional:
	// when CATALYST_OPENBAO_ADDR is unset the bootstrap flow renders
	// state=Disabled on the Organization status so the operator console
	// shows a "feature not wired" badge rather than appearing to hang.
	openbaoAddr := strings.TrimSpace(os.Getenv("CATALYST_OPENBAO_ADDR"))
	openbaoToken := strings.TrimSpace(os.Getenv("CATALYST_OPENBAO_TOKEN"))
	openbaoMount := envOr("CATALYST_OPENBAO_KV_MOUNT", "kv")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "organization-controller.orgs.openova.io",
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

	giteaClient := gitea.New(giteaURL, giteaToken)

	// IaC bootstrap deps: reuse the same Gitea client (it already
	// surfaces the admin-user / collaborator / branch-protection
	// methods via core/controllers/pkg/gitea/admin_users.go) plus an
	// OpenBao-backed token store when wired.
	var (
		iacGitea  iacbootstrap.GiteaClient
		iacTokens iacbootstrap.TokenStore
	)
	if openbaoAddr != "" && openbaoToken != "" {
		iacGitea = giteaClient
		iacTokens = iacbootstrap.NewOpenBaoStore(iacbootstrap.OpenBaoConfig{
			Addr:      openbaoAddr,
			Token:     openbaoToken,
			MountPath: openbaoMount,
		})
		log.Info("iac-bootstrap: OpenBao seam wired",
			"addr", openbaoAddr, "kv_mount", openbaoMount)
	} else {
		log.Info("iac-bootstrap: disabled (CATALYST_OPENBAO_ADDR/TOKEN unset)")
	}

	r := &controller.Reconciler{
		Client:                            mgr.GetClient(),
		Log:                               log.WithName("reconciler"),
		Keycloak:                          controller.NewLiveKeycloak(kcAddr, kcRealm, kcSAID, kcSASecret),
		PerOrgRealmEnabled:                perOrgRealmEnabled,
		GiteaClient:                       giteaClient,
		HostCluster:                       hostCluster,
		EnvRegionProvider:                 envRegionProvider,
		EnvRegionCode:                     envRegionCode,
		EnvRegionBuildingBlock:            envRegionBuildingBlock,
		VClusterChartVersion:              chartVer,
		VClusterHelmRepoName:              helmRepoName,
		VClusterHelmRepoNamespace:         helmRepoNs,
		VClusterImageRegistry:             vclusterImageRegistry,
		Branch:                            branch,
		GiteaInClusterURL:                 giteaInClusterURL,
		FluxNamespace:                     fluxNamespace,
		FluxIntervalSeconds:               fluxIntervalSeconds,
		FluxGiteaSecretRef:                fluxGiteaSecretRef,
		FederationSecretNamespace:         fedSecretNs,
		UserAccessNamespace:               uaNs,
		IacBootstrapGitea:                 iacGitea,
		IacBootstrapTokens:                iacTokens,
		ConsoleGatewayName:                consoleGatewayName,
		ConsoleGatewayNamespace:           consoleGatewayNs,
		ConsoleTLSClusterIssuer:           consoleTLSIssuer,
		ConsoleTLSCertNamespace:           consoleTLSCertNs,
		ConsoleRouteNamespace:             consoleRouteNs,
		ConsoleAPIService:                 consoleAPISvc,
		ConsoleUIService:                  consoleUISvc,
		ConsoleAPIPort:                    consoleAPIPort,
		ConsoleUIPort:                     consoleUIPort,
		PoolPowerDNSURL:                   poolPowerDNSURL,
		PoolPowerDNSAPIKey:                poolPowerDNSAPIKey,
		PoolPowerDNSSecretName:            poolPowerDNSSecretName,
		PoolPowerDNSSecretNamespace:       poolPowerDNSSecretNS,
		PoolPowerDNSSourceSecretName:      poolPowerDNSSourceSecretName,
		PoolPowerDNSSourceSecretNamespace: poolPowerDNSSourceSecretNS,
		TenantConsoleLBIPv4:               tenantConsoleLBIPv4,
		TenantPrimaryLBIPv4:               tenantPrimaryLBIPv4,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "setup reconciler")
		os.Exit(1)
	}

	// #4236 — surface whether the per-Org pool-DNS writer is wired so an
	// operator can tell at a glance whether fresh-signup console hosts will get
	// their central-pdns A-record (the #4179 close gate). Logged once at startup.
	switch {
	case poolPowerDNSURL != "" && poolPowerDNSAPIKey != "":
		log.Info("tenant-dns: central pool-PowerDNS writer wired (console.<slug>.<pool> A-records target central pdns)",
			"pool_url", poolPowerDNSURL,
			"console_lb_ipv4", tenantConsoleLBIPv4,
			"primary_lb_ipv4_fallback", tenantPrimaryLBIPv4)
	case poolPowerDNSURL != "":
		// #4290/#4179 — URL is set so this Sovereign DOES expect a central pool
		// write, but the env key is empty (the secretKeyRef bound before
		// bp-reflector filled the bridged Secret). This is NO LONGER a dead end:
		// reconcileTenantDNS reads the key LIVE from the Secret each pass and
		// self-heals the moment reflection lands. Log the live-read fallback so an
		// operator knows the writer will activate without a Pod roll.
		log.Info("tenant-dns: central pool-PowerDNS URL wired but env key empty — will resolve key via LIVE Secret read each reconcile (self-heals when reflector lands it; #4290/#4179)",
			"pool_url", poolPowerDNSURL,
			"secret", poolPowerDNSSecretName,
			"secret_namespace", poolPowerDNSSecretNS,
			"console_lb_ipv4", tenantConsoleLBIPv4,
			"primary_lb_ipv4_fallback", tenantPrimaryLBIPv4)
	default:
		log.Info("tenant-dns: central pool-PowerDNS writer NOT wired — per-Org pool A-records will not be written (single-pdns / Catalyst-Zero)",
			"have_url", false, "have_key", poolPowerDNSAPIKey != "")
	}

	// D35 consume-leg — subscribe to the two canonical Catalyst NATS
	// subjects so a `tenant.created` / `order.placed` envelope nudges
	// the matching Organization CR into a fresh Reconcile within ~50ms
	// of the publish. Best-effort wiring: when NATS_URL is unset (e.g.
	// Catalyst-Zero contabo path where NATS is not deployed) we log
	// "NATS not wired" and continue — the existing 30s informer
	// requeue fallback inside r.Reconcile keeps the controller correct.
	natsURL := strings.TrimSpace(os.Getenv("NATS_URL"))
	if natsURL != "" {
		// #5364 — publish leg: wire a NATS publisher so the reconciler emits the
		// canonical `tenant.deleted` event on Org-CR finalizer teardown. That
		// event drives the provisioning service's tenant.deleted consumer to run
		// the SECOND half of Org teardown (prune the `org-tenants` gitops dir: the
		// <slug> ns manifest + tenant HelmReleases). Without it a raw
		// `kubectl delete organization` runs only the org-controller half of
		// teardown and the org-tenants Kustomization recreates the ns + HRs
		// forever. Best-effort: a connect failure logs + leaves the publisher nil
		// (the reconciler's publishTenantDeleted then degrades to a no-op), never
		// fatal — the informer/finalizer path stays correct either way.
		if pub, err := natsbus.NewPublisher(natsURL); err != nil {
			log.Error(err, "natsbus: publisher connect failed — tenant.deleted emit-on-finalizer disabled (#5364); Org deletes still run the org-controller-half teardown",
				"nats_url", natsURL)
		} else {
			r.TenantEventPublisher = pub
			defer pub.Close()
			log.Info("natsbus: tenant.deleted publish-leg wired (#5364)",
				"nats_url", natsURL, "subject", natsbus.SubjectTenantDeleted)
		}

		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			sub, err := natsbus.Connect(natsURL)
			if err != nil {
				log.Error(err, "natsbus: connect failed — D35 consume-leg disabled",
					"nats_url", natsURL)
				return nil // non-fatal — informer requeue is the canonical fallback
			}
			bridge := &controller.NATSBridge{
				Client: mgr.GetClient(),
				Log:    log.WithName("natsbridge"),
			}
			if err := sub.Subscribe(ctx,
				natsbus.SubjectTenantCreated,
				"organization-controller-tenant-created",
				bridge.HandleTenantCreated,
				natsbus.SubscribeOptions{},
			); err != nil {
				log.Error(err, "natsbus: subscribe tenant.created failed")
			}
			if err := sub.Subscribe(ctx,
				natsbus.SubjectOrderPlaced,
				"organization-controller-order-placed",
				bridge.HandleOrderPlaced,
				natsbus.SubscribeOptions{},
			); err != nil {
				log.Error(err, "natsbus: subscribe order.placed failed")
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
			"subjects", []string{natsbus.SubjectTenantCreated, natsbus.SubjectOrderPlaced},
		)
	} else {
		log.Info("natsbus: NATS_URL unset — D35 consume-leg disabled (informer-requeue fallback only)")
	}

	log.Info("starting manager",
		"host_cluster", hostCluster,
		"keycloak_addr", kcAddr,
		"keycloak_realm", kcRealm,
		"gitea_url", giteaURL,
		"vcluster_chart_version", chartVer,
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

// envIntOr reads a positive-integer env var, falling back to `fallback`
// when unset, empty, or unparseable / non-positive (a 0 or negative
// interval would make Flux reject the CR). Per Inviolable Principle #4 the
// knob is operator-overridable but defended against a fat-fingered value.
func envIntOr(key string, fallback int) int {
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

// envBoolDefaultFalse reads a boolean env var that defaults to FALSE when
// unset/empty (opt-in). Only the explicit strings "true"/"1"/"yes"/"on"
// (case-insensitive) enable it — every other value (including unset)
// is false. Modeled to avoid the Sprig-style false-is-empty trap
// (memory feedback_sprig_default_bool_unsafe.md): we branch on the
// enable-tokens, NOT on emptiness, so a missing var correctly stays off.
func envBoolDefaultFalse(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}
