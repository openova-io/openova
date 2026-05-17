// deployment_handover_export.go — mother-side cutover data transfer.
//
// At handover (fireHandover), the mother POSTs the full deployment
// record (events, jobs history, HRs, cloud topology, kubeconfig
// metadata) to the freshly-provisioned child's catalyst-api at
//
//   POST https://api.<sovereign-fqdn>/api/v1/internal/deployments/import
//
// The receiving Sovereign persists it to its local store (see
// deployment_handover_import.go) so its operator-facing endpoints
// answer with byte-byte-identical data. Closes the data half of the
// mother→child contract.
package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// exportDeploymentToChild ships the deployment record to the child's
// catalyst-api. Called as a goroutine from fireHandover so it never
// blocks the SSE emit.
func (h *Handler) exportDeploymentToChild(dep *Deployment, fqdn string) {
	if h.store == nil {
		h.log.Warn("deployment-export: no store; cannot export record",
			"id", dep.ID,
		)
		return
	}
	dep.mu.Lock()
	rec := dep.toRecord()
	depID := dep.ID
	dep.mu.Unlock()

	body, err := json.Marshal(rec)
	if err != nil {
		h.log.Error("deployment-export: marshal failed",
			"id", depID,
			"err", err,
		)
		return
	}

	url := "https://api." + fqdn + "/api/v1/internal/deployments/import"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		h.log.Error("deployment-export: NewRequest failed",
			"id", depID,
			"url", url,
			"err", err,
		)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child's LE cert may be seconds behind handover; operator browsers always see the validated cert
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		h.log.Error("deployment-export: POST failed",
			"id", depID,
			"url", url,
			"err", err,
		)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		h.log.Error("deployment-export: child rejected import",
			"id", depID,
			"url", url,
			"status", resp.StatusCode,
		)
		return
	}
	h.log.Info("deployment-export: shipped to child",
		"id", depID,
		"url", url,
		"events", len(rec.Events),
	)

	// D16 PR B (2026-05-17): after the deployment record is shipped,
	// iterate the secondary regions and POST each region's kubeconfig
	// to the chroot's POST /api/v1/sovereign/secondary-kubeconfig
	// endpoint (PR #1579) so the chroot's k8sCache.Factory can register
	// every cluster + the dashboard handler's per-cluster fan-out
	// (PR #1580) enumerates pods from all N regions when
	// group_by=cluster|region. Without this, Layer-1=Cluster renders
	// 1 bubble instead of N on a multi-region Sovereign.
	go h.exportSecondaryKubeconfigsToChild(dep, fqdn, depID)
}

// exportSecondaryKubeconfigsToChild iterates the deployment's secondary
// regions and POSTs each region's mothership-side kubeconfig to the
// chroot's /api/v1/sovereign/secondary-kubeconfig endpoint. Best-effort
// per region — a failure on region N doesn't abort regions N+1...
//
// The mothership stores secondary kubeconfigs at
// `/var/lib/catalyst/kubeconfigs/<depID>-<region>.yaml` (per the
// existing per-region kubeconfig PUT path). We read each, POST to the
// chroot, and log loudly on failure. The chroot's handler is
// idempotent (re-POSTing the same {depID, region} overwrites the file
// + AddCluster on duplicate ID is a no-op).
func (h *Handler) exportSecondaryKubeconfigsToChild(dep *Deployment, fqdn, depID string) {
	dep.mu.Lock()
	regions := append([]string(nil), regionKeysForExport(dep)...)
	dep.mu.Unlock()
	if len(regions) == 0 {
		return
	}
	dir := "/var/lib/catalyst/kubeconfigs"
	if v := os.Getenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR"); v != "" {
		dir = v
	}
	url := "https://api." + fqdn + "/api/v1/sovereign/secondary-kubeconfig"
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child cert may be seconds behind handover, same rationale as exportDeploymentToChild
		},
	}
	for _, regionKey := range regions {
		path := filepath.Join(dir, depID+"-"+regionKey+".yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			h.log.Warn("d16-export: skip — kubeconfig not on mothership",
				"id", depID, "region", regionKey, "path", path, "err", err,
			)
			continue
		}
		payload := map[string]string{
			"deploymentId":   depID,
			"regionKey":      regionKey,
			"kubeconfigYaml": string(raw),
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			h.log.Error("d16-export: NewRequest failed",
				"id", depID, "region", regionKey, "err", err,
			)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			h.log.Error("d16-export: POST failed",
				"id", depID, "region", regionKey, "url", url, "err", err,
			)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			h.log.Error("d16-export: child rejected secondary kubeconfig",
				"id", depID, "region", regionKey, "status", resp.StatusCode,
			)
			continue
		}
		h.log.Info("d16-export: secondary kubeconfig shipped to child",
			"id", depID, "region", regionKey,
		)
	}
}

// regionKeysForExport returns the deployment's secondary region keys
// (primary is auto-registered by the chroot's FactoryFromEnv self-
// registration branch, so we skip it here to avoid the duplicate
// AddCluster warn). Order: regions[1:] from dep.Request.Regions.
// MUST be called with dep.mu held.
func regionKeysForExport(dep *Deployment) []string {
	if len(dep.Request.Regions) <= 1 {
		return nil
	}
	keys := make([]string, 0, len(dep.Request.Regions)-1)
	for i := 1; i < len(dep.Request.Regions); i++ {
		// Region filename convention is `<region>-<slot>` (e.g. nbg1-1,
		// sin-2) per the existing per-region kubeconfig postback path.
		// Mirror that here: regions[i].CloudRegion + "-" + i.
		cr := dep.Request.Regions[i].CloudRegion
		if cr == "" {
			continue
		}
		keys = append(keys, cr+"-"+itoa(i))
	}
	return keys
}

// itoa — local int→string without pulling strconv into the import set.
// Single-digit fast path (we never have >9 regions per Sovereign).
func itoa(n int) string {
	if n >= 0 && n < 10 {
		return string(rune('0' + n))
	}
	// Multi-digit fallback (defensive — multi-region currently <=5).
	out := ""
	if n < 0 {
		out = "-"
		n = -n
	}
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return out + string(digits)
}
