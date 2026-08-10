// tier_gate_lockstep_4292_test.go — the #4292 TIER GATE lockstep contract for
// the catalyst-api surface (UAT row 100).
//
// Row 100 asserts: "an Organization on plan free or S is backed by a HOST
// namespace and has NO vCluster — the #4292 tier gate; the null is proven
// against a control Org that does have one."
//
// The gate that AUTHORS that backing is
// core/controllers/organization/internal/gitops/manifests.go
// `boundaryIsVcluster(planSlug string) bool` — one argument, planSlug. It never
// reads spec.kind. Two more copies of the same policy exist because Go forbids
// importing another module's internal/ package:
//
//	core/services/provisioning/gitops/gitops.go                 BoundaryIsVcluster
//	products/.../internal/handler/organization_provisioning.go  isolationForTier
//
// This file pins the catalyst-api copy to the same truth table by literal — the
// pattern placement_vocabulary_drift_test.go already uses for the placement
// vocabulary, for the same cross-module reason.
package handler

import "testing"

// tierGateHostNS is the exact set of plan slugs that back a HOST NAMESPACE. It
// is the literal copy of the `case "", "s", "free":` arm in boundaryIsVcluster.
// Everything the CRD enum allows outside this set (m/l/xl/flexi) is
// vCluster-backed.
//
// The CRD enum (products/catalyst/chart/crds/organization.yaml, spec.planSlug)
// is [s, m, l, xl, flexi]; "" and "free" are the two non-enum inputs the gate is
// documented to accept (a legacy CR with no planSlug, and the free tier the
// funnel may not have resolved to a catalog slug yet).
var tierGateHostNS = map[string]bool{
	"":      true,
	"free":  true,
	"s":     true,
	"m":     false,
	"l":     false,
	"xl":    false,
	"flexi": false,
}

// TestIsolationForTier_MatchesAuthoritativeGate walks the FULL input domain of
// the authoritative gate and asserts the catalyst-api label agrees on every one.
// A partial table is what let the divergence below live: the pre-existing table
// sampled `internal S` and `internal M` but never asked whether kind should be
// an input at all.
func TestIsolationForTier_MatchesAuthoritativeGate(t *testing.T) {
	t.Parallel()
	if len(tierGateHostNS) == 0 {
		t.Fatal("empty truth table — this test would assert nothing")
	}
	for slug, wantHostNS := range tierGateHostNS {
		want := "vcluster"
		if wantHostNS {
			want = "namespace"
		}
		if got := isolationForTier(slug); got != want {
			t.Errorf("isolationForTier(%q) = %q, want %q (must match "+
				"gitops.boundaryIsVcluster(%q)=%v in "+
				"core/controllers/organization/internal/gitops/manifests.go)",
				slug, got, want, slug, !wantHostNS)
		}
	}
}

// TestIsolationForTier_IgnoresKind is the row-100 regression gate.
//
// #4539 added `if kind == "internal" { return "namespace" }` to
// isolationForTier. The org-controller has no such branch —
// gitops.boundaryIsVcluster takes planSlug and nothing else — so an internal Org
// on plan m/l/xl/flexi got a real vCluster HelmRelease rendered into its
// boundary tree while the BSS create response AND the Organization directory
// (org_list_from_cr.go) both reported isolation="namespace". The label
// contradicted the backing, which is the same class of defect #4813 (Ready
// message) and #5489 (status.vcluster stamp) each fixed on their own surface.
//
// The CRD settles which side was wrong. products/catalyst/chart/crds/
// organization.yaml documents spec.kind as: "Customer Orgs (kind=customer) and
// internal-team Orgs (kind=internal) share the SAME shape and the SAME code
// path. Difference is the billingMode dimension only." So kind selects billing;
// it must not select the boundary primitive.
//
// Live relevance to row 100: on hw292 the ONLY namespace-backed Organization is
// `hw292-omani-works`, which is namespace-backed because kind=internal, not
// because its plan is free/S. That is exactly the short-circuit under test —
// with it removed, "namespace-backed" means "free/S plan" and nothing else, so
// the clause has a subject that can actually be instantiated.
func TestIsolationForTier_IgnoresKind(t *testing.T) {
	t.Parallel()
	// The helper no longer TAKES a kind, so the compiler enforces most of this.
	// What the compiler cannot enforce is that resolveOrgShape stops feeding
	// kind back in through a side door, which is what this walks.
	for slug := range tierGateHostNS {
		if slug == "" {
			// resolveOrgShape clamps an empty/invalid slug to "s"; the ""
			// input is exercised on the helper above and on the CR read path
			// (org_list_from_cr.go) where an unset spec.planSlug reaches the
			// helper verbatim.
			continue
		}
		customer := resolveOrgShape(orgTenantCreateRequest{Kind: "customer", PlanSlug: slug})
		internal := resolveOrgShape(orgTenantCreateRequest{Kind: "internal", PlanSlug: slug})
		if customer.Isolation != internal.Isolation {
			t.Errorf("plan %q: isolation differs by kind (customer=%q internal=%q) — "+
				"the org-controller gate that authors the backing never reads "+
				"spec.kind, so a kind-dependent label is a label that lies",
				slug, customer.Isolation, internal.Isolation)
		}
		// And the shared value is the one the authoritative gate implies.
		want := "vcluster"
		if tierGateHostNS[slug] {
			want = "namespace"
		}
		if internal.Isolation != want {
			t.Errorf("internal Org on plan %q: isolation = %q, want %q",
				slug, internal.Isolation, want)
		}
		// Control — kind DOES still drive billingMode, so this test is not
		// simply asserting that kind is inert everywhere.
		if internal.BillingMode == customer.BillingMode {
			t.Errorf("plan %q: kind stopped driving billingMode (both %q); the fix "+
				"was meant to remove kind from the ISOLATION decision only",
				slug, internal.BillingMode)
		}
	}
}

// TestIsolationForTier_ExplicitOverrideStillWins pins the advanced-operator
// escape hatch, so the fix above cannot be mistaken for "isolation is now
// unconditional". An explicit valid isolation in the request is still honoured
// for either kind.
func TestIsolationForTier_ExplicitOverrideStillWins(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"customer", "internal"} {
		got := resolveOrgShape(orgTenantCreateRequest{
			Kind: kind, PlanSlug: "s", Isolation: "vcluster",
		})
		if got.Isolation != "vcluster" {
			t.Errorf("kind %s: explicit isolation override dropped, got %q", kind, got.Isolation)
		}
	}
}
