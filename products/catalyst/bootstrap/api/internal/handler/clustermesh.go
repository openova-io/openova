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
//        LoadBalancer (per the chart overlay).
//   A3 — clustermesh-apiserver Service = LoadBalancer always. The DIAL
//        PORT however depends on what implements the LB (#3241 hw128):
//        a real cloud LB (Hetzner hcloud-ccm) listens on 2379 itself,
//        but Cilium nodeIPAM (kom4dc/Huawei — no CCM) "implements" the
//        Service by stamping a NODE-owned EIP as the ingress IP. That
//        EIP is DNAT'd to the node's private IP, so cilium's BPF VIP
//        frontend (keyed on the public EIP) never matches inbound
//        packets and EIP:2379 is connection-refused from outside; the
//        only externally-reachable path is the Service NodePort.
//        resolveClusterMeshEndpoint therefore dials NodePort exactly
//        when the ingress IP equals a node ExternalIP, and 2379
//        otherwise. Verified live on hw128: 212.72.24.52:2379 refused,
//        :31744 (NodePort) open.
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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
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
	provider       string               // cloud provider of THIS region ("huawei"/"hetzner"/…) — selects the dial port (#4811)
	clientset      kubernetes.Interface // typed client built from kubeconfig
	lbIP           string               // public LB IP of clustermesh-apiserver Service
	apiPort        int                  // port peers DIAL on lbIP — 2379 on a real cloud LB (Hetzner hcloud-ccm); the clustermesh-proxy hostPort (12379) on a no-CCM cloud (Huawei/kom4dc, CP-EIP ingress collides with k3s-etcd :2379). Never a NodePort (#4765).
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
	// clusterMeshAPIServerPort — the DEFAULT etcd port a peer dials
	// (2379). Overridable at runtime via CLUSTERMESH_APISERVER_DIAL_PORT
	// (see clusterMeshDialPort). On no-CCM Huawei the CP-node public EIP is
	// a Huawei-fabric 1:1 NAT AND the CP node runs k3s' own embedded etcd on
	// the host :2379 — so an off-cluster peer dialing <CP-EIP>:2379 is
	// DNAT'd to <CP-private-IP>:2379 and lands on K3S ETCD (server cert
	// CN=etcd-server, k3s CA), NEVER the Cilium clustermesh-apiserver
	// (CN=clustermesh-apiserver.cilium.io, Cilium CA). The peer's etcd
	// client then hangs on "Connecting to etcd server…" → "context
	// canceled" → heartbeat-watcher timeout (issue #4784, proven live on
	// hw225 with openssl: <EIP>:2379 → etcd-server cert; a host-socket on a
	// NON-2379 port → clustermesh-apiserver.cilium.io cert, Verify OK). The
	// durable fix routes peers to the clustermesh-proxy host-socket on a
	// dedicated non-2379 port (bp-cilium clustermesh-proxy DaemonSet) via
	// this override; Hetzner keeps the default 2379 behind hcloud-ccm.
	clusterMeshAPIServerPort    = 2379
	// clusterMeshProxyDialPort — the bp-cilium clustermesh-proxy DaemonSet
	// hostPort (#4784). On a no-CCM cloud (Huawei/kom4dc) there is no real
	// LoadBalancer: the clustermesh-apiserver Service's ingress IP is the
	// CP-node's public EIP (1:1 NAT to the CP private IP), and that host's
	// :2379 is answered by k3s' OWN embedded etcd — so peers MUST dial the
	// dedicated proxy host-socket instead. This value MUST match infra
	// `clustermesh_proxy_port` + bp-cilium `clustermeshProxy.hostPort`
	// (both 12379). NOT a NodePort — the proxy binds a hostNetwork socket.
	clusterMeshProxyDialPort    = 12379
	clusterMeshDialPortEnvVar   = "CLUSTERMESH_APISERVER_DIAL_PORT"
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

	// Steady-state heal cadence (#3583). After the retry loop first
	// converges (mesh fully established + #3236 cnpg-pair flip landed),
	// runAutoEstablishClusterMesh does NOT exit — it keeps re-running the
	// idempotent establish at this low frequency for as long as the
	// deployment stays status=ready, so a replica-auth Secret that gets
	// collaterally deleted from the replica's namespace (hw144: convergence
	// churn's gitea/harbor uninstall-remediation loops wiped the freshly
	// copied shared-pg `<name>-replication`/`-ca` Secrets out of shared-data,
	// and nothing re-copied because the loop had already exited) is re-copied
	// on the next pass. The Handler field clusterMeshSteadyStateInterval
	// overrides this for tests; zero falls back to the default below.
	clusterMeshSteadyStateIntervalDefault = 5 * time.Minute

	// #4811 — startup mesh-reconcile retry cadence + budget. When
	// restoreFromStore finds a ready multi-region deployment whose primary
	// kubeconfig is unresolved (the k8scache dir-load race — the file is on the
	// PVC but os.Stat missed it by a few ms), retryStartupClusterMeshReconcile
	// re-evaluates shouldStartupClusterMeshReconcile on this interval and
	// launches the establish the first time it resolves, giving up after the
	// budget so a genuinely-lost kubeconfig stops instead of spinning forever.
	// The Handler fields clusterMeshStartupRetry* override these for tests;
	// zero falls back to the defaults below.
	clusterMeshStartupRetryIntervalDefault = 5 * time.Second
	clusterMeshStartupRetryBudgetDefault   = 3 * time.Minute
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
	// clusterMeshRegionRoleSubstituteKey is each region's OWN role in the
	// pair ("primary" / "secondary"), stamped by cloud-init onto the
	// region's bootstrap-kit Kustomization. bp-cnpg-pair keys
	// `cnpgPair.side` off it; the two-stage flip (#3241 first-flip
	// deadlock) keys the patch ordering off it.
	clusterMeshRegionRoleSubstituteKey = "SOVEREIGN_REGION_ROLE"
	fluxReconcileRequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"

	// clusterMeshPeerClusterMeshNamesSubstituteKey (#4846, Refs #4656 #4275)
	// carries the Cilium ClusterMesh cluster.name(s) of the OTHER region(s)
	// for the region whose bootstrap-kit Kustomization it is stamped onto,
	// as a YAML flow-sequence string (e.g. `[hw228-me-east-b]` on region-A;
	// `[hw228-mesh]` on region-B). bp-postgres (16a/16c/16d) + bp-cnpg-pair
	// (16b) consume it as `crossRegionPeerClusters:
	// ${SOVEREIGN_PEER_CLUSTERMESH_NAMES:-[]}` to render the cross-region DR
	// CiliumNetworkPolicy that admits the ClusterMesh remote replica by
	// `io.cilium.k8s.policy.cluster` identity. This REPLACES the inert #4846
	// ipBlock: a k8s-NetworkPolicy ipBlock matches only a CIDR identity
	// (assigned solely to NON-endpoint IPs), so a ClusterMesh remote pod — a
	// KNOWN endpoint with its own pod identity — is `match none` → deny
	// (proven with cilium-dbg on hw228). It is stamped alongside the
	// SOVEREIGN_ENABLE_CNPG_PAIR flip below (the crossRegion netpol half only
	// renders once that gate is on), so the CNP appears exactly when the
	// cross-region datapath activates; single-region provs never reach this
	// flip → the slot default `[]` → no CNP (byte-identical).
	clusterMeshPeerClusterMeshNamesSubstituteKey = "SOVEREIGN_PEER_CLUSTERMESH_NAMES"

	// Cross-region shared-pg WRITE-host substitute keys (#4436, Refs
	// #4159). On a 2-region shared-pg Sovereign the CNPG primary (the
	// region-local `shared-pg-rw` Service) lives ONLY in region-A; the
	// SECONDARY region runs the streaming replica and has NO `shared-pg-rw`
	// Service, so a secondary consumer that dials it NXDOMAINs
	// (`cannot resolve host shared-pg-rw…` — keycloak-0 CrashLoop, live on
	// dep 4635277cae4ffed9 region-B). keycloak/gitea/harbor read this host
	// as a scalar (bitnami externalDatabase.host / a plain env value, NOT a
	// secretKeyRef), so they can't pick up the topology-aware host the hub
	// Secret carries — the host must be flipped in the bootstrap-kit
	// substitute map. cloud-init #4159 already renders these as `-mesh-rw`
	// on the secondary for FRESH provs, but the substitute map is baked at
	// boot and never re-patched, so PRE-#4159 (stale) 2-region envs keep the
	// region-local `-rw` host (or omit the keycloak key entirely → the slot
	// default `:=shared-pg-rw` wins). The post-mesh gate re-stamps these to
	// the ClusterMesh-global `shared-pg-mesh-rw` WRITE alias so stale envs
	// self-heal AND fresh provs are belt-and-suspenders idempotent.
	clusterMeshSharedPGMeshRWHost          = "shared-pg-mesh-rw.shared-data.svc.cluster.local"
	clusterMeshKeycloakPGHostSubstituteKey = "SOVEREIGN_KEYCLOAK_PG_HOST"
	clusterMeshGiteaPGHostSubstituteKey    = "SOVEREIGN_GITEA_PG_HOST"
	clusterMeshHarborPGHostSubstituteKey   = "SOVEREIGN_HARBOR_PG_HOST"
)

// Cross-cluster CNPG replica-auth Secret sync (#3254, the prerequisite
// #3253's PR body flagged as unsolved). The bp-cnpg-pair replica Cluster
// CR — rendered on the SECONDARY region's cluster since chart 0.2.0's
// split-side topology (#3253) — streams its pg_basebackup/WAL from the
// primary over Cilium ClusterMesh and authenticates as the
// `streaming_replica` role with the PRIMARY cluster's CNPG-generated TLS
// material. Those Secrets are created by CNPG ONLY on the primary
// region's cluster (namespace `cnpg`); the replica chart's
// `externalClusters[].sslKey`/`sslCert`/`sslRootCert`
// (platform/cnpg-pair/chart/templates/replica-cluster.yaml ~L127-135)
// reference them BY NAME, so the same-named Secrets must also exist on
// the replica cluster. The chart comment claimed "ESO syncs this across
// clusters in production" but nothing did — so the slot-16b flip copies
// them here, primary → every replica, BEFORE enabling the gate.
//
// Names are DERIVED THE SAME WAY THE CHART DOES: the chart's
// `cnpg-pair.fullname` helper resolves to <releaseName>-<chartName>
// (releaseName `cnpg-pair` + chartName `bp-cnpg-pair`; the release name
// does not contain the chart name, so they concatenate →
// `cnpg-pair-bp-cnpg-pair`), and the externalClusters refs are
// `<fullname>-primary-replication` / `<fullname>-primary-ca`. The HR
// (clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml) pins
// releaseName=cnpg-pair + targetNamespace=cnpg, both stable; if either
// ever changes, these constants move in lockstep.
//
// 🛑 Cert ROTATION is NOT handled here: the copy is a point-in-time
// snapshot. The #3241 level-trigger re-runs AutoEstablishClusterMesh
// (and post-handover finalisation re-fires the flip), each of which
// re-copies the then-current bytes, so a rotation eventually propagates
// on a later establish run — but there is no dedicated watch/refresh. A
// long-lived rotation story (an ESO ClusterExternalSecret / reflector
// annotation that keeps the replica copy live) is follow-up
// (#3249-class).
const (
	cnpgPairNamespace       = "cnpg"
	cnpgPairReleaseFullname = "cnpg-pair-bp-cnpg-pair"
	cnpgPairReplicationCert = cnpgPairReleaseFullname + "-primary-replication"
	cnpgPairReplicationCA   = cnpgPairReleaseFullname + "-primary-ca"
)

// cnpgPairReplicaAuthSecrets — the exact Secret names the replica-side
// chart's externalClusters block references (sslKey/sslCert →
// `-primary-replication`, sslRootCert → `-primary-ca`). Copied primary →
// every replica region before the flip.
var cnpgPairReplicaAuthSecrets = []string{
	cnpgPairReplicationCert,
	cnpgPairReplicationCA,
}

// Cross-cluster CNPG replica-auth Secret sync for the SHARED-PG instances
// (#3571, Refs #3375 North-Star-4). bp-postgres 0.2.0 makes the 3 shared
// data instances (shared-pg / -b / -c) render the SAME bp-cnpg-pair
// split-side shape when topology.crossRegion flips on — and crossRegion is
// wired to the SAME SOVEREIGN_ENABLE_CNPG_PAIR substitute this flip patches.
// So when the gate flips, each shared instance's REPLICA half (rendered on
// the secondary cluster) streams WAL from its primary and authenticates with
// the primary's CNPG-generated `<instance>-replication` / `<instance>-ca`
// TLS Secrets — created by CNPG only on the PRIMARY cluster, in namespace
// `shared-data`. Exactly like the bp-cnpg-pair pair, those Secrets must also
// exist on the replica cluster (the replica chart references them by name
// from externalClusters.sslKey/sslCert/sslRootCert), so this flip copies
// them primary → every replica BEFORE enabling the gate.
//
// Names are DERIVED THE SAME WAY THE CHART DOES: a CNPG Cluster named
// `<instance>` generates `<instance>-replication` (streaming_replica client
// cert) + `<instance>-ca` (the cluster CA). The instance names are the slot
// releaseNames (16a→shared-pg, 16c→shared-pg-b, 16d→shared-pg-c) and the
// namespace is shared-data (the shared-pg slots' targetNamespace) — all
// stable; if any ever changes, these constants move in lockstep.
//
// CONDITIONAL: the shared-pg instances exist only when shared-pg is enabled
// (SOVEREIGN_ENABLE_SHARED_PG=true). When it is OFF the slots render empty
// releases, CNPG never mints these Secrets, and the sync SKIPS the whole
// group (so a non-shared-pg Sovereign's cnpg-pair flip is never blocked by
// absent shared-pg Secrets). Detected from the substitute map already
// gathered for the region precondition (clusterMeshSharedPGSubstituteKey).
const (
	clusterMeshSharedPGSubstituteKey = "SOVEREIGN_ENABLE_SHARED_PG"
	sharedPGNamespace                = "shared-data"
)

// sharedPGInstanceNames — the 3 shared data-instance Cluster names (= the
// slot releaseNames). Each contributes a `<name>-replication` + `<name>-ca`
// Secret pair to copy primary → every replica when crossRegion flips on.
var sharedPGInstanceNames = []string{
	"shared-pg",   // slot 16a — gitea / harbor / keycloak
	"shared-pg-b", // slot 16c — grafana / powerdns / powerdns-admin
	"shared-pg-c", // slot 16d — the Organization mesh / newapi / openova-flow
}

// sharedPGReplicaAuthSecrets — the per-instance `-replication` + `-ca` Secret
// names the bp-postgres replica-cluster.yaml externalClusters block
// references (sslKey/sslCert → `<instance>-replication`, sslRootCert →
// `<instance>-ca`). Built from sharedPGInstanceNames so the set stays in
// lockstep with the slot releaseNames.
var sharedPGReplicaAuthSecrets = func() []string {
	out := make([]string, 0, len(sharedPGInstanceNames)*2)
	for _, n := range sharedPGInstanceNames {
		out = append(out, n+"-replication", n+"-ca")
	}
	return out
}()

// sharedPGConsumerHubSecrets — the per-consumer HUB CONNECTION Secret names
// (bp-postgres role-secrets.yaml `reflect.secretName`, namespace shared-data)
// that the cross-region CONSUMER apps read for their DB host + password
// (#3629). Unlike the replica-auth TLS Secrets above, these carry CONSUMER
// credentials, and they DIVERGE across regions: bp-postgres mints a fresh
// random password per region during each region's pre-crossRegion-flip
// SINGLETON phase (role-secrets.yaml is primary/singleton-side only) and
// freezes it via `helm.sh/resource-policy: keep`. After the flip, region-B's
// `<instance>-replica` follower streams region-A's catalog via pg_basebackup —
// so the AUTHORITATIVE role password is region-A's — but region-B's consumer
// namespaces still carry region-B's STALE, DIVERGENT hub Secret (its OWN random
// password + the region-LOCAL `<instance>-rw` host that NXDOMAINs on the replica
// cluster). Measured live on hw147 region-B: grafana + powerdns-admin
// CrashLoopBackOff (`lookup shared-pg-b-rw…: no such host`), and even the host
// aside the region-B password (`RXq1…`/`CmXM…`) did not match the region-A
// catalog password (`E5WJ…`/`OB0h…`). The emberstack reflector copies a Secret
// only WITHIN a cluster (never across the ClusterMesh), so the only fix is to
// copy region-A's authoritative hub Secrets (correct password + the
// topology-aware `<instance>-mesh-rw` host bp-postgres 0.2.2 renders on the
// active-hot-standby primary) primary → replica into shared-data; region-B's
// reflector then re-pushes the corrected Secret into each consumer namespace,
// overwriting the divergent copy. Same names the slots 16a/16c/16d declare.
//
// Sourced from the slot `reflect.secretName` values — keep in lockstep with
// clusters/_template/bootstrap-kit/16{a,c,d}-bp-postgres-shared*.yaml.
//
// vc-mgmt mangled copies (#4158, Refs #3878): the four consumers that run
// INSIDE vc-mgmt (keycloak/gitea/harbor on shared-pg, grafana on shared-pg-b)
// declare `reflect.mangledTarget`, so role-secrets.yaml Pass 4 ALSO renders a
// SECOND hub Secret named with the vCluster syncer-mangled object name
// `<secretName>-x-<vclusterNamespace>-x-mgmt-vcluster` (annotated
// `reflection-auto-namespaces: mgmt`). That mangled object is the EXACT host
// Secret the in-vc-mgmt pod mounts in single-namespace mode — the failure
// string `MountVolume.SetUp … secret "<…>-x-<ns>-x-mgmt-vcluster" not found`.
// Like the plain copies these are PRIMARY-side only (the whole template is
// `renderReplicaHalf`-gated) and the reflector never crosses the ClusterMesh,
// so on a fresh active-hot-standby prov region-B's mgmt-vCluster keycloak (and
// gitea/harbor/grafana) wedge at FailedMount for the mangled object that
// nothing in region-B materialises — measured live on dep 4635277cae4ffed9
// region-B `me-east-215-b` (keycloak-0 Init:0/2 x291 over 9h, cascading
// bp-oidc-gate / bp-sso-bridge / bp-powerdns-admin / bp-newapi-host-seams into
// `dependency 'mgmt/bp-keycloak' is not ready`). Copying the mangled copies
// primary → replica (they carry the same `-mesh-rw` host so the readiness gate
// passes, and the `reflection-auto-namespaces: mgmt` annotation so the
// replica's own reflector re-pushes them into region-B's `mgmt` namespace)
// closes the gap identically to the plain consumer copies. Mangled-name
// pattern from role-secrets.yaml Pass 4 (`-x-mgmt-vcluster` is the default
// vcluster); only the 4 vc-mgmt-homed bindings declare mangledTarget — the
// shared-pg-c bindings (org/newapi/openova-flow) keep their `<ns>/*` fromHost
// wildcard (#3876) and need NO mangled copy.
var sharedPGConsumerHubSecrets = []string{
	// instance A (shared-pg, slot 16a)
	"harbor-database-secret",
	"gitea-database-secret",
	"keycloak-database-secret",
	// instance B (shared-pg-b, slot 16c)
	"grafana-database-env",
	"pdns-database-secret",
	"pda-shared-database-secret",
	// instance C (shared-pg-c, slot 16d)
	"org-database-secret",
	"newapi-database-secret",
	"openova-flow-database-secret",
	// vc-mgmt mangled copies (#4158) — the host object the in-vc-mgmt pod
	// mounts; rendered primary-side only by role-secrets.yaml Pass 4
	// (reflect.mangledTarget), so they must cross the mesh too.
	"harbor-database-secret-x-harbor-x-mgmt-vcluster",
	"gitea-database-secret-x-gitea-x-mgmt-vcluster",
	"keycloak-database-secret-x-keycloak-x-mgmt-vcluster",
	"grafana-database-env-x-grafana-x-mgmt-vcluster",
}

// sharedPGConsumerWorkload identifies the long-running host-cluster workload a
// consumer hub-secret feeds, so #4878's post-copy rollout-restart can target it.
type sharedPGConsumerWorkload struct {
	kind      string // "statefulset" | "deployment"
	name      string
	namespace string
}

// sharedPGConsumerRestartTargets maps the SUBSET of sharedPGConsumerHubSecrets
// whose consumer is a LONG-RUNNING host-cluster workload that reads its DB
// password ONCE at boot (into a JVM/connection pool) and does NOT crash-restart
// when that password later rotates — so it must be explicitly rolled to pick up
// the corrected credential (#4878).
//
// THE BUG: on a 2-region prov, region-B's keycloak/gitea/harbor boot during the
// pre-ClusterMesh-flip SINGLETON phase against a region-LOCAL, randomly-minted
// shared-pg password. Once region-A's hubs are authoritative (`-mesh-rw` —
// since #5230 already in the pre-mesh phase; before #5230 only after the
// full-mesh flip), syncSharedPGConsumerHubSecrets overwrites
// region-B's divergent hub Secret with region-A's AUTHORITATIVE password (and
// the DB role replicates onto the follower), but the already-running pod stays
// pinned to the stale password it cached at boot → endless
// `FATAL: password authentication failed`. The mounted Secret + the DB are both
// correct; only the process is stale. A rollout-restart is the fix.
//
// Confirmed against the charts + bootstrap-kit slots:
//   - keycloak-database-secret → StatefulSet keycloak/keycloak
//     (bitnami keycloak subchart, HelmRelease releaseName=keycloak,
//     targetNamespace=keycloak — slot 09-keycloak.yaml)
//   - gitea-database-secret    → Deployment gitea/gitea
//     (gitea subchart renders a Deployment — platform/gitea Chart.yaml L18 +
//     strategy.type=Recreate; releaseName=gitea, ns=gitea — slot 10-gitea.yaml)
//   - harbor-database-secret   → Deployment harbor/harbor-core
//     (goharbor subchart core Deployment; releaseName=harbor, ns=harbor —
//     slot 19-harbor.yaml)
//
// DELIBERATELY NOT mapped: the other consumers (grafana / powerdns-admin / pdns
// / org / newapi / openova-flow) CrashLoopBackOff on the stale region-LOCAL host
// before the flip, so they already restart-and-re-read on their own once the
// corrected Secret lands; and the `-x-…-x-mgmt-vcluster` mangled copies are
// vestigial after the #4325 de-vcluster (keycloak/gitea/harbor now run directly
// on the region control plane, not inside vc-mgmt). Restarting an unmapped name
// is a silent no-op (the map lookup misses), so the set is conservative by
// construction — no restart-thrash risk beyond the three verified workloads.
var sharedPGConsumerRestartTargets = map[string]sharedPGConsumerWorkload{
	"keycloak-database-secret": {kind: "statefulset", name: "keycloak", namespace: "keycloak"},
	"gitea-database-secret":    {kind: "deployment", name: "gitea", namespace: "gitea"},
	"harbor-database-secret":   {kind: "deployment", name: "harbor-core", namespace: "harbor"},
}

// sharedPGConsumerCredHashAnnotation records, on a restarted consumer's POD
// TEMPLATE, a short fingerprint of the shared-pg credential the rollout-restart
// last rolled it onto (#4878 residual). It is the IDEMPOTENCY key: the restart
// reconcile re-fires only when the authoritative credential's fingerprint
// differs from the one already stamped here, so a steady-state / already-healed
// pass never restarts (the #3241/#3583 no-thrash contract) while a genuine
// rotation re-fires exactly once — and, crucially, the restart no longer fires
// on the mere hub-secret CHANGE before the credential is consistent.
const sharedPGConsumerCredHashAnnotation = "catalyst.openova.io/sharedpg-cred-hash"

// sharedPGConsumerCredentialFingerprint returns a stable, short hex fingerprint
// of a shared-pg hub Secret's EFFECTIVE data (Data overlaid with StringData —
// the same bytes the consumer reads). Used as the #4878 idempotency key so the
// consumer rollout-restart fires exactly once per distinct credential value.
func sharedPGConsumerCredentialFingerprint(s *corev1.Secret) string {
	if s == nil {
		return ""
	}
	data := effectiveSecretData(s)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sum := sha256.New()
	for _, k := range keys {
		sum.Write([]byte(k))
		sum.Write([]byte{0})
		sum.Write(data[k])
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// sharedPGMeshRWHostMarker — the substring a hub Secret's `host` key carries
// ONLY once bp-postgres has rendered the active-hot-standby topology-aware write
// alias (`<instance>-mesh-rw`). The consumer-hub sync uses this as a readiness
// gate: it propagates region-A's hub Secret to the replicas ONLY after region-A
// has reconciled crossRegion=true (so its `host`/`uri` point at `-mesh-rw`), to
// avoid ever pushing the region-LOCAL `-rw` host (which would NXDOMAIN on the
// replica) during the brief window before region-A's own slot-16{a,c,d} upgrade
// lands. Single-region / pre-flip hubs carry `-rw` and are correctly skipped.
const sharedPGMeshRWHostMarker = "-mesh-rw."

// keycloakAdminSecretNamespace / keycloakAdminSecretName / keycloakAdminSecretNames
// — the HOST-cluster namespace + Secret names of the keycloak master-realm admin
// credentials synced primary → replica (#4158, refreshed for #4915).
//
// WHY THIS NEEDS A CROSS-REGION COPY (proven live on dep 4635277cae4ffed9
// region-B `me-east-215-b`, 2026-06-23; re-confirmed hw233 dep aef1a818344814b4,
// 2026-07-09):
//
// The bp-keycloak chart's #4344 defense materialises a STABLE per-region
// `keycloak-admin` Secret (`admin-password`, resource-policy: keep) and wires the
// bitnami subchart at it — but that value is generated INDEPENDENTLY per region
// (region-A `mMPW…`, region-B `aevF…`), because each cluster's chart runs its own
// `randAlphaNum` at install with no prior Secret to adopt.
//
// Both regions' keycloak SERVERS share ONE keycloak DB: the distributed CNPG
// `shared-pg-mesh-rw` (region-A primary + region-B streaming replica), and
// KC_CACHE_STACK=jdbc-ping makes the two keycloak Pods one cross-region Infinispan
// cluster. Region-A (primary) boots first and seeds the shared DB's master-realm
// `admin` password hash from ITS Secret. Region-B boots later against the
// NON-empty shared DB, so KC_BOOTSTRAP_ADMIN is ignored (the admin row exists) —
// the only password that authenticates against the shared keycloak is region-A's:
//
//	region-A pw → keycloak server: login OK
//	region-B pw → keycloak server: "invalid_user_credentials" (HTTP 401)
//
// So region-B's `keycloak-config-cli` post-install hook (and every kcadm /
// sso-bridge consumer) reads region-B's LOCAL, divergent `admin-password` → HTTP
// 401 → the sovereign-realm import never runs → the bp-keycloak HR's hook times
// out → the whole SSO tier (bp-grafana / bp-gitea / bp-guacamole / bp-newapi /
// bp-oidc-gate / bp-powerdns-admin / bp-sso-bridge) stays `Ready=False` with
// `dependency 'flux-system/bp-keycloak' is not ready` and region-B apps never
// install.
//
// The #4325 de-vcluster moved keycloak OUT of the `mgmt` vCluster INTO the host
// `keycloak` namespace, so the credentials now live at their PLAIN host names
// (`keycloak/keycloak-admin`, `keycloak/catalyst-kc-master-admin-credentials`) —
// NOT the retired syncer-mangled `mgmt/keycloak-x-keycloak-x-mgmt-vcluster`. The
// emberstack reflector copies only WITHIN a cluster, never across the ClusterMesh,
// so nothing materialises region-A's value on region-B on its own. catalyst-api
// is the only actor that spans both regions, so it copies region-A's authoritative
// admin Secret(s) primary → every replica: the replica's config-cli + sso-bridge
// then authenticate against the SAME shared keycloak DB region-A seeded. No DB
// surgery + no keycloak restart is needed on a fresh prov — once the replica's
// Secret carries region-A's value, its config-cli retry succeeds against the value
// already in the shared DB. Idempotent + best-effort: on the steady-state pass the
// bytes already match and copySecretAcrossClusters skips the write.
//
// UNLIKE the consumer-hub sync this is NOT gated on the `-mesh-rw` host marker:
// the admin Secret carries no DB host, only the password, and region-A's value
// is authoritative the moment region-A's keycloak has minted it — there is no
// "pushing it would NXDOMAIN" window to wait out.
const (
	keycloakAdminSecretName      = "keycloak-admin"
	keycloakAdminSecretNamespace = "keycloak"
)

// keycloakAdminSecretNames — every host-namespace keycloak admin Secret synced
// region-A → replica by syncKeycloakAdminSecret. `keycloak-admin` (#4344 stable
// admin-password) is what config-cli + KC_BOOTSTRAP_ADMIN read; the derived
// `catalyst-kc-master-admin-credentials` (#2914) is the master-realm token
// credential bp-sso-bridge uses to create per-Org realms — both MUST equal
// region-A's value since both regions share ONE keycloak DB (#4915).
var keycloakAdminSecretNames = []string{
	keycloakAdminSecretName,
	"catalyst-kc-master-admin-credentials",
}

// ssoOIDCMangledHostSecrets — the vCluster-syncer-mangled HOST-namespace names
// of the per-app SSO/OIDC credential Secrets the in-vc-mgmt apps mount, that must
// be copied primary → replica (#4158, the SSO layer above the #4159 DB-secret +
// #4162 keycloak-admin fixes). namespace = ssoOIDCMangledHostSecretsNamespace
// (`mgmt`), the host namespace the mgmt vCluster runs in.
//
// WHY THIS NEEDS A CROSS-REGION COPY (proven live on dep 4635277cae4ffed9
// region-B `me-east-215-b`, 2026-06-24):
//
// Unlike the DB-secret (#4159, sourced from bp-postgres role-secrets) and the
// keycloak admin Secret (#4162, a plain Helm-minted Secret), the per-app SSO
// credential Secret is produced by an ESO `ExternalSecret` that resolves from the
// `vault-region1` ClusterSecretStore — region-A's in-vc-mgmt OpenBao reached over
// the ClusterMesh. ESO runs HOST-side and authenticates to OpenBao with a
// ServiceAccount JWT via OpenBao's `kubernetes/` auth mount, which (in the
// cross-cluster vc-mgmt topology, bp-openbao crossClusterTokenReview) TokenReviews
// ONLY against the PRIMARY region's host apiserver (10.96.0.1). region-B's ESO
// presents a token signed by REGION-B's host apiserver, which region-A's host
// apiserver cannot validate → OpenBao `kubernetes/login` returns 403 permission
// denied → the `vault-region1` store is `InvalidProviderConfig` on region-B →
// every per-app SSO `ExternalSecret` (grafana/harbor/openbao/powerdns-admin) sits
// `SecretSyncedError`, so the in-vc Secret never materialises and the vCluster
// syncer never surfaces the mangled HOST object the kubelet mounts.
//
// Measured live region-B: `grafana-…-x-grafana-x-mgmt-vcluster`
// CreateContainerConfigError — `secret "grafana-sso-oidc-credentials-x-grafana-x-
// mgmt-vcluster" not found` (the grafana Pod env-references it `Optional: false`).
// region-A resolves all of these fine (its ESO TokenReviews against ITS OWN host
// apiserver natively), and the syncer surfaces the mangled host object in region-A
// `mgmt`.
//
// catalyst-api is the only actor that spans both regions, so — exactly as
// syncKeycloakAdminSecret does for the admin Secret — it copies region-A's
// RESOLVED mangled host Secret primary → every replica directly into the HOST
// `mgmt` namespace. The in-vc-mgmt app (single-namespace sync mode) mounts that
// host object DIRECTLY by its mangled name (verified: the #4159
// `grafana-database-env-x-grafana-x-mgmt-vcluster` reflector copy, NOT a
// syncer-created object, is mounted the same way and survives in region-B), so no
// in-vc source or live ESO is required on the replica. Region-A is the SOURCE,
// never a destination (slots[0] is skipped), so this never regresses the primary.
//
// Best-effort + level-trigger: a missing source (region-A's ESO hasn't resolved
// yet) or a per-replica copy failure is logged + skipped; the #3241/#3583 re-run
// converges it. Idempotent — copySecretAcrossClusters skips the write when the
// destination bytes already match. NOT gated on `-mesh-rw` (these carry OIDC
// client creds + URLs, no DB host). Single-region (len(slots)<2) is a no-op.
//
// Scope = only the vc-mgmt-homed SSO consumers whose Pod env-mounts the mangled
// host Secret `Optional: false` and whose source is the OpenBao-backed ESO that
// 403s on the replica. bp-guacamole is intentionally ABSENT: its OIDC client
// Secret is chart-minted in-vc (`lookup`-or-generate, no OpenBao/ESO dependency),
// so region-B materialises it locally + guacamole runs without any cross-region
// copy (verified live region-B: guacamole-server Running, no ExternalSecret).
var ssoOIDCMangledHostSecrets = []string{
	"grafana-sso-oidc-credentials-x-grafana-x-mgmt-vcluster",
	"harbor-sso-oidc-credentials-x-harbor-x-mgmt-vcluster",
	"openbao-sso-oidc-credentials-x-openbao-x-mgmt-vcluster",
}

const ssoOIDCMangledHostSecretsNamespace = "mgmt"

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

// clusterMeshTestClientFactory — test-only hook. When set,
// buildRegionSlots routes its kubeconfig → clientset construction
// through this function instead of the production helmwatch parser.
// Tests inject fakes; production leaves it unset.
//
// Returning (nil, false) means "no override for this path, fall
// through to production". Returning (client, true) injects the fake.
//
// #4811 part-b: stored behind an atomic.Pointer rather than a bare
// package var. The establish reconcile loop (runAutoEstablishClusterMesh)
// READS this hook from a background goroutine that a prior test may leak
// past its own boundary (e.g. the bounded-retry self-heal test); the NEXT
// test's install/restore WRITES it. Under `go test -race` that unsynchronized
// read/write on a plain var is a reported data race that crashes the whole
// package binary (seen only in CI, where scheduling differs). The atomic
// load/store makes the seam race-free without any production lock on the hot
// path (production leaves the pointer nil → a single atomic load returning nil).
type clusterMeshClientFactory func(kubeconfigPath string) (kubernetes.Interface, bool)

var clusterMeshTestClientFactoryPtr atomic.Pointer[clusterMeshClientFactory]

func loadClusterMeshTestClientFactory() clusterMeshClientFactory {
	if p := clusterMeshTestClientFactoryPtr.Load(); p != nil {
		return *p
	}
	return nil
}

func setClusterMeshTestClientFactory(fn clusterMeshClientFactory) {
	if fn == nil {
		clusterMeshTestClientFactoryPtr.Store(nil)
		return
	}
	clusterMeshTestClientFactoryPtr.Store(&fn)
}

// clusterMeshTestDynamicClientFactory — test-only hook mirroring
// clusterMeshTestClientFactory for the DYNAMIC client the cnpg-pair
// gate flip (#3236) uses to patch the bootstrap-kit Flux Kustomization.
// Same contract + same atomic-pointer race-safety rationale as above.
type clusterMeshDynamicClientFactory func(kubeconfigPath string) (dynamic.Interface, bool)

var clusterMeshTestDynamicClientFactoryPtr atomic.Pointer[clusterMeshDynamicClientFactory]

func loadClusterMeshTestDynamicClientFactory() clusterMeshDynamicClientFactory {
	if p := clusterMeshTestDynamicClientFactoryPtr.Load(); p != nil {
		return *p
	}
	return nil
}

func setClusterMeshTestDynamicClientFactory(fn clusterMeshDynamicClientFactory) {
	if fn == nil {
		clusterMeshTestDynamicClientFactoryPtr.Store(nil)
		return
	}
	clusterMeshTestDynamicClientFactoryPtr.Store(&fn)
}

// clusterMeshTestOverrideLBTimeout / clusterMeshTestOverrideLBInterval
// — test-only override knobs for the LB-discovery poll loop. Zero
// falls back to the production constants. Tests set these to
// sub-second values so the LB-absent path runs in milliseconds.
//
// #4811 part-b: stored as atomic.Int64 nanoseconds for the same reason
// the factory hooks above are atomic — waitForClusterMeshLB READS them
// from the long-lived steady-state heal goroutine, which some tests leave
// running past their own boundary (status never leaves "ready"), while a
// LATER test WRITES them. A plain time.Duration var makes that a `-race`
// data race that crashes the package test binary in CI. Production never
// sets them (both stay 0 → the load returns 0 → production constants win).
var (
	clusterMeshTestOverrideLBTimeoutNanos  atomic.Int64
	clusterMeshTestOverrideLBIntervalNanos atomic.Int64
)

func clusterMeshTestOverrideLBTimeout() time.Duration {
	return time.Duration(clusterMeshTestOverrideLBTimeoutNanos.Load())
}

func clusterMeshTestOverrideLBInterval() time.Duration {
	return time.Duration(clusterMeshTestOverrideLBIntervalNanos.Load())
}

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
//
// This public wrapper preserves the (statuses, error) contract every
// call site + test relies on. The reconcile loop calls the internal
// autoEstablishClusterMesh directly so it can also observe whether the
// #3236 cnpg-pair flip landed (the missing convergence signal that left
// hw126 partially flipped — see runAutoEstablishClusterMesh).
func (h *Handler) AutoEstablishClusterMesh(ctx context.Context, dep *Deployment) ([]ClusterMeshStatus, error) {
	statuses, _, err := h.autoEstablishClusterMesh(ctx, dep)
	return statuses, err
}

// autoEstablishClusterMesh is the worker behind AutoEstablishClusterMesh.
// It returns the per-region statuses, an error, AND cnpgPairConverged —
// true when the deployment is fully converged from the cnpg-pair flip's
// point of view: single-region (no flip applicable), OR multi-region with
// the mesh fully established AND the slot-16b flip fully landed. It is
// false when the mesh is fully meshed but the flip refused/failed (e.g.
// the primary postgres hasn't minted its replica-auth Secrets yet — the
// exact hw126 state), so the reconcile loop keeps retrying instead of
// declaring victory on mesh-only readiness.
func (h *Handler) autoEstablishClusterMesh(ctx context.Context, dep *Deployment) ([]ClusterMeshStatus, bool, error) {
	if dep == nil {
		return nil, false, fmt.Errorf("autoEstablishClusterMesh: dep is nil")
	}

	// Resolve the primary kubeconfig path via the CONVENTIONAL fallback
	// (#3241): dep.Result.KubeconfigPath carries json `omitempty` and is
	// lost when a mothership roll / catalyst-api restart persists a
	// rehydrated record before PutKubeconfig stamped it — the exact hw128
	// failure where the primary mesh slot got an empty path → "kubeconfig
	// path empty" → the secondary never peered → fullyMeshed=0 →
	// SOVEREIGN_ENABLE_CNPG_PAIR stayed OFF. resolvePrimaryKubeconfigPath
	// recovers the path from the PVC at `<kubeconfigsDir>/<id>.yaml` (guarded
	// by os.Stat) WITHOUT mutating dep.Result — State() leaks the *Result
	// pointer to lock-free JSON marshals, so a stamp from this goroutine
	// would race them; the resolved value is threaded into buildRegionSlots
	// explicitly instead. Called OUTSIDE the lock below — it takes dep.mu
	// itself (sync.Mutex is not reentrant). The returned bool is intentionally
	// ignored here: a genuinely-lost file leaves primaryKubeconfigPath ""
	// and buildRegionSlots' own primary fallback + the existing
	// kubeconfig-path-empty error slot handle it (the startup-reconcile gate
	// shouldStartupClusterMeshReconcile already warn-and-skips that case).
	primaryKubeconfigPath, _ := h.resolvePrimaryKubeconfigPath(dep)

	dep.mu.Lock()
	regions := append([]provisioner.RegionSpec(nil), dep.Request.Regions...)
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
		// Single-region: no peer mesh, no cnpg-pair flip — vacuously
		// converged so the loop's single-region branch never blocks.
		return nil, true, nil
	}

	slots := h.buildRegionSlots(dep, regions, primaryKubeconfigPath, secondaryPaths)
	if len(slots) < 2 {
		h.log.Warn("clustermesh: fewer than 2 reachable regions, skipping",
			"id", dep.ID,
			"reachableRegions", len(slots),
		)
		// Multi-region request but <2 reachable regions — the mesh cannot
		// be established, so this is NOT a converged state; the loop's
		// len(statuses)>=2 guard also rejects it, but report false so the
		// flip-convergence signal is never misread as success.
		return nil, false, nil
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

	// ── #5230: EARLY shared-pg consumer-hub Secret sync (pre-mesh) ──────
	// Hoisted OUT of enableCNPGPairAfterFullMesh (where it sat behind the
	// countFullyMeshedRegions full-mesh gate): the #3629 consumer hub
	// Secrets (harbor-database-secret + siblings) are plain data copies
	// whose only real prerequisites are (a) region-A's authoritative hub
	// Secrets carrying the topology-aware `-mesh-rw` host and (b) the
	// replica region's apiserver being reachable — the mesh itself is NOT
	// required (the `-mesh-rw` host resolves on the replica from first
	// render via the replica-side stub Service). Keeping the sync behind
	// the flip left region-B's harbor-core in CreateContainerConfigError
	// (`secret "harbor-database-secret" not found`) for the whole
	// mesh-establishment window — ≈22m37s on hw274, recurring on every
	// fresh 2-region prov. Firing it here, BEFORE the up-to-5-min-per-
	// region LB polling below, lands the hubs as early as the reconcile
	// tick allows. Level-triggered + BEST-EFFORT like every #3629-family
	// sync: it skips quietly when a source hub is absent (shared-pg
	// disabled / consumer unconfigured / not yet minted), DEFERS any hub
	// whose host is not yet `-mesh-rw`, and tolerates a nil replica
	// clientset — so calling it on every pass (including passes where the
	// mesh cannot establish) is safe and the re-run converges whatever
	// this pass skipped. The flip-coupled replica-auth syncs
	// (syncCNPGPairReplicaAuthSecrets / syncSharedPGReplicaAuthSecrets)
	// deliberately stay inside enableCNPGPairAfterFullMesh — the replica
	// Cluster CRs they feed exist only post-flip.
	h.syncSharedPGConsumerHubSecrets(ctx, dep, slots)

	// Step 1: per-region LB IP discovery (poll up to 5 min each).
	for i := range slots {
		s := &slots[i]
		if s.err != nil {
			continue
		}
		lbIP, apiPort, err := h.waitForClusterMeshLB(ctx, s.clientset, s.provider)
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
		s.apiPort = apiPort
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: region %q apiserver LB ready at %s (dial port %d)", s.key, lbIP, apiPort))
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
			peerEntries[b.clusterName] = buildPeerConfigBlob(b.clusterName, b.apiPort)
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
		secretChanged, err := h.applyClusterMeshSecret(ctx, a.clientset, peerEntries)
		if err != nil {
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
		aliasesChanged, aliasErr := h.patchCiliumHostAliases(ctx, a.clientset, peers)
		if aliasErr != nil {
			h.log.Warn("clustermesh: hostAliases patch failed (continuing)",
				"id", dep.ID,
				"region", a.key,
				"err", aliasErr,
			)
		}

		// Trigger rollout-restart on cilium + cilium-operator +
		// clustermesh-apiserver in this region so they pick up the
		// new peer entries + hostAliases deterministically — but ONLY
		// when something actually changed (#3241 layer 4). The
		// level-triggered reconcile re-runs this every ~2 min; an
		// unconditional restart per pass crash-cycled the mesh
		// components (apiserver Deployment generation 35 on hw128) and
		// the agents never got a stable window to finish the
		// remote-config sync — the loop kept resetting the very state
		// it was waiting on. Best-effort: errors are logged, not fatal.
		if secretChanged || aliasesChanged {
			h.rolloutRestartClusterMeshTargets(ctx, dep, a)
		} else {
			h.log.Info("clustermesh: peer config unchanged — skipping rollout-restart (idempotent re-run)",
				"id", dep.ID,
				"region", a.key,
			)
		}

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
	// cnpgPairConverged is true only when the mesh is fully established
	// AND the slot-16b flip lands on every region. When the mesh is meshed
	// but the flip refuses (e.g. the primary postgres hasn't minted its
	// replica-auth Secrets yet — the hw126 22:38 state), this stays false
	// so the reconcile loop keeps retrying instead of stopping on
	// mesh-only readiness.
	cnpgPairConverged := false
	if totalReady == len(statuses) && len(statuses) >= 2 {
		cnpgPairConverged = h.enableCNPGPairAfterFullMesh(ctx, dep, slots)
	} else {
		h.log.Info("clustermesh: mesh not fully established — SOVEREIGN_ENABLE_CNPG_PAIR left untouched (slot 16b stays gated OFF)",
			"id", dep.ID,
			"fullyMeshed", totalReady,
			"regions", len(statuses),
		)
	}

	return statuses, cnpgPairConverged, nil
}

// buildPeerClusterMeshNamesValue returns the SOVEREIGN_PEER_CLUSTERMESH_NAMES
// substitute value for the region at slots[idx] — a YAML flow-sequence string
// listing EVERY OTHER region's Cilium cluster.name (the identity the
// cross-region DR CiliumNetworkPolicy matches via io.cilium.k8s.policy.cluster,
// #4846). Each slot's clusterName was already derived (buildRegionSlots) via
// provisioner.DeriveClusterMeshName (primary = `<label>-mesh`) /
// DeriveSecondaryClusterMeshName (`<stem>-<region-no-digits>`), so this reuses
// the SAME strings cilium-config carries on each cluster — the identity the
// remote pods actually present.
//
// Returns "[]" when there is no peer (single-region, or every other slot's
// clusterName is empty), so the chart renders NO CiliumNetworkPolicy. Emitted
// as a flow sequence (e.g. "[hw228-me-east-b]") so the slot's
// `crossRegionPeerClusters: ${SOVEREIGN_PEER_CLUSTERMESH_NAMES:-[]}` substitutes
// to a valid YAML list.
func buildPeerClusterMeshNamesValue(slots []regionSlot, idx int) string {
	peers := make([]string, 0, len(slots))
	for j := range slots {
		if j == idx {
			continue
		}
		name := strings.TrimSpace(slots[j].clusterName)
		if name != "" {
			peers = append(peers, name)
		}
	}
	return "[" + strings.Join(peers, ",") + "]"
}

// enableCNPGPairAfterFullMesh flips the slot-16b gate on EVERY
// region's bootstrap-kit Flux Kustomization (the one cloud-init applies
// — infra/providers/_shared/cloudinit-control-plane.tftpl) by merging
// `spec.postBuild.substitute.SOVEREIGN_ENABLE_CNPG_PAIR: "true"` and
// stamping `reconcile.fluxcd.io/requestedAt` so Flux reconciles
// immediately instead of waiting out the 5m interval. The cross-region
// CNPG pair (clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml)
// then deploys zero-touch, correctly gated on CONFIRMED mesh readiness.
//
// ALL-REGIONS scope (chart 0.2.0 split-side topology): slot 16b is no
// longer a primary-only HR — it applies on every control plane with
// `cnpgPair.side: ${SOVEREIGN_REGION_ROLE:-primary}`, so side=primary
// renders the primary Cluster + mesh Service on cluster-A and
// side=replica renders the replica Cluster + failover-readiness probe
// on cluster-B. Flipping only the primary's Kustomization (the pre-
// split behaviour) would activate the primary half while the secondary
// keeps rendering an empty release — no replica, no WAL stream. The
// regionSlots passed in carry each region's kubeconfig path (on the
// full-success path that triggers this flip, every slot is reachable).
//
// Defense (non-negotiable, per the chart's required-fail-fast): the
// flip is REFUSED — for ALL regions, atomically — unless every
// region's substitute map carries non-empty, DISTINCT
// SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION (cloud-init
// stamps both on every control plane of a 2-region prov). With
// enabled=true and a missing/equal region the bp-cnpg-pair chart
// render fails, and a render failure inside the bootstrap-kit
// Kustomization fails the WHOLE atomic apply → 0 HRs (the #2981/#2982
// fresh-prov wedge shape). Likewise, if ANY region's Kustomization is
// unreadable the whole flip is refused — atomically, before ANY patch.
//
// Patch ordering is TWO-STAGE (#3241 first-flip deadlock): the primary
// side flips unconditionally; the replica side flips only after the
// primary's replica-auth Secrets exist + sync (#3254). primary-ON /
// replica-pending is the INTENDED intermediate of a first flip — the
// primary postgres must initdb before CNPG can mint the Secrets the
// replica streams with. The pre-#3241 shape (sync gates ALL patches)
// deadlocked the first flip on a fresh env: the Secrets could never
// appear while the gate stayed OFF everywhere (hw128 live). A refusal
// at either stage logs loudly and returns NOT-converged; the
// level-triggered reconcile / post-handover finalisation re-runs and
// converges.
//
// Scope: ONLY SOVEREIGN_ENABLE_CNPG_PAIR. The shared-pg flags
// (SOVEREIGN_ENABLE_SHARED_PG / *_PG_OWN_CLUSTER) are #3188 scope and
// are deliberately not touched here.
//
// Failure modes follow this file's convention: every failure is logged
// + surfaced as a clustermesh-progress warn event, never an error —
// the post-handover finalisation re-runs AutoEstablishClusterMesh and
// the flip converges then. A patch failure AFTER the gather phase is
// per-region (warn + continue): the merge patch is idempotent and the
// re-run converges the missed region.
//
// Returns true ONLY when the flip fully landed (every region's
// bootstrap-kit Kustomization patched ON). Any refusal/failure returns
// false so the #3241 level-trigger keeps retrying: on hw126 the mesh was
// fully established at 22:38 but the replica-auth Secrets had not been
// minted yet, so this function correctly refused — yet the reconcile
// loop treated "mesh meshed" as converged and STOPPED, leaving the flip
// permanently unapplied until a manual catalyst-api restart. Threading
// the outcome out lets runAutoEstablishClusterMesh keep the deployment
// NOT-converged until the flip lands.
func (h *Handler) enableCNPGPairAfterFullMesh(ctx context.Context, dep *Deployment, slots []regionSlot) bool {
	if len(slots) == 0 {
		h.log.Warn("clustermesh: cnpg-pair gate: no region slots — cannot reach any bootstrap-kit Kustomization",
			"id", dep.ID)
		return false
	}

	// ── Gather phase (all-or-nothing) ───────────────────────────────
	// Build a dynamic client + read the bootstrap-kit Kustomization in
	// EVERY region and verify the region-substitute precondition before
	// patching anything. Any gap refuses the whole flip — a half-
	// flipped split-side pair is the hw126 broken topology.
	type flipTarget struct {
		regionKey   string
		dyn         dynamic.Interface
		primarySide bool
		// sharedPGEnabled — the region's SOVEREIGN_ENABLE_SHARED_PG
		// substitute (#3571). When true the bp-postgres shared instances
		// (shared-pg/-b/-c) ALSO render the cnpg-pair split-side shape on
		// this crossRegion flip, so their `<instance>-replication`/`-ca`
		// Secrets must be synced primary → replica alongside bp-cnpg-pair's.
		sharedPGEnabled bool
		// peerClusterMeshNames (#4846) — the OTHER region(s)' Cilium
		// cluster.name(s) as a YAML flow-sequence string, stamped onto THIS
		// region's SOVEREIGN_PEER_CLUSTERMESH_NAMES substitute so the
		// bp-postgres / bp-cnpg-pair cross-region DR CiliumNetworkPolicy
		// admits the ClusterMesh remote replica by identity. Derived from the
		// slots' already-computed clusterName (buildPeerClusterMeshNamesValue).
		peerClusterMeshNames string
	}
	targets := make([]flipTarget, 0, len(slots))
	for i := range slots {
		s := &slots[i]
		regionLabel := s.key
		if regionLabel == "" {
			regionLabel = "primary"
		}
		if s.kubeconfigPath == "" {
			h.log.Warn("clustermesh: cnpg-pair gate: region kubeconfig path empty — cannot reach its bootstrap-kit Kustomization",
				"id", dep.ID, "region", regionLabel)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable skipped — region %q kubeconfig path empty; re-run converges", regionLabel))
			return false
		}
		dyn, err := h.clusterMeshDynamicClient(s.kubeconfigPath)
		if err != nil {
			h.log.Warn("clustermesh: cnpg-pair gate: dynamic client build failed",
				"id", dep.ID, "region", regionLabel, "err", err)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable skipped — region %q dynamic client build failed (%v); re-run converges", regionLabel, err))
			return false
		}

		getCtx, cancelGet := context.WithTimeout(ctx, clusterMeshCallTimeout)
		ks, err := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
			Get(getCtx, bootstrapKitKustomizationName, metav1.GetOptions{})
		cancelGet()
		if err != nil {
			h.log.Warn("clustermesh: cnpg-pair gate: Get bootstrap-kit Kustomization failed",
				"id", dep.ID, "region", regionLabel, "err", err)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable skipped — region %q Get Kustomization %s/%s failed (%v); re-run converges",
					regionLabel, fluxSystemNamespace, bootstrapKitKustomizationName, err))
			return false
		}

		substitute, found, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if err != nil || !found {
			h.log.Warn("clustermesh: refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR — bootstrap-kit Kustomization has no readable spec.postBuild.substitute map",
				"id", dep.ID, "region", regionLabel, "found", found, "err", err)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable refused — region %q bootstrap-kit Kustomization carries no postBuild.substitute map", regionLabel))
			return false
		}
		primaryRegion := strings.TrimSpace(substitute[clusterMeshPrimaryRegionSubstituteKey])
		replicaRegion := strings.TrimSpace(substitute[clusterMeshReplicaRegionSubstituteKey])
		if primaryRegion == "" || replicaRegion == "" || primaryRegion == replicaRegion {
			h.log.Warn("clustermesh: refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR — substitute region precondition failed (bp-cnpg-pair `required`s distinct non-empty regions; a render failure would fail the whole atomic bootstrap-kit apply → 0 HRs)",
				"id", dep.ID,
				"region", regionLabel,
				"primaryRegion", primaryRegion,
				"replicaRegion", replicaRegion,
			)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable REFUSED — region %q SOVEREIGN_PRIMARY_REGION=%q / SOVEREIGN_REPLICA_REGION=%q must be non-empty and distinct in the bootstrap-kit substitute map",
					regionLabel, primaryRegion, replicaRegion))
			return false
		}
		// Classify the region's SIDE for the two-stage patch below. The
		// region's own role substitute is authoritative (cloud-init stamps
		// it; the chart's `cnpgPair.side` keys off the same value); when
		// absent fall back to the deployment model invariant — slot 0 IS
		// the primary region (mirrors the chart's `:-primary` default).
		role := strings.TrimSpace(substitute[clusterMeshRegionRoleSubstituteKey])
		if role == "" {
			if i == 0 {
				role = "primary"
			} else {
				role = "secondary"
			}
		}
		sharedPGEnabled := strings.EqualFold(strings.TrimSpace(substitute[clusterMeshSharedPGSubstituteKey]), "true")
		targets = append(targets, flipTarget{
			regionKey:            regionLabel,
			dyn:                  dyn,
			primarySide:          role == "primary",
			sharedPGEnabled:      sharedPGEnabled,
			peerClusterMeshNames: buildPeerClusterMeshNamesValue(slots, i),
		})
	}

	// ── Patch phase: TWO-STAGE (#3241 first-flip deadlock) ──────────
	// The primary-side Cluster CR has NO dependency on the replica-auth
	// Secrets — but the Secrets are minted by CNPG only AFTER the
	// primary postgres initdb's, and the primary postgres only renders
	// once its region's gate flips ON. Gating ALL patches on the Secret
	// sync (the pre-#3241 shape) therefore DEADLOCKED the FIRST flip on
	// a fresh env: hw128 sat with the level-trigger refusing forever
	// ("source Secret unavailable on primary — refusing flip") while
	// cnpg stayed empty in both regions. hw126 never hit this because
	// its gate was already ON before the sync landed (#3254).
	//
	// Stage 1 flips every PRIMARY-side region unconditionally. Stage 2
	// (replica-side regions) stays gated on the replica-auth sync — a
	// half state there (gate flipped, auth Secrets missing) would
	// render a replica Cluster waiting forever on a secret it cannot
	// find. A Stage-2 refusal returns false so the level-trigger
	// re-runs: the primary initdb proceeds meanwhile, the Secrets
	// appear, the idempotent re-run syncs + flips the replicas.
	// JSON merge patch (RFC 7386) merges nested objects key-by-key, so
	// sibling substitute keys + existing annotations are preserved —
	// only the gate key and the reconcile-request stamp change.
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	// Per-region patch: SOVEREIGN_ENABLE_CNPG_PAIR is identical for every
	// region, but SOVEREIGN_PEER_CLUSTERMESH_NAMES (#4846) differs — each
	// region's substitute must carry the OTHER region(s)' Cilium cluster.name,
	// so the cross-region DR CiliumNetworkPolicy admits the correct remote
	// peer by identity. Both keys land in ONE merge patch so the CNP appears
	// atomically with the crossRegion netpol half it gates. The JSON merge
	// patch (RFC 7386) merges nested objects key-by-key, so sibling substitute
	// keys + existing annotations are preserved.
	patchOne := func(t flipTarget) bool {
		substitute := map[string]any{
			clusterMeshCNPGPairSubstituteKey: "true",
		}
		if t.peerClusterMeshNames != "" {
			substitute[clusterMeshPeerClusterMeshNamesSubstituteKey] = t.peerClusterMeshNames
		}
		patch := map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{
					fluxReconcileRequestedAtAnnotation: stamp,
				},
			},
			"spec": map[string]any{
				"postBuild": map[string]any{
					"substitute": substitute,
				},
			},
		}
		patchBytes, err := json.Marshal(patch)
		if err != nil {
			h.log.Warn("clustermesh: cnpg-pair gate: marshal merge patch failed",
				"id", dep.ID, "region", t.regionKey, "err", err)
			return false
		}
		patchCtx, cancelPatch := context.WithTimeout(ctx, clusterMeshCallTimeout)
		_, patchErr := t.dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
			Patch(patchCtx, bootstrapKitKustomizationName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		cancelPatch()
		if patchErr != nil {
			h.log.Warn("clustermesh: cnpg-pair gate: Patch bootstrap-kit Kustomization failed",
				"id", dep.ID, "region", t.regionKey, "err", patchErr)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable incomplete — region %q Patch Kustomization %s/%s (%v); re-run converges",
					t.regionKey, fluxSystemNamespace, bootstrapKitKustomizationName, patchErr))
			return false
		}
		return true
	}

	primaryTotal, replicaTotal := 0, 0
	for _, t := range targets {
		if t.primarySide {
			primaryTotal++
		} else {
			replicaTotal++
		}
	}

	// Stage 1 — primary-side regions, unconditional.
	patched := 0
	for _, t := range targets {
		if t.primarySide && patchOne(t) {
			patched++
		}
	}
	if patched < primaryTotal {
		return false
	}

	// Stage 2 — replica-side regions, gated on the replica-auth sync
	// (#3254). No replica-side regions → nothing to gate.
	if replicaTotal > 0 {
		if !h.syncCNPGPairReplicaAuthSecrets(ctx, dep, slots) {
			h.log.Info("clustermesh: cnpg-pair two-stage flip — primary side ON, replica side awaiting the primary's replica-auth Secrets (initdb in progress); level-trigger re-run converges",
				"id", dep.ID,
				"primaryRegions", primaryTotal,
				"replicaRegions", replicaTotal,
			)
			h.emitClusterMeshProgress(dep, "info",
				fmt.Sprintf("ClusterMesh: cnpg-pair primary side enabled (%d region(s)); replica side waits for the primary postgres to mint its replication Secrets — re-run converges", primaryTotal))
			return false
		}
		// #3571: the bp-postgres SHARED instances (shared-pg/-b/-c) ride the
		// SAME crossRegion flip, so their replicas also need the primary's
		// `<instance>-replication`/`-ca` Secrets on the replica cluster. Sync
		// them too — but ONLY when shared-pg is enabled (else those Secrets
		// never exist and would falsely block the cnpg-pair flip). The
		// primary region's substitute is authoritative for the gate.
		sharedPGOn := false
		for _, t := range targets {
			if t.primarySide && t.sharedPGEnabled {
				sharedPGOn = true
				break
			}
		}
		if sharedPGOn && !h.syncSharedPGReplicaAuthSecrets(ctx, dep, slots) {
			h.log.Info("clustermesh: shared-pg two-stage flip — cnpg-pair replica-auth synced, shared-pg instances awaiting their primary replica-auth Secrets (initdb in progress); level-trigger re-run converges",
				"id", dep.ID,
				"primaryRegions", primaryTotal,
				"replicaRegions", replicaTotal,
			)
			h.emitClusterMeshProgress(dep, "info",
				fmt.Sprintf("ClusterMesh: shared-pg cross-region replicas wait for the shared engines' primary postgres to mint their replication Secrets (%d instance(s)) — re-run converges", len(sharedPGInstanceNames)))
			return false
		}
		for _, t := range targets {
			if !t.primarySide && patchOne(t) {
				patched++
			}
		}
		// #3629 → #5230: the consumer-hub Secret sync
		// (syncSharedPGConsumerHubSecrets) is NO LONGER invoked here — it is
		// hoisted into the early per-slot phase of autoEstablishClusterMesh
		// (which precedes EVERY invocation of this function in the same
		// reconcile pass), so the hubs land on the replica as soon as
		// region-A's authoritative `-mesh-rw` Secrets + the replica clientset
		// exist instead of waiting out the full-mesh flip (the ~22-min
		// region-B harbor CreateContainerConfigError window on every fresh
		// 2-region prov, hw274).
		if sharedPGOn {
			// #4158 (best-effort, never blocks): one layer above the #3629
			// consumer-hub DB secrets — region-B's keycloak boots against the
			// cross-region-replicated shared-pg catalog (so its admin password
			// hash = region-A's), but its config-cli/kcadm read region-B's
			// DIVERGENT local admin Secret → HTTP 401 → realm import never runs →
			// the bp-keycloak HR post-upgrade hook times out → the whole
			// mgmt-vCluster SSO tier stays Ready=False. Copy region-A's
			// authoritative keycloak admin Secret primary → replica so the
			// replica authenticates against its own (replicated) keycloak. NOT
			// gated on `-mesh-rw` (no DB host on this Secret); a too-early pass
			// (region-A keycloak not yet installed) is a harmless skip and the
			// level-trigger re-run converges it. NEVER gates the flip.
			h.syncKeycloakAdminSecret(ctx, dep, slots)
			// #4158 (best-effort, never blocks): the SSO layer above the #4159
			// DB-secret + #4162 admin-secret fixes — region-B's per-app SSO
			// `ExternalSecret`s (grafana/harbor/openbao/…) 403 against region-A's
			// in-vc-mgmt OpenBao (cross-cluster TokenReview trusts only region-A's
			// host apiserver), so their mangled host `mgmt` Secret never
			// materialises and the in-vc-mgmt apps wedge at
			// CreateContainerConfigError (`secret "<app>-sso-oidc-credentials-x-…"
			// not found`). Copy region-A's RESOLVED mangled SSO Secrets primary →
			// replica so the replica apps mount the credential directly. NOT gated
			// on `-mesh-rw` (no DB host); a too-early pass (region-A ESO not yet
			// resolved) is a harmless skip and the level-trigger re-run converges
			// it. NEVER gates the flip.
			h.syncSSOOIDCMangledSecrets(ctx, dep, slots)
			// #4436 (best-effort, never blocks): keycloak/gitea/harbor run on
			// EVERY region's control plane and dial the shared-pg WRITE host as a
			// scalar (bitnami externalDatabase.host / a plain env value). The
			// region-local `shared-pg-rw` Service exists ONLY in region-A, so a
			// SECONDARY region that dials it NXDOMAINs (`cannot resolve host
			// shared-pg-rw…` — keycloak-0 CrashLoop, live on dep 4635277cae4ffed9
			// region-B). cloud-init #4159 already renders these as `-mesh-rw` on
			// the secondary for FRESH provs, but the substitute map is baked at
			// boot and never re-patched → PRE-#4159 (stale) 2-region envs keep the
			// region-local `-rw` host (keycloak omits the key entirely → the slot
			// default `:=shared-pg-rw` wins). Re-stamp the secondary regions'
			// substitute maps to the ClusterMesh-global `shared-pg-mesh-rw` WRITE
			// alias so stale envs self-heal AND fresh provs stay idempotent. NEVER
			// gates the flip.
			h.patchSecondaryCrossRegionPGHosts(ctx, dep, slots)
		}
	}
	if patched == 0 {
		return false
	}

	h.log.Info("clustermesh: SOVEREIGN_ENABLE_CNPG_PAIR=true merged onto bootstrap-kit Kustomizations + Flux reconcile requested (split-side: every region renders its own half of the pair)",
		"id", dep.ID,
		"regionsPatched", patched,
		"regionsTotal", len(targets),
		"requestedAt", stamp,
	)
	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh confirmed across all regions — enabled bp-cnpg-pair (slot 16b) via SOVEREIGN_ENABLE_CNPG_PAIR=true on %d/%d region Kustomizations and requested Flux reconcile", patched, len(targets)))

	// Fully landed ONLY when EVERY region's Kustomization patched ON. A
	// partial patch (some regions failed mid-loop) is a half-flipped
	// split-side pair — exactly the broken topology the gather phase
	// guards against — so report NOT-converged and let the level-trigger
	// re-run finish the remaining regions (the merge patch is idempotent).
	return patched == len(targets)
}

// syncCNPGPairReplicaAuthSecrets copies the primary cluster's CNPG
// replication-auth Secrets (cnpgPairReplicaAuthSecrets, namespace
// cnpgPairNamespace) onto EVERY replica region's cluster, so the
// replica-side bp-cnpg-pair Cluster CR can authenticate its
// pg_basebackup/WAL stream against the primary (the chart references
// these Secrets by name from `externalClusters[].sslKey`/`sslCert`/
// `sslRootCert` — see #3254). Returns true when the whole pair-side is
// satisfied (single-region — nothing to sync, vacuously true; OR every
// replica got every Secret), false to REFUSE the flip.
//
// All-or-nothing, matching the gather phase: a missing SOURCE Secret on
// the primary (the primary postgres is still bootstrapping — CNPG mints
// `-replication`/`-ca` only after initdb) or ANY per-replica copy
// failure refuses the entire flip and leaves the gate OFF everywhere
// (safe: empty Ready releases). The #3241 level-trigger + post-handover
// finalisation re-run AutoEstablishClusterMesh and converge once the
// primary's Secrets exist. Every refusal logs loudly AND emits a
// clustermesh-progress warn event, per this file's failure convention.
//
// slots[0] is the primary (regionKeyFromSpec returns "" for idx 0);
// slots[1:] are the replica regions. Each slot's typed clientset was
// built in buildRegionSlots; on the full-success path that gates this
// flip, every slot is reachable.
func (h *Handler) syncCNPGPairReplicaAuthSecrets(ctx context.Context, dep *Deployment, slots []regionSlot) bool {
	if len(slots) < 2 {
		// No replica region — nothing to sync. (AutoEstablishClusterMesh
		// only calls the flip on len(statuses) >= 2, so this is a guard,
		// not the live path.)
		return true
	}
	primary := &slots[0]
	if primary.clientset == nil {
		h.log.Warn("clustermesh: cnpg-pair replica-auth sync: primary clientset nil — refusing flip",
			"id", dep.ID)
		h.emitClusterMeshProgress(dep, "warn",
			"ClusterMesh: cnpg-pair enable refused — primary cluster client unavailable for replica-auth Secret sync; re-run converges")
		return false
	}

	// Read the source Secrets from the primary ONCE. A missing source =
	// the primary postgres has not initialised yet → refuse + converge
	// on a later run.
	sources := make([]*corev1.Secret, 0, len(cnpgPairReplicaAuthSecrets))
	for _, name := range cnpgPairReplicaAuthSecrets {
		getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		src, err := primary.clientset.CoreV1().Secrets(cnpgPairNamespace).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		if err != nil {
			h.log.Warn("clustermesh: cnpg-pair replica-auth sync: source Secret unavailable on primary — refusing flip (primary postgres likely still bootstrapping)",
				"id", dep.ID, "namespace", cnpgPairNamespace, "secret", name, "err", err)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable refused — primary Secret %s/%s not ready (%v); the replica needs it for WAL-stream auth — re-run converges once the primary postgres initialises",
					cnpgPairNamespace, name, err))
			return false
		}
		sources = append(sources, src)
	}

	// Copy every source Secret onto every replica region. Any failure
	// refuses the whole flip (no partial state).
	for i := 1; i < len(slots); i++ {
		replica := &slots[i]
		regionLabel := replica.key
		if regionLabel == "" {
			regionLabel = "secondary"
		}
		if replica.clientset == nil {
			h.log.Warn("clustermesh: cnpg-pair replica-auth sync: replica clientset nil — refusing flip",
				"id", dep.ID, "region", regionLabel)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: cnpg-pair enable refused — replica region %q cluster client unavailable for replica-auth Secret sync; re-run converges", regionLabel))
			return false
		}
		for _, src := range sources {
			if _, err := h.copySecretAcrossClusters(ctx, src, replica.clientset, cnpgPairNamespace); err != nil {
				h.log.Warn("clustermesh: cnpg-pair replica-auth sync: copy to replica failed — refusing flip",
					"id", dep.ID, "region", regionLabel, "namespace", cnpgPairNamespace, "secret", src.Name, "err", err)
				h.emitClusterMeshProgress(dep, "warn",
					fmt.Sprintf("ClusterMesh: cnpg-pair enable refused — copying Secret %s/%s to replica region %q failed (%v); re-run converges",
						cnpgPairNamespace, src.Name, regionLabel, err))
				return false
			}
		}
		h.log.Info("clustermesh: cnpg-pair replica-auth Secrets synced to replica region",
			"id", dep.ID, "region", regionLabel, "namespace", cnpgPairNamespace, "secrets", cnpgPairReplicaAuthSecrets)
	}

	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh: synced cnpg-pair replica-auth Secrets (%s) primary → %d replica region(s) for WAL-stream auth",
			strings.Join(cnpgPairReplicaAuthSecrets, ", "), len(slots)-1))
	return true
}

// syncSharedPGReplicaAuthSecrets copies the primary cluster's CNPG
// replication-auth Secrets for the 3 SHARED data instances
// (sharedPGReplicaAuthSecrets, namespace sharedPGNamespace=shared-data) onto
// EVERY replica region's cluster, so each shared instance's replica-side
// Cluster CR (bp-postgres 0.2.0 split-side, #3571) can authenticate its
// pg_basebackup/WAL stream against its primary. Same contract +
// all-or-nothing semantics as syncCNPGPairReplicaAuthSecrets (the bp-cnpg-pair
// analogue), only the namespace + Secret-name set differ.
//
// Called ONLY when shared-pg is enabled (the caller gates on the primary
// region's SOVEREIGN_ENABLE_SHARED_PG substitute) — when OFF the shared
// instances render empty releases, CNPG never mints these Secrets, and this
// function is never reached, so a non-shared-pg Sovereign's cnpg-pair flip is
// never blocked by absent shared-pg Secrets.
//
// A missing SOURCE Secret on the primary (the shared engine's postgres is
// still bootstrapping — CNPG mints `<instance>-replication`/`-ca` only after
// initdb) or ANY per-replica copy failure refuses the whole flip and leaves
// the gate OFF everywhere (safe: empty Ready releases). The #3241
// level-trigger + post-handover finalisation re-run AutoEstablishClusterMesh
// and converge once the primaries' Secrets exist.
func (h *Handler) syncSharedPGReplicaAuthSecrets(ctx context.Context, dep *Deployment, slots []regionSlot) bool {
	if len(slots) < 2 {
		return true // no replica region — nothing to sync (guard, not live path)
	}
	primary := &slots[0]
	if primary.clientset == nil {
		h.log.Warn("clustermesh: shared-pg replica-auth sync: primary clientset nil — refusing flip",
			"id", dep.ID)
		h.emitClusterMeshProgress(dep, "warn",
			"ClusterMesh: shared-pg cross-region enable refused — primary cluster client unavailable for replica-auth Secret sync; re-run converges")
		return false
	}

	// Read the source Secrets from the primary ONCE. A missing source = the
	// shared engine's postgres has not initialised yet → refuse + converge.
	sources := make([]*corev1.Secret, 0, len(sharedPGReplicaAuthSecrets))
	for _, name := range sharedPGReplicaAuthSecrets {
		getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		src, err := primary.clientset.CoreV1().Secrets(sharedPGNamespace).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		if err != nil {
			h.log.Warn("clustermesh: shared-pg replica-auth sync: source Secret unavailable on primary — refusing flip (shared engine postgres likely still bootstrapping)",
				"id", dep.ID, "namespace", sharedPGNamespace, "secret", name, "err", err)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: shared-pg cross-region enable refused — primary Secret %s/%s not ready (%v); the replica needs it for WAL-stream auth — re-run converges once the shared engine postgres initialises",
					sharedPGNamespace, name, err))
			return false
		}
		sources = append(sources, src)
	}

	// Copy every source Secret onto every replica region. Any failure refuses
	// the whole flip (no partial state).
	for i := 1; i < len(slots); i++ {
		replica := &slots[i]
		regionLabel := replica.key
		if regionLabel == "" {
			regionLabel = "secondary"
		}
		if replica.clientset == nil {
			h.log.Warn("clustermesh: shared-pg replica-auth sync: replica clientset nil — refusing flip",
				"id", dep.ID, "region", regionLabel)
			h.emitClusterMeshProgress(dep, "warn",
				fmt.Sprintf("ClusterMesh: shared-pg cross-region enable refused — replica region %q cluster client unavailable for replica-auth Secret sync; re-run converges", regionLabel))
			return false
		}
		for _, src := range sources {
			if _, err := h.copySecretAcrossClusters(ctx, src, replica.clientset, sharedPGNamespace); err != nil {
				h.log.Warn("clustermesh: shared-pg replica-auth sync: copy to replica failed — refusing flip",
					"id", dep.ID, "region", regionLabel, "namespace", sharedPGNamespace, "secret", src.Name, "err", err)
				h.emitClusterMeshProgress(dep, "warn",
					fmt.Sprintf("ClusterMesh: shared-pg cross-region enable refused — copying Secret %s/%s to replica region %q failed (%v); re-run converges",
						sharedPGNamespace, src.Name, regionLabel, err))
				return false
			}
		}
		h.log.Info("clustermesh: shared-pg replica-auth Secrets synced to replica region",
			"id", dep.ID, "region", regionLabel, "namespace", sharedPGNamespace, "secrets", sharedPGReplicaAuthSecrets)
	}

	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh: synced shared-pg replica-auth Secrets (%s) primary → %d replica region(s) for WAL-stream auth",
			strings.Join(sharedPGReplicaAuthSecrets, ", "), len(slots)-1))
	return true
}

// syncSharedPGConsumerHubSecrets copies the primary cluster's per-consumer HUB
// CONNECTION Secrets (sharedPGConsumerHubSecrets, namespace shared-data) onto
// every replica region's cluster so the cross-region consumer apps (grafana,
// powerdns-admin, …) read the AUTHORITATIVE region-A password + the
// topology-aware `<instance>-mesh-rw` write host instead of the divergent,
// region-LOCAL hub Secret each region minted during its own singleton phase
// (#3629 — see the sharedPGConsumerHubSecrets doc for the full divergence
// analysis + the hw147 region-B evidence). Once copied into the replica's
// shared-data, the replica's own emberstack reflector re-pushes the corrected
// Secret into each consumer namespace (the source carries the reflection-auto
// annotations), overwriting the stale copy.
//
// BEST-EFFORT — UNLIKE the replica-auth sync this NEVER blocks/refuses the
// cnpg-pair flip: the hub Secrets are not needed for the WAL stream, only for
// the consumers, so a missing/not-yet-`-mesh-rw` source or a per-replica copy
// failure is logged and SKIPPED, and the #3241/#3583 level-trigger re-run
// converges it on a later pass. It returns nothing (the caller ignores the
// outcome) precisely so it can never wedge convergence.
//
// CALL SITE (#5230): invoked from the EARLY per-slot phase of
// autoEstablishClusterMesh — BEFORE LB discovery, on every level-trigger pass
// — NOT from enableCNPGPairAfterFullMesh. The hubs need neither the mesh nor
// the flip (plain data copies; the `-mesh-rw` host resolves on the replica
// from first render via the stub Service), and gating them on the full-mesh
// flip cost region-B's harbor-core a ≈22-min CreateContainerConfigError
// window on every fresh 2-region prov (hw274). The internal skips above make
// arbitrarily-early invocation safe: a pass before region-A minted its hubs
// (or on a shared-pg-disabled Sovereign, where they never exist) skips every
// name quietly.
//
// READINESS GATE: a hub Secret is propagated ONLY once its `host` key carries
// the `-mesh-rw` marker — i.e. region-A has reconciled crossRegion=true and its
// bp-postgres slot upgrade has rendered the topology-aware host. Before that the
// source still carries the region-LOCAL `<instance>-rw` (which NXDOMAINs on the
// replica), so pushing it would make things WORSE; skipping waits for the next
// pass. Single-region (len(slots)<2) is a no-op.
func (h *Handler) syncSharedPGConsumerHubSecrets(ctx context.Context, dep *Deployment, slots []regionSlot) {
	if len(slots) < 2 {
		return // single-region — no replica to sync to
	}
	primary := &slots[0]
	if primary.clientset == nil {
		h.log.Warn("clustermesh: shared-pg consumer-hub sync: primary clientset nil — skipping (best-effort, re-run converges)",
			"id", dep.ID)
		return
	}

	synced := make([]string, 0, len(sharedPGConsumerHubSecrets))
	for _, name := range sharedPGConsumerHubSecrets {
		getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		src, err := primary.clientset.CoreV1().Secrets(sharedPGNamespace).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		if err != nil {
			// A consumer that isn't wired on this Sovereign (its slot binding
			// is absent) simply has no hub Secret — skip quietly. Best-effort.
			h.log.Info("clustermesh: shared-pg consumer-hub sync: source hub Secret not present on primary — skipping (consumer may be unconfigured; best-effort)",
				"id", dep.ID, "namespace", sharedPGNamespace, "secret", name)
			continue
		}
		// READINESS GATE: only propagate once region-A has rendered the
		// `-mesh-rw` write host. A `-rw`-only host means region-A hasn't
		// reconciled crossRegion yet — pushing it would NXDOMAIN on the
		// replica, so wait for the next level-trigger pass.
		if host := string(src.Data["host"]); !strings.Contains(host, sharedPGMeshRWHostMarker) {
			h.log.Info("clustermesh: shared-pg consumer-hub sync: source host not yet topology-aware (-mesh-rw) — deferring (region-A crossRegion upgrade still landing; best-effort, re-run converges)",
				"id", dep.ID, "secret", name, "host", host)
			continue
		}
		copied := true
		for i := 1; i < len(slots); i++ {
			replica := &slots[i]
			if replica.clientset == nil {
				copied = false
				continue
			}
			if _, err := h.copySecretAcrossClusters(ctx, src, replica.clientset, sharedPGNamespace); err != nil {
				h.log.Warn("clustermesh: shared-pg consumer-hub sync: copy to replica failed — skipping this Secret/region (best-effort, re-run converges)",
					"id", dep.ID, "region", replica.key, "namespace", sharedPGNamespace, "secret", name, "err", err)
				copied = false
				continue
			}
			// #4878 (+ residual, live hw232): after we OVERWRITE the replica's
			// divergent shared-data hub Secret with region-A's authoritative one,
			// the LONG-RUNNING consumer pod (keycloak/gitea/harbor) is still pinned
			// to the STALE password it cached in-process at boot and never re-reads
			// the remounted Secret on its own → repeated
			// `FATAL: password authentication failed`. It must be rolled to pick up
			// the corrected credential. BUT firing the restart the instant the hub
			// Secret changes is PREMATURE: the emberstack reflector has not yet
			// re-pushed region-A's bytes from shared-data into the consumer's OWN
			// namespace, so the fresh pod re-reads the stale mounted Secret and
			// crashloops until kubelet retries happen to catch the propagated value
			// (hw232: keycloak-0 crashlooped 5× 06:43:50→06:47:23Z). Instead we
			// reconcile the restart against CREDENTIAL CONSISTENCY every
			// level-trigger pass — restart ONLY once the consumer-namespace Secret
			// already carries region-A's bytes, and only ONCE per credential value
			// (idempotent; a steady-state pass never restarts, honouring the
			// #3241/#3583 no-thrash contract). `changed` no longer gates it; the
			// level-trigger re-run drives it to consistency.
			if target, ok := sharedPGConsumerRestartTargets[name]; ok {
				h.reconcileSharedPGConsumerRestart(ctx, dep, replica, name, target, src)
			}
		}
		if copied {
			synced = append(synced, name)
		}
	}

	if len(synced) > 0 {
		h.log.Info("clustermesh: shared-pg consumer-hub Secrets synced primary → replica region(s) (#3629 cross-region consumer DB host/password)",
			"id", dep.ID, "namespace", sharedPGNamespace, "secrets", synced, "replicaRegions", len(slots)-1)
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: synced %d shared-pg consumer-hub Secret(s) (%s) primary → %d replica region(s) — cross-region consumer DB host/password (#3629)",
				len(synced), strings.Join(synced, ", "), len(slots)-1))
	}
}

// patchSecondaryCrossRegionPGHosts re-stamps the cross-region shared-pg WRITE
// host onto every SECONDARY region's bootstrap-kit Kustomization substitute map
// (#4436, Refs #4159). keycloak/gitea/harbor run on every region's control
// plane and dial the write host as a SCALAR (bitnami externalDatabase.host / a
// plain env value — NOT a secretKeyRef, so they cannot pick up the topology-
// aware host the hub Secret carries the way grafana's hostPortKey does). The
// region-local `shared-pg-rw` Service exists ONLY where the primary Cluster runs
// (region-A), so a SECONDARY control plane that dials it gets NXDOMAIN (measured
// live on dep 4635277cae4ffed9 region-B: keycloak-0 CrashLoop `cannot resolve
// host shared-pg-rw…`). The ClusterMesh-global WRITE alias `shared-pg-mesh-rw`
// (bp-postgres publishes it on the primary as a `service.cilium.io/global`
// managed Service + a same-named replica stub) resolves in BOTH regions and
// routes writes to the CURRENT primary — and bp-continuum re-homes the `rw`
// selector on failover, so the host needs no change on promotion.
//
// cloud-init #4159 already renders these keys as `-mesh-rw` on the secondary for
// FRESH provs, but the substitute map is baked at boot and NEVER re-patched, so
// PRE-#4159 (stale) 2-region envs keep the region-local `-rw` host — or omit the
// keycloak key entirely, in which case the slot 09 default `:=shared-pg-rw`
// wins. Re-stamping at the post-mesh gate self-heals those stale envs AND is a
// belt-and-suspenders idempotent no-op on fresh provs (the merge patch only
// fires when a key is missing or still carries the region-local host).
//
// BEST-EFFORT — like syncSharedPGConsumerHubSecrets / syncKeycloakAdminSecret
// this NEVER blocks/refuses the cnpg-pair flip: a Get/Patch failure or a region
// whose substitute map already carries `-mesh-rw` is logged/skipped, and the
// #3241/#3583 level-trigger re-run converges it on a later pass. It returns
// nothing precisely so it can never wedge convergence.
//
// Scope guard: ONLY secondary regions whose substitute map carries
// SOVEREIGN_ENABLE_SHARED_PG=true are patched. The PRIMARY region keeps its
// region-local `shared-pg-rw` (its `-rw` Service is local and resolves — flipping
// it to `-mesh-rw` would be a needless indirection), and an own-cluster
// (non-shared) Sovereign is left entirely untouched (its keycloak/gitea/harbor
// dial their OWN per-app `<app>-pg-rw` host, never shared-pg). Single-region
// (len(slots)<2) is a no-op.
func (h *Handler) patchSecondaryCrossRegionPGHosts(ctx context.Context, dep *Deployment, slots []regionSlot) {
	if len(slots) < 2 {
		return // single-region — no secondary control plane to patch
	}
	// slots[0] is the primary (regionKeyFromSpec returns "" for idx 0); the
	// region-local `shared-pg-rw` resolves there, so skip it. Patch slots[1:].
	patchedRegions := make([]string, 0, len(slots)-1)
	for i := 1; i < len(slots); i++ {
		s := &slots[i]
		regionLabel := s.key
		if regionLabel == "" {
			regionLabel = fmt.Sprintf("region-%d", i)
		}
		if s.kubeconfigPath == "" {
			h.log.Warn("clustermesh: secondary PG-host patch: region kubeconfig path empty — skipping (best-effort, re-run converges)",
				"id", dep.ID, "region", regionLabel)
			continue
		}
		dyn, err := h.clusterMeshDynamicClient(s.kubeconfigPath)
		if err != nil {
			h.log.Warn("clustermesh: secondary PG-host patch: dynamic client build failed — skipping (best-effort, re-run converges)",
				"id", dep.ID, "region", regionLabel, "err", err)
			continue
		}
		getCtx, cancelGet := context.WithTimeout(ctx, clusterMeshCallTimeout)
		ks, err := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
			Get(getCtx, bootstrapKitKustomizationName, metav1.GetOptions{})
		cancelGet()
		if err != nil {
			h.log.Warn("clustermesh: secondary PG-host patch: Get bootstrap-kit Kustomization failed — skipping (best-effort, re-run converges)",
				"id", dep.ID, "region", regionLabel, "err", err)
			continue
		}
		substitute, found, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if err != nil || !found {
			h.log.Warn("clustermesh: secondary PG-host patch: bootstrap-kit Kustomization has no readable substitute map — skipping (best-effort)",
				"id", dep.ID, "region", regionLabel, "found", found, "err", err)
			continue
		}
		// Scope guard: only shared-pg Sovereigns. An own-cluster install dials
		// per-app hosts (gitea-pg-rw.gitea, …) that resolve locally — never touch.
		if !strings.EqualFold(strings.TrimSpace(substitute[clusterMeshSharedPGSubstituteKey]), "true") {
			h.log.Info("clustermesh: secondary PG-host patch: shared-pg not enabled on this region — skipping (own-cluster hosts resolve locally)",
				"id", dep.ID, "region", regionLabel)
			continue
		}
		// Build the merge patch ONLY for keys that are missing or still carry a
		// region-local (non-`-mesh-rw`) host — so the patch is a true no-op once
		// the map already carries the mesh host (fresh prov / already-healed).
		hostKeys := []string{
			clusterMeshKeycloakPGHostSubstituteKey,
			clusterMeshGiteaPGHostSubstituteKey,
			clusterMeshHarborPGHostSubstituteKey,
		}
		sub := map[string]any{}
		for _, k := range hostKeys {
			if strings.TrimSpace(substitute[k]) != clusterMeshSharedPGMeshRWHost {
				sub[k] = clusterMeshSharedPGMeshRWHost
			}
		}
		if len(sub) == 0 {
			h.log.Info("clustermesh: secondary PG-host patch: substitute map already carries the -mesh-rw write host on every key — no-op",
				"id", dep.ID, "region", regionLabel)
			continue
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		patch := map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{
					fluxReconcileRequestedAtAnnotation: stamp,
				},
			},
			"spec": map[string]any{
				"postBuild": map[string]any{
					"substitute": sub,
				},
			},
		}
		patchBytes, err := json.Marshal(patch)
		if err != nil {
			h.log.Warn("clustermesh: secondary PG-host patch: marshal merge patch failed — skipping (best-effort)",
				"id", dep.ID, "region", regionLabel, "err", err)
			continue
		}
		patchCtx, cancelPatch := context.WithTimeout(ctx, clusterMeshCallTimeout)
		_, patchErr := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
			Patch(patchCtx, bootstrapKitKustomizationName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		cancelPatch()
		if patchErr != nil {
			h.log.Warn("clustermesh: secondary PG-host patch: Patch bootstrap-kit Kustomization failed — skipping (best-effort, re-run converges)",
				"id", dep.ID, "region", regionLabel, "err", patchErr)
			continue
		}
		keys := make([]string, 0, len(sub))
		for k := range sub {
			keys = append(keys, k)
		}
		h.log.Info("clustermesh: secondary PG-host patch: stamped shared-pg-mesh-rw WRITE host onto secondary region's bootstrap-kit Kustomization (keycloak/gitea/harbor cross-region DB host, #4436)",
			"id", dep.ID, "region", regionLabel, "keys", keys, "host", clusterMeshSharedPGMeshRWHost, "requestedAt", stamp)
		patchedRegions = append(patchedRegions, regionLabel)
	}
	if len(patchedRegions) > 0 {
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: flipped keycloak/gitea/harbor cross-region DB write host to %s on %d secondary region(s) (%s) — region-local shared-pg-rw NXDOMAINs the replica region (#4436)",
				clusterMeshSharedPGMeshRWHost, len(patchedRegions), strings.Join(patchedRegions, ", ")))
	}
}

// syncKeycloakAdminSecret copies the primary region's keycloak master-realm admin
// Secrets (keycloakAdminSecretNames, host namespace keycloakAdminSecretNamespace)
// onto every replica region so the replica's keycloak-config-cli post-install hook
// (and every kcadm / sso-bridge consumer) authenticates with the SAME
// `admin-password` the shared keycloak DB — seeded by region-A — expects
// (#4158/#4915 — one layer above the #4159 DB-secret fix). See
// keycloakAdminSecretName for the full shared-DB divergence analysis + the live
// region-B 401 evidence.
//
// BEST-EFFORT — like syncSharedPGConsumerHubSecrets this NEVER blocks/refuses the
// cnpg-pair flip: the admin Secret is not part of the WAL-stream auth, so a
// missing source (region-A's keycloak hasn't minted it yet) or a per-replica
// copy failure is logged and SKIPPED, and the #3241/#3583 level-trigger re-run
// converges it on a later pass. It returns nothing (the caller ignores the
// outcome) precisely so it can never wedge convergence.
//
// NOT gated on the `-mesh-rw` host marker: the admin Secret carries only the
// password (no DB host), and region-A's value is authoritative the moment
// region-A's keycloak has minted it. Single-region (len(slots)<2) is a no-op.
//
// The destination is the HOST `keycloak` namespace where the de-vcluster (#4325)
// keycloak StatefulSet + config-cli read the Secret DIRECTLY — no syncer hop. Once
// the replica's `keycloak-admin` carries region-A's value, its config-cli retry
// authenticates against the value already in the shared DB (no DB surgery / no
// keycloak restart needed on a fresh prov). Region-A's own keycloak is untouched
// (it is the SOURCE, never a destination — slots[0] is skipped), so this can never
// regress the working primary. Each Secret in keycloakAdminSecretNames is synced
// INDEPENDENTLY: a source not present yet is skipped without holding back the rest.
func (h *Handler) syncKeycloakAdminSecret(ctx context.Context, dep *Deployment, slots []regionSlot) {
	if len(slots) < 2 {
		return // single-region — no replica to sync to
	}
	primary := &slots[0]
	if primary.clientset == nil {
		h.log.Warn("clustermesh: keycloak admin-secret sync: primary clientset nil — skipping (best-effort, re-run converges)",
			"id", dep.ID)
		return
	}

	synced := make([]string, 0, len(keycloakAdminSecretNames))
	for _, name := range keycloakAdminSecretNames {
		getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		src, err := primary.clientset.CoreV1().Secrets(keycloakAdminSecretNamespace).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		if err != nil {
			// region-A has not minted THIS Secret yet — the host keycloak may still
			// be installing, or catalyst-kc-master-admin-credentials has not
			// back-filled its derived value. Skip this Secret quietly; the
			// level-trigger re-run converges once it exists. Best-effort — never
			// holds back the others.
			h.log.Info("clustermesh: keycloak admin-secret sync: source Secret not present on primary — skipping (host keycloak may still be installing; best-effort, re-run converges)",
				"id", dep.ID, "namespace", keycloakAdminSecretNamespace, "secret", name)
			continue
		}
		changedAny := false
		for i := 1; i < len(slots); i++ {
			replica := &slots[i]
			if replica.clientset == nil {
				continue
			}
			changed, err := h.copySecretAcrossClusters(ctx, src, replica.clientset, keycloakAdminSecretNamespace)
			if err != nil {
				h.log.Warn("clustermesh: keycloak admin-secret sync: copy to replica failed — skipping this Secret/region (best-effort, re-run converges)",
					"id", dep.ID, "region", replica.key, "namespace", keycloakAdminSecretNamespace, "secret", name, "err", err)
				continue
			}
			if changed {
				changedAny = true
			}
		}
		if changedAny {
			synced = append(synced, name)
		}
	}

	if len(synced) > 0 {
		h.log.Info("clustermesh: keycloak master-realm admin Secret(s) synced primary → replica region(s) (#4915/#4158 — kill the region-B config-cli 401 / realm-import HR wedge; both regions share ONE keycloak DB seeded by region-A)",
			"id", dep.ID, "namespace", keycloakAdminSecretNamespace, "secrets", strings.Join(synced, ", "), "replicaRegions", len(slots)-1)
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: synced keycloak admin Secret(s) (%s) primary → %d replica region(s) — the replica's config-cli + sso-bridge now authenticate against the shared keycloak DB region-A seeded (#4915)",
				strings.Join(synced, ", "), len(slots)-1))
	}
}

// syncSSOOIDCMangledSecrets copies the primary region's RESOLVED per-app SSO/OIDC
// credential Secrets (ssoOIDCMangledHostSecrets, vCluster-syncer-mangled host
// names, namespace `mgmt`) onto every replica region so the in-vc-mgmt apps
// (grafana, harbor, openbao, …) that mount them by their mangled host name find
// the credential on the replica — where the per-app `ExternalSecret` cannot
// resolve them (region-B's ESO 403s against region-A's in-vc-mgmt OpenBao
// cross-cluster TokenReview). See ssoOIDCMangledHostSecrets for the full
// divergence analysis + the live region-B grafana CreateContainerConfigError
// evidence (#4158, the SSO layer above #4159's DB-secret + #4162's admin-secret).
//
// BEST-EFFORT — like syncKeycloakAdminSecret / syncSharedPGConsumerHubSecrets this
// NEVER blocks/refuses the cnpg-pair flip: the SSO Secrets are not part of the
// WAL-stream auth, so a missing source (region-A's ESO hasn't resolved it yet,
// or that consumer isn't wired on this Sovereign) or a per-replica copy failure
// is logged + SKIPPED, and the #3241/#3583 level-trigger re-run converges it on a
// later pass. It returns nothing (the caller ignores the outcome) precisely so it
// can never wedge convergence.
//
// NOT gated on the `-mesh-rw` host marker: these Secrets carry OIDC client creds +
// URLs, no DB host, so region-A's value is authoritative the moment region-A's ESO
// has resolved it — there is no "pushing it would NXDOMAIN" window to wait out.
//
// The destination is the HOST `mgmt` namespace where the in-vc-mgmt Pod (single-
// namespace sync mode) mounts the mangled object directly by name. region-A is the
// SOURCE, never a destination (slots[0] is skipped), so this can never regress the
// working primary. Single-region (len(slots)<2) is a no-op.
func (h *Handler) syncSSOOIDCMangledSecrets(ctx context.Context, dep *Deployment, slots []regionSlot) {
	if len(slots) < 2 {
		return // single-region — no replica to sync to
	}
	primary := &slots[0]
	if primary.clientset == nil {
		h.log.Warn("clustermesh: SSO-OIDC mangled-secret sync: primary clientset nil — skipping (best-effort, re-run converges)",
			"id", dep.ID)
		return
	}

	synced := make([]string, 0, len(ssoOIDCMangledHostSecrets))
	for _, name := range ssoOIDCMangledHostSecrets {
		getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		src, err := primary.clientset.CoreV1().Secrets(ssoOIDCMangledHostSecretsNamespace).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		if err != nil {
			// region-A's ESO hasn't resolved this app's SSO Secret yet (or the
			// consumer isn't wired on this Sovereign) — skip quietly. Best-effort;
			// the level-trigger re-run converges once the source materialises.
			h.log.Info("clustermesh: SSO-OIDC mangled-secret sync: source Secret not present on primary — skipping (ESO may still be resolving / consumer unconfigured; best-effort, re-run converges)",
				"id", dep.ID, "namespace", ssoOIDCMangledHostSecretsNamespace, "secret", name)
			continue
		}
		copied := true
		for i := 1; i < len(slots); i++ {
			replica := &slots[i]
			if replica.clientset == nil {
				copied = false
				continue
			}
			if _, err := h.copySecretAcrossClusters(ctx, src, replica.clientset, ssoOIDCMangledHostSecretsNamespace); err != nil {
				h.log.Warn("clustermesh: SSO-OIDC mangled-secret sync: copy to replica failed — skipping this Secret/region (best-effort, re-run converges)",
					"id", dep.ID, "region", replica.key, "namespace", ssoOIDCMangledHostSecretsNamespace, "secret", name, "err", err)
				copied = false
				continue
			}
		}
		if copied {
			synced = append(synced, name)
		}
	}

	if len(synced) > 0 {
		h.log.Info("clustermesh: SSO-OIDC mangled Secrets synced primary → replica region(s) (#4158 — kill the region-B grafana/harbor/openbao CreateContainerConfigError; the replica's ESO 403s against region-A OpenBao)",
			"id", dep.ID, "namespace", ssoOIDCMangledHostSecretsNamespace, "secrets", synced, "replicaRegions", len(slots)-1)
		h.emitClusterMeshProgress(dep, "info",
			fmt.Sprintf("ClusterMesh: synced %d per-app SSO-OIDC Secret(s) (%s) primary → %d replica region(s) — the replica's in-vc-mgmt apps (grafana, …) mount the resolved credential the region-B ESO cannot fetch (#4158)",
				len(synced), strings.Join(synced, ", "), len(slots)-1))
	}
}

// copySecretAcrossClusters create-or-updates a copy of src into the dst
// cluster under the given namespace, keeping src's name. Server-side and
// owner-scoped metadata (resourceVersion / UID / ownerReferences /
// managedFields / creationTimestamp / generation / selfLink) are
// stripped so the object is admissible on the destination; Data, Type,
// Labels, and (non-Kustomize) Annotations carry over. Idempotent: a
// re-run refreshes the destination's Data/Type from the current source
// bytes (which is what makes the #3241 level-trigger / #3583 steady-state
// re-run heal the copy). When the destination already matches the source
// (the common steady-state pass — nothing drifted) the Update is SKIPPED
// (#3583), so a heal pass is a cheap Get+compare that writes only on
// drift. The destination namespace is assumed to exist (slot 16b ships
// the `cnpg` Namespace unconditionally on every region).
//
// Returns changed=true ONLY when the destination bytes were actually
// written — a Create (the copy didn't exist) or an Update (the copy
// drifted). A steady-state match returns changed=false so a caller can
// gate a downstream side-effect (e.g. #4878's consumer rollout-restart)
// STRICTLY on a real change and never thrash on the level-trigger re-run.
func (h *Handler) copySecretAcrossClusters(ctx context.Context, src *corev1.Secret, dst kubernetes.Interface, namespace string) (bool, error) {
	if src == nil {
		return false, fmt.Errorf("source Secret is nil")
	}
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        src.Name,
			Namespace:   namespace,
			Labels:      src.Labels,
			Annotations: src.Annotations,
		},
		Type:       src.Type,
		Data:       src.Data,
		StringData: src.StringData,
	}

	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	existing, err := dst.CoreV1().Secrets(namespace).Get(callCtx, src.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("Get Secret %s/%s on replica: %w", namespace, src.Name, err)
		}
		if _, createErr := dst.CoreV1().Secrets(namespace).Create(callCtx, desired, metav1.CreateOptions{}); createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				// Lost a race with another writer — fall through to update.
				return h.updateCopiedSecret(callCtx, desired, dst, namespace)
			}
			return false, fmt.Errorf("Create Secret %s/%s on replica: %w", namespace, src.Name, createErr)
		}
		return true, nil
	}
	// #3583: the destination already exists. On the common steady-state pass
	// the bytes match (nothing drifted) — skip the Update entirely so the
	// heal phase stays a cheap Get+compare and only writes when it is
	// genuinely re-healing. An Update on every pass would churn
	// resourceVersions for no reason and add needless apiserver load.
	if secretContentMatches(existing, desired) {
		return false, nil
	}
	// Update in place — preserve the destination's resourceVersion so the
	// Update is accepted, overwrite Data/Type/Labels/Annotations.
	existing.Data = desired.Data
	existing.StringData = desired.StringData
	existing.Type = desired.Type
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	if _, updErr := dst.CoreV1().Secrets(namespace).Update(callCtx, existing, metav1.UpdateOptions{}); updErr != nil {
		return false, fmt.Errorf("Update Secret %s/%s on replica: %w", namespace, src.Name, updErr)
	}
	return true, nil
}

// secretContentMatches reports whether the destination Secret already
// carries the desired content, so copySecretAcrossClusters /
// updateCopiedSecret can skip a redundant Update on a steady-state heal
// pass (#3583). It compares Type and the EFFECTIVE data — Data overlaid
// with StringData — because the apiserver folds StringData into Data on
// write, so a destination that was created from `desired` reports the
// merged bytes under .Data with .StringData cleared. Labels/Annotations
// are intentionally NOT part of the match: the replica-auth Secrets this
// path copies carry no meaningful labels, and the heal contract is about
// the WAL-stream auth material (Data/Type), not cosmetic metadata.
func secretContentMatches(existing, desired *corev1.Secret) bool {
	if existing == nil || desired == nil {
		return false
	}
	if existing.Type != desired.Type {
		return false
	}
	return reflect.DeepEqual(effectiveSecretData(existing), effectiveSecretData(desired))
}

// effectiveSecretData returns Data overlaid with StringData (StringData
// wins, matching apiserver semantics), as a fresh map so callers never
// mutate the Secret. Empty values normalise to a non-nil empty map so a
// nil-Data and empty-Data Secret compare equal.
func effectiveSecretData(s *corev1.Secret) map[string][]byte {
	out := make(map[string][]byte, len(s.Data)+len(s.StringData))
	for k, v := range s.Data {
		out[k] = v
	}
	for k, v := range s.StringData {
		out[k] = []byte(v)
	}
	return out
}

// updateCopiedSecret is the create-raced-update fallback for
// copySecretAcrossClusters: re-Get to pick up the live resourceVersion,
// then Update with the desired Data/Type/Labels/Annotations. Returns
// changed=true only when it actually writes (matches copySecretAcrossClusters'
// contract so the race path never spuriously reports a change).
func (h *Handler) updateCopiedSecret(ctx context.Context, desired *corev1.Secret, dst kubernetes.Interface, namespace string) (bool, error) {
	existing, err := dst.CoreV1().Secrets(namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("Get Secret %s/%s on replica (race fallback): %w", namespace, desired.Name, err)
	}
	// #3583: the racing creator may have already written the desired bytes —
	// skip the redundant Update if so.
	if secretContentMatches(existing, desired) {
		return false, nil
	}
	existing.Data = desired.Data
	existing.StringData = desired.StringData
	existing.Type = desired.Type
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	if _, updErr := dst.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); updErr != nil {
		return false, fmt.Errorf("Update Secret %s/%s on replica (race fallback): %w", namespace, desired.Name, updErr)
	}
	return true, nil
}

// clusterMeshDynamicClient builds a dynamic.Interface for the cluster
// behind the given kubeconfig path, honouring the test-only factory
// override the same way buildRegionSlots does for typed clients.
func (h *Handler) clusterMeshDynamicClient(kubeconfigPath string) (dynamic.Interface, error) {
	if factory := loadClusterMeshTestDynamicClientFactory(); factory != nil {
		if d, ok := factory(kubeconfigPath); ok {
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

	// pickPrimaryPath mirrors pickPath's filesystem fallback for the
	// PRIMARY slot. dep.Result.KubeconfigPath (the passed-in
	// primaryKubeconfigPath) is empty when catalyst-api restarted /
	// deploy-bot rolled and dep.Result wasn't re-hydrated — the exact
	// hw128 (#3241) failure: the primary mesh slot (cluster=primaryMeshName)
	// got an empty path → "kubeconfig path empty" → the secondary never
	// peered with the primary → fullyMeshed=0 → SOVEREIGN_ENABLE_CNPG_PAIR
	// stayed OFF (blocks north-star row 1 region-kill). The secondaries
	// already survive this via pickPath's filesystem fallback; the primary
	// had none. The primary kubeconfig IS on the PVC at the deterministic
	// `<kubeconfigsDir>/<id>.yaml` (the jobs informer +
	// verify-sovereign-convergence.sh both read it there), so fall back to
	// it — guarded by h.kubeconfigsDir != "" + os.Stat, identical to the
	// secondary candidate's guard — even if dep.Result is nil/empty.
	pickPrimaryPath := func() string {
		if primaryKubeconfigPath != "" {
			return primaryKubeconfigPath
		}
		if h.kubeconfigsDir == "" {
			return ""
		}
		// Best-effort filesystem fallback: <dir>/<id>.yaml.
		candidate := filepath.Join(h.kubeconfigsDir, dep.ID+".yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}

	for idx, rs := range regions {
		key := regionKeyFromSpec(rs, idx)
		isPrimary := idx == 0
		kc := ""
		if isPrimary {
			kc = pickPrimaryPath()
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

		// #4811 — the dial port keys on THIS region's cloud. Prefer the
		// per-region Provider; fall back to the deployment-level Provider
		// (single-cloud provs often set only the top-level field).
		regionProvider := strings.TrimSpace(rs.Provider)
		if regionProvider == "" {
			regionProvider = strings.TrimSpace(dep.Request.Provider)
		}

		slot := regionSlot{
			key:            key,
			kubeconfigPath: kc,
			clusterName:    clusterName,
			provider:       regionProvider,
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
		if factory := loadClusterMeshTestClientFactory(); factory != nil {
			if c, ok := factory(kc); ok {
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
// up to clusterMeshLBLookupTimeout for a LoadBalancer ingress IP, then
// returns that IP paired with the canonical dial port :2379. Per #4765
// (founder 2026-07-03) peers ALWAYS dial the VIP on 2379 — NodePort is
// ABSOLUTELY FORBIDDEN, so there is no node-owned-EIP NodePort branch:
// the single sovereign-vip LB-IPAM pool gives the Service a real VIP that
// Cilium's kube-proxy-replacement LB frontend serves on 2379. The Service
// type MUST be LoadBalancer; a typed-mismatch is a hard failure rather
// than retried. A missing ingress after the timeout is likewise a hard
// failure — the slot never silently falls back to a NodePort.
// clusterMeshDialPort returns the etcd port a peer dials for the
// clustermesh-apiserver endpoint. Default is clusterMeshAPIServerPort
// (2379, the canonical Hetzner-hcloud-ccm path). On no-CCM Huawei the
// cloud-init/deploy sets CLUSTERMESH_APISERVER_DIAL_PORT to the
// clustermesh-proxy host-socket port (a dedicated non-2379 port that does
// NOT collide with k3s' embedded etcd on the CP-node host :2379) — see
// clusterMeshAPIServerPort's doc + issue #4784. A NodePort value here is
// still forbidden; the proxy binds a hostNetwork socket, not a NodePort.
//
// #4811 — the port MUST key on the DIALED region's cloud, not on a single
// static env. The mothership catalyst-api establishes meshes for BOTH
// Hetzner and Huawei Sovereigns from ONE (env-unset) deployment, so a static
// CLUSTERMESH_APISERVER_DIAL_PORT env can never be right for a multi-cloud
// mothership: with the env unset it defaulted to 2379 and wrote every Huawei
// Sovereign's peer endpoint as `:2379` (→ k3s KINE, ClusterMesh 0/1). The
// env is retained as an explicit single-cloud override (highest precedence),
// but when it is unset the default is resolved from `provider`: a no-CCM
// cloud (Huawei/kom4dc) dials the clustermesh-proxy hostPort (12379); a CCM
// cloud (Hetzner, real hcloud-ccm LB IP with no k3s-etcd host collision)
// dials the canonical etcd :2379.
func clusterMeshDialPort(provider string) int {
	if v := strings.TrimSpace(os.Getenv(clusterMeshDialPortEnvVar)); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	if isNoCCMCloud(provider) {
		return clusterMeshProxyDialPort
	}
	return clusterMeshAPIServerPort
}

// isNoCCMCloud reports whether the given deployment/region provider runs
// WITHOUT a cloud-controller-manager LoadBalancer — i.e. the
// clustermesh-apiserver Service has no real LB IP and the CP-node public EIP
// (which serves the ingress) collides with k3s' embedded etcd on host :2379,
// so peers must dial the clustermesh-proxy hostPort (clusterMeshProxyDialPort)
// instead of :2379. Hetzner (hcloud-ccm) returns false. Matches the provider
// strings validated in deployments.go (req.Provider / Regions[i].Provider).
func isNoCCMCloud(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "huawei", "hcs", "kom4dc":
		return true
	default:
		return false
	}
}

func (h *Handler) waitForClusterMeshLB(ctx context.Context, client kubernetes.Interface, provider string) (string, int, error) {
	timeout := clusterMeshLBLookupTimeout
	if v := clusterMeshTestOverrideLBTimeout(); v > 0 {
		timeout = v
	}
	interval := clusterMeshLBLookupInterval
	if v := clusterMeshTestOverrideLBInterval(); v > 0 {
		interval = v
	}
	deadline := time.Now().Add(timeout)
	for {
		callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
		svc, err := client.CoreV1().Services(clusterMeshNamespace).Get(callCtx, clusterMeshApiserverService, metav1.GetOptions{})
		cancel()
		if err != nil {
			if apierrors.IsNotFound(err) {
				if time.Now().After(deadline) {
					return "", 0, fmt.Errorf("Service %s/%s not found after %s",
						clusterMeshNamespace, clusterMeshApiserverService, timeout)
				}
				if err := sleepCtx(ctx, interval); err != nil {
					return "", 0, err
				}
				continue
			}
			return "", 0, fmt.Errorf("Get Service %s/%s: %w",
				clusterMeshNamespace, clusterMeshApiserverService, err)
		}
		// Hard-fail if someone has retyped the Service: invariant A3.
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			return "", 0, fmt.Errorf("Service %s/%s type %q violates invariant A3 (must be LoadBalancer)",
				clusterMeshNamespace, clusterMeshApiserverService, svc.Spec.Type)
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			ip := ing.IP
			if ip == "" {
				ip = ing.Hostname
			}
			if ip == "" {
				continue
			}
			// #4765 (founder 2026-07-03, "FUCK THE NODEPORTS!!!") — peers
			// dial the clustermesh endpoint on clusterMeshDialPort(); a
			// NodePort is ABSOLUTELY FORBIDDEN and there is no node-owned-EIP
			// NodePort fallback (if the endpoint is unreachable the etcd
			// connect step surfaces it with full peer context).
			//
			// #4784 CORRECTION (proven live on hw225): the pre-#4784 belief
			// that ":2379 is served by Cilium's LB frontend on the ingress
			// IP, reached over the WG node path on the no-CCM Huawei
			// 1:1-NAT'd EIP" is FALSE. The ingress IP IS the CP node's public
			// EIP, the EIP is a 1:1 NAT to the CP node's PRIVATE IP, and the
			// CP node runs k3s' OWN embedded etcd on the host :2379 — so the
			// post-NAT packet (dst=<CP-private>:2379) is answered by k3s etcd
			// (host socket, dst-IP-agnostic), never by Cilium's LB-VIP
			// datapath (which is keyed on the public EIP and never sees the
			// post-NAT packet). openssl from region-B: <EIP>:2379 →
			// CN=etcd-server (k3s CA, Verify 19); the SAME certs against a
			// host-socket on a NON-2379 port → CN=clustermesh-apiserver.
			// cilium.io (Cilium CA, Verify 0). The durable fix therefore
			// serves the clustermesh etcd on a dedicated non-2379 host socket
			// (clustermesh-proxy DaemonSet) and points peers at it via
			// CLUSTERMESH_APISERVER_DIAL_PORT — still NO NodePort. Hetzner
			// keeps the default 2379 (hcloud-ccm real LB, no k3s-etcd host
			// collision on the LB IP).
			return ip, clusterMeshDialPort(provider), nil
		}
		if time.Now().After(deadline) {
			return "", 0, fmt.Errorf("Service %s/%s has no LoadBalancer ingress after %s",
				clusterMeshNamespace, clusterMeshApiserverService, timeout)
		}
		if err := sleepCtx(ctx, interval); err != nil {
			return "", 0, err
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
//
// peerDialPort is the port resolved by resolveClusterMeshDialPort —
// 2379 behind a real cloud LB, the Service NodePort when the peer's
// ingress IP is node-owned (nodeIPAM, #3241).
func buildPeerConfigBlob(peerClusterName string, peerDialPort int) []byte {
	endpoint := fmt.Sprintf("https://%s:%d", peerMeshHostname(peerClusterName), peerDialPort)
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
// The returned bool reports whether the Secret actually CHANGED (created
// or content updated) — the caller keys the rollout-restart on it
// (#3241 layer 4): the level-triggered reconcile re-runs this every
// ~2 min, and an unconditional restart per pass turned the loop into a
// mesh-component crash-cycle (clustermesh-apiserver Deployment hit
// generation 35 on hw128) that never left the agents a stable window to
// finish the remote-config sync.
//
// #4811 part-b INVARIANT (relied on by the establish path + pinned by
// TestAutoEstablishClusterMesh_RewritesStaleDialPortOnReEstablish): the
// per-peer ENDPOINT config-blob key (`<peer>` → buildPeerConfigBlob output)
// is a MANAGED key, so it is ALWAYS overwritten — never merged-and-kept —
// with the freshly-built blob. That blob carries the current
// clusterMeshDialPort(), so an env whose peer endpoint was written in a
// pre-12379 window (stale `:2379`) is authoritatively corrected to
// `:12379` on the next establish pass rather than preserved. A future
// refactor that turns this into skip-if-key-exists semantics would
// silently reintroduce the stuck-`:2379` / ClusterMesh 0/1 symptom proven
// live on hw228 — keep managed keys authoritative.
func (h *Handler) applyClusterMeshSecret(ctx context.Context, client kubernetes.Interface, entries map[string][]byte) (bool, error) {
	if len(entries) == 0 {
		return false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	existing, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(callCtx, clusterMeshSecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("Get Secret %s/%s: %w",
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
				// Race window — fall through to Update. Treat as changed:
				// the racer's content is unknown.
				return true, h.updateClusterMeshSecret(ctx, client, entries)
			}
			return false, fmt.Errorf("Create Secret %s/%s: %w",
				clusterMeshNamespace, clusterMeshSecretName, createErr)
		}
		return true, nil
	}
	// Merge: keep entries we don't manage, overwrite ones we do.
	merged := make(map[string][]byte, len(existing.Data)+len(entries))
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range entries {
		merged[k] = v
	}
	// Byte-identical content → no write, no restart (idempotent re-run).
	if len(merged) == len(existing.Data) {
		identical := true
		for k, v := range merged {
			if ev, ok := existing.Data[k]; !ok || !bytes.Equal(ev, v) {
				identical = false
				break
			}
		}
		if identical {
			return false, nil
		}
	}
	patch := []byte(fmt.Sprintf(`{"data":%s}`, encodeSecretDataJSON(merged)))
	if _, patchErr := client.CoreV1().Secrets(clusterMeshNamespace).Patch(callCtx, clusterMeshSecretName, types.MergePatchType, patch, metav1.PatchOptions{}); patchErr != nil {
		return false, fmt.Errorf("Patch Secret %s/%s: %w",
			clusterMeshNamespace, clusterMeshSecretName, patchErr)
	}
	return true, nil
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
// The returned bool reports whether the DaemonSet pod template actually
// changed — same restart-thrash rationale as applyClusterMeshSecret.
func (h *Handler) patchCiliumHostAliases(ctx context.Context, client kubernetes.Interface, peers []hostAliasPeer) (bool, error) {
	if len(peers) == 0 {
		return false, nil
	}
	aliases := make([]map[string]any, 0, len(peers))
	desired := make([]corev1.HostAlias, 0, len(peers))
	for _, p := range peers {
		if p.LBIP == "" || p.PeerName == "" {
			continue
		}
		aliases = append(aliases, map[string]any{
			"ip":        p.LBIP,
			"hostnames": []string{peerMeshHostname(p.PeerName)},
		})
		desired = append(desired, corev1.HostAlias{IP: p.LBIP, Hostnames: []string{peerMeshHostname(p.PeerName)}})
	}
	if len(aliases) == 0 {
		return false, nil
	}
	// No-op guard: identical hostAliases already on the pod template →
	// skip the patch (a strategic-merge write with identical content
	// still bumps nothing, but skipping keeps intent explicit + cheap).
	getCtx, cancelGet := context.WithTimeout(ctx, clusterMeshCallTimeout)
	ds, getErr := client.AppsV1().DaemonSets(clusterMeshNamespace).Get(getCtx, "cilium", metav1.GetOptions{})
	cancelGet()
	if getErr == nil && reflect.DeepEqual(ds.Spec.Template.Spec.HostAliases, desired) {
		return false, nil
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
		return false, fmt.Errorf("marshal hostAliases patch: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()
	if _, err := client.AppsV1().DaemonSets(clusterMeshNamespace).Patch(callCtx, "cilium", types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return false, fmt.Errorf("patch cilium DaemonSet hostAliases: %w", err)
	}
	return true, nil
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

// reconcileSharedPGConsumerRestart is the #4878-residual credential-consistency
// gate in front of rolloutRestartConsumerWorkload. The original #4878 fix rolled
// the consumer the instant syncSharedPGConsumerHubSecrets OVERWROTE the replica's
// divergent shared-data hub Secret (gated on copySecretAcrossClusters'
// changed==true). Live hw232 proved that is PREMATURE: at that moment the
// emberstack reflector has not yet re-pushed region-A's authoritative password
// from shared-data into the consumer's OWN namespace, so the recreated pod
// mounts the STALE bytes and crashloops on `FATAL: password authentication
// failed` until kubelet-driven restarts happen to catch the propagated value
// (keycloak-0 crashlooped 5× 06:43:50→06:47:23Z; powerdns/powerdns-admin in the
// same window).
//
// Instead this reconciles the restart against CONSISTENCY on every ~2-min
// level-trigger pass:
//
//	1. CONSISTENCY GATE — read the consumer-namespace copy of the hub Secret on
//	   the replica. If it is absent, or its effective bytes do NOT yet match
//	   region-A's authoritative hub Secret (`src`), the reflector has not
//	   propagated the corrected credential — DEFER (no restart; the re-run
//	   re-checks). Because region-A mints the hub Secret AND the PG role password
//	   from the same source, and keycloak/gitea/harbor dial the `-mesh-rw` alias
//	   that routes writes to region-A's primary, `consumer-ns == src`
//	   transitively means the mounted password matches the LIVE PG role password
//	   on the primary — so no in-process DB probe is needed to prove (c).
//	2. IDEMPOTENCY / RESTART — hand the authoritative credential's fingerprint to
//	   rolloutRestartConsumerWorkload, which restarts ONLY when the workload's pod
//	   template was not already rolled onto this exact fingerprint. So a
//	   steady-state / already-healed pass never restarts (the #3241/#3583
//	   no-thrash contract) while a genuine future rotation re-fires exactly once.
//
// Net effect: the restart fires ONCE, AFTER the mounted credential is consistent
// — the single clean restart the #4878 goal wanted — and the level-trigger
// re-run keeps re-checking until that consistency is reached, so nothing depends
// on the reflector winning a race with the first restart.
func (h *Handler) reconcileSharedPGConsumerRestart(ctx context.Context, dep *Deployment, slot *regionSlot, hubSecretName string, w sharedPGConsumerWorkload, src *corev1.Secret) {
	if slot == nil || slot.clientset == nil || src == nil {
		return
	}
	// 1. CONSISTENCY GATE — the reflector must have re-pushed region-A's bytes
	// into the consumer's own namespace before a restart can help.
	getCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	consumerSecret, err := slot.clientset.CoreV1().Secrets(w.namespace).Get(getCtx, hubSecretName, metav1.GetOptions{})
	cancel()
	if err != nil {
		h.log.Info("clustermesh: #4878 consumer restart deferred — consumer-namespace DB Secret not present yet (reflector has not re-pushed region-A's credential; level-trigger re-run re-checks)",
			"id", dep.ID, "region", slot.key, "namespace", w.namespace, "secret", hubSecretName, "err", err)
		return
	}
	if !secretContentMatches(consumerSecret, src) {
		h.log.Info("clustermesh: #4878 consumer restart deferred — consumer-namespace DB Secret has not caught up to region-A's authoritative credential (reflector propagation lag; re-run re-checks — this is what prevents the premature-restart crashloop)",
			"id", dep.ID, "region", slot.key, "namespace", w.namespace, "secret", hubSecretName)
		return
	}
	// 2. Credential is consistent — hand the fingerprint to the idempotent
	// restart (it no-ops when the workload was already rolled onto this value).
	h.rolloutRestartConsumerWorkload(ctx, dep, slot, w, sharedPGConsumerCredentialFingerprint(src))
}

// rolloutRestartConsumerWorkload bumps the same
// `catalyst.openova.io/restartedAt` annotation idiom
// rolloutRestartClusterMeshTargets uses, but on a SINGLE consumer workload in a
// SPECIFIC replica region, so its Pod re-reads the (now corrected) shared-pg DB
// Secret it cached at boot (#4878). Idempotent per CREDENTIAL: it first reads the
// workload's current pod-template `sharedPGConsumerCredHashAnnotation`; if it
// already equals credFingerprint the workload was ALREADY rolled onto this exact
// password → no-op (so a steady-state or re-checked pass never thrashes, honouring
// #3241/#3583). Only a differing fingerprint patches the pod template (stamping
// both restartedAt and the fingerprint), which rolls the workload exactly once
// per distinct credential value. Best-effort: a NotFound (the workload isn't
// wired on this Sovereign) or any Get/Patch error is logged and swallowed, never
// fatal — the level-triggered reconcile re-runs.
func (h *Handler) rolloutRestartConsumerWorkload(ctx context.Context, dep *Deployment, slot *regionSlot, w sharedPGConsumerWorkload, credFingerprint string) {
	if slot == nil || slot.clientset == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterMeshCallTimeout)
	defer cancel()

	// Idempotency gate: read the current pod-template credential fingerprint so a
	// pass that already rolled onto this credential does not restart again.
	var currentFingerprint string
	var getErr error
	switch w.kind {
	case "statefulset":
		sts, e := slot.clientset.AppsV1().StatefulSets(w.namespace).Get(callCtx, w.name, metav1.GetOptions{})
		if e == nil {
			currentFingerprint = sts.Spec.Template.Annotations[sharedPGConsumerCredHashAnnotation]
		}
		getErr = e
	case "deployment":
		dp, e := slot.clientset.AppsV1().Deployments(w.namespace).Get(callCtx, w.name, metav1.GetOptions{})
		if e == nil {
			currentFingerprint = dp.Spec.Template.Annotations[sharedPGConsumerCredHashAnnotation]
		}
		getErr = e
	default:
		h.log.Warn("clustermesh: #4878 consumer rollout-restart: unknown workload kind — skipping",
			"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace)
		return
	}
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			h.log.Info("clustermesh: #4878 consumer rollout-restart: workload not present on replica — skipping (consumer may be unconfigured on this Sovereign; best-effort)",
				"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace)
			return
		}
		h.log.Warn("clustermesh: #4878 consumer rollout-restart: get workload failed (continuing; re-run converges)",
			"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace, "err", getErr)
		return
	}
	if credFingerprint != "" && currentFingerprint == credFingerprint {
		// Already rolled onto this exact credential — no-op (no thrash).
		return
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"catalyst.openova.io/restartedAt":%q,%q:%q}}}}}`,
		stamp, sharedPGConsumerCredHashAnnotation, credFingerprint))
	var err error
	switch w.kind {
	case "statefulset":
		_, err = slot.clientset.AppsV1().StatefulSets(w.namespace).Patch(callCtx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "deployment":
		_, err = slot.clientset.AppsV1().Deployments(w.namespace).Patch(callCtx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			h.log.Info("clustermesh: #4878 consumer rollout-restart: workload not present on replica — skipping (consumer may be unconfigured on this Sovereign; best-effort)",
				"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace)
			return
		}
		h.log.Warn("clustermesh: #4878 consumer rollout-restart failed (continuing; re-run converges)",
			"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace, "err", err)
		return
	}
	h.log.Info("clustermesh: #4878 rolled consumer workload so it re-reads region-A's authoritative shared-pg password once the corrected credential propagated into the consumer namespace",
		"id", dep.ID, "region", slot.key, "kind", w.kind, "name", w.name, "namespace", w.namespace, "credFingerprint", credFingerprint)
	h.emitClusterMeshProgress(dep, "info",
		fmt.Sprintf("ClusterMesh: rolled %s/%s in region %q so it re-reads region-A's authoritative shared-pg password once the credential was consistent in-namespace (#4878 — single clean restart, clears the stale-password FATAL)",
			w.namespace, w.name, slot.key))
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

// resolvePrimaryKubeconfigPath resolves a deployment's primary kubeconfig
// path with the same CONVENTIONAL fallback #3153 added for the Phase-1
// resume (shouldResumePhase1 in deployments.go): Result.KubeconfigPath
// carries json `omitempty` and is lost when a mothership roll persists a
// rehydrated record before PutKubeconfig stamped it — but the kubeconfig
// FILE itself always survives on the PVC at `<kubeconfigsDir>/<id>.yaml`.
//
// Returns the resolved, stat-readable path and true. READ-ONLY with respect
// to dep.Result by design: it must NOT stamp the resolved path back onto
// dep.Result.KubeconfigPath. Deployment.State() hands the *Result
// pointer to writeJSON, which marshals it OUTSIDE dep.mu — a write
// here from the mesh-reconcile goroutine (markPhase1Done →
// runAutoEstablishClusterMesh) races that marshal (caught by -race in
// TestGetDeploymentEvents_ReturnsComponentEventsInBuffer). Callers that
// need the path persisted (shouldResumePhase1's startup-only resume)
// stamp it themselves under dep.mu; the mesh path threads the returned
// value into buildRegionSlots explicitly. The stat guard matches #3153
// exactly: a record whose file was genuinely lost across the restart
// (PVC unmount / wipe race) or whose kubeconfigsDir is unset returns
// ("", false) so the caller can warn-and-skip rather than spin a retry
// budget against a phantom path.
//
// #5131 — chroot self-materialize. On the in-cluster (chroot) Sovereign the
// mothership never posts the PRIMARY (local region-a) kubeconfig — only
// SECONDARY kubeconfigs arrive via POST /api/v1/sovereign/secondary-kubeconfig
// at handover — so the conventional file is absent and the os.Stat above
// missed forever, permanently skipping the ClusterMesh startup reconcile on a
// perfectly healthy 2-region Sovereign. When the primary file is absent AND
// we are the chroot that owns dep, materialize it from the in-cluster
// ServiceAccount (a NO-NETWORK build — see materializeChrootPrimaryKubeconfig)
// and re-stat. This is still dep.Result-write-free (it writes a FILE, not the
// record), so the -race contract above holds. Best-effort: a materialize
// failure keeps the honest ("", false) miss.
func (h *Handler) resolvePrimaryKubeconfigPath(dep *Deployment) (string, bool) {
	dep.mu.Lock()
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()
	// The conventional PVC path is the deterministic materialize target: the
	// mothership's PutKubeconfig stamps this exact string, so on a chroot the
	// recorded field (when present) already equals it.
	conventional := ""
	if h.kubeconfigsDir != "" {
		conventional = filepath.Join(h.kubeconfigsDir, dep.ID+".yaml")
	}
	if kubeconfigPath == "" {
		kubeconfigPath = conventional
	}
	if kubeconfigPath == "" {
		return "", false
	}
	if _, err := os.Stat(kubeconfigPath); err == nil {
		return kubeconfigPath, true
	}
	// Primary file absent. On the chroot that owns dep, synthesize the local
	// cluster's kubeconfig from the in-cluster ServiceAccount and re-stat.
	if conventional != "" && h.materializeChrootPrimaryKubeconfig(dep, conventional) {
		if _, err := os.Stat(conventional); err == nil {
			return conventional, true
		}
	}
	return "", false
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
// The kubeconfig path is resolved with the #3153 conventional fallback
// (resolvePrimaryKubeconfigPath): a record restored from the PVC loses
// the `omitempty` Result.KubeconfigPath FIELD, but the kubeconfig FILE
// survives at `<kubeconfigsDir>/<id>.yaml` — without this fallback an
// hw126-shaped Sovereign would warn "primary kubeconfig path empty" and
// skip the mesh reconcile forever. The guard stays warn-and-skip, not
// fail: a record whose file was genuinely lost across the restart (PVC
// unmount / wipe race) returns false rather than spinning the whole
// retry budget. Fully-meshed deployments pass this check too — the
// loop's first attempt is a cheap idempotent re-run that confirms full
// mesh and exits.
func (h *Handler) shouldStartupClusterMeshReconcile(dep *Deployment) bool {
	if !h.clusterMeshReconcileStatusGate(dep) {
		return false
	}
	if _, ok := h.resolvePrimaryKubeconfigPath(dep); !ok {
		dep.mu.Lock()
		regionCount := len(dep.Request.Regions)
		dep.mu.Unlock()
		h.log.Warn("clustermesh: startup reconcile skipped — primary kubeconfig unresolved on ready multi-region deployment",
			"id", dep.ID,
			"regions", regionCount,
		)
		return false
	}
	return true
}

// clusterMeshReconcileStatusGate reports whether a rehydrated deployment's
// status + region count make it a candidate for the startup ClusterMesh
// reconcile — the STATUS/REGION half of shouldStartupClusterMeshReconcile,
// factored out for #4811 so restoreFromStore can tell "not a mesh candidate at
// all" (single-region, hard-failed) apart from "mesh candidate whose primary
// kubeconfig was merely UNRESOLVED at restore". The latter is a TRANSIENT
// condition — the kubeconfig FILE is on the PVC but the k8scache dir-load had
// not finished when restoreFromStore ran, so os.Stat missed it — and must be
// RETRIED (retryStartupClusterMeshReconcile) rather than skipped forever, which
// is exactly the bug that left region-b ClusterMesh 0/1 across every OOM
// restart.
//
// ready, OR failed-by-TIMEOUT (#3285/hw130): a timeout record's cluster keeps
// converging under Flux — abandoning its mesh forever contradicted the
// never-wipe-a-timeout doctrine. Hard failures (OutcomeFailed /
// flux-not-reconciling) stay excluded.
func (h *Handler) clusterMeshReconcileStatusGate(dep *Deployment) bool {
	dep.mu.Lock()
	status := dep.Status
	regionCount := len(dep.Request.Regions)
	outcome := ""
	if dep.Result != nil {
		outcome = dep.Result.Phase1Outcome
	}
	dep.mu.Unlock()
	rescuableTimeout := status == "failed" && outcome == helmwatch.OutcomeTimeout
	if (status != "ready" && !rescuableTimeout) || regionCount < 2 {
		return false
	}
	return true
}

// retryStartupClusterMeshReconcile is the #4811 bounded self-heal for the
// TRANSIENT "primary kubeconfig unresolved at restore" race. restoreFromStore
// runs inside New(), sometimes milliseconds BEFORE the k8scache "loaded cluster
// from kubeconfigs dir" step writes the primary kubeconfig file to the PVC — so
// shouldStartupClusterMeshReconcile's resolvePrimaryKubeconfigPath os.Stat
// misses the file and the gate returns false. The old code dropped the mesh
// reconcile on the floor (a one-shot else-if), so the establish loop — which
// carries the steady-state heal that REGENERATES a stale cilium-clustermesh
// endpoint (the live #4811 symptom: the mesh endpoint stuck at the :2379 KINE
// port instead of the :12379 proxy dial port → ClusterMesh 0/1 forever) — never
// started, and every OOM restart re-lost the same race.
//
// This goroutine re-evaluates shouldStartupClusterMeshReconcile(dep) on
// clusterMeshStartupRetryInterval and launches runAutoEstablishClusterMesh the
// FIRST time it passes, then returns (the establish loop owns convergence +
// steady-state from there; its own clusterMeshLoopActive guard makes any
// double-start a no-op). It stops when the deployment leaves the
// ready/rescuable-timeout status gate (a wipe landed mid-wait — the kubeconfigs
// are gone).
//
// #5131 — the retry is LEVEL-triggered: it no longer gives up after a fixed
// budget with "next trigger is a catalyst-api restart". A ready multi-region
// Sovereign whose primary kubeconfig is momentarily unresolved (the #4811
// dir-load race) OR structurally unresolved until materialized (the #5131
// chroot case, where the mothership never posts the local region's kubeconfig)
// keeps re-checking until it resolves and self-heals without a pod restart. The
// only terminal edges are "resolved → launch establish" and "no longer a mesh
// candidate → stop"; the budget field is repurposed as a heartbeat-log cadence.
func (h *Handler) retryStartupClusterMeshReconcile(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("clustermesh: startup reconcile retry panic recovered",
				"id", dep.ID,
				"panic", r,
			)
		}
	}()

	interval := h.clusterMeshStartupRetryInterval
	if interval <= 0 {
		interval = clusterMeshStartupRetryIntervalDefault
	}
	// #5131 — the retry is LEVEL-triggered, not a one-shot budget. It
	// re-evaluates the gate every `interval` and returns ONLY on a terminal
	// edge: the deployment leaving the ready/rescuable-timeout candidate set (a
	// wipe removed the kubeconfigs → nothing left to mesh), or the establish
	// loop launching. It NEVER gives up "until a catalyst-api restart" — a
	// transient miss (or, the #5131 chroot case, a primary kubeconfig that must
	// first be materialized from the in-cluster ServiceAccount) self-heals
	// without a pod restart. `clusterMeshStartupRetryBudget` is repurposed as
	// the heartbeat cadence: how often an unresolved-but-still-candidate
	// deployment re-logs that it is still waiting, so the level-triggered spin
	// stays observable without logging on every interval.
	heartbeat := h.clusterMeshStartupRetryBudget
	if heartbeat <= 0 {
		heartbeat = clusterMeshStartupRetryBudgetDefault
	}

	// A background context: sleepCtx here only paces the loop by `interval`.
	// The stop condition is the candidate gate below, never a deadline — that
	// is what makes the retry level-triggered rather than restart-gated.
	ctx := context.Background()

	h.log.Info("clustermesh: startup reconcile deferred — primary kubeconfig unresolved at restore; retrying (level-triggered) until it resolves or the deployment leaves the mesh-candidate set",
		"id", dep.ID,
		"interval", interval,
		"heartbeat", heartbeat,
	)

	lastHeartbeat := time.Now()
	for attempt := 1; ; attempt++ {
		// Stop the moment the deployment stops being a mesh candidate — a wipe
		// mid-wait removes the kubeconfigs, so there is nothing left to mesh.
		if !h.clusterMeshReconcileStatusGate(dep) {
			h.log.Info("clustermesh: startup reconcile retry stopped — deployment no longer a ready multi-region mesh candidate",
				"id", dep.ID,
				"attempt", attempt,
			)
			return
		}
		if h.shouldStartupClusterMeshReconcile(dep) {
			h.log.Info("clustermesh: startup reconcile primary kubeconfig resolved — launching establish loop",
				"id", dep.ID,
				"attempt", attempt,
			)
			go h.runAutoEstablishClusterMesh(dep)
			return
		}
		if time.Since(lastHeartbeat) >= heartbeat {
			h.log.Warn("clustermesh: startup reconcile still waiting — primary kubeconfig unresolved; retrying on a level basis (no restart required)",
				"id", dep.ID,
				"attempts", attempt,
			)
			lastHeartbeat = time.Now()
		}
		_ = sleepCtx(ctx, interval)
	}
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
