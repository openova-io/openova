// Command continuum-controller — ADR-0015 D8.
//
// Continuum is reduced to three things, and runs in EVERY region:
//
//   - the cross-region lease (a file in Git, compare-and-swapped through the
//     writer Gitea — see internal/transition/lease.go for why Git);
//   - region-loss detection (a peer region whose observations stop answering);
//   - committing the next phase of a topology transition to the per-region
//     Application files (ADR-0015 D1), waiting between phases on the native
//     status of CNPG, Flux and the Application (internal/transition).
//
// It serves its own region's native observations read-only on
// OBSERVATIONS_ADDR (GET /v1/observations) for the other regions, behind the
// region-addressable ClusterMesh Service continuum-<region-id>.
//
// Configuration (environment; nothing hardcoded):
//
//	SOVEREIGN_REGION_ID     this region's canonical id (required)
//	SOVEREIGN_REGIONS_JSON  the Sovereign's region registry (pkg/regions)
//	GITEA_API_URL           the WRITER region's Gitea (gitea-<writer-id> over
//	                        ClusterMesh in a follower region)
//	GITEA_TOKEN             token with write access to the Org repos
//	OBSERVATIONS_ADDR       listen address for /v1/observations (default :8082)
//	PEER_URL_TEMPLATE       peer observation URL, "{region}" substituted
//	                        (default http://continuum-{region}.<ns>.svc.cluster.local:8082)
//	TICK_SECONDS            evaluation interval (default 10)
//	LEASE_TTL_SECONDS       Git lease TTL (default 60)
//	REBOOT_HOLD             "true" (default): hold this region's reboot agent
//	                        (kured) while a transition of the region is in
//	                        progress, and wait for its drains; "false": neither
//	REBOOT_AGENT_NAMESPACE  namespace of kured's DaemonSet (default kube-system)
//	REBOOT_AGENT_DAEMONSET  its name (default kured)
//	REBOOT_HOLD_TTL_SECONDS TTL of the hold: how long it outlives a controller
//	                        that stopped renewing it (default 300)
//	REBOOT_WAIT_SECONDS     bound of a move's wait for a region's reboot agent,
//	                        and of a Failed move's hold of it (default 1200)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/core/controllers/continuum/internal/controller"
	"github.com/openova-io/openova/core/controllers/continuum/internal/observe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/reboot"
	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envSeconds(k string, def time.Duration) time.Duration {
	if v, err := strconv.Atoi(env(k, "")); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return def
}

func podNamespace() string {
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "catalyst-system"
}

// observationServer serves /v1/observations on every replica (not only the
// leader): a peer's read must not depend on which pod holds the in-region
// leader lock.
type observationServer struct {
	addr string
	h    http.Handler
}

func (s observationServer) NeedLeaderElection() bool { return false }

func (s observationServer) Start(ctx context.Context) error {
	srv := &http.Server{Addr: s.addr, Handler: s.h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func main() {
	var metricsAddr, probeAddr string
	var leaderElect bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", env("METRICS_ADDR", ":8080"), "metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", env("HEALTH_ADDR", ":8081"), "health probe address")
	flag.BoolVar(&leaderElect, "leader-elect", env("LEADER_ELECT", "true") == "true", "in-region leader election between replicas")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	region := env("SOVEREIGN_REGION_ID", "")
	if region == "" {
		fmt.Fprintln(os.Stderr, "SOVEREIGN_REGION_ID is required: Continuum acts per region")
		os.Exit(1)
	}
	reg, err := regions.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "region registry: %v\n", err)
		os.Exit(1)
	}
	ns := podNamespace()

	cfg := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "continuum-controller.openova.io",
		LeaderElectionNamespace: env("LEADER_ELECT_NS", ns),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager: %v\n", err)
		os.Exit(1)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dynamic client: %v\n", err)
		os.Exit(1)
	}

	// The reboot agent of this region: a move and a drain take turns.
	var agent *reboot.Agent
	if env("REBOOT_HOLD", "true") == "true" {
		agent = &reboot.Agent{
			Dyn:       dyn,
			Namespace: env("REBOOT_AGENT_NAMESPACE", reboot.DefaultNamespace),
			DaemonSet: env("REBOOT_AGENT_DAEMONSET", reboot.DefaultDaemonSet),
			HolderID:  "continuum-" + region,
			TTL:       envSeconds("REBOOT_HOLD_TTL_SECONDS", reboot.DefaultTTL),
		}
	}
	obs := &observe.Observer{Dyn: dyn, Region: region}
	if agent != nil {
		obs.Reboot = agent
	}
	tmpl := env("PEER_URL_TEMPLATE", "http://continuum-{region}."+ns+".svc.cluster.local:8082")
	peers := observe.Peers{
		HTTP:   &http.Client{Timeout: 5 * time.Second},
		URLFor: func(r string) string { return strings.ReplaceAll(tmpl, "{region}", r) },
	}
	c := &controller.Controller{
		Dyn:      dyn,
		Store:    controller.GiteaStore{Client: gitea.New(env("GITEA_API_URL", "http://gitea-http.gitea.svc.cluster.local:3000"), os.Getenv("GITEA_TOKEN"))},
		Local:    obs,
		Peers:    peers,
		Region:   region,
		Regions:  reg.IDs(),
		Tick:     envSeconds("TICK_SECONDS", controller.DefaultTick),
		LeaseTTL: envSeconds("LEASE_TTL_SECONDS", controller.DefaultLeaseTTL),
		Log:      log,

		RebootWait: envSeconds("REBOOT_WAIT_SECONDS", transition.DefaultRebootWait),
	}
	if agent != nil {
		c.Reboot = agent
	}
	if err := mgr.Add(manager.RunnableFunc(c.Start)); err != nil {
		fmt.Fprintf(os.Stderr, "add controller: %v\n", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/observations", observe.Handler(obs))
	mux.Handle("/v1/clusters", observe.ClusterHandler(obs))
	if err := mgr.Add(observationServer{addr: env("OBSERVATIONS_ADDR", ":8082"), h: mux}); err != nil {
		fmt.Fprintf(os.Stderr, "add observation server: %v\n", err)
		os.Exit(1)
	}
	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	log.Info("continuum starting", "region", region, "regions", reg.IDs(), "peerURL", tmpl)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "manager: %v\n", err)
		os.Exit(1)
	}
}
