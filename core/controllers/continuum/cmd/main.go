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
// K-Cont-2 ships the per-CR goroutine + lease state machine +
// switchover sequencer + NATS audit publisher. K-Cont-3 (this
// build) wires the real lease witness implementations
// (cloudflare-kv + dns-quorum) — the impl packages register their
// Factory at init() time via blank-import below. K-Cont-4 ships the
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
// K-Cont-3 wires this:
//
//	WITNESS_SECRET_NS   — K8s namespace where lease-witness Secret refs are
//	                       looked up (CF API token, TSIG key). Default
//	                       "catalyst-controllers" (mirrors EPIC-3 F's
//	                       `FederationSecretNamespace`).
//
// Slice F-2 + F-3 wires these:
//
//	CONTINUUM_API_ADDR  — :8082 default; HTTP server for dry-run + health
//	                       endpoints (slice F-2 + F-3). Empty = disabled.
//	CONTINUUM_API_TOKEN — optional bearer token gating dry-run/health
//	                       endpoints. Empty = relies solely on the
//	                       X-Catalyst-Owner-Tier header (catalyst-api
//	                       stamps it after JWT validation).
//	CONTINUUM_HEALTH_DELAY_SECONDS — post-switchover health-check delay.
//	                       Default 30s per design doc §9.5.
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
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/continuum/internal/api"
	"github.com/openova-io/openova/core/controllers/continuum/internal/controller"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pdm"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pgprobe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
	// K-Cont-3 lease-witness implementations — register their
	// Factory at init() time so DefaultSelector dispatches them. The
	// controller does NOT need typed access to either package; the
	// blank-import is the WIRING — removing it disables the kinds.
	_ "github.com/openova-io/openova/core/controllers/continuum/internal/witness/cloudflarekv"
	_ "github.com/openova-io/openova/core/controllers/continuum/internal/witness/dnsquorum"
	// #3829 — k8s-lease witness: a native coordination.k8s.io/v1 Lease in
	// the control-plane cluster. The air-gappable default (no Cloudflare,
	// no PowerDNS-TXT dependency) — cloudflare-kv needs external egress +
	// a Worker, and dns-quorum was a never-completed Phase-1 POC (nil
	// TXTWriter → Acquire always fails). The blank-import registers its
	// Factory; the controller stamps its dynamic client into the witness
	// config (selectWitness) so the package needs no client-go ctor.
	_ "github.com/openova-io/openova/core/controllers/continuum/internal/witness/k8slease"
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

	// Witness selector. K-Cont-3 wires the cloudflare-kv +
	// dns-quorum impls via blank-import (see top-of-file imports).
	// The in-memory selector remains gated by the WITNESS_IN_MEMORY
	// env var — TEST ONLY. The SecretReader resolves CF API tokens
	// + TSIG keys from K8s Secrets in WITNESS_SECRET_NS (default
	// "catalyst-controllers", same as EPIC-3 F's federation
	// secret namespace).
	witnessSecretNS := env("WITNESS_SECRET_NS", "catalyst-controllers")
	sel := &witness.DefaultSelector{
		InMemoryAllowed: envBool("WITNESS_IN_MEMORY", false),
		SecretReader: &k8sSecretReader{
			Client:    mgr.GetClient(),
			Namespace: witnessSecretNS,
		},
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

	// Slice F-3: post-switchover health-check delay (default 30s
	// per design doc §9.5; 0 = run immediately, useful for tests).
	healthDelay := 30 * time.Second
	if v := env("CONTINUUM_HEALTH_DELAY_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			healthDelay = time.Duration(n) * time.Second
		}
	}

	// #3492 — raft-transition promotion backend (bp-openbao). OpenBao OSS
	// peers.json recovery: the RaftExecPromoter (optionally) execs `bao
	// operator raft snapshot restore`, then writes a single-voter peers.json
	// into the surviving openbao standby's raft data dir via SPDY exec, then
	// RESTARTS the Pod (Restarter — same spdyPodExecutor backend, which holds
	// pods/delete) so openbao re-reads peers.json on boot and self-elects as
	// the sole leader. (transition-to-primary does NOT exist in OSS — openbao
	// PR #996.) The PodLister resolves a podSelector to a Ready Pod. cnpg-pair
	// CRs never touch this path. A nil executor (clientset init failure)
	// leaves raft-transition CRs failing at step-2 with a clear error rather
	// than crashing the controller — cnpg-pair DR is unaffected.
	var raftPromoter switchover.Promoter
	if execBackend, err := newSPDYPodExecutor(ctrl.GetConfigOrDie()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: raft-transition exec backend init failed (%v); raft-transition switchovers will error until fixed (cnpg-pair unaffected)\n", err)
	} else {
		raftPromoter = &switchover.RaftExecPromoter{
			Exec:      execBackend,
			Restarter: execBackend,
			Lister:    newDynamicPodLister(dyn),
		}
	}

	// #3829 — namespace where the k8s-lease witness stores its
	// coordination.k8s.io/v1 Lease objects. CATALYST_LEASE_NS wins;
	// otherwise the controller's own namespace (the leases live next to
	// the controller, which already has the coordination.k8s.io RBAC).
	leaseNamespace := env("CATALYST_LEASE_NS", podNamespace())

	r := &controller.ContinuumReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Dyn:             dyn,
		WitnessSelector: sel,
		HoldingRegion:   env("CATALYST_REGION", ""),
		LeaseNamespace:  leaseNamespace,
		PDMClient:       pdmClient,
		Audit:           audit,
		Drainer:         switchover.NewDynamicHTTPRouteDrainer(dyn),
		RaftPromoter:    raftPromoter,
		HealthDelay:     healthDelay,
		// #5311 — standby observation source that works on a TRUE
		// 2-region Sovereign: reads the acting primary's
		// pg_stat_replication (the region-b replica Cluster CR is not in
		// this controller's local API). Authenticates with the
		// CNPG-provisioned streaming_replica client cert
		// (`<primary>-replication` Secret) — the same credential the
		// dr-promoter / dr-failback probes use. The controller already
		// holds `secrets: get` cluster-wide (see the chart RBAC), and
		// the target is the LOCAL primary `-rw` Service. Reachable from
		// the single mgr client since ClusterDomain defaults to
		// cluster.local; CLUSTER_DOMAIN overrides for customized
		// kubelet cluster domains.
		StandbyProbe: &pgprobe.PGXProber{
			Secrets:       &pgSecretReader{Client: mgr.GetClient()},
			ClusterDomain: env("CLUSTER_DOMAIN", pgprobe.DefaultClusterDomain),
		},
		HealthOpts: switchover.HealthOptions{
			// Production: 8.8.8.8 / 1.1.1.1 / 9.9.9.9 multi-vantage
			// DNS resolver fanout. Tests that don't want live DNS
			// override via the per-CR Sequencer.
			Resolvers: switchover.DefaultMultiResolverDial,
			// AuditTail is left nil for now — wiring NATS PullConsumer
			// is a separate slice (the dry-run + replicas + DNS checks
			// are sufficient for the F-3 acceptance gate; audit-posted
			// is recorded as deferred until a follow-up slice wires the
			// JetStream consumer).
		},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "setup reconciler: %v\n", err)
		os.Exit(1)
	}

	// Slice F-2 + F-3: HTTP server for dry-run + health endpoints.
	// Empty CONTINUUM_API_ADDR disables the server (e.g. for binaries
	// that ship without the admin surface). Default :8082.
	if apiAddr := env("CONTINUUM_API_ADDR", ":8082"); apiAddr != "" {
		apiSrv := &api.Server{
			Provider:    r,
			HealthCache: api.NewInMemoryHealthCache(),
			HealthOpts:  r.HealthOpts,
			AuthToken:   env("CONTINUUM_API_TOKEN", ""),
			Audit:       audit,
		}
		go func() {
			httpSrv := &http.Server{
				Addr:              apiAddr,
				Handler:           apiSrv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			fmt.Fprintf(os.Stderr, "continuum API server listening on %s\n", apiAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "continuum API server: %v\n", err)
			}
		}()
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

// k8sSecretReader implements witness.SecretReader by GETting the
// referenced K8s Secret via the controller-runtime client.
//
// Per Inviolable Principle #5 the value bytes stay in memory only
// until the calling Factory hands them to the wire client (CF bearer
// token / TSIG key). NEVER log; NEVER persist outside the Secret.
//
// Mirrors the EPIC-3 F readClientSecret seam — but lifted to an
// interface so the witness package doesn't import client-go.
type k8sSecretReader struct {
	Client    client.Client
	Namespace string
}

// ReadSecret implements witness.SecretReader.
func (r *k8sSecretReader) ReadSecret(ctx context.Context, secretName, key string) ([]byte, error) {
	if secretName == "" {
		return nil, fmt.Errorf("witness/k8sSecretReader: secret name is empty")
	}
	if key == "" {
		return nil, fmt.Errorf("witness/k8sSecretReader: secret key is empty")
	}
	ns := r.Namespace
	if ns == "" {
		ns = "catalyst-controllers"
	}
	var sec corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: secretName}, &sec); err != nil {
		return nil, fmt.Errorf("witness/k8sSecretReader: get Secret %s/%s: %w", ns, secretName, err)
	}
	val, ok := sec.Data[key]
	if !ok {
		return nil, fmt.Errorf("witness/k8sSecretReader: Secret %s/%s missing key %q", ns, secretName, key)
	}
	if len(val) == 0 {
		return nil, fmt.Errorf("witness/k8sSecretReader: Secret %s/%s key %q is empty", ns, secretName, key)
	}
	return val, nil
}

// pgSecretReader implements pgprobe.SecretReader (#5311) by GETting the
// referenced Secret key via the controller-runtime client. Unlike
// k8sSecretReader (fixed namespace for lease-witness secrets), the
// cnpg-pair namespace is passed per call — the streaming_replica client
// cert lives in the Continuum's spec.cnpgPair.namespace. The controller
// holds cluster-wide `secrets: get`, so any cnpg namespace resolves.
//
// Per Inviolable Principle #5 the cert bytes stay in memory only until
// PGXProber hands them to the TLS dialer. NEVER log; NEVER persist.
type pgSecretReader struct {
	Client client.Client
}

// ReadSecret implements pgprobe.SecretReader.
func (r *pgSecretReader) ReadSecret(ctx context.Context, namespace, name, key string) ([]byte, error) {
	if namespace == "" || name == "" || key == "" {
		return nil, fmt.Errorf("pgSecretReader: namespace/name/key required (ns=%q name=%q key=%q)", namespace, name, key)
	}
	var sec corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return nil, fmt.Errorf("pgSecretReader: get Secret %s/%s: %w", namespace, name, err)
	}
	val, ok := sec.Data[key]
	if !ok {
		return nil, fmt.Errorf("pgSecretReader: Secret %s/%s missing key %q", namespace, name, key)
	}
	if len(val) == 0 {
		return nil, fmt.Errorf("pgSecretReader: Secret %s/%s key %q is empty", namespace, name, key)
	}
	return val, nil
}
