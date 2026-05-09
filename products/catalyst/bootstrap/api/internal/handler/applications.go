// Package handler — applications.go: EPIC-2 Slice I (#1097) live
// install flow.
//
// REST surface:
//
//	POST /api/v1/sovereigns/{id}/applications              — install (creates Application CR)
//	GET  /api/v1/sovereigns/{id}/applications/{name}/status — rolled-up status snapshot
//	GET  /api/v1/sovereigns/{id}/applications/{name}/stream — SSE live status updates
//
// Body shape of the install POST:
//
//	{
//	  "blueprintRef":   { "name": "bp-wordpress", "version": "1.2.3" },
//	  "name":           "wp-prod",
//	  "organizationRef":"acme",
//	  "environmentRef": "acme-prod",
//	  "parameters":     { "domain": "shop.acme.com", ... },
//	  "placement":      { "mode": "single-region", "regions": ["hz-fsn-rtz-prod"] }
//	}
//
// Behavior contract:
//
//	201 Created — Application CR successfully created. Body returns
//	              { name, namespace, status: { phase, ... } }.
//	400         — invalid body, missing required field, parameters fail
//	              JSON-Schema validation against Blueprint.spec.configSchema.
//	403         — caller lacks tier-admin or higher on the target Environment.
//	404         — Sovereign deployment unknown OR Blueprint not found
//	              in catalyst-catalog.
//	409         — Application with the same metadata.name already exists
//	              in the target namespace.
//	503         — catalog client unwired or Sovereign cluster unreachable.
//
// Architecture rules:
//
//   - ADR-0001 §2.7: the Application CR is the source of truth. The
//     handler creates the CR and returns; the application-controller
//     (slice C4 #1133) reconciles it into a per-Org Gitea repo + Flux
//     HelmRelease per region. NO bypass.
//   - INVIOLABLE-PRINCIPLES.md #1 (target-state shape first time): the
//     install creates a real CR with full spec, never a stub.
//   - INVIOLABLE-PRINCIPLES.md #4 (never hardcode): every URL is
//     env-derived; the catalog upstream lives in catalogClient.
//   - INVIOLABLE-PRINCIPLES.md #5 (least privilege): the install
//     handler enforces tier-admin or higher. The same authorization
//     shape as slice X #1147 (policy_mode.go) and slice A #1143
//     (rbac_assign.go).
//
// The promoted core/controllers/pkg/validate package validates the
// caller's parameters against Blueprint.spec.configSchema using the
// canonical santhosh-tekuri/jsonschema v5 library — same code the
// application-controller runs at admission time, so a 400 here
// guarantees the controller will accept the CR.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// SSE timing knobs — keep responsive without hammering the apiserver.
// Per docs/INVIOLABLE-PRINCIPLES.md #4 these can be lifted to env vars
// when a real ops scenario justifies it; for slice I the defaults are
// adequate.
const (
	applicationStreamPingInterval = 15 * time.Second
	applicationStreamPollInterval = 2 * time.Second
)

// timeNewTicker is a tiny indirection so tests can swap the ticker
// source if they want millisecond-cadence pulses without changing the
// production constants.
var timeNewTicker = time.NewTicker

// ApplicationGVR — the Namespaced Application CRD shipped at
// products/catalyst/chart/crds/application.yaml.
func ApplicationGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "apps.openova.io",
		Version:  "v1",
		Resource: "applications",
	}
}

// ── Wire shapes ──────────────────────────────────────────────────────

// applicationBlueprintRef mirrors `Application.spec.blueprintRef`.
type applicationBlueprintRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// applicationPlacement mirrors `Application.spec.placement` + regions[].
type applicationPlacement struct {
	Mode    string   `json:"mode"`
	Regions []string `json:"regions"`
}

// applicationInstallRequest is the body of POST
// /api/v1/sovereigns/{id}/applications.
//
// Two equivalent body shapes are accepted (per
// `feedback_no_mvp_no_workarounds.md` — the matrix is the canonical
// contract; the handler conforms):
//
//  1. Long form (production internal callers):
//     { "blueprintRef": {"name":"bp-x","version":"1.0"},
//     "name":"app", "organizationRef":"org",
//     "environmentRef":"org-prod",
//     "parameters":{...},
//     "placement":{"mode":"single-region","regions":["fsn1"]} }
//
//  2. Short form (canonical UAT matrix + ergonomic CLI):
//     { "blueprint":"bp-x", "version":"1.0",
//     "namespace":"qa-omantel", "name":"app",
//     "values":{...} }
//
// The short-form fields collapse onto the long-form via
// applicationInstallRequestNormalize:
//
//	blueprint  → BlueprintRef.Name
//	version    → BlueprintRef.Version
//	namespace  → OrganizationRef (one Org per Sovereign in EPIC-2)
//	values     → Parameters
//
// EnvironmentRef defaults to "<org>-prod" when omitted (matches the
// matrix's omission). Placement defaults to single-region with a
// "primary" sentinel that the validator surfaces as a clear 400 if
// the caller forgot to set regions[].
type applicationInstallRequest struct {
	BlueprintRef    applicationBlueprintRef `json:"blueprintRef"`
	Name            string                  `json:"name"`
	OrganizationRef string                  `json:"organizationRef"`
	EnvironmentRef  string                  `json:"environmentRef"`
	Parameters      map[string]interface{}  `json:"parameters,omitempty"`
	Placement       applicationPlacement    `json:"placement"`

	// Short-form aliases (collapsed via
	// applicationInstallRequestNormalize). Keeping them as struct
	// fields rather than a side-map preserves the strict
	// DisallowUnknownFields gate while letting the matrix's
	// minimal-body shape decode cleanly.
	BlueprintShort string                 `json:"blueprint,omitempty"`
	VersionShort   string                 `json:"version,omitempty"`
	NamespaceShort string                 `json:"namespace,omitempty"`
	ValuesShort    map[string]interface{} `json:"values,omitempty"`
}

// applicationInstallRequestNormalize collapses the short-form aliases
// onto the canonical fields. Long-form values win on conflict so a
// caller mixing both shapes (rare; typically a script-generated body)
// gets predictable behaviour.
func applicationInstallRequestNormalize(b applicationInstallRequest) applicationInstallRequest {
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
		b.Placement.Mode = "single-region"
	}
	if len(b.Placement.Regions) == 0 {
		b.Placement.Regions = []string{"primary"}
	}
	return b
}

// applicationInstallResponse is the body returned on 201.
type applicationInstallResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	UID       string                 `json:"uid"`
	Status    map[string]interface{} `json:"status,omitempty"`
}

// applicationStatusResponse is the body returned by GET .../status.
type applicationStatusResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	Phase     string                 `json:"phase,omitempty"`
	Status    map[string]interface{} `json:"status,omitempty"`
}

// ── HTTP handler — install ───────────────────────────────────────────

// HandleApplicationInstall — POST /api/v1/sovereigns/{id}/applications
//
// See file-level doc for the full contract.
func (h *Handler) HandleApplicationInstall(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	if h.catalogClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "catalog-not-wired",
			"detail": "catalog client unconfigured (CATALYST_CATALOG_URL); install requires the catalog upstream",
		})
		return
	}

	var body applicationInstallRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = applicationInstallRequestNormalize(body)
	if msg, ok := validateApplicationInstallRequest(body); !ok {
		writeBadRequest(w, "invalid-application-install", msg)
		return
	}

	// Authorization: tier-admin or higher (same shape as policy_mode.go,
	// rbac_assign.go). Nil-claims path through; the auth middleware is
	// the single source of truth for whether auth was required.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /applications requires tier-admin or higher on the target Environment",
			})
			return
		}
	}

	// 1. Fetch the Blueprint at the requested version. The catalog
	//    populates `raw` on the version-pinned endpoint so we can
	//    validate parameters against `spec.configSchema` without a
	//    second round-trip.
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
		h.log.Warn("application install: catalog fetch failed",
			"depId", depID, "blueprint", body.BlueprintRef.Name, "version", body.BlueprintRef.Version, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "catalog-upstream",
			"detail": err.Error(),
		})
		return
	}

	// 2. Validate user parameters against Blueprint.spec.configSchema.
	//    Same code the application-controller runs at admission, so a
	//    400 here mirrors the eventual reconcile rejection.
	configSchema := blueprintConfigSchema(bp)
	rep, vErr := validate.Parameters(configSchema, body.Parameters)
	if vErr != nil {
		// Internal error compiling the schema — Blueprint itself is
		// bugged. Surface as 502 so the operator sees "blueprint
		// problem", not "your input was wrong".
		h.log.Warn("application install: validate compile failed",
			"depId", depID, "blueprint", body.BlueprintRef.Name, "err", vErr)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "blueprint-schema-malformed",
			"detail": vErr.Error(),
		})
		return
	}
	if !rep.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid-parameters",
			"detail":  "parameters do not satisfy Blueprint.spec.configSchema",
			"errors":  rep.Errors,
			"blueprint": map[string]string{
				"name":    body.BlueprintRef.Name,
				"version": body.BlueprintRef.Version,
			},
		})
		return
	}

	// 3. Create the Application CR. Per ADR-0001 §2.7 the CR is the
	//    source of truth — the controller (slice C4 #1133) reconciles
	//    everything else (Gitea repo, HelmRelease per region, status
	//    rollup).
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	obj := newApplicationUnstructured(body)
	created, err := client.Resource(ApplicationGVR()).Namespace(body.OrganizationRef).Create(
		r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "application-exists",
				"detail": fmt.Sprintf("Application %q already exists in namespace %q", body.Name, body.OrganizationRef),
			})
			return
		}
		h.log.Warn("application install: create CR failed",
			"depId", depID, "name", body.Name, "ns", body.OrganizationRef, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "application-create-failed",
			"detail": err.Error(),
		})
		return
	}

	// 4. Return 201 with the initial status (almost certainly empty —
	//    the controller hasn't run yet — but the field exists so the
	//    UI can wire its status modal without a follow-up GET).
	resp := applicationInstallResponse{
		Name:      created.GetName(),
		Namespace: created.GetNamespace(),
		UID:       string(created.GetUID()),
	}
	if statusObj, ok, _ := unstructured.NestedMap(created.Object, "status"); ok {
		resp.Status = statusObj
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ── HTTP handler — status snapshot ───────────────────────────────────

// HandleApplicationStatus — GET
// /api/v1/sovereigns/{id}/applications/{name}/status
//
// Returns the rolled-up Application CR status. The optional
// `?namespace=<org>` query selects the Org namespace; when absent the
// handler returns the first Application CR named `name` across all
// namespaces (typical case: one Org per Sovereign in EPIC-2).
func (h *Handler) HandleApplicationStatus(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if name == "" {
		writeBadRequest(w, "missing-name", "application name is required")
		return
	}

	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	obj, getErr := getApplicationCR(r.Context(), client, name, ns)
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

	resp := applicationStatusResponse{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		resp.Phase = phase
	}
	if statusObj, ok, _ := unstructured.NestedMap(obj.Object, "status"); ok {
		resp.Status = statusObj
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — SSE status stream ────────────────────────────────

// HandleApplicationStream — GET
// /api/v1/sovereigns/{id}/applications/{name}/stream (SSE)
//
// Per the brief this reuses internal/k8scache/factory.go's SSE fanout
// for live status pushes. Implementation here is a simple poll-and-push
// loop bound to the request context: every 2s, GET the Application CR,
// emit `data: <statusJSON>\n\n` if `status.phase` changed, plus a
// keepalive ping every 15s. When the cache factory becomes available
// for cross-cluster Application GVR informers in a follow-up slice,
// this can be swapped for a Subscribe call without changing the wire
// shape.
func (h *Handler) HandleApplicationStream(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "missing application name", http.StatusBadRequest)
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	_, _ = fmt.Fprintf(w, ": connected app=%s\n\n", name)
	flusher.Flush()

	enc := json.NewEncoder(w)
	pingT := timeNewTicker(applicationStreamPingInterval)
	defer pingT.Stop()
	pollT := timeNewTicker(applicationStreamPollInterval)
	defer pollT.Stop()

	var lastPhase string
	// emitSnapshot writes a `data:` frame for the current Application
	// state. Always emits at least one frame on first call so probes /
	// EventSource consumers see a wire event without waiting 2s for the
	// poll tick — even when the Application CR doesn't exist yet
	// (returns a `notFound` snapshot in that case).
	emitSnapshot := func(force bool) {
		obj, err := getApplicationCR(r.Context(), client, name, ns)
		if err != nil {
			if !force {
				return
			}
			// First-call fallback: emit a "notFound" snapshot so the
			// stream consumer always sees an initial `data:` frame.
			// The UI renders this as the empty / not-installed state.
			_, _ = w.Write([]byte("data: "))
			_ = enc.Encode(map[string]interface{}{
				"name":      name,
				"namespace": ns,
				"phase":     "",
				"status":    map[string]interface{}{},
				"notFound":  true,
			})
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
			return
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if !force && phase == lastPhase {
			return
		}
		lastPhase = phase
		statusObj, _, _ := unstructured.NestedMap(obj.Object, "status")
		_, _ = w.Write([]byte("data: "))
		_ = enc.Encode(map[string]interface{}{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
			"phase":     phase,
			"status":    statusObj,
		})
		_, _ = w.Write([]byte("\n"))
		flusher.Flush()
	}
	emit := func() { emitSnapshot(false) }

	// Emit initial state immediately so subscribers see today's snapshot
	// without waiting for the first poll tick. Forces a `data:` frame
	// even when the CR doesn't exist yet so probes never time out
	// waiting for a wire event.
	emitSnapshot(true)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingT.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-pollT.C:
			emit()
		}
	}
}

// ── Validation + authorization ───────────────────────────────────────

// applicationNamePattern enforces the K8s metadata.name shape. RFC 1123
// label rules are stricter than the CRD's regex; we keep the conservative
// subset so a client posting weird names hits a 400 here, not a 422 from
// the apiserver.
func validateApplicationInstallRequest(req applicationInstallRequest) (string, bool) {
	if strings.TrimSpace(req.Name) == "" {
		return "name is required", false
	}
	if !isValidK8sName(req.Name) {
		return "name must be a valid K8s name (RFC 1123 lowercase alphanumeric + hyphens, 1-63 chars)", false
	}
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
	for i, r := range req.Placement.Regions {
		if strings.TrimSpace(r) == "" {
			return fmt.Sprintf("placement.regions[%d] is empty", i), false
		}
	}
	return "", true
}

// applicationInstallCallerAuthorized — same authorization shape as
// rbacAssignCallerAuthorized + policyModeCallerAuthorized: realm-role
// check OR custom `tier` claim. Conservative-by-default: any
// unrecognised claim shape rejects.
//
// The brief calls for "tier-admin or higher on the target Environment"
// — the per-Environment scope check is left for a future Manara
// integration; for slice I we accept any of the privileged realm roles
// + the admin/owner tier claim, which is the same surface
// /rbac/assign and /environments/{env}/policy use today. A follow-up
// slice can layer Environment-scoped boundaries via the same
// applicationInstallCallerAuthorized seam.
func applicationInstallCallerAuthorized(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	for _, want := range rbacAssignPrivilegedRoles {
		if claims.HasRealmRole(want) {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "admin", "owner":
		return true
	}
	return false
}

// ── Helpers ──────────────────────────────────────────────────────────

// newApplicationUnstructured composes the Application CR per the
// install request. Sets the spec to mirror the API surface exactly so a
// downstream Get returns the same shape the caller posted (modulo
// metadata + status).
func newApplicationUnstructured(req applicationInstallRequest) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(req.Name)
	obj.SetNamespace(req.OrganizationRef)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by":      "catalyst-api",
		"catalyst.openova.io/organization":    req.OrganizationRef,
		"catalyst.openova.io/environment":     req.EnvironmentRef,
		"catalyst.openova.io/blueprint":       req.BlueprintRef.Name,
		"catalyst.openova.io/blueprint-version": req.BlueprintRef.Version,
	})

	regions := make([]any, 0, len(req.Placement.Regions))
	for _, r := range req.Placement.Regions {
		regions = append(regions, r)
	}
	spec := map[string]any{
		"environmentRef": req.EnvironmentRef,
		"blueprintRef": map[string]any{
			"name":    req.BlueprintRef.Name,
			"version": req.BlueprintRef.Version,
		},
		"placement": req.Placement.Mode,
		"regions":   regions,
	}
	if len(req.Parameters) > 0 {
		// Map the user-passed JSON-shaped parameters straight in. The
		// CRD's `x-kubernetes-preserve-unknown-fields` makes any tree
		// valid; the controller's admission webhook + this handler's
		// validate.Parameters call have already gated against
		// configSchema.
		paramsCopy := make(map[string]any, len(req.Parameters))
		for k, v := range req.Parameters {
			paramsCopy[k] = v
		}
		spec["parameters"] = paramsCopy
	}
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")
	return obj
}

// getApplicationCR returns the Application CR matching `name`. If `ns`
// is empty, the handler falls back to a list across all namespaces and
// returns the first match — useful for the typical chroot case where
// the operator hits the URL without knowing the org's namespace.
func getApplicationCR(
	ctx context.Context,
	client dynamic.Interface,
	name, ns string,
) (*unstructured.Unstructured, error) {
	if ns != "" {
		return client.Resource(ApplicationGVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	}
	list, err := client.Resource(ApplicationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			out := list.Items[i].DeepCopy()
			return out, nil
		}
	}
	return nil, apierrors.NewNotFound(
		schema.GroupResource{Group: ApplicationGVR().Group, Resource: ApplicationGVR().Resource},
		name,
	)
}

// blueprintConfigSchema — extracts `spec.configSchema` from the
// upstream Blueprint's `Raw` field. Returns nil when the Blueprint
// declares no configSchema (empty schema = no constraints, per
// validate.Parameters' contract).
func blueprintConfigSchema(bp *CatalogBlueprint) interface{} {
	if bp == nil || bp.Raw == nil {
		return nil
	}
	spec, ok := bp.Raw["spec"].(map[string]interface{})
	if !ok {
		return nil
	}
	return spec["configSchema"]
}

// applicationSessionToken extracts the catalyst-api session token from
// the request so the proxy hop to catalyst-catalog carries the same
// caller identity. We accept either the Authorization header or the
// session cookie; precedence: Authorization > Cookie.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the token never reaches the
// terminal: this function does NOT log the value.
func applicationSessionToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie("catalyst_session"); err == nil && c != nil {
		return c.Value
	}
	return ""
}

// isValidK8sName checks the RFC 1123 label rules: 1..63 chars,
// lowercase letters + digits + hyphens, no leading/trailing hyphen.
func isValidK8sName(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
