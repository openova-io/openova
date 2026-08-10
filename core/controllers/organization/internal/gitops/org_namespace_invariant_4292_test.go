// org_namespace_invariant_4292_test.go — UAT row 106.
//
// The row as originally authored read: "Organization namespace count equals the
// number of Organizations with HOST-TIER backing — no orphan namespace, no
// missing one." That clause is arithmetically unsatisfiable on any mixed-tier
// Sovereign, and this file is the source evidence for why.
//
// Render() emits `vcluster/namespace.yaml` UNCONDITIONALLY — it is seeded into
// the file set before the tier gate is consulted, and only
// `vcluster/vcluster.yaml` sits behind boundaryIsVcluster(). So a vCluster-tier
// Organization has a host namespace TOO: the namespace is where its vCluster
// HelmRelease, its plan-quota/plan-limits pair, its CNP and its provisioning
// RBAC all live, and it is the namespace the syncer mirrors the Org's pods
// into. Org-labelled namespace count therefore equals TOTAL Organization
// count, not host-tier count, and the two coincide only on an all-host estate.
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
		// `vcluster/apps/` is excluded on purpose. For a vcluster-tier Org the
		// per-Org apps Kustomization carries spec.kubeConfig, so
		// vcluster/apps/namespace.yaml is created INSIDE that Org's vCluster
		// apiserver (per_org_flux.go, #4991) — it is invisible to a host-side
		// `kubectl get ns` and counting it would inflate every vcluster-tier
		// Org to two. Verified by this very assertion: scoping it wrongly made
		// the m/l/xl/flexi renders report 2.
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
		// Control on the exclusion itself: the vcluster tier DOES author an
		// in-vCluster namespace, and the host tier does not. Without this the
		// filter above could be hiding a real second host namespace.
		_, hasInVcluster := out["vcluster/apps/"+appsNamespaceDoc]
		if wantInVcluster := BoundaryIsVcluster(plan); hasInVcluster != wantInVcluster {
			t.Errorf("plan %q: in-vCluster apps namespace present=%v, want %v",
				plan, hasInVcluster, wantInVcluster)
		}

		// (2) Its identity is the Org slug, on BOTH the name and the join
		// label the walker counts by. Asserting only presence would pass on a
		// namespace labelled for a different Organization.
		raw, ok := out["vcluster/namespace.yaml"]
		if !ok {
			t.Fatalf("plan %q: no vcluster/namespace.yaml — the boundary namespace "+
				"is NOT tier-gated and must render for every plan", plan)
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

// TestRender_HostNamespaceIsNotTierGated states the negative half of row 106
// directly: the tier gate moves exactly ONE file, and it is not the namespace.
//
// Without this, a future edit could put namespace.yaml behind the gate and the
// test above would still pass for every plan that happens to be on the
// rendering side of it.
func TestRender_HostNamespaceIsNotTierGated(t *testing.T) {
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
	hostTier := render("s")     // boundaryIsVcluster == false
	vclusterTier := render("m") // boundaryIsVcluster == true

	// The file sets differ ONLY by vcluster.yaml and the in-vcluster apps
	// namespace doc. Anything else differing means the gate grew a second
	// consequence nobody declared.
	expectedExtra := map[string]bool{
		"vcluster/vcluster.yaml":            true,
		"vcluster/apps/" + appsNamespaceDoc: true,
	}
	for path := range vclusterTier {
		if _, inHost := hostTier[path]; inHost {
			continue
		}
		if !expectedExtra[path] {
			t.Errorf("the tier gate also adds %q for the vcluster tier — row 106's "+
				"namespace arithmetic assumes the gate moves vcluster.yaml (plus the "+
				"in-vcluster apps namespace) and NOTHING else", path)
		}
	}
	for path := range hostTier {
		if _, inVcluster := vclusterTier[path]; !inVcluster {
			t.Errorf("the host tier renders %q that the vcluster tier does not — the "+
				"boundary tree must be a superset in the vcluster direction", path)
		}
	}
	// Vacuity guard: if the two renders were identical the loops above would
	// pass while proving nothing about the gate.
	if len(vclusterTier) == len(hostTier) {
		t.Fatalf("host tier and vcluster tier rendered the same %d files — the tier "+
			"gate is inert and neither loop above asserted anything", len(hostTier))
	}
	// And the one file whose ABSENCE defines the host tier is absent.
	if _, bad := hostTier["vcluster/vcluster.yaml"]; bad {
		t.Error("plan s rendered vcluster/vcluster.yaml — the free/S tier must have NO vCluster")
	}
	// The namespace is present on BOTH sides — the row-106 point.
	for name, out := range map[string]map[string][]byte{"s": hostTier, "m": vclusterTier} {
		if _, ok := out["vcluster/namespace.yaml"]; !ok {
			t.Errorf("plan %s: host namespace missing", name)
		}
	}
	// Sanity: the kustomization lists the namespace on both sides too, so the
	// namespace is not merely rendered-but-unreferenced bytes.
	for name, out := range map[string]map[string][]byte{"s": hostTier, "m": vclusterTier} {
		kz := string(out["vcluster/kustomization.yaml"])
		if !strings.Contains(kz, "- namespace.yaml") {
			t.Errorf("plan %s: kustomization does not list namespace.yaml:\n%s", name, kz)
		}
	}
}
