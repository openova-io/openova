// sovereign_self.go — GET /api/v1/sovereign/self
//
// Sovereign self-discovery: returns the deployment id and FQDN of the
// Sovereign cluster the catalyst-api Pod is running on. Used by the
// Sovereign-mode catalyst-ui to resolve the deployment id for clean URLs
// (`/dashboard`, `/jobs`, `/cloud`, …) and to wire the deployment-scoped
// data fetches the canonical UI components expect.
//
// Resolution order (first non-empty wins):
//
//  1. CATALYST_SELF_DEPLOYMENT_ID env (populated by the contabo
//     orchestrator through the bp-catalyst-platform chart's
//     sovereign-fqdn ConfigMap when it stamps the per-Sovereign overlay).
//  2. Local store scan — find the single deployment record whose
//     SovereignFQDN matches CATALYST_OTECH_FQDN. The
//     POST /api/v1/internal/deployments/import endpoint persists exactly
//     one such record at handover time after rejecting any FQDN that
//     doesn't match this cluster's env, so a single-record lookup is
//     unambiguous. This branch is what makes clean URLs render data on
//     the very first handover-arrival, before the orchestrator has had
//     a chance to update the values overlay + Flux to stamp the env.
//  3. CATALYST_OTECH_FQDN populated but no record yet — return 503
//     so the UI surfaces a "waiting for handover" state instead of
//     looping on an empty id. This is the genuine
//     `mother-not-yet-fired` window.
//  4. Both env empty AND no record — return 404 (mothership). The UI
//     hook treats this as "not on a Sovereign" and falls back to URL
//     params for `/provision/$id/...`.
//
// No authentication required: the response carries no secrets, only
// public identifiers (deployment id + FQDN are both visible in URLs).
// Bypassing the session gate keeps the cookie-vs-OIDC pre-flight in
// SovereignConsoleLayout usable on the very first browser hit.
package handler

import (
	"net/http"
	"os"
	"strings"
)

// SovereignSelfResponse — wire shape of GET /api/v1/sovereign/self.
//
// Field names mirror the Sovereign deployment record so the UI can use
// the same schema across mothership-side `/api/v1/deployments/{id}` and
// Sovereign-side `/api/v1/sovereign/self`.
type SovereignSelfResponse struct {
	DeploymentID  string `json:"deploymentId"`
	SovereignFQDN string `json:"sovereignFQDN"`
}

// HandleSovereignSelf returns the active Sovereign's deployment id + FQDN.
func (h *Handler) HandleSovereignSelf(w http.ResponseWriter, _ *http.Request) {
	deploymentID := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	fqdn := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))

	// Mothership: neither env is set — surface 404 so the UI hook treats
	// this as "not on a Sovereign" and uses URL params instead.
	if deploymentID == "" && fqdn == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api Pod is not running on a Sovereign cluster (no CATALYST_OTECH_FQDN/SELF_DEPLOYMENT_ID env)",
		})
		return
	}

	// Step 2: store fallback — scan the local store for a record whose
	// SovereignFQDN matches CATALYST_OTECH_FQDN. The cutover-import
	// endpoint enforces the FQDN match before persisting, so the only
	// record on disk should already be ours — but we still filter
	// defensively in case a tenant migration ever lands a stale record.
	if deploymentID == "" && h.store != nil {
		recs, err := h.store.LoadAll(nil)
		if err == nil {
			for _, r := range recs {
				if strings.EqualFold(r.Request.SovereignFQDN, fqdn) {
					deploymentID = r.ID
					break
				}
			}
		}
	}

	// Step 3: still no id — handover hasn't fired yet. Return 503 so the
	// UI shows a "waiting for handover" state.
	if deploymentID == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "deployment-id-not-yet-stamped",
			"detail": "Sovereign FQDN is known but the contabo orchestrator has not yet POSTed the deployment id to this cluster. The handover step is behind — retry shortly.",
			"fqdn":   fqdn,
		})
		return
	}

	writeJSON(w, http.StatusOK, SovereignSelfResponse{
		DeploymentID:  deploymentID,
		SovereignFQDN: fqdn,
	})
}
