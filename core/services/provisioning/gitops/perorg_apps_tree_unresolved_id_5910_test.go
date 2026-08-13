package gitops

import (
	"strings"
	"testing"
)

// UAT row 95 / #5910 — a purchased app is recorded at checkout (`Apps (1)`), the
// Organization converges, and NO Application ever materialises.
//
// The chain, traced to the write seam:
//
//  1. resolveAppSlugs (handlers.go:363-381) substitutes the RAW UUID when the
//     catalog id lookup misses — and returns appIDs wholesale when the catalog
//     fetch fails. Either way len(out) == len(in), so the cart count survives
//     intact. That is precisely why checkout showed `Apps (1)` and why every
//     count-based check reads green.
//  2. consumer.go:589 hands that value to GeneratePerOrgAppsTree AS A SLUG.
//  3. GetAppSpec (apps.go:259) returns a bare AppSpec{} for anything not in
//     KnownApps.
//
// So an id-scheme mismatch (the #4815 family) degrades SILENTLY into "render an
// app whose spec is empty", while the Organization still reports state=done
// because every step it measures did succeed.
//
// These tests pin the RENDER half of that chain, which needs no live env. They
// are deliberately written as characterisation + control rather than as an
// assertion of the desired behaviour: the fix belongs in resolveAppSlugs (make
// the unresolvable case reportable), and when it lands, the first test here
// should be revisited rather than silently kept passing.
func TestPerOrgAppsTree_UnresolvedIDRendersNoWorkload_5910(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	// A catalog UUID that never resolved to a slug — exactly what
	// resolveAppSlugs emits on a lookup miss.
	const unresolved = "0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"

	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			files, _ := g.GeneratePerOrgAppsTree("g95walk", plan, []string{unresolved}, "pw123")

			// CONTROL FIRST. The identical call with a REAL slug must produce a
			// workload — otherwise the assertion below passes because the
			// generator produces nothing for any input, which would make this
			// test worthless. This is the vacuity check.
			ctl, _ := g.GeneratePerOrgAppsTree("g95walk", plan, []string{"wordpress"}, "pw123")
			ctlHasWorkload := false
			for path := range ctl {
				if strings.Contains(path, "app-wordpress") {
					ctlHasWorkload = true
					break
				}
			}
			if !ctlHasWorkload {
				t.Fatalf("CONTROL FAILED: a real slug (wordpress) rendered no app-* file, so this test "+
					"cannot distinguish 'unresolved id drops the app' from 'the generator renders nothing'. "+
					"keys: %v", keysOf(ctl))
			}

			// WHAT ACTUALLY HAPPENS — measured, not assumed. The app is NOT
			// dropped at this seam. A Deployment IS emitted, named after the raw
			// UUID, carrying a spec-less husk:
			//
			//     image:                     <- bare, YAML null
			//     ports:
			//       - containerPort: 0
			//     livenessProbe: { tcpSocket: { port: 0 } }
			//
			// containerPort 0 is INVALID to the apiserver (1-65535), so this
			// manifest cannot be applied. And per the #4389 note in
			// helmrelease_apps.go, a manifest the Kyverno/dry-run stage rejects
			// fails the WHOLE vcluster/apps Kustomization — "the app (+ any
			// co-installed app in the same apply) never lands".
			//
			// That is the real blast radius of an unresolved catalog id, and it
			// matches UAT row 95's symptom exactly: the Organization converges
			// (every step IT measures succeeded), the cart count reads 1, and no
			// Application exists — nor would any sibling app in the same apply.
			//
			// NOTE ON THE FIRST DRAFT OF THIS TEST: it checked for `image: ""`
			// and `image: ''`. The generator emits a BARE `image:`, so that check
			// could never fire — a guard testing a shape that cannot occur. It is
			// asserted below against the shape that is actually produced.
			//
			// THE FIX (AppIsRenderable, apps.go): the unresolvable entry now
			// renders NOTHING. This test was written as a characterisation of
			// the broken output and its own comment said it should be rewritten
			// when the fix landed — this is that rewrite.
			//
			// Asserting the FILE IS ABSENT is the whole point: it is the husk's
			// existence, not its contents, that fails the Kustomization.
			if husk, ok := files["vcluster/apps/app-"+unresolved+".yaml"]; ok {
				t.Errorf("#5910: an unresolvable cart id STILL renders a Deployment. That manifest "+
					"carries a null `image` and `containerPort: 0`, is invalid to the apiserver, and "+
					"per #4389 fails the WHOLE vcluster/apps Kustomization — so every co-installed "+
					"app in the same apply never lands (UAT rows 90/95/234):\n%s", husk)
			}
			// And nothing else may carry the raw id either — a route or a db doc
			// naming it would be a dangling reference in the kustomization index,
			// which breaks the build just as completely (the #4567 shape).
			for path := range files {
				if strings.Contains(path, unresolved) {
					t.Errorf("#5910: file %q still names the unresolvable id; it can only "+
						"reference objects that are never created", path)
				}
			}
			t.Logf("#5910 FIXED at the render seam: the unresolvable id contributes no manifest, "+
				"so the vcluster/apps Kustomization stays appliable for the rest of the cart")
		})
	}
}

// TestPerOrgAppsTree_UnresolvedIDIsIndistinguishable_5910 states the property that
// makes this class hard to catch: at the render seam an unresolved UUID and a
// legitimate-but-unknown slug are the SAME input. Nothing downstream of
// resolveAppSlugs can tell them apart, which is why the fix has to live upstream
// where the lookup miss is still observable.
func TestPerOrgAppsTree_UnresolvedIDIsIndistinguishable_5910(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	uuidFiles, _ := g.GeneratePerOrgAppsTree("g95walk", "s",
		[]string{"0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"}, "pw123")
	wordFiles, _ := g.GeneratePerOrgAppsTree("g95walk", "s",
		[]string{"definitely-not-a-real-app"}, "pw123")

	if len(uuidFiles) != len(wordFiles) {
		t.Errorf("expected an unresolved UUID and an unknown slug to render identically at this seam "+
			"(that indistinguishability IS the defect, #5910) — got %d vs %d files",
			len(uuidFiles), len(wordFiles))
	}
}

// TestPerOrgAppsTree_UnresolvableEntryDoesNotSinkTheCart_5910 is the assertion
// the rows actually depend on, and the one the characterisation test above could
// not make: the BLAST RADIUS.
//
// UAT row 90 is the funnel's terminal acceptance — the purchased WordPress must
// SERVE. Row 234 is the same claim for the mail Blueprint. Neither is about the
// unresolvable entry at all; they fail because Flux aborts the ENTIRE
// vcluster/apps Kustomization on one invalid doc, so a single bad cart id takes
// WordPress and its MySQL down with it (`inventory: 0` — the #5423 shape).
//
// So the property under test is not "the bad entry is dropped", it is "the GOOD
// entries survive alongside it".
func TestPerOrgAppsTree_UnresolvableEntryDoesNotSinkTheCart_5910(t *testing.T) {
	const unresolved = "0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"

	// Row 95 is literally "two Orgs, two TLDs, two running apps, identical
	// mechanism", so the second Org is a CONTROL that shares the suspect
	// property (a mixed cart) while differing in the one dimension the row
	// names — the pool TLD. Both must survive identically; a fix that worked
	// only on the primary pool would pass with one Org and fail row 95.
	orgs := []struct{ slug, tld string }{
		{"g95walkone", "omani.homes"},
		{"g95walktwo", "omani.trade"},
	}

	for _, org := range orgs {
		for _, plan := range []string{"s", "m"} {
			t.Run(org.slug+"/"+org.tld+"/plan="+plan, func(t *testing.T) {
				g := NewManifestGenerator("clusters/sov/org-tenants")
				g.ParentDomain = org.tld

				// The realistic funnel cart: a real purchase, its database, and
				// one entry whose catalog id never resolved.
				cart := []string{"wordpress", "mysql", unresolved}
				files, appDocs := g.GeneratePerOrgAppsTree(org.slug, plan, cart, "pw123")

				// 1. The paid-for app still renders — row 90's subject.
				wp, ok := files["vcluster/apps/app-wordpress.yaml"]
				if !ok {
					t.Fatalf("row 90: the purchased WordPress rendered NO manifest when the cart also "+
						"held an unresolvable id — the customer paid and nothing serves. keys: %v",
						keysOf(files))
				}
				// 2. It renders under THIS Org's chosen TLD — row 95's mechanism.
				// The app host must match the pool the per-Org console/listener
				// live on, or it resolves to a gateway holding no listener for it.
				wantHost := "wordpress." + org.slug + "." + org.tld
				if !strings.Contains(wp, wantHost) {
					t.Errorf("row 95: WordPress did not render under this Org's pool zone; want host %q "+
						"(console==apps invariant, #4999/#4421):\n%s", wantHost, wp)
				}
				// 3. Its database still renders — the co-installed sibling that
				// the whole-Kustomization abort used to take down with it.
				if _, ok := files["vcluster/apps/db-mysql.yaml"]; !ok {
					t.Errorf("#5910: the co-installed MySQL did not render, so WordPress would have no "+
						"database even if it applied. keys: %v", keysOf(files))
				}
				// 4. The unresolvable entry contributes nothing.
				if _, ok := files["vcluster/apps/app-"+unresolved+".yaml"]; ok {
					t.Errorf("#5910: the unresolvable entry still rendered a husk that invalidates the "+
						"whole apply")
				}
				// 5. The kustomization INDEX must not name it either. An index
				// entry with no file breaks the kustomize build outright (#4567),
				// which is the same total failure by another route.
				for _, d := range appDocs {
					if strings.Contains(d, unresolved) {
						t.Errorf("#5910: appDocs indexes %q, but no such file is emitted — a dangling "+
							"reference fails the whole kustomize build (#4567)", d)
					}
				}
				// 6. And the index MUST still carry the real app, so this test
				// cannot be satisfied by a generator that indexes nothing.
				indexed := false
				for _, d := range appDocs {
					if d == "app-wordpress.yaml" {
						indexed = true
					}
				}
				if !indexed {
					t.Errorf("VACUITY: app-wordpress.yaml is not in appDocs %v — the index assertion "+
						"above would pass on an empty list", appDocs)
				}
			})
		}
	}
}

// TestPerOrgHostAppRoutes_NoRouteForUnrenderableEntry_5910 pins the SECOND call
// site of the shared predicate. The vcluster tier emits a host-native HTTPRoute
// per Deployment-shaped app; for an entry that renders no Deployment and no
// Service, that route would bind `<id>-x-<slug>-x-vcluster` — a backend that can
// never exist — and sit ResolvedRefs=False forever.
//
// The control is in the same call: the real app in the same cart MUST still get
// its route, so this cannot pass by emitting no routes at all.
func TestPerOrgHostAppRoutes_NoRouteForUnrenderableEntry_5910(t *testing.T) {
	const unresolved = "0f8b2c41-9d3e-4a77-b512-6ac0e9f31d84"

	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.trade"

	// "m" is a vcluster-tier plan — the only tier that emits host-native routes.
	files, docs := g.GeneratePerOrgHostAppRoutes("g95walktwo", "m",
		[]string{"wordpress", unresolved})

	if _, ok := files[PerOrgHostAppsDir+"/app-wordpress-hostroute.yaml"]; !ok {
		t.Fatalf("CONTROL FAILED: the real app got no host-native route, so the absence assertion "+
			"below would pass on nothing. keys: %v", keysOf(files))
	}
	if _, ok := files[PerOrgHostAppsDir+"/app-"+unresolved+"-hostroute.yaml"]; ok {
		t.Errorf("#5910: a host-native HTTPRoute was emitted for an entry that renders no Service — "+
			"it can only ever report ResolvedRefs=False / BackendNotFound")
	}
	for _, d := range docs {
		if strings.Contains(d, unresolved) {
			t.Errorf("#5910: host-apps index names %q with no matching file — a dangling reference "+
				"breaks the whole kustomize build (#4567)", d)
		}
	}
}

// keysOf lives in rbac_test.go — shared across this package's tests.
