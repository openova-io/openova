// deployment_handover_export.go — mother-side cutover data transfer.
//
// At handover (fireHandover), the mother POSTs the full deployment
// record (events, jobs history, HRs, cloud topology, kubeconfig
// metadata) to the freshly-provisioned child's catalyst-api at
//
//	POST https://api.<sovereign-fqdn>/api/v1/internal/deployments/import
//
// The receiving Sovereign persists it to its local store (see
// deployment_handover_import.go) so its operator-facing endpoints
// answer with byte-byte-identical data. Closes the data half of the
// mother→child contract.
package handler

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// exportDeploymentToChild ships the deployment record to the child's
// catalyst-api. Called as a goroutine from fireHandover so it never
// blocks the SSE emit.
//
// D16 PR E (2026-05-17 t138 bug fix): the child's ingress + cert + gateway
// are racing to become reachable from outside in the seconds after handover
// fires. The initial POST routinely fails with EOF / connection refused
// because Cilium Gateway hasn't programmed the HTTPRoute yet. Earlier
// behaviour (no retry, early return) silently lost both the deployment
// record AND the secondary-kubeconfig fan-out (the goroutine was guarded
// behind the early return). The fix:
//   - retry deployment-export with exponential backoff (up to ~5 min)
//   - kick off secondary-kubeconfig export UNCONDITIONALLY at the top, so
//     a deployment-export failure can't suppress the D16 fan-out
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

	// D16 PR E: kick off secondary-kubeconfig fan-out IMMEDIATELY in its
	// own goroutine. It is independent of the deployment-record export
	// — it must not be suppressed by an EOF on the deployment POST.
	go h.exportSecondaryKubeconfigsToChild(dep, fqdn, depID)

	// #3263 #3277: ship the deployment's Job rows (BOTH regions) to the
	// chroot's jobs store. The chroot's own bootstrap watch seeds only
	// region-a; the secondary regions' install rows exist solely in the
	// mothership's store, so without this the chroot /jobs page shows
	// half the regions. Independent of the record export for the same
	// reason as the kubeconfig fan-out above.
	go h.exportJobsToChild(depID, fqdn)

	body, err := json.Marshal(rec)
	if err != nil {
		h.log.Error("deployment-export: marshal failed",
			"id", depID,
			"err", err,
		)
		return
	}

	url := "https://api." + fqdn + "/api/v1/internal/deployments/import"
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child's LE cert may be seconds behind handover; operator browsers always see the validated cert
		},
	}

	// Shared handover-export retry (#3747): backoff 5s→ceiling, total budget
	// defaults to 20m (was a fixed 5m), retries connection errors + 5xx while
	// the child's `api.<fqdn>` backend is still warming up, gives up on a 4xx.
	// Most handovers succeed on attempt 2-4 (15-45s after first try).
	outcome := postHandoverExportWithRetry(client, url, body, func(attempt int, kind string, status int, err error) {
		switch kind {
		case "request-error":
			h.log.Error("deployment-export: NewRequest failed",
				"id", depID, "url", url, "err", err,
			)
		case "conn-error":
			h.log.Warn("deployment-export: POST failed (will retry)",
				"id", depID, "url", url, "attempt", attempt, "err", err,
			)
		case "5xx":
			h.log.Warn("deployment-export: child 5xx (will retry)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "4xx":
			h.log.Error("deployment-export: child 4xx (giving up)",
				"id", depID, "url", url, "attempt", attempt, "status", status,
			)
		case "ok":
			h.log.Info("deployment-export: shipped to child",
				"id", depID, "url", url, "attempt", attempt, "events", len(rec.Events),
			)
		}
	})
	if outcome == handoverExportGaveUpBudget {
		h.log.Error("deployment-export: gave up after export budget exhausted (backend never reachable — operator can re-mint via /mint-handover-token)",
			"id", depID, "url", url, "budget", handoverExportBudget().String(),
		)
	}
}

// secondaryKubeconfigFileWaitBudget bounds how long
// exportSecondaryKubeconfigsToChild will wait for a secondary region's
// kubeconfig file to appear on the mothership PVC before giving up on
// that region. Phase-1 fires handover when the PRIMARY CP's HRs go
// Ready — secondary CPs PUT their kubeconfigs as their own cloud-init
// completes, which is asynchronous and frequently 1-3min behind. The
// pre-#1760 code did `os.ReadFile` once, found nothing, logged a
// `skip — kubeconfig not on mothership` warning, and moved on, leaving
// the chroot's `/var/lib/catalyst/kubeconfigs/` empty forever. Override
// via CATALYST_D16_EXPORT_FILE_WAIT_SECONDS for tests + dev.
var secondaryKubeconfigFileWaitBudget = 10 * time.Minute

// secondaryKubeconfigFilePollInterval — how often we re-stat the
// expected on-disk path while waiting for it to appear. Tight enough
// that a kubeconfig PUT mid-wait shows up within seconds.
var secondaryKubeconfigFilePollInterval = 3 * time.Second

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
//
// TBD-A10 / #1760 (2026-05-18): pre-fix this function read each
// kubeconfig file ONCE and silently `continue`d on os.ReadFile error.
// Secondary CPs PUT their kubeconfigs back asynchronously as their own
// cloud-init completes, which is frequently 1-3min behind the primary
// CP's Phase-1 ready event that triggers handover-fire. Result: on
// every multi-region prov the chroot's `kubeconfigs/` stayed empty,
// /cloud/list rendered 1 bubble instead of N, D16 silently regressed.
// Fix: wait up to secondaryKubeconfigFileWaitBudget per region for the
// file to land before declaring the region un-exportable. Per region
// run as its own goroutine so a slow region N doesn't block N+1.
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

	// Honour CATALYST_D16_EXPORT_FILE_WAIT_SECONDS overrides for tests +
	// air-gapped operators (INVIOLABLE-PRINCIPLES.md #4 — knobs are
	// runtime-configurable).
	fileWait := secondaryKubeconfigFileWaitBudget
	if v := os.Getenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS"); v != "" {
		if d, perr := time.ParseDuration(v + "s"); perr == nil {
			fileWait = d
		}
	}
	pollInt := secondaryKubeconfigFilePollInterval
	if v := os.Getenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS"); v != "" {
		if d, perr := time.ParseDuration(v + "s"); perr == nil {
			pollInt = d
		}
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // child cert may be seconds behind handover, same rationale as exportDeploymentToChild
		},
	}
	// Fan out one goroutine per secondary region. A slow region (whose
	// CP cloud-init is still in flight) MUST NOT block another region
	// that's already ready. The exec waits for all regions in a
	// WaitGroup so test code can assert post-conditions deterministically.
	var wg sync.WaitGroup
	for _, regionKey := range regions {
		wg.Add(1)
		go func(regionKey string) {
			defer wg.Done()
			path := filepath.Join(dir, depID+"-"+regionKey+".yaml")
			raw, ok := h.waitForSecondaryKubeconfig(path, fileWait, pollInt, depID, regionKey)
			if !ok {
				h.log.Error("d16-export: kubeconfig never landed on mothership; chroot will see 1 fewer cluster than expected",
					"id", depID, "region", regionKey, "path", path, "budget", fileWait.String(),
				)
				return
			}
			payload := map[string]string{
				"deploymentId":   depID,
				"regionKey":      regionKey,
				"kubeconfigYaml": string(raw),
			}
			// #3991 — replay the secondary CP's private node IP (if the
			// CP supplied one and we stashed it as a sidecar) so the
			// chroot can rewrite the kubeconfig's server host from the
			// VPC-external EIP to the VPC-peered private IP it can route
			// to. Best-effort: a missing sidecar means the chroot keeps
			// the EIP (pre-#3991 behaviour).
			clusterID := depID + "-" + regionKey
			if ipRaw, ierr := os.ReadFile(nodeIPSidecarPath(dir, clusterID)); ierr == nil {
				if ip := strings.TrimSpace(string(ipRaw)); ip != "" {
					payload["nodeInternalIp"] = ip
				}
			}
			body, _ := json.Marshal(payload)
			h.postSecondaryKubeconfigWithRetry(client, url, body, depID, regionKey)
		}(regionKey)
	}
	wg.Wait()
}

// waitForSecondaryKubeconfig polls for the kubeconfig file at path to
// appear (and be non-empty) within budget. Returns (bytes, true) on
// success, (nil, false) on timeout. Emits a single
// `d16-export: waiting on secondary kubeconfig` log every 30s so the
// catalyst-api journal shows progress instead of going silent.
func (h *Handler) waitForSecondaryKubeconfig(path string, budget, poll time.Duration, depID, regionKey string) ([]byte, bool) {
	deadline := time.Now().Add(budget)
	logEvery := 30 * time.Second
	nextLog := time.Now().Add(logEvery)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return raw, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		if time.Now().After(nextLog) {
			h.log.Info("d16-export: waiting on secondary kubeconfig PUT-back",
				"id", depID, "region", regionKey, "path", path,
				"remaining", time.Until(deadline).Truncate(time.Second).String(),
			)
			nextLog = time.Now().Add(logEvery)
		}
		time.Sleep(poll)
	}
}

// postSecondaryKubeconfigWithRetry POSTs body to url via the shared
// handover-export retry policy (#3747). Refactored out of
// exportSecondaryKubeconfigsToChild so the per-region goroutine reads
// top-to-bottom and the retry shape is unit-testable. Budget defaults to
// 20m (was a fixed 5m), backoff 5s→ceiling, retries connection errors + 5xx
// while the child backend warms up, gives up on a 4xx.
func (h *Handler) postSecondaryKubeconfigWithRetry(client *http.Client, url string, body []byte, depID, regionKey string) {
	outcome := postHandoverExportWithRetry(client, url, body, func(attempt int, kind string, status int, err error) {
		switch kind {
		case "request-error":
			h.log.Error("d16-export: NewRequest failed",
				"id", depID, "region", regionKey, "err", err,
			)
		case "conn-error":
			h.log.Warn("d16-export: POST failed (will retry)",
				"id", depID, "region", regionKey, "url", url, "attempt", attempt, "err", err,
			)
		case "5xx":
			h.log.Warn("d16-export: child 5xx (will retry)",
				"id", depID, "region", regionKey, "attempt", attempt, "status", status,
			)
		case "4xx":
			h.log.Error("d16-export: child 4xx (giving up)",
				"id", depID, "region", regionKey, "attempt", attempt, "status", status,
			)
		case "ok":
			h.log.Info("d16-export: secondary kubeconfig shipped to child",
				"id", depID, "region", regionKey, "attempt", attempt,
			)
		}
	})
	if outcome == handoverExportGaveUpBudget {
		h.log.Error("d16-export: gave up on region after export budget exhausted (backend never reachable — operator can re-mint via /mint-handover-token)",
			"id", depID, "region", regionKey, "budget", handoverExportBudget().String(),
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
		keys = append(keys, cr+"-"+regionSlotIndex(i))
	}
	return keys
}

// regionSlotIndex — local int→string without pulling strconv into the import set.
// Single-digit fast path (we never have >9 regions per Sovereign).
func regionSlotIndex(n int) string {
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
