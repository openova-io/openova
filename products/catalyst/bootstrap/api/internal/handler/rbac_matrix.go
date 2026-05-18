// Package handler — rbac_matrix.go: EPIC-3 (#1098) slice A2 access-matrix
// endpoint.
//
// REST surface:
//
//	GET /api/v1/sovereigns/{id}/rbac/access-matrix
//	  ?org=<slug>          (optional filter)
//	  &application=<name>  (optional filter)
//
// The endpoint is the data source for the EPIC-3 U7 access-matrix UI —
// a Manara-style grid of users × applications × tier with inline
// warnings for broken contracts (e.g. developer-tier grants missing
// the auto-injected env-type=dev scope).
//
// Per ADR-0001 §2.7 every row is sourced from a UserAccess CR; the
// endpoint is read-only. There is no mutation surface here — callers
// edit by issuing /rbac/assign or DELETE on a specific UserAccess.
package handler

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── Canonical scope keys (from NAMING-CONVENTION.md §6) ──────────────

const (
	scopeKeyApplication = "openova.io/application"
	scopeKeyEnvType     = "openova.io/env-type"
	scopeKeyOrg         = "openova.io/org"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// AccessMatrixUser is one row in the matrix.
type AccessMatrixUser struct {
	ID       string                          `json:"id"`
	Email    string                          `json:"email,omitempty"`
	Source   string                          `json:"source"` // keycloak | azure_ad_federated
	Access   map[string]AccessMatrixGrant    `json:"access"`
	Warnings []string                        `json:"warnings,omitempty"`
}

// AccessMatrixGrant is a per-Application tier grant attached to a user.
type AccessMatrixGrant struct {
	Tier          string                 `json:"tier"`
	UserAccessRef string                 `json:"userAccessRef"`
	Scopes        []rbacAssignScopeBody  `json:"scopes,omitempty"`
}

// AccessMatrixResponse is the full payload returned by GET
// /rbac/access-matrix. Shape designed for direct consumption by the
// EPIC-3 U7 UI (slice U7).
//
// `OrgFilter` and `ApplicationFilter` echo the request's ?org=
// and ?application= query params back so the UI can render an
// "Org: omantel-platform" pill above the grid without having to
// re-parse the URL. They are omitted when empty.
type AccessMatrixResponse struct {
	Users             []AccessMatrixUser `json:"users"`
	Applications      []string           `json:"applications"`
	Tiers             []string           `json:"tiers"`
	OrgFilter         string             `json:"orgFilter,omitempty"`
	ApplicationFilter string             `json:"applicationFilter,omitempty"`
}

// ── HTTP handler ─────────────────────────────────────────────────────

// HandleRBACAccessMatrix — GET /api/v1/sovereigns/{id}/rbac/access-matrix
func (h *Handler) HandleRBACAccessMatrix(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	h.serveRBACAccessMatrix(w, r, dep, depID)
}

// HandleSovereignRBACMatrix — GET /api/v1/sovereign/rbac/matrix
//
// Chroot-friendly Sovereign-prefix wrapper around HandleRBACAccessMatrix
// (TBD-F4 / C6-007). Same response shape; resolves the active Sovereign
// deployment from the in-cluster context (SOVEREIGN_FQDN +
// CATALYST_SELF_DEPLOYMENT_ID, with the synthesised "sovereign-<fqdn>"
// fallback that the rest of the /api/v1/sovereign/* family uses).
//
// Mirrors the canonical chroot-side seam established by
// HandleSovereignStatus / HandleSovereignApps / HandleSovereignCloud —
// no {id} path param, server-side resolution of the active deployment.
// This is what an operator-side Playwright probe to the Sovereign
// Console / users / rbac matrix view hits without first having to
// resolve `/api/v1/sovereign/self`.
func (h *Handler) HandleSovereignRBACMatrix(w http.ResponseWriter, r *http.Request) {
	depID := resolveSovereignDeploymentID(r)
	if depID == "" {
		// Mothership (no SOVEREIGN_FQDN env, no handover cookie). The
		// per-deployment surface at /api/v1/sovereigns/{id}/rbac/access-matrix
		// is the right entry point on that side; emit a structured 404
		// so the UI can fall back to the mothership form.
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api Pod is not running on a Sovereign cluster; use /api/v1/sovereigns/{id}/rbac/access-matrix instead",
		})
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	h.serveRBACAccessMatrix(w, r, dep, depID)
}

// serveRBACAccessMatrix is the shared body of the per-deployment and the
// Sovereign-prefix RBAC matrix handlers. Both surfaces use identical
// query-param filters (?org, ?application) and identical response
// shape (AccessMatrixResponse).
func (h *Handler) serveRBACAccessMatrix(w http.ResponseWriter, r *http.Request, dep *Deployment, depID string) {
	orgFilter := strings.TrimSpace(r.URL.Query().Get("org"))
	appFilter := strings.TrimSpace(r.URL.Query().Get("application"))

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	listIface, err := client.Resource(UserAccessGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// CRD not installed → empty matrix shape; UI renders the
			// "no grants yet" state.
			writeJSON(w, http.StatusOK, AccessMatrixResponse{
				Users:             []AccessMatrixUser{},
				Applications:      []string{},
				Tiers:             canonicalTierOrder(),
				OrgFilter:         orgFilter,
				ApplicationFilter: appFilter,
			})
			return
		}
		h.log.Warn("rbac.access-matrix: list failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "list-failed",
			"detail": err.Error(),
		})
		return
	}

	resp := buildAccessMatrix(listIface.Items, orgFilter, appFilter)
	resp.OrgFilter = orgFilter
	resp.ApplicationFilter = appFilter
	writeJSON(w, http.StatusOK, resp)
}

// resolveSovereignDeploymentID mirrors HandleSovereignSelf's resolution
// chain so /api/v1/sovereign/* endpoints converge on the same active
// deployment id without having to round-trip through /sovereign/self
// first. Returns "" when no chroot context is detectable (= mothership
// process). Order:
//
//  1. catalyst_session cookie claims (deployment_id) — authoritative
//     post-handover.
//  2. CATALYST_SELF_DEPLOYMENT_ID env (orchestrator chart-values
//     overlay).
//  3. Synthesised "sovereign-<fqdn>" from SOVEREIGN_FQDN /
//     CATALYST_OTECH_FQDN — matches the rest of the /sovereign/*
//     family and the k8scache.factory.go cluster-id convention.
func resolveSovereignDeploymentID(r *http.Request) string {
	if _, jwtDepID, ok := readSessionClaimsFromCookie(r); ok && jwtDepID != "" {
		return jwtDepID
	}
	if v := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID")); v != "" {
		return v
	}
	fqdn := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	}
	if fqdn == "" {
		return ""
	}
	return "sovereign-" + fqdn
}

// ── Pure aggregator (extracted for testability) ──────────────────────

// buildAccessMatrix groups UserAccess CRs by user (Keycloak subject) and
// builds a per-Application tier map. Warnings surface when a CR fails
// the documented Manara contract — e.g. a developer-tier grant without
// `openova.io/env-type=dev`.
//
// Pure function: no apiserver, no clock, no I/O. Test-driven directly
// from envtest fixtures.
func buildAccessMatrix(items []unstructured.Unstructured, orgFilter, appFilter string) AccessMatrixResponse {
	type userBucket struct {
		ID       string
		Email    string
		Source   string
		Grants   map[string]AccessMatrixGrant // application name → grant (highest tier wins)
		Warnings []string
	}
	users := map[string]*userBucket{}
	apps := map[string]struct{}{}

	for i := range items {
		item := &items[i]
		// Identify the user this CR grants to.
		subj := rbacAssignReadKeycloakSubject(item)
		if subj == "" {
			// Group-only grants are out of scope for the access matrix
			// UI's per-user view; future enhancement could surface them
			// as a separate "groups" list.
			continue
		}
		// Read the tier from the canonical label.
		tier := rbacAssignReadTierLabel(item)
		// Read scopes.
		scopes := rbacAssignReadScopes(item)
		// Apply org filter (if set, the CR must carry a matching
		// openova.io/org scope OR the wildcard).
		if orgFilter != "" && !scopeMatchesFilter(scopes, scopeKeyOrg, orgFilter) {
			continue
		}
		// Identify the Application(s) this CR scopes to. A CR with no
		// openova.io/application scope is treated as a "global" grant
		// scoped to every Application — surfaced under a synthetic
		// `*` Application key the UI renders as "all applications".
		appsFromCR := applicationsFromScope(scopes)
		// Apply application filter.
		if appFilter != "" {
			matched := false
			for _, a := range appsFromCR {
				if a == appFilter || a == "*" {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		// Build user bucket if missing.
		bucket, ok := users[subj]
		if !ok {
			bucket = &userBucket{
				ID:     subj,
				Email:  rbacAssignReadEmailHint(item),
				Source: rbacAssignReadSource(item),
				Grants: map[string]AccessMatrixGrant{},
			}
			users[subj] = bucket
		}
		// Per-Application: keep the highest tier on duplicate.
		for _, app := range appsFromCR {
			apps[app] = struct{}{}
			existing, exists := bucket.Grants[app]
			if exists && tierLevel(existing.Tier) >= tierLevel(tier) {
				continue
			}
			bucket.Grants[app] = AccessMatrixGrant{
				Tier:          tier,
				UserAccessRef: item.GetName(),
				Scopes:        scopes,
			}
		}
		// Warnings: developer-tier without env-type=dev scope is a
		// broken Manara contract per design doc §6.2.
		if strings.EqualFold(tier, "developer") && !scopeContains(scopes, scopeKeyEnvType, "dev") {
			for _, app := range appsFromCR {
				bucket.Warnings = append(bucket.Warnings,
					"developer-tier UserAccess for "+app+" missing env-type=dev scope (CR: "+item.GetName()+")")
			}
		}
	}

	// Materialize buckets to sorted output.
	out := AccessMatrixResponse{
		Users:        make([]AccessMatrixUser, 0, len(users)),
		Applications: make([]string, 0, len(apps)),
		Tiers:        canonicalTierOrder(),
	}
	for _, b := range users {
		out.Users = append(out.Users, AccessMatrixUser{
			ID:       b.ID,
			Email:    b.Email,
			Source:   b.Source,
			Access:   b.Grants,
			Warnings: b.Warnings,
		})
	}
	for a := range apps {
		out.Applications = append(out.Applications, a)
	}
	sort.Slice(out.Users, func(i, j int) bool { return out.Users[i].ID < out.Users[j].ID })
	sort.Strings(out.Applications)
	return out
}

// ── Helpers ──────────────────────────────────────────────────────────

// applicationsFromScope returns the list of Application names a
// UserAccess CR scopes to. A CR with an `openova.io/application=foo`
// scope returns ["foo"]. A CR with no application scope returns ["*"]
// (the synthetic "all applications" key the UI renders as a header
// row).
func applicationsFromScope(scopes []rbacAssignScopeBody) []string {
	out := []string{}
	for _, s := range scopes {
		if s.Key == scopeKeyApplication {
			out = append(out, s.Value)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

// scopeMatchesFilter reports whether the scope set carries the given
// key=value pair (or a wildcard for the value).
func scopeMatchesFilter(scopes []rbacAssignScopeBody, key, want string) bool {
	for _, s := range scopes {
		if s.Key != key {
			continue
		}
		if s.Value == want || s.Value == "*" {
			return true
		}
	}
	return false
}

// scopeContains reports whether the scope set carries the exact pair.
func scopeContains(scopes []rbacAssignScopeBody, key, value string) bool {
	for _, s := range scopes {
		if s.Key == key && s.Value == value {
			return true
		}
	}
	return false
}

// canonicalTierOrder returns the 5 tier names in ascending precedence.
// Source-of-truth is core/controllers/internal/labels/CatalogTiers().
// Re-declared here (not imported) because catalyst-api and core/controllers
// are separate Go modules — a duplicated 5-entry constant is the
// pragmatic seam vs. a cross-module dependency on internal/labels.
func canonicalTierOrder() []string {
	return []string{"viewer", "developer", "operator", "admin", "owner"}
}

// tierLevel returns the numeric precedence (10/20/30/40/50). Mirrors
// core/controllers/internal/labels/TierLevel(). 0 for unknown.
func tierLevel(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "viewer":
		return 10
	case "developer":
		return 20
	case "operator":
		return 30
	case "admin":
		return 40
	case "owner":
		return 50
	default:
		return 0
	}
}

// rbacAssignReadEmailHint reads an optional email annotation from a
// UserAccess CR. Catalyst-zero may stamp `catalyst.openova.io/user-email`
// on creation; the matrix surfaces it for UI rendering. Empty string
// when absent.
func rbacAssignReadEmailHint(u *unstructured.Unstructured) string {
	annos := u.GetAnnotations()
	if annos == nil {
		return ""
	}
	return strings.TrimSpace(annos["catalyst.openova.io/user-email"])
}

// rbacAssignReadSource reports whether the UserAccess CR was authored
// for a Keycloak-native user or a federated Azure-AD identity. Source
// is encoded as a label `catalyst.openova.io/identity-source` (per
// EPIC-3 slice F1's Azure SSO federation contract). Defaults to
// "keycloak" when absent.
func rbacAssignReadSource(u *unstructured.Unstructured) string {
	labels := u.GetLabels()
	if labels == nil {
		return "keycloak"
	}
	if v, ok := labels["catalyst.openova.io/identity-source"]; ok && v != "" {
		return v
	}
	return "keycloak"
}
