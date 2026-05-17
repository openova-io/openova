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
	"net/http"
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
type secondaryKubeconfigRequest struct {
	DeploymentID   string `json:"deploymentId"`
	RegionKey      string `json:"regionKey"`
	KubeconfigYAML string `json:"kubeconfigYaml"`
}

// safeIDPattern rejects path-traversal + shell-meta in deploymentId
// and regionKey. Both come from authenticated callers but defense-in-
// depth — the filename is composed from these and written to a
// shared-PVC directory.
var safeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

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
