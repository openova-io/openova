package identity

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// resolverFromClaims exercises the (context, tier, scope) derivation
// without minting a signed token — the insecure resolver + fromClaims.
func TestDeriveContextAndTier(t *testing.T) {
	cases := []struct {
		name      string
		claims    *sharedauth.Claims
		pin       Context
		wantCtx   Context
		wantTier  Tier
		wantOrg   string
		expectErr bool
	}{
		{
			name: "sovereign-admin role → sovereign context, top tier",
			claims: &sharedauth.Claims{
				Email: "emrah.baysal@openova.io", Role: "sovereign-admin",
				DeploymentID: "7bb723da8da06047",
			},
			wantCtx: ContextSovereign, wantTier: TierSovereignAdmin,
		},
		{
			name: "org owner → organization context, owner tier",
			claims: &sharedauth.Claims{
				Email: "owner@acme.test", OrgID: "acme", Role: "owner",
			},
			wantCtx: ContextOrganization, wantTier: TierOwner, wantOrg: "acme",
		},
		{
			name: "org viewer via catalyst-viewer capability",
			claims: &sharedauth.Claims{
				Email: "v@acme.test", OrgID: "acme",
				Capabilities: []string{"catalyst-viewer"},
			},
			wantCtx: ContextOrganization, wantTier: TierViewer, wantOrg: "acme",
		},
		{
			name: "highest of multiple roles wins",
			claims: &sharedauth.Claims{
				OrgID: "acme", Capabilities: []string{"catalyst-viewer", "catalyst-admin", "catalyst-developer"},
			},
			wantCtx: ContextOrganization, wantTier: TierAdmin, wantOrg: "acme",
		},
		{
			name: "pin organization but no org_id → error",
			claims: &sharedauth.Claims{
				Email: "x@y.test", Role: "owner",
			},
			pin: ContextOrganization, expectErr: true,
		},
		{
			name: "non-admin without org_id → under-scoped error",
			claims: &sharedauth.Claims{
				Email: "x@y.test", Role: "viewer",
			},
			expectErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewInsecureResolver(tc.pin)
			id, err := r.fromClaims(tc.claims)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil (id=%+v)", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.Context != tc.wantCtx {
				t.Errorf("context = %q, want %q", id.Context, tc.wantCtx)
			}
			if id.Tier != tc.wantTier {
				t.Errorf("tier = %s, want %s", id.Tier, tc.wantTier)
			}
			if id.OrgID != tc.wantOrg {
				t.Errorf("orgID = %q, want %q", id.OrgID, tc.wantOrg)
			}
		})
	}
}

// TestResolveInsecureFromSignedToken confirms the JWT parse path populates
// the shared claim fields the handover JWT carries.
func TestResolveInsecureFromSignedToken(t *testing.T) {
	claims := &sharedauth.Claims{
		Email:         "emrah.baysal@openova.io",
		Role:          "sovereign-admin",
		DeploymentID:  "7bb723da8da06047",
		SovereignFQDN: "hw173.omani.works",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Insecure resolver uses ParseUnverified so an alg=none token resolves.
	id, err := NewInsecureResolver("").Resolve(signed)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id.Context != ContextSovereign || id.Tier != TierSovereignAdmin {
		t.Fatalf("got context=%s tier=%s", id.Context, id.Tier)
	}
	if id.DeploymentID != "7bb723da8da06047" {
		t.Errorf("deployment_id = %q", id.DeploymentID)
	}
	if id.RawBearer != signed {
		t.Errorf("raw bearer not retained for facade forwarding")
	}
}

func TestPinForcesContext(t *testing.T) {
	// A sovereign-admin token presented to a per-Org-pinned MCP must NOT
	// widen the surface: the pin forces Org context, and (lacking org_id)
	// it is rejected — a per-Org instance only accepts org-scoped tokens.
	claims := &sharedauth.Claims{Email: "a@b.test", Role: "sovereign-admin"}
	_, err := NewInsecureResolver(ContextOrganization).fromClaims(claims)
	if err == nil {
		t.Fatal("expected per-Org pin to reject a non-org-scoped token")
	}

	// With an org_id the pin holds the caller to that Org even though the
	// token also carries sovereign-admin.
	claims2 := &sharedauth.Claims{Email: "a@b.test", Role: "sovereign-admin", OrgID: "acme"}
	id, err := NewInsecureResolver(ContextOrganization).fromClaims(claims2)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if id.Context != ContextOrganization {
		t.Fatalf("pin failed to force org context: %s", id.Context)
	}
}
