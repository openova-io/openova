package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// #5848 (UAT row R17) — "deleting an Organization CR cascades cleanly — no
// orphaned ns / app / DNS leak."
//
// The R17 walk on hw292 found the cascade working everywhere except the gateway
// surface: Org CR gone, namespace gone, HelmReleases 0, Certificates 0 — and an
// HTTPRoute still present, still carrying the deleted Org's hostname, still
// DNS-backed. That is customer-visible: a stale host that resolves.
//
// CAUSE. teardownTenantRoute deleted exactly ONE route, by a name it derives
// from the Org's own hostname (`catalyst-ui-<host-dashed>`). That finds the
// route THIS controller created and nothing else. Since #5647 made console-route
// creation multi-region, a second producer also emits per-Org routes, and one
// named differently is invisible to a name-targeted delete — it survives in the
// same cluster the reaper is running in.
//
// The label is the durable identity (`openova.io/organization: <slug>`, stamped
// by the create path); the NAME is a derivation each producer chooses. So the
// reap is now selector-based.
//
// WHY THESE ARE BEHAVIOURAL, NOT AST, TESTS. Unlike a wiring guard, what can
// silently regress here is the SELECTOR SEMANTICS — a reap that misses the other
// producer's route, or one that over-matches into a live Org. Neither is visible
// in the call graph; both are visible to a fake client holding real objects.
//
// Mutation-tested, and one of the three attempts was INERT — recorded because an
// ineffective mutation proves nothing and pretending otherwise is how a test
// earns unwarranted trust:
//
//	remove the selector reap   -> RED  "routes survived the cascade:
//	                                    [console-r17probe-region-b]"
//	drop the label from it     -> RED  "expected exactly the 2 foreign routes to
//	                                    survive, got []"
//	set the ITEM GVK on the    -> GREEN. controller-runtime normalises the list
//	list instead of the List          kind, so this is NOT a failure mode and no
//	                                  test here defends against it.

func newRoute(ns, name, orgSlug string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(httpRouteGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	if orgSlug != "" {
		u.SetLabels(map[string]string{"openova.io/organization": orgSlug})
	}
	return u
}

func teardownReconciler(t *testing.T, objs ...runtime.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	// Register the List kind explicitly: an UnstructuredList with only the item
	// GVK set returns ZERO items rather than erroring, which would make this
	// whole test file pass while the reaper found nothing.
	scheme.AddKnownTypeWithName(httpRouteGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(httpRouteListGVK, &unstructured.UnstructuredList{})

	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	return &Reconciler{Client: c, Log: logf.Log.WithName("test")}
}

func orgFor(slug string) *orgapi.Organization {
	o := &orgapi.Organization{}
	o.Name = slug
	o.Spec.Slug = slug
	o.Spec.TenantPublic.ParentDomain = "omani.homes"
	o.Spec.TenantPublic.Subdomain = slug
	return o
}

func routeNames(t *testing.T, r *Reconciler, ns string) []string {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(httpRouteListGVK)
	if err := r.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := []string{}
	for i := range list.Items {
		if list.Items[i].GetNamespace() == ns {
			out = append(out, list.Items[i].GetName())
		}
	}
	return out
}

// The exact R17 shape: the controller's own route PLUS one written by the other
// producer under a different name. Before the fix only the first was reaped.
func TestTeardownTenantRoute_ReapsRoutesTheNameDerivationMisses(t *testing.T) {
	org := orgFor("r17probe")
	ns, ownName, ok := (&Reconciler{}).tenantRouteNameNS(org)
	if !ok {
		t.Fatal("tenantRouteNameNS returned !ok for a pool Org — the fixture is wrong, not the code")
	}

	r := teardownReconciler(t,
		newRoute(ns, ownName, "r17probe"),                     // this controller's route
		newRoute(ns, "console-r17probe-region-b", "r17probe"), // the other producer's
	)

	changed, err := r.teardownTenantRoute(context.Background(), org)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !changed {
		t.Fatal("teardown reported no change while two routes existed")
	}
	if left := routeNames(t, r, ns); len(left) != 0 {
		t.Fatalf("routes survived the cascade: %v\n"+
			"A route still carrying the deleted Org's hostname stays DNS-backed and "+
			"customer-visible — that is exactly what UAT row R17 measured on hw292 (#5848).", left)
	}
}

// The control that makes the test above mean something: the reap must be scoped
// to THIS Org. A selector that dropped the label, or matched on namespace alone,
// would pass the first test and quietly delete a live Org's ingress.
func TestTeardownTenantRoute_LeavesOtherOrgsRoutesAlone(t *testing.T) {
	org := orgFor("r17probe")
	ns, ownName, _ := (&Reconciler{}).tenantRouteNameNS(org)

	r := teardownReconciler(t,
		newRoute(ns, ownName, "r17probe"),
		newRoute(ns, "console-r17probe-region-b", "r17probe"),
		newRoute(ns, "catalyst-ui-console-uatco-omani-homes", "uatco"), // a LIVE Org
		newRoute(ns, "unlabelled-shared-route", ""),                    // not Org-owned
	)

	if _, err := r.teardownTenantRoute(context.Background(), org); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	left := routeNames(t, r, ns)
	if len(left) != 2 {
		t.Fatalf("expected exactly the 2 foreign routes to survive, got %v — "+
			"deleting a live Organization's console route while reaping a different Org "+
			"would take that Org's console offline", left)
	}
	for _, n := range left {
		if n == ownName || n == "console-r17probe-region-b" {
			t.Fatalf("a r17probe route survived: %v", left)
		}
	}
}

// Absent-as-success: the finalizer must not wedge on an already-clean Org.
// This is the idempotency the orchestrator's comment promises.
func TestTeardownTenantRoute_NoRoutesIsCleanNoOp(t *testing.T) {
	r := teardownReconciler(t)
	changed, err := r.teardownTenantRoute(context.Background(), orgFor("r17probe"))
	if err != nil {
		t.Fatalf("teardown on an already-clean Org errored: %v — the finalizer would "+
			"never be removed and the Organization would hang in Terminating", err)
	}
	if changed {
		t.Fatal("teardown reported changed=true with no routes present")
	}
}

// An Org with no pool parentDomain never created a route; the selector must not
// run at all. Without the short-circuit the reap would select on
// `openova.io/organization=""`.
func TestTeardownTenantRoute_NoParentDomainShortCircuits(t *testing.T) {
	org := &orgapi.Organization{}
	org.Name = "nopool"
	org.Spec.Slug = "nopool"
	// no TenantPublic.ParentDomain

	ns, _, ok := (&Reconciler{}).tenantRouteNameNS(org)
	if ok {
		t.Fatalf("tenantRouteNameNS returned ok for an Org with no parentDomain (ns=%q)", ns)
	}

	r := teardownReconciler(t, newRoute("catalyst-system", "someone-elses-route", "nopool"))
	changed, err := r.teardownTenantRoute(context.Background(), org)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if changed {
		t.Fatal("teardown touched something for an Org that never engaged the route path")
	}
	if left := routeNames(t, r, "catalyst-system"); len(left) != 1 {
		t.Fatalf("short-circuit failed — routes deleted for a no-parentDomain Org: %v", left)
	}
}
