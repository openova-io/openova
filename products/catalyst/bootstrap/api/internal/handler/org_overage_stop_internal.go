// org_overage_stop_internal.go — the ServiceAccount-authenticated door for the
// BSS spend limit (products/chargeback DESIGN.md §22.11, #6971).
//
//	POST /api/v1/internal/organizations/{id}/overage-stop    {"until": "<RFC3339>", "reason": "..."}
//	POST /api/v1/internal/organizations/{id}/overage-resume  {}
//
// A grow Organization may set a monthly spend limit on the usage above its
// package. When the chargeback application's evaluator finds the period's
// accrued usage above the package has reached that limit, the usage must STOP
// — not be given away on the bill. The stop is the quota: the
// organization-controller renders a grow Organization capped (the package
// headline on the host plan-quota limits, the package disk on the in-vCluster
// app-storage quota) while spec.overageStoppedUntil is in the future, and
// returns it to grow on its own once that instant — the next billing-period
// start — has passed.
//
// This is the same reverse seam the collections enforcer already uses for
// suspension (org_suspend_internal.go): the chargeback Pod presents its
// projected ServiceAccount token, the apiserver's TokenReview resolves it, the
// username must be on the same allow-list (orgSuspendCallers), and the routes
// merge-patch two spec fields on the canonical Organization CR — nothing else.
// overage-stop stamps spec.overageStoppedUntil + spec.overageStopReason;
// overage-resume clears both (JSON null in the merge patch). The actor is
// recorded in the orgs.openova.io/overage-stop-actor annotation. Responses are
// the apiserver's truth: 404 for an Organization that does not exist, 502 with
// the verbatim detail when the patch is refused, 400 for a missing or
// unparseable `until`.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// overageStopActorAnnotation records who last stamped or cleared the stop.
const overageStopActorAnnotation = "orgs.openova.io/overage-stop-actor"

// orgOverageStopResponse is what both routes answer with.
type orgOverageStopResponse struct {
	Slug                string `json:"slug"`
	OverageStoppedUntil string `json:"overage_stopped_until"`
	Actor               string `json:"actor,omitempty"`
}

// HandleInternalOverageStopOrganization — POST /api/v1/internal/organizations/{id}/overage-stop.
func (h *Handler) HandleInternalOverageStopOrganization(w http.ResponseWriter, r *http.Request) {
	h.internalSetOrganizationOverageStop(w, r, true)
}

// HandleInternalOverageResumeOrganization — POST /api/v1/internal/organizations/{id}/overage-resume.
func (h *Handler) HandleInternalOverageResumeOrganization(w http.ResponseWriter, r *http.Request) {
	h.internalSetOrganizationOverageStop(w, r, false)
}

func (h *Handler) internalSetOrganizationOverageStop(w http.ResponseWriter, r *http.Request, stop bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error":  "method-not-allowed",
			"detail": "this endpoint accepts POST only",
		})
		return
	}
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
	slug := h.organizationSlugFor(chi.URLParam(r, "id"))
	if !orgSlugRE.MatchString(slug) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
		return
	}
	var body struct {
		Until  string `json:"until"`
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid-json-body"})
			return
		}
	}
	var until, reason string
	if stop {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(body.Until))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid-until",
				"detail": "until must be an RFC3339 instant — the next billing-period start the stop lifts at, e.g. 2026-11-01T00:00:00Z",
			})
			return
		}
		until = t.UTC().Format(time.RFC3339)
		if reason = strings.TrimSpace(body.Reason); len(reason) > 512 {
			reason = reason[:512]
		}
	}
	obj, err := patchOrganizationOverageStop(r.Context(), deps, slug, stop, until, reason, actor)
	switch {
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org-tenant-not-found"})
		return
	case err != nil:
		h.log.Error("org-overage-stop: Organization CR patch FAILED", "slug", slug, "stop", stop, "until", until, "actor", actor, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "organization-cr-patch-failed",
			"detail": truncate(err.Error(), 512),
			"hint":   "the Organization CR is unchanged; this route is idempotent — re-issue it once the apiserver permits the patch",
		})
		return
	}
	got, _, _ := unstructured.NestedString(obj.Object, "spec", "overageStoppedUntil")
	h.log.Info("org-overage-stop: Organization CR stamped", "slug", slug, "overage_stopped_until", got, "reason", reason, "actor", actor)
	writeJSON(w, http.StatusOK, orgOverageStopResponse{Slug: slug, OverageStoppedUntil: got, Actor: actor})
}

// patchOrganizationOverageStop merge-patches spec.overageStoppedUntil and
// spec.overageStopReason — set on a stop, removed (null) on a resume — and
// records the actor beside them.
func patchOrganizationOverageStop(ctx context.Context, deps *sovereignDeps, slug string, stop bool, until, reason, actor string) (*unstructured.Unstructured, error) {
	spec := map[string]any{"overageStoppedUntil": nil, "overageStopReason": nil}
	if stop {
		spec["overageStoppedUntil"] = until
		spec["overageStopReason"] = reason
	}
	body := map[string]any{"spec": spec}
	if actor = strings.TrimSpace(actor); actor != "" {
		body["metadata"] = map[string]any{"annotations": map[string]any{overageStopActorAnnotation: actor}}
	}
	patch, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return deps.dyn.Resource(organizationGVR()).Patch(ctx, slug, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sovereign-admin-api/org-overage-stop"})
}
