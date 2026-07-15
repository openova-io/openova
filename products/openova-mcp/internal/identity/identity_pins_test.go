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
