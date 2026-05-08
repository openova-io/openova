// Command useraccess-controller — UserAccess CR reconciler.
//
// Runs as a Deployment on every Sovereign cluster (per the slice C5
// brief, deploy manifests in `core/controllers/useraccess/deploy/`).
// Replaces the broken Crossplane Composition path described in
// docs/EPICS-1-6-unified-design.md §3.5 (provider-kubernetes is not
// installed on any production Sovereign — silent P0 bug).
//
// Configuration is environment-only — per
// docs/INVIOLABLE-PRINCIPLES.md #4 nothing is hardcoded:
//
//	METRICS_ADDR        — :8080 default; metrics + readyz endpoint
//	HEALTH_ADDR         — :8081 default; /healthz + /readyz
//	LEADER_ELECT        — "true" | "false" (default true in-cluster)
//	LEADER_ELECT_NS     — namespace for the leader-election Lease
//	                       (default: in-cluster pod namespace; falls
//	                       back to "openova-system" if unreadable)
//	LOG_LEVEL           — debug | info | warn | error (default info)
//
// The binary uses controller-runtime's Manager which gives leader
// election (Lease-backed), graceful shutdown on SIGINT/SIGTERM, and a
// shared informer cache for all watched kinds.
package main

import (
	"flag"
	"fmt"
	"os"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/core/controllers/useraccess/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		leaderElectNS        string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", env("METRICS_ADDR", ":8080"), "Address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", env("HEALTH_ADDR", ":8081"), "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", envBool("LEADER_ELECT", true), "Enable leader election for the controller manager.")
	flag.StringVar(&leaderElectNS, "leader-elect-namespace", env("LEADER_ELECT_NS", podNamespace()), "Namespace for the leader-election Lease.")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "useraccess-controller.openova.io",
		LeaderElectionNamespace: leaderElectNS,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager init: %v\n", err)
		os.Exit(1)
	}

	r := &controller.UserAccessReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "setup reconciler: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintf(os.Stderr, "healthz: %v\n", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintf(os.Stderr, "readyz: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "manager exit: %v\n", err)
		os.Exit(1)
	}
}

// env reads a string env var with a default fallback.
func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envBool reads a bool env var; the empty value or invalid input
// returns the fallback.
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
