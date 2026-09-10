// org_suspend_internal.go — the ServiceAccount-authenticated door for billing
// enforcement (products/chargeback DESIGN.md §9, EPIC #6867).
//
//	POST /api/v1/internal/organizations/{id}/suspend   {"reason": "..."}
//	POST /api/v1/internal/organizations/{id}/resume
//
// Why a second pair of routes
// ───────────────────────────
// The operator routes in org_suspend.go live inside RequireSession: they
// accept a `catalyst_session` cookie minted for a human. The chargeback
// application's collections Enforcer is a Pod — no browser, no cookie jar —
// and until these routes existed it POSTed
// /api/v1/organizations/{slug}/suspend with a bearer nothing on the platform
// could accept, so on a Sovereign it was a Nop: the customer flipped to
// suspended inside chargeback and the Organization kept reconciling.
//
// These routes live OUTSIDE RequireSession, exactly like
// /api/v1/internal/cutover/trigger (#935), and authenticate the SAME way: the
// caller presents its projected ServiceAccount token as `Authorization:
// Bearer`, the apiserver's TokenReview resolves it, and the username must be
// on this route's allow-list (internal_auth.go — orgSuspendCallers:
// system:serviceaccount:chargeback:chargeback by default, overridable with
// CATALYST_INTERNAL_ORG_SUSPEND_SA_USERNAME[S]). The chargeback chart projects
// that token at /var/run/secrets/platform-api/token (products/chargeback/
// chart, platformApi.*) and the binary re-reads the file on every call, so a
// rotated token is picked up without a restart.
//
// What happens next is the operator route's own body
// (stampOrganizationSuspended): spec.suspended + spec.suspendReason are
// merge-patched onto the Organization CR, the ServiceAccount username is
// recorded as the actor, and the org-controller parks the per-Org Flux
// Kustomizations and surfaces the Suspended condition. Responses are the
// apiserver's truth — 404 for an Organization that does not exist, 502 with
// the verbatim detail when the patch is refused — never a 200 over an
// unchanged CR.
package handler

import "net/http"

// HandleInternalSuspendOrganization — POST /api/v1/internal/organizations/{id}/suspend.
func (h *Handler) HandleInternalSuspendOrganization(w http.ResponseWriter, r *http.Request) {
	h.internalSetOrganizationSuspended(w, r, true)
}

// HandleInternalResumeOrganization — POST /api/v1/internal/organizations/{id}/resume.
func (h *Handler) HandleInternalResumeOrganization(w http.ResponseWriter, r *http.Request) {
	h.internalSetOrganizationSuspended(w, r, false)
}

func (h *Handler) internalSetOrganizationSuspended(w http.ResponseWriter, r *http.Request, suspended bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error":  "method-not-allowed",
			"detail": "this endpoint accepts POST only",
		})
		return
	}
	// The cluster client is needed twice — for the TokenReview and for the
	// CR patch — so it is resolved before the bearer is looked at: without
	// it nothing could be verified, and 503 says so.
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.core == nil || deps.dyn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-dynamic-client-unavailable",
			"detail": "no in-cluster client; the caller's token cannot be reviewed and the Organization CR cannot be reached from here",
		})
		return
	}
	actor, ok := h.authenticateInternalCaller(w, r, deps.core, orgSuspendCallers())
	if !ok {
		return
	}
	h.stampOrganizationSuspended(w, r, deps, suspended, actor)
}
