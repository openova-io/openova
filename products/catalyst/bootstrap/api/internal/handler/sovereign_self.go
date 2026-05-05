// sovereign_self.go — GET /api/v1/sovereign/self
//
// Sovereign self-discovery: returns the deployment id and FQDN of the
// Sovereign cluster the catalyst-api Pod is running on. Used by the
// Sovereign-mode catalyst-ui to resolve the deployment id for clean URLs
// (`/console/dashboard` → `/provision/<self-id>/dashboard`) and to wire
// the deployment-scoped data fetches the canonical UI components expect.
//
// Resolution order:
//
//  1. CATALYST_SELF_DEPLOYMENT_ID env (populated at handover from the
//     contabo orchestrator via the bp-catalyst-platform chart's
//     sovereign-fqdn ConfigMap). This is the canonical source of truth
//     post-handover.
//  2. CATALYST_OTECH_FQDN env (already wired via the same ConfigMap, see
//     B1 PR #912). Used to populate the FQDN field. If
//     CATALYST_SELF_DEPLOYMENT_ID is empty AND CATALYST_OTECH_FQDN is
//     populated, returns 503 deployment-id-not-yet-stamped to make the
//     UI fall back gracefully (instead of looping on empty).
//  3. Both empty → 404 not-a-sovereign — the mothership (contabo) hits
//     this and the UI silently treats it as "not on a Sovereign", which
//     is the correct default for `console.openova.io`.
//
// No authentication required: the response carries no secrets, only
// public identifiers (deployment id + FQDN are both visible in URLs).
// Bypassing the session gate keeps the SovereignConsoleRedirect helper
// usable on the very first browser hit before login.
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
// Returns 404 on the mothership, 503 on Sovereigns that haven't received
// the post-handover deployment-id stamp yet.
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
	// Sovereign without stamped deployment id — the handover step is
	// behind. UI sees 503 and shows a "waiting for handover" pill rather
	// than looping.
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

