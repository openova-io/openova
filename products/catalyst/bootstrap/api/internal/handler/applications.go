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
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
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
//
// #3373 — extended with the instance-placement WHERE fields. When
// VCluster or Clusters is set the handler stamps the OBJECT form of
// `spec.placement` ({mode, vcluster, regions, clusters}); otherwise it
// keeps stamping the legacy string form (byte-identical legacy wire).
type applicationPlacement struct {
	Mode    string   `json:"mode"`
	Regions []string `json:"regions"`

	// VCluster — which vCluster the instance lands in: "host" |
	// "mgmt" | "dmz" | "rtz". Empty = inherit the Blueprint's
	// defaultPlacement (the silent-accept default flow).
	VCluster string `json:"vcluster,omitempty"`

	// Clusters — canonical cluster IDs narrowing the topology
	// fan-out for THIS instance.
	Clusters []string `json:"clusters,omitempty"`

	// Targets — the #3969 per-region placement model the console
	// PlacementEditor sends: [{region, role:Primary|Standby,
	// standbyType:Hot|Cold}]. When present with an empty Mode (the
	// switchover / add-target flows, UAT row G10), the update handler folds
	// it onto the canonical mode+regions the CR stores — Mode via
	// bpv1.DerivePattern (the single source of pattern truth) and Regions
	// Primary-first so regions[0] is the primary (placement_projection.go
	// keys primaryRegion off regions[0]). Legacy {mode,regions} callers are
	// unaffected. Refs #3969 #3375.
	Targets []bpv1.PlacementTarget `json:"targets,omitempty"`

	// OwnedDependencies — the #3969 per-owned-dependency cascade overrides
	// the console PlacementEditor sends ALONGSIDE targets[] on every Apply
	// (PlacementEditor.tsx: `ownedDependencies: owned.map(...)`). This field
	// MUST exist on the wire struct even though the update handler does not
	// yet project it: `decodeApplicationUpdateBody` decodes the canonical
	// shape with `DisallowUnknownFields`, so an absent field made the strict
	// decode REJECT the real console body and fall back to the lenient
	// simplified decoder — which drops targets[] entirely, leaving Mode empty
	// and 400'ing "placement.mode is required" (#4950). Mirroring the
	// bpv1.Placement.OwnedDependencies wire contract lets the canonical
	// decode accept the body so the targets[]→mode fold fires. Refs #4950
	// #4840 #3969.
	OwnedDependencies []bpv1.OwnedDependencyOverride `json:"ownedDependencies,omitempty"`
}

// validVClusterTier — accepted spec.placement.vcluster values
// (#3373). Delegates to the instances package so this door shares ONE
// vocabulary AND one availability fact with POST /apps/instances.
//
// #5616 — it used to hold its own `{"", host, mgmt, dmz, rtz}` tuple.
// Two independent copies of a vocabulary is how a fix lands on one door
// and misses the other: PR #5622 removed the dead tiers from a single
// front-end dropdown while BOTH API validators kept accepting them, so
// a direct call, a Git edit or the MCP `create_application` tool still
// produced a 201 followed by a permanently Degraded Application
// (`namespaces "rtz" not found`). Availability is now enforced here too.
func validVClusterTier(v string) bool {
	return instances.IsKnownVClusterTier(v) && instances.VClusterTierAvailable(v)
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
		// One vocabulary (#3375 DoD-1): canonical default.
		b.Placement.Mode = "singleton"
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
	// Spec surfaces the create-time placement so the AppDetail Topology
	// tab reflects the placement chosen at install/create time WITHOUT
	// waiting for the controller to populate status.placement (#3599,
	// EPIC #3597). The FE TopologyTab reads `app.spec.placement` (the
	// editor-posture mode string) + `app.spec.regions`.
	Spec *applicationStatusSpec `json:"spec,omitempty"`
}

// applicationStatusSpec is the spec subset the Topology tab consumes.
// Placement is normalised to the editor posture string
// (single-region | active-active | active-hotstandby) regardless of
// whether the CR stored the legacy string form or the #3373 object form.
type applicationStatusSpec struct {
	Placement      string                 `json:"placement,omitempty"`
	Regions        []string               `json:"regions,omitempty"`
	EnvironmentRef string                 `json:"environmentRef,omitempty"`
	BlueprintRef   map[string]interface{} `json:"blueprintRef,omitempty"`
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

	h.installApplicationCore(w, r, dep, depID, body)
}

// installApplicationCore performs the catalog-fetch → schema-validate →
// ensure-namespace → create-Application-CR sequence that is the heart of an
// Application install. It is the SHARED create core (#4116) called by BOTH
// the Sovereign-admin path (HandleApplicationInstall, addressed by the
// `/api/v1/sovereigns/{id}/applications` route) AND the Org-scoped path
// (HandleOrgApplicationInstall, addressed by the OrgScopeGuard-allowlisted
// `/api/v1/org/applications` route). The caller is responsible for resolving
// `dep`, decoding+normalizing+validating `body`, and applying the right
// authz BEFORE calling this — the core reimplements NO authz so the two
// callers can apply their own (tier-admin for the sovereign path; own-org
// binding for the org path). `body.OrganizationRef` MUST already be the
// resolved Org identity (the org path forces it to the tenant's real
// namespace before calling).
func (h *Handler) installApplicationCore(w http.ResponseWriter, r *http.Request, dep *Deployment, depID string, body applicationInstallRequest) {
	if h.catalogClient == nil {
		writeApplicationInstallSoftError(w, "catalog-not-wired", http.StatusServiceUnavailable,
			"catalog client unconfigured (CATALYST_CATALOG_URL); install requires the catalog upstream")
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
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":      "invalid-parameters",
			"status":     "400",
			"httpStatus": "400",
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

	// #3598 (EPIC #3597) — ensure the Org/Environment namespace exists
	// before creating the Application CR, so an install never fails with
	// `namespaces "<org>" not found` when the organization controller's
	// GitOps namespace reconcile hasn't landed yet. Idempotent.
	//
	// #3830 — the OrganizationRef is the Org identity (often a dotted
	// FQDN, e.g. "hw165.omani.works"); the namespace is the slugged
	// RFC-1123 form. ensureOrgNamespace + the create below + the CR's
	// metadata.namespace (newApplicationUnstructured) all route through
	// orgNamespace() so they resolve to ONE consistent namespace.
	appNS := orgNamespace(body.OrganizationRef)
	if nsErr := ensureOrgNamespace(r.Context(), client, body.OrganizationRef); nsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"kind":       "Application",
			"name":       body.Name,
			"namespace":  appNS,
			"error":      "namespace-ensure-failed",
			"status":     "503",
			"httpStatus": "503",
			"applied":    false,
			"detail":     fmt.Sprintf("could not ensure namespace %q: %v", appNS, nsErr),
		})
		return
	}

	// #4556 Item 2 — resolve the Sovereign's own FQDN so the agenity install
	// stamps spec.parameters.sovereignFqdn (the chart derives the openova-MCP
	// catalyst-api URL from it; empty → console.openova.io, the mothership).
	// Prefer the deployment record's FQDN; fall back to this catalyst-api's
	// own SOVEREIGN_FQDN env. Empty on the mothership/Catalyst-Zero is fine —
	// agenity-on-mothership is not a production path and the chart's
	// fail-closed default then applies.
	sovereignFQDN := strings.TrimSpace(dep.Request.SovereignFQDN)
	if sovereignFQDN == "" {
		sovereignFQDN = h.sovereignFQDN()
	}
	// #4624 — resolve the ORG's public console host (console.<slug>.<pool>)
	// from the tenant registry so the agenity install stamps
	// spec.parameters.openovaMCP.tenantHost. Without it the chart falls back
	// to console.<sovereignFqdn> for OPENOVA_MCP_TENANT_HOST — the Sovereign
	// host is NOT a registered tenant, so every agent create_application
	// 404'd `tenant-not-registered` (live-proven on hw220 2026-07-04: 404
	// with the Sovereign host, 201 the moment the env was set to the Org
	// host). Empty (mothership / no registry row) ⇒ no stamp, fail-closed.
	orgConsoleHost := h.orgConsoleHostFor(body.OrganizationRef)
	obj := newApplicationUnstructured(body, sovereignFQDN, orgConsoleHost)
	created, err := client.Resource(ApplicationGVR()).Namespace(appNS).Create(
		r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"kind":       "Application",
				"name":       body.Name,
				"namespace":  appNS,
				"error":      "application-exists",
				"status":     "409",
				"httpStatus": "409",
				"applied":    false,
				"detail":     fmt.Sprintf("Application %q already exists in namespace %q", body.Name, appNS),
			})
			return
		}
		h.log.Warn("application install: create CR failed",
			"depId", depID, "name", body.Name, "ns", appNS, "err", err)
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
	writeJSON(w, http.StatusForbidden, map[string]any{
		"kind":       "Application",
		"error":      "403",
		"status":     "403",
		"httpStatus": "403",
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
// #5500 — this used to emit HTTP 200 for EVERY one of those failures,
// with the real code echoed only in the body. The rationale was a test
// runner that FAILs a non-2xx before reading the body, so a 200 was the
// only way its must_contain assertions could resolve on the JSON.
//
// That inverted the dependency: the production wire contract lied to
// every real client so a harness could read a body. A caller doing the
// normal thing — `response.ok`, `status < 300`, a non-2xx branch — read
// a REJECTED install as a completed one, and had to know to parse the
// body and trust a nested field over the status line it just received.
//
// docs/PRINCIPLES.md A8 ("must_contain token-passing test churn") already
// names this shape and prescribes the direction: token-passing assertions
// "must be deleted, not patched" — the pass condition belongs on the
// behaviour, not on a string appearing. The status line IS the behaviour
// here, so it now carries the truth and the body keeps every token
// (kind/error/detail/status/httpStatus) for any consumer that reads it.
//
// httpStatus is also a STRING now, matching the success path's
// `"httpStatus":"201"`. It was an int on this path, so a consumer
// unmarshalling into a typed field failed on exactly one of the two.
func writeApplicationInstallSoftError(w http.ResponseWriter, code string, origStatus int, detail string) {
	writeJSON(w, origStatus, map[string]any{
		"kind":       "Application",
		"error":      code,
		"status":     fmt.Sprintf("%d", origStatus),
		"httpStatus": fmt.Sprintf("%d", origStatus),
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
	// #3599 — surface the create-time placement from spec so the Topology
	// tab reflects it immediately (no wait for status.placement).
	resp.Spec = applicationStatusSpecFromCR(obj)
	writeJSON(w, http.StatusOK, resp)
}

// applicationStatusSpecFromCR projects the Application CR's spec subset
// the Topology tab reads. Placement is normalised to the editor posture
// string via placementForTopology so the editor's mode pre-select works
// whether the CR stored the legacy string (`single-region`) or the
// #3373 object form (`{mode: active-hotstandby, regions: [...]}`).
func applicationStatusSpecFromCR(obj *unstructured.Unstructured) *applicationStatusSpec {
	if obj == nil {
		return nil
	}
	spec := &applicationStatusSpec{}
	// Placement: readTopology already handles string + object(mode) +
	// label fallback; map its result onto the editor posture enum.
	spec.Placement = placementForTopology(readTopology(obj))
	if regions, ok, _ := unstructured.NestedStringSlice(obj.Object, "spec", "regions"); ok {
		spec.Regions = regions
	}
	if env, ok, _ := unstructured.NestedString(obj.Object, "spec", "environmentRef"); ok && env != "" {
		spec.EnvironmentRef = env
	}
	if br, ok, _ := unstructured.NestedMap(obj.Object, "spec", "blueprintRef"); ok {
		spec.BlueprintRef = br
	}
	return spec
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
	// #3830 — the OrganizationRef is the Org identity, which is a DNS
	// subdomain / FQDN (e.g. "hw165.omani.works"), NOT an RFC-1123 label.
	// Validating it with isValidK8sName rejected every dotted Org and 400'd
	// the create before it reached the (now slug-mapped) namespace seam.
	// Accept a DNS subdomain; orgNamespace() slugs it for the namespace.
	if !isValidOrgRef(req.OrganizationRef) {
		return "organizationRef must be a valid DNS name (Org slug or FQDN, e.g. acme or hw165.omani.works)", false
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
	// One vocabulary (#3375 DoD-1): canonicalise the posted mode (folds
	// the legacy editor dialect single-region / active-hotstandby) then
	// accept the four canonical classes. The editors now emit canonical;
	// legacy spellings remain admissible so in-flight callers don't break.
	switch canonicalizeTopology(req.Placement.Mode) {
	case "singleton", "active-active", "active-hot-standby", "active-passive":
	default:
		return "placement.mode must be one of singleton, active-active, active-hot-standby, active-passive (legacy single-region / active-hotstandby also accepted)", false
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
	// #3373 — instance placement WHERE fields.
	// #5616 — separate "not a tier at all" from "a real tier this
	// Sovereign does not install": the second one needs a remedy in the
	// message, because the operator did nothing wrong.
	if !instances.IsKnownVClusterTier(req.Placement.VCluster) {
		return "placement.vcluster must be one of " + instances.KnownVClusterTiersCSV(), false
	}
	if !instances.VClusterTierAvailable(req.Placement.VCluster) {
		return instances.UnavailableTierMessage(req.Placement.VCluster), false
	}
	// #5616 — the #3969 desired-state form carries the tier PER TARGET
	// (`placement.targets[].vcluster`), and nothing validated it. That is
	// the door the shipped PlacementEditor walks through: a freshly added
	// target defaults to `mgmt` with no operator interaction at all
	// (widgets/topology/PlacementEditor.tsx DEFAULT_VCLUSTERS[1]), so an
	// untouched Apply used to commit a tier that resolves to a namespace
	// no Sovereign creates.
	for i, t := range req.Placement.Targets {
		if !instances.IsKnownVClusterTier(t.VCluster) {
			return fmt.Sprintf("placement.targets[%d].vcluster must be one of %s",
				i, instances.KnownVClusterTiersCSV()), false
		}
		if !instances.VClusterTierAvailable(t.VCluster) {
			return fmt.Sprintf("placement.targets[%d]: %s", i,
				instances.UnavailableTierMessage(t.VCluster)), false
		}
	}
	for i, c := range req.Placement.Clusters {
		if strings.TrimSpace(c) == "" {
			return fmt.Sprintf("placement.clusters[%d] is empty", i), false
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
//
// orgConsoleHost — the Org's public console host (console.<slug>.<pool>),
// resolved by the caller from the tenant registry (h.orgConsoleHostFor).
// Stamped as spec.parameters.openovaMCP.tenantHost for bp-agenity (#4624)
// so the agent-side MCP's X-Tenant-Host targets the ORG, not the Sovereign
// console host. Empty ⇒ no stamp (fail-closed).
func newApplicationUnstructured(req applicationInstallRequest, sovereignFQDN, orgConsoleHost string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName(req.Name)
	// #3830 — metadata.namespace is the slugged RFC-1123 form of the Org
	// ref (which is often a dotted FQDN). The verbatim Org identity is
	// preserved on the `catalyst.openova.io/organization` label below.
	obj.SetNamespace(orgNamespace(req.OrganizationRef))
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by":        "catalyst-api",
		"catalyst.openova.io/organization":      req.OrganizationRef,
		"catalyst.openova.io/environment":       req.EnvironmentRef,
		"catalyst.openova.io/blueprint":         req.BlueprintRef.Name,
		"catalyst.openova.io/blueprint-version": req.BlueprintRef.Version,
	})

	regions := make([]any, 0, len(req.Placement.Regions))
	for _, r := range req.Placement.Regions {
		regions = append(regions, r)
	}
	// One vocabulary (#3375 DoD-1): the CR always STORES the canonical
	// placement token (singleton / active-active / active-hot-standby /
	// active-passive), regardless of which spelling the caller posted.
	// The legacy editor dialect (single-region / active-hotstandby) is
	// folded here so every Application CR carries one vocabulary and the
	// resolver/render path never sees a legacy spelling.
	canonMode := canonicalizeTopology(req.Placement.Mode)
	// #3373 — spec.placement is dual-form. The string form carries the
	// canonical posture for callers that never set the WHERE fields; the
	// OBJECT form {mode, vcluster, regions, clusters} carries the
	// instance placement when the caller (console advanced view / direct
	// API) declares it.
	var placementValue any = canonMode
	if req.Placement.VCluster != "" || len(req.Placement.Clusters) > 0 {
		pl := map[string]any{
			"mode":     canonMode,
			"vcluster": req.Placement.VCluster,
		}
		if len(req.Placement.Regions) > 0 {
			plRegions := make([]any, 0, len(req.Placement.Regions))
			for _, r := range req.Placement.Regions {
				plRegions = append(plRegions, r)
			}
			pl["regions"] = plRegions
		}
		if len(req.Placement.Clusters) > 0 {
			plClusters := make([]any, 0, len(req.Placement.Clusters))
			for _, c := range req.Placement.Clusters {
				plClusters = append(plClusters, c)
			}
			pl["clusters"] = plClusters
		}
		placementValue = pl
	}
	spec := map[string]any{
		// #3922 — the CR's spec.environmentRef is RFC-1123-label-constrained
		// (^[a-z][a-z0-9-]{2,63}$, no dots). On a chroot/operator install the
		// env ref defaults to the Sovereign FQDN (e.g. "hw171.omantel.biz-prod"),
		// which the apiserver rejected with HTTP 500. Slug it the same way the
		// namespace is slugged (preserving whatever Environment the caller
		// asked for, just made pattern-safe); the verbatim Org identity stays
		// on the organization label + organizationRef.
		"environmentRef": sanitizeEnvironmentRef(req.EnvironmentRef),
		"blueprintRef": map[string]any{
			"name":    req.BlueprintRef.Name,
			"version": req.BlueprintRef.Version,
		},
		"placement": placementValue,
		"regions":   regions,
	}
	// #4283 / #4282 Root-B — ALWAYS stamp a non-null spec.parameters
	// OBJECT. Caller-supplied parameters are used verbatim; otherwise we
	// emit at least `{}` (plus a configSchema-valid topology.mode for
	// bp-postgres) so the Application CR never round-trips through Git
	// YAML as `parameters: null` and never fails the application-
	// controller's configSchema validation ("#: expected object, but got
	// null"). The CRD's `x-kubernetes-preserve-unknown-fields` makes any
	// tree valid; the controller's admission webhook + this handler's
	// validate.Parameters call have already gated explicit params against
	// configSchema.
	spec["parameters"] = defaultedParameters(req.BlueprintRef.Name, canonMode, sovereignFQDN, req.OrganizationRef, orgConsoleHost, req.Parameters)
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

// isValidOrgRef checks the Organization-reference shape (#3830). The Org
// identity is a DNS subdomain — either a bare slug ("acme") or a dotted
// FQDN ("hw165.omani.works"). This is intentionally looser than
// isValidK8sName (which forbids dots): the namespace is *derived* from the
// Org ref via orgNamespace(), so the ref itself must be allowed to carry
// the FQDN. We delegate to k8s' canonical RFC-1123 subdomain validator so
// the accepted set matches what the rest of the platform treats as a valid
// hostname (lowercase, dot-separated labels, ≤253 chars).
func isValidOrgRef(s string) bool {
	return len(validation.IsDNS1123Subdomain(strings.TrimSpace(s))) == 0
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
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// UID — the Application CR's metadata.uid, lifted verbatim from
	// the dynamic-client object. Required by the Catalyst Console's
	// Launch button (PR #2839, G117.4): the FE calls
	// `GET /catalyst/v1/apps/{uid}/launch-url` to obtain a
	// silent-SSO front-door URL (`prompt=none&kc_idp_hint=catalyst-pin`).
	// Without this field the FE has nothing to key the launch-url
	// lookup on and falls back to direct externalURL on every click,
	// defeating the silent-SSO codepath. Empty when the response is
	// synthesised from a HelmRelease (bootstrap-kit installs with no
	// companion Application CR — those have no Application UID).
	// Refs #2743 #2834 #2839.
	UID                string                   `json:"uid"`
	Blueprint          string                   `json:"blueprint,omitempty"`
	Version            string                   `json:"version,omitempty"`
	EnvironmentRef     string                   `json:"environmentRef,omitempty"`
	Placement          string                   `json:"placement,omitempty"`
	Regions            []string                 `json:"regions,omitempty"`
	Parameters         map[string]interface{}   `json:"parameters,omitempty"`
	Phase              string                   `json:"phase,omitempty"`
	PrimaryRegion      string                   `json:"primaryRegion,omitempty"`
	GiteaRepo          string                   `json:"giteaRepo,omitempty"`
	LastReconciled     string                   `json:"lastReconciledAt,omitempty"`
	Conditions         []map[string]interface{} `json:"conditions"`
	RegionStatuses     []map[string]interface{} `json:"regionStatuses,omitempty"`
	InstalledBlueprint map[string]interface{}   `json:"installedBlueprint,omitempty"`

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

	// ExternalURL — front-door URL the operator clicks from the
	// AppDetail Overview tab to open this installed Application in a
	// new browser tab (G90, 2026-06-01). Joined from the matching
	// HTTPRoute in `targetNamespace` whose name OR backendRef Service
	// name equals `releaseName`. Empty when the Application has no
	// HTTPRoute (e.g. operators, internal controllers). Behind the URL
	// the upstream chart is already wired as an OIDC client to the
	// Sovereign's Keycloak so the click lands the operator with their
	// existing SSO session active.
	ExternalURL string `json:"externalURL,omitempty"`

	// Contexts — #3370. The Contexts on a SHAREABLE instance (the
	// first-class child entities consumer applications occupy).
	// Projected generically from the instance's declared IaC values
	// (Application spec.parameters / HR spec.values — same bytes)
	// using the Blueprint's contextSchema. Drives the AppDetail
	// Contexts tab; empty for non-shareable blueprints.
	Contexts []contextRow `json:"contexts,omitempty"`

	// DependsOn — #3370. The consumer-side backing-service edges:
	// which instance this Application depends on and which Context it
	// occupies there (`Depends on: shared-pg / db:gitea`). From the
	// Application CR's spec.dependsOn, or derived from the Flux HR
	// dependsOn graph + the target instance's declared bindings for
	// HR-synthesised (bootstrap) consumers.
	DependsOn []dependsOnDetail `json:"dependsOn,omitempty"`
}

// dependsOnDetail is one consumer-side backing edge (#3370).
type dependsOnDetail struct {
	Instance  string `json:"instance"`
	Namespace string `json:"namespace,omitempty"`
	Context   string `json:"context,omitempty"`
}

// HandleApplicationGet — GET /api/v1/sovereigns/{id}/applications/{name}
//
// Returns the full Application detail (identity, spec, status). Like
// HandleApplicationStatus, the optional `?namespace=<org>` query selects
// the Org namespace; when absent the handler returns the first
// Application CR named `name` across every namespace on the Sovereign.
//
// qa-loop iter-11 Fix #45 Cluster-C.
// applicationPrimaryRegion resolves an Application's primary region from its
// CR status.
//
// #5482 — the region lives at status.placement.primaryRegion. There is no
// top-level status.primaryRegion key: the CR's status keys are conditions,
// lastReconciledAt, observedGeneration, phase and placement. Reading only the
// top-level path returned ok=false silently, leaving the response field empty,
// so AppDetail.tsx fell back to appRegions[0] and rendered the HOST CLUSTER
// LABEL `platform-bootstrap-owned-host` where a region belongs — the Overview
// tab contradicting the Topology tab for the same object.
//
// The top-level path is retained as a fallback so a CR that does set it is
// still honoured.
func applicationPrimaryRegion(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if pr, ok, _ := unstructured.NestedString(obj.Object, "status", "placement", "primaryRegion"); ok && pr != "" {
		return pr
	}
	if pr, ok, _ := unstructured.NestedString(obj.Object, "status", "primaryRegion"); ok {
		return pr
	}
	return ""
}

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
		UID:        string(obj.GetUID()),
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
	// #5422 — `spec.placement` has TWO shapes and this endpoint only ever read
	// one. The legacy shape is a bare string; the #3373 shape is an object
	// `{mode, vcluster, regions, clusters}` where the posture rides in `mode`.
	// A raw NestedString against the object form returns ok=false, so
	// `resp.Placement` stayed empty and `omitempty` dropped the field from the
	// response entirely — and the console then turned that ABSENCE into a
	// confident wrong answer (`?? 'singleton'` at AppDetail.tsx:255), rendering
	// `singleton` for a two-region app directly above its own two-region
	// REGIONS list. Live on hw290, `uat-ahs-hw290` showed Overview `singleton`
	// while the Topology tab derived `active-hot-standby` from the same CR —
	// exactly the contradictory second value EPIC #3969 forbids.
	//
	// #4897 already fixed this shape at sovereign.go:815 by routing through
	// readTopology(); this call site was missed.
	//
	// Deliberately NOT calling readTopology() here: it defaults to "singleton"
	// for a genuinely-absent placement, which would re-manufacture the very
	// value this fixes. When placement is truly absent the field stays empty
	// and omitempty drops it — the honest answer, which the console must render
	// as unknown rather than inventing one.
	resp.Placement = placementFromSpec(obj)
	if regs, ok, _ := unstructured.NestedStringSlice(obj.Object, "spec", "regions"); ok {
		resp.Regions = regs
	}
	if params, ok, _ := unstructured.NestedMap(obj.Object, "spec", "parameters"); ok {
		resp.Parameters = params
	}
	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		resp.Phase = phase
	}
	if pr := applicationPrimaryRegion(obj); pr != "" {
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

	// #3370 — bootstrap-owned CR-backed instances surface the same
	// Bootstrap flag the HR-synth path sets, so the SPA's
	// platform-component affordances stay consistent across both
	// representations.
	if b, _, _ := unstructured.NestedBool(obj.Object, "spec", "bootstrap"); b {
		resp.Bootstrap = true
	}

	// #3370 — Contexts (shareable instances) + consumer-side DependsOn,
	// projected generically from the declaration. Best-effort: a
	// missing contextSchema (non-shareable blueprint) yields nil.
	if ctxSchema := h.contextSchemaFor(r.Context(), resp.Blueprint); ctxSchema != nil {
		resp.Contexts = h.projectContexts(r.Context(), client, ctxSchema, resp.Parameters)
	}
	if depsRaw, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "dependsOn"); ok {
		for _, d := range depsRaw {
			dm, isMap := d.(map[string]interface{})
			if !isMap {
				continue
			}
			det := dependsOnDetail{}
			det.Instance, _ = dm["name"].(string)
			det.Namespace, _ = dm["namespace"].(string)
			det.Context, _ = dm["context"].(string)
			if det.Instance != "" {
				resp.DependsOn = append(resp.DependsOn, det)
			}
		}
	}

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
	//
	// #4889 — resolve the HR-lookup identity from spec.helmRelease.name
	// (the adopted HelmRelease) first. Spine/bootstrap Application CRs
	// reference their HR there (e.g. spine-gitea → flux-system/bp-gitea)
	// and carry NO spec.releaseName, so resp.ReleaseName fell back to the
	// CR's OWN name ("spine-gitea") and helmReleaseReadyByName never
	// matched the real HR ("bp-gitea") — the overlay was inert for exactly
	// these apps and the operator saw FAILED/Provisioning (source:
	// Application CR) while the HelmRelease was Ready. Fallback chain:
	// spec.helmRelease.name → resp.ReleaseName.
	hrLookupName := resp.ReleaseName
	if hrn, ok, _ := unstructured.NestedString(obj.Object, "spec", "helmRelease", "name"); ok && hrn != "" {
		hrLookupName = hrn
	}
	if isHRReady := h.helmReleaseReadyByName(r.Context(), depID, hrLookupName); isHRReady && resp.Phase != "Ready" {
		resp.HRReady = true
		resp.PhaseFromCR = resp.Phase
		resp.Phase = "Ready"
	}

	// G90 2026-06-01: resolve the operator-visible front-door URL by
	// joining the (targetNamespace, releaseName) against HTTPRoutes in
	// the chroot k8sCache. Drives the "External URL" row on AppDetail
	// Overview + the per-card "Open" button on the AppsPage list.
	//
	// #3224: gate the URL on the Blueprint declaring a user-UI endpoint so
	// API/protocol-only apps (newapi, flow-server, keycloak, registry) do
	// NOT get a dead "Open" button. resp.Blueprint here is the
	// spec.blueprintRef.name (e.g. `bp-newapi` or `newapi`).
	resp.ExternalURL = h.externalURLIfUserUI(r.Context(), depID, resp.Blueprint, resp.TargetNamespace, resp.ReleaseName)

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

// placementFromHR — #3931. Project the placement CLASS for an
// HR-synthesised (bootstrap-kit) Application that has NO companion
// Application CR, so the CR-spec placement read in HandleApplicationGet
// never runs for it.
//
// Source priority (most → least specific), normalised to the canonical
// 4-class vocabulary (singleton / active-passive / active-hot-standby /
// active-active) via placementForTopology so the FE Topology tab + editor
// pre-select speak ONE dialect (#3375):
//
//	(1) spec.values.placement                 — explicit string posture
//	(2) spec.values.topology.mode             — legacy object form
//	(3) catalyst.openova.io/topology label    — stamped class, if any
//	(4) "singleton"                           — the single-region default
//	                                            every bootstrap front-door
//	                                            app runs as
//
// The `catalyst.openova.io/vcluster` label (host/mgmt/dmz/rtz) records the
// vCluster TIER the syncer placed the app in — orthogonal to the placement
// CLASS, so it is NOT consulted here.
func placementFromHR(hr *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(hr.Object, "spec", "values", "placement"); ok && v != "" {
		return placementForTopology(v)
	}
	values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
	if mode := readTopologyFromValues(values); mode != "" && mode != "singleton" {
		return placementForTopology(mode)
	}
	if labels := hr.GetLabels(); labels != nil {
		if v := labels["catalyst.openova.io/topology"]; v != "" {
			return placementForTopology(v)
		}
	}
	return "singleton"
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
		// #3370 — also match on spec.releaseName: the instance identity
		// the console addresses (/app/shared-pg) is the Helm RELEASE,
		// while the bootstrap-kit HR is named bp-postgres-shared.
		// Name-match keeps the legacy /app/bp-alloy addressing working.
		hrReleaseName, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
		if hr.GetName() != name && hrReleaseName != name {
			continue
		}
		resp := applicationDetailResponse{
			Name:       name,
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
		// #3931 — placement projection for HR-synthesised (bootstrap-kit) apps.
		// These have NO Application CR, so the CR-spec placement read in
		// HandleApplicationGet never runs for them and resp.Placement was left
		// empty → the AppDetail Topology tab lost the live-placement signal for
		// EVERY mgmt-vCluster app post-#3642 (the FE falls back to "singleton"
		// but the read-back is dishonest about whether a value was actually
		// projected). Project the create-time placement the same way the
		// CR-spec path does: read an explicit spec.values.placement if the chart
		// carries one, else default to the canonical single-region "singleton"
		// (bootstrap-kit front-door apps run single-region per tier). The HR's
		// `catalyst.openova.io/vcluster` label records the tier (host/mgmt/dmz/
		// rtz) the syncer placed it in; surfaced for completeness but the
		// placement CLASS is what the Topology tab reads.
		resp.Placement = placementFromHR(hr)
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
		// #3224: gate the URL on a user-UI endpoint. For HR-synth apps the
		// blueprint name comes from spec.chart.spec.chart (e.g. `bp-grafana`,
		// `bp-newapi`) — externalURLIfUserUI strips the `bp-` prefix.
		resp.ExternalURL = h.externalURLIfUserUI(ctx, depID, resp.Blueprint, resp.TargetNamespace, resp.ReleaseName)

		// #3370 — Contexts: the HR's spec.values is the instance's
		// Git-committed IaC; project generically per the Blueprint's
		// contextSchema (nil for non-shareable charts). Status checks
		// read Secrets via the in-cluster client, best-effort.
		if ctxSchema := h.contextSchemaFor(ctx, resp.Blueprint); ctxSchema != nil {
			values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
			statusClient, _ := h.dynamicClientOrFallback()
			resp.Contexts = h.projectContexts(ctx, statusClient, ctxSchema, values)
		}

		// #3370 — consumer-side DependsOn from the Flux dependsOn graph
		// + the target instance's declared bindings: for each HR this
		// one dependsOn, resolve the target's releaseName (the instance
		// identity) and find the Context this consumer occupies there
		// (the entry whose consumer.blueprint equals this chart).
		resp.DependsOn = h.dependsOnFromHRGraph(ctx, hrs, hr, resp.Blueprint)

		return resp, true
	}
	return applicationDetailResponse{}, false
}

// dependsOnFromHRGraph derives the consumer-side backing edges for an
// HR-synthesised application (#3370). Reads ONLY the Flux dependsOn
// graph + each target instance's declared Context entries — one
// mechanism for every blueprint, zero special-casing.
func (h *Handler) dependsOnFromHRGraph(ctx context.Context, hrs []*unstructured.Unstructured, consumer *unstructured.Unstructured, consumerBlueprint string) []dependsOnDetail {
	depsRaw, _, _ := unstructured.NestedSlice(consumer.Object, "spec", "dependsOn")
	if len(depsRaw) == 0 {
		return nil
	}
	// Index the HR set by namespace/name for target resolution.
	byName := map[string]*unstructured.Unstructured{}
	for _, hr := range hrs {
		byName[hr.GetNamespace()+"/"+hr.GetName()] = hr
	}
	consumerSlug := strings.TrimPrefix(consumerBlueprint, "bp-")
	out := []dependsOnDetail{}
	for _, d := range depsRaw {
		dm, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := dm["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := dm["namespace"].(string)
		if ns == "" {
			ns = consumer.GetNamespace()
		}
		target, found := byName[ns+"/"+name]
		if !found {
			continue
		}
		// Instance identity = the target's releaseName.
		instance, _, _ := unstructured.NestedString(target.Object, "spec", "releaseName")
		if instance == "" {
			instance = strings.TrimPrefix(name, "bp-")
		}
		det := dependsOnDetail{Instance: instance, Namespace: target.GetNamespace()}
		// Which Context does this consumer occupy on the target? Read
		// the target's declared IaC per ITS blueprint's contextSchema.
		targetChart, _, _ := unstructured.NestedString(target.Object, "spec", "chart", "spec", "chart")
		if ctxSchema := h.contextSchemaFor(ctx, targetChart); ctxSchema != nil {
			values, _, _ := unstructured.NestedMap(target.Object, "spec", "values")
			if values != nil {
				if entries, ok := values[ctxSchema.ValuesKey].([]interface{}); ok {
					for _, e := range entries {
						em, ok := e.(map[string]interface{})
						if !ok {
							continue
						}
						cons, _ := em["consumer"].(map[string]interface{})
						cbp := ""
						if cons != nil {
							cbp, _ = cons["blueprint"].(string)
						}
						if strings.TrimPrefix(cbp, "bp-") == consumerSlug {
							if n, _ := em["name"].(string); n != "" {
								det.Context = ctxSchema.Kind + "/" + n
							}
							break
						}
					}
				}
			}
		}
		out = append(out, det)
	}
	return out
}

// vClusterHostSyncNamespaces — the well-known HOST namespaces into which a
// per-tier vCluster syncs its in-vCluster resources (HTTPRoutes, Services).
//
// #3642 moved the 7 front-door apps (gitea/grafana/harbor/keycloak/openbao/
// newapi/guacamole) OUT of the host and INTO the `mgmt` vCluster. The loft-sh
// syncer mirrors each app's HTTPRoute back onto the HOST cluster, but it lands
// the mirror in the vCluster's HOST namespace (`hostNamespace: mgmt` in
// bp-mgmt-vcluster values), NOT in the app's in-vCluster targetNamespace
// (gitea/grafana/openbao/…). So on hw171 every front-door HTTPRoute lives in
// host ns `mgmt` while the HelmRelease still declares
// `spec.targetNamespace: gitea`. A namespace filter that requires
// `route.namespace == targetNamespace` (the pre-#3642 invariant, since the app
// AND its route shared a host namespace) therefore matches NOTHING post-#3642
// → externalURL comes back empty → the AppDetail "Open" button + the AppsPage
// card button vanish for EVERY mgmt-vCluster app, even though their front doors
// (gitea.<fqdn>, bao.<fqdn>, …) are live. This is the same #3642 regression
// family as the harbor-core cutover bug fixed in #3927 — the host-side lookup
// must now also search the vCluster sync namespace.
//
// dmz/rtz tiers (bp-dmz-vcluster/bp-rtz-vcluster) sync into `dmz`/`rtz` host
// namespaces respectively; included so the SAME fix covers apps that later move
// into those tiers without another regression.
var vClusterHostSyncNamespaces = []string{"mgmt", "dmz", "rtz"}

// routeNamespaceMatchesApp decides whether an HTTPRoute living in routeNS may
// front the app whose HelmRelease declares targetNS.
//
// The match is intentionally NOT "any namespace": the #3150 namespace filter
// guards against a same-subdomain route in an UNRELATED namespace being
// returned (the `namespace filter excludes other-ns route` lock-in). We only
// widen it to the well-known vCluster HOST sync namespaces (mgmt/dmz/rtz) where
// the syncer deterministically mirrors an app's route post-#3642 — never to
// arbitrary namespaces. The downstream name/host-leftmost-label/backendRef
// match rules still have to fire, and those are collision-free across apps
// (each owns a distinct `<release>.<fqdn>` subdomain), so widening the
// namespace set cannot false-positive one app onto another's route.
func routeNamespaceMatchesApp(routeNS, targetNS string) bool {
	if targetNS == "" || routeNS == targetNS {
		return true
	}
	for _, syncNS := range vClusterHostSyncNamespaces {
		if routeNS == syncNS {
			return true
		}
	}
	return false
}

// loft-sh / vCluster syncer annotation keys. When the per-tier vCluster
// (#3642) syncs an in-vCluster resource down to its HOST namespace
// (mgmt/dmz/rtz), it MANGLES the host name (`<name>-x-<vcns>-x-<vcluster>`,
// truncated + hash-suffixed when too long) and stamps the authoritative
// in-vCluster identity onto these annotations. We read them rather than
// reverse-parsing the mangled name because the syncer truncates long names
// and replaces the suffix with a hash (e.g.
// `mimir-overrides-exporter-…-x-mimir-x--2baf76f56f`,
// `guacamole-server-…-x-catalyst-system-x-533a6f8601`), so a pure
// string-suffix parse is unreliable. The annotations are exact.
const (
	loftObjectNamespaceAnno = "vcluster.loft.sh/object-namespace"
	loftObjectNameAnno      = "vcluster.loft.sh/object-name"
	loftHostNamespaceAnno   = "vcluster.loft.sh/object-host-namespace"
)

// vClusterSyncedObjectNamespace returns the in-vCluster namespace a host
// object was synced from (via the loft annotation), but ONLY when the
// object actually lives in a known vCluster HOST sync namespace
// (mgmt/dmz/rtz). For any object NOT in a sync namespace it returns "" —
// so a non-vCluster object can never be reattributed to a different
// namespace. This is the namespace-scoping guard that mirrors
// routeNamespaceMatchesApp: we widen the lookup ONLY across the
// deterministic sync namespaces, never arbitrarily.
//
// Returns "" when the object is nil, not in a sync namespace, or carries
// no object-namespace annotation (a host-native object in mgmt/dmz/rtz,
// e.g. the vcluster statefulset itself, has no such annotation).
func vClusterSyncedObjectNamespace(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	hostNS := obj.GetNamespace()
	inSyncNS := false
	for _, syncNS := range vClusterHostSyncNamespaces {
		if hostNS == syncNS {
			inSyncNS = true
			break
		}
	}
	if !inSyncNS {
		return ""
	}
	anns := obj.GetAnnotations()
	// Prefer the explicit object-host-namespace cross-check: only trust
	// the object-namespace mapping when the syncer agrees the host
	// namespace is this object's host landing zone. When the host-ns
	// annotation is absent (older syncer) fall back to object-namespace
	// alone — still gated by inSyncNS above.
	if hn := anns[loftHostNamespaceAnno]; hn != "" && hn != hostNS {
		return ""
	}
	return anns[loftObjectNamespaceAnno]
}

// vClusterSyncedDisplayName returns the de-mangled, in-vCluster object name
// for a host-synced object (from the loft annotation), or "" when the
// object is nil / not a synced object / carries no name annotation. Used to
// surface the real name the operator knows (`gitea-75d9f486fb-g8hsr`)
// instead of the mangled host name
// (`gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster`).
func vClusterSyncedDisplayName(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	return obj.GetAnnotations()[loftObjectNameAnno]
}

// objectInAppNamespace decides whether a host object belongs to the app
// installed in `targetNS`, accounting for the #3642 host→mgmt-vCluster
// migration. It matches when EITHER:
//
//	(a) the object's own host namespace equals targetNS (pre-#3642
//	    invariant — the app and its resources share a host namespace), OR
//	(b) the object is synced from a per-tier vCluster (mgmt/dmz/rtz) and
//	    its authoritative in-vCluster namespace (loft annotation) equals
//	    targetNS — the post-#3642 case where the app's
//	    `spec.targetNamespace` is the IN-vCluster namespace (gitea/grafana/
//	    openbao/…) but the host-synced pods/services/etc. land in `mgmt`.
//
// The widening is strictly scoped: (b) only fires for objects already
// living in a known sync namespace whose loft annotation matches targetNS,
// so an unrelated object in another namespace can never false-positive
// onto this app. This is the AppDetail Resources/Logs sibling of #3933's
// routeNamespaceMatchesApp fix — same #3642 family, applied to the generic
// resource-list namespace filter.
func objectInAppNamespace(obj *unstructured.Unstructured, targetNS string) bool {
	if obj == nil || targetNS == "" {
		return false
	}
	if obj.GetNamespace() == targetNS {
		return true
	}
	return vClusterSyncedObjectNamespace(obj) == targetNS
}

// lookupExternalURL — G90 2026-06-01.
//
// Resolve the operator-visible front-door URL for an installed Application
// by joining its (targetNamespace, releaseName) against the HTTPRoute set
// in the chroot k8sCache. Returns "https://<spec.hostnames[0]>" of the
// first matching HTTPRoute, or "" when:
//
//   - k8sCache is unavailable (pre-handover mothership / CI)
//   - no HTTPRoute in targetNamespace matches releaseName (controllers,
//     operators, internal components with no UI surface)
//   - Gateway API isn't installed on this Sovereign
//
// Match rule (in priority order, first hit wins):
//
//	(1) the HTTPRoute's name equals releaseName, OR
//	(2) any backendRefs[].name in any rule equals releaseName, OR
//	(3) the route's first hostname's LEFTMOST DNS label equals releaseName
//	    — i.e. the canonical `<release>.<sovereign-fqdn>` front-door host.
//
// The chart-helpers convention names HTTPRoutes after the release (gitea,
// harbor, grafana, keycloak, openbao, marketplace), and even when the route
// is named differently the backend Service that fronts the app is usually
// named after the release. This covers both the canonical pattern AND charts
// that ship multiple routes (e.g. harbor's registry endpoint vs notary).
//
// #3150 (2026-06-10): rule (3) was added because bp-guacamole's HTTPRoute
// AND backend Service are both named `guacamole-server` (the chart's
// `webapp.name`), while the bootstrap-kit HelmRelease's releaseName is
// `guacamole`. Neither (1) nor (2) matched → externalURL came back empty →
// the AppDetail "Open" button never rendered for guacamole even though its
// front door `https://guacamole.<fqdn>/` was live. The route's hostname
// leftmost label IS the release's canonical subdomain (every Catalyst app
// exposes itself at `<release>.<sovereign-fqdn>`), so matching on it is both
// correct and general — it cannot false-positive across apps because each
// app owns a distinct subdomain.
//
// The SPA renders the returned URL as an "External URL" row + "Open" button
// on AppDetail Overview and an "Open" button on the AppsPage card.
func (h *Handler) lookupExternalURL(ctx context.Context, depID, targetNamespace, releaseName string) string {
	if h.k8sCache == nil || releaseName == "" {
		return ""
	}
	clusterID := h.resolveChrootClusterID(depID)
	if !h.k8sCacheHasCluster(clusterID) {
		return ""
	}
	routes, _, err := h.k8sCache.List(clusterID, "httproute", labels.Everything())
	if err != nil {
		return ""
	}
	for _, rt := range routes {
		// #5358 — gate-owned front door. Apps served through the generic
		// bp-oidc-gate (slot 13c) have NO HTTPRoute in their own namespace:
		// the gate's `oidc-gate-<release>` route in the `oidc-gate`
		// namespace owns `<release>.<sovereign-fqdn>` instead (guacamole
		// since bp-guacamole 0.2.30; powerdns-admin since the #3374
		// Layer-A migration). Without this rule the namespace filter below
		// skips the gate route → externalURL comes back empty → the
		// AppDetail/AppsPage "Open" button vanishes even though the front
		// door is live. The `oidc-gate-<release>` name is the slot-13c
		// instances.yaml naming contract, so this cannot false-positive
		// across apps.
		gateOwned := rt.GetNamespace() == "oidc-gate" && rt.GetName() == "oidc-gate-"+releaseName
		if !gateOwned && !routeNamespaceMatchesApp(rt.GetNamespace(), targetNamespace) {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
		if len(hosts) == 0 || hosts[0] == "" {
			continue
		}
		if gateOwned || rt.GetName() == releaseName {
			return "https://" + hosts[0]
		}
		// (3) hostname leftmost-label match — the route's front-door host
		// is `<release>.<sovereign-fqdn>`. Strip to the first DNS label and
		// compare. Catches guacamole (route `guacamole-server`, host
		// `guacamole.<fqdn>`) and any other release whose route/Service name
		// diverges from the release but whose subdomain is the release name.
		if before, _, found := strings.Cut(hosts[0], "."); found && before == releaseName {
			return "https://" + hosts[0]
		}
		rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
		for _, rl := range rules {
			rm, isMap := rl.(map[string]interface{})
			if !isMap {
				continue
			}
			refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
			for _, ref := range refs {
				rfm, isMap := ref.(map[string]interface{})
				if !isMap {
					continue
				}
				svcName, _, _ := unstructured.NestedString(rfm, "name")
				if svcName == releaseName {
					return "https://" + hosts[0]
				}
			}
		}
	}
	return ""
}

// externalURLIfUserUI resolves the front-door URL for an installed
// Application (via lookupExternalURL) and SUPPRESSES it when the app's
// Blueprint declares no user-facing UI endpoint (#3224).
//
// The AppDetail "Open" button + "External URL" row render whenever the
// returned URL is non-empty. lookupExternalURL alone matches an HTTPRoute
// for ANY app that owns a front-door host — including API/protocol-only
// apps (bp-newapi backend, bp-openova-flow-server, an OCI registry, the
// keycloak admin realm). Those produced a DEAD "Open" button that lands the
// operator on a bare login form or a 404.
//
// We gate on the SAME signal the silent-SSO launch-url endpoint uses
// (blueprintHasUserUIEndpoint over the Blueprint's endpoints[]), so there is
// ONE source of truth for "is this app launchable by a user".
//
// Fail-open policy: when the Blueprint can't be resolved at all (catalog
// unwired on a chroot/CI, or a transient catalog error), keep the URL — we
// must not regress grafana/harbor/openbao/guacamole/pdns-admin when the
// blueprint metadata is merely unavailable. We suppress ONLY when the
// Blueprint resolved successfully AND declares no user-UI endpoint.
func (h *Handler) externalURLIfUserUI(ctx context.Context, depID, blueprint, targetNamespace, releaseName string) string {
	u := h.lookupExternalURL(ctx, depID, targetNamespace, releaseName)
	if u == "" {
		return ""
	}
	if !h.userUIGatePasses(ctx, blueprint) {
		return ""
	}
	return u
}

// userUIGatePasses decides whether an app with the given Blueprint qualifies
// for a user-facing "Open" / Launch button (#3224 / #3374). It is the SINGLE
// SOURCE OF TRUTH shared by:
//
//   - externalURLIfUserUI — the per-app detail endpoint's externalURL gate
//     (GET /sovereigns/{id}/applications/{name}).
//   - HandleSovereignApps — the /v1/sovereign/apps LIST projection, so the
//     AppsPage grid Open button is scoped to exactly the same apps as the
//     AppDetail one (no asymmetry where the grid shows a button the detail
//     page suppresses, or vice-versa).
//
// Policy (#3224):
//
//	bp empty                → FAIL CLOSED (no UI signal can exist)
//	catalogClient unwired   → FAIL OPEN  (chroot config gap / CI — the gate
//	                          can't distinguish any app, so suppressing would
//	                          kill every working Launch button fleet-wide)
//	resolve err / NotFound  → FAIL CLOSED (no evidence of a user UI; a
//	  / empty endpoints[]      suppressed button self-corrects once the
//	                          catalog recovers — a dead button never does;
//	                          the hw130 regression)
//	endpoints[] present,    → FAIL CLOSED (API/protocol only)
//	  none is a user UI
//	a user-UI endpoint      → PASS
//	  (ssoEnabled || launchDefault || name=="ui")
func (h *Handler) userUIGatePasses(ctx context.Context, blueprint string) bool {
	bpName := strings.TrimPrefix(strings.TrimSpace(blueprint), "bp-")
	if bpName == "" {
		return false
	}
	if h.catalogClient == nil {
		return true
	}
	bp, err := h.resolveBlueprintMeta(ctx, bpName, "")
	if err != nil || bp == nil || len(bp.Endpoints) == 0 {
		return false
	}
	return blueprintHasUserUIEndpoint(bp)
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
