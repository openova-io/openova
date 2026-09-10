// org_suspend.go — billing enforcement on an Organization (products/chargeback
// DESIGN.md §9.7).
//
// The chargeback application suspends an Organization when collections
// escalate, a prepaid balance reaches zero, an operator decides, or the
// operator's external billing system commands it — and resumes it when the
// account is settled. The Organization sync ran only CR → customer; nothing
// let a billing decision reach the platform. This is the NARROWEST reverse
// seam: two operator-only routes that stamp `spec.suspended` (and the
// reason) on the canonical Organization CR through the dynamic client. The
// org-controller honours the flag — it parks the per-Org Flux reconciliation
// (spec.suspend on the per-Org Kustomizations) and surfaces a Suspended
// condition — so a suspended Organization gets nothing new reconciled until
// the flag is cleared. Nothing else on the platform infers suspension; only
// these routes set it.
//
//	POST /api/v1/organizations/{id}/suspend   {"reason": "..."}
//	POST /api/v1/organizations/{id}/resume
//
// The chargeback application has no operator session — it is a Pod with a
// projected ServiceAccount token — so the same stamp is ALSO reachable on the
// ServiceAccount-authenticated internal routes in org_suspend_internal.go
// (POST /api/v1/internal/organizations/{id}/suspend | /resume). Both pairs
// run stampOrganizationSuspended below; they differ only in who may call
// them and in the actor the audit records (the session's email here, the
// ServiceAccount username there).
//
// `{id}` is the provision-record id OR the Organization slug (the chargeback
// application knows the slug). Idempotent: suspending a suspended Organization
// re-stamps the same flag. The apiserver's answer is the response — a 404 when
// there is no such CR, a 502 with the verbatim detail when the patch is
// refused — never a 200 over an unchanged CR (#5426's lesson).
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// orgSuspendResponse is what both routes answer with.
type orgSuspendResponse struct {
	Slug          string `json:"slug"`
	Suspended     bool   `json:"suspended"`
	SuspendReason string `json:"suspend_reason,omitempty"`
	Generation    int64  `json:"generation,omitempty"`
	// Actor is who stamped the flag: the operator session's email on the
	// operator routes, the ServiceAccount username on the internal ones.
	Actor string `json:"actor,omitempty"`
}

// suspendActorAnnotation records, on the Organization CR itself, who last
// changed spec.suspended — the durable audit line an operator can read with
// kubectl next to the flag it explains.
const suspendActorAnnotation = "orgs.openova.io/suspend-actor"

// sessionActor names the operator behind the request for the audit: the
// session's email, else its subject, else a constant that says a session
// (not a ServiceAccount) did it.
func sessionActor(r *http.Request) string {
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		if e := strings.TrimSpace(c.Email); e != "" {
			return e
		}
		if s := strings.TrimSpace(c.Sub); s != "" {
			return s
		}
	}
	return "operator-session"
}

// HandleSuspendOrganization — POST /api/v1/organizations/{id}/suspend.
func (h *Handler) HandleSuspendOrganization(w http.ResponseWriter, r *http.Request) {
	h.setOrganizationSuspended(w, r, true, sessionActor(r))
}

// HandleResumeOrganization — POST /api/v1/organizations/{id}/resume.
func (h *Handler) HandleResumeOrganization(w http.ResponseWriter, r *http.Request) {
	h.setOrganizationSuspended(w, r, false, sessionActor(r))
}

// organizationSlugFor resolves `{id}` to the Organization slug: a provision
// record's subdomain when the id names one, else the id itself as a slug.
func (h *Handler) organizationSlugFor(id string) string {
	id = strings.TrimSpace(id)
	if deps := h.orgTenantDeps; deps.Store != nil {
		if rec, ok := deps.Store.Get(id); ok && strings.TrimSpace(rec.Subdomain) != "" {
			return strings.ToLower(strings.TrimSpace(rec.Subdomain))
		}
	}
	return strings.ToLower(id)
}

// setOrganizationSuspended resolves the in-cluster client and stamps.
func (h *Handler) setOrganizationSuspended(w http.ResponseWriter, r *http.Request, suspended bool, actor string) {
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-dynamic-client-unavailable",
			"detail": "no in-cluster client; the Organization CR cannot be reached from here",
		})
		return
	}
	h.stampOrganizationSuspended(w, r, deps, suspended, actor)
}

// stampOrganizationSuspended is the shared body of the operator and the
// internal routes: resolve `{id}` to a slug, read the reason, merge-patch the
// CR, answer with the apiserver's truth.
func (h *Handler) stampOrganizationSuspended(w http.ResponseWriter, r *http.Request, deps *sovereignDeps, suspended bool, actor string) {
	slug := h.organizationSlugFor(chi.URLParam(r, "id"))
	if !orgSlugRE.MatchString(slug) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		// An empty body is fine (resume needs none); a malformed one is not.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid-json-body"})
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if !suspended {
		reason = ""
	}
	obj, err := patchOrganizationSuspended(r.Context(), deps, slug, suspended, reason, actor)
	switch {
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
		return
	case err != nil:
		h.log.Error("org-suspend: Organization CR patch FAILED", "slug", slug, "suspended", suspended, "actor", actor, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "organization-cr-patch-failed",
			"detail": truncate(err.Error(), 512),
			"hint":   "the Organization CR is unchanged; this route is idempotent — re-issue it once the apiserver permits the patch",
		})
		return
	}
	got, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspended")
	why, _, _ := unstructured.NestedString(obj.Object, "spec", "suspendReason")
	h.log.Info("org-suspend: Organization CR stamped", "slug", slug, "suspended", got, "reason", why, "actor", actor)
	writeJSON(w, http.StatusOK, orgSuspendResponse{Slug: slug, Suspended: got, SuspendReason: why, Generation: obj.GetGeneration(), Actor: actor})
}

// patchOrganizationSuspended merge-patches the two spec fields on the CR and
// records the actor beside them. An empty actor leaves the annotation as it
// was.
func patchOrganizationSuspended(ctx context.Context, deps *sovereignDeps, slug string, suspended bool, reason, actor string) (*unstructured.Unstructured, error) {
	body := map[string]any{"spec": map[string]any{"suspended": suspended, "suspendReason": reason}}
	if actor = strings.TrimSpace(actor); actor != "" {
		body["metadata"] = map[string]any{"annotations": map[string]any{suspendActorAnnotation: actor}}
	}
	patch, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return deps.dyn.Resource(organizationGVR()).Patch(ctx, slug, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sovereign-admin-api/org-suspend"})
}
