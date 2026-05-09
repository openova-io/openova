// Package handler — applications_update.go: EPIC-2 Slice T+O+P (#1097).
//
// REST surface added on top of slice I:
//
//	PUT    /api/v1/sovereigns/{id}/applications/{name}                — update Application (topology / parameters / version)
//	DELETE /api/v1/sovereigns/{id}/applications/{name}                — uninstall (Application CR delete; controller cascades)
//	POST   /api/v1/sovereigns/{id}/applications/{name}/topology/preview — preview manifests for a new placement
//	POST   /api/v1/sovereigns/{id}/applications/{name}/upgrade/preview  — preview manifests for a new blueprintRef.version
//
// The PUT endpoint accepts a partial body and patches the Application
// CR's spec in place. The application-controller (slice C4 #1133)
// reconciles the resulting fan-out (regions added/removed, version
// upgrade, parameters re-rendered).
//
// Architecture rules:
//
//   - Per ADR-0001 §2.7 the Application CR is the source of truth — no
//     direct Helm/Flux operations from this handler. PUT/DELETE simply
//     modify the CR; the controller catches up.
//   - Per INVIOLABLE-PRINCIPLES.md #5 every mutation requires
//     tier-admin or higher (same shape as install).
//   - Per the brief, active-active → single-region transitions REQUIRE
//     `?force=true` because they SCALE DOWN replicas — a destructive
//     change that a tier-admin should opt into explicitly.
//   - The preview endpoints REUSE applications_preview.go's renderer
//     (one source-of-truth via core/controllers/pkg/render), differing
//     only in how the request body composes (existing CR's settings +
//     proposed delta).
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// applicationUpdateRequest is the body of PUT
// /api/v1/sovereigns/{id}/applications/{name}. All fields are optional:
// the handler patches only the keys that are non-zero. A bare `{}` body
// is a no-op (returns 200 with the CR's current spec).
//
// Accepts both the long form (`parameters`) and the canonical UAT
// matrix's short form (`values`) per
// `feedback_no_mvp_no_workarounds.md`. Long form wins on conflict.
type applicationUpdateRequest struct {
	// BlueprintRef — when Version is non-empty, the version is updated.
	// (We never let a UI rename the blueprint; that's an uninstall +
	// reinstall, not an update.)
	BlueprintRef *applicationBlueprintRef `json:"blueprintRef,omitempty"`
	// Parameters — when non-nil, replaces the spec.parameters tree
	// wholesale (same as install). nil = leave as-is.
	Parameters map[string]interface{} `json:"parameters,omitempty"`
	// Placement — when non-empty Mode, replaces spec.placement +
	// spec.regions. The handler rejects active-active → single-region
	// (or anything that drops regions) without ?force=true.
	Placement *applicationPlacement `json:"placement,omitempty"`

	// ValuesShort is the canonical UAT matrix's alias for Parameters.
	// Collapsed via applicationUpdateRequestNormalize.
	ValuesShort map[string]interface{} `json:"values,omitempty"`
	// VersionShort is the canonical UAT matrix's short alias for
	// `BlueprintRef.Version` on a version-only update — equivalent to
	// `{"blueprintRef":{"version":"x.y.z"}}`.
	VersionShort string `json:"version,omitempty"`
	// ToVersionShort mirrors the upgrade-preview endpoint's `toVersion`
	// shorthand so a caller can issue a one-shot upgrade via PUT
	// `/applications/{name}` with `{"toVersion":"x.y.z"}`. Same
	// resolution path as VersionShort.
	ToVersionShort string `json:"toVersion,omitempty"`
}

// applicationUpdateRequestNormalize collapses the short-form aliases
// onto the canonical fields. Long-form values win on conflict.
func applicationUpdateRequestNormalize(b applicationUpdateRequest) applicationUpdateRequest {
	if b.Parameters == nil && len(b.ValuesShort) > 0 {
		b.Parameters = b.ValuesShort
	}
	short := strings.TrimSpace(b.ToVersionShort)
	if short == "" {
		short = strings.TrimSpace(b.VersionShort)
	}
	if short != "" {
		if b.BlueprintRef == nil {
			b.BlueprintRef = &applicationBlueprintRef{}
		}
		if strings.TrimSpace(b.BlueprintRef.Version) == "" {
			b.BlueprintRef.Version = short
		}
	}
	return b
}

// applicationUpdateResponse mirrors applicationStatusResponse — the UI
// gets back the patched CR's metadata + status snapshot.
type applicationUpdateResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	UID       string                 `json:"uid"`
	Status    map[string]interface{} `json:"status,omitempty"`
}

// applicationDeleteResponse is returned on DELETE to confirm the cascade
// will follow.
type applicationDeleteResponse struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Message   string `json:"message"`
}

// ── HTTP handler — update (PUT) ──────────────────────────────────────

// HandleApplicationUpdate — PUT
// /api/v1/sovereigns/{id}/applications/{name}
//
// Accepts a partial body. Patches the Application CR's spec in place.
// Returns 200 with the patched metadata + current status. The
// application-controller reconciles the fan-out.
func (h *Handler) HandleApplicationUpdate(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "application name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "PUT /applications requires tier-admin or higher",
			})
			return
		}
	}

	var body applicationUpdateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = applicationUpdateRequestNormalize(body)
	if msg, ok := validateApplicationUpdateRequest(body); !ok {
		writeBadRequest(w, "invalid-application-update", msg)
		return
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cur, getErr := getApplicationCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "application-not-found",
				"detail": fmt.Sprintf("Application %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	// Pull the current placement so we can enforce safety rules + carry
	// it forward when the body doesn't override.
	curMode, _, _ := unstructured.NestedString(cur.Object, "spec", "placement")
	curRegionsRaw, _, _ := unstructured.NestedSlice(cur.Object, "spec", "regions")
	curRegions := stringsFromAnySlice(curRegionsRaw)

	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")
	if body.Placement != nil && !force {
		if msg, ok := topologyTransitionAllowed(curMode, curRegions, body.Placement.Mode, body.Placement.Regions); !ok {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "placement-transition-blocked",
				"detail": msg,
				"hint":   "re-issue the PUT with ?force=true to confirm the destructive transition",
			})
			return
		}
	}

	// If the caller is changing the blueprintRef.version OR parameters,
	// re-validate against the target Blueprint's configSchema. The
	// catalog is the source-of-truth for both.
	if body.BlueprintRef != nil && strings.TrimSpace(body.BlueprintRef.Version) != "" {
		if h.catalogClient == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "catalog-not-wired",
				"detail": "version change requires the catalog upstream",
			})
			return
		}
		bpName, _, _ := unstructured.NestedString(cur.Object, "spec", "blueprintRef", "name")
		if bpName == "" {
			bpName = body.BlueprintRef.Name
		}
		bp, fetchErr := h.catalogClient.GetVersion(r.Context(), bpName, body.BlueprintRef.Version, applicationSessionToken(r))
		if fetchErr != nil {
			if errors.Is(fetchErr, ErrBlueprintNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"error":  "blueprint-not-found",
					"detail": fmt.Sprintf("blueprint %s@%s is not in the catalog", bpName, body.BlueprintRef.Version),
				})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "catalog-upstream",
				"detail": fetchErr.Error(),
			})
			return
		}
		// Validate parameters when supplied; otherwise skip — the
		// existing parameters are already reconciled and the controller
		// will surface drift on next pass.
		if body.Parameters != nil {
			rep, vErr := validate.Parameters(blueprintConfigSchema(bp), body.Parameters)
			if vErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{
					"error":  "blueprint-schema-malformed",
					"detail": vErr.Error(),
				})
				return
			}
			if !rep.Valid {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":  "invalid-parameters",
					"detail": "parameters do not satisfy Blueprint.spec.configSchema",
					"errors": rep.Errors,
				})
				return
			}
		}
	} else if body.Parameters != nil && h.catalogClient != nil {
		// Parameter-only edit — re-validate against current version's schema.
		bpName, _, _ := unstructured.NestedString(cur.Object, "spec", "blueprintRef", "name")
		bpVersion, _, _ := unstructured.NestedString(cur.Object, "spec", "blueprintRef", "version")
		if bpName != "" && bpVersion != "" {
			bp, fetchErr := h.catalogClient.GetVersion(r.Context(), bpName, bpVersion, applicationSessionToken(r))
			if fetchErr == nil && bp != nil {
				rep, vErr := validate.Parameters(blueprintConfigSchema(bp), body.Parameters)
				if vErr == nil && !rep.Valid {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"error":  "invalid-parameters",
						"detail": "parameters do not satisfy Blueprint.spec.configSchema",
						"errors": rep.Errors,
					})
					return
				}
			}
		}
	}

	// Build the patched object. Per controller-runtime convention we Get
	// → mutate → Update; controller-runtime's optimistic concurrency
	// surfaces 409 on conflict which we propagate to the caller.
	patched := cur.DeepCopy()
	if body.BlueprintRef != nil && strings.TrimSpace(body.BlueprintRef.Version) != "" {
		_ = unstructured.SetNestedField(patched.Object, body.BlueprintRef.Version, "spec", "blueprintRef", "version")
	}
	if body.Parameters != nil {
		paramsCopy := make(map[string]interface{}, len(body.Parameters))
		for k, v := range body.Parameters {
			paramsCopy[k] = v
		}
		_ = unstructured.SetNestedMap(patched.Object, paramsCopy, "spec", "parameters")
	}
	if body.Placement != nil {
		_ = unstructured.SetNestedField(patched.Object, body.Placement.Mode, "spec", "placement")
		regionsAny := make([]interface{}, 0, len(body.Placement.Regions))
		for _, reg := range body.Placement.Regions {
			regionsAny = append(regionsAny, reg)
		}
		_ = unstructured.SetNestedSlice(patched.Object, regionsAny, "spec", "regions")
	}

	updated, updErr := client.Resource(ApplicationGVR()).Namespace(patched.GetNamespace()).Update(
		r.Context(), patched, metav1.UpdateOptions{})
	if updErr != nil {
		if apierrors.IsConflict(updErr) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "application-conflict",
				"detail": updErr.Error(),
			})
			return
		}
		h.log.Warn("application update: failed",
			"depId", depID, "name", name, "err", updErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-update-failed",
			"detail": updErr.Error(),
		})
		return
	}

	resp := applicationUpdateResponse{
		Name:      updated.GetName(),
		Namespace: updated.GetNamespace(),
		UID:       string(updated.GetUID()),
	}
	if statusObj, ok, _ := unstructured.NestedMap(updated.Object, "status"); ok {
		resp.Status = statusObj
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — uninstall (DELETE) ───────────────────────────────

// HandleApplicationDelete — DELETE
// /api/v1/sovereigns/{id}/applications/{name}
//
// Deletes the Application CR. The application-controller cascades the
// removal: per-region HelmRelease deletion, Org Gitea repo cleanup,
// finalizer removal. Requires tier-admin or higher.
func (h *Handler) HandleApplicationDelete(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "application name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "DELETE /applications requires tier-admin or higher",
			})
			return
		}
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cur, getErr := getApplicationCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "application-not-found",
				"detail": fmt.Sprintf("Application %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	if delErr := client.Resource(ApplicationGVR()).Namespace(cur.GetNamespace()).Delete(
		r.Context(), name, metav1.DeleteOptions{}); delErr != nil {
		if apierrors.IsNotFound(delErr) {
			// Already gone — idempotent success.
			writeJSON(w, http.StatusOK, applicationDeleteResponse{
				Name:      name,
				Namespace: cur.GetNamespace(),
				Message:   "already deleted",
			})
			return
		}
		h.log.Warn("application delete: failed",
			"depId", depID, "name", name, "err", delErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-delete-failed",
			"detail": delErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, applicationDeleteResponse{
		Name:      name,
		Namespace: cur.GetNamespace(),
		Message:   "delete requested; controller will cascade region cleanup",
	})
}

// ── HTTP handler — topology preview (POST) ──────────────────────────

// HandleApplicationTopologyPreview — POST
// /api/v1/sovereigns/{id}/applications/{name}/topology/preview
//
// Reuses the install-preview renderer (applications_preview.go) so the
// "what would happen" simulation is identical to the install path. The
// caller supplies the proposed placement; the handler reads the current
// CR for the rest of the install fields (org/env/parameters/version).
//
// Body shape (only `placement` is required; everything else falls back
// to the current Application CR):
//
//	{
//	  "placement": { "mode": "active-hotstandby", "regions": ["a","b"] },
//	  "parameters": { ... }   // optional override
//	}
func (h *Handler) HandleApplicationTopologyPreview(w http.ResponseWriter, r *http.Request) {
	h.handleApplicationChangePreview(w, r, false)
}

// HandleApplicationUpgradePreview — POST
// /api/v1/sovereigns/{id}/applications/{name}/upgrade/preview?targetVersion=<v>
//
// Same machinery as topology preview, but the body / query carries the
// target Blueprint version. The renderer runs against the target
// Blueprint's manifests so the operator sees the post-upgrade state
// before they commit.
func (h *Handler) HandleApplicationUpgradePreview(w http.ResponseWriter, r *http.Request) {
	h.handleApplicationChangePreview(w, r, true)
}

// applicationChangePreviewRequest is the body of the topology / upgrade
// preview endpoints. Everything is optional; missing fields fall back to
// the current Application CR.
//
// Accepts the canonical UAT matrix's short form for the upgrade-preview
// case: `{"toVersion":"x.y.z"}` is equivalent to
// `{"blueprintRef":{"version":"x.y.z"}}`. The `values` alias maps to
// `parameters` (matches the install/update bodies).
type applicationChangePreviewRequest struct {
	Placement      *applicationPlacement    `json:"placement,omitempty"`
	Parameters     map[string]interface{}   `json:"parameters,omitempty"`
	BlueprintRef   *applicationBlueprintRef `json:"blueprintRef,omitempty"`
	EnvironmentRef string                   `json:"environmentRef,omitempty"`

	// Short-form aliases per the canonical UAT matrix.
	ToVersionShort string                 `json:"toVersion,omitempty"`
	VersionShort   string                 `json:"version,omitempty"`
	ValuesShort    map[string]interface{} `json:"values,omitempty"`
	BlueprintShort string                 `json:"blueprint,omitempty"`
}

// applicationChangePreviewRequestNormalize collapses the short-form
// aliases onto the canonical fields so renderApplicationPreview never
// has to know about the matrix vocabulary.
func applicationChangePreviewRequestNormalize(b applicationChangePreviewRequest) applicationChangePreviewRequest {
	short := strings.TrimSpace(b.ToVersionShort)
	if short == "" {
		short = strings.TrimSpace(b.VersionShort)
	}
	if short != "" {
		if b.BlueprintRef == nil {
			b.BlueprintRef = &applicationBlueprintRef{}
		}
		if strings.TrimSpace(b.BlueprintRef.Version) == "" {
			b.BlueprintRef.Version = short
		}
	}
	if strings.TrimSpace(b.BlueprintShort) != "" {
		if b.BlueprintRef == nil {
			b.BlueprintRef = &applicationBlueprintRef{}
		}
		if strings.TrimSpace(b.BlueprintRef.Name) == "" {
			b.BlueprintRef.Name = strings.TrimSpace(b.BlueprintShort)
		}
	}
	if b.Parameters == nil && len(b.ValuesShort) > 0 {
		b.Parameters = b.ValuesShort
	}
	return b
}

// handleApplicationChangePreview is the shared core for topology +
// upgrade preview. The two endpoints differ only in (a) which "default"
// blueprint version they pull when the body omits it (current CR vs
// query param) and (b) the wire-shape error key.
func (h *Handler) handleApplicationChangePreview(w http.ResponseWriter, r *http.Request, isUpgrade bool) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "missing-name", "application name is required")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if h.catalogClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "catalog-not-wired",
			"detail": "preview requires the catalog upstream",
		})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "preview requires tier-admin or higher",
			})
			return
		}
	}

	var body applicationChangePreviewRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = applicationChangePreviewRequestNormalize(body)

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	cur, getErr := getApplicationCR(r.Context(), client, name, ns)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "application-not-found",
				"detail": fmt.Sprintf("Application %q not found", name),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	// Compose an applicationPreviewRequest by overlaying the body onto
	// the current CR — preview is the install-time machinery applied to
	// "current state + delta".
	curBPName, _, _ := unstructured.NestedString(cur.Object, "spec", "blueprintRef", "name")
	curBPVersion, _, _ := unstructured.NestedString(cur.Object, "spec", "blueprintRef", "version")
	curEnv, _, _ := unstructured.NestedString(cur.Object, "spec", "environmentRef")
	curMode, _, _ := unstructured.NestedString(cur.Object, "spec", "placement")
	curRegionsRaw, _, _ := unstructured.NestedSlice(cur.Object, "spec", "regions")
	curRegions := stringsFromAnySlice(curRegionsRaw)
	curParamsRaw, _, _ := unstructured.NestedMap(cur.Object, "spec", "parameters")

	// Compose the effective target.
	target := applicationPreviewRequest{
		Name:            cur.GetName(),
		OrganizationRef: cur.GetNamespace(),
		EnvironmentRef:  curEnv,
		BlueprintRef:    applicationBlueprintRef{Name: curBPName, Version: curBPVersion},
		Placement:       applicationPlacement{Mode: curMode, Regions: curRegions},
		Parameters:      curParamsRaw,
	}
	if body.EnvironmentRef != "" {
		target.EnvironmentRef = body.EnvironmentRef
	}
	if body.BlueprintRef != nil {
		if strings.TrimSpace(body.BlueprintRef.Name) != "" {
			target.BlueprintRef.Name = body.BlueprintRef.Name
		}
		if strings.TrimSpace(body.BlueprintRef.Version) != "" {
			target.BlueprintRef.Version = body.BlueprintRef.Version
		}
	}
	// Upgrade endpoint: targetVersion query takes precedence, then body.
	if isUpgrade {
		if qv := strings.TrimSpace(r.URL.Query().Get("targetVersion")); qv != "" {
			target.BlueprintRef.Version = qv
		}
	}
	if body.Placement != nil {
		target.Placement = *body.Placement
	}
	if body.Parameters != nil {
		target.Parameters = body.Parameters
	}

	if msg, ok := validateApplicationPreviewRequest(target); !ok {
		writeBadRequest(w, "invalid-application-preview", msg)
		return
	}

	// Hand off to the install-preview renderer via the shared helper.
	resp, status, perr := h.renderApplicationPreview(r, target)
	if perr != nil {
		writeJSON(w, status, perr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Validation + helpers ────────────────────────────────────────────

// validateApplicationUpdateRequest enforces shape rules on the partial
// update body. Unlike validateApplicationInstallRequest it accepts an
// empty body (no-op).
func validateApplicationUpdateRequest(req applicationUpdateRequest) (string, bool) {
	if req.BlueprintRef != nil {
		if strings.TrimSpace(req.BlueprintRef.Version) == "" {
			return "blueprintRef.version is required when blueprintRef is set", false
		}
	}
	if req.Placement != nil {
		if strings.TrimSpace(req.Placement.Mode) == "" {
			return "placement.mode is required when placement is set", false
		}
		switch req.Placement.Mode {
		case "single-region", "active-active", "active-hotstandby":
		default:
			return "placement.mode must be one of single-region, active-active, active-hotstandby", false
		}
		if len(req.Placement.Regions) == 0 {
			return "placement.regions must list at least one region", false
		}
		if len(req.Placement.Regions) > 5 {
			return "placement.regions cannot exceed 5 entries", false
		}
		for i, reg := range req.Placement.Regions {
			if strings.TrimSpace(reg) == "" {
				return fmt.Sprintf("placement.regions[%d] is empty", i), false
			}
		}
	}
	return "", true
}

// topologyTransitionAllowed — guards against destructive transitions
// (anything that scales DOWN replicas) without explicit ?force=true.
//
// Allowed without force:
//   - same mode, same regions (no-op)
//   - same mode, regions ADDED (scale up)
//   - single-region → active-active or active-hotstandby (scale up)
//   - active-hotstandby promote (regions reordered, count same/up)
//
// Blocked without force:
//   - active-active → single-region (replica drop)
//   - any mode → fewer regions
func topologyTransitionAllowed(curMode string, curRegions []string, newMode string, newRegions []string) (string, bool) {
	if newMode == "single-region" && curMode == "active-active" {
		return "active-active → single-region scales down replicas; pass ?force=true to confirm", false
	}
	if newMode == "single-region" && curMode == "active-hotstandby" {
		return "active-hotstandby → single-region drops standby replicas; pass ?force=true to confirm", false
	}
	if len(newRegions) < len(curRegions) {
		return fmt.Sprintf("regions count drops %d → %d; pass ?force=true to confirm", len(curRegions), len(newRegions)), false
	}
	return "", true
}

// stringsFromAnySlice — flattens an unstructured `[]any` of region
// strings into a typed []string. Skips non-string entries (the CRD
// schema constrains them to strings, but be defensive).
func stringsFromAnySlice(in []interface{}) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
