package handler

// Pre-fire resource preflight gate (#3889).
//
// THE "NO FUEL BEFORE TAKEOFF" GUARD
// ──────────────────────────────────
// The mothership node accumulates unbounded containerd image layers from
// dead/wiped deployments plus /var/lib/catalyst execution logs and tofu
// workdirs. On hw168 the node crossed ~93% disk → DiskPressure → kubelet
// began evicting pods → the pool-domain-manager's ghcr token went dead →
// a deployment record failed to persist → orphaned Huawei infra (#3889).
//
// The provisioning goroutine runs INSIDE the mothership catalyst-api
// process (see runProvisioning). Firing a fresh prov into a disk-starved
// node is the "take off with the tank empty" mistake. This gate reads the
// mothership node's headroom BEFORE accepting a deployment create and
// REFUSES with an actionable error when the tank is low, instead of
// letting the prov start and die mid-flight.
//
// FAIL-OPEN, NEVER FAIL-CLOSED ON INFRA-UNAVAILABILITY
// ────────────────────────────────────────────────────
// If the in-cluster client is unavailable (CI / unit tests / local dev
// where catalyst-api runs outside a cluster), or the node list comes back
// empty, the gate returns nil (allow). The gate ONLY refuses when it has
// POSITIVE evidence of pressure — a DiskPressure/MemoryPressure=True
// condition, or a measured disk-usage percentage over the high-water
// mark. A flaky stats read must never wedge the create path.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// preflightDiskHighWaterPct — refuse a deployment create when the
// mothership node's root/imagefs usage is at or above this percentage.
// 85% leaves a margin below the kubelet's default DiskPressure eviction
// threshold (imagefs.available<15% ⇒ ~85% used) so we refuse BEFORE the
// kubelet starts evicting, not after the cascade has begun. Operator
// override: CATALYST_PREFLIGHT_DISK_HIGH_WATER_PCT.
const preflightDiskHighWaterPct = 85

// resourcePreflightResult is the structured outcome of one preflight
// evaluation. ok=false means REFUSE; reason carries the actionable
// operator-facing message.
type resourcePreflightResult struct {
	ok     bool
	reason string
}

// nodeStatsSummary is the slice of the kubelet /stats/summary response we
// care about: the node-level filesystem usage. capacityBytes/usedBytes on
// node.fs reflect the root filesystem; node.runtime.imageFs reflects the
// containerd image store (often the same fs on k3s). We take the higher
// of the two usage percentages so an image-layer blowup is caught even
// when imagefs is a separate device.
type nodeStatsSummary struct {
	Node struct {
		Fs *fsStats `json:"fs"`
		Runtime *struct {
			ImageFs *fsStats `json:"imageFs"`
		} `json:"runtime"`
	} `json:"node"`
}

type fsStats struct {
	CapacityBytes *uint64 `json:"capacityBytes"`
	UsedBytes     *uint64 `json:"usedBytes"`
}

// usedPct returns the integer percent used, or -1 when the stats are
// incomplete (so callers can distinguish "unknown" from "0% used").
func (f *fsStats) usedPct() int {
	if f == nil || f.CapacityBytes == nil || f.UsedBytes == nil || *f.CapacityBytes == 0 {
		return -1
	}
	return int((*f.UsedBytes * 100) / *f.CapacityBytes)
}

// isControlPlaneNode reports whether n carries a control-plane / master
// role label. The mothership catalyst-api always runs on a control-plane
// node (single-node k3s mothership, or the primary CP of a multi-node
// one), so the control-plane node IS the node whose disk we must guard.
func isControlPlaneNode(n *corev1.Node) bool {
	for k := range n.Labels {
		if k == "node-role.kubernetes.io/control-plane" ||
			k == "node-role.kubernetes.io/master" {
			return true
		}
	}
	return false
}

// nodePressure reports whether the node currently has DiskPressure or
// MemoryPressure asserted True. These are the exact conditions that
// triggered the hw168 eviction cascade.
func nodePressure(n *corev1.Node) (disk, mem bool) {
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeDiskPressure:
			disk = c.Status == corev1.ConditionTrue
		case corev1.NodeMemoryPressure:
			mem = c.Status == corev1.ConditionTrue
		}
	}
	return disk, mem
}

// nodeDiskUsedPct fetches the kubelet /stats/summary for the node via the
// apiserver node-proxy and returns the higher of node.fs and
// node.runtime.imageFs usage percentages. Returns -1 (unknown) on any
// error — the caller treats unknown as "allow" (fail-open). This call is
// best-effort: the node-proxy subresource may be RBAC-denied on some
// clusters, in which case we still have the condition-based check above.
func nodeDiskUsedPct(ctx context.Context, core kubernetes.Interface, nodeName string) (pct int) {
	// Best-effort + panic-safe. The fake clientset used in unit tests
	// returns a REST client that panics on .Get(); production
	// rest.InClusterConfig clients never do. Recovering here keeps the
	// gate fail-open under any client that can't serve the node-proxy
	// subresource (RBAC-denied, fake, or a future client-go change),
	// degrading gracefully to the condition-based pressure check.
	pct = -1
	defer func() {
		if recover() != nil {
			pct = -1
		}
	}()
	raw, err := core.CoreV1().RESTClient().
		Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("stats", "summary").
		DoRaw(ctx)
	if err != nil {
		return -1
	}
	var s nodeStatsSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		return -1
	}
	fsPct := s.Node.Fs.usedPct()
	imgPct := -1
	if s.Node.Runtime != nil {
		imgPct = s.Node.Runtime.ImageFs.usedPct()
	}
	if imgPct > fsPct {
		return imgPct
	}
	return fsPct
}

// evaluateResourcePreflight inspects the mothership node(s) and decides
// whether a deployment create may proceed. It is pure with respect to its
// inputs (the core client + the high-water mark) so it is exhaustively
// unit-testable with a fake clientset.
//
// Decision order (first positive evidence wins, all fail-open):
//  1. No nodes / list error            → allow (infra unknown).
//  2. DiskPressure=True on the CP node  → REFUSE.
//  3. MemoryPressure=True on the CP node → REFUSE.
//  4. Measured disk usage >= highWater  → REFUSE.
//  5. Otherwise                          → allow.
func evaluateResourcePreflight(ctx context.Context, core kubernetes.Interface, highWaterPct int) resourcePreflightResult {
	allow := resourcePreflightResult{ok: true}
	if core == nil {
		return allow
	}
	nodes, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		// Cluster unreachable or empty — fail open. We never block a
		// create on inability to read the gauge; the gate only fires on
		// POSITIVE evidence of pressure.
		return allow
	}

	// Identify the node to guard: the control-plane (mothership) node.
	// Fall back to the only node when there is exactly one and no CP
	// label is present (defensive — every real mothership labels its CP).
	var guard *corev1.Node
	for i := range nodes.Items {
		if isControlPlaneNode(&nodes.Items[i]) {
			guard = &nodes.Items[i]
			break
		}
	}
	if guard == nil {
		if len(nodes.Items) == 1 {
			guard = &nodes.Items[0]
		} else {
			// Multi-node cluster with no discernible CP label — can't
			// confidently pick the mothership node, so fail open.
			return allow
		}
	}

	disk, mem := nodePressure(guard)
	if disk {
		return resourcePreflightResult{
			ok: false,
			reason: fmt.Sprintf(
				"mothership node %q reports DiskPressure — prune before firing (#3889)",
				guard.Name,
			),
		}
	}
	if mem {
		return resourcePreflightResult{
			ok: false,
			reason: fmt.Sprintf(
				"mothership node %q reports MemoryPressure — free memory before firing (#3889)",
				guard.Name,
			),
		}
	}

	if pct := nodeDiskUsedPct(ctx, core, guard.Name); pct >= 0 && pct >= highWaterPct {
		return resourcePreflightResult{
			ok: false,
			reason: fmt.Sprintf(
				"mothership disk at %d%% (>=%d%% high-water) on node %q — prune before firing (#3889)",
				pct, highWaterPct, guard.Name,
			),
		}
	}

	return allow
}

// preflightHighWaterPct resolves the disk high-water mark, honouring the
// CATALYST_PREFLIGHT_DISK_HIGH_WATER_PCT operator override (Inviolable
// Principle #4 — every threshold operator-tunable). An unset / unparseable
// / out-of-range (1..99) value falls back to the default.
func preflightHighWaterPct() int {
	v := strings.TrimSpace(os.Getenv("CATALYST_PREFLIGHT_DISK_HIGH_WATER_PCT"))
	if v == "" {
		return preflightDiskHighWaterPct
	}
	n := 0
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 || n > 99 {
		return preflightDiskHighWaterPct
	}
	return n
}

// checkResourcePreflight is the Handler-level entry point called from
// CreateDeployment. It resolves the in-cluster core client via the same
// sovereignDeps seam the other local-cluster endpoints use (production:
// rest.InClusterConfig; tests: injected fake), evaluates the gate, and
// returns a non-nil error carrying the operator-facing reason when the
// create must be REFUSED. A nil error means "proceed".
//
// Fail-open contract: if the deps client cannot be built (CI / local dev
// outside a cluster) the gate allows the create — exactly the existing
// behaviour for every other path that builds sovereignDeps.
func (h *Handler) checkResourcePreflight(ctx context.Context) error {
	// Allow an operator to disable the gate entirely
	// (CATALYST_PREFLIGHT_DISABLE=1) as a break-glass — e.g. when the
	// node-stats read itself is the thing wedged.
	if strings.TrimSpace(os.Getenv("CATALYST_PREFLIGHT_DISABLE")) == "1" {
		return nil
	}
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.core == nil {
		// In-cluster client unavailable — fail open (CI / dev / tests
		// that don't wire sovereignDepsFactory).
		return nil
	}
	res := evaluateResourcePreflight(ctx, deps.core, preflightHighWaterPct())
	if !res.ok {
		return fmt.Errorf("%s", res.reason)
	}
	return nil
}
