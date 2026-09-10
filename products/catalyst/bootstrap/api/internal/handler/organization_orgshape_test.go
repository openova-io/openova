// organization_orgshape_test.go — coverage for resolveOrgShape, the
// Organizations internal-door defaulting (issue #3378 B1). Locks the §2.1/§2.3
// model: kind defaults to customer (the funnel door); kind-derived billingMode
// default; isolation is the ONE boundary every Organization gets — a dedicated
// vCluster on every plan and for both kinds (orgIsolation; founder direction
// 2026-09-10, Refs #4292 #4539 #6135); the plan normalises onto the catalog
// and drives only the quota; and the malformed-enum fallback that keeps a bad
// body from stamping a nonsense shape.
//
// The plan-keyed tier gate this file used to pin (free/S → namespace, M+ →
// vcluster) is gone; org_isolation_every_plan_test.go sweeps the full input
// domain. The cases here keep the per-field defaulting readable.
package handler

import "testing"

func TestResolveOrgShape(t *testing.T) {
	tests := []struct {
		name string
		in   orgTenantCreateRequest
		want orgShape
	}{
		{
			// #4292: an omitted plan slug defaults to "s" (smallest paid cap),
			// never empty — the org-controller must always materialize a quota.
			// The boundary is a dedicated vCluster regardless of that plan.
			name: "omitted kind defaults to the customer funnel shape — S plan, vcluster",
			in:   orgTenantCreateRequest{},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			// kind drives billing only; the boundary is the same vCluster.
			name: "internal door → showback billing, vcluster boundary",
			in:   orgTenantCreateRequest{Kind: "internal"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "explicit customer at default S plan → real + vcluster",
			in:   orgTenantCreateRequest{Kind: "customer"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "customer M plan → dedicated vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "m"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			name: "customer XL plan → dedicated vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "xl"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "xl"},
		},
		{
			// "free" is not a quota slug → planSlug falls back to "s"; the
			// boundary does not read the plan at all.
			name: "customer free plan → S quota, vcluster boundary",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "free"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			// #6135 (UAT row G7) — a declaration is never an input to the
			// resolver. `vcluster` on an S plan AGREES with the boundary every
			// plan delivers and resolves to exactly that; the resolver would
			// answer the same without the declaration, which is the point:
			// ONE producer for the boundary.
			name: "declared vcluster on an S plan resolves to vcluster (agrees, not overrides)",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "s", Isolation: "vcluster"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "declared vcluster on an internal org resolves to vcluster",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "chargeback", Isolation: "vcluster"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "chargeback", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			// A declared `namespace` is refused at the door with 422 by
			// declaredIsolationConflict and never reaches this resolver in
			// production; if it did, the resolver still answers the constant
			// — the declaration is not honoured anywhere.
			name: "declared namespace does NOT steer the resolver",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "m", Isolation: "namespace"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			name: "corporate tier honored",
			in:   orgTenantCreateRequest{Kind: "internal", Tier: "corporate"},
			want: orgShape{Kind: "internal", Tier: "corporate", BillingMode: "showback", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "case-insensitive enum normalization (M plan)",
			in:   orgTenantCreateRequest{Kind: "CUSTOMER", Tier: "Corporate", BillingMode: "Real", PlanSlug: "M"},
			want: orgShape{Kind: "customer", Tier: "corporate", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			name: "malformed kind falls back to the customer S shape",
			in:   orgTenantCreateRequest{Kind: "garbage"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			// malformed billingMode/tier fall back to the kind-derived default
			// (internal → showback, tier org); a malformed isolation is inert
			// because isolation is never read from the request.
			name: "malformed billingMode/isolation/tier fall back to the derived default",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "bogus", Isolation: "bogus", Tier: "bogus"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "malformed isolation on M-plan customer → vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "m", Isolation: "bogus"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			// #4292: a malformed plan slug falls back to "s" so a bad body can
			// never mint an uncapped Org; the boundary is unaffected.
			name: "malformed plan slug falls back to s",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "jumbo"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrgShape(tc.in)
			if got != tc.want {
				t.Fatalf("resolveOrgShape(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
