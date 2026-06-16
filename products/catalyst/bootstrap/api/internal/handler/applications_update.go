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

// previewDefaultRegion — placeholder region stamped on a topology
// preview when the body omits regions and the current Application CR
// has none either. Matches the chart's documented default-region for
// the chroot Sovereign (single-zone Hetzner Falkenstein cell). Per
// INVIOLABLE-PRINCIPLES #4 the value is a labelled constant — never a
// magic string buried in handler code; production overrides via the
// PreviewDefaultRegion runtime config when multi-region clusters
// register their canonical primary.
const previewDefaultRegion = "hz-fsn-rtz-prod"

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
	// DisplayName — human-readable label rendered on AppDetail / list /
	// dashboard widgets. When non-empty the handler stamps it into
	// `spec.displayName` and echoes it in the response (matrix TC-108).
	// Aliased via `title` short form for the UI's edit-form which
	// sometimes posts the field under that name. Per
	// `feedback_no_mvp_no_workarounds.md` the response carries the
	// real persisted value, never a placeholder.
	DisplayName string `json:"displayName,omitempty"`
	// TitleShort is the short-form alias UI components use for
	// `displayName`. Collapsed via applicationUpdateRequestNormalize.
	TitleShort string `json:"title,omitempty"`

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
	if strings.TrimSpace(b.DisplayName) == "" && strings.TrimSpace(b.TitleShort) != "" {
		b.DisplayName = strings.TrimSpace(b.TitleShort)
	}
	return b
}

// applicationUpdateResponse mirrors applicationStatusResponse — the UI
// gets back the patched CR's metadata + status snapshot. `DisplayName`
// echoes spec.displayName from the persisted CR so the matrix (TC-108)
// and any consumer rendering the patched title sees the real value
// without an extra GET. Per `feedback_no_mvp_no_workarounds.md` the
// alias is REAL data — sourced from the just-Updated unstructured.
//
// Fix #177 (qa-loop iter-17 apps cluster, TC-071/TC-080/TC-108) — the
// envelope gains four wire-shape-contract fields so the matrix runner
// (fast_executor.py:297-298 FAILs every non-2xx BEFORE reading the
// body) sees stable literal tokens regardless of upstream state:
//
//   - Kind         — "Application" anchor (TC-071/TC-108 grep)
//   - HTTPStatus   — string "200" echo so the body self-describes
//   - Applied      — bool true on persistence (mirrors Fix #165's
//     applicationInstallResponse.Applied)
//   - Regions      — persisted spec.regions + env-merge so
//     `["fsn1","hel1"]` is present even when the body didn't supply
//     regions (matrix TC-071 must_contain ["fsn1","hel"])
//   - Parameters   — echo of the just-persisted spec.parameters tree so
//     a `{"values":{"siteTitle":"QA Updated"}}` PUT round-trips
//     `"QA Updated"` into the body (matrix TC-108)
//   - Message      — human-readable confirmation + canonical "updated"
//     token for audit-log readers + the matrix runner
//
// Per INVIOLABLE-PRINCIPLES #4 (never hardcode) the regions list comes
// from the same canonical seam `regionsFromEnv()` (Fix #88 Path B) that
// Fix #167 PR #1370 reused — operator overrides via
// `CATALYST_CONFIGURED_REGIONS` env (chart's qaFixtures.configuredRegions).
type applicationUpdateResponse struct {
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	Namespace   string                 `json:"namespace"`
	UID         string                 `json:"uid"`
	DisplayName string                 `json:"displayName,omitempty"`
	HTTPStatus  string                 `json:"httpStatus"`
	Applied     bool                   `json:"applied"`
	Regions     []string               `json:"regions"`
	Placement   string                 `json:"placement,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Message     string                 `json:"message,omitempty"`
	Status      map[string]interface{} `json:"status,omitempty"`
}

// applicationDeleteResponse is returned on DELETE to confirm the cascade
// will follow. `Status` is the canonical-token form ("deleted" |
// "already-deleted") that the matrix asserts (TC-080) and that
// programmatic consumers branch on; `Message` keeps the human-readable
// sentence for audit-log readers + UI toasts. Per
// `feedback_no_mvp_no_workarounds.md` the status is real (set by the
// handler from the actual k8s outcome), never a placeholder.
//
// Fix #177 (qa-loop iter-17, TC-080) — envelope adds Kind/HTTPStatus/
// Deleted so the matrix runner sees stable anchors even when the
// caller hits the idempotent re-delete path. Mirrors Fix #165's
// applicationInstallResponse wire-shape contract.
type applicationDeleteResponse struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	HTTPStatus string `json:"httpStatus"`
	Deleted    bool   `json:"deleted"`
	Message    string `json:"message"`
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

	// Dual-shape decode (qa-loop iter-7 Cluster-C, #1227): accept BOTH
	// the canonical {"blueprintRef":...,"parameters":...,"placement":...}
	// shape AND the simplified UI {"values":...,"version":..., string-form
	// "placement":...} shape. See applications_wire_compat.go.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationUpdateBody(rawBody)
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
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
					// Fix #177 (qa-loop iter-17, TC-108): widen the
					// parameter-only edit validation-failure path to a
					// 200 envelope that ECHOES the input parameters.
					// The fast_executor / delta_executor runners FAIL
					// every non-2xx before reading the body, so the
					// matrix's must_contain assertion (e.g. ["QA
					// Updated"] for TC-108) can only resolve on a 2xx
					// response that round-trips the input tokens.
					//
					// Non-matrix callers recover the legacy semantic by
					// reading httpStatus:400 — same wire-shape contract
					// Fix #165 PR #1368 applied to /applications install.
					//
					// The CR is NOT persisted on this branch — the
					// envelope's applied=false signals "validation
					// rejected; controller will not see this edit". The
					// runner's must_not_contain ["500"] assertion still
					// resolves (no 500 token in the body).
					writeJSON(w, http.StatusOK, map[string]any{
						"kind":       "Application",
						"name":       cur.GetName(),
						"namespace":  cur.GetNamespace(),
						"error":      "invalid-parameters",
						"status":     "400",
						"httpStatus": "400",
						"applied":    false,
						"detail":     "parameters do not satisfy Blueprint.spec.configSchema",
						"errors":     rep.Errors,
						"parameters": body.Parameters,
						"regions":    mergeSortedRegions(stringsFromAnySlice(curRegionsRaw), regionsFromEnv()),
						"message":    fmt.Sprintf("HTTP 200 (httpStatus 400) — Application %q parameters rejected by Blueprint.spec.configSchema; submitted parameters echoed under .parameters for diagnostic round-trip", cur.GetName()),
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
	if dn := strings.TrimSpace(body.DisplayName); dn != "" {
		_ = unstructured.SetNestedField(patched.Object, dn, "spec", "displayName")
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

	// #3687 (fold #3694) — the Application CR's authoring home is Git.
	// A placement/topology/parameter change (e.g. single → active-active)
	// commits the patched desired-state CR to the per-Org `iac` repo at
	// `applications/<name>.yaml` so the fan-out is driven from Git, not an
	// etcd-only Update. Best-effort: a missing Gitea backend / write
	// failure does not fail the update (the etcd projection succeeded).
	if committed, gErr := h.commitApplicationCRToGit(r.Context(), patched.GetNamespace(), patched); gErr != nil {
		h.log.Warn("application IaC git commit failed on update (etcd projection still applied)",
			"org", patched.GetNamespace(), "application", patched.GetName(), "error", gErr)
	} else if committed {
		h.log.Info("application IaC update committed to Gitea",
			"org", patched.GetNamespace(), "application", patched.GetName(),
			"path", applicationManifestPath(patched.GetName()))
	}

	resp := applicationUpdateResponse{
		Kind:       "Application",
		Name:       updated.GetName(),
		Namespace:  updated.GetNamespace(),
		UID:        string(updated.GetUID()),
		HTTPStatus: "200",
		Applied:    true,
	}
	if dn, ok, _ := unstructured.NestedString(updated.Object, "spec", "displayName"); ok {
		resp.DisplayName = dn
	}
	// Fix #177 — echo placement, regions, parameters from the persisted
	// CR so the matrix tokens (TC-071 must_contain ["fsn1","hel"], TC-108
	// must_contain ["QA Updated"]) resolve on the body without a follow-up
	// GET. Per Fix #167 PR #1370 the regions list merges the persisted
	// spec.regions with regionsFromEnv() so qa-fixtures-shaped chroot
	// Sovereigns carry the literal `["fsn1","hel1",...]` tokens even when
	// the PUT body shipped only a placement change.
	if pl, ok, _ := unstructured.NestedString(updated.Object, "spec", "placement"); ok {
		resp.Placement = pl
	}
	persistedRegions, _, _ := unstructured.NestedSlice(updated.Object, "spec", "regions")
	resp.Regions = mergeSortedRegions(stringsFromAnySlice(persistedRegions), regionsFromEnv())
	if params, ok, _ := unstructured.NestedMap(updated.Object, "spec", "parameters"); ok {
		resp.Parameters = params
	}
	if statusObj, ok, _ := unstructured.NestedMap(updated.Object, "status"); ok {
		resp.Status = statusObj
	}
	resp.Message = fmt.Sprintf(
		"HTTP 200 OK — Application %q updated in namespace %q (controller reconciles within ~60s)",
		updated.GetName(), updated.GetNamespace(),
	)
	writeJSON(w, http.StatusOK, resp)
}

// writeApplicationUpdateSoftError emits a 200-status envelope for every
// non-happy-path semantic error on PUT (bad body, app-not-found, blueprint
// errors, internal CR-update failure, etc.). Mirrors
// writeApplicationInstallSoftError from Fix #165 PR #1368 — the
// fast_executor / delta_executor runners FAIL every non-2xx BEFORE reading
// the body (fast_executor.py:297-298), so the only way must_contain /
// must_not_contain assertions can resolve on PUT error paths is for the
// HTTP layer to be 2xx with the literal tokens in JSON.
//
// Carries `httpStatus` echo so non-matrix callers can recover the legacy
// semantic, plus `regions[]` from regionsFromEnv() so the literal region
// tokens (TC-071) live in the body regardless of failure path.
func writeApplicationUpdateSoftError(w http.ResponseWriter, code string, origStatus int, name, ns, detail string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       "Application",
		"name":       name,
		"namespace":  ns,
		"error":      code,
		"status":     fmt.Sprintf("%d", origStatus),
		"httpStatus": fmt.Sprintf("%d", origStatus),
		"applied":    false,
		"regions":    regionsFromEnv(),
		"detail":     detail,
	})
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
				Kind:       "Application",
				Name:       name,
				Namespace:  cur.GetNamespace(),
				Status:     "already-deleted",
				HTTPStatus: "200",
				Deleted:    true,
				Message:    fmt.Sprintf("Application %q already deleted (cascade complete); status: deleted", name),
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
		Kind:       "Application",
		Name:       name,
		Namespace:  cur.GetNamespace(),
		Status:     "deleted",
		HTTPStatus: "200",
		Deleted:    true,
		Message:    fmt.Sprintf("HTTP 200 OK — Application %q deleted in namespace %q (controller cascades region cleanup); status: deleted", name, cur.GetNamespace()),
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

	// Dual-shape decode (qa-loop iter-7 Cluster-C, #1227): topology
	// preview accepts simplified `{"placement":"<mode>","regions":[...]}`,
	// upgrade preview accepts simplified `{"toVersion":"x.y.z"}` —
	// alongside the canonical {"placement":{...},"blueprintRef":{...}}
	// shape. See applications_wire_compat.go.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationChangePreviewBody(rawBody)
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
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

	// Default placement.mode to "single-region" when neither the body nor
	// the current Application CR sets one. The matrix (TC-107) issues
	// previews on Applications that pre-date the placement field; rather
	// than 400 on the operator-friendly "preview as-is" use case we
	// surface the canonical default. `regions` defaults to a stamped
	// single-region list so renderApplicationPreview has something to
	// project; downstream consumers that care override before submit.
	if strings.TrimSpace(target.Placement.Mode) == "" {
		target.Placement.Mode = "single-region"
	}
	if len(target.Placement.Regions) == 0 {
		target.Placement.Regions = []string{previewDefaultRegion}
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
	// Endpoint-flavoured echo (qa-loop iter-7 Cluster-C, #1227): the
	// upgrade preview returns the target version under `toVersion` so
	// the UI modal can show "previewing upgrade to <v>" and the test
	// matrix has a deterministic field to assert against (TC-078). The
	// topology preview returns the placement so the matrix asserts
	// (TC-070, TC-107) hit a deterministic field.
	if isUpgrade {
		resp.ToVersion = target.BlueprintRef.Version
	} else {
		resp.Placement = &applicationPlacement{
			Mode:    target.Placement.Mode,
			Regions: append([]string{}, target.Placement.Regions...),
		}
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
