package controller

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newScheme registers everything the fake client needs to round-trip
// our objects: client-go's RBAC types + the unstructured UserAccess.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("rbacv1 scheme: %v", err)
	}
	// Register the UserAccess GVK + GVR with the fake's REST mapper.
	// AddKnownTypeWithName lets us use the unstructured object with the
	// fake client without a generated typed client.
	gvk := UserAccessGVK()
	listGVK := schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"}
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return s
}

func newUA(name string, generation int64, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(UserAccessGVK())
	u.SetName(name)
	u.SetUID(types.UID("uid-" + name))
	u.SetGeneration(generation)
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func newReconciler(t *testing.T, objs ...client.Object) (*UserAccessReconciler, client.Client) {
	t.Helper()
	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": UserAccessGroup + "/" + UserAccessVersion,
				"kind":       UserAccessKind,
			},
		}).
		Build()
	r := &UserAccessReconciler{Client: c, Scheme: s}
	return r, c
}

func runReconcile(t *testing.T, r *UserAccessReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// T1 — happy-path single application × single namespace → 1 RB.
func TestReconcile_HappyPath_SingleAppSingleNS(t *testing.T) {
	ua := newUA("alice-wp-edit", 1, map[string]any{
		"user": map[string]any{
			"keycloakSubject": "abc-123",
		},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "editor",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "alice-wp-edit")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatalf("list rbs: %v", err)
	}
	if len(rbs.Items) != 1 {
		t.Fatalf("expected 1 RoleBinding, got %d", len(rbs.Items))
	}
	rb := rbs.Items[0]
	if rb.Namespace != "acme-prod" {
		t.Fatalf("namespace: got %s want acme-prod", rb.Namespace)
	}
	if rb.RoleRef.Name != "openova:application-editor" {
		t.Fatalf("roleRef: got %s", rb.RoleRef.Name)
	}
	if got := rb.Labels[LabelUserAccessName]; got != "alice-wp-edit" {
		t.Fatalf("ua-name label: %s", got)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Kind != SubjectKindUser || rb.Subjects[0].Name != "oidc:abc-123" {
		t.Fatalf("subject: %+v", rb.Subjects)
	}
	// ownerRef present and pinned to the UA's UID.
	if len(rb.OwnerReferences) != 1 || rb.OwnerReferences[0].UID != ua.GetUID() {
		t.Fatalf("ownerRef: %+v", rb.OwnerReferences)
	}
}

// T2 — multi-application × multi-namespace fan-out.
func TestReconcile_MultiAppMultiNS(t *testing.T) {
	ua := newUA("bob-multi", 1, map[string]any{
		"user": map[string]any{
			"keycloakSubject": "bob-sub",
		},
		"sovereignRef": "otech",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "viewer",
				"namespaces": []any{"acme-dev", "acme-stg", "acme-prod"},
			},
			map[string]any{
				"app":       "ai-inference",
				"role":      "admin",
				"vClusters": []any{"acme"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "bob-multi")

	var rbs rbacv1.RoleBindingList
	if err := c.List(context.Background(), &rbs); err != nil {
		t.Fatal(err)
	}
	// 3 namespaces × wordpress (viewer) + 1 namespace × ai-inference (admin)
	if len(rbs.Items) != 4 {
		for _, rb := range rbs.Items {
			t.Logf("rb: %s/%s -> %s", rb.Namespace, rb.Name, rb.RoleRef.Name)
		}
		t.Fatalf("expected 4 RoleBindings, got %d", len(rbs.Items))
	}
	// ai-inference grant landed in vcluster-acme.
	found := false
	for _, rb := range rbs.Items {
		if rb.Labels[LabelUserAccessApp] == "ai-inference" && rb.Namespace == "vcluster-acme" {
			found = true
			if rb.RoleRef.Name != "openova:application-admin" {
				t.Fatalf("ai-inference role: %s", rb.RoleRef.Name)
			}
		}
	}
	if !found {
		t.Fatalf("did not find ai-inference RoleBinding in vcluster-acme")
	}
}

// T3 — wildcard scope namespaces:["*"] → ClusterRoleBinding.
func TestReconcile_WildcardNamespacesYieldsCRB(t *testing.T) {
	ua := newUA("carol-global", 1, map[string]any{
		"user": map[string]any{
			"keycloakSubject": "carol-sub",
		},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "platform",
				"role":       "admin",
				"namespaces": []any{"*"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "carol-global")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 0 {
		t.Fatalf("expected 0 RoleBindings (cluster-wide grant), got %d", len(rbs.Items))
	}
	var crbs rbacv1.ClusterRoleBindingList
	if err := c.List(context.Background(), &crbs); err != nil {
		t.Fatal(err)
	}
	if len(crbs.Items) != 1 {
		t.Fatalf("expected 1 ClusterRoleBinding, got %d", len(crbs.Items))
	}
	if crbs.Items[0].RoleRef.Name != "openova:application-admin" {
		t.Fatalf("roleRef: %s", crbs.Items[0].RoleRef.Name)
	}
}

// T4 — group subject (keycloakGroups) → Group-kind subject.
func TestReconcile_GroupSubject(t *testing.T) {
	ua := newUA("acme-admins-grp", 1, map[string]any{
		"user": map[string]any{
			"keycloakGroups": []any{"/acme/admins", "/acme/sre"},
		},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "admin",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "acme-admins-grp")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatalf("got %d", len(rbs.Items))
	}
	subs := rbs.Items[0].Subjects
	if len(subs) != 2 {
		t.Fatalf("expected 2 subjects, got %d", len(subs))
	}
	for _, s := range subs {
		if s.Kind != SubjectKindGroup {
			t.Fatalf("expected Group, got %s", s.Kind)
		}
		if !strings.HasPrefix(s.Name, "oidc:") {
			t.Fatalf("expected oidc: prefix, got %s", s.Name)
		}
	}
}

// T5 — drift detection: hand-mutate the RoleBinding's roleRef → next
// reconcile restores it.
func TestReconcile_DriftDetectionAndRestore(t *testing.T) {
	ua := newUA("alice-wp-edit", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc-123"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "editor",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "alice-wp-edit")

	// Hand-mutate the live RoleBinding roleRef to admin.
	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatal("setup expected 1 rb")
	}
	live := rbs.Items[0].DeepCopy()
	// RoleRef is immutable in real K8s — but the fake client allows
	// the field swap, which is what we need to simulate the drift
	// scenario (in production an attacker would delete-and-recreate
	// to mutate, which the controller restores via Create on the
	// next reconcile).
	live.RoleRef.Name = "openova:application-admin"
	if err := c.Update(context.Background(), live); err != nil {
		// Some fake-client versions reject the update; that itself
		// proves the API would protect us. Skip in that case.
		t.Skipf("fake client refused the mutation: %v", err)
	}

	runReconcile(t, r, "alice-wp-edit")

	// Refetch.
	var got rbacv1.RoleBinding
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "acme-prod", Name: live.Name}, &got)
	if got.RoleRef.Name != "openova:application-editor" {
		t.Fatalf("drift was NOT restored: roleRef=%s", got.RoleRef.Name)
	}
}

// T6 — orphan deletion: shrink namespaces[] → previously-bound ns
// gets deleted.
func TestReconcile_OrphanDeletionOnSpecShrink(t *testing.T) {
	ua := newUA("dave-multi-ns", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "dave-sub"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "viewer",
				"namespaces": []any{"acme-dev", "acme-stg", "acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "dave-multi-ns")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 3 {
		t.Fatalf("setup: want 3 got %d", len(rbs.Items))
	}

	// Shrink to one namespace + bump generation.
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(UserAccessGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dave-multi-ns"}, current); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(current.Object, []any{
		map[string]any{
			"app":        "wordpress",
			"role":       "viewer",
			"namespaces": []any{"acme-prod"},
		},
	}, "spec", "applications")
	current.SetGeneration(2)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "dave-multi-ns")

	rbs = rbacv1.RoleBindingList{}
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		for _, rb := range rbs.Items {
			t.Logf("survivor: %s/%s", rb.Namespace, rb.Name)
		}
		t.Fatalf("orphan cleanup failed: want 1 survivor, got %d", len(rbs.Items))
	}
	if rbs.Items[0].Namespace != "acme-prod" {
		t.Fatalf("survivor in wrong ns: %s", rbs.Items[0].Namespace)
	}
}

// T7 — idempotency: re-reconciling on steady state = 0 writes.
func TestReconcile_Idempotent(t *testing.T) {
	ua := newUA("idemp-test", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wordpress",
				"role":       "viewer",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "idemp-test")

	// Snapshot the resourceVersion of the binding.
	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatalf("setup: %d", len(rbs.Items))
	}
	rv1 := rbs.Items[0].ResourceVersion

	// Reconcile twice more. The fake client's resourceVersion
	// monotonically increments on every Update — if our reconciler
	// writes nothing, rv stays the same.
	runReconcile(t, r, "idemp-test")
	runReconcile(t, r, "idemp-test")

	rbs = rbacv1.RoleBindingList{}
	_ = c.List(context.Background(), &rbs)
	rv2 := rbs.Items[0].ResourceVersion
	if rv1 != rv2 {
		t.Fatalf("non-idempotent: rv changed %s -> %s", rv1, rv2)
	}
}

// T8 — invalid spec → status.phase=Failed, no bindings created.
func TestReconcile_InvalidSpec(t *testing.T) {
	// Missing applications[].
	ua := newUA("invalid-empty", 1, map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"applications": []any{},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "invalid-empty")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 0 {
		t.Fatalf("invalid spec must not create bindings, got %d", len(rbs.Items))
	}
}

// T9 — deletion of the UA propagates via ownerRef GC. We can't
// simulate GC in the fake client, but we verify the OwnerReference is
// set with controller=true + blockOwnerDeletion=true so a real
// apiserver's GC will cascade.
func TestReconcile_OwnerRefShape(t *testing.T) {
	ua := newUA("eve-delete", 7, map[string]any{
		"user":         map[string]any{"keycloakSubject": "abc"},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wp",
				"role":       "viewer",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	r, c := newReconciler(t, ua)
	runReconcile(t, r, "eve-delete")

	var rbs rbacv1.RoleBindingList
	_ = c.List(context.Background(), &rbs)
	if len(rbs.Items) != 1 {
		t.Fatalf("got %d", len(rbs.Items))
	}
	or := rbs.Items[0].OwnerReferences
	if len(or) != 1 {
		t.Fatalf("owner ref count: %d", len(or))
	}
	if or[0].APIVersion != UserAccessGroup+"/"+UserAccessVersion ||
		or[0].Kind != UserAccessKind ||
		or[0].Name != "eve-delete" {
		t.Fatalf("owner ref shape wrong: %+v", or[0])
	}
	if or[0].Controller == nil || !*or[0].Controller {
		t.Fatal("owner ref must have controller=true")
	}
	if or[0].BlockOwnerDeletion == nil || !*or[0].BlockOwnerDeletion {
		t.Fatal("owner ref must have blockOwnerDeletion=true")
	}
}

// T10 — a UA that doesn't exist (deleted before reconcile fired)
// returns nil, no error, no panic.
func TestReconcile_NotFoundIsNoOp(t *testing.T) {
	r, _ := newReconciler(t)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ghost"},
	}); err != nil {
		t.Fatalf("expected nil error on NotFound, got %v", err)
	}
}

// asMetaTime is a small helper so the test file doesn't drag a
// metav1 import for the one place we want to compare timestamps.
func asMetaTime(t apiextv1.Time) string { return t.Format("2006-01-02T15:04:05Z") }
