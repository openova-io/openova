// sme_tenant_orgshape_test.go — coverage for resolveOrgShape, the
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
			name: "omitted kind defaults to the customer funnel shape (byte-unchanged)",
			in:   orgTenantCreateRequest{},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster"},
		},
		{
			name: "internal door → showback + namespace (kind-derived defaults)",
			in:   orgTenantCreateRequest{Kind: "internal"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace"},
		},
		{
			name: "explicit customer → real + vcluster",
			in:   orgTenantCreateRequest{Kind: "customer"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster"},
		},
		{
			name: "advanced override: internal org forced onto chargeback + vcluster",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "chargeback", Isolation: "vcluster"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "chargeback", Isolation: "vcluster"},
		},
		{
			name: "corporate tier honored",
			in:   orgTenantCreateRequest{Kind: "internal", Tier: "corporate"},
			want: orgShape{Kind: "internal", Tier: "corporate", BillingMode: "showback", Isolation: "namespace"},
		},
		{
			name: "case-insensitive enum normalization",
			in:   orgTenantCreateRequest{Kind: "INTERNAL", Tier: "Corporate", BillingMode: "ShowBack", Isolation: "NameSpace"},
			want: orgShape{Kind: "internal", Tier: "corporate", BillingMode: "showback", Isolation: "namespace"},
		},
		{
			name: "malformed kind falls back to customer default shape",
			in:   orgTenantCreateRequest{Kind: "garbage"},
			want: orgShape{Kind: "customer", Tier: "org", BillingMode: "real", Isolation: "vcluster"},
		},
		{
			name: "malformed billingMode/isolation fall back to the kind-derived default",
			in:   orgTenantCreateRequest{Kind: "internal", BillingMode: "bogus", Isolation: "bogus", Tier: "bogus"},
			want: orgShape{Kind: "internal", Tier: "org", BillingMode: "showback", Isolation: "namespace"},
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
