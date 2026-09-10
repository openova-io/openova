// every_plan_vcluster_test.go — every Organization is vCluster-backed (#4292).
//
// Founder direction 2026-09-10: "this is confusing why S and M are treated
// separately, I never like the ad hoc conditional approach - we are already
// very clear that the 1 SME customer must have 1 vcluster." The funnel used to
// carry a per-plan gate (BoundaryIsVcluster / allTiersVcluster, mirrored from
// the org-controller) that routed s/free/"" Orgs' apps straight into the host
// `<slug>` namespace with no kubeConfig and every other plan into a dedicated
// vCluster. That gate is gone: the org-controller renders the `vcluster`
// HelmRelease for every Organization, and this funnel reconciles every
// Organization's apps tree INTO it.
//
// This test replaces tier_gate_lockstep_4292_test.go (whose subject, the
// switch, no longer exists). It walks every plan slug — the CRD enum, the
// aliases, whitespace/case variants, the empty string and an unknown slug —
// through every seam the old gate used to branch on and asserts the vCluster
// shape at each one. If a plan-conditional creeps back into any of those
// seams, exactly one row of this table goes red and names the plan.
package gitops

import (
	"fmt"
	"strings"
	"testing"
)

// everyPlanSlug is the full input space the removed gate used to switch on:
// the CRD enum (s/m/l/xl/flexi), the "free" alias, the empty string, the
// case/whitespace variants the gate normalised, and an unknown slug (which
// the gate defaulted to vCluster — now every input is).
var everyPlanSlug = []string{"s", "m", "l", "xl", "flexi", "", "free", "S", "  s ", "enterprise-2027"}

// TestEveryPlanIsVclusterBacked_NoTierGate is the #4292 invariant, one plan per
// subtest. Refs #4292 #4297 #4293.
func TestEveryPlanIsVclusterBacked_NoTierGate(t *testing.T) {
	// Vacuity guard: an empty slug list would make the loop below prove
	// nothing while the test stayed green.
	if len(everyPlanSlug) == 0 {
		t.Fatal("vacuity: everyPlanSlug is empty — the per-plan loop would assert nothing")
	}

	const slug = "acme"
	cart := []string{"wordpress", "openclaw", "stalwart-mail"}
	mirror := fmt.Sprintf("name: tenant-%s-kubeconfig", slug)

	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = "omani.homes"

	// The HelmRelease-shaped set the generator writes files for (includes the
	// openclaw ⇒ newapi closure, UAT row 225). Vacuity guard on it too: the
	// per-file kubeConfig assertion below iterates this set.
	hrApps := helmReleaseAppsFor(cart)
	if len(hrApps) == 0 {
		t.Fatalf("vacuity: helmReleaseAppsFor(%v) is empty — the HR kubeConfig assertion would iterate nothing", cart)
	}

	// --- plan-blind seams, asserted once ---
	// These functions take no plan any more; that is the point. A per-plan
	// loop around them would re-call the same code and prove nothing per
	// slug, so they are pinned here exactly once.
	base := PerOrgAppsBaselineDocs()
	if !contains(base, "namespace.yaml") {
		t.Errorf("PerOrgAppsBaselineDocs() = %v — must carry namespace.yaml (the #4992 in-vcluster target-ns) for every Organization", base)
	}
	if !contains(base, "networkpolicy.yaml") {
		t.Errorf("PerOrgAppsBaselineDocs() = %v — must carry networkpolicy.yaml", base)
	}
	routeFiles, routeDocs := g.GeneratePerOrgHostAppRoutes(slug, []string{"wordpress"})
	if len(routeFiles) == 0 || len(routeDocs) == 0 {
		t.Errorf("GeneratePerOrgHostAppRoutes(%q, [wordpress]) = (%v, %v) — the host-native route is the only path from the host gateway to an in-vcluster app and must be emitted for every Organization", slug, keys(routeFiles), routeDocs)
	}
	hostDocs := PerOrgHostHelmReleaseAppDocs(cart)
	for _, a := range hrApps {
		if !contains(hostDocs, fmt.Sprintf("app-%s.yaml", a)) {
			t.Errorf("PerOrgHostHelmReleaseAppDocs(%v) = %v — must index app-%s.yaml (the HR docs live in host-apps for every Organization)", cart, hostDocs, a)
		}
	}

	// --- plan-taking seams, asserted per plan ---
	// GenerateAllWithAppConfigs / GeneratePerOrgAppsTree still take the plan
	// for QoS; the assertions below prove it no longer selects the boundary.
	var firstAppsSync string
	for _, plan := range everyPlanSlug {
		plan := plan
		t.Run(fmt.Sprintf("plan=%q", plan), func(t *testing.T) {
			out := g.GenerateAllWithAppConfigs(slug, plan, cart, "pw", nil)

			// 1. apps-sync carries the kubeConfig mirror.
			sync, ok := out[testBasePath+"/"+slug+"/apps-sync.yaml"]
			if !ok {
				t.Fatalf("apps-sync.yaml missing (keys: %v)", keys(out))
			}
			if !strings.Contains(sync, "kubeConfig:") {
				t.Errorf("apps-sync MUST carry kubeConfig — every Organization's apps tree is reconciled INTO its vCluster:\n%s", sync)
			}
			if !strings.Contains(sync, mirror) {
				t.Errorf("apps-sync MUST reference the kubeconfig mirror %q:\n%s", mirror, sync)
			}
			if !strings.Contains(sync, "key: config") {
				t.Errorf("apps-sync secretRef key must be `config`:\n%s", sync)
			}
			// The plan is not an input to the boundary: every plan renders
			// the same apps-sync, byte for byte.
			if firstAppsSync == "" {
				firstAppsSync = sync
			} else if sync != firstAppsSync {
				t.Errorf("apps-sync differs from plan %q's — the plan must not select the boundary:\n--- first ---\n%s\n--- this ---\n%s", everyPlanSlug[0], firstAppsSync, sync)
			}

			// 2. Every HelmRelease-shaped app file carries the same mirror.
			for _, a := range hrApps {
				path := testBasePath + "/" + slug + "/app-" + a + ".yaml"
				body, ok := out[path]
				if !ok {
					t.Errorf("%s missing (keys: %v)", path, keys(out))
					continue
				}
				if !strings.Contains(body, "kubeConfig:") || !strings.Contains(body, mirror) {
					t.Errorf("%s MUST carry the HR-level kubeConfig mirror %q — the host helm-controller installs it INTO the vCluster:\n%s", path, mirror, body)
				}
			}

			// 3. The per-Org tree re-roots the HR docs into host-apps and
			//    keeps them out of the apps index (#5423, now for every plan).
			files, appDocs := g.GeneratePerOrgAppsTree(slug, plan, cart, "pw")
			for _, a := range hrApps {
				name := "app-" + a + ".yaml"
				if _, ok := files[PerOrgHostAppsDir+"/"+name]; !ok {
					t.Errorf("%s must be re-rooted under %s (the apps Kustomization is kubeConfig-targeted at the CRD-less vCluster)", name, PerOrgHostAppsDir)
				}
				if _, ok := files[PerOrgAppsDir+"/"+name]; ok {
					t.Errorf("%s must NOT remain under %s", name, PerOrgAppsDir)
				}
				if contains(appDocs, name) {
					t.Errorf("appDocs %v must not index %s — its file is not in %s", appDocs, name, PerOrgAppsDir)
				}
			}
			// The customer's Deployment-shaped purchase stays in the apps tree.
			if _, ok := files[PerOrgAppsDir+"/app-wordpress.yaml"]; !ok {
				t.Errorf("app-wordpress.yaml must stay under %s (keys: %v)", PerOrgAppsDir, keys(files))
			}

			// 4. The merged apps index carries the in-vcluster target-ns.
			idx := MergePerOrgAppsKustomization("", nil, appDocs)
			if !strings.Contains(idx, "- namespace.yaml") {
				t.Errorf("merged apps index must carry namespace.yaml (Flux does not auto-create the vcluster target-ns; #5104):\n%s", idx)
			}
		})
	}
}
