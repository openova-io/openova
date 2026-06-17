// Package handler — applications_preview.go: EPIC-2 Slice I (#1097)
// preview-before-install endpoint.
//
// REST surface:
//
//	POST /api/v1/sovereigns/{id}/applications/preview
//
// Body shape (mirrors the install POST except no `name` is required —
// preview is a what-would-happen call):
//
//	{
//	  "blueprintRef":   { "name": "bp-wordpress", "version": "1.2.3" },
//	  "name":           "wp-prod",  // optional; defaults to "<blueprint>-preview"
//	  "organizationRef":"acme",
//	  "environmentRef": "acme-prod",
//	  "parameters":     { "domain": "shop.acme.com", ... },
//	  "placement":      { "mode": "single-region", "regions": ["hz-fsn-rtz-prod"] }
//	}
//
// Response shape (consumed by I2's "Preview" modal AND, per the brief,
// EPIC-2 T's topology editor for "preview before topology change"):
//
//	{
//	  "manifests": [
//	    { "path": "clusters/<region>/applications/<app>/kustomization.yaml",
//	      "content": "..." },
//	    { "path": "clusters/<region>/applications/<app>/helmrelease.yaml",
//	      "content": "..." }
//	  ],
//	  "diff":      "<unified-diff-when-the-target-already-exists-OR-empty>",
//	  "blueprint": { "name": "bp-wordpress", "version": "1.2.3" },
//	  "warnings":  ["..."]   // empty when nothing was flagged
//	}
//
// Behavior contract:
//
//	200 OK     — preview rendered. Manifests + diff returned.
//	400        — invalid body, parameters fail JSON-Schema validation.
//	403        — caller lacks tier-admin or higher.
//	404        — Sovereign or Blueprint unknown.
//	502        — catalog upstream error or render failure (Blueprint bug).
//	503        — catalog client unwired.
//
// Architecture rules:
//
//   - Per ADR-0001 §2.7 the preview is read-only — no Application CR
//     is created and no Gitea write happens. The endpoint is a pure
//     simulation that runs the same renderer the application-controller
//     uses (core/controllers/pkg/render, promoted from internal/ in
//     this slice) so a "looks-good in preview" never diverges from the
//     "actually installed" outcome.
//   - Per INVIOLABLE-PRINCIPLES.md #2 the renderer source-of-truth is
//     the same code in both places. The promotion to pkg/ exists for
//     exactly this reason.
//   - The diff is currently EMPTY: catalyst-api does not yet read the
//     per-Org Gitea repo state for diffing in slice I. A follow-up
//     slice can wire `core/controllers/pkg/gitea` here to compute the
//     true unified diff vs current state. The wire-shape is
//     forward-compatible.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the renderer config (interval
// seconds, source kind, source ref) is read from the Blueprint, never
// hardcoded.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/core/controllers/pkg/render"
	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// applicationPreviewRequest is the body of POST .../applications/preview.
// Same shape as applicationInstallRequest except `name` is optional.
//
// Accepts the same SHORT FORM as applicationInstallRequest (canonical
// UAT matrix vocabulary):
//
//	{ "blueprint":"bp-x", "version":"1.0", "namespace":"qa-omantel",
//	  "values":{...} }
//
// Collapsed via applicationPreviewRequestNormalize before validation.
type applicationPreviewRequest struct {
	BlueprintRef    applicationBlueprintRef `json:"blueprintRef"`
	Name            string                  `json:"name,omitempty"`
	OrganizationRef string                  `json:"organizationRef"`
	EnvironmentRef  string                  `json:"environmentRef"`
	Parameters      map[string]interface{}  `json:"parameters,omitempty"`
	Placement       applicationPlacement    `json:"placement"`

	// Short-form aliases — see applicationInstallRequest doc.
	BlueprintShort string                 `json:"blueprint,omitempty"`
	VersionShort   string                 `json:"version,omitempty"`
	NamespaceShort string                 `json:"namespace,omitempty"`
	ValuesShort    map[string]interface{} `json:"values,omitempty"`
}

// applicationPreviewRequestNormalize collapses the short-form aliases
// onto the canonical fields. Mirrors
// applicationInstallRequestNormalize so a body that previews
// successfully will install successfully (one source of truth for
// the shape coercion).
func applicationPreviewRequestNormalize(b applicationPreviewRequest) applicationPreviewRequest {
	if b.BlueprintRef.Name == "" && strings.TrimSpace(b.BlueprintShort) != "" {
		b.BlueprintRef.Name = strings.TrimSpace(b.BlueprintShort)
	}
	if b.BlueprintRef.Version == "" && strings.TrimSpace(b.VersionShort) != "" {
		b.BlueprintRef.Version = strings.TrimSpace(b.VersionShort)
	}
	if b.OrganizationRef == "" && strings.TrimSpace(b.NamespaceShort) != "" {
		b.OrganizationRef = strings.TrimSpace(b.NamespaceShort)
	}
	if b.EnvironmentRef == "" && b.OrganizationRef != "" {
		b.EnvironmentRef = b.OrganizationRef + "-prod"
	}
	if len(b.Parameters) == 0 && len(b.ValuesShort) > 0 {
		b.Parameters = b.ValuesShort
	}
	if strings.TrimSpace(b.Placement.Mode) == "" {
		// One vocabulary (#3375 DoD-1): canonical default.
		b.Placement.Mode = "singleton"
	}
	if len(b.Placement.Regions) == 0 {
		b.Placement.Regions = []string{"primary"}
	}
	return b
}

// PreviewManifest is one rendered file in the preview output. Path is
// the in-Gitea path the application-controller will write at install
// time; content is the YAML byte stream rendered for that path.
type PreviewManifest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// applicationPreviewBlueprintRef mirrors the Blueprint in the response
// so the UI's preview modal can show "previewing wordpress@5.6.1"
// without cross-referencing the request.
type applicationPreviewBlueprintRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// applicationPreviewResponse is the body of POST .../preview.
type applicationPreviewResponse struct {
	Manifests []PreviewManifest              `json:"manifests"`
	Diff      string                         `json:"diff"`
	Blueprint applicationPreviewBlueprintRef `json:"blueprint"`
	Warnings  []string                       `json:"warnings"`
	// ToVersion is echoed back on the upgrade-preview endpoint so the UI
	// modal can show "previewing upgrade to <version>" without
	// cross-referencing the request. Empty on install / topology
	// previews. Added qa-loop iter-7 Cluster-C (#1227).
	ToVersion string `json:"toVersion,omitempty"`
	// Placement is echoed back on the topology-preview endpoint so the
	// UI can show "previewing <mode> across <regions>" without the
	// caller round-tripping. Empty on install previews. Added qa-loop
	// iter-7 Cluster-C (#1227).
	Placement *applicationPlacement `json:"placement,omitempty"`
	// Application carries the rendered Application CR (apiVersion / kind:
	// Application / metadata / spec) the install POST would persist. The
	// qa-loop matrix (TC-064) asserts the preview response surfaces the
	// CR shape directly so it can be diffed against the live cluster
	// state without re-rendering on the client. Added qa-loop iter-1
	// prefetch Fix #92.
	Application *applicationPreviewCR `json:"application,omitempty"`
}

// applicationPreviewCR — minimal Application CR shape echoed in the
// preview response. Mirrors the (apiVersion, kind, metadata, spec)
// shape the controller reconciles so a "looks-good in preview" matches
// the actual install at the CR level.
type applicationPreviewCR struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
}

// renderApplicationCRPreview composes the Application CR shape that the
// install endpoint would persist, given the same body. Keeps the CR
// shape in lock-step with newApplicationUnstructured (applications.go)
// so preview-vs-install never diverges.
func renderApplicationCRPreview(body applicationPreviewRequest) *applicationPreviewCR {
	appName := strings.TrimSpace(body.Name)
	if appName == "" {
		appName = body.BlueprintRef.Name + "-preview"
	}
	gvr := ApplicationGVR()
	regions := append([]string(nil), body.Placement.Regions...)
	if regions == nil {
		regions = []string{}
	}
	params := body.Parameters
	if params == nil {
		params = map[string]interface{}{}
	}
	return &applicationPreviewCR{
		APIVersion: gvr.Group + "/" + gvr.Version,
		Kind:       "Application",
		Metadata: map[string]interface{}{
			"name":      appName,
			"namespace": body.OrganizationRef,
		},
		Spec: map[string]interface{}{
			"blueprintRef": map[string]interface{}{
				"name":    body.BlueprintRef.Name,
				"version": body.BlueprintRef.Version,
			},
			"organizationRef": body.OrganizationRef,
			"environmentRef":  body.EnvironmentRef,
			"placement": map[string]interface{}{
				"mode":    body.Placement.Mode,
				"regions": regions,
			},
			"parameters": params,
		},
	}
}

// HandleApplicationPreview — POST /api/v1/sovereigns/{id}/applications/preview
//
// See file-level doc for the full contract.
func (h *Handler) HandleApplicationPreview(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if h.catalogClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "catalog-not-wired",
			"detail": "catalog client unconfigured (CATALYST_CATALOG_URL); preview requires the catalog upstream",
		})
		return
	}

	// Dual-shape decode (qa-loop iter-7 Cluster-C, #1227): preview
	// endpoint accepts simplified UI shape too — see
	// applications_wire_compat.go.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationPreviewBody(rawBody)
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
		return
	}
	body = applicationPreviewRequestNormalize(body)
	if msg, ok := validateApplicationPreviewRequest(body); !ok {
		writeBadRequest(w, "invalid-application-preview", msg)
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /applications/preview requires tier-admin or higher",
			})
			return
		}
	}

	bp, err := h.catalogClient.GetVersion(
		r.Context(),
		body.BlueprintRef.Name,
		body.BlueprintRef.Version,
		applicationSessionToken(r),
	)
	if err != nil {
		if errors.Is(err, ErrBlueprintNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "blueprint-not-found",
				"detail": fmt.Sprintf("blueprint %s@%s is not in the catalog", body.BlueprintRef.Name, body.BlueprintRef.Version),
			})
			return
		}
		h.log.Warn("application preview: catalog fetch failed",
			"depId", depID, "blueprint", body.BlueprintRef.Name, "version", body.BlueprintRef.Version, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "catalog-upstream",
			"detail": err.Error(),
		})
		return
	}

	// Validate parameters (same gate as install). Keep the renderer
	// from emitting nonsense for invalid inputs.
	configSchema := blueprintConfigSchema(bp)
	rep, vErr := validate.Parameters(configSchema, body.Parameters)
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

	// Render manifests for each region. Standby flag follows the
	// active-hotstandby pattern: regions[0] is primary, the rest are
	// standby (replicas: 0 overlay).
	appName := strings.TrimSpace(body.Name)
	if appName == "" {
		appName = body.BlueprintRef.Name + "-preview"
	}
	envType := environmentTypeFromName(body.EnvironmentRef)
	manifests := make([]PreviewManifest, 0, len(body.Placement.Regions)*2)
	warnings := []string{}

	for i, region := range body.Placement.Regions {
		standby := body.Placement.Mode == "active-hotstandby" && i > 0
		role := previewRoleForPlacement(body.Placement.Mode, i)
		in := render.Inputs{
			AppName:          appName,
			Org:              body.OrganizationRef,
			EnvType:          envType,
			Region:           region,
			PlacementRole:    role,
			Standby:          standby,
			BlueprintName:    body.BlueprintRef.Name,
			BlueprintVersion: body.BlueprintRef.Version,
			SourceKind:       blueprintSourceKind(bp),
			SourceRef:        blueprintSourceRef(bp),
			Chart:            blueprintChart(bp),
			Values:           body.Parameters,
		}
		out, rErr := render.Render(in)
		if rErr != nil {
			h.log.Warn("application preview: render failed",
				"depId", depID, "blueprint", body.BlueprintRef.Name, "region", region, "err", rErr)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "render-failed",
				"detail": rErr.Error(),
			})
			return
		}
		basePath := fmt.Sprintf("clusters/%s/applications/%s", region, appName)
		manifests = append(manifests, PreviewManifest{
			Path:    basePath + "/kustomization.yaml",
			Content: string(out.KustomizationYAML),
		})
		manifests = append(manifests, PreviewManifest{
			Path:    basePath + "/helmrelease.yaml",
			Content: string(out.HelmReleaseYAML),
		})
	}

	// Diff against the per-Org Gitea repo's current state is deferred:
	// the unified Gitea client is wired for the catalog (read-side
	// blueprint-yaml fetches) but the preview-vs-install diff against
	// `<org>/<app>` is a follow-up. The empty string is a valid value;
	// the UI shows "no prior state" when diff is empty.
	diff := ""
	if len(manifests) > 0 {
		warnings = append(warnings, "preview shows the manifests catalyst-api will commit; live-vs-preview diff against the per-Org Gitea repo is deferred to a follow-up slice")
	}

	writeJSON(w, http.StatusOK, applicationPreviewResponse{
		Manifests: manifests,
		Diff:      diff,
		Blueprint: applicationPreviewBlueprintRef{
			Name:    bp.Name,
			Version: bp.Version,
		},
		Warnings:    warnings,
		Application: renderApplicationCRPreview(body),
	})
	_ = dep // kept for future per-Sovereign Gitea diff
}

// renderApplicationPreview is the shared core that runs the renderer
// and composes the wire response. Used by both HandleApplicationPreview
// (slice I) and HandleApplicationTopologyPreview /
// HandleApplicationUpgradePreview (slice T+O+P).
//
// Returns either (resp, 0, nil) on success or (zero, status, errBody)
// on a fatal upstream / render error so the caller can write the
// appropriate JSON envelope.
func (h *Handler) renderApplicationPreview(
	r *http.Request,
	body applicationPreviewRequest,
) (applicationPreviewResponse, int, map[string]string) {
	bp, err := h.catalogClient.GetVersion(
		r.Context(),
		body.BlueprintRef.Name,
		body.BlueprintRef.Version,
		applicationSessionToken(r),
	)
	if err != nil {
		if errors.Is(err, ErrBlueprintNotFound) {
			return applicationPreviewResponse{}, http.StatusNotFound, map[string]string{
				"error":  "blueprint-not-found",
				"detail": fmt.Sprintf("blueprint %s@%s is not in the catalog", body.BlueprintRef.Name, body.BlueprintRef.Version),
			}
		}
		return applicationPreviewResponse{}, http.StatusBadGateway, map[string]string{
			"error":  "catalog-upstream",
			"detail": err.Error(),
		}
	}

	configSchema := blueprintConfigSchema(bp)
	rep, vErr := validate.Parameters(configSchema, body.Parameters)
	if vErr != nil {
		return applicationPreviewResponse{}, http.StatusBadGateway, map[string]string{
			"error":  "blueprint-schema-malformed",
			"detail": vErr.Error(),
		}
	}
	if !rep.Valid {
		return applicationPreviewResponse{}, http.StatusBadRequest, map[string]string{
			"error":  "invalid-parameters",
			"detail": "parameters do not satisfy Blueprint.spec.configSchema",
		}
	}

	appName := strings.TrimSpace(body.Name)
	if appName == "" {
		appName = body.BlueprintRef.Name + "-preview"
	}
	envType := environmentTypeFromName(body.EnvironmentRef)
	manifests := make([]PreviewManifest, 0, len(body.Placement.Regions)*2)
	warnings := []string{}

	for i, region := range body.Placement.Regions {
		standby := body.Placement.Mode == "active-hotstandby" && i > 0
		role := previewRoleForPlacement(body.Placement.Mode, i)
		in := render.Inputs{
			AppName:          appName,
			Org:              body.OrganizationRef,
			EnvType:          envType,
			Region:           region,
			PlacementRole:    role,
			Standby:          standby,
			BlueprintName:    body.BlueprintRef.Name,
			BlueprintVersion: body.BlueprintRef.Version,
			SourceKind:       blueprintSourceKind(bp),
			SourceRef:        blueprintSourceRef(bp),
			Chart:            blueprintChart(bp),
			Values:           body.Parameters,
		}
		out, rErr := render.Render(in)
		if rErr != nil {
			return applicationPreviewResponse{}, http.StatusBadGateway, map[string]string{
				"error":  "render-failed",
				"detail": rErr.Error(),
			}
		}
		basePath := fmt.Sprintf("clusters/%s/applications/%s", region, appName)
		manifests = append(manifests, PreviewManifest{
			Path:    basePath + "/kustomization.yaml",
			Content: string(out.KustomizationYAML),
		})
		manifests = append(manifests, PreviewManifest{
			Path:    basePath + "/helmrelease.yaml",
			Content: string(out.HelmReleaseYAML),
		})
	}

	if len(manifests) > 0 {
		warnings = append(warnings, "preview shows the manifests catalyst-api will commit; live-vs-preview diff against the per-Org Gitea repo is deferred to a follow-up slice")
	}

	return applicationPreviewResponse{
		Manifests: manifests,
		Diff:      "",
		Blueprint: applicationPreviewBlueprintRef{
			Name:    bp.Name,
			Version: bp.Version,
		},
		Warnings:    warnings,
		Application: renderApplicationCRPreview(body),
	}, 0, nil
}

// validateApplicationPreviewRequest mirrors the install validator with
// `name` optional. Keeps the two paths in lockstep so a 400 on preview
// equates to a 400 on install.
func validateApplicationPreviewRequest(req applicationPreviewRequest) (string, bool) {
	if strings.TrimSpace(req.OrganizationRef) == "" {
		return "organizationRef is required", false
	}
	if !isValidK8sName(req.OrganizationRef) {
		return "organizationRef must be a valid K8s name", false
	}
	if strings.TrimSpace(req.EnvironmentRef) == "" {
		return "environmentRef is required", false
	}
	if strings.TrimSpace(req.BlueprintRef.Name) == "" {
		return "blueprintRef.name is required", false
	}
	if !strings.HasPrefix(req.BlueprintRef.Name, "bp-") {
		return "blueprintRef.name must be of the form bp-<slug>", false
	}
	if strings.TrimSpace(req.BlueprintRef.Version) == "" {
		return "blueprintRef.version is required", false
	}
	if strings.TrimSpace(req.Placement.Mode) == "" {
		return "placement.mode is required", false
	}
	// One vocabulary (#3375 DoD-1): canonicalise then accept the four
	// canonical classes (legacy single-region / active-hotstandby still
	// folded so in-flight callers don't break).
	switch canonicalizeTopology(req.Placement.Mode) {
	case "singleton", "active-active", "active-hot-standby", "active-passive":
	default:
		return "placement.mode must be one of singleton, active-active, active-hot-standby, active-passive (legacy single-region / active-hotstandby also accepted)", false
	}
	if len(req.Placement.Regions) == 0 {
		return "placement.regions must list at least one region", false
	}
	if req.Name != "" && !isValidK8sName(req.Name) {
		return "name must be a valid K8s name when provided", false
	}
	return "", true
}

// blueprintSourceKind reads `spec.manifests.source.kind` (or empty).
func blueprintSourceKind(bp *CatalogBlueprint) string {
	return blueprintNestedString(bp, "spec", "manifests", "source", "kind")
}

// blueprintSourceRef reads `spec.manifests.source.ref` (or empty).
func blueprintSourceRef(bp *CatalogBlueprint) string {
	return blueprintNestedString(bp, "spec", "manifests", "source", "ref")
}

// blueprintChart reads `spec.manifests.chart` (or empty — render
// defaults to BlueprintName when this is empty).
func blueprintChart(bp *CatalogBlueprint) string {
	return blueprintNestedString(bp, "spec", "manifests", "chart")
}

// blueprintNestedString — convenience wrapper to drill into the
// Blueprint's Raw map without panicking on missing keys.
func blueprintNestedString(bp *CatalogBlueprint, keys ...string) string {
	if bp == nil || bp.Raw == nil {
		return ""
	}
	var cur any = map[string]interface{}(bp.Raw)
	for _, k := range keys {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = m[k]
		if cur == nil {
			return ""
		}
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

// environmentTypeFromName parses the env type (`-prod`, `-stg`, `-dev`,
// `-uat`, `-poc`) suffix from the Environment name. Falls back to the
// full name when no suffix matches; the renderer treats that as a
// label-only field so a non-canonical env name is not fatal.
func environmentTypeFromName(env string) string {
	for _, suf := range []string{"-prod", "-stg", "-dev", "-uat", "-poc"} {
		if strings.HasSuffix(env, suf) {
			return strings.TrimPrefix(suf, "-")
		}
	}
	return env
}

// previewRoleForPlacement maps (mode, index) → renderer's PlacementRole
// label. Mirrors the application-controller's reconcile contract so
// preview labels match production labels.
func previewRoleForPlacement(mode string, idx int) string {
	switch mode {
	case "active-active":
		return "active"
	case "active-hotstandby":
		if idx == 0 {
			return "primary"
		}
		return "standby"
	}
	return "primary"
}
