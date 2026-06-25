// organization_orgshape_test.go — coverage for resolveOrgShape, the
// Organizations internal-door defaulting (issue #3378 B1). Locks the
// §2.1/§2.3 model: kind defaults to customer (the funnel door); kind-
// derived billingMode + isolation defaults; the advanced override; and
// the malformed-enum fallback that keeps a bad body from stamping a
// nonsense shape.
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
			name: "omitted kind defaults to the customer funnel shape (byte-unchanged)",
			in:   orgTenantCreateRequest{},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "internal door → showback + namespace (kind-derived defaults)",
			in:   orgTenantCreateRequest{Kind: "internal"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			name: "explicit customer → real + vcluster",
			in:   orgTenantCreateRequest{Kind: "customer"},
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
			name: "case-insensitive enum normalization",
			in:   orgTenantCreateRequest{Kind: "INTERNAL", Tier: "Corporate", BillingMode: "ShowBack", Isolation: "NameSpace", PlanSlug: "M"},
			want: orgShape{Kind: "internal", Tier: "corporate", BillingMode: "showback", Isolation: "namespace", PlanSlug: "m"},
		},
		{
			name: "malformed kind falls back to customer default shape",
			in:   orgTenantCreateRequest{Kind: "garbage"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "s"},
		},
		{
			name: "malformed billingMode/isolation fall back to the kind-derived default",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "bogus", Isolation: "bogus", Tier: "bogus"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace", PlanSlug: "s"},
		},
		{
			// #4292: an explicit valid plan slug passes through verbatim.
			name: "explicit plan slug passes through",
			in:   orgTenantCreateRequest{Kind: "customer", PlanSlug: "xl"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster", PlanSlug: "xl"},
		},
		{
			// #4292: a malformed plan slug falls back to "s" so a bad body can
			// never mint an uncapped Org.
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
