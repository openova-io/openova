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
			// CHARACTERISATION: these assertions pin the CURRENT broken output on
			// purpose. When the fix lands upstream in resolveAppSlugs, this test
			// SHOULD go red and be rewritten — that failure is the signal, not a
			// regression.
			husk, ok := files["vcluster/apps/app-"+unresolved+".yaml"]
			if !ok {
				t.Fatalf("#5910: expected a Deployment rendered for the unresolved id "+
					"(that is the defect being pinned); keys: %v", keysOf(files))
			}
			if !strings.Contains(husk, "containerPort: 0") {
				t.Errorf("#5910: expected the invalid `containerPort: 0` this defect produces — "+
					"if this no longer renders, the upstream fix may have landed and this "+
					"characterisation test must be rewritten:\n%s", husk)
			}
			if !strings.Contains(husk, "\n          image: \n") &&
				!strings.Contains(husk, "\n                  image: \n") &&
				!strings.Contains(husk, "image:\n") {
				t.Errorf("#5910: expected a bare/null `image:` from the empty AppSpec:\n%s", husk)
			}
			t.Logf("#5910 CONFIRMED at the render seam: unresolved id renders an UNAPPLIABLE "+
				"Deployment (null image, containerPort 0), which per #4389 fails the whole "+
				"vcluster/apps Kustomization — taking co-installed apps down with it")
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

// keysOf lives in rbac_test.go — shared across this package's tests.
