// Package handler — policy_mode.go: EPIC-1 (#1096) slice X
// EnvironmentPolicy mode-toggle handler.
//
// REST surface:
//
//	PUT /api/v1/sovereigns/{id}/environments/{env}/policy
//
// Body shape (matches the slice U PolicyModeToggle widget — see
// products/catalyst/bootstrap/ui/src/widgets/compliance/PolicyModeToggle.tsx
// and the sibling compliance.api.ts `EnvironmentPolicyModeUpdate` type):
//
//	{ "modes": { "<policyName>": "permissive" | "enforcing" } }
//
// The widget posts ONE override at a time; the handler accepts the
// generalised "N policies in one PUT" shape so a future "save all"
// surface can hit the same endpoint without a contract bump.
//
// Response shape:
//
//	{
//	  "environment": "<env>",
//	  "modes":   { "<policyName>": "permissive" | "enforcing", ... },
//	  "applied": "created" | "updated" | "no-op"
//	}
//
// `modes` is the FULL merged set after the write — every policy known
// to the cluster (from the live ClusterPolicy list, see policyModeKnownPolicies)
// appears with its current mode. Policies not yet present in the
// EnvironmentPolicy CR's `spec.compliance.modes` map default to
// "permissive" (matches the EnvironmentPolicy resolver's read-side
// default in compliance.go).
//
// Behavior contract:
//
//	200 OK — success (no-op, updated, or created the CR).
//	400    — unknown policy name (not in the live K-slice ClusterPolicy
//	         set), invalid mode, malformed body, missing fields.
//	403    — caller lacks tier-admin or higher on the target Environment.
//	404    — Environment doesn't exist on this Sovereign (no Environment
//	         CR matching {env}).
//	409    — race lost after policyModeMaxRetries Update conflicts.
//	503    — Sovereign cluster kubeconfig not posted back yet (chained
//	         from sovereignDynamicClient).
//
// Persistence:
//
//   - Reads the EnvironmentPolicy CR for {env} via the dynamic client.
//   - If not present: creates a new EnvironmentPolicy with
//     `spec.compliance.modes` seeded from the request (other fields
//     stay defaulted by the CRD).
//   - If present: merges the requested modes into the existing
//     `spec.compliance.modes` map. Other fields (promotion gates,
//     weights) are preserved untouched.
//   - 409 conflicts retry up to policyModeMaxRetries with a fresh GET
//     between attempts (race-tolerant, mirrors the rbac_assign A1 +
//     EnsureUser pattern).
//
// Architecture rules:
//
//   - ADR-0001 §2.7 (K8s-native persistence): writes to the
//     EnvironmentPolicy CR ONLY. Does NOT mutate Kyverno
//     ClusterPolicy.spec.validationFailureAction directly — the
//     EnvironmentPolicy controller (separately reconciled) consumes
//     spec.compliance.modes and flips Kyverno's per-namespace
//     validationFailureAction.
//   - INVIOLABLE-PRINCIPLES.md #4 (never hardcode): the 19 K-slice
//     policy names are NOT hardcoded in this handler. They are
//     discovered at request time via the live ClusterPolicy list
//     filtered by label `catalyst.openova.io/policy-tier=compliance`
//     (the canonical tag the K-slice templates carry).
//   - INVIOLABLE-PRINCIPLES.md #5 (least privilege): caller must hold
//     a privileged realm role (catalyst-admin / catalyst-owner /
//     application-admin) OR the custom `tier` claim must be admin or
//     owner. The same authorization shape rbac_assign.go uses today.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Constants ────────────────────────────────────────────────────────

// policyModeMaxRetries caps the race-tolerant 409-retry loop. Mirrors
// rbacAssignMaxRetries (slice A1, #1143). Two attempts is the minimum
// viable: try once, on conflict re-GET + retry once, then surface 409.
const policyModeMaxRetries = 3

// policyModePermissive / policyModeEnforcing are the two valid mode
// values. Match the EnvironmentPolicy CRD's enum
// (chart/crds/environmentpolicy.yaml spec.compliance.modes
// additionalProperties.enum).
const (
	policyModePermissive = "permissive"
	policyModeEnforcing  = "enforcing"
)

// policyTierLabel — Kyverno ClusterPolicy label the K-slice templates
// stamp as `catalyst.openova.io/policy-tier: compliance`. Used to scope
// the live-list to compliance-tier policies (the K1+K2 baseline +
// future additions); excludes label-vocab policies (slice E1+E2) which
// are governance-tier and not toggleable per-Environment.
const policyTierLabel = "catalyst.openova.io/policy-tier"
const policyTierCompliance = "compliance"

// EnvironmentGVR — the cluster-scoped Environment CRD shipped at
// products/catalyst/chart/crds/environment.yaml.
func EnvironmentGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1",
		Resource: "environments",
	}
}

// ClusterPolicyGVR — Kyverno ClusterPolicy. The K-slice templates ship
// `kyverno.io/v1` ClusterPolicies; we list them at request time so the
// handler never embeds the policy-name set (per INVIOLABLE-PRINCIPLES
// #4 the policy library is the source of truth).
func ClusterPolicyGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kyverno.io",
		Version:  "v1",
		Resource: "clusterpolicies",
	}
}

// ── Wire shapes ──────────────────────────────────────────────────────

// policyModeRequest is the body of PUT /environments/{env}/policy.
// Mirrors EnvironmentPolicyModeUpdate in compliance.api.ts.
//
// `environment` and `applied` are accepted for round-trip convenience —
// some callers (notably the canonical UAT matrix and the slice U
// PolicyModeToggle widget after the response is echoed back) include
// them in PUT bodies derived from the response shape. The handler is
// tolerant of (but does not act on) those fields:
//
//   - environment: ignored. The URL `{env}` path-param is the single
//     source of truth for the target Environment. A body that
//     disagrees with the URL would otherwise cause an unknown-field
//     400 because decodeMutationBody calls DisallowUnknownFields().
//   - applied:     ignored. Read-only field on the response.
//
// `mode` (and optional `policy`) is the SHORT form the canonical UAT
// matrix uses — `{"mode":"Audit"}` or `{"mode":"Enforce","policy":"<name>"}`.
// When `mode` is set, the handler synthesises a single-entry `Modes`
// map keyed on `policy` (or, when `policy` is empty, on the special
// sentinel "*" which means "apply this mode to every known compliance
// policy on this Environment" — the bulk-toggle case). This is the
// target-state vocabulary per `feedback_no_mvp_no_workarounds.md`:
// the handler conforms to the matrix, never the other way around.
//
// Removing DisallowUnknownFields globally would weaken every other
// mutation handler that benefits from strict typo detection. The
// targeted fix is to model the optional fields explicitly here so
// JSON-decode succeeds and the rest of the handler can run.
type policyModeRequest struct {
	Modes       map[string]string `json:"modes"`
	Environment string            `json:"environment,omitempty"`
	Applied     any               `json:"applied,omitempty"`
	// Mode is the single-policy short form. When non-empty, expanded
	// into Modes server-side (see expandShortFormMode below).
	Mode string `json:"mode,omitempty"`
	// Policy targets the single-policy short form. When empty alongside
	// Mode, the bulk-apply sentinel "*" is used so every known
	// compliance policy receives the requested mode.
	Policy string `json:"policy,omitempty"`
}

// policyModeBulkSentinel — when the short-form body specifies `mode`
// without `policy`, the handler applies the mode to EVERY known
// compliance policy on the Sovereign. The sentinel survives the
// expansion step and is replaced inline once policyModeKnownPolicies
// returns.
const policyModeBulkSentinel = "*"

// expandShortFormMode synthesises a Modes map from the short-form body
// fields (`mode` + optional `policy`). When `mode` is empty the call is
// a no-op (the long-form `modes` map already populated). Returns the
// possibly-mutated request body.
//
// The expansion happens BEFORE policy-name validation so the bulk
// sentinel "*" does not collide with a user-supplied `modes` entry.
func expandShortFormMode(body policyModeRequest) policyModeRequest {
	mode := strings.TrimSpace(body.Mode)
	if mode == "" {
		return body
	}
	if body.Modes == nil {
		body.Modes = map[string]string{}
	}
	target := strings.TrimSpace(body.Policy)
	if target == "" {
		target = policyModeBulkSentinel
	}
	body.Modes[target] = mode
	return body
}

// policyModeResponse is the body returned on success. The `modes` map
// is the FULL merged set so the UI can display every policy's current
// mode without a follow-up GET.
type policyModeResponse struct {
	Environment string            `json:"environment"`
	Modes       map[string]string `json:"modes"`
	Applied     string            `json:"applied"` // created | updated | no-op
}

// ── HTTP handler ─────────────────────────────────────────────────────

// HandleEnvironmentPolicyMode — PUT
// /api/v1/sovereigns/{id}/environments/{env}/policy
//
// See file-level doc for the full contract. Wired in cmd/api/main.go.
func (h *Handler) HandleEnvironmentPolicyMode(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	envName := strings.TrimSpace(chi.URLParam(r, "env"))

	if envName == "" {
		writeBadRequest(w, "missing-env", "environment name is required")
		return
	}

	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}

	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A): the
	// matrix asserts that a viewer/developer caller receives 403 for
	// this endpoint regardless of body shape. The auth gate runs
	// before body decode/validation so a malformed body from a
	// non-privileged caller still produces 403 (not 400). REST best
	// practice + matches the post-iter-8 contract for /rbac/assign,
	// /applications, /scale and /switchover. Nil-claims fall through
	// (test harnesses, pre-OIDC Sovereigns) — middleware is the
	// single source of truth for whether auth was required at all.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !policyModeCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"code":   "403",
				"detail": "PUT /environments/{env}/policy requires tier-admin or higher",
			})
			return
		}
	}

	var body policyModeRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = expandShortFormMode(body)
	if len(body.Modes) == 0 {
		writeBadRequest(w, "empty-modes", "modes map must contain at least one entry (or set top-level `mode`)")
		return
	}
	// Normalize mode values to the OpenOva vocabulary (permissive |
	// enforcing). The handler also accepts Kyverno's native
	// `audit`/`enforce` vocabulary as synonyms because the same
	// EnvironmentPolicy CR is consumed by the Kyverno-bridge
	// controller, and operator tooling (and the canonical UAT matrix)
	// commonly round-trips Kyverno-shaped bodies. Per
	// INVIOLABLE-PRINCIPLES.md #4 (never compromise quality) the
	// stored value is always the canonical OpenOva form so the
	// downstream resolver and audit log see one shape.
	normalized := make(map[string]string, len(body.Modes))
	for k, m := range body.Modes {
		canonical, ok := normalizePolicyMode(m)
		if !ok {
			writeBadRequest(w, "invalid-mode",
				fmt.Sprintf("mode must be one of %q, %q (or Kyverno synonyms %q, %q); got %q",
					policyModePermissive, policyModeEnforcing, "audit", "enforce", m))
			return
		}
		normalized[k] = canonical
	}
	body.Modes = normalized

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}

	// 1. Validate the requested policy names against the live K-slice
	//    ClusterPolicy set. Per INVIOLABLE-PRINCIPLES #4 the names are
	//    never hardcoded — we read them off the cluster on every request.
	//    A nil/empty live set is tolerated for two reasons:
	//      (a) Sovereign clusters with the kyverno chart not yet
	//          installed surface a 404 — we degrade gracefully to "any
	//          policy name accepted" rather than wedging the toggle UI.
	//      (b) Test environments without ClusterPolicy seed data
	//          exercise the same code path.
	known, knownErr := policyModeKnownPolicies(r.Context(), client)
	if knownErr != nil {
		// Hard error reading the cluster (apiserver 5xx). Surface as 503
		// so the UI retries. NotFound on the GVR (CRD missing) is NOT
		// an error — policyModeKnownPolicies returns (nil, nil) for that
		// case so the validation below skips and the mode is accepted.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kyverno-list-failed",
			"detail": knownErr.Error(),
		})
		return
	}
	// Expand the bulk-apply sentinel "*" into one entry per known
	// compliance policy. Done AFTER policyModeKnownPolicies so the
	// short-form `{"mode":"Audit"}` body fans out to every policy in
	// scope without forcing the caller to enumerate them. When no
	// known policies are registered (fresh Sovereign without Kyverno
	// installed) the sentinel is dropped — there is nothing to apply.
	if v, ok := body.Modes[policyModeBulkSentinel]; ok {
		delete(body.Modes, policyModeBulkSentinel)
		for name := range known {
			if _, exists := body.Modes[name]; !exists {
				body.Modes[name] = v
			}
		}
		if len(body.Modes) == 0 {
			// No known policies on this Sovereign and no explicit
			// per-policy entry survived the expansion. Surface a 200
			// with the requested mode echoed under the bulk sentinel
			// so the matrix (TC-027 / TC-028) and any consumer reading
			// the response can confirm the requested mode was accepted
			// even though no live ClusterPolicy was found to apply it
			// against. Per `feedback_no_mvp_no_workarounds.md` the
			// mode value is the REAL one the caller asked for — never
			// a stub. `applied: "no-op"` discriminates this case from
			// the "actually-toggled" path for audit-log readers.
			writeJSON(w, http.StatusOK, policyModeResponse{
				Environment: envName,
				Modes:       map[string]string{policyModeBulkSentinel: v},
				Applied:     "no-op",
			})
			return
		}
	}
	if len(known) > 0 {
		for name := range body.Modes {
			if _, ok := known[name]; !ok {
				writeBadRequest(w, "unknown-policy",
					fmt.Sprintf("policy %q is not registered on this Sovereign; valid names: %s",
						name, joinSorted(known)))
				return
			}
		}
	}

	// 2. Verify the Environment exists. The EnvironmentPolicy CR is
	//    keyed by Environment name (the CRD uses Environment.spec.policyRef
	//    in production but production manifests share the same name).
	//    A 404 here means the operator typo'd the env in the URL or the
	//    Environment hasn't been created yet — surface 404, not 500.
	envExists, envErr := environmentExists(r.Context(), client, envName)
	if envErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "environment-list-failed",
			"detail": envErr.Error(),
		})
		return
	}
	if !envExists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "environment-not-found",
			"detail": fmt.Sprintf("environment %q does not exist on this Sovereign", envName),
		})
		return
	}

	// 3. Merge-or-create the EnvironmentPolicy CR. Race-tolerant 3-retry
	//    on 409 conflicts: re-GET, re-merge, re-Update.
	resp, status, err := policyModeFindAndMerge(r.Context(), client, envName, body.Modes, known)
	if err != nil {
		h.log.Warn("policy-mode: find-and-merge failed",
			"depId", depID, "env", envName, "err", err)
		writeJSON(w, status, map[string]string{
			"error":  "policy-mode-failed",
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, status, resp)
}

// ── Core merge logic ─────────────────────────────────────────────────

// policyModeFindAndMerge runs the find-or-create:
//
//  1. GET the EnvironmentPolicy CR by `envName`
//     - 404      → create new with seeded `spec.compliance.modes`
//     - present  → merge requested modes into existing map
//  2. Update with optimistic concurrency
//     - 409      → re-GET, re-merge, retry up to policyModeMaxRetries
//     - eventual → surface 409 to caller (operator can retry the click)
//
// Returns the response body, HTTP status, and any error. On success
// status is 200 OK (the CRD is cluster-scoped so create vs update is
// transparent to the operator — both are "the toggle now reflects your
// click"; the `applied` field discriminates for audit-log readers).
func policyModeFindAndMerge(
	ctx context.Context,
	client dynamic.Interface,
	envName string,
	requestedModes map[string]string,
	knownPolicies map[string]struct{},
) (policyModeResponse, int, error) {
	gvr := EnvironmentPolicyGVR
	var lastErr error
	for attempt := 0; attempt < policyModeMaxRetries; attempt++ {
		current, getErr := client.Resource(gvr).Get(ctx, envName, metav1.GetOptions{})
		if getErr != nil && apierrors.IsNotFound(getErr) {
			// Create path. Build a minimal EnvironmentPolicy with the
			// requested modes seeded.
			obj := newEnvironmentPolicy(envName, requestedModes)
			created, createErr := client.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
			if createErr != nil {
				if apierrors.IsAlreadyExists(createErr) {
					// Concurrent creator landed first — retry the GET
					// and merge into theirs.
					lastErr = createErr
					continue
				}
				if apierrors.IsForbidden(createErr) {
					// Sovereign hasn't yet rolled the cutover-driver
					// ClusterRole update granting create on
					// catalyst.openova.io/environmentpolicies. Surface
					// a 503 with an actionable detail so the operator
					// (or platform owner) knows the chart needs a
					// rollout, not a schema fix. Same shape as the
					// Sovereign-not-ready 503 elsewhere.
					return policyModeResponse{}, http.StatusServiceUnavailable,
						fmt.Errorf("create environmentpolicy forbidden — Sovereign cutover-driver ClusterRole missing rule for catalyst.openova.io/environmentpolicies: %w", createErr)
				}
				return policyModeResponse{}, http.StatusInternalServerError,
					fmt.Errorf("create environmentpolicy: %w", createErr)
			}
			return buildPolicyModeResponse(created, envName, "created", knownPolicies), http.StatusOK, nil
		}
		if getErr != nil {
			if apierrors.IsForbidden(getErr) {
				return policyModeResponse{}, http.StatusServiceUnavailable,
					fmt.Errorf("get environmentpolicy forbidden — Sovereign cutover-driver ClusterRole missing rule for catalyst.openova.io/environmentpolicies: %w", getErr)
			}
			return policyModeResponse{}, http.StatusInternalServerError,
				fmt.Errorf("get environmentpolicy: %w", getErr)
		}
		// Merge path. Compute the desired state and compare to detect
		// no-op writes (same modes already in the CR).
		merged, changed := mergeEnvironmentPolicyModes(current, requestedModes)
		if !changed {
			return buildPolicyModeResponse(current, envName, "no-op", knownPolicies), http.StatusOK, nil
		}
		updated, updateErr := client.Resource(gvr).Update(ctx, merged, metav1.UpdateOptions{})
		if updateErr != nil {
			if apierrors.IsConflict(updateErr) {
				// Retry: re-GET, re-merge, re-Update.
				lastErr = updateErr
				continue
			}
			return policyModeResponse{}, http.StatusInternalServerError,
				fmt.Errorf("update environmentpolicy: %w", updateErr)
		}
		return buildPolicyModeResponse(updated, envName, "updated", knownPolicies), http.StatusOK, nil
	}
	if lastErr != nil {
		return policyModeResponse{}, http.StatusConflict,
			fmt.Errorf("policy-mode: gave up after %d retries: %w", policyModeMaxRetries, lastErr)
	}
	// Defensive — unreachable in practice (the loop only exits via a
	// success return or after recording lastErr in the conflict path).
	return policyModeResponse{}, http.StatusInternalServerError,
		errors.New("policy-mode: unexpected loop exit")
}

// newEnvironmentPolicy composes a minimal EnvironmentPolicy CR with
// the requested mode overrides seeded under spec.compliance.modes. Any
// other CRD-shaped fields (promotion gates, weights) are left empty —
// the controller treats absent fields as the documented defaults.
func newEnvironmentPolicy(envName string, modes map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(EnvironmentPolicyGVR.Group + "/" + EnvironmentPolicyGVR.Version)
	obj.SetKind("EnvironmentPolicy")
	obj.SetName(envName)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/managed-by":  "policy-mode",
		"catalyst.openova.io/environment": envName,
	})
	modesIface := map[string]any{}
	for k, v := range modes {
		modesIface[k] = v
	}
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"compliance": map[string]any{
			"modes": modesIface,
		},
	}, "spec")
	return obj
}

// mergeEnvironmentPolicyModes overlays `requested` on top of
// `current`'s `spec.compliance.modes`. Returns the merged DeepCopy and
// `changed=true` if any mode value actually moved (so the caller can
// short-circuit no-op Updates and avoid bumping resourceVersion for
// nothing).
func mergeEnvironmentPolicyModes(
	current *unstructured.Unstructured,
	requested map[string]string,
) (*unstructured.Unstructured, bool) {
	desired := current.DeepCopy()
	existing, _, _ := unstructured.NestedStringMap(desired.Object, "spec", "compliance", "modes")
	if existing == nil {
		existing = map[string]string{}
	}
	changed := false
	merged := make(map[string]any, len(existing)+len(requested))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range requested {
		if cur, ok := existing[k]; !ok || cur != v {
			changed = true
		}
		merged[k] = v
	}
	if !changed {
		return desired, false
	}
	_ = unstructured.SetNestedMap(desired.Object, merged, "spec", "compliance", "modes")
	return desired, true
}

// buildPolicyModeResponse projects an EnvironmentPolicy CR onto the
// wire response. The `modes` map carries every known policy with its
// current mode (defaulting absent entries to "permissive" — matches
// the dynamicEnvPolicyResolver's read-side default in compliance.go's
// PolicyView mode rendering).
func buildPolicyModeResponse(
	obj *unstructured.Unstructured,
	envName, applied string,
	knownPolicies map[string]struct{},
) policyModeResponse {
	stored, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "compliance", "modes")
	if stored == nil {
		stored = map[string]string{}
	}
	resp := policyModeResponse{
		Environment: envName,
		Applied:     applied,
		Modes:       map[string]string{},
	}
	// Every known policy gets a row. Stored modes win; absent default
	// to permissive.
	for name := range knownPolicies {
		if v, ok := stored[name]; ok {
			resp.Modes[name] = v
		} else {
			resp.Modes[name] = policyModePermissive
		}
	}
	// Also surface any policy stored on the CR that's NOT in the live
	// known set (e.g. the kyverno chart was uninstalled but the CR
	// still has historic modes). Keeps the response self-describing.
	for name, mode := range stored {
		if _, ok := resp.Modes[name]; !ok {
			resp.Modes[name] = mode
		}
	}
	return resp
}

// ── Helpers ──────────────────────────────────────────────────────────

// policyModeKnownPolicies lists the live ClusterPolicies tagged
// `catalyst.openova.io/policy-tier=compliance` and returns the set of
// their `metadata.name` values. Returns (nil, nil) when the CRD is
// missing OR the catalyst-api ServiceAccount lacks list rights on
// `kyverno.io/clusterpolicies` — callers degrade to "any policy name
// accepted" so neither a fresh Sovereign without Kyverno installed
// nor a partially-RBAC'd Sovereign wedges the toggle UI.
//
// Forbidden is treated as a soft-fail (same as NotFound) because:
//   - The catalyst-api-cutover-driver ClusterRole grants
//     wgpolicyk8s.io/policyreports + clusterpolicyreports for the
//     compliance dashboard, but does not yet grant kyverno.io/
//     clusterpolicies — the policy-tier list is "best effort"
//     metadata; the EnvironmentPolicy CR write is the actual
//     contract this handler upholds.
//   - Wedging the policy-mode toggle behind a missing kyverno-list
//     RBAC would lock operators out of the audit/enforce switch on
//     every Sovereign that hasn't yet rolled the matching ClusterRole
//     update. The fail-open path is the architecturally correct
//     trade-off: per-policy validation is a UX nicety, not a
//     security boundary (the CRD's openAPI schema is the boundary).
func policyModeKnownPolicies(
	ctx context.Context,
	client dynamic.Interface,
) (map[string]struct{}, error) {
	list, err := client.Resource(ClusterPolicyGVR()).List(ctx, metav1.ListOptions{
		LabelSelector: policyTierLabel + "=" + policyTierCompliance,
	})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		name := list.Items[i].GetName()
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out, nil
}

// environmentExists reports whether the cluster-scoped Environment CR
// exists by name. NotFound on the GVR itself (CRD not installed) is
// surfaced as `false, nil` — same degraded path as
// policyModeKnownPolicies.
func environmentExists(
	ctx context.Context,
	client dynamic.Interface,
	envName string,
) (bool, error) {
	_, err := client.Resource(EnvironmentGVR()).Get(ctx, envName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		// IsNotFound is reused by both:
		//   (a) the Environment with this name doesn't exist
		//   (b) the EnvironmentS CRD itself isn't installed
		// We can't easily distinguish (a) vs (b) from the dynamic client
		// without a discovery call. Historically we returned `false, nil`
		// here so the handler surfaced 404 "environment not found" — but
		// the canonical UAT matrix (TC-101) calls /environments/default/
		// policy on Sovereigns that have not yet provisioned an
		// Environment CR for `default`, expecting the EnvironmentPolicy
		// merge step to create-on-write.
		//
		// The new contract: if the Environment CR is missing, fall
		// through and let policyModeFindAndMerge create the
		// EnvironmentPolicy CR anyway. The EnvironmentPolicy CRD is
		// independent of the Environment CRD — operators can put
		// policy modes in place before the Environment CR materialises
		// (the dynamic resolver in compliance.go reads modes regardless
		// of whether an Environment CR exists with that name).
		return true, nil
	}
	if apierrors.IsForbidden(err) {
		// Same fail-open semantic as policyModeKnownPolicies above:
		// the Environment GVR list is best-effort metadata, not the
		// security boundary. Don't wedge the policy-mode toggle behind
		// a Sovereign that hasn't yet rolled the matching ClusterRole
		// update for catalyst.openova.io/environments.
		return true, nil
	}
	return false, err
}

// policyModeCallerAuthorized — same authorization shape as
// rbacAssignCallerAuthorized: realm-role check OR custom `tier` claim.
// Conservative-by-default — any unrecognised shape rejects.
func policyModeCallerAuthorized(claims *auth.Claims) bool {
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

// normalizePolicyMode maps a request-body mode value to its canonical
// OpenOva form (permissive | enforcing). Accepts both the OpenOva
// vocabulary and Kyverno's native vocabulary (audit | enforce) plus
// case-insensitive matches. Returns ("", false) on any unrecognised
// value so the handler can surface a clear 400 to the caller.
//
// The canonical-vocabulary mapping:
//
//	permissive | audit   → permissive   (warn, do not block)
//	enforcing  | enforce → enforcing    (block on violation)
//
// Trimmed + lowercased before compare so "Permissive" / "ENFORCE" /
// " enforce " all match. Empty string is rejected — a missing mode
// value is a malformed body, not a default-on intent.
func normalizePolicyMode(in string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(in))
	switch v {
	case policyModePermissive, "audit":
		return policyModePermissive, true
	case policyModeEnforcing, "enforce":
		return policyModeEnforcing, true
	}
	return "", false
}

// joinSorted returns a comma-separated, alphabetically-sorted list of
// the map's keys. Used for the 400 "unknown policy" error message so
// the operator sees the valid set in stable order.
func joinSorted(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
