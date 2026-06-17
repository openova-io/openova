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
		Client:                    mgr.GetClient(),
		Log:                       log.WithName("reconciler"),
		Keycloak:                  controller.NewLiveKeycloak(kcAddr, kcRealm, kcSAID, kcSASecret),
		PerOrgRealmEnabled:        perOrgRealmEnabled,
		GiteaClient:               giteaClient,
		HostCluster:               hostCluster,
		VClusterChartVersion:      chartVer,
		VClusterHelmRepoName:      helmRepoName,
		VClusterHelmRepoNamespace: helmRepoNs,
		VClusterImageRegistry:     vclusterImageRegistry,
		Branch:                    branch,
		GiteaInClusterURL:         giteaInClusterURL,
		FluxNamespace:             fluxNamespace,
		FluxIntervalSeconds:       fluxIntervalSeconds,
		FluxGiteaSecretRef:        fluxGiteaSecretRef,
		FederationSecretNamespace: fedSecretNs,
		UserAccessNamespace:       uaNs,
		IacBootstrapGitea:         iacGitea,
		IacBootstrapTokens:        iacTokens,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "setup reconciler")
		os.Exit(1)
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
