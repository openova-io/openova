// Package handler — Cilium ClusterMesh auto-establishment after Phase-1.
//
// Why this exists:
//
//   docs/SOVEREIGN-MULTI-REGION-DOD.md gates D9-D12 require Cilium
//   ClusterMesh to be live and peered across every region of a freshly
//   provisioned Sovereign. The upstream operator runbook at
//   platform/cilium/chart/values-clustermesh.yaml documents the manual
//   steps:
//
//     cilium clustermesh enable  --context <each peer>
//     cilium clustermesh connect --context <A> --destination-context <B>
//
//   Those CLI calls are a zero-touch violation per the founder ruling
//   2026-05-15: every visible feature MUST converge without operator
//   intervention. The catalyst-api Pod doesn't ship the `cilium` CLI
//   anyway, so a shell-out wouldn't even work — we have to talk to each
//   region's API server directly via the K8s Go client and write the
//   per-peer trust material into the same Secrets the upstream chart
//   reads on reload.
//
// What this writes (per the upstream Cilium chart's contract):
//
//   In every region's kube-system namespace, we ensure a Secret named
//   `cilium-clustermesh` exists with one entry per peer. The entry key
//   is the peer's `cluster.name` and the value is an etcd client config
//   blob shaped like the upstream chart expects:
//
//     endpoints:
//       - https://<peer-LB-IP>:2379
//     trusted-ca-file: /var/lib/cilium/clustermesh/<peer-name>-ca.crt
//     cert-file:       /var/lib/cilium/clustermesh/<peer-name>.crt
//     key-file:        /var/lib/cilium/clustermesh/<peer-name>.key
//
//   The chart auto-mounts the Secret's keys into
//   /var/lib/cilium/clustermesh on each Cilium DaemonSet Pod, so the
//   peer's CA / cert / key files appear at exactly those paths. We add
//   per-peer CA + cert + key entries with suffixes (`-ca.crt`, `.crt`,
//   `.key`) keyed by peer name. The chart's reload-watch on the
//   `cilium-clustermesh` Secret picks up new entries automatically, and
//   we follow up with a rollout-restart on both `cilium-operator` and
//   `cilium` to guarantee pickup even on chart versions that don't have
//   inotify watch.
//
// Architecture invariants this respects:
//
//   A2 — Inter-region link = Cilium WireGuard over PUBLIC IPs ALWAYS.
//        We read the LoadBalancer IP of each region's
//        clustermesh-apiserver Service. The Service type must be
//        LoadBalancer (per the chart overlay); we never look up
//        NodePort.
//   A3 — clustermesh-apiserver Service = LoadBalancer always. The word
//        `NodePort` does not appear in this file.
//   A6 — Provider-agnostic. We pull region kubeconfigs from disk and
//        talk to whatever API server they describe. No Hetzner-specific
//        API calls.
//
// Idempotency:
//
//   Every write is a Get-then-Update with apierrors.IsAlreadyExists /
//   IsNotFound handling. Re-running on a partially-meshed Sovereign
//   converges to fully meshed. The function never deletes peer entries
//   (a removed region would need an explicit wipe — out of scope).
//
// Failure modes:
//
//   - region kubeconfig missing → log warning + record peer as
//     Connected=false in the status, function returns nil (caller logs
//     + continues, post-handover finalisation can re-run).
//   - LoadBalancer IP absent for up to 5 min → status Peer.Error set,
//     Connected=false, no error returned.
//   - cilium-ca Secret missing → status Peer.Error set, Connected=false.
//   - rollout-restart fails → log warning, status records but does not
//     fail the function (the chart's watch may still pick up the new
//     bytes on the next reload tick).

package handler

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ClusterMeshStatus is the per-region terminal summary returned to the
// caller. One entry per region; PeerStatus describes whether the
// outbound trust to each OTHER region was successfully wired.
type ClusterMeshStatus struct {
	RegionKey      string       `json:"regionKey"`
	ClusterName    string       `json:"clusterName"`
	ClusterID      int          `json:"clusterID"`
	LoadBalancerIP string       `json:"loadBalancerIP"`
	Peers          []PeerStatus `json:"peers"`
	ReadyAt        time.Time    `json:"readyAt,omitempty"`
}

// PeerStatus describes the trust wiring outbound from one region to
// another. Connected=true means the cross-cluster Secret entries were
// written successfully; it does NOT mean Cilium has finished its peer
// handshake (that's observed in the cilium-clustermesh-apiserver Pod
// logs and surfaced via the optional WaitForPeerLogs step).
type PeerStatus struct {
	Name           string `json:"name"`
	LoadBalancerIP string `json:"loadBalancerIP"`
	Connected      bool   `json:"connected"`
	Error          string `json:"error,omitempty"`
}

// regionSlot is a per-region intermediate the orchestrator carries
// through the three steps (LB IP discovery, CA snapshot, peer write).
type regionSlot struct {
	key            string               // e.g. "fsn1-1", "hel1-2", "" for primary
	kubeconfigPath string               // on-disk path to the region's kubeconfig YAML
	clusterName    string               // Cilium cluster.name for this region
	clusterID      int                  // Cilium cluster.id (1..255)
	clientset      kubernetes.Interface // typed client built from kubeconfig
	lbIP           string               // public LB IP of clustermesh-apiserver Service
	caCert         []byte               // cilium-ca tls.crt bytes
	caKey          []byte               // cilium-ca tls.key bytes
	err            error                // non-nil if LB lookup / CA snapshot failed
}

// clusterMeshConstants — module-level tunables. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), these are
// overridable via env vars, but the defaults are sized for the
// canonical Hetzner-LB-on-public-IP path (A2/A3).
const (
	clusterMeshSecretName       = "cilium-clustermesh"
	clusterMeshCASecretName     = "cilium-ca"
	clusterMeshApiserverService = "clustermesh-apiserver"
	clusterMeshNamespace        = "kube-system"
	clusterMeshAPIServerPort    = 2379
	clusterMeshLBLookupTimeout  = 5 * time.Minute
	clusterMeshLBLookupInterval = 10 * time.Second
	clusterMeshCallTimeout      = 30 * time.Second
	clusterMeshPhase            = "clustermesh-progress"
	clusterMeshPeerCertValidity = 365 * 24 * time.Hour
)

// Level-triggered ClusterMesh reconcile defaults (#3241). hw126
// (c986326a77d391d4) proved the one-shot fan-out loses the race
// against LB-IPAM: the primary's clustermesh-apiserver LB IP landed
// seconds AFTER the single 3-second fan-out gave up, and nothing ever
// re-ran the establish (next trigger = handover), so a healthy cluster
// stayed partially meshed forever — and the #3236 cnpg-pair flip
// correctly refused forever with it. The runAutoEstablishClusterMesh
// wrapper therefore loops until fully meshed: exponential backoff from
// the initial value, capped at the max, bounded by the total budget.
// The Handler fields clusterMeshRetry* override these for tests; zero
// falls back to the defaults below.
const (
	clusterMeshRetryInitialBackoffDefault = 1 * time.Minute
	clusterMeshRetryMaxBackoffDefault     = 5 * time.Minute
	clusterMeshRetryBudgetDefault         = 6 * time.Hour
	// Per-attempt bound: the orchestrator's per-region LB lookup is
	// the longest internal wait (5 min). Three regions x 5 min worst
	// case is ~15 min; pad to 20 min for the per-peer cert mint +
	// Patch stack.
	clusterMeshAttemptTimeoutDefault = 20 * time.Minute
)

// ClusterMesh-gated cnpg-pair enable (#3236) — names of the Flux
// objects/keys the post-mesh gate flip touches. The bootstrap-kit
// Kustomization is the one cloud-init applies on every control plane
// (infra/providers/_shared/cloudinit-control-plane.tftpl,
// flux-bootstrap.yaml writeup); its postBuild.substitute map threads
// every Sovereign-shape value into clusters/_template/bootstrap-kit/*,
// including slot 16b's `cnpgPair.enabled:
// ${SOVEREIGN_ENABLE_CNPG_PAIR:-false}` gate.
const (
	bootstrapKitKustomizationName         = "bootstrap-kit"
	fluxSystemNamespace                   = "flux-system"
	clusterMeshCNPGPairSubstituteKey      = "SOVEREIGN_ENABLE_CNPG_PAIR"
	clusterMeshPrimaryRegionSubstituteKey = "SOVEREIGN_PRIMARY_REGION"
	clusterMeshReplicaRegionSubstituteKey = "SOVEREIGN_REPLICA_REGION"
	fluxReconcileRequestedAtAnnotation    = "reconcile.fluxcd.io/requestedAt"
)

// fluxKustomizationGVR — kustomize.toolkit.fluxcd.io/v1 Kustomizations,
// addressed via the dynamic client (no Flux Go types vendored here).
var fluxKustomizationGVR = schema.GroupVersionResource{
	Group:    "kustomize.toolkit.fluxcd.io",
	Version:  "v1",
	Resource: "kustomizations",
}

// clusterMeshDeploymentLabels — the upstream Cilium chart's
// DaemonSet/Deployment names. Used by rollout-restart hints.
var clusterMeshRolloutTargets = []struct {
	kind string
	name string
}{
	{"daemonset", "cilium"},
	{"deployment", "cilium-operator"},
	{"deployment", clusterMeshApiserverService},
}

// clusterMeshTestClientFactory — test-only hook. When non-nil,
// buildRegionSlots routes its kubeconfig → clientset construction
// through this function instead of the production helmwatch parser.
// Tests inject fakes; production leaves this nil.
//
// Returning (nil, false) means "no override for this path, fall
// through to production". Returning (client, true) injects the fake.
var clusterMeshTestClientFactory func(kubeconfigPath string) (kubernetes.Interface, bool)

// clusterMeshTestDynamicClientFactory — test-only hook mirroring
// clusterMeshTestClientFactory for the DYNAMIC client the cnpg-pair
// gate flip (#3236) uses to patch the bootstrap-kit Flux Kustomization.
// Same contract: (nil, false) falls through to production; production
// leaves this nil.
var clusterMeshTestDynamicClientFactory func(kubeconfigPath string) (dynamic.Interface, bool)

// clusterMeshTestOverrideLBTimeout / clusterMeshTestOverrideLBInterval
// — test-only override knobs for the LB-discovery poll loop. Zero
// falls back to the production constants. Tests set these to
// sub-second values so the LB-absent path runs in milliseconds.
var (
	clusterMeshTestOverrideLBTimeout  time.Duration
	clusterMeshTestOverrideLBInterval time.Duration
)

// AutoEstablishClusterMesh wires every region's clustermesh-apiserver
// into a fully-connected peer mesh. Called from deployments.go AFTER
// phase1-watching reports all HRs Ready across all regions.
//
// Idempotent — re-running on a partially-meshed Sovereign converges to
// fully meshed. Caller writes to dep.Events for progress; this function
// returns the final mesh-status summary (per-region peer count +
// readiness).
//
// Returns one ClusterMeshStatus per region. A nil error indicates the
// orchestrator ran to completion (regardless of per-peer outcomes);
// per-peer failures are surfaced via PeerStatus.Error so the caller can
// re-trigger on a follow-up cycle.
//
// Single-region provs (len(dep.Request.Regions) < 2) skip the whole
// orchestrator — there is no peer to mesh with.
func (h *Handler) AutoEstablishClusterMesh(ctx context.Context, dep *Deployment) ([]ClusterMeshStatus, error) {
	if dep == nil {
		return nil, fmt.Errorf("autoEstablishClusterMesh: dep is nil")
	}

	dep.mu.Lock()
	regions := append([]provisioner.RegionSpec(nil), dep.Request.Regions...)
	primaryKubeconfigPath := ""
	if dep.Result != nil {
		primaryKubeconfigPath = dep.Result.KubeconfigPath
	}
	secondaryPaths := make(map[string]string, len(dep.secondaryKubeconfigPaths))
	for k, v := range dep.secondaryKubeconfigPaths {
		secondaryPaths[k] = v
	}
	dep.mu.Unlock()

	if len(regions) < 2 {
		h.log.Info("clustermesh: single-region deployment, skipping mesh establishment",
			"id", dep.ID,
			"regionCount", len(regions),
		)
		return nil, nil
	}

	slots := h.buildRegionSlots(dep, regions, primaryKubeconfigPath, secondaryPaths)
	if len(slots) < 2 {
		h.log.Warn("clustermesh: fewer than 2 reachable regions, skipping",
			"id", dep.ID,
			"reachableRegions", len(slots),
		)
		return nil, nil
	}

	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh auto-establish: starting fan-out across %d regions", len(slots)))

	// Surface slots that failed during buildRegionSlots (kubeconfig
	// path empty / unreadable / client-build failure) BEFORE the
	// fan-out. These slots enter Steps 1-3 with err pre-set and every
	// step `continue`s past them before its first emit — on hw126
	// (#3241) region-A failed exactly here and produced ZERO events
	// (no "LB ready", no failure) while region-B reported "wired 0/1
	// peers"; the operator had no way to tell the region's failure-
	// source from the stream. G91 lesson: a call whose failure has
	// domain meaning must never vanish into a server-side log only —
	// every per-region failure emits a clustermesh-progress warn event
	// carrying the region key + error string.
	for i := range slots {
		s := &slots[i]
		if s.err == nil {
			continue
		}
		h.log.Warn("clustermesh: region slot failed before fan-out",
			"id", dep.ID,
			"region", s.key,
			"cluster", s.clusterName,
			"err", s.err,
		)
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh: region %q (cluster %q) unreachable before fan-out (%v) — peers will be marked disconnected", s.key, s.clusterName, s.err))
	}

	// Step 1: per-region LB IP discovery (poll up to 5 min each).
	for i := range slots {
		s := &slots[i]
		if s.err != nil {
			continue
		}
		lbIP, err := h.waitForClusterMeshLB(ctx, s.clientset)
		if err != nil {
			s.err = fmt.Errorf("lb-discovery: %w", err)
			h.log.Warn("clustermesh: LB lookup failed for region",
				"id", dep.ID,
				"region", s.key,
				"cluster", s.clusterName,
				"err", err,
			)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: region %q LB lookup failed (%v) — peer will be marked disconnected", s.key, err))
			continue
		}
		s.lbIP = lbIP
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: region %q apiserver LB ready at %s", s.key, lbIP))
	}

	// Step 2: per-region cilium-ca snapshot. Without this we can't
	// mint per-peer client certs nor populate the trusted-ca-file
	// entries that other regions need to verify this region.
	for i := range slots {
		s := &slots[i]
		if s.err != nil {
			continue
		}
		cert, key, err := h.snapshotCiliumCA(ctx, s.clientset)
		if err != nil {
			s.err = fmt.Errorf("ca-snapshot: %w", err)
			h.log.Warn("clustermesh: cilium-ca read failed for region",
				"id", dep.ID,
				"region", s.key,
				"cluster", s.clusterName,
				"err", err,
			)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: region %q cilium-ca read failed (%v) — peer will be marked disconnected", s.key, err))
			continue
		}
		s.caCert = cert
		s.caKey = key
	}

	// Step 3: for every ordered pair (A, B), write into A the trust
	// bundle that lets A's clustermesh-apiserver contact B. Builds a
	// cilium-clustermesh Secret entry per peer.
	statuses := make([]ClusterMeshStatus, 0, len(slots))
	for i := range slots {
		a := &slots[i]
		st := ClusterMeshStatus{
			RegionKey:      a.key,
			ClusterName:    a.clusterName,
			ClusterID:      a.clusterID,
			LoadBalancerIP: a.lbIP,
			Peers:          make([]PeerStatus, 0, len(slots)-1),
		}
		if a.err != nil {
			// Region is unreachable — every peer slot fails through.
			for j := range slots {
				if i == j {
					continue
				}
				b := &slots[j]
				st.Peers = append(st.Peers, PeerStatus{
					Name:           b.clusterName,
					LoadBalancerIP: b.lbIP,
					Connected:      false,
					Error:          fmt.Sprintf("local region unreachable: %v", a.err),
				})
			}
			statuses = append(statuses, st)
			// Terminal per-region event for the failed slot — the
			// healthy regions get "wired N/M peers" below; without
			// this emit a failed region ends the attempt with no
			// terminal line at all (the hw126/#3241 silence shape).
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: region %q skipped — region unreachable (%v); wired 0/%d peers", a.key, a.err, len(slots)-1))
			continue
		}

		// Build the per-peer Secret payload for this region (A). Each
		// peer entry contains B's CA + a freshly minted A-as-client cert
		// signed by A's CA. (The cert SAN is the cluster name; the
		// upstream chart's mTLS only verifies cluster identity via SAN.)
		peerEntries := make(map[string][]byte)
		for j := range slots {
			if i == j {
				continue
			}
			b := &slots[j]
			peer := PeerStatus{
				Name:           b.clusterName,
				LoadBalancerIP: b.lbIP,
			}
			if b.err != nil {
				peer.Error = fmt.Sprintf("peer region unreachable: %v", b.err)
				st.Peers = append(st.Peers, peer)
				continue
			}
			if b.lbIP == "" {
				peer.Error = "peer LB IP absent"
				st.Peers = append(st.Peers, peer)
				continue
			}
			if len(b.caCert) == 0 {
				peer.Error = "peer cilium-ca CA cert missing"
				st.Peers = append(st.Peers, peer)
				continue
			}
			if strings.TrimSpace(b.clusterName) == "" {
				// Empty cluster name would become an empty Secret data
				// key and Kubernetes rejects empty keys ("Invalid value:
				// '': a valid config key must consist of alphanumeric
				// characters, '-', '_' or '.'"). Skip this peer pair
				// loudly rather than silently producing a Secret-Create
				// error that kills the whole region's write. Caught on
				// t125 (2026-05-16) — fixed in same PR by deriving
				// clusterName from SovereignFQDN when the operator body
				// omits ClusterMeshName.
				peer.Error = "peer cluster name empty (operator ClusterMeshName unset AND auto-derive failed — check FQDN)"
				st.Peers = append(st.Peers, peer)
				continue
			}
			// Mint A's client cert signed by B's CA (so B's
			// clustermesh-apiserver, which trusts B's cilium-ca as its
			// mTLS root, accepts the handshake when A connects).
			//
			// Caught on t126 (84c0848406dd6fdd, 2026-05-16): the prior
			// code signed with `a.caCert/a.caKey`, putting A's CA at the
			// signing root. B's apiserver (running B's cilium-ca trust
			// pool) rejected the handshake with "unexpected eof while
			// reading" because A's CA was not in B's trust root. Result:
			// Snapshot B's existing `clustermesh-apiserver-remote-cert`
			// Secret and use THOSE bytes as the client cert A presents
			// when connecting to B's etcd. The upstream Cilium chart
			// generates this Secret on every cluster where
			// clustermesh.useAPIServer=true; the cert's CN is `remote`
			// — exactly the etcd RBAC user that has read access on the
			// cilium/* prefix (etcd auth was set up by the chart's
			// post-install Job).
			//
			// Minting a fresh cert (the prior approach across PRs #1525,
			// #1528, #1530) produced a valid TLS handshake but etcd
			// returned `permission denied` because the cert's CN was
			// the LOCAL cluster name, not `remote`. Caught on t129
			// (6cddff7ef4432bdc, 2026-05-16): cilium-dbg status showed
			// `etcd: 1/1 connected` (TLS OK) but
			// `remote configuration: expected=true, retrieved=false`
			// (etcd RBAC blocking the kvstore List call).
			//
			// This matches the canonical `cilium clustermesh connect`
			// CLI behavior — it copies the peer's existing remote-cert
			// rather than minting a new one.
			clientCert, clientKey, err := h.snapshotRemoteCert(ctx, b.clientset)
			if err != nil {
				peer.Error = fmt.Sprintf("peer remote-cert snapshot failed: %v", err)
				st.Peers = append(st.Peers, peer)
				continue
			}
			// Secret keys the upstream chart mounts at
			// /var/lib/cilium/clustermesh/<filename>.
			peerEntries[b.clusterName] = buildPeerConfigBlob(b.clusterName, b.lbIP)
			peerEntries[b.clusterName+"-ca.crt"] = b.caCert
			peerEntries[b.clusterName+".crt"] = clientCert
			peerEntries[b.clusterName+".key"] = clientKey
			peer.Connected = true
			st.Peers = append(st.Peers, peer)
		}

		// Surface empty peerEntries explicitly — silent applyClusterMeshSecret
		// no-op (line 743) used to make every-peer-failed runs invisible.
		// Caught on t124 (2026-05-16): regionKeyFromSpec off-by-one had
		// every slot.err set, so peerEntries stayed empty for all 3
		// regions and the only stdout line was the misleading
		// `fullyMeshed=0`. Log per-peer reasons so the next regression
		// is debuggable from logs alone.
		if len(peerEntries) == 0 {
			reasons := make([]string, 0, len(st.Peers))
			for _, p := range st.Peers {
				reasons = append(reasons, fmt.Sprintf("%s=%q", p.Name, p.Error))
			}
			h.log.Warn("clustermesh: zero peer entries built for region",
				"id", dep.ID,
				"region", a.key,
				"cluster", a.clusterName,
				"reasons", strings.Join(reasons, ", "),
			)
		}

		// Stable order for the Secret update (so an idempotent re-run
		// produces byte-identical Secret data and no rollout-restart
		// thrash).
		if err := h.applyClusterMeshSecret(ctx, a.clientset, peerEntries); err != nil {
			h.log.Warn("clustermesh: Secret apply failed",
				"id", dep.ID,
				"region", a.key,
				"cluster", a.clusterName,
				"err", err,
			)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: region %q Secret write failed (%v) — chart's reload-watch will not see peer config", a.key, err))
			for k := range st.Peers {
				if st.Peers[k].Error == "" {
					st.Peers[k].Connected = false
					st.Peers[k].Error = fmt.Sprintf("secret apply failed: %v", err)
				}
			}
			statuses = append(statuses, st)
			continue
		}

		// Patch cilium DaemonSet's pod spec with hostAliases mapping
		// `<peer>.mesh.cilium.io` -> peer LB IP, so the agent's TLS
		// client connects to a hostname the apiserver-server-cert
		// covers via its `*.mesh.cilium.io` SAN. Without this the
		// handshake fails on hostname verification — agents stay
		// `0/N remote clusters ready` despite valid peer Secrets.
		peers := make([]hostAliasPeer, 0, len(slots)-1)
		for j := range slots {
			if i == j {
				continue
			}
			b := &slots[j]
			if b.err == nil && b.lbIP != "" && b.clusterName != "" {
				peers = append(peers, hostAliasPeer{PeerName: b.clusterName, LBIP: b.lbIP})
			}
		}
		if err := h.patchCiliumHostAliases(ctx, a.clientset, peers); err != nil {
			h.log.Warn("clustermesh: hostAliases patch failed (continuing)",
				"id", dep.ID,
				"region", a.key,
				"err", err,
			)
		}

		// Trigger rollout-restart on cilium + cilium-operator +
		// clustermesh-apiserver in this region so they pick up the
		// new peer entries + hostAliases deterministically. Best-effort:
		// errors are logged, not fatal.
		h.rolloutRestartClusterMeshTargets(ctx, dep, a)

		readyCount := 0
		for _, p := range st.Peers {
			if p.Connected {
				readyCount++
			}
		}
		if readyCount == len(slots)-1 {
			st.ReadyAt = time.Now().UTC()
		}
		statuses = append(statuses, st)

		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: region %q wired %d/%d peers", a.key, readyCount, len(slots)-1))
	}

	// Stable order for the caller — primary first, then secondaries by
	// region key.
	sort.SliceStable(statuses, func(i, j int) bool {
		if statuses[i].RegionKey == "" {
			return true
		}
		if statuses[j].RegionKey == "" {
			return false
		}
		return statuses[i].RegionKey < statuses[j].RegionKey
	})

	totalReady := countFullyMeshedRegions(statuses)
	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh auto-establish: completed (%d/%d regions fully meshed)", totalReady, len(statuses)))

	h.log.Info("clustermesh: orchestrator completed",
		"id", dep.ID,
		"regions", len(statuses),
		"fullyMeshed", totalReady,
	)

	// ── ClusterMesh-gated cnpg-pair enable (#3236) ──────────────────
	// 🛑 ANTI-PATTERN GUARD (hw124/#3196): slot 16b (bp-cnpg-pair)
	// gates on its OWN substitute SOVEREIGN_ENABLE_CNPG_PAIR precisely
	// because flipping on raw 2-region-ness (SOVEREIGN_ENABLE_HOT_
	// STANDBY) rendered a replica that could never stream its
	// basebackup when the mesh wasn't actually wired. The flip below
	// therefore fires ONLY on the FULL-success path — every region's
	// ReadyAt stamped, i.e. every peer pair Connected — never on
	// partial success, never at bootstrap. Idempotent: re-running
	// patches the same key to the same value and merely re-requests a
	// reconcile.
	if totalReady == len(statuses) && len(statuses) >= 2 {
		h.enableCNPGPairAfterFullMesh(ctx, dep, primaryKubeconfigPath)
	} else {
		h.log.Info("clustermesh: mesh not fully established — SOVEREIGN_ENABLE_CNPG_PAIR left untouched (slot 16b stays gated OFF)",
			"id", dep.ID,
			"fullyMeshed", totalReady,
			"regions", len(statuses),
		)
	}

	return statuses, nil
}

// enableCNPGPairAfterFullMesh flips the slot-16b gate on the PRIMARY
// region's bootstrap-kit Flux Kustomization (the one cloud-init applies
// — infra/providers/_shared/cloudinit-control-plane.tftpl) by merging
// `spec.postBuild.substitute.SOVEREIGN_ENABLE_CNPG_PAIR: "true"` and
// stamping `reconcile.fluxcd.io/requestedAt` so Flux reconciles
// immediately instead of waiting out the 5m interval. The cross-region
// CNPG pair (clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml)
// then deploys zero-touch, correctly gated on CONFIRMED mesh readiness.
//
// Defense (non-negotiable, per the chart's required-fail-fast): the
// flip is REFUSED unless the substitute map already carries non-empty,
// DISTINCT SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION —
// cloud-init stamps both on 2-region provs. With enabled=true and a
// missing/equal region the bp-cnpg-pair chart render fails, and a
// render failure inside the bootstrap-kit Kustomization fails the WHOLE
// atomic apply → 0 HRs (the #2981/#2982 fresh-prov wedge shape). A
// refused flip logs loudly and leaves the gate OFF (empty Ready
// release — safe).
//
// Scope: ONLY SOVEREIGN_ENABLE_CNPG_PAIR. The shared-pg flags
// (SOVEREIGN_ENABLE_SHARED_PG / *_PG_OWN_CLUSTER) are #3188 scope and
// are deliberately not touched here. Only the PRIMARY's Kustomization
// is patched — slot 16b is a primary-only HR (SECONDARY_HR_SUSPEND
// suspends it on secondary control planes).
//
// Failure modes follow this file's convention: every failure is logged
// + surfaced as a clustermesh-progress warn event, never an error —
// the post-handover finalisation re-runs AutoEstablishClusterMesh and
// the flip converges then.
func (h *Handler) enableCNPGPairAfterFullMesh(ctx context.Context, dep *Deployment, primaryKubeconfigPath string) {
	if primaryKubeconfigPath == "" {
		// Unreachable from the full-success path (an empty path fails
		// the primary slot, which blocks full success) — but this
		// method must stay safe if ever called from another site.
		h.log.Warn("clustermesh: cnpg-pair gate: primary kubeconfig path empty — cannot reach bootstrap-kit Kustomization",
			"id", dep.ID)
		return
	}
	dyn, err := h.clusterMeshDynamicClient(primaryKubeconfigPath)
	if err != nil {
		h.log.Warn("clustermesh: cnpg-pair gate: dynamic client build failed",
			"id", dep.ID, "err", err)
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh: cnpg-pair enable skipped — dynamic client build failed (%v); re-run converges", err))
		return
	}

	getCtx, cancelGet := context.WithTimeout(ctx, clusterMeshCallTimeout)
	ks, err := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
		Get(getCtx, bootstrapKitKustomizationName, metav1.GetOptions{})
	cancelGet()
	if err != nil {
		h.log.Warn("clustermesh: cnpg-pair gate: Get bootstrap-kit Kustomization failed",
			"id", dep.ID, "err", err)
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh: cnpg-pair enable skipped — Get Kustomization %s/%s failed (%v); re-run converges",
				fluxSystemNamespace, bootstrapKitKustomizationName, err))
		return
	}

	substitute, found, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if err != nil || !found {
		h.log.Warn("clustermesh: refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR — bootstrap-kit Kustomization has no readable spec.postBuild.substitute map",
			"id", dep.ID, "found", found, "err", err)
		h.emitClusterMeshProgress(dep, "warn",
			"ClusterMesh: cnpg-pair enable refused — bootstrap-kit Kustomization carries no postBuild.substitute map")
		return
	}
	primaryRegion := strings.TrimSpace(substitute[clusterMeshPrimaryRegionSubstituteKey])
	replicaRegion := strings.TrimSpace(substitute[clusterMeshReplicaRegionSubstituteKey])
	if primaryRegion == "" || replicaRegion == "" || primaryRegion == replicaRegion {
		h.log.Warn("clustermesh: refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR — substitute region precondition failed (bp-cnpg-pair `required`s distinct non-empty regions; a render failure would fail the whole atomic bootstrap-kit apply → 0 HRs)",
			"id", dep.ID,
			"primaryRegion", primaryRegion,
			"replicaRegion", replicaRegion,
		)
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh: cnpg-pair enable REFUSED — SOVEREIGN_PRIMARY_REGION=%q / SOVEREIGN_REPLICA_REGION=%q must be non-empty and distinct in the bootstrap-kit substitute map",
				primaryRegion, replicaRegion))
		return
	}

	// JSON merge patch (RFC 7386) merges nested objects key-by-key, so
	// sibling substitute keys + existing annotations are preserved —
	// only the gate key and the reconcile-request stamp change.
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				fluxReconcileRequestedAtAnnotation: stamp,
			},
		},
		"spec": map[string]any{
			"postBuild": map[string]any{
				"substitute": map[string]any{
					clusterMeshCNPGPairSubstituteKey: "true",
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		h.log.Warn("clustermesh: cnpg-pair gate: marshal merge patch failed",
			"id", dep.ID, "err", err)
		return
	}
	patchCtx, cancelPatch := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancelPatch()
	if _, err := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
		Patch(patchCtx, bootstrapKitKustomizationName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		h.log.Warn("clustermesh: cnpg-pair gate: Patch bootstrap-kit Kustomization failed",
			"id", dep.ID, "err", err)
		h.emitClusterMeshProgress(dep, "warn",
			fmt.Sprintf("ClusterMesh: cnpg-pair enable failed — Patch Kustomization %s/%s (%v); re-run converges",
				fluxSystemNamespace, bootstrapKitKustomizationName, err))
		return
	}

	h.log.Info("clustermesh: SOVEREIGN_ENABLE_CNPG_PAIR=true merged onto primary bootstrap-kit Kustomization + Flux reconcile requested",
		"id", dep.ID,
		"primaryRegion", primaryRegion,
		"replicaRegion", replicaRegion,
		"requestedAt", stamp,
	)
	h.emitClusterMeshProgress(dep, "info",
		"ClusterMesh confirmed across all regions — enabled bp-cnpg-pair (slot 16b) via SOVEREIGN_ENABLE_CNPG_PAIR=true and requested Flux reconcile")
}

// clusterMeshDynamicClient builds a dynamic.Interface for the cluster
// behind the given kubeconfig path, honouring the test-only factory
// override the same way buildRegionSlots does for typed clients.
func (h *Handler) clusterMeshDynamicClient(kubeconfigPath string) (dynamic.Interface, error) {
	if clusterMeshTestDynamicClientFactory != nil {
		if d, ok := clusterMeshTestDynamicClientFactory(kubeconfigPath); ok {
			return d, nil
		}
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig %q: %w", kubeconfigPath, err)
	}
	return helmwatch.NewDynamicClientFromKubeconfig(string(raw))
}

// buildRegionSlots gathers per-region clients + identity. The primary
// kubeconfig path is `<kubeconfigsDir>/<id>.yaml`; each secondary is
// `<kubeconfigsDir>/<id>-<region>.yaml`. The first non-empty region in
// dep.Request.Regions is treated as the primary.
//
// Regions whose kubeconfig is missing on disk are returned as
// already-failed slots so the per-peer status surfaces the gap.
func (h *Handler) buildRegionSlots(
	dep *Deployment,
	regions []provisioner.RegionSpec,
	primaryKubeconfigPath string,
	secondaryPaths map[string]string,
) []regionSlot {
	out := make([]regionSlot, 0, len(regions))

	// Index secondaries by the cloud region string (e.g. "hel1") and by
	// the suffixed key (e.g. "hel1-2") so we tolerate either form on
	// disk. spawnSecondaryRegionWatchers writes by the suffixed key.
	pickPath := func(key, cloudRegion string) string {
		if p, ok := secondaryPaths[key]; ok && p != "" {
			return p
		}
		if p, ok := secondaryPaths[cloudRegion]; ok && p != "" {
			return p
		}
		if h.kubeconfigsDir == "" {
			return ""
		}
		// Best-effort filesystem fallback: <dir>/<id>-<key>.yaml.
		candidate := filepath.Join(h.kubeconfigsDir, dep.ID+"-"+key+".yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}

	// Cilium cluster IDs are deterministic: primary gets the derived id,
	// each secondary gets primary+1, primary+2, … (matching infra
	// /hetzner/main.tf's allocation). When dep.Request.ClusterMeshID is
	// unset (operator submitted the canonical multi-region body without
	// pinning IDs), derive via the same hash main.tf uses so the
	// orchestrator + tofu + cilium-config agree byte-identically.
	// Caught on t125 (2026-05-16): primaryID stayed 0 → every peer's
	// clusterName fell back to dep.Request.ClusterMeshName which was
	// also empty → empty-string key in cilium-clustermesh Secret →
	// "Invalid value" Create rejection.
	primaryID := provisioner.DeriveClusterMeshID(dep.Request)

	// Derive primary mesh name once for consistent secondary suffixing.
	// Same trio of fallbacks as the per-region loop below, but lifted
	// out so secondaries don't re-derive on every iteration.
	primaryMeshName := strings.TrimSpace(dep.Request.ClusterMeshName)
	if primaryMeshName == "" {
		primaryMeshName = provisioner.DeriveClusterMeshName(dep.Request)
	}

	for idx, rs := range regions {
		key := regionKeyFromSpec(rs, idx)
		isPrimary := idx == 0
		kc := ""
		if isPrimary {
			kc = primaryKubeconfigPath
		} else {
			kc = pickPath(key, rs.CloudRegion)
		}

		clusterName := strings.TrimSpace(rs.ClusterMeshName)
		if clusterName == "" {
			if isPrimary {
				clusterName = primaryMeshName
			} else {
				// Match tofu's `secondary_region_cluster_mesh_name`
				// local exactly: `<sovereign-stem>-<region-stem-no-digits>`
				// (e.g. `t129-nbg`). Tofu sets the SECONDARY's
				// cilium-config cluster.name to this value via
				// CLUSTER_MESH_NAME envsubst; the orchestrator MUST
				// use the same string as the peer-name key in
				// `cilium-clustermesh` so the agent's
				// `cilium/cluster-config/v1/<peer-name>` etcd query
				// hits an existing key. Caught on t129 (2026-05-16):
				// orchestrator used `<primary>-<region-key>` (e.g.
				// `t129-mesh-nbg1-1`) but actual cluster.name was
				// `t129-nbg` → agent got
				// `failed to retrieve cluster configuration: not found`.
				clusterName = provisioner.DeriveSecondaryClusterMeshName(dep.Request, rs)
			}
		}

		slot := regionSlot{
			key:            key,
			kubeconfigPath: kc,
			clusterName:    clusterName,
		}
		if isPrimary {
			slot.clusterID = primaryID
		} else if primaryID != 0 {
			slot.clusterID = primaryID + idx
		}

		if kc == "" {
			slot.err = fmt.Errorf("kubeconfig path empty (cloud-init may not have posted back yet)")
			out = append(out, slot)
			continue
		}

		// Test-only short-circuit: if a factory override is wired,
		// route the path through it. Production override is nil; the
		// fall-through reads the file and calls helmwatch.
		if clusterMeshTestClientFactory != nil {
			if c, ok := clusterMeshTestClientFactory(kc); ok {
				slot.clientset = c
				out = append(out, slot)
				continue
			}
		}
		raw, err := os.ReadFile(kc)
		if err != nil {
			slot.err = fmt.Errorf("read kubeconfig %q: %w", kc, err)
			out = append(out, slot)
			continue
		}
		client, err := helmwatch.NewKubernetesClientFromKubeconfig(string(raw))
		if err != nil {
			slot.err = fmt.Errorf("build client from kubeconfig: %w", err)
			out = append(out, slot)
			continue
		}
		slot.clientset = client
		out = append(out, slot)
	}
	return out
}

// regionKeyFromSpec returns the per-region key string used to disambiguate
// kubeconfig filenames + secondaryWatchers / secondaryKubeconfigPaths
// map keys. The primary region (idx 0) returns "" so its kubeconfig
// path stays `<dir>/<id>.yaml`.
func regionKeyFromSpec(rs provisioner.RegionSpec, idx int) string {
	if idx == 0 {
		return ""
	}
	// Convention used by tofu's `secondary_regions = { for i, r in
	// var.regions : "${r.cloudRegion}-${i}" => r if i > 0 }` AND by
	// PutKubeconfig (?region=<k>) AND by spawnSecondaryRegionWatchers
	// (scan of `<id>-<rest>.yaml`): the suffix is the ORIGINAL spec
	// index `i`, NOT `i+1`. With 3 regions (idx 0=primary, idx 1, idx 2)
	// the secondary keys are `<region>-1` and `<region>-2` respectively.
	// The prior `idx+1` here returned `<region>-2`/`<region>-3` and
	// missed the kubeconfigs cloud-init had stored under `-1`/`-2`,
	// silently producing empty peer entries and `fullyMeshed=0` —
	// caught on t124 (2026-05-16).
	return fmt.Sprintf("%s-%d", rs.CloudRegion, idx)
}

// waitForClusterMeshLB polls Service kube-system/clustermesh-apiserver
// up to clusterMeshLBLookupTimeout for a LoadBalancer ingress IP. Per
// invariants A2/A3 the Service type MUST be LoadBalancer (always public
// IP, never NodePort). A typed-mismatch is treated as a hard failure
// rather than retried.
func (h *Handler) waitForClusterMeshLB(ctx context.Context, client kubernetes.Interface) (string, error) {
	timeout := clusterMeshLBLookupTimeout
	if clusterMeshTestOverrideLBTimeout > 0 {
		timeout = clusterMeshTestOverrideLBTimeout
	}
	interval := clusterMeshLBLookupInterval
	if clusterMeshTestOverrideLBInterval > 0 {
		interval = clusterMeshTestOverrideLBInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		svc, err := client.CoreV1().Services(clusterMeshNamespace).Get(callCtx, clusterMeshApiserverService, metav1.GetOptions{})
		cancel()
		if err != nil {
			if apierrors.IsNotFound(err) {
				if time.Now().After(deadline) {
					return "", fmt.Errorf("Service %s/%s not found after %s",
						clusterMeshNamespace, clusterMeshApiserverService, timeout)
				}
				if err := sleepCtx(ctx, interval); err != nil {
					return "", err
				}
				continue
			}
			return "", fmt.Errorf("Get Service %s/%s: %w",
				clusterMeshNamespace, clusterMeshApiserverService, err)
		}
		// Hard-fail if someone has retyped the Service: invariant A3.
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			return "", fmt.Errorf("Service %s/%s type %q violates invariant A3 (must be LoadBalancer)",
				clusterMeshNamespace, clusterMeshApiserverService, svc.Spec.Type)
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				return ing.IP, nil
			}
			if ing.Hostname != "" {
				return ing.Hostname, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("Service %s/%s has no LoadBalancer ingress after %s",
				clusterMeshNamespace, clusterMeshApiserverService, timeout)
		}
		if err := sleepCtx(ctx, interval); err != nil {
			return "", err
		}
	}
}

// snapshotRemoteCert reads kube-system/clustermesh-apiserver-remote-cert
// and returns the tls.crt + tls.key bytes. The upstream Cilium chart
// generates this Secret on every cluster where useAPIServer=true; the
// cert's CN is `remote` and matches an etcd RBAC user that has read
// access on the cilium/* prefix. Use these bytes verbatim as the
// client cert a REMOTE cluster presents when connecting in — matches
// the canonical `cilium clustermesh connect` CLI behavior.
//
// Caught on t129 (6cddff7ef4432bdc, 2026-05-16): minting a fresh
// cert (CN=local-cluster-name) failed etcd RBAC even though TLS
// handshake succeeded. Snapshotting the existing remote-cert puts
// the right CN on the wire.
func (h *Handler) snapshotRemoteCert(ctx context.Context, client kubernetes.Interface) (cert, key []byte, err error) {
	const name = "clustermesh-apiserver-remote-cert"
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(callCtx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("Get Secret %s/%s: %w",
			clusterMeshNamespace, name, err)
	}
	cert = firstNonEmptyBytes(secret.Data["tls.crt"], secret.Data["ca.crt"])
	key = firstNonEmptyBytes(secret.Data["tls.key"], secret.Data["ca.key"])
	if len(cert) == 0 || len(key) == 0 {
		return nil, nil, fmt.Errorf("Secret %s/%s missing tls.crt/tls.key (have keys: %v)",
			clusterMeshNamespace, name, secretKeys(secret))
	}
	return cert, key, nil
}

// snapshotCiliumCA reads kube-system/cilium-ca and returns the CA cert
// + key bytes. The upstream Cilium chart auto-generates this Secret on
// every cluster where clustermesh.useAPIServer is true.
func (h *Handler) snapshotCiliumCA(ctx context.Context, client kubernetes.Interface) (cert, key []byte, err error) {
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(callCtx, clusterMeshCASecretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("Get Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshCASecretName, err)
	}
	// Cilium chart uses ca.crt / ca.key by default; some older builds
	// used tls.crt / tls.key. Accept either, prefer ca.*.
	cert = firstNonEmptyBytes(secret.Data["ca.crt"], secret.Data["tls.crt"])
	key = firstNonEmptyBytes(secret.Data["ca.key"], secret.Data["tls.key"])
	if len(cert) == 0 || len(key) == 0 {
		return nil, nil, fmt.Errorf("Secret %s/%s missing CA cert/key bytes (have keys: %v)",
			clusterMeshNamespace, clusterMeshCASecretName, secretKeys(secret))
	}
	return cert, key, nil
}

// mintPeerClientCert issues a short-lived client certificate signed by
// the local Cilium CA. The SAN is the local cluster name; that's what
// the upstream chart's clustermesh-apiserver verifies on the mTLS
// handshake.
//
// The cert is intentionally not stored in a K8s Secret on the local
// cluster — it's transmitted as the value of one peer entry in B's
// cilium-clustermesh Secret. (B verifies it against A's CA, which we
// also write into the same entry under `<peer>-ca.crt`.)
func (h *Handler) mintPeerClientCert(caCertPEM, caKeyPEM []byte, clusterName string) (certPEM, keyPEM []byte, err error) {
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		return nil, nil, fmt.Errorf("decode CA cert PEM: empty")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("decode CA key PEM: empty")
	}
	caKey, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate client key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: clusterName},
		DNSNames:     []string{clusterName, clusterMeshApiserverService},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(clusterMeshPeerCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes := x509.MarshalPKCS1PrivateKey(clientKey)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM, nil
}

// parsePrivateKey accepts both PKCS1 and PKCS8-encoded RSA keys, which
// is the union of what cert-manager and openssl emit.
func parsePrivateKey(der []byte) (any, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("unsupported private key encoding")
}

// buildPeerConfigBlob returns the YAML clustermesh peer config the
// upstream chart's apiserver consumes. Filename paths inside the blob
// point at the well-known mount path /var/lib/cilium/clustermesh —
// those filenames must match the Secret entry keys we write (peer ->
// `<peer>-ca.crt`, `<peer>.crt`, `<peer>.key`).
//
// The endpoint uses the canonical Cilium `<peer>.mesh.cilium.io`
// hostname, NOT the LB IP directly. That hostname matches the
// `*.mesh.cilium.io` SAN in the clustermesh-apiserver server cert
// the upstream Cilium chart generates by default. Cilium agents
// resolve this hostname via a hostAliases entry on the cilium
// DaemonSet pod spec that maps `<peer>.mesh.cilium.io` -> LB IP
// (written by patchCiliumHostAliasesForPeer below). Caught on
// t128 (9680edbdce8fefe8, 2026-05-16): the prior code put the
// LB IP in the endpoint URL; TLS handshake failed because the
// server cert had no IP SAN matching the public LB IP.
func buildPeerConfigBlob(peerClusterName, peerLBIP string) []byte {
	endpoint := fmt.Sprintf("https://%s:%d", peerMeshHostname(peerClusterName), clusterMeshAPIServerPort)
	blob := strings.Join([]string{
		"endpoints:",
		"- " + endpoint,
		fmt.Sprintf("trusted-ca-file: /var/lib/cilium/clustermesh/%s-ca.crt", peerClusterName),
		fmt.Sprintf("cert-file:       /var/lib/cilium/clustermesh/%s.crt", peerClusterName),
		fmt.Sprintf("key-file:        /var/lib/cilium/clustermesh/%s.key", peerClusterName),
		"",
	}, "\n")
	return []byte(blob)
}

// peerMeshHostname returns the canonical Cilium clustermesh hostname
// for a peer — `<cluster-name>.mesh.cilium.io`. Used by both the
// peer config blob (etcd endpoint URL) and the hostAliases patch on
// the local cilium DaemonSet pod spec, so the agent's TLS client
// connects to a hostname the apiserver-server-cert covers via its
// `*.mesh.cilium.io` SAN.
func peerMeshHostname(peerClusterName string) string {
	return peerClusterName + ".mesh.cilium.io"
}

// applyClusterMeshSecret writes/merges peer entries into the local
// cluster's kube-system/cilium-clustermesh Secret. Existing entries
// for OTHER peer names are preserved; entries for the peer names in
// `entries` are overwritten with the freshly minted bytes (idempotent
// re-runs converge byte-identically because mintPeerClientCert is the
// only non-deterministic step and the new bytes always supersede).
func (h *Handler) applyClusterMeshSecret(ctx context.Context, client kubernetes.Interface, entries map[string][]byte) error {
	if len(entries) == 0 {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	existing, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(callCtx, clusterMeshSecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("Get Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshSecretName, err)
	}
	if apierrors.IsNotFound(err) {
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterMeshSecretName,
				Namespace: clusterMeshNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       "cilium-clustermesh",
					"app.kubernetes.io/managed-by": "catalyst-api",
					"catalyst.openova.io/seed":     "clustermesh-peers",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: entries,
		}
		if _, createErr := client.CoreV1().Secrets(clusterMeshNamespace).Create(callCtx, s, metav1.CreateOptions{}); createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				// Race window — fall through to Update.
				return h.updateClusterMeshSecret(ctx, client, entries)
			}
			return fmt.Errorf("Create Secret %s/%s: %w",
				clusterMeshNamespace, clusterMeshSecretName, createErr)
		}
		return nil
	}
	// Merge: keep entries we don't manage, overwrite ones we do.
	merged := make(map[string][]byte, len(existing.Data)+len(entries))
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range entries {
		merged[k] = v
	}
	patch := []byte(fmt.Sprintf(`{"data":%s}`, encodeSecretDataJSON(merged)))
	if _, patchErr := client.CoreV1().Secrets(clusterMeshNamespace).Patch(callCtx, clusterMeshSecretName, types.MergePatchType, patch, metav1.PatchOptions{}); patchErr != nil {
		return fmt.Errorf("Patch Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshSecretName, patchErr)
	}
	return nil
}

// updateClusterMeshSecret is the race-window fallback when Create
// raced an external writer.
func (h *Handler) updateClusterMeshSecret(ctx context.Context, client kubernetes.Interface, entries map[string][]byte) error {
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	existing, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(callCtx, clusterMeshSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Get Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshSecretName, err)
	}
	merged := make(map[string][]byte, len(existing.Data)+len(entries))
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range entries {
		merged[k] = v
	}
	existing.Data = merged
	if _, updErr := client.CoreV1().Secrets(clusterMeshNamespace).Update(callCtx, existing, metav1.UpdateOptions{}); updErr != nil {
		return fmt.Errorf("Update Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshSecretName, updErr)
	}
	return nil
}

// patchCiliumHostAliases adds one hostAliases entry per peer to the
// cilium DaemonSet's pod spec, mapping `<peer>.mesh.cilium.io` to
// the peer's clustermesh-apiserver LoadBalancer IP. Without this
// the agent can resolve the hostname but the TLS handshake fails
// because the LB IP is not in the apiserver-server-cert's SANs.
//
// The hostAliases list is a strategic-merge replace of the entire
// list on each call — idempotent re-runs converge to the same set.
// Caught on t128 (9680edbdce8fefe8, 2026-05-16): clustermesh agents
// stayed `0/2 remote clusters ready` despite full peer entries
// because TLS hostname verification failed at handshake time.
func (h *Handler) patchCiliumHostAliases(ctx context.Context, client kubernetes.Interface, peers []hostAliasPeer) error {
	if len(peers) == 0 {
		return nil
	}
	aliases := make([]map[string]any, 0, len(peers))
	for _, p := range peers {
		if p.LBIP == "" || p.PeerName == "" {
			continue
		}
		aliases = append(aliases, map[string]any{
			"ip":        p.LBIP,
			"hostnames": []string{peerMeshHostname(p.PeerName)},
		})
	}
	if len(aliases) == 0 {
		return nil
	}
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"hostAliases": aliases,
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal hostAliases patch: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	if _, err := client.AppsV1().DaemonSets(clusterMeshNamespace).Patch(callCtx, "cilium", types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch cilium DaemonSet hostAliases: %w", err)
	}
	return nil
}

// hostAliasPeer is a minimal projection used by patchCiliumHostAliases.
type hostAliasPeer struct {
	PeerName string
	LBIP     string
}

// rolloutRestartClusterMeshTargets bumps a restartedAt annotation on
// cilium, cilium-operator, and clustermesh-apiserver so they pick up
// the new Secret entries deterministically. Failures here are logged
// but never abort the orchestrator — the chart's reload watch can
// still pick up the new bytes on its next tick.
func (h *Handler) rolloutRestartClusterMeshTargets(ctx context.Context, dep *Deployment, slot *regionSlot) {
	stamp := time.Now().UTC().Format(time.RFC3339)
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"catalyst.openova.io/restartedAt":%q}}}}}`, stamp))
	for _, t := range clusterMeshRolloutTargets {
		callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		var err error
		switch t.kind {
		case "daemonset":
			_, err = slot.clientset.AppsV1().DaemonSets(clusterMeshNamespace).Patch(callCtx, t.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		case "deployment":
			_, err = slot.clientset.AppsV1().Deployments(clusterMeshNamespace).Patch(callCtx, t.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		}
		cancel()
		if err != nil && !apierrors.IsNotFound(err) {
			h.log.Warn("clustermesh: rollout-restart failed (continuing)",
				"id", dep.ID,
				"region", slot.key,
				"kind", t.kind,
				"name", t.name,
				"err", err,
			)
		}
	}
}

// countFullyMeshedRegions returns how many regions in the status
// summary reached the fully-meshed state (every peer Connected —
// AutoEstablishClusterMesh stamps ReadyAt exactly then). The
// runAutoEstablishClusterMesh reconcile loop uses this as its
// level-trigger condition (#3241): retry until the count equals the
// region total.
func countFullyMeshedRegions(statuses []ClusterMeshStatus) int {
	n := 0
	for _, st := range statuses {
		if !st.ReadyAt.IsZero() {
			n++
		}
	}
	return n
}

// shouldStartupClusterMeshReconcile reports whether a rehydrated
// deployment needs the level-triggered ClusterMesh reconcile loop
// kicked at catalyst-api startup (#3241): status=ready AND >1 region
// AND the primary kubeconfig file still readable on the PVC. This is
// what heals an hw126-shaped Sovereign zero-touch on the next
// mothership roll — Phase 1 terminated long ago, so nothing else ever
// re-fires the establish, and a partially-meshed cluster would
// otherwise stay partial until handover.
//
// The kubeconfig guard is warn-and-skip, not fail: a ready record
// whose kubeconfig file was lost across the restart (PVC unmount /
// wipe race) would otherwise spin the whole retry budget against
// "kubeconfig path empty". Fully-meshed deployments pass this check
// too — the loop's first attempt is a cheap idempotent re-run that
// confirms full mesh and exits.
func (h *Handler) shouldStartupClusterMeshReconcile(dep *Deployment) bool {
	dep.mu.Lock()
	status := dep.Status
	regionCount := len(dep.Request.Regions)
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()
	if status != "ready" || regionCount < 2 {
		return false
	}
	if kubeconfigPath == "" {
		h.log.Warn("clustermesh: startup reconcile skipped — primary kubeconfig path empty on ready multi-region deployment",
			"id", dep.ID,
			"regions", regionCount,
		)
		return false
	}
	if _, err := os.Stat(kubeconfigPath); err != nil {
		h.log.Warn("clustermesh: startup reconcile skipped — primary kubeconfig unreadable",
			"id", dep.ID,
			"path", kubeconfigPath,
			"err", err,
		)
		return false
	}
	return true
}

// emitClusterMeshProgress pushes a typed SSE event onto the deployment
// event bus so the canvas can render per-peer progress in real time.
// Phase is hardcoded to clusterMeshPhase ("clustermesh-progress") so
// the canvas reducer can filter on it.
func (h *Handler) emitClusterMeshProgress(dep *Deployment, level, msg string) {
	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   clusterMeshPhase,
		Level:   level,
		Message: msg,
	})
}

// ── small helpers ───────────────────────────────────────────────────

func firstNonEmptyBytes(a, b []byte) []byte {
	if len(a) > 0 {
		return a
	}
	return b
}

func secretKeys(s *corev1.Secret) []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Data))
	for k := range s.Data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// encodeSecretDataJSON marshals a map[string][]byte as the JSON shape
// the K8s API expects for Secret.data (base64-encoded values keyed by
// the entry name). Keeps the order stable so idempotent re-runs
// produce byte-identical patches.
func encodeSecretDataJSON(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"`)
		sb.WriteString(k)
		sb.WriteString(`":"`)
		sb.WriteString(base64Encode(data[k]))
		sb.WriteString(`"`)
	}
	sb.WriteString("}")
	return sb.String()
}

// base64Encode is std-lib base64.
func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// sleepCtx is context.Sleep — interruptible sleep. Returns ctx.Err()
// if the context is cancelled before the duration elapses.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
