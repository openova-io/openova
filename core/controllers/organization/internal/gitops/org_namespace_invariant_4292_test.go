// org_namespace_invariant_4292_test.go — UAT row 106.
//
// The row as originally authored read: "Organization namespace count equals the
// number of Organizations with HOST-TIER backing — no orphan namespace, no
// missing one." That clause is arithmetically unsatisfiable on any mixed-tier
// Sovereign, and this file is the source evidence for why.
//
// Render() emits `vcluster/namespace.yaml` for every Organization, and since
// 2026-09-10 it emits `vcluster/vcluster.yaml` for every Organization too —
// the #4292 tier gate that kept free/S on the bare namespace is gone. The host
// namespace is where the Org's vCluster HelmRelease, its plan-quota/plan-limits
// pair, its CNP and its provisioning RBAC all live, and it is the namespace the
// syncer mirrors the Org's pods into. Org-labelled namespace count therefore
// equals TOTAL Organization count.
//
// The invariant the row was reaching for — and the one this file pins — is:
//
//	EVERY Organization, at EVERY tier, owns EXACTLY ONE host namespace, named
//	after its slug and carrying openova.io/organization=<slug>.
//
// That is the property that makes "no orphan namespace, no missing one"
// checkable: the walker counts org-labelled namespaces and compares to the
// count of Organization CRs, with no tier partition in between.
//
// The re-authored clause is recorded in docs/ledger/uat-retirements.csv.
package gitops

import (
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestRender_EveryTierOwnsExactlyOneOrgNamespace walks the full CRD planSlug
// enum plus the two non-enum inputs the gate accepts, and an unknown slug.
func TestRender_EveryTierOwnsExactlyOneOrgNamespace(t *testing.T) {
	t.Parallel()
	// products/catalyst/chart/crds/organization.yaml spec.planSlug enum, plus
	// "" (a legacy CR with no planSlug), "free", and an unrecognised slug.
	slugs := []string{"", "free", "s", "m", "l", "xl", "flexi", "enterprise-2027"}

	for _, plan := range slugs {
		out, err := Render(Inputs{
			Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: plan,
			SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*",
		})
		if err != nil {
			t.Fatalf("Render(plan=%q): %v", plan, err)
		}

		// (1) Exactly ONE HOST namespace. The tally row 106 compares against
		// the Organization count is what `kubectl get ns -l
		// openova.io/organization` returns on the HOST cluster, so the count
		// is taken over the host-applied trees only:
		//
		//	vcluster/            boundary tree, host-applied
		//	vcluster/host-apps/  CNP + provisioning RBAC, ALWAYS host-applied
		//
		// `vcluster/apps/` is excluded on purpose. For every Organization the
		// per-Org apps Kustomization carries spec.kubeConfig, so
		// vcluster/apps/namespace.yaml is created INSIDE that Org's vCluster
		// apiserver (per_org_flux.go, #4991) — it is invisible to a host-side
		// `kubectl get ns` and counting it would inflate every Org to two.
		// Verified by this very assertion: scoping it wrongly made the renders
		// report 2.
		hostApplied := func(path string) bool {
			return !strings.HasPrefix(path, "vcluster/apps/")
		}
		nsDocs := 0
		for path, raw := range out {
			if !hostApplied(path) {
				continue
			}
			var probe struct {
				Kind string `json:"kind"`
			}
			if err := yaml.Unmarshal(raw, &probe); err != nil {
				// kustomization.yaml has no `kind`; that is not a failure.
				continue
			}
			if probe.Kind == "Namespace" {
				nsDocs++
				if path != "vcluster/namespace.yaml" {
					t.Errorf("plan %q: a SECOND host Namespace is authored at %q — every "+
						"Organization must own exactly one host namespace", plan, path)
				}
			}
		}
		if nsDocs != 1 {
			t.Errorf("plan %q: Render emitted %d host Namespace documents, want exactly 1 "+
				"(UAT row 106: one host namespace per Organization, every tier)",
				plan, nsDocs)
		}
		// Control on the exclusion itself: every Organization DOES author an
		// in-vCluster namespace under vcluster/apps/. Without this the filter
		// above could be hiding a real second host namespace.
		if _, hasInVcluster := out["vcluster/apps/"+appsNamespaceDoc]; !hasInVcluster {
			t.Errorf("plan %q: in-vCluster apps namespace missing — every Organization authors one", plan)
		}

		// (2) Its identity is the Org slug, on BOTH the name and the join
		// label the walker counts by. Asserting only presence would pass on a
		// namespace labelled for a different Organization.
		raw, ok := out["vcluster/namespace.yaml"]
		if !ok {
			t.Fatalf("plan %q: no vcluster/namespace.yaml — the boundary namespace "+
				"must render for every plan", plan)
		}
		var ns struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal(raw, &ns); err != nil {
			t.Fatalf("plan %q: namespace.yaml did not parse: %v", plan, err)
		}
		if ns.Metadata.Name != "acme" {
			t.Errorf("plan %q: namespace name = %q, want the Org slug %q",
				plan, ns.Metadata.Name, "acme")
		}
		if got := ns.Metadata.Labels["openova.io/organization"]; got != "acme" {
			t.Errorf("plan %q: openova.io/organization = %q, want %q — this label is "+
				"the join key the namespace tally and the per-Org showback both "+
				"count by, so a wrong VALUE is an orphan namespace and a missing "+
				"one at the same time", plan, got, "acme")
		}
	}
}

// TestRender_BoundaryFileSetIsPlanIndependent states the negative half of row
// 106 in its one-boundary form (founder 2026-09-10): the plan slug moves
// NOTHING in the boundary file set except `vcluster/resourcequota.yaml`, which
// is absent for the soft-cap Flexi plan alone (PlanRendersResourceQuota). Every
// other file — the vCluster HelmRelease and the in-vCluster apps namespace
// included — renders for every plan.
//
// This is the anti-creep guard: an edit that puts vcluster.yaml (or the apps
// namespace) back behind a plan check produces a file-set difference between
// two plans, and that difference is exactly what fails here. It is deliberately
// NOT written as "s and m render the same files", which would pass again the
// moment a THIRD plan were gated.
func TestRender_BoundaryFileSetIsPlanIndependent(t *testing.T) {
	t.Parallel()
	render := func(plan string) map[string][]byte {
		out, err := Render(Inputs{
			Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: plan,
			SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*",
		})
		if err != nil {
			t.Fatalf("Render(plan=%q): %v", plan, err)
		}
		return out
	}
	pathsOf := func(out map[string][]byte) []string {
		paths := make([]string, 0, len(out))
		for p := range out {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return paths
	}
	without := func(paths []string, drop string) []string {
		kept := make([]string, 0, len(paths))
		for _, p := range paths {
			if p != drop {
				kept = append(kept, p)
			}
		}
		return kept
	}

	reference := pathsOf(render("s"))
	// Vacuity guards: the reference set must contain the files the removed
	// tier gate used to move, or the comparison below proves nothing about
	// them; and the Flexi exception must be real, or its branch is inert.
	for _, must := range []string{"vcluster/vcluster.yaml", "vcluster/resourcequota.yaml", "vcluster/apps/" + appsNamespaceDoc} {
		found := false
		for _, p := range reference {
			found = found || p == must
		}
		if !found {
			t.Fatalf("plan s does not render %q — the plan-independence check below would be vacuous", must)
		}
	}
	if PlanRendersResourceQuota("flexi") {
		t.Fatal("flexi renders a ResourceQuota — the one declared plan difference does not exist, so this test would assert identity by accident")
	}

	for _, plan := range []string{"", "free", "s", "S", "m", "l", "xl", "flexi", "enterprise-2027"} {
		want := reference
		if !PlanRendersResourceQuota(plan) {
			want = without(reference, "vcluster/resourcequota.yaml")
		}
		got := pathsOf(render(plan))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("plan %q renders a different boundary file set than plan s — the plan must size the boundary, never select it\n got: %v\nwant: %v",
				plan, got, want)
		}
	}
}
