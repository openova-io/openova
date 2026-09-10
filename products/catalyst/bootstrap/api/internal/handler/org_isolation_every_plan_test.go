// org_isolation_every_plan_test.go — every Organization on every plan is
// backed by a dedicated vCluster (founder direction 2026-09-10; Refs #4292
// #4539 #6135).
//
// This replaces tier_gate_lockstep_4292_test.go, whose subject — a plan-keyed
// "tier gate" mirrored across three modules and pinned to one truth table —
// no longer exists. The boundary keys off neither `kind` nor `planSlug`; the
// plan drives only the ResourceQuota/LimitRange/QoS inside the vCluster. These
// tests pin that no second gate creeps back into the catalyst-api half:
//
//   - resolveOrgShape answers `vcluster` for every plan the catalog knows, for
//     the non-catalog inputs the door normalises, and for BOTH kinds — while
//     kind still drives billingMode (the control that proves kind is not
//     simply inert).
//   - declaredIsolationConflict refuses anything but `vcluster` on every plan
//     and names what it refused.
//   - plansDeliveringIsolation is COMPUTED from the constant, so a refused
//     caller is told the truth: every plan delivers `vcluster`, none delivers
//     anything else.
package handler

import (
	"fmt"
	"strings"
	"testing"
)

// everyPlanInput is every catalog plan plus the non-catalog inputs the create
// door normalises: an omitted plan, the funnel's "free", upper case, padding,
// and a slug no catalog has ever carried. Fails the calling test outright on
// an empty catalog, so the sweeps below can never pass on nothing.
func everyPlanInput(t *testing.T) []string {
	t.Helper()
	if len(catalogPlanSlugs) == 0 {
		t.Fatal("catalogPlanSlugs is empty — this sweep would assert nothing")
	}
	return append(append([]string(nil), catalogPlanSlugs...),
		"", "free", "S", "  m ", "enterprise-2027")
}

// TestResolveOrgShape_EveryPlanEveryKindIsVClusterBacked is the gate that
// cannot creep back: a resolver that answers anything but `vcluster` for ANY
// plan or kind fails, and so does one that stops letting kind drive billing.
func TestResolveOrgShape_EveryPlanEveryKindIsVClusterBacked(t *testing.T) {
	t.Parallel()
	if orgIsolation != "vcluster" {
		t.Fatalf("orgIsolation = %q; the one boundary every Organization gets is a dedicated vCluster", orgIsolation)
	}
	inputs := everyPlanInput(t)
	if len(inputs) <= len(catalogPlanSlugs) {
		t.Fatal("the sweep carries no non-catalog input — the normalisation path is untested")
	}
	for _, plan := range inputs {
		for _, kind := range []string{"customer", "internal"} {
			t.Run(kind+"/plan="+strings.TrimSpace(plan), func(t *testing.T) {
				got := resolveOrgShape(orgTenantCreateRequest{Kind: kind, PlanSlug: plan})
				if got.Isolation != orgIsolation {
					t.Fatalf("resolveOrgShape(kind=%q, plan=%q).Isolation = %q, want %q — "+
						"a plan- or kind-keyed boundary gate is back; every Organization is "+
						"backed by a dedicated vCluster", kind, plan, got.Isolation, orgIsolation)
				}
				// CONTROL — kind still drives billingMode, so this test is not
				// simply asserting that the request is ignored.
				wantBilling := "real"
				if kind == "internal" {
					wantBilling = "showback"
				}
				if got.BillingMode != wantBilling {
					t.Fatalf("kind %q: billingMode = %q, want %q — kind must keep driving the "+
						"billing dimension; only the boundary stopped reading it",
						kind, got.BillingMode, wantBilling)
				}
				// And the plan still normalises onto the catalog: the quota
				// input survives even though the boundary no longer reads it.
				if !isCatalogPlanSlug(got.PlanSlug) {
					t.Fatalf("plan %q resolved to non-catalog slug %q", plan, got.PlanSlug)
				}
			})
		}
	}
}

// TestDeclaredIsolationConflict_OnlyVClusterAgrees_EveryPlan pins the door's
// adjudicator across every plan: an omitted declaration and `vcluster` are
// never refused; `namespace` and an unrecognised enum are ALWAYS refused, and
// the refusal names what was asked, the plan, the boundary every plan
// delivers, and the way out.
func TestDeclaredIsolationConflict_OnlyVClusterAgrees_EveryPlan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		declared     string
		wantConflict bool
	}{
		{"", false},
		{"vcluster", false},
		{"  VCluster ", false},
		{"namespace", true},
		{"  Namespace ", true},
		{"dedicated-cluster", true},
	}
	sawRefusal, sawAcceptance := false, false
	for _, plan := range everyPlanInput(t) {
		for _, tc := range cases {
			detail, conflict := declaredIsolationConflict(tc.declared, plan)
			if conflict != tc.wantConflict {
				t.Errorf("declaredIsolationConflict(%q, %q) conflict = %v, want %v",
					tc.declared, plan, conflict, tc.wantConflict)
				continue
			}
			if !conflict {
				sawAcceptance = true
				if detail != "" {
					t.Errorf("declaredIsolationConflict(%q, %q): no conflict but detail = %q",
						tc.declared, plan, detail)
				}
				continue
			}
			sawRefusal = true
			// Assert on the VALUE of the message, not on its presence: a
			// refusal the caller cannot act on is the silent downgrade with
			// extra steps.
			for _, want := range []string{
				strings.ToLower(strings.TrimSpace(tc.declared)),
				fmt.Sprintf("%q", plan),
				orgIsolation,
				"dedicated vCluster",
				"No catalog plan delivers",
				"Omit `isolation`",
			} {
				if !strings.Contains(detail, want) {
					t.Errorf("422 detail for (%q, %q) = %q does not name %q",
						tc.declared, plan, detail, want)
				}
			}
			if strings.Contains(detail, "Plans that deliver") {
				t.Errorf("422 detail for (%q, %q) names plans that deliver %q, but none does: %q",
					tc.declared, plan, tc.declared, detail)
			}
		}
	}
	if !sawRefusal || !sawAcceptance {
		t.Fatalf("sweep exercised refusal=%v acceptance=%v — both must occur for this to guard anything",
			sawRefusal, sawAcceptance)
	}
}

// TestPlansDeliveringIsolation_EveryPlanDeliversVClusterNoneDeliversNamespace
// is the VACUITY CHECK for the caller-facing plan list: it is computed from
// orgIsolation over catalogPlanSlugs, so `vcluster` names every catalog plan
// and anything else names none.
func TestPlansDeliveringIsolation_EveryPlanDeliversVClusterNoneDeliversNamespace(t *testing.T) {
	t.Parallel()
	got := plansDeliveringIsolation("vcluster")
	if len(got) == 0 {
		t.Fatal("plansDeliveringIsolation(\"vcluster\") = [] — the 422 message would tell the caller " +
			"no plan delivers the one boundary every plan delivers")
	}
	if strings.Join(got, ",") != strings.Join(catalogPlanSlugs, ",") {
		t.Fatalf("plansDeliveringIsolation(\"vcluster\") = %v, want every catalog plan %v", got, catalogPlanSlugs)
	}
	// The returned slice must be a copy: a caller that sorts or appends must
	// not mutate the catalog.
	got[0] = "mutated"
	if catalogPlanSlugs[0] == "mutated" {
		t.Fatal("plansDeliveringIsolation returned the catalog slice itself, not a copy")
	}
	for _, other := range []string{"namespace", "", "dedicated-cluster"} {
		if alt := plansDeliveringIsolation(other); len(alt) != 0 {
			t.Errorf("plansDeliveringIsolation(%q) = %v, want none — no catalog plan delivers "+
				"anything but a dedicated vCluster", other, alt)
		}
	}
}
