// tier.go — slice T3 (developer scope auto-injection — generic,
// annotation-driven) + the scaffolding the C5-followup tier-aware
// emission path consumes.
//
// Generic auto-injection contract (the why):
//
//   Slice T1 (#1142) ships 5 tier ClusterRoles
//   `openova:tier-{viewer,developer,operator,admin,owner}`. Each
//   carries a `catalyst.openova.io/enforced-scopes` annotation —
//   JSON-encoded list of `{key, value}` rows — populated from
//   `.Values.tierActions[<tier>].enforcedScopes` in the chart. Today
//   only the developer tier ships with enforced scopes
//   (`openova.io/env-type=dev`); future tiers can add their own without
//   any controller change. The reconciler MUST be generic — it reads
//   the annotation, never hardcodes a tier-specific scope.
//
//   For each UserAccess CR:
//     1. Resolve tier from the canonical label
//        `catalyst.openova.io/tier=<tier>`. No label = no tier path.
//     2. Look up `openova:tier-<tier>` ClusterRole. NotFound = surface
//        TierResolved=False/TierClusterRoleNotFound and stop.
//     3. Read `catalyst.openova.io/enforced-scopes` annotation. Empty
//        or missing = no auto-injection (still surfaces
//        EnforcedScopeApplied=True with reason AlreadyPresent).
//     4. Compare to spec.scopes[]. Missing rows are appended via a
//        spec-only patch on the CR. Surfaces EnforcedScopeApplied=True
//        reason AutoInjected on the patched pass; AlreadyPresent on
//        subsequent passes.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the scope keys live in the
// canonical openova.io vocabulary; the controller never invents one.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/internal/labels"
)

// Reasons surfaced on the new conditions. Keep stable — UI code reads
// them. Mirrors the rbacAssign* reason vocabulary in catalyst-api so
// the U7 access-matrix UI can render identical strings client-side.
const (
	condEnforcedScopeApplied = "EnforcedScopeApplied"
	condTierResolved         = "TierResolved"
	condBindingsReconciled   = "BindingsReconciled"

	reasonAutoInjected            = "AutoInjected"
	reasonAlreadyPresent          = "AlreadyPresent"
	reasonNoTierLabel             = "NoTierLabel"
	reasonTierClusterRoleNotFound = "TierClusterRoleNotFound"
	reasonTierResolved            = "TierResolved"
	reasonBindingsReconciledOK    = "Reconciled"
	reasonLegacyApplicationsPath  = "LegacyApplicationsPath"
	reasonTierPathPreferred       = "TierPathPreferred"
)

// tierAutoInjectResult captures what the auto-injection pass decided
// without performing the patch. The reconciler decides separately
// whether to emit the patch (it does so iff added != nil).
//
// The split is deliberate: auto-injection is a spec mutation; we want
// to surface the decision via a Condition even when the patch fails or
// is a no-op, and we want to keep the spec patch out of the hot loop
// for the (common) AlreadyPresent path.
type tierAutoInjectResult struct {
	// Tier resolved from the CR label, lowercased+trimmed. Empty when
	// the CR carries no tier label.
	Tier string

	// TierClusterRoleFound reports whether the lookup of
	// `openova:tier-<tier>` succeeded. False = surface
	// TierResolved=False/TierClusterRoleNotFound.
	TierClusterRoleFound bool

	// Enforced is the canonical (sorted, de-duped) scope set the tier
	// ClusterRole pins via the `catalyst.openova.io/enforced-scopes`
	// annotation.
	Enforced []labels.Scope

	// Added is the subset of Enforced that the CR was missing (the new
	// rows the patch will append). Empty when AlreadyPresent.
	Added []labels.Scope

	// CombinedScopes is the merged set the CR will carry after the
	// patch (if any) — useful for the binding-emission step so it
	// doesn't have to re-merge.
	CombinedScopes []labels.Scope
}

// reconcileTierAutoInject implements slice T3 — generic, annotation-
// driven auto-injection of tier-required scopes onto a UserAccess CR.
//
// Returns the auto-inject decision (which the reconciler maps to the
// EnforcedScopeApplied + TierResolved Conditions) and any error from
// the lookup or patch. Errors are returned as-is; the reconciler
// surfaces them on Status without panic.
//
// Idempotent: a CR that already carries the enforced scopes is a no-op
// (no apiserver write); the function still returns Enforced/Added so
// the reconciler can render the AlreadyPresent condition.
func (r *UserAccessReconciler) reconcileTierAutoInject(
	ctx context.Context,
	ua *unstructured.Unstructured,
	spec UserAccessSpec,
) (tierAutoInjectResult, error) {
	res := tierAutoInjectResult{
		Tier:           strings.ToLower(strings.TrimSpace(spec.Tier)),
		CombinedScopes: append([]labels.Scope(nil), spec.Scopes...),
	}
	if res.Tier == "" {
		return res, nil
	}

	// Look up the tier ClusterRole. Use a typed Get because rbacv1 is
	// already in the scheme via SetupWithManager (the controller Owns
	// RoleBinding + ClusterRoleBinding).
	cr := &rbacv1.ClusterRole{}
	name := TierClusterRolePrefix + res.Tier
	if err := r.Client.Get(ctx, types.NamespacedName{Name: name}, cr); err != nil {
		if apierrors.IsNotFound(err) {
			// Tier ClusterRole missing — surface a Condition rather
			// than fail the whole reconcile. T1 should have shipped
			// it; if absent, this CR is on a Sovereign that hasn't
			// rolled the tier ClusterRoles yet.
			return res, nil
		}
		return res, fmt.Errorf("get tier clusterrole %q: %w", name, err)
	}
	res.TierClusterRoleFound = true

	// Read the enforced-scopes annotation. Empty / missing → no
	// auto-injection (the AlreadyPresent path runs since 0 ⊆ any).
	raw := cr.Annotations[AnnotationEnforcedScopes]
	enforced, err := parseEnforcedScopesAnnotation(raw)
	if err != nil {
		return res, fmt.Errorf("parse enforced-scopes annotation on %q: %w", name, err)
	}
	res.Enforced = enforced

	// Compute the missing set.
	have := make(map[string]struct{}, len(spec.Scopes))
	for _, s := range spec.Scopes {
		have[scopeKey(s)] = struct{}{}
	}
	for _, s := range enforced {
		if _, ok := have[scopeKey(s)]; !ok {
			res.Added = append(res.Added, s)
		}
	}

	// Idempotency anchor: if nothing's missing, return without writing.
	if len(res.Added) == 0 {
		return res, nil
	}

	// Compute combined scopes (existing + added).
	res.CombinedScopes = append(res.CombinedScopes, res.Added...)

	// Patch the CR's spec.scopes[] to include the missing rows. We use
	// a JSON merge-patch on the whole array because (a) the CRD's
	// scopes is unconstrained-merge and (b) JSON-patch's append-to-
	// array isn't structurally compatible with the unstructured client
	// without resourceVersion gymnastics. The merge-patch overwrites
	// the array — safe because we're submitting a superset.
	patch, err := buildScopesPatch(res.CombinedScopes)
	if err != nil {
		return res, fmt.Errorf("build scopes patch: %w", err)
	}
	if err := r.Client.Patch(ctx, ua, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return res, fmt.Errorf("patch useraccess scopes: %w", err)
	}
	return res, nil
}

// parseEnforcedScopesAnnotation parses the JSON-encoded list of
// `{key, value}` rows. Tolerant of leading/trailing whitespace and the
// empty-string case (returns no scopes, no error). Anything else that
// doesn't unmarshal returns an error.
func parseEnforcedScopesAnnotation(raw string) ([]labels.Scope, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	out := make([]labels.Scope, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		k := strings.TrimSpace(row["key"])
		v := strings.TrimSpace(row["value"])
		if k == "" && v == "" {
			continue
		}
		key := scopeKey(labels.Scope{Key: k, Value: v})
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, labels.Scope{Key: k, Value: v})
	}
	// Stable order — by key then value — for deterministic patches.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

// buildScopesPatch returns the JSON merge-patch body for a spec.scopes
// rewrite. Uses the post-A1 `{key, value}` shape since A1's CRD
// extension is what made this controller path useful in the first
// place; legacy CRs that arrived with `{labelKey, labelValue}` are
// migrated to the new shape on the patch (idempotent — the new shape
// is what the apiserver will accept).
func buildScopesPatch(combined []labels.Scope) ([]byte, error) {
	rows := make([]map[string]string, 0, len(combined))
	for _, s := range combined {
		rows = append(rows, map[string]string{
			"key":   s.Key,
			"value": s.Value,
		})
	}
	body := map[string]any{
		"spec": map[string]any{
			"scopes": rows,
		},
	}
	return json.Marshal(body)
}

// scopeKey returns the canonical fingerprint of a scope row used for
// set-equality comparisons.
func scopeKey(s labels.Scope) string {
	return s.Key + "=" + s.Value
}

// autoInjectReason maps a tierAutoInjectResult to the Condition reason
// the reconciler posts on EnforcedScopeApplied.
func autoInjectReason(res tierAutoInjectResult) (status bool, reason, message string) {
	if res.Tier == "" {
		return false, reasonNoTierLabel, "CR carries no catalyst.openova.io/tier label"
	}
	if !res.TierClusterRoleFound {
		return false, reasonTierClusterRoleNotFound,
			"tier ClusterRole openova:tier-" + res.Tier + " not found on this cluster"
	}
	if len(res.Added) > 0 {
		return true, reasonAutoInjected,
			fmt.Sprintf("auto-injected %d enforced scope(s) for tier %q", len(res.Added), res.Tier)
	}
	return true, reasonAlreadyPresent,
		fmt.Sprintf("all enforced scopes for tier %q already present", res.Tier)
}
