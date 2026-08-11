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

import (
	"strings"
	"testing"
)

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

// TestIsolationForTier_DeclarationNeverOverridesTheTierGate replaces the former
// TestIsolationForTier_ExplicitOverrideStillWins, which pinned an
// "advanced-operator escape hatch" that never escaped anything (#6135, UAT row
// G7). The hatch let a declared `isolation` win over the tier gate in
// resolveOrgShape while `boundaryIsVcluster(planSlug)` — which takes the plan
// and nothing else — went on authoring the host `<slug>` namespace. Measured on
// hw293 (dep a0077ba47e3720e5): 202, `isolation: vcluster`, no vCluster.
//
// The declaration is now an ASSERTION, adjudicated at the door. Both halves
// are pinned here so the change cannot be read as either "declarations are
// ignored" or "declarations still steer the boundary".
func TestIsolationForTier_DeclarationNeverOverridesTheTierGate(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"customer", "internal"} {
		got := resolveOrgShape(orgTenantCreateRequest{
			Kind: kind, PlanSlug: "s", Isolation: "vcluster",
		})
		if got.Isolation != "namespace" {
			t.Errorf("kind %s: resolved isolation %q for plan s — a declaration steered the "+
				"boundary again, and the org-controller will still author a host namespace "+
				"(gitops.BoundaryIsVcluster(\"s\") == false)", kind, got.Isolation)
		}
	}
}

// TestDeclaredIsolationConflict_RefusesOnlyTheUndeliverable pins the door's
// adjudicator. The CONTROL cases share the suspect property — they all carry a
// non-empty declared `isolation` — and stay accepted, so the guard is a new
// constraint on the undeliverable combination rather than a blanket rejection
// of the field.
func TestDeclaredIsolationConflict_RefusesOnlyTheUndeliverable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		declared     string
		planSlug     string
		wantConflict bool
	}{
		// The measured row: the signup door has no plan picker, so the plan
		// resolves to "s" and a declared vcluster cannot be delivered.
		{"row G7: vcluster declared on plan s", "vcluster", "s", true},
		{"vcluster declared on plan s, mixed case + padding", "  VCluster ", "s", true},
		{"namespace declared on plan m", "namespace", "m", true},
		{"unrecognised enum on plan s", "dedicated-cluster", "s", true},

		// CONTROLS — a declaration is present in every one of these.
		{"vcluster declared on plan m (agrees)", "vcluster", "m", false},
		{"vcluster declared on plan flexi (agrees)", "vcluster", "flexi", false},
		{"namespace declared on plan s (agrees)", "namespace", "s", false},
		// The marketplace funnel's own body: no declaration, nothing to refuse.
		{"omitted declaration on plan s", "", "s", false},
		{"omitted declaration on plan xl", "", "xl", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail, conflict := declaredIsolationConflict(tc.declared, tc.planSlug)
			if conflict != tc.wantConflict {
				t.Fatalf("declaredIsolationConflict(%q, %q) conflict = %v, want %v",
					tc.declared, tc.planSlug, conflict, tc.wantConflict)
			}
			if !conflict {
				if detail != "" {
					t.Fatalf("no conflict but detail = %q, want empty", detail)
				}
				return
			}
			// Assert on the VALUE of the message, not on its presence: a
			// refusal the caller cannot act on is the silent downgrade with
			// extra steps. It must name what was asked, what the plan gives,
			// and the way out.
			for _, want := range []string{
				strings.ToLower(strings.TrimSpace(tc.declared)),
				tc.planSlug,
				isolationForTier(tc.planSlug),
				"Omit `isolation`",
			} {
				if !strings.Contains(detail, want) {
					t.Errorf("422 detail %q does not name %q — the caller cannot tell what they "+
						"were refused or how to proceed", detail, want)
				}
			}
		})
	}
}

// TestPlansDeliveringIsolation_ComesFromTheGate is the VACUITY CHECK for the
// caller-facing plan list: it must be COMPUTED from isolationForTier, not
// transcribed. A hand-written list would keep naming the same plans after a
// policy flip and hand every refused caller a wrong instruction.
//
// It fails if the list is empty, if it names a plan the gate contradicts, or if
// it omits a plan the gate qualifies — so it cannot pass on a stub.
func TestPlansDeliveringIsolation_ComesFromTheGate(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"namespace", "vcluster"} {
		got := plansDeliveringIsolation(want)
		if len(got) == 0 {
			t.Fatalf("plansDeliveringIsolation(%q) = [] — the 422 message would tell the "+
				"caller no plan delivers a boundary the gate does deliver", want)
		}
		named := map[string]bool{}
		for _, p := range got {
			named[p] = true
			if isolationForTier(p) != want {
				t.Errorf("plansDeliveringIsolation(%q) named plan %q, whose gate answer is %q",
					want, p, isolationForTier(p))
			}
		}
		for _, p := range catalogPlanSlugs {
			if isolationForTier(p) == want && !named[p] {
				t.Errorf("plansDeliveringIsolation(%q) omitted plan %q, which the gate DOES "+
					"deliver — a refused caller is steered away from a plan that would work",
					want, p)
			}
		}
	}
}
