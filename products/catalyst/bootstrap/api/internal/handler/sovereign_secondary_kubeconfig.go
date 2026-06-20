// sovereign_secondary_kubeconfig.go — chroot endpoint for D16
// multi-cluster fan-out (docs/SOVEREIGN-MULTI-REGION-DOD.md gate D16).
//
// POST /api/v1/sovereign/secondary-kubeconfig
//
// Accepts a secondary region's kubeconfig bytes + region key + deploymentID,
// writes the kubeconfig to /var/lib/catalyst/kubeconfigs/<depID>-<region>.yaml
// (the canonical location FactoryFromEnv reads at startup), and calls
// k8sCache.Factory.AddCluster so the dashboard handler's per-cluster
// h.k8sCache.List(clusterID, ...) fan-out can immediately enumerate all
// 3 regions' pods.
//
// Why this exists (D16 root cause):
//
//   The Sovereign Console's /dashboard?group_by=cluster handler reads
//   ONE clusterID from the query string and lists pods from THAT cluster
//   only — Layer-1=Cluster renders 1 bubble instead of 3 on a 3-region
//   Sovereign. The fix requires the chroot to register the secondary
//   regions' kubeconfigs in k8sCache so fan-out enumerates them.
//
//   The chroot has its OWN in-cluster kubeconfig auto-registered, but
//   secondary kubeconfigs live on the mothership's PVC and aren't
//   replicated to the chroot. This handler bridges that — mothership
//   POSTs each secondary kubeconfig to the chroot's catalyst-api at
//   handover, the chroot writes them to disk and registers each one.
//
// Auth: requires sovereign-admin role (matches /admin/* surface).
//
// Idempotent: re-POSTing the same {depID, region} overwrites the file
// + AddCluster on a duplicate ID is a no-op per k8sCache contract.

package handler

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// kubeconfigsDir is the canonical on-disk path FactoryFromEnv reads at
// startup. Mirrored from k8scache/factory.go:982 default. Overridable
// via CATALYST_K8SCACHE_KUBECONFIGS_DIR for tests + dev.
func kubeconfigsDir() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR")); v != "" {
		return v
	}
	return "/var/lib/catalyst/kubeconfigs"
}

// secondaryKubeconfigRequest is the POST body schema. RegionKey MUST
// match the on-disk filename suffix convention `<depID>-<region>.yaml`
// (e.g. depID="abc123", region="nbg1-1" → /var/lib/catalyst/kubeconfigs/
// abc123-nbg1-1.yaml). KubeconfigYAML is the raw kubeconfig bytes.
//
// NodeInternalIP (#3991) — the secondary CP's PRIVATE node IP, detected
// at cloud-init time by the same `ip -4 -o addr show dev eth0` the k3s
// `--node-ip` uses. The IaC writes the kubeconfig's server URL to the
// region's PUBLIC EIP (`MY_EIP`) because the EXTERNAL mothership — which
// loads this same kubeconfig into its own k8sCache — sits outside both
// per-region VPCs and can only reach the DNAT'd EIP. But the IN-CLUSTER
// catalyst-api (the chroot) runs INSIDE region-a's VPC, where that EIP
// is unroutable (HCS DNAT is external-only; verified live on hw173:
// region-a pod → EIP:6443 times out, region-a pod → peer private-IP:6443
// is OPEN over the existing cross-VPC peering). So the chroot rewrites
// the server host to this private IP, which IS region-a-routable via the
// VPC-peering routes provisioned by the huawei/hetzner IaC. The k3s
// apiserver cert already carries `--tls-san=$NODE_IP`, so the pinned-CA
// TLS handshake still validates after the host swap. Optional + only
// honoured on a chroot (SOVEREIGN_FQDN set); empty/absent → no rewrite
// (mothership keeps the EIP).
type secondaryKubeconfigRequest struct {
	DeploymentID   string `json:"deploymentId"`
	RegionKey      string `json:"regionKey"`
	KubeconfigYAML string `json:"kubeconfigYaml"`
	NodeInternalIP string `json:"nodeInternalIp,omitempty"`
}

// rewriteKubeconfigServerHost replaces the host:port host portion of the
// `server:` URL(s) in a kubeconfig with newHost, preserving the scheme,
// port, and path. Returns the rewritten bytes and the number of server
// lines changed. A kubeconfig with multiple clusters has its every
// server line rewritten (a secondary k3s kubeconfig has exactly one).
//
// Only the HOST is swapped — scheme (https), port (6443), and any path
// stay intact, so the pinned `certificate-authority-data` continues to
// validate against the apiserver cert (which lists the node IP as a SAN).
func rewriteKubeconfigServerHost(raw, newHost string) (string, int) {
	newHost = strings.TrimSpace(newHost)
	if newHost == "" {
		return raw, 0
	}
	lines := strings.Split(raw, "\n")
	changed := 0
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "server:") {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		rawURL := strings.TrimSpace(strings.TrimPrefix(trimmed, "server:"))
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			continue
		}
		port := u.Port()
		if port != "" {
			u.Host = net.JoinHostPort(newHost, port)
		} else {
			u.Host = newHost
		}
		lines[i] = indent + "server: " + u.String()
		changed++
	}
	if changed == 0 {
		return raw, 0
	}
	return strings.Join(lines, "\n"), changed
}

// isChroot reports whether this catalyst-api runs as an in-cluster
// Sovereign (chroot) rather than the external mothership. The canonical
// discriminator used throughout the handler package is the SOVEREIGN_FQDN
// env: set on every Sovereign by the bp-catalyst-platform chart, unset on
// the mothership. Only the chroot rewrites the secondary kubeconfig to the
// VPC-peered private IP (#3991); the mothership keeps the public EIP it
// needs to reach the region from outside the VPC.
func isChroot() bool {
	return strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != ""
}

// safeIDPattern rejects path-traversal + shell-meta in deploymentId
// and regionKey. Both come from authenticated callers but defense-in-
// depth — the filename is composed from these and written to a
// shared-PVC directory.
var safeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// nodeIPSidecarPath is the on-disk path of the private-node-IP sidecar
// for a secondary cluster (#3991). It sits next to `<clusterID>.yaml` as
// `<clusterID>.nodeip` so the mothership's handover forward can replay
// the IP to the chroot. The `.nodeip` extension keeps it out of
// LoadClustersFromDir's `*.yaml` glob.
func nodeIPSidecarPath(dir, clusterID string) string {
	return filepath.Join(dir, clusterID+".nodeip")
}

// HandleSovereignSecondaryKubeconfig handles POST
// /api/v1/sovereign/secondary-kubeconfig.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the kubeconfig bytes are
// written 0o600 and the directory inherits 0o700 from FactoryFromEnv's
// initial mkdir. The bytes never enter a logged struct.
func (h *Handler) HandleSovereignSecondaryKubeconfig(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "k8scache-unavailable",
			"detail": "catalyst-api was started without k8sCache; cannot register a new cluster",
		})
		return
	}
	var body secondaryKubeconfigRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "expect JSON {deploymentId, regionKey, kubeconfigYaml}",
		})
		return
	}
	body.DeploymentID = strings.TrimSpace(body.DeploymentID)
	body.RegionKey = strings.TrimSpace(body.RegionKey)
	if !safeIDPattern.MatchString(body.DeploymentID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-deploymentId",
			"detail": "must match ^[a-z0-9][a-z0-9-]{0,62}$",
		})
		return
	}
	if !safeIDPattern.MatchString(body.RegionKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-regionKey",
			"detail": "must match ^[a-z0-9][a-z0-9-]{0,62}$",
		})
		return
	}
	raw := strings.TrimSpace(body.KubeconfigYAML)
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "kubeconfigYaml-required",
			"detail": "kubeconfigYaml is required",
		})
		return
	}

	// #3991 — cross-region datapath fix. The IaC stamps the secondary
	// region's `server:` URL to its PUBLIC EIP so the external mothership
	// (outside the per-region VPCs) can reach it. But the IN-CLUSTER
	// catalyst-api (chroot) runs inside region-a's VPC, where the peer
	// region's EIP is unroutable (HCS DNAT is external-only). When a
	// nodeInternalIp is supplied AND we are the chroot, rewrite the server
	// host to that VPC-peered private IP — region-a-routable via the
	// IaC's cross-VPC peering routes, and still cert-valid because the k3s
	// apiserver lists the node IP as a TLS SAN. The mothership (no
	// SOVEREIGN_FQDN) leaves the kubeconfig untouched: it keeps the EIP.
	nodeIP := strings.TrimSpace(body.NodeInternalIP)
	if nodeIP != "" && net.ParseIP(nodeIP) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-nodeInternalIp",
			"detail": "nodeInternalIp must be a valid IP address",
		})
		return
	}
	rewroteFromNodeIP := false
	if nodeIP != "" && isChroot() {
		rewritten, n := rewriteKubeconfigServerHost(raw, nodeIP)
		if n > 0 {
			raw = rewritten
			rewroteFromNodeIP = true
			h.log.Info("secondary-kubeconfig: rewrote server host to VPC-peered private IP (#3991)",
				"depId", body.DeploymentID,
				"region", body.RegionKey,
				"serversRewritten", n,
			)
		}
	}

	// #4000 — durable self-heal fallback. When NO nodeInternalIp was
	// shipped (cloud-init predating the #3991 IaC change, or its runtime
	// `ip addr` detection returned empty), the kubeconfig still carries the
	// unroutable EIP. On the chroot, probe the server host; if it's an
	// unreachable PUBLIC address, discover a reachable PRIVATE SAN off the
	// apiserver's own TLS cert and heal to it — no pre-shipped data needed.
	// Skipped when the #3991 rewrite already fired (host is private + the
	// probe would just confirm it). Best-effort: a no-op leaves the EIP
	// exactly as the pre-#4000 path did.
	if !rewroteFromNodeIP && isChroot() {
		healed, healedTo, reason := selfHealKubeconfigServer(raw)
		if healedTo != "" {
			raw = healed
			h.log.Info("secondary-kubeconfig: self-healed server host EIP -> reachable private SAN (#4000)",
				"depId", body.DeploymentID,
				"region", body.RegionKey,
				"healedTo", healedTo,
			)
		} else {
			h.log.Debug("secondary-kubeconfig: self-heal no-op (#4000)",
				"depId", body.DeploymentID, "region", body.RegionKey, "reason", reason)
		}
	}

	dir := kubeconfigsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.log.Warn("secondary-kubeconfig: mkdir failed", "dir", dir, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "dir-create-failed",
			"detail": err.Error(),
		})
		return
	}
	clusterID := fmt.Sprintf("%s-%s", body.DeploymentID, body.RegionKey)
	path := filepath.Join(dir, clusterID+".yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		h.log.Warn("secondary-kubeconfig: write failed", "path", path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "write-failed",
			"detail": err.Error(),
		})
		return
	}

	// #3991 — persist the private node IP as a sidecar so the mothership's
	// handover forward (exportSecondaryKubeconfigsToChild) can replay it to
	// the chroot, which is the consumer that must rewrite EIP→private-IP.
	// Best-effort: a missing sidecar simply means the chroot keeps the EIP
	// (the pre-#3991 behaviour), so a write failure must not fail the POST.
	if nodeIP != "" {
		sidecar := nodeIPSidecarPath(dir, clusterID)
		if werr := os.WriteFile(sidecar, []byte(nodeIP), 0o600); werr != nil {
			h.log.Warn("secondary-kubeconfig: node-ip sidecar write failed (non-fatal)",
				"path", sidecar, "err", werr)
		}
	}
	if err := h.k8sCache.AddCluster(k8scache.ClusterRef{
		ID:             clusterID,
		KubeconfigPath: path,
	}); err != nil {
		// Best-effort: file persists so a catalyst-api restart will
		// re-register the cluster via FactoryFromEnv's LoadClustersFromDir.
		h.log.Warn("secondary-kubeconfig: AddCluster failed", "id", clusterID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "add-cluster-failed",
			"detail": err.Error(),
		})
		return
	}
	h.log.Info("secondary-kubeconfig: registered",
		"depId", body.DeploymentID,
		"region", body.RegionKey,
		"clusterID", clusterID,
	)
	writeJSON(w, http.StatusCreated, map[string]string{
		"status":    "registered",
		"clusterID": clusterID,
		"path":      path,
	})
}
