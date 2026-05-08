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

	"github.com/openova-io/openova/core/controllers/organization/internal/controller"
	"github.com/openova-io/openova/core/controllers/organization/internal/gitea"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
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

	hostCluster := mustEnv("CATALYST_HOST_CLUSTER", log)
	chartVer := envOr("CATALYST_VCLUSTER_CHART_VERSION", "0.33.*")
	helmRepoName := envOr("CATALYST_VCLUSTER_HELMREPO_NAME", "loft")
	helmRepoNs := envOr("CATALYST_VCLUSTER_HELMREPO_NAMESPACE", "vcluster-system")
	branch := envOr("CATALYST_GITEA_BRANCH", "main")

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

	r := &controller.Reconciler{
		Client:                    mgr.GetClient(),
		Log:                       log.WithName("reconciler"),
		Keycloak:                  controller.NewLiveKeycloak(kcAddr, kcRealm, kcSAID, kcSASecret),
		GiteaClient:               gitea.New(giteaURL, giteaToken),
		HostCluster:               hostCluster,
		VClusterChartVersion:      chartVer,
		VClusterHelmRepoName:      helmRepoName,
		VClusterHelmRepoNamespace: helmRepoNs,
		Branch:                    branch,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "setup reconciler")
		os.Exit(1)
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
