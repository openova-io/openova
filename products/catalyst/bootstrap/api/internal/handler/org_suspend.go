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
}

// HandleSuspendOrganization — POST /api/v1/organizations/{id}/suspend.
func (h *Handler) HandleSuspendOrganization(w http.ResponseWriter, r *http.Request) {
	h.setOrganizationSuspended(w, r, true)
}

// HandleResumeOrganization — POST /api/v1/organizations/{id}/resume.
func (h *Handler) HandleResumeOrganization(w http.ResponseWriter, r *http.Request) {
	h.setOrganizationSuspended(w, r, false)
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

func (h *Handler) setOrganizationSuspended(w http.ResponseWriter, r *http.Request, suspended bool) {
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
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-dynamic-client-unavailable",
			"detail": "no in-cluster client; the Organization CR cannot be reached from here",
		})
		return
	}
	obj, err := patchOrganizationSuspended(r.Context(), deps, slug, suspended, reason)
	switch {
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
		return
	case err != nil:
		h.log.Error("org-suspend: Organization CR patch FAILED", "slug", slug, "suspended", suspended, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "organization-cr-patch-failed",
			"detail": truncate(err.Error(), 512),
			"hint":   "the Organization CR is unchanged; this route is idempotent — re-issue it once the apiserver permits the patch",
		})
		return
	}
	got, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspended")
	why, _, _ := unstructured.NestedString(obj.Object, "spec", "suspendReason")
	h.log.Info("org-suspend: Organization CR stamped", "slug", slug, "suspended", got, "reason", why)
	writeJSON(w, http.StatusOK, orgSuspendResponse{Slug: slug, Suspended: got, SuspendReason: why, Generation: obj.GetGeneration()})
}

// patchOrganizationSuspended merge-patches the two spec fields on the CR.
func patchOrganizationSuspended(ctx context.Context, deps *sovereignDeps, slug string, suspended bool, reason string) (*unstructured.Unstructured, error) {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"suspended": suspended, "suspendReason": reason}})
	if err != nil {
		return nil, err
	}
	return deps.dyn.Resource(organizationGVR()).Patch(ctx, slug, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sovereign-admin-api/org-suspend"})
}
