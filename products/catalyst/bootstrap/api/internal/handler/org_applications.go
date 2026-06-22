// org_applications.go — the Org-scoped Application install path (#4116).
//
// THE PROBLEM this closes: a customer Org-scoped session (a User on their
// own console.<org>.<pool> console, tier=org-admin per org_scope.go) cannot
// install an Application via the Sovereign-admin seam
// `POST /api/v1/sovereigns/{id}/applications` (HandleApplicationInstall),
// because:
//
//   1. OrgScopeGuard (org_scope.go) deny-by-defaults the
//      `/api/v1/sovereigns/...` surface for org sessions (the #4110/#4112
//      privilege-escalation fix) → 403 org-scoped-forbidden before the
//      handler runs.
//   2. Even if it reached the handler, applicationInstallCallerAuthorized
//      rejects tier=org-admin (it wants tier-admin/owner/sovereign-admin).
//   3. orgNamespace(ref) is a pure slugger and never consults the tenant
//      registry — a free-subdomain Org (e.g. slug "demo", real namespace
//      `org-<tenant-uuid>`) would land the CR in the wrong namespace.
//
// This is exactly what blocked the agentic journey: the bp-agenity solo
// agent's openova-mcp create_application tool forwards the User's Org-scoped
// session bearer, which got 403'd at the Sovereign seam.
//
// THE FIX (Approach A — respects the #4110/#4112 model, no surface widening):
// a NEW Org-scoped route `POST /api/v1/org/applications` that is ALREADY on
// the OrgScopeGuard allowlist (orgSafePathPrefixes in org_scope.go). It:
//
//   - resolves the caller's OWN Organization via resolveOrganization() — the
//     same host→tenant-registry seam HandleCreateOrgUser uses, which returns
//     tenant.OrganizationNamespace (the real `org-<uuid>` namespace, solving
//     problem 3) AND enforces the #4110 cross-org binding (an Org-scoped
//     session can only ever resolve its OWN Org, solving the confinement
//     requirement),
//   - forces body.OrganizationRef to that real namespace so the shared
//     install core writes the Application CR into the caller's own namespace,
//   - reuses h.installApplicationCore — the SAME catalog-fetch → validate →
//     ensure-namespace → create-CR core the Sovereign path uses (no
//     duplicated CR-write logic),
//   - applies NO applicationInstallCallerAuthorized tier gate: reaching this
//     route already proves an Org-scoped session confined to its own Org by
//     resolveOrganization's binding — that IS the authz boundary.
//
// The Sovereign surface stays sealed: `/api/v1/sovereigns/{id}/applications`
// is unchanged and still 403s org sessions. Only the dedicated
// `/api/v1/org/applications` own-org route is opened.

package handler

import (
	"net/http"
	"os"
	"strings"
)

// HandleOrgApplicationInstall — POST /api/v1/org/applications.
//
// Installs an Application into the CALLER'S OWN Organization. The target
// namespace is resolved server-side from the request host (X-Tenant-Host)
// via the tenant registry — the client cannot point this at another Org
// (resolveOrganization enforces the #4110 own-org binding). The body is the
// SAME applicationInstallRequest shape the Sovereign install seam accepts
// (long-form or short-form), minus organizationRef/namespace, which are
// forced to the resolved Org namespace and IGNORED if supplied.
func (h *Handler) HandleOrgApplicationInstall(w http.ResponseWriter, r *http.Request) {
	// Resolve the caller's own Organization (host → tenant registry). This
	// returns tenant.OrganizationNamespace (the real `org-<uuid>` namespace)
	// and enforces the #4110 cross-org binding: an Org-scoped session may
	// only ever resolve its OWN Org (a forged X-Tenant-Host for a sibling
	// Org is 403 org-scope-mismatch).
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	orgNS := strings.TrimSpace(tenant.OrganizationNamespace)
	if orgNS == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "org-namespace-unresolved",
			"detail": "tenant registry has no org_tenant_namespace for this Organization",
		})
		return
	}

	// Resolve the local Sovereign deployment. On a Sovereign chroot, ANY
	// non-empty id resolves lookupDeploymentForInfra → chrootEnsureDeployment
	// → the in-cluster client (sovereignDynamicClient), so the synthesized
	// self id is sufficient. This mirrors HandleSovereignSelf's id-resolution
	// ladder so the org path uses the same single self-registered cluster.
	depID := h.selfDeploymentID(r)
	dep, found := h.lookupDeploymentForInfra(depID)
	if !found {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "deployment-unresolved",
			"detail": "could not resolve the local Sovereign deployment for an org-scoped install",
		})
		return
	}

	// Decode + normalize + validate the body, identical to the Sovereign
	// path, so the same dual-shape (long/short-form) contract holds.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationInstallBody(rawBody)
	if decodeErr != nil {
		writeApplicationInstallSoftError(w, "invalid-body", http.StatusBadRequest, decodeErr.Error())
		return
	}

	// FORCE the target Org to the caller's own resolved namespace — the
	// client's organizationRef / namespace are IGNORED so an org session can
	// never create outside its own Org. orgNamespace() is idempotent on an
	// already-valid RFC-1123 label (the `org-<uuid>` namespace is one), so
	// the shared core's orgNamespace(body.OrganizationRef) == orgNS.
	body.OrganizationRef = orgNS
	body.NamespaceShort = ""

	body = applicationInstallRequestNormalize(body)
	if msg, ok := validateApplicationInstallRequest(body); !ok {
		writeApplicationInstallSoftError(w, "invalid-application-install", http.StatusBadRequest, msg)
		return
	}

	// No applicationInstallCallerAuthorized tier gate: reaching this
	// OrgScopeGuard-allowlisted, own-org-bound route IS the authz boundary
	// for an Org-scoped session (it can only ever touch its own Org).
	h.installApplicationCore(w, r, dep, depID, body)
}

// selfDeploymentID resolves the local Sovereign's deployment id for an
// org-scoped request, mirroring HandleSovereignSelf's ladder: the explicit
// self-id env, then the session cookie claims, then a stable id synthesized
// from SOVEREIGN_FQDN. Any non-empty value resolves to the in-cluster client
// on a Sovereign chroot.
func (h *Handler) selfDeploymentID(r *http.Request) string {
	if id := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID")); id != "" {
		return id
	}
	if _, jwtDepID, ok := readSessionClaimsFromCookie(r); ok && strings.TrimSpace(jwtDepID) != "" {
		return strings.TrimSpace(jwtDepID)
	}
	fqdn := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	}
	if fqdn != "" {
		return "sovereign-" + fqdn
	}
	return ""
}
