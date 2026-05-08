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
type policyModeRequest struct {
	Modes map[string]string `json:"modes"`
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

	var body policyModeRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if len(body.Modes) == 0 {
		writeBadRequest(w, "empty-modes", "modes map must contain at least one entry")
		return
	}
	for _, m := range body.Modes {
		if m != policyModePermissive && m != policyModeEnforcing {
			writeBadRequest(w, "invalid-mode",
				fmt.Sprintf("mode must be %q or %q; got %q",
					policyModePermissive, policyModeEnforcing, m))
			return
		}
	}

	// Authorization: caller must hold tier-admin or higher. Nil-claims
	// (test harnesses without a wired Keycloak; Sovereign clusters
	// pre-OIDC) fall through — the auth middleware is the single
	// source of truth for whether auth was required at all. Mirrors
	// rbac_assign.go's authz seam.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !policyModeCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "PUT /environments/{env}/policy requires tier-admin or higher",
			})
			return
		}
	}

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
				return policyModeResponse{}, http.StatusInternalServerError,
					fmt.Errorf("create environmentpolicy: %w", createErr)
			}
			return buildPolicyModeResponse(created, envName, "created", knownPolicies), http.StatusOK, nil
		}
		if getErr != nil {
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
// missing — callers degrade to "any policy name accepted" so a fresh
// Sovereign without Kyverno installed doesn't wedge the toggle UI.
func policyModeKnownPolicies(
	ctx context.Context,
	client dynamic.Interface,
) (map[string]struct{}, error) {
	list, err := client.Resource(ClusterPolicyGVR()).List(ctx, metav1.ListOptions{
		LabelSelector: policyTierLabel + "=" + policyTierCompliance,
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
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
		// without a discovery call; the conservative choice is to treat
		// "not found" as "environment not found" → 404. That's the
		// operationally correct path: if the Environment CRD isn't
		// installed, neither is the EnvironmentPolicy CRD that this
		// handler ultimately writes to, and the merge step would
		// surface its own 404. The widget shows a clear error and the
		// operator knows to install the chart.
		return false, nil
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
