package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// mkNS returns a Namespace object carrying the given labels — used
// to seed the fake client's namespace cache for scope-translation
// tests.
func mkNS(name string, lbls map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: lbls,
		},
	}
}

// ── C5-followup #1 — tierRoleRef + application scope ──────────────
// → emits 1 RoleBinding in the application's namespace(s).
func TestTierEmission_ApplicationScope_OneNamespace(t *testing.T) {
	ua := newUA("alice-wp-dev", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "alice-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-developer",
		"scopes": []any{
			map[string]any{"key": "openova.io/application", "value": "wordpress"},
			map[string]any{"key": "openova.io/env-type", "value": "dev"},
		},
	})
	addTierLabel(ua, "developer")

	tierCR := mkTierClusterRole("openova:tier-developer",
		`[{"key":"openova.io/env-type","value":"dev"}]`)

	// Two candidate namespaces: one matches both labels (acme-dev),
	// one matches only application (acme-prod) and should be excluded.
	nsDev := mkNS("acme-dev", map[string]string{
		"openova.io/application": "wordpress",
		"openova.io/env-type":    "dev",
	})
	nsProd := mkNS("acme-prod", map[string]string{
		"openova.io/application": "wordpress",
		"openova.io/env-type":    "prod",
	})

	r, c := newReconciler(t, ua, tierCR, nsDev, nsProd)
	runReconcile(t, r, "alice-wp-dev")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 1 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s -> %s", rb.Namespace, rb.Name, rb.RoleRef.Name)
		}
		t.Fatalf("expected 1 RoleBinding (matching ns only), got %d", len(rbs.Items))
	}
	rb := rbs.Items[0]
	if rb.Namespace != "acme-dev" {
		t.Fatalf("RoleBinding ended up in %s, want acme-dev", rb.Namespace)
	}
	if rb.RoleRef.Name != "openova:tier-developer" {
		t.Fatalf("roleRef = %s, want openova:tier-developer", rb.RoleRef.Name)
	}
	if rb.RoleRef.Kind != "ClusterRole" {
		t.Fatalf("roleRef Kind = %s, want ClusterRole", rb.RoleRef.Kind)
	}
}

// ── C5-followup #2 — tierRoleRef + org scope ──────────────────────
// → emits 1 RoleBinding per org-labeled namespace.
func TestTierEmission_OrgScope_MultipleNamespaces(t *testing.T) {
	ua := newUA("bob-acme-admin", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "bob-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-admin",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
		},
	})
	addTierLabel(ua, "admin")

	tierCR := mkTierClusterRole("openova:tier-admin", "")

	ns1 := mkNS("acme-prod", map[string]string{"openova.io/org": "acme"})
	ns2 := mkNS("acme-dev", map[string]string{"openova.io/org": "acme"})
	ns3 := mkNS("globex-prod", map[string]string{"openova.io/org": "globex"})

	r, c := newReconciler(t, ua, tierCR, ns1, ns2, ns3)
	runReconcile(t, r, "bob-acme-admin")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 2 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s -> %s", rb.Namespace, rb.Name, rb.RoleRef.Name)
		}
		t.Fatalf("expected 2 RoleBindings (acme-prod + acme-dev), got %d", len(rbs.Items))
	}
	want := map[string]bool{"acme-prod": true, "acme-dev": true}
	for _, rb := range rbs.Items {
		if !want[rb.Namespace] {
			t.Fatalf("RoleBinding in unexpected ns %s", rb.Namespace)
		}
		if rb.RoleRef.Name != "openova:tier-admin" {
			t.Fatalf("roleRef: %s", rb.RoleRef.Name)
		}
	}
}

// ── C5-followup #3 — tierRoleRef + wildcard scope → ClusterRoleBinding
func TestTierEmission_WildcardScope_ClusterRoleBinding(t *testing.T) {
	ua := newUA("carol-owner", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "carol-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-owner",
		"scopes": []any{
			map[string]any{"key": "*", "value": "*"},
		},
	})
	addTierLabel(ua, "owner")

	tierCR := mkTierClusterRole("openova:tier-owner", "")
	r, c := newReconciler(t, ua, tierCR)
	runReconcile(t, r, "carol-owner")

	// No namespaced RoleBindings.
	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 0 {
		t.Fatalf("wildcard scope must NOT emit RoleBindings, got %d", len(rbs.Items))
	}
	// One ClusterRoleBinding pointing at the tier ClusterRole.
	var crbs rbacv1.ClusterRoleBindingList
	if err := c.List(context.Background(), &crbs); err != nil {
		t.Fatal(err)
	}
	if len(crbs.Items) != 1 {
		t.Fatalf("expected 1 ClusterRoleBinding, got %d", len(crbs.Items))
	}
	if crbs.Items[0].RoleRef.Name != "openova:tier-owner" {
		t.Fatalf("roleRef: %s", crbs.Items[0].RoleRef.Name)
	}
}

// ── C5-followup #3b — empty scopes also means cluster-wide ─────────
func TestTierEmission_EmptyScopes_ClusterRoleBinding(t *testing.T) {
	ua := newUA("dan-owner-empty", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "dan-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-owner",
		// scopes intentionally omitted → cluster-wide per CRD §6.3.
	})
	addTierLabel(ua, "owner")

	tierCR := mkTierClusterRole("openova:tier-owner", "")
	r, c := newReconciler(t, ua, tierCR)
	runReconcile(t, r, "dan-owner-empty")

	var crbs rbacv1.ClusterRoleBindingList
	_ = c.List(context.Background(), &crbs)
	if len(crbs.Items) != 1 {
		t.Fatalf("expected 1 ClusterRoleBinding for empty scopes, got %d", len(crbs.Items))
	}
}

// ── C5-followup #4 — multi-scope AND-within ───────────────────────
func TestTierEmission_MultiScope_AndWithin(t *testing.T) {
	ua := newUA("eve-multi", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "eve-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-operator",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
			map[string]any{"key": "openova.io/env-type", "value": "dev"},
		},
	})
	addTierLabel(ua, "operator")

	tierCR := mkTierClusterRole("openova:tier-operator", "")

	// 4 candidate namespaces, only one matches BOTH scopes.
	matchAll := mkNS("acme-dev", map[string]string{
		"openova.io/org":      "acme",
		"openova.io/env-type": "dev",
	})
	matchOrgOnly := mkNS("acme-prod", map[string]string{
		"openova.io/org":      "acme",
		"openova.io/env-type": "prod",
	})
	matchEnvOnly := mkNS("globex-dev", map[string]string{
		"openova.io/org":      "globex",
		"openova.io/env-type": "dev",
	})
	matchNeither := mkNS("widgets-prod", map[string]string{
		"openova.io/org":      "widgets",
		"openova.io/env-type": "prod",
	})

	r, c := newReconciler(t, ua, tierCR, matchAll, matchOrgOnly, matchEnvOnly, matchNeither)
	runReconcile(t, r, "eve-multi")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 1 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s", rb.Namespace, rb.Name)
		}
		t.Fatalf("expected 1 RoleBinding (AND-within), got %d", len(rbs.Items))
	}
	if rbs.Items[0].Namespace != "acme-dev" {
		t.Fatalf("AND-within selected wrong ns: %s", rbs.Items[0].Namespace)
	}
}

// ── Regression — legacy applications[] CR (no tierRoleRef) still works
func TestTierEmission_LegacyApplicationsPath_StillWorks(t *testing.T) {
	ua := newUA("legacy-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "leg-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"legacy-ns"}},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "legacy-grant")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 1 {
		t.Fatalf("legacy path broken: got %d RoleBindings", len(rbs.Items))
	}
	rb := rbs.Items[0]
	if rb.Namespace != "legacy-ns" {
		t.Fatalf("legacy path ns: got %s", rb.Namespace)
	}
	// roleRef should resolve via the per-application enum
	// (openova:application-viewer), not the new tier ClusterRole.
	if rb.RoleRef.Name != "openova:application-viewer" {
		t.Fatalf("legacy roleRef: %s", rb.RoleRef.Name)
	}
}

// ── Coexistence — tierRoleRef + applications[] → tier wins, apps ignored
func TestTierEmission_BothPathsCoexist_TierWins(t *testing.T) {
	ua := newUA("dual-shape", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "dual-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-admin",
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"unused-ns"}},
		},
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
		},
	})
	addTierLabel(ua, "admin")

	tierCR := mkTierClusterRole("openova:tier-admin", "")
	ns := mkNS("acme-prod", map[string]string{"openova.io/org": "acme"})

	r, c := newReconciler(t, ua, tierCR, ns)
	runReconcile(t, r, "dual-shape")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	if len(rbs.Items) != 1 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s -> %s", rb.Namespace, rb.Name, rb.RoleRef.Name)
		}
		t.Fatalf("expected exactly 1 RoleBinding (tier path wins), got %d", len(rbs.Items))
	}
	rb := rbs.Items[0]
	if rb.Namespace != "acme-prod" {
		t.Fatalf("tier-path ns wrong: %s (legacy app would have been unused-ns)", rb.Namespace)
	}
	if rb.RoleRef.Name != "openova:tier-admin" {
		t.Fatalf("tier-path roleRef wrong: %s (legacy would have been openova:application-viewer)", rb.RoleRef.Name)
	}

	// Status condition surfaces the LegacyApplicationsIgnored note.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dual-shape"}, got); err != nil {
		t.Fatal(err)
	}
	if !hasCondition(got, condBindingsReconciled, "True", reasonBindingsReconciledOK) {
		t.Fatalf("expected BindingsReconciled=True/Reconciled, status=%v", got.Object["status"])
	}
	// Message should mention applications[] ignored.
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	hadIgnored := false
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == condBindingsReconciled {
			if msg, _ := m["message"].(string); msg != "" && containsSub(msg, "applications[] ignored") {
				hadIgnored = true
			}
		}
	}
	if !hadIgnored {
		t.Fatalf("expected message to mention 'applications[] ignored'; conditions=%+v", conds)
	}
}

// ── Tier path with no matching ns → 0 bindings, condition still True
func TestTierEmission_NoMatchingNamespacesIsEmptyButReady(t *testing.T) {
	ua := newUA("isolated-grant", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "iso-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-developer",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "ghost-org"},
		},
	})
	addTierLabel(ua, "developer")

	tierCR := mkTierClusterRole("openova:tier-developer",
		`[{"key":"openova.io/env-type","value":"dev"}]`)
	// No namespace carrying openova.io/org=ghost-org.
	r, c := newReconciler(t, ua, tierCR)
	runReconcile(t, r, "isolated-grant")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 0 {
		t.Fatalf("expected 0 bindings (no matching ns), got %d", len(rbs.Items))
	}
	var crbs rbacv1.ClusterRoleBindingList
	_ = c.List(context.Background(), &crbs)
	if len(crbs.Items) != 0 {
		t.Fatalf("expected 0 ClusterRoleBindings, got %d", len(crbs.Items))
	}
}

// ── Drift recovery on tier-path RoleBinding ───────────────────────
func TestTierEmission_DriftRecovery(t *testing.T) {
	ua := newUA("drift-tier", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "drift-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-admin",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
		},
	})
	addTierLabel(ua, "admin")

	tierCR := mkTierClusterRole("openova:tier-admin", "")
	ns := mkNS("acme-prod", map[string]string{"openova.io/org": "acme"})

	r, c := newReconciler(t, ua, tierCR, ns)
	runReconcile(t, r, "drift-tier")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatalf("setup: expected 1 RB, got %d", len(rbs.Items))
	}
	live := rbs.Items[0].DeepCopy()
	// Mutate roleRef.
	live.RoleRef.Name = "openova:tier-owner"
	if err := c.Update(context.Background(), live); err != nil {
		t.Skipf("fake client refused mutation: %v", err)
	}
	runReconcile(t, r, "drift-tier")

	rbs = rbacv1.RoleBindingList{}
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatalf("post-drift: %d", len(rbs.Items))
	}
	if rbs.Items[0].RoleRef.Name != "openova:tier-admin" {
		t.Fatalf("drift not restored: roleRef=%s", rbs.Items[0].RoleRef.Name)
	}
}

// ── Spec validation — tierRoleRef without applications is now valid
func TestParseSpec_TierOnlyShape_Valid(t *testing.T) {
	u := mkUA(map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-developer",
		"scopes": []any{
			map[string]any{"key": "openova.io/application", "value": "wp"},
		},
	})
	spec, msg := ParseSpec(u)
	if msg != "" {
		t.Fatalf("ParseSpec rejected tier-only shape: %q", msg)
	}
	if spec.TierRoleRef != "openova:tier-developer" {
		t.Fatalf("TierRoleRef parse: %q", spec.TierRoleRef)
	}
	if len(spec.Scopes) != 1 || spec.Scopes[0].Key != "openova.io/application" {
		t.Fatalf("Scopes parse: %+v", spec.Scopes)
	}
}

// ── ParseSpec accepts BOTH key/value (post-A1) and labelKey/labelValue
// (legacy) shapes ──────────────────────────────────────────────────
func TestParseSpec_AcceptsBothScopeShapes(t *testing.T) {
	u := mkUA(map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-admin",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
			map[string]any{"labelKey": "openova.io/env-type", "labelValue": "dev"},
		},
	})
	spec, msg := ParseSpec(u)
	if msg != "" {
		t.Fatalf("ParseSpec err: %q", msg)
	}
	if len(spec.Scopes) != 2 {
		t.Fatalf("expected 2 scopes, got %d: %+v", len(spec.Scopes), spec.Scopes)
	}
}

// ── ParseSpec — tier read from CR label, not spec.tier ────────────
func TestParseSpec_TierFromLabelTakesPrecedence(t *testing.T) {
	u := mkUA(map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"tier":         "viewer", // legacy spec.tier
		"applications": []any{
			map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"x"}},
		},
	})
	addTierLabel(u, "admin")

	spec, msg := ParseSpec(u)
	if msg != "" {
		t.Fatalf("err: %q", msg)
	}
	if spec.Tier != "admin" {
		t.Fatalf("expected tier=admin (label), got %q", spec.Tier)
	}
}

// ── Orphan cleanup — scope shrinks → previously-bound ns drops out
func TestTierEmission_OrphanCleanupOnScopeShrink(t *testing.T) {
	ua := newUA("orph-shrink", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "orph-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "openova:tier-admin",
		"scopes": []any{
			map[string]any{"key": "openova.io/org", "value": "acme"},
		},
	})
	addTierLabel(ua, "admin")

	tierCR := mkTierClusterRole("openova:tier-admin", "")
	ns1 := mkNS("acme-prod", map[string]string{"openova.io/org": "acme"})
	ns2 := mkNS("acme-dev", map[string]string{"openova.io/org": "acme"})

	r, c := newReconciler(t, ua, tierCR, ns1, ns2)
	runReconcile(t, r, "orph-shrink")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 2 {
		t.Fatalf("setup: expected 2 RBs, got %d", len(rbs.Items))
	}

	// Tighten scope by adding env-type=prod — only acme-prod survives.
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "orph-shrink"}, current); err != nil {
		t.Fatal(err)
	}
	// Add env-type label to ns1 only so the AND-within filter narrows.
	if err := c.Get(context.Background(), types.NamespacedName{Name: "acme-prod"}, ns1); err != nil {
		t.Fatal(err)
	}
	ns1.Labels["openova.io/env-type"] = "prod"
	if err := c.Update(context.Background(), ns1); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(current.Object, []any{
		map[string]any{"key": "openova.io/org", "value": "acme"},
		map[string]any{"key": "openova.io/env-type", "value": "prod"},
	}, "spec", "scopes")
	current.SetGeneration(2)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "orph-shrink")

	rbs = rbacv1.RoleBindingList{}
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s", rb.Namespace, rb.Name)
		}
		t.Fatalf("orphan cleanup failed: want 1 survivor, got %d", len(rbs.Items))
	}
	if rbs.Items[0].Namespace != "acme-prod" {
		t.Fatalf("survivor in wrong ns: %s", rbs.Items[0].Namespace)
	}
}

// ── Tier path with malformed tierRoleRef (still emits binding) ─────
// The CRD pattern guards against malformed tierRoleRef at admission;
// this test confirms the controller doesn't panic if a CR somehow
// arrives with one anyway (e.g. an older CRD that didn't have the
// pattern).
func TestTierEmission_NonStandardTierRoleRefStillEmits(t *testing.T) {
	ua := newUA("custom-role", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "custom-sub"},
		"sovereignRef": "omantel",
		"tierRoleRef":  "some:custom-role",
		"scopes": []any{
			map[string]any{"key": "*", "value": "*"},
		},
	})
	addTierLabel(ua, "admin")

	tierCR := mkTierClusterRole("openova:tier-admin", "")
	r, c := newReconciler(t, ua, tierCR)
	runReconcile(t, r, "custom-role")

	var crbs rbacv1.ClusterRoleBindingList
	_ = c.List(context.Background(), &crbs)
	if len(crbs.Items) != 1 {
		t.Fatalf("expected 1 CRB even with non-standard roleRef, got %d", len(crbs.Items))
	}
	if crbs.Items[0].RoleRef.Name != "some:custom-role" {
		t.Fatalf("roleRef: %s", crbs.Items[0].RoleRef.Name)
	}
}

// containsSub is a tiny strings.Contains stand-in to avoid importing
// strings into a large test file twice.
func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
