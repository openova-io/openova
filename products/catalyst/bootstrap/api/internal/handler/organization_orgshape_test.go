// organization_orgshape_test.go — coverage for resolveOrgShape, the
// Organizations internal-door defaulting (issue #3378 B1) + the #4539
// isolation-label-from-tier-gate fix. Locks the §2.1/§2.3 model: kind defaults
// to customer (the funnel door); kind-derived billingMode default; the #4292
// TIER GATE deriving the isolation label (free/S → namespace host-ns, M+ →
// vcluster; internal always namespace); the advanced override; and the
// malformed-enum fallback that keeps a bad body from stamping a nonsense shape.
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
			// #4539: an S-plan customer Org backs a HOST NAMESPACE per the tier
			// gate, so the derived isolation is "namespace" (was wrongly
			// "vcluster" pre-#4539).
			name: "omitted kind defaults to the customer funnel shape — S → host-ns",
			in:   orgTenantCreateRequest{},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			name: "internal door → showback + namespace (kind-derived defaults)",
			in:   orgTenantCreateRequest{Kind: "internal"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539: explicit customer at the default S plan still backs host-ns.
			name: "explicit customer at default S plan → real + host-ns",
			in:   orgTenantCreateRequest{Kind: "customer"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539 core assertion: an M-plan customer Org gets a dedicated
			// vCluster per the #4292 tier gate → isolation "vcluster".
			name: "customer M plan → dedicated vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "m"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			// #4539: every paid M+ tier (l/xl/flexi) is vcluster.
			name: "customer XL plan → dedicated vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "xl"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "xl"},
		},
		{
			// #4539: an explicit "free" plan is a host-ns boundary like S.
			name: "customer free plan → host-ns",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "free"},
			// "free" is not a quota slug → planSlug falls back to "s", but the
			// isolation derivation runs on the resolved slug → still namespace.
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539: an explicit isolation in the request still overrides the
			// derived value (the advanced operator view) — an S-plan Org can be
			// force-put onto a vcluster.
			name: "advanced override: S-plan customer forced onto vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "s", Isolation: "vcluster"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "advanced override: internal org forced onto chargeback + vcluster",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "chargeback", Isolation: "vcluster"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "chargeback", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "corporate tier honored",
			in:   orgTenantCreateRequest{Kind: "internal", Tier: "corporate"},
			want: orgShape{Kind: "internal", Tier: "corporate", BillingMode: "showback", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539: case-insensitive — M plan customer derives vcluster.
			name: "case-insensitive enum normalization (M plan → vcluster)",
			in:   orgTenantCreateRequest{Kind: "CUSTOMER", Tier: "Corporate", BillingMode: "Real", PlanSlug: "M"},
			want: orgShape{Kind: "customer", Tier: "corporate", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			// #4539: malformed kind → customer + S default → host-ns.
			name: "malformed kind falls back to customer S host-ns shape",
			in:   orgTenantCreateRequest{Kind: "garbage"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539: malformed billingMode/isolation fall back to the
			// kind+tier-derived default (internal S → showback + namespace).
			name: "malformed billingMode/isolation fall back to the derived default",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "bogus", Isolation: "bogus", Tier: "bogus"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4539: malformed isolation on an M-plan customer → derived vcluster.
			name: "malformed isolation on M-plan customer → derived vcluster",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "m", Isolation: "bogus"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "m"},
		},
		{
			// #4292: a malformed plan slug falls back to "s" so a bad body can
			// never mint an uncapped Org; #4539: the resolved S → host-ns.
			name: "malformed plan slug falls back to s → host-ns",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "jumbo"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "namespace", PlanSlug: "s"},
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

// TestIsolationForTier locks the #4539 tier-gate derivation directly — the
// single helper both the BSS create path (resolveOrgShape) and the CR read path
// (orgCRToResponse) call so the displayed isolation label always matches the
// actual backing the org-controller authors (gitops.boundaryIsVcluster).
//
// The `internal M → host-ns` case that used to sit in this table is GONE, and
// its removal is the point: see TestIsolationForTier_IgnoresKind in
// tier_gate_lockstep_4292_test.go for why an internal Org on a paid plan is
// vcluster-backed like any other.
func TestIsolationForTier(t *testing.T) {
	tests := []struct {
		name     string
		planSlug string
		want     string
	}{
		{"empty plan → host-ns", "", "namespace"},
		{"S → host-ns", "s", "namespace"},
		{"free → host-ns", "free", "namespace"},
		{"M → vcluster", "m", "vcluster"},
		{"L → vcluster", "l", "vcluster"},
		{"XL → vcluster", "xl", "vcluster"},
		{"flexi → vcluster", "flexi", "vcluster"},
		{"case-insensitive M → vcluster", "M", "vcluster"},
		{"whitespace tolerated", "  m  ", "vcluster"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isolationForTier(tc.planSlug); got != tc.want {
				t.Fatalf("isolationForTier(%q) = %q, want %q", tc.planSlug, got, tc.want)
			}
		})
	}
}
