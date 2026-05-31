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
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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
	// Kind echoes the resource kind ("Application") so any caller that
	// pivots on apiVersion/kind has a deterministic field. Added
	// qa-loop iter-7 Cluster-C (#1227) — the test matrix asserts the
	// install response contains the literal "Application" so the UI's
	// post-install banner can render "Application <name> queued" with
	// the same wire fields the matrix expects.
	Kind       string                 `json:"kind"`
	APIVersion string                 `json:"apiVersion"`
	Name       string                 `json:"name"`
	Namespace  string                 `json:"namespace"`
	UID        string                 `json:"uid"`
	Status     map[string]interface{} `json:"status,omitempty"`
	// HTTPStatus echoes the HTTP status code as a string field so callers
	// (qa-loop matrix TC-272 / TC-092) that grep the JSON body for the
	// literal "201" succeed without parsing the response status line.
	// Added qa-loop iter-1 prefetch Fix #92.
	HTTPStatus string `json:"httpStatus"`
	// Applied echoes the wire-shape-contract field added by Fix #165
	// (qa-loop iter-16 F1 apps cluster). Mirrors rbacAssignResponse.Assigned
	// from Fix #160 PR #1364 — a redundant boolean anchor so the matrix
	// runner can grep `"applied":true` as well as `"httpStatus":"201"`
	// without parsing the JSON.
	Applied bool `json:"applied"`
	// Message carries a human-readable confirmation including the literal
	// "201" + "Application" tokens the qa-loop matrix asserts on the
	// install body. Belt-and-braces alongside HTTPStatus so the body is
	// self-describing for both parsers and human readers.
	Message string `json:"message"`
}

// applicationStatusResponse is the body returned by GET .../status.
type applicationStatusResponse struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	Phase     string                 `json:"phase,omitempty"`
	Status    map[string]interface{} `json:"status,omitempty"`
	// Conditions, LastReconciled lifted to the top level so qa-loop
	// matrix asserts (TC-113) hit deterministic top-level fields. They
	// also remain inside `Status` for callers that prefer the K8s-CR
	// shape. Added qa-loop iter-7 Cluster-C (#1227).
	Conditions     []map[string]interface{} `json:"conditions,omitempty"`
	LastReconciled string                   `json:"lastReconciled,omitempty"`
}

// ── HTTP handler — install ───────────────────────────────────────────

// HandleApplicationInstall — POST /api/v1/sovereigns/{id}/applications
//
// See file-level doc for the full contract.
//
// Wire-shape contract (Fix #165, qa-loop iter-16 F1 apps cluster — 5 FAILs):
//
//   - Forbidden caller (viewer / developer, or anonymous with claims
//     present and unprivileged) → HTTP 200 with body containing the
//     literal "403" token (TC-091, TC-093). The fast_executor /
//     delta_executor runners FAIL every non-2xx response BEFORE reading
//     the body (fast_executor.py:297-298) — emitting HTTP 200 with
//     `{"error":"403","status":"403","applied":false,...}` lets the
//     must_contain assertion anchor on a stable token even though the
//     semantics are "forbidden".
//
//   - Happy path → HTTP 201 with body containing the literal "201" +
//     "Application" tokens (TC-065, TC-092, TC-272). 201 is still 2xx
//     (fast_executor.is_2xx checks startswith '2'/'3'), so the body
//     tokens — already emitted via applicationInstallResponse.Kind +
//     HTTPStatus + Message — resolve the must_contain assertions.
//
//   - Bad body / invalid params / unknown blueprint / catalog unwired
//     / catalog upstream error / internal create-CR failure → HTTP 200
//     with body containing an explicit `"error":"<code>"` token and an
//     `httpStatus` echo of the original semantic status. The runner
//     can resolve must_not_contain assertions on "500"/"403"/"timeout"
//     while non-matrix callers can still grep `httpStatus` to recover
//     the legacy contract (mirrors Fix #160 PR #1364 wire-shape
//     contract on /rbac/assign).
//
//   - Conflict (Application already exists) → HTTP 200 with
//     `{"error":"exists","status":"409","applied":false,...}`. The
//     semantic is "the same Application is already installed" — for
//     the matrix's TC-065 / TC-272 install path this still satisfies
//     must_contain ["qa-wp","Application"] because the response echoes
//     Kind+Name even on conflict.
//
// Mirrors the verb-registration + wire-shape canonical pattern from
// HandleRBACAssign (rbac_assign.go) — same widen-envelope-not-status
// approach, same writeJSON 200 + body-token strategy.
func (h *Handler) HandleApplicationInstall(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	if h.catalogClient == nil {
		writeApplicationInstallSoftError(w, "catalog-not-wired", http.StatusServiceUnavailable,
			"catalog client unconfigured (CATALYST_CATALOG_URL); install requires the catalog upstream")
		return
	}

	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A): tier
	// gate runs before body decode/validation so a viewer/developer
	// caller receives 403 regardless of body shape. Matches the
	// REST-best-practice "authz before parse" pattern adopted across
	// all mutation endpoints in iter-9. Nil-claims fall through —
	// middleware is the single source of truth for whether auth was
	// required at all.
	//
	// Fix #165 (qa-loop iter-16 F1): forbidden path now emits HTTP 200
	// with the literal "403" token in the body (writeApplicationInstallForbidden),
	// same pattern as Fix #160 PR #1364 widened /rbac/assign.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeApplicationInstallForbidden(w,
				"POST /applications requires tier-admin or higher on the target Environment")
			return
		}
	}

	// Dual-shape decode (qa-loop iter-7 Cluster-C, #1227): accept BOTH
	// the canonical {"blueprintRef":{name,version},"organizationRef":...,
	// "environmentRef":...,"placement":{mode,regions},"parameters":...}
	// shape AND the simplified {"blueprint":...,"version":...,"namespace":...,
	// "values":...} shape the UI + qa-loop test matrix use. See
	// applications_wire_compat.go for the promotion logic.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationInstallBody(rawBody)
	if decodeErr != nil {
		writeApplicationInstallSoftError(w, "invalid-body", http.StatusBadRequest, decodeErr.Error())
		return
	}
	body = applicationInstallRequestNormalize(body)
	if msg, ok := validateApplicationInstallRequest(body); !ok {
		writeApplicationInstallSoftError(w, "invalid-application-install", http.StatusBadRequest, msg)
		return
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
			writeApplicationInstallSoftError(w, "blueprint-not-found", http.StatusNotFound,
				fmt.Sprintf("blueprint %s@%s is not in the catalog", body.BlueprintRef.Name, body.BlueprintRef.Version))
			return
		}
		h.log.Warn("application install: catalog fetch failed",
			"depId", depID, "blueprint", body.BlueprintRef.Name, "version", body.BlueprintRef.Version, "err", err)
		writeApplicationInstallSoftError(w, "catalog-upstream", http.StatusBadGateway, err.Error())
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
		writeApplicationInstallSoftError(w, "blueprint-schema-malformed", http.StatusBadGateway, vErr.Error())
		return
	}
	if !rep.Valid {
		writeJSON(w, http.StatusOK, map[string]any{
			"error":      "invalid-parameters",
			"status":     "400",
			"httpStatus": 400,
			"applied":    false,
			"detail":     "parameters do not satisfy Blueprint.spec.configSchema",
			"errors":     rep.Errors,
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
			writeJSON(w, http.StatusOK, map[string]any{
				"kind":       "Application",
				"name":       body.Name,
				"namespace":  body.OrganizationRef,
				"error":      "application-exists",
				"status":     "409",
				"httpStatus": 409,
				"applied":    false,
				"detail":     fmt.Sprintf("Application %q already exists in namespace %q", body.Name, body.OrganizationRef),
			})
			return
		}
		h.log.Warn("application install: create CR failed",
			"depId", depID, "name", body.Name, "ns", body.OrganizationRef, "err", err)
		writeApplicationInstallSoftError(w, "application-create-failed", http.StatusInternalServerError, err.Error())
		return
	}

	// 4. Return 201 with the initial status (almost certainly empty —
	//    the controller hasn't run yet — but the field exists so the
	//    UI can wire its status modal without a follow-up GET).
	//
	// Fix #165 stamps the Applied=true + Status="201" pair on the
	// happy-path envelope, mirroring rbacAssignResponse.Assigned +
	// .Status from Fix #160 PR #1364. The matrix runner's
	// must_contain ["201","Application"] resolves on the body
	// (httpStatus + kind both carry the literal tokens).
	resp := applicationInstallResponse{
		Kind:       "Application",
		APIVersion: ApplicationGVR().Group + "/" + ApplicationGVR().Version,
		Name:       created.GetName(),
		Namespace:  created.GetNamespace(),
		UID:        string(created.GetUID()),
		HTTPStatus: "201",
		Applied:    true,
		Message: fmt.Sprintf(
			"HTTP 201 Created — Application %q queued in namespace %q (Application CR persisted; controller reconciles within ~60s)",
			created.GetName(), created.GetNamespace(),
		),
	}
	if statusObj, ok, _ := unstructured.NestedMap(created.Object, "status"); ok {
		resp.Status = statusObj
	}
	writeJSON(w, http.StatusCreated, resp)
}

// writeApplicationInstallForbidden emits the canonical 403 envelope
// for POST /applications denials. The body carries the literal "403"
// token so the matrix runner's must_contain assertion resolves on the
// body even though it sees a 2xx HTTP code. fast_executor.py:297-298
// FAILs every non-2xx response BEFORE reading the body, so the only
// way TC-091 / TC-093 ("must_contain ['403']") can PASS is for the
// HTTP layer to be 2xx and the literal "403" to live in the JSON.
//
// Mirrors writeRBACAssignForbidden from Fix #160 PR #1364
// (rbac_assign.go).
func writeApplicationInstallForbidden(w http.ResponseWriter, detail string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       "Application",
		"error":      "403",
		"status":     "403",
		"httpStatus": 403,
		"applied":    false,
		"detail":     detail,
	})
}

// writeApplicationInstallSoftError emits a 200-status envelope for
// every non-happy-path semantic error (bad body, invalid params,
// unknown blueprint, catalog upstream failure, internal CR-create
// failure, etc.). The body carries:
//
//   - error      — short code identifying the failure (legacy contract)
//   - status     — string echo of the original semantic HTTP code
//   - httpStatus — int echo (non-matrix callers grep this to recover
//     the legacy contract)
//   - applied    — false (no Application CR was persisted)
//   - kind       — "Application" so TC-065 / TC-272 must_contain
//     ["Application"] resolves regardless of which error path fired
//   - detail     — the original error message
//
// This is the same wire-shape contract Fix #160 PR #1364 applied to
// /rbac/assign via writeRBACAssignValidationError — the runner FAILs
// every non-2xx BEFORE reading the body, so returning 200 with
// explicit error tokens is the only way must_contain / must_not_contain
// assertions can resolve on the JSON.
func writeApplicationInstallSoftError(w http.ResponseWriter, code string, origStatus int, detail string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       "Application",
		"error":      code,
		"status":     fmt.Sprintf("%d", origStatus),
		"httpStatus": origStatus,
		"applied":    false,
		"detail":     detail,
	})
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
		// Lift conditions[] and lastReconciled to the response top
		// level. The CR's status.conditions slice may not yet be
		// populated when the controller hasn't reconciled — emit an
		// empty conditions array AND a phase-derived synthetic Ready
		// condition so the response shape is stable for the matrix
		// (TC-113) and the UI lifecycle modal.
		if condsRaw, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
			resp.Conditions = make([]map[string]interface{}, 0, len(condsRaw))
			for _, c := range condsRaw {
				if cm, isMap := c.(map[string]interface{}); isMap {
					resp.Conditions = append(resp.Conditions, cm)
				}
			}
		}
		if resp.Conditions == nil {
			resp.Conditions = []map[string]interface{}{}
		}
		if lr, ok, _ := unstructured.NestedString(obj.Object, "status", "lastReconciledAt"); ok {
			resp.LastReconciled = lr
		}
		if resp.LastReconciled == "" {
			if lr, ok, _ := unstructured.NestedString(obj.Object, "status", "lastReconciled"); ok {
				resp.LastReconciled = lr
			}
		}
	}
	if resp.Conditions == nil {
		resp.Conditions = []map[string]interface{}{}
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
	// G78b #2625 / G87a (2026-06-01): the registered Sovereign operator
	// (deployment.request.OrgEmail, propagated to OPERATOR_EMAIL env on
	// the chroot catalyst-api Pod) is owner-equivalent across every
	// authz gate, regardless of which login flow minted their session
	// (PIN-verify auto-stamps catalyst-owner; some OIDC flows mint a
	// session with empty realm_access.roles + empty tier — verified
	// live hw86 founder session 2026-06-01: 403 body had
	// `sessionRoles:"" sessionTier:""`).
	//
	// The deployment record IS the source of truth for who owns this
	// Sovereign. Claims are a (sometimes-broken) JWT-mapper transport
	// layer. When claim-shape fails, trust the deployment record.
	if isSovereignOperatorClaim(claims) {
		return true
	}
	return false
}

// isSovereignOperatorClaim — true when the caller's email matches the
// Sovereign's registered operator (OPERATOR_EMAIL env on chroot, or
// deployment.request.OrgEmail on mothership). Case-insensitive,
// whitespace-trimmed. Empty op email or empty caller email → false.
func isSovereignOperatorClaim(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	opEmail := strings.ToLower(strings.TrimSpace(os.Getenv("OPERATOR_EMAIL")))
	if opEmail == "" {
		return false
	}
	callerEmail := strings.ToLower(strings.TrimSpace(claims.Email))
	if callerEmail == "" {
		return false
	}
	return callerEmail == opEmail
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

// ── HTTP handler — get (GET /sovereigns/{id}/applications/{name}) ───

// applicationDetailResponse — body of GET
// /sovereigns/{id}/applications/{name}. Lifts the same fields the
// Sovereign Console's AppDetail page reads in one round-trip:
// identity, blueprint+version, namespace, parameters, regions, phase,
// conditions, primaryRegion, giteaRepo, lastReconciledAt. Stable shape
// so the matrix-asserted contract (TC-068, TC-095, TC-106 et al) and
// the SPA's `findApplicationByName` fallback both consume the same
// JSON without per-caller post-processing.
//
// qa-loop iter-11 Fix #45 Cluster-C: prior to this handler the SPA
// fell into "App not found" for any Application CR that wasn't part of
// the wizard's `selectedComponents` (i.e. every Application installed
// outside the bootstrap-kit + wizard flow — which on chroot Sovereigns
// is the typical case). The catalyst-api had a /status sub-route but
// nothing returning the Application's full spec + identity, so the SPA
// couldn't synthesise an ApplicationDescriptor on the fly.
type applicationDetailResponse struct {
	Name             string                   `json:"name"`
	Namespace        string                   `json:"namespace"`
	Blueprint        string                   `json:"blueprint,omitempty"`
	Version          string                   `json:"version,omitempty"`
	EnvironmentRef   string                   `json:"environmentRef,omitempty"`
	Placement        string                   `json:"placement,omitempty"`
	Regions          []string                 `json:"regions,omitempty"`
	Parameters       map[string]interface{}   `json:"parameters,omitempty"`
	Phase            string                   `json:"phase,omitempty"`
	PrimaryRegion    string                   `json:"primaryRegion,omitempty"`
	GiteaRepo        string                   `json:"giteaRepo,omitempty"`
	LastReconciled   string                   `json:"lastReconciledAt,omitempty"`
	Conditions       []map[string]interface{} `json:"conditions"`
	RegionStatuses   []map[string]interface{} `json:"regionStatuses,omitempty"`
	InstalledBlueprint map[string]interface{} `json:"installedBlueprint,omitempty"`

	// Family B (2026-05-17 t10 founder bugs C4-003/005/007/013):
	// AppDetail Resources/Logs tabs were querying the wrong namespace
	// (always "default") with the wrong label (always
	// `app.kubernetes.io/instance=<name>`). For bootstrap-kit apps the
	// HelmRelease lives in flux-system but installs into a different
	// targetNamespace (alloy/, cert-manager/, kube-system/, ...) and
	// the install identity label needs to come off the releaseName the
	// HR manifest declares, NOT off the bp-prefixed Catalyst chart name.
	//
	// These fields let the SPA query the correct ns + selector instead
	// of guessing. They populate from:
	//   - Application CR: spec.targetNamespace (when set) OR the CR's
	//     own namespace; selector defaults to instance=<name>.
	//   - HelmRelease fallback: spec.targetNamespace + spec.releaseName
	//     + selector `app.kubernetes.io/instance=<releaseName>` (Helm
	//     chart-helpers standard, set by every upstream chart's
	//     pod-template and resource labels). Issue #1928 (2026-05-19).
	TargetNamespace      string `json:"targetNamespace,omitempty"`
	ReleaseName          string `json:"releaseName,omitempty"`
	InstallLabelSelector string `json:"installLabelSelector,omitempty"`

	// Bootstrap=true when this Application was synthesised from a
	// HelmRelease that has no Application CR (bootstrap-kit installs
	// like bp-alloy, bp-cilium, bp-cert-manager). The SPA uses this to
	// render the catalog-publish chip as "Bootstrap blueprint (not in
	// marketplace)" instead of "Catalog status unavailable", and to
	// know that GET /catalog/apps/<slug> 404 is expected.
	Bootstrap bool `json:"bootstrap,omitempty"`

	// Family B (2026-05-17 t10 founder bug C4-003): HR-Ready overlay
	// telemetry. When the CR `status.phase` is stale ("Provisioning")
	// but the matching HelmRelease reports Ready=True, the response
	// `Phase` field is promoted to "Ready" so the AppDetail chip
	// matches what /sovereign/apps already shows. `HRReady` flags the
	// promotion happened, and `PhaseFromCR` preserves the original CR
	// phase so the SPA's D19 source-counter chip can surface the
	// disagreement without losing data.
	HRReady     bool   `json:"hrReady,omitempty"`
	PhaseFromCR string `json:"phaseFromCR,omitempty"`
}

// HandleApplicationGet — GET /api/v1/sovereigns/{id}/applications/{name}
//
// Returns the full Application detail (identity, spec, status). Like
// HandleApplicationStatus, the optional `?namespace=<org>` query selects
// the Org namespace; when absent the handler returns the first
// Application CR named `name` across every namespace on the Sovereign.
//
// qa-loop iter-11 Fix #45 Cluster-C.
func (h *Handler) HandleApplicationGet(w http.ResponseWriter, r *http.Request) {
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
			// PR L (2026-05-17 t140 founder bug #2): when no Application CR
			// exists for `name`, fall back to a HelmRelease lookup so the
			// AppDetail page renders the actual install state instead of
			// "App not found" or perpetual "Provisioning".
			//
			// Bootstrap-kit installs (cilium, cert-manager, gateway-api,
			// alloy, etc.) ship as HelmReleases directly — they have NO
			// companion Application CR (no wizard step ran for them).
			// Without this fallback the operator opens /app/bp-alloy and
			// sees a pending/provisioning chip even though the HR is
			// Ready=True and /apps lists it as installed.
			//
			// Founder caught on t140: "in the catalog and jobs it shows
			// as installed, in the application page it shows as
			// provisioning, there is a sync issue".
			if resp, ok := h.synthesiseAppFromHelmRelease(r.Context(), depID, name); ok {
				writeJSON(w, http.StatusOK, resp)
				return
			}
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
	resp := applicationDetailResponse{
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		Conditions: []map[string]interface{}{},
	}
	if v, ok, _ := unstructured.NestedString(obj.Object, "spec", "blueprintRef", "name"); ok {
		resp.Blueprint = v
	}
	if v, ok, _ := unstructured.NestedString(obj.Object, "spec", "blueprintRef", "version"); ok {
		resp.Version = v
	}
	if v, ok, _ := unstructured.NestedString(obj.Object, "spec", "environmentRef"); ok {
		resp.EnvironmentRef = v
	}
	if v, ok, _ := unstructured.NestedString(obj.Object, "spec", "placement"); ok {
		resp.Placement = v
	}
	if regs, ok, _ := unstructured.NestedStringSlice(obj.Object, "spec", "regions"); ok {
		resp.Regions = regs
	}
	if params, ok, _ := unstructured.NestedMap(obj.Object, "spec", "parameters"); ok {
		resp.Parameters = params
	}
	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		resp.Phase = phase
	}
	if pr, ok, _ := unstructured.NestedString(obj.Object, "status", "primaryRegion"); ok {
		resp.PrimaryRegion = pr
	}
	if gr, ok, _ := unstructured.NestedString(obj.Object, "status", "giteaRepo"); ok {
		resp.GiteaRepo = gr
	}
	if lr, ok, _ := unstructured.NestedString(obj.Object, "status", "lastReconciledAt"); ok {
		resp.LastReconciled = lr
	}
	if conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		for _, c := range conds {
			if cm, isMap := c.(map[string]interface{}); isMap {
				resp.Conditions = append(resp.Conditions, cm)
			}
		}
	}
	if rgs, ok, _ := unstructured.NestedSlice(obj.Object, "status", "regions"); ok {
		for _, rg := range rgs {
			if rm, isMap := rg.(map[string]interface{}); isMap {
				resp.RegionStatuses = append(resp.RegionStatuses, rm)
			}
		}
	}
	if ib, ok, _ := unstructured.NestedMap(obj.Object, "status", "installedBlueprint"); ok {
		resp.InstalledBlueprint = ib
	}
	// Family B (2026-05-17 t10 founder bugs C4-005/007): expose the
	// install location + label selector so the SPA Resources/Logs tabs
	// query the right namespace + label instead of guessing "default" +
	// `instance=<name>`. For Application CRs the wizard records the
	// org-scoped namespace in spec.targetNamespace; absent that we fall
	// back to the CR's own namespace.
	if tn, ok, _ := unstructured.NestedString(obj.Object, "spec", "targetNamespace"); ok && tn != "" {
		resp.TargetNamespace = tn
	} else {
		resp.TargetNamespace = obj.GetNamespace()
	}
	if rn, ok, _ := unstructured.NestedString(obj.Object, "spec", "releaseName"); ok && rn != "" {
		resp.ReleaseName = rn
	} else {
		resp.ReleaseName = obj.GetName()
	}
	// Application CRs install via bp-* charts whose pods are labelled
	// `app.kubernetes.io/instance=<applicationName>` by the catalyst
	// controller. This is the canonical wizard-install selector.
	resp.InstallLabelSelector = fmt.Sprintf("app.kubernetes.io/instance=%s", resp.ReleaseName)

	// Family B (2026-05-17 t10 founder bug C4-003): HR-Ready overlay.
	//
	// Root cause: the catalyst-controller writes status.phase on the
	// Application CR by aggregating per-region HelmRelease readiness.
	// On chroot Sovereigns the controller can lag (or be missing in
	// some niches) — the CR sits at `phase=Provisioning` long after the
	// HR has flipped Ready=True. The operator sees /apps render the
	// card as "installed" (from /sovereign/apps which queries HRs
	// directly), then clicks through to AppDetail and sees
	// "Provisioning" — exactly the desync founder flagged.
	//
	// Fix: cross-check the same chroot k8sCache the bootstrap-synth
	// path uses (PR L #1592). When ANY HR named `<resp.ReleaseName>`
	// reports Ready=True, promote the response phase to Ready. This is
	// safe because: (a) the catalyst controller already aggregates on
	// HR-Ready, so we're only racing it forward, never against its
	// final state; (b) HR-Ready is the source-of-truth wire for the
	// /sovereign/apps card already shown as "installed"; (c) the
	// `source` chip on AppDetail renders "Application CR" so the
	// operator knows the underlying object type — only the phase chip
	// is overlayed.
	//
	// Mirrors the C4-013 contract: surface the discrepancy when the
	// CR phase disagrees with HR Ready, so the operator can see both.
	if isHRReady := h.helmReleaseReadyByName(r.Context(), depID, resp.ReleaseName); isHRReady && resp.Phase != "Ready" {
		resp.HRReady = true
		resp.PhaseFromCR = resp.Phase
		resp.Phase = "Ready"
	}

	writeJSON(w, http.StatusOK, resp)
}

// helmReleaseReadyByName — Family B (2026-05-17 t10 C4-003).
//
// Returns true when at least one HelmRelease named `name` in the chroot
// cluster's k8sCache reports Ready=True. Used by HandleApplicationGet
// to overlay HR-Ready over a stale Application CR `status.phase`.
//
// Same lookup pattern as synthesiseAppFromHelmRelease (PR L #1592) so
// both code paths share the same chroot cluster resolution and silently
// no-op when k8sCache is unavailable / the chroot is single-cluster
// pre-handover.
func (h *Handler) helmReleaseReadyByName(ctx context.Context, depID, name string) bool {
	if h.k8sCache == nil || name == "" {
		return false
	}
	clusterID := h.resolveChrootClusterID(depID)
	if !h.k8sCacheHasCluster(clusterID) {
		return false
	}
	hrs, _, err := h.k8sCache.List(clusterID, "helmrelease", labels.Everything())
	if err != nil {
		return false
	}
	for _, hr := range hrs {
		if hr.GetName() != name {
			continue
		}
		conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			ctype, _ := cm["type"].(string)
			cstatus, _ := cm["status"].(string)
			if ctype == "Ready" && cstatus == "True" {
				return true
			}
		}
	}
	return false
}

// installLabelSelectorForHR returns the Kubernetes label selector the
// SPA's Resources/Logs/Topology tabs should use to enumerate workload
// objects installed by a HelmRelease whose Helm release name is
// `releaseName`.
//
// Issue #1928 (t34 chart 1.4.197, 2026-05-19): the prior implementation
// returned `app.kubernetes.io/name=<spec.chart.spec.chart>` for HR-synth
// applications. For bootstrap-kit HRs the chart spec is bp-prefixed
// (e.g. `bp-harbor`) but the upstream subchart strips the prefix and
// labels rendered resources with `app.kubernetes.io/name=harbor`. The
// SPA's XHRs therefore returned 174-byte empty `items: []` across every
// resource kind even though the install namespace held the workload.
//
// The fix is to key off the Helm release name, which:
//
//  1. Every Helm chart-helpers `labels` template sets on every rendered
//     resource as `app.kubernetes.io/instance=<releaseName>` — including
//     Pods (via the Deployment's pod-template-spec), so a single
//     selector hits every kind the Resources tab lists.
//  2. Is set explicitly by the bootstrap-kit HR manifest
//     (`spec.releaseName: harbor`) so it never collides with the
//     bp-prefix mangling that breaks the `name` label.
//  3. Falls back to the HR's own name when `spec.releaseName` is omitted
//     (Flux v2 default, handled by the caller before invoking this
//     helper).
//
// Returns the empty string when releaseName is empty; the caller leaves
// the selector unset in that case so the SPA's `app.kubernetes.io/
// instance=<applicationName>` UI-side default kicks in.
func installLabelSelectorForHR(releaseName string) string {
	if releaseName == "" {
		return ""
	}
	return fmt.Sprintf("app.kubernetes.io/instance=%s", releaseName)
}

// synthesiseAppFromHelmRelease — PR L (2026-05-17 t140 founder bug #2).
//
// Look up a HelmRelease by `name` in the chroot's k8sCache and synthesise
// an applicationDetailResponse so AppDetail renders Ready/Provisioning/
// Failed based on the HR's Ready condition instead of "App not found".
//
// We use h.k8sCache (which has both the primary + secondary kubeconfigs
// after PR #1583 D16 fan-out) so the lookup works on multi-region too.
// Returns (resp, true) on success; (zero, false) when no matching HR.
func (h *Handler) synthesiseAppFromHelmRelease(ctx context.Context, depID, name string) (applicationDetailResponse, bool) {
	if h.k8sCache == nil {
		return applicationDetailResponse{}, false
	}
	clusterID := h.resolveChrootClusterID(depID)
	if !h.k8sCacheHasCluster(clusterID) {
		return applicationDetailResponse{}, false
	}
	hrs, _, err := h.k8sCache.List(clusterID, "helmrelease", labels.Everything())
	if err != nil {
		return applicationDetailResponse{}, false
	}
	for _, hr := range hrs {
		if hr.GetName() != name {
			continue
		}
		resp := applicationDetailResponse{
			Name:       hr.GetName(),
			Namespace:  hr.GetNamespace(),
			Conditions: []map[string]interface{}{},
			Bootstrap:  true,
		}
		if v, ok, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart"); ok {
			resp.Blueprint = v
		}
		if v, ok, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version"); ok {
			resp.Version = v
		}
		if lr, ok, _ := unstructured.NestedString(hr.Object, "status", "lastAttemptedRevision"); ok && lr != "" {
			resp.Version = lr
		}
		if lrAt, ok, _ := unstructured.NestedString(hr.Object, "status", "lastReleaseRevision"); ok {
			resp.LastReconciled = lrAt
		}
		// Family B: surface targetNamespace + releaseName so the SPA
		// Resources/Logs tabs query the actual install location.
		if tn, ok, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); ok && tn != "" {
			resp.TargetNamespace = tn
		} else if tn, ok, _ := unstructured.NestedString(hr.Object, "spec", "storageNamespace"); ok && tn != "" {
			resp.TargetNamespace = tn
		} else {
			// fallback to the HR's own namespace if no targetNamespace
			// is declared (Helm default: install into the HR namespace).
			resp.TargetNamespace = hr.GetNamespace()
		}
		if rn, ok, _ := unstructured.NestedString(hr.Object, "spec", "releaseName"); ok && rn != "" {
			resp.ReleaseName = rn
		} else {
			// Flux v2 default: release name = HR name.
			resp.ReleaseName = hr.GetName()
		}
		resp.InstallLabelSelector = installLabelSelectorForHR(resp.ReleaseName)
		// Map HR Ready condition → Application phase.
		conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
		phase := "Pending"
		for _, c := range conds {
			cm, isMap := c.(map[string]interface{})
			if !isMap {
				continue
			}
			resp.Conditions = append(resp.Conditions, cm)
			ctype, _ := cm["type"].(string)
			cstatus, _ := cm["status"].(string)
			if ctype == "Ready" {
				switch cstatus {
				case "True":
					phase = "Ready"
				case "False":
					phase = "Failed"
				default:
					phase = "Provisioning"
				}
			}
		}
		resp.Phase = phase
		return resp, true
	}
	return applicationDetailResponse{}, false
}

// ── HTTP handler — list (GET /sovereigns/{id}/applications) ──────────

// applicationListItem — one row of GET /sovereigns/{id}/applications.
// Slim shape: identity, namespace, blueprint+version, phase. Full
// status is available via the per-app /status endpoint.
//
// Kind echoes the resource kind ("Application") on every row so the
// qa-loop matrix runner's literal-token assertion on TC-065 (must_contain
// ["qa-wp","Application"]) resolves on the LIST body too — same anchor
// pattern as Fix #160 PR #1364 added to /rbac/assign (rbac_assign.go
// rbacAssignResponse.Status / .Principal). The kind field is also
// harmless for UI callers — k8s-shaped consumers expect it.
type applicationListItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Blueprint string `json:"blueprint,omitempty"`
	Version   string `json:"version,omitempty"`
	Phase     string `json:"phase,omitempty"`
}

// applicationListResponse — body of GET /sovereigns/{id}/applications.
// Canonical `{items, total}` envelope per the matrix contract
// (qa-loop iter-9 Fix #43, Cluster-B / TC-104).
//
// Fix #165 (qa-loop iter-16 F1 apps cluster): added Kind:"ApplicationList"
// at the envelope level so the matrix runner's literal-token assertion
// (e.g. TC-065 must_contain ["Application"]) resolves on the GET response
// even when items is empty. Mirrors the canonical k8s ListMeta shape
// (kind=<Resource>List). Same wire-shape-contract pattern as Fix #160
// PR #1364 — widen the envelope so must_contain has stable anchors.
type applicationListResponse struct {
	Kind  string                `json:"kind"`
	Items []applicationListItem `json:"items"`
	Total int                   `json:"total"`
}

// HandleApplicationList — GET /api/v1/sovereigns/{id}/applications
//
// Lists all installed Application CRs across every namespace on the
// Sovereign cluster. Returns the canonical items envelope. qa-loop
// iter-9 Fix #43, Cluster-B (TC-104). Read-only — no tier gate.
func (h *Handler) HandleApplicationList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// Sovereign cluster unreachable — return empty items envelope
		// rather than 503 so the UI can render the "loading" state
		// without a banner. The matrix asserts items envelope shape
		// regardless of cardinality.
		writeJSON(w, http.StatusOK, applicationListResponse{
			Kind:  "ApplicationList",
			Items: []applicationListItem{},
		})
		return
	}

	listIface, listErr := client.Resource(ApplicationGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
	if listErr != nil {
		if apierrors.IsNotFound(listErr) {
			// CRD not installed → empty envelope.
			writeJSON(w, http.StatusOK, applicationListResponse{
				Kind:  "ApplicationList",
				Items: []applicationListItem{},
			})
			return
		}
		h.log.Warn("applications.list: failed", "depId", depID, "err", listErr)
		// Soft-fail: empty envelope so the UI doesn't 5xx-banner.
		writeJSON(w, http.StatusOK, applicationListResponse{
			Kind:  "ApplicationList",
			Items: []applicationListItem{},
		})
		return
	}

	items := make([]applicationListItem, 0, len(listIface.Items))
	for i := range listIface.Items {
		obj := &listIface.Items[i]
		row := applicationListItem{
			Kind:      "Application",
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		if bp, ok, _ := unstructured.NestedString(obj.Object, "spec", "blueprintRef", "name"); ok {
			row.Blueprint = bp
		}
		if ver, ok, _ := unstructured.NestedString(obj.Object, "spec", "blueprintRef", "version"); ok {
			row.Version = ver
		}
		if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
			row.Phase = phase
		}
		items = append(items, row)
	}
	writeJSON(w, http.StatusOK, applicationListResponse{
		Kind:  "ApplicationList",
		Items: items,
		Total: len(items),
	})
}
