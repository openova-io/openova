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
	"context"
	"encoding/base64"
	"encoding/json"
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
//
// Resolution order:
//   1. CATALYST_SELF_DEPLOYMENT_ID env (populated by orchestrator overlay).
//   2. Local store fallback — scan /var/lib/catalyst/deployments for a
//      record whose Request.SovereignFQDN matches CATALYST_OTECH_FQDN.
//      The cutover-import endpoint enforces FQDN match before persisting,
//      so a single matching record is unambiguously this Sovereign's.
//      This branch is what makes clean URLs render data on first
//      handover-arrival, before the orchestrator has had a chance to
//      stamp the env via a chart-values overlay write.
//   3. CATALYST_OTECH_FQDN populated but no record yet — return 503.
//   4. Both env empty AND no record → return 404 (mothership).
func (h *Handler) HandleSovereignSelf(w http.ResponseWriter, r *http.Request) {
	deploymentID := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	fqdn := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))

	// Step 1.5: read the Sovereign session cookie's deployment_id /
	// sovereign_fqdn claims (populated by auth_handover.go after the
	// operator follows the handover JWT redirect). The cookie is the
	// authoritative post-handover source — env vars are populated
	// only on the slow path via the orchestrator's chart-values
	// overlay write, which can lag handover by minutes. The store
	// fallback further requires mother to have POSTed the deployment
	// record to the Sovereign (not always the case on BYO).
	// Caught on omantel.biz 2026-05-06.
	if jwtFQDN, jwtDepID, ok := readSessionClaimsFromCookie(r); ok {
		if fqdn == "" {
			fqdn = jwtFQDN
		}
		if deploymentID == "" {
			deploymentID = jwtDepID
		}
	}

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
	// endpoint enforces FQDN match before persisting, so the only
	// record on disk should already be ours.
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

	// Step 3: still no id — handover hasn't fired yet.
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

// readSessionClaimsFromCookie peeks at the catalyst_session cookie's
// payload (middle base64 segment) and extracts sovereign_fqdn +
// deployment_id without verifying the signature. This handler is OUTSIDE
// RequireSession by design (it must serve anonymous bootstrap on
// Catalyst-Zero), so we do best-effort extraction. On Sovereign mode
// the catalyst-api is the same process that minted the cookie at
// /auth/handover, so there's no trust boundary worry — a tampered
// cookie at most flips deploymentId resolution back to the env path.
//
// Returns ok=false on any parse failure (no cookie / malformed JWT /
// unparseable payload). Caller falls through to the env+store path.
func readSessionClaimsFromCookie(r *http.Request) (fqdn, deploymentID string, ok bool) {
	c, err := r.Cookie("catalyst_session")
	if err != nil || c == nil || c.Value == "" {
		return "", "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	var claims struct {
		SovereignFQDN string `json:"sovereign_fqdn"`
		DeploymentID  string `json:"deployment_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", false
	}
	return claims.SovereignFQDN, claims.DeploymentID, true
}

// silence unused-import warning when context isn't directly referenced
// elsewhere in this file's variable wiring.
var _ = context.Background

