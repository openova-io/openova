package controller

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/internal/labels"
)

// mkTierClusterRole returns a typed ClusterRole carrying the
// `catalyst.openova.io/enforced-scopes` annotation. The tier name is
// substituted into the standard openova:tier-<name> form.
func mkTierClusterRole(name, enforcedScopesJSON string) *rbacv1.ClusterRole {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	if enforcedScopesJSON != "" {
		cr.Annotations = map[string]string{
			AnnotationEnforcedScopes: enforcedScopesJSON,
		}
	}
	return cr
}

// addTierLabel sets the canonical `catalyst.openova.io/tier=<v>` label
// on the unstructured CR.
func addTierLabel(u *unstructured.Unstructured, tier string) {
	l := u.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l[LabelTier] = tier
	u.SetLabels(l)
}

// ── T3 — developer-tier missing env-type=dev → scope auto-injected ──
func TestTierAutoInject_DeveloperGetsEnvTypeDev(t *testing.T) {
	ua := newUA("dev-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "dev-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"acme-dev"}},
		},
	})
	addTierLabel(ua, "developer")

	cr := mkTierClusterRole(
		"openova:tier-developer",
		`[{"key":"openova.io/env-type","value":"dev"}]`,
	)
	r, c := newReconciler(t, ua, cr)
	runReconcile(t, r, "dev-grant")

	// Re-read the CR — the patch should have appended the enforced scope.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dev-grant"}, got); err != nil {
		t.Fatal(err)
	}
	scopesRaw, ok, _ := unstructured.NestedSlice(got.Object, "spec", "scopes")
	if !ok || len(scopesRaw) != 1 {
		t.Fatalf("expected 1 scope row after auto-inject, got %v (ok=%v)", scopesRaw, ok)
	}
	row := scopesRaw[0].(map[string]any)
	if row["key"] != "openova.io/env-type" || row["value"] != "dev" {
		t.Fatalf("auto-inject wrote wrong scope: %+v", row)
	}
	// Condition should fire EnforcedScopeApplied=True / AutoInjected.
	if !hasCondition(got, condEnforcedScopeApplied, "True", reasonAutoInjected) {
		t.Fatalf("expected EnforcedScopeApplied=True/AutoInjected, status=%v", got.Object["status"])
	}
}

// ── T3 — developer-tier already has env-type=dev → no patch, AlreadyPresent
func TestTierAutoInject_DeveloperWithEnvTypeDevIsNoOp(t *testing.T) {
	ua := newUA("dev-grant-already", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "dev-sub-2"},
		"sovereignRef": "omantel",
		"scopes":       []any{map[string]any{"key": "openova.io/env-type", "value": "dev"}},
		"tierRoleRef":  "openova:tier-developer",
	})
	addTierLabel(ua, "developer")

	cr := mkTierClusterRole(
		"openova:tier-developer",
		`[{"key":"openova.io/env-type","value":"dev"}]`,
	)
	r, c := newReconciler(t, ua, cr)
	runReconcile(t, r, "dev-grant-already")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dev-grant-already"}, got); err != nil {
		t.Fatal(err)
	}
	scopesRaw, ok, _ := unstructured.NestedSlice(got.Object, "spec", "scopes")
	if !ok || len(scopesRaw) != 1 {
		t.Fatalf("expected 1 scope row (unchanged), got %d", len(scopesRaw))
	}
	if !hasCondition(got, condEnforcedScopeApplied, "True", reasonAlreadyPresent) {
		t.Fatalf("expected EnforcedScopeApplied=True/AlreadyPresent, status=%v", got.Object["status"])
	}
}

// ── T3 — tier label missing → no auto-inject Condition ──────────────
func TestTierAutoInject_NoTierLabelIsNoOp(t *testing.T) {
	ua := newUA("no-tier", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "no-tier-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"acme-dev"}},
		},
	})
	cr := mkTierClusterRole(
		"openova:tier-developer",
		`[{"key":"openova.io/env-type","value":"dev"}]`,
	)
	r, c := newReconciler(t, ua, cr)
	runReconcile(t, r, "no-tier")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "no-tier"}, got); err != nil {
		t.Fatal(err)
	}
	// EnforcedScopeApplied with NoTierLabel reason.
	if !hasCondition(got, condEnforcedScopeApplied, "False", reasonNoTierLabel) {
		t.Fatalf("expected EnforcedScopeApplied=False/NoTierLabel, status=%v", got.Object["status"])
	}
	// scopes should be unchanged (none in spec).
	if scopesRaw, ok, _ := unstructured.NestedSlice(got.Object, "spec", "scopes"); ok && len(scopesRaw) > 0 {
		t.Fatalf("spec.scopes should remain empty when no tier; got %v", scopesRaw)
	}
}

// ── T3 — tier ClusterRole missing → False / TierClusterRoleNotFound ──
func TestTierAutoInject_TierClusterRoleNotFound(t *testing.T) {
	ua := newUA("ghost-tier", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "ghost-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"acme-dev"}},
		},
	})
	addTierLabel(ua, "developer")
	// NO tier ClusterRole installed.
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "ghost-tier")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ghost-tier"}, got); err != nil {
		t.Fatal(err)
	}
	if !hasCondition(got, condEnforcedScopeApplied, "False", reasonTierClusterRoleNotFound) {
		t.Fatalf("expected EnforcedScopeApplied=False/TierClusterRoleNotFound, status=%v", got.Object["status"])
	}
	if !hasCondition(got, condTierResolved, "False", reasonTierClusterRoleNotFound) {
		t.Fatalf("expected TierResolved=False/TierClusterRoleNotFound, status=%v", got.Object["status"])
	}
}

// ── T3 — non-developer tier with custom enforced-scopes annotation ──
// Validates the GENERIC annotation-driven path: the controller does
// NOT hardcode the developer special case. A custom tier with its own
// enforcedScopes auto-injects identically.
func TestTierAutoInject_GenericAnnotationDrivenPath(t *testing.T) {
	ua := newUA("custom-tier-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "custom-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "platform", "role": "admin", "namespaces": []any{"x"}},
		},
	})
	addTierLabel(ua, "operator")

	// Fake operator tier with TWO enforced scopes — neither is the
	// developer's env-type=dev. If the controller hardcodes the
	// developer case, this test fails.
	cr := mkTierClusterRole(
		"openova:tier-operator",
		`[{"key":"openova.io/env-type","value":"prod"},{"key":"openova.io/region","value":"fsn"}]`,
	)
	r, c := newReconciler(t, ua, cr)
	runReconcile(t, r, "custom-tier-grant")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "custom-tier-grant"}, got); err != nil {
		t.Fatal(err)
	}
	scopesRaw, _, _ := unstructured.NestedSlice(got.Object, "spec", "scopes")
	if len(scopesRaw) != 2 {
		t.Fatalf("expected 2 enforced scopes auto-injected, got %d (raw=%v)", len(scopesRaw), scopesRaw)
	}
	// Both scopes should be present — order-insensitive.
	got1 := scopesRaw[0].(map[string]any)["key"]
	got2 := scopesRaw[1].(map[string]any)["key"]
	gotKeys := []string{got1.(string), got2.(string)}
	if !containsAll(gotKeys, []string{"openova.io/env-type", "openova.io/region"}) {
		t.Fatalf("expected env-type + region scopes; got %v", gotKeys)
	}
	if !hasCondition(got, condEnforcedScopeApplied, "True", reasonAutoInjected) {
		t.Fatalf("expected EnforcedScopeApplied=True/AutoInjected, status=%v", got.Object["status"])
	}
}

// ── T3 — empty enforced-scopes annotation → AlreadyPresent (no-op) ──
func TestTierAutoInject_EmptyAnnotationIsAlreadyPresent(t *testing.T) {
	ua := newUA("admin-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "admin-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "admin", "namespaces": []any{"x"}},
		},
	})
	addTierLabel(ua, "admin")
	// admin tier ClusterRole exists but carries no enforced-scopes
	// annotation.
	cr := mkTierClusterRole("openova:tier-admin", "")
	r, c := newReconciler(t, ua, cr)
	runReconcile(t, r, "admin-grant")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "admin-grant"}, got); err != nil {
		t.Fatal(err)
	}
	scopesRaw, _, _ := unstructured.NestedSlice(got.Object, "spec", "scopes")
	if len(scopesRaw) != 0 {
		t.Fatalf("expected no scopes, got %v", scopesRaw)
	}
	if !hasCondition(got, condEnforcedScopeApplied, "True", reasonAlreadyPresent) {
		t.Fatalf("expected EnforcedScopeApplied=True/AlreadyPresent, status=%v", got.Object["status"])
	}
	if !hasCondition(got, condTierResolved, "True", reasonTierResolved) {
		t.Fatalf("expected TierResolved=True/TierResolved, status=%v", got.Object["status"])
	}
}

// ── T3 — invalid JSON in annotation → surfaces error path quietly ──
// The controller MUST NOT panic on malformed annotation. The condition
// should still surface (False/TierClusterRoleNotFound is acceptable
// because the lookup happened); the binding emission should still run.
func TestTierAutoInject_InvalidAnnotationJSONIsTolerated(t *testing.T) {
	ua := newUA("bad-anno-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "bad-anno-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"acme-prod"}},
		},
	})
	addTierLabel(ua, "viewer")
	cr := mkTierClusterRole("openova:tier-viewer", `not-json-at-all`)
	r, c := newReconciler(t, ua, cr)
	// The reconciler logs the parse error but proceeds (per the design:
	// auto-inject failure does not block emission).
	runReconcile(t, r, "bad-anno-grant")

	// RoleBinding for the legacy applications[] path should still be
	// created.
	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 1 {
		t.Fatalf("expected 1 RoleBinding (legacy path), got %d", len(rbs.Items))
	}
}

// ── parseEnforcedScopesAnnotation — pure-function tests ────────────

func TestParseEnforcedScopesAnnotation_HappyPath(t *testing.T) {
	got, err := parseEnforcedScopesAnnotation(`[{"key":"a","value":"b"},{"key":"c","value":"d"}]`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
}

func TestParseEnforcedScopesAnnotation_Empty(t *testing.T) {
	got, err := parseEnforcedScopesAnnotation("")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
	got, err = parseEnforcedScopesAnnotation("   ")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestParseEnforcedScopesAnnotation_InvalidJSON(t *testing.T) {
	_, err := parseEnforcedScopesAnnotation("not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseEnforcedScopesAnnotation_DeDupesAndSorts(t *testing.T) {
	got, err := parseEnforcedScopesAnnotation(
		`[{"key":"b","value":"x"},{"key":"a","value":"y"},{"key":"b","value":"x"}]`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected dedup, got %d: %+v", len(got), got)
	}
	if got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("expected sort: %+v", got)
	}
}

// ── Reuse: hasCondition reads spec/status/conditions[] and matches
// (type, status, reason) on a single row. Helper for the tier tests.
func hasCondition(u *unstructured.Unstructured, condType, condStatus, reason string) bool {
	conds, ok, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !ok {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] != condType {
			continue
		}
		if m["status"] != condStatus {
			continue
		}
		if reason != "" && m["reason"] != reason {
			continue
		}
		return true
	}
	return false
}

// containsAll reports whether haystack contains every needle.
func containsAll(haystack, needles []string) bool {
	set := map[string]struct{}{}
	for _, h := range haystack {
		set[h] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// Compile-time wiring sanity — every condition reason is referenced
// somewhere in the tests above. Suppress "declared but not used" in
// case any test gets removed later.
var _ = strings.ToLower
var _ = ctrl.Result{}
var _ = labels.Wildcard
var _ = client.MatchingLabels{}
