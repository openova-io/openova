package identity

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

func mintNone(t *testing.T, claims *sharedauth.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// The issuer pin (#3988 §4.3 "mode selects the trusted realm") — an exact
// `iss` mismatch is rejected before any identity derivation.
func TestExpectedIssuerPin(t *testing.T) {
	issuer := "https://keycloak.hw255.omani.works/realms/sovereign"
	claims := &sharedauth.Claims{
		Email: "e@openova.io", Role: "sovereign-admin",
		RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer},
	}
	bearer := mintNone(t, claims)

	if _, err := NewInsecureResolver("").WithExpectedIssuer(issuer).Resolve(bearer); err != nil {
		t.Fatalf("matching issuer rejected: %v", err)
	}

	_, err := NewInsecureResolver("").
		WithExpectedIssuer("https://keycloak.other.example/realms/sovereign").
		Resolve(bearer)
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("mismatched issuer accepted (err=%v)", err)
	}

	// Empty pin = no issuer check (default) — existing behavior unchanged.
	if _, err := NewInsecureResolver("").Resolve(bearer); err != nil {
		t.Fatalf("no-pin resolve failed: %v", err)
	}
}

// The org-scope pin (#3988 §4.3 "pinned scope") — a per-Org instance
// rejects a token minted for a DIFFERENT Org even when the signer/realm
// shape is identical.
func TestOrgScopePin(t *testing.T) {
	demo := mintNone(t, &sharedauth.Claims{Email: "u@demo.test", OrgID: "demo", Tier: "org-admin"})
	other := mintNone(t, &sharedauth.Claims{Email: "u@other.test", OrgID: "other", Tier: "org-admin"})

	r := NewInsecureResolver(ContextOrganization).WithOrgPin("demo")

	id, err := r.Resolve(demo)
	if err != nil {
		t.Fatalf("own-org token rejected: %v", err)
	}
	if id.OrgID != "demo" || id.Context != ContextOrganization {
		t.Fatalf("got org=%q ctx=%s", id.OrgID, id.Context)
	}

	if _, err := r.Resolve(other); err == nil {
		t.Fatal("cross-org token accepted by org-pinned instance")
	}
}

// #5206 — the symmetric guard: a Sovereign-pinned instance (mode=sovereign,
// the ONLY instance deployed today — bootstrap-kit slot 13d) must REJECT an
// Org-scoped token outright rather than silently relabelling its context to
// Sovereign. The pre-fix behaviour let the token through as ContextSovereign,
// which then misrouted every downstream tool call at the Sovereign-wide
// catalyst-api seam and got an opaque "org-scoped-forbidden" 403 far from its
// actual cause (the live hw270 symptom).
func TestSovereignPinRejectsOrgScopedToken(t *testing.T) {
	orgToken := mintNone(t, &sharedauth.Claims{Email: "u@acme.test", OrgID: "acme", Tier: "org-admin"})

	r := NewInsecureResolver(ContextSovereign)
	_, err := r.Resolve(orgToken)
	if err == nil {
		t.Fatal("Sovereign-pinned instance accepted an Org-scoped token instead of rejecting it")
	}
	if !strings.Contains(err.Error(), "sovereign-admin") || !strings.Contains(err.Error(), "acme") {
		t.Fatalf("rejection error should name the sovereign-admin requirement + the caller's org, got: %v", err)
	}

	// A genuine sovereign-admin token still resolves fine on the same pin.
	sovToken := mintNone(t, &sharedauth.Claims{Email: "e@openova.io", Role: "sovereign-admin"})
	id, err := r.Resolve(sovToken)
	if err != nil {
		t.Fatalf("Sovereign-pinned instance rejected a genuine sovereign-admin token: %v", err)
	}
	if id.Context != ContextSovereign {
		t.Fatalf("got context %q, want sovereign", id.Context)
	}
}
