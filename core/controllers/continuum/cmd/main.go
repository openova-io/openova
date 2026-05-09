// Command continuum-controller — slices K-Cont-1 + K-Cont-2 of
// EPIC-6 (#1101).
//
// Watches `Continuum.dr.openova.io/v1` CRs (and, via the per-CR
// goroutine, the referenced `active-hotstandby` Application CRs +
// CNPG cluster-pair Cluster CRs) and orchestrates per-Application
// DR — lease maintenance, replication-health watching, 7-step
// switchover sequence per docs/SRE.md §2 + docs/MULTI-REGION-DNS.md.
//
// K-Cont-1 shipped the binary + chart + CI workflow + skeleton.
// K-Cont-2 (this slice) replaces the Reconcile body with the full
// per-CR goroutine + lease state machine + switchover sequencer +
// NATS audit publisher. K-Cont-3 wires the real lease witness
// implementations (cloudflare-kv + dns-quorum); K-Cont-4 ships the
// Cloudflare Worker source.
//
// Configuration is environment-only — per
// docs/INVIOLABLE-PRINCIPLES.md #4 nothing is hardcoded:
//
//	METRICS_ADDR        — :8080 default; metrics endpoint
//	HEALTH_ADDR         — :8081 default; /healthz + /readyz
//	LEADER_ELECT        — "true" | "false" (default true in-cluster)
//	LEADER_ELECT_NS     — namespace for the leader-election Lease
//	                       (default: in-cluster pod namespace; falls
//	                       back to "openova-system" if unreadable)
//	LOG_LEVEL           — debug | info | warn | error (default info)
//
// K-Cont-2 wires these additional env vars:
//
//	PDM_API_URL         — pool-domain-manager URL (POSTs lua-records to /v1/lua/commit)
//	PDM_AUTH_TOKEN      — optional X-Catalyst-Token header value
//	NATS_URL            — for catalyst.audit publishing (e.g. nats://nats.openova-system.svc.cluster.local:4222)
//	CATALYST_REGION     — host-cluster name THIS controller represents (stamped on Witness.Acquire)
//	WITNESS_IN_MEMORY   — "true" enables the in-memory witness selector (TEST ONLY; default false)
//
// Per-CR config (lease TTL/renew, witness kind, regions) is read
// from Continuum CR spec, NEVER env (per INVIOLABLE-PRINCIPLES #4).
//
// The binary uses controller-runtime's Manager which gives leader
// election (Lease-backed), graceful shutdown on SIGINT/SIGTERM, and a
// shared informer cache for all watched kinds.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/core/controllers/continuum/internal/controller"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pdm"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
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
		LeaderElectionID:        "continuum-controller.openova.io",
		LeaderElectionNamespace: leaderElectNS,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager init: %v\n", err)
		os.Exit(1)
	}

	// Build the dynamic client (Continuum CRs + CNPG Cluster CRs +
	// HTTPRoute access via Unstructured per ADR-0001 §2.7).
	dyn, err := dynamic.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dynamic client: %v\n", err)
		os.Exit(1)
	}

	// Witness selector. K-Cont-2 ships ErrNotImplemented for the
	// real kinds (cloudflare-kv + dns-quorum); K-Cont-3 swaps in
	// the implementations. The in-memory selector is gated by the
	// WITNESS_IN_MEMORY env var — TEST ONLY.
	sel := &witness.DefaultSelector{
		InMemoryAllowed: envBool("WITNESS_IN_MEMORY", false),
	}

	// PDM client — when PDM_API_URL is empty, the PDMCommit closure
	// surfaces a "not configured" error, which keeps the
	// switchover-step-4 path explicit rather than silently no-oping.
	var pdmClient *pdm.Client
	if u := env("PDM_API_URL", ""); u != "" {
		pdmClient = pdm.New(u, env("PDM_AUTH_TOKEN", ""))
	}

	// Audit publisher — JetStreamPublisher when NATS_URL is set,
	// else nil (the controller no-ops audit publishes when nil; this
	// is OK for dev/test but production deploys MUST set NATS_URL).
	var audit events.Publisher
	if u := env("NATS_URL", ""); u != "" {
		jp, err := events.NewJetStreamPublisher(context.Background(), u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: NATS init failed (%v); audit emits will be dropped\n", err)
		} else {
			defer jp.Close()
			audit = jp
		}
	}

	r := &controller.ContinuumReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Dyn:             dyn,
		WitnessSelector: sel,
		HoldingRegion:   env("CATALYST_REGION", ""),
		PDMClient:       pdmClient,
		Audit:           audit,
		Drainer:         switchover.NewDynamicHTTPRouteDrainer(dyn),
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
