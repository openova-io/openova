package identity

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// #6369 — a Sovereign-mode MCP instance admitted a NON-admin and handed it the
// fleet-wide tool surface.
//
// MEASURED on hw299 with an ordinary signed-in console session as the bearer
// (whoami: context=sovereign, tier=owner, sovereign_admin=false, org_id=""):
//
//	list_applications   -> 11 applications across 6 namespaces, including
//	                       another Organization's (`mailwalk`)
//	get_application     -> that Org's application returned in full
//	create_application  -> HTTP 201, an Application CREATED inside that Org
//
// WHY NOTHING DOWNSTREAM CAUGHT IT: with org_id empty there is no left-hand
// side for any org-scoping comparison, so every such check is vacuous — it
// cannot fail for any input. The isolation was not bypassed; it was never
// evaluated.
//
// WHY THE EXISTING GUARD DID NOT CATCH IT: the #5206 guard immediately above
// the fix asserts in its own error message that the instance "requires a
// sovereign-admin session" — but it only ever tested CONTEXT. Context is not a
// proxy for admin-ness: deriveContext returns ContextSovereign for ANY token
// whose issuer ends /realms/sovereign once org_id is absent, without ever
// consulting tier. So the non-admin session derived ContextSovereign and passed
// the check whose message promised admin-only.

func sovereignRealmClaims(role, orgID string) *sharedauth.Claims {
	return &sharedauth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "https://auth.hw299.omantel.biz/realms/sovereign",
		},
		Email: "emrah.baysal@openova.io",
		Role:  role,
		OrgID: orgID,
	}
}

// TestSovereignMode_RefusesNonAdmin is the regression proof.
func TestSovereignMode_RefusesNonAdmin(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "viewer"} {
		t.Run("role="+role, func(t *testing.T) {
			r := &Resolver{pinnedCtx: ContextSovereign}
			id, err := r.fromClaims(sovereignRealmClaims(role, ""))
			if err == nil {
				t.Fatalf("role=%q with org_id=\"\" was ADMITTED to a Sovereign-mode instance (#6369).\n"+
					"That hands the fleet-wide tool surface to a non-admin, and with org_id empty no\n"+
					"downstream org-scoping check can deny it. resolved identity: %+v", role, id)
			}
			if !strings.Contains(err.Error(), "sovereign-admin") {
				t.Errorf("role=%q: refused, but the error does not name the missing property: %v", role, err)
			}
		})
	}
}

// TestSovereignMode_AdmitsRealAdmin is the CONTROL. Without it the fix could
// refuse EVERYONE and the test above would still pass — a guard that denies
// unconditionally is as wrong as one that permits unconditionally.
func TestSovereignMode_AdmitsRealAdmin(t *testing.T) {
	r := &Resolver{pinnedCtx: ContextSovereign}

	id, err := r.fromClaims(sovereignRealmClaims("sovereign-admin", ""))
	if err != nil {
		t.Fatalf("a genuine sovereign-admin was REFUSED by the Sovereign-mode instance — the fix over-corrected: %v", err)
	}
	if id.Tier != TierSovereignAdmin {
		t.Errorf("admin resolved to tier %v, want TierSovereignAdmin", id.Tier)
	}
	if id.Context != ContextSovereign {
		t.Errorf("admin resolved to context %v, want ContextSovereign", id.Context)
	}
}

// TestSovereignMode_StillRefusesOrgScopedToken pins the pre-existing #5206
// behaviour so this fix does not silently replace one guard with another.
func TestSovereignMode_StillRefusesOrgScopedToken(t *testing.T) {
	r := &Resolver{pinnedCtx: ContextSovereign}

	_, err := r.fromClaims(sovereignRealmClaims("member", "acme"))
	if err == nil {
		t.Fatal("an Org-scoped token was admitted to a Sovereign-mode instance — the #5206 guard regressed")
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Errorf("the #5206 error should name the caller's Organization so the message is actionable: %v", err)
	}
}
