// identity_agent_test.go — agent tokens (typ agent) at the tool server
// (AIP-150): every rule refuses on its own, and the tier is capped at developer.
package identity

import (
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const agentIssuer = "https://console.hw307.omani.works"

// agentClaims is the exact claim set catalyst-api mints for an agent token.
func agentClaims(org, agent, tier string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": agentIssuer, "sub": "agent:" + org + "/" + agent,
		"aud": []string{"model-gateway", "platform-tools", "catalyst-api"},
		"typ": "agent", "org": org, "org_id": org, "agent": agent, "tier": tier,
		"realm_access": map[string]any{"roles": []string{"catalyst-" + tier}},
		"role":         "openova-user", "email": agent + "@" + org + ".agents.invalid",
		"email_verified": false, "caller": "system:serviceaccount:catalyst-system:catalyst-conformance",
		"iat": now.Unix(), "exp": now.Add(30 * time.Minute).Unix(), "jti": "6f1c2a1e-0d1b-4c55-9a43-7f1e2d3c4b5a",
	}
}

// signAgent signs claims RS256 with k and writes kid into the header ("" = no kid).
func signAgent(t *testing.T, k *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(k)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

// The key identifier is the RFC 7638 thumbprint: the example of section 3.1
// of RFC 7638 must give its published value.
func TestKeyIDIsTheRFC7638Thumbprint(t *testing.T) {
	n, err := base64.RawURLEncoding.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	if err != nil {
		t.Fatalf("decode n: %v", err)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}
	if got, want := KeyID(pub), "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"; got != want {
		t.Fatalf("KeyID = %q, want %q", got, want)
	}
}

// A valid agent token resolves to its Organization, its agent and its tier.
func TestAgentTokenAccepted(t *testing.T) {
	k := mustKey(t)
	r := NewRS256SetResolver([]*rsa.PublicKey{&mustKey(t).PublicKey, &k.PublicKey}, "").WithAgentIssuer(agentIssuer)
	id, err := r.Resolve(signAgent(t, k, KeyID(&k.PublicKey), agentClaims("acme", "correlator", "viewer")))
	if err != nil {
		t.Fatalf("a valid agent token was refused: %v", err)
	}
	if id.Context != ContextOrganization || id.OrgID != "acme" || id.Tier != TierViewer || id.Agent != "correlator" {
		t.Fatalf("resolved context=%s org=%q tier=%s agent=%q", id.Context, id.OrgID, id.Tier, id.Agent)
	}
	if id.Email != "correlator@acme.agents.invalid" {
		t.Fatalf("email = %q", id.Email)
	}
}

// The tier is the claim tier alone, capped at developer; role, groups and
// capabilities of an agent token are not read.
func TestAgentTierIsTheTierClaimCappedAtDeveloper(t *testing.T) {
	k := mustKey(t)
	r := NewRS256Resolver(&k.PublicKey, "")
	for tier, want := range map[string]Tier{
		"viewer": TierViewer, "developer": TierDeveloper, "operator": TierDeveloper,
		"admin": TierDeveloper, "org-admin": TierDeveloper, "owner": TierDeveloper,
	} {
		c := agentClaims("acme", "fixer", tier)
		c["role"] = "sovereign-admin"
		c["groups"] = []string{"sovereign-admins"}
		c["capabilities"] = []string{"catalyst-owner"}
		id, err := r.Resolve(signAgent(t, k, KeyID(&k.PublicKey), c))
		if err != nil {
			t.Fatalf("tier %q: refused: %v", tier, err)
		}
		if id.Tier != want || id.Context != ContextOrganization {
			t.Fatalf("tier claim %q: got tier=%s context=%s, want tier=%s context=organization", tier, id.Tier, id.Context, want)
		}
	}
}

// Every rule of an agent token refuses on its own. The letter case of typ does
// not matter: an "Agent" token is held to the same rules.
func TestAgentTokenRefusals(t *testing.T) {
	k, other := mustKey(t), mustKey(t)
	kid := KeyID(&k.PublicKey)
	cases := []struct {
		name   string
		kid    string
		mutate func(jwt.MapClaims)
		want   string
	}{
		{"no platform-tools audience", kid, func(c jwt.MapClaims) { c["aud"] = []string{"model-gateway", "catalyst-api"} }, "aud does not contain"},
		{"no audience, typ Agent", kid, func(c jwt.MapClaims) { delete(c, "aud"); c["typ"] = "Agent" }, "aud does not contain"},
		{"no kid", "", func(jwt.MapClaims) {}, "kid"},
		{"kid of another key", KeyID(&other.PublicKey), func(jwt.MapClaims) {}, "kid"},
		{"another issuer", kid, func(c jwt.MapClaims) { c["iss"] = "https://console.other.omani.works" }, "issuer"},
		{"no exp", kid, func(c jwt.MapClaims) { delete(c, "exp") }, "no exp"},
		{"no org_id", kid, func(c jwt.MapClaims) { delete(c, "org_id") }, "no org_id"},
		{"agent name with a capital", kid, func(c jwt.MapClaims) { c["agent"] = "Fixer" }, "agent name"},
		{"agent name of one letter", kid, func(c jwt.MapClaims) { c["agent"] = "f" }, "agent name"},
		{"no agent name", kid, func(c jwt.MapClaims) { delete(c, "agent") }, "agent name"},
		{"no tier", kid, func(c jwt.MapClaims) { delete(c, "tier") }, "not on the ladder"},
		{"tier off the ladder", kid, func(c jwt.MapClaims) { c["tier"] = "sovereign-admin" }, "not on the ladder"},
	}
	r := NewRS256SetResolver([]*rsa.PublicKey{&k.PublicKey, &other.PublicKey}, "").WithAgentIssuer(agentIssuer)
	for _, tc := range cases {
		c := agentClaims("acme", "fixer", "viewer")
		tc.mutate(c)
		_, err := r.Resolve(signAgent(t, k, tc.kid, c))
		if err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
}

// Without an agent issuer pin any issuer the signature verifies is accepted;
// the pin binds agent tokens only, never sessions.
func TestAgentIssuerPinBindsAgentTokensOnly(t *testing.T) {
	k := mustKey(t)
	c := agentClaims("acme", "fixer", "viewer")
	c["iss"] = "https://console.other.omani.works"
	if _, err := NewRS256Resolver(&k.PublicKey, "").Resolve(signAgent(t, k, KeyID(&k.PublicKey), c)); err != nil {
		t.Fatalf("no agent issuer pin: refused: %v", err)
	}
	r := NewRS256Resolver(&k.PublicKey, "").WithAgentIssuer(agentIssuer)
	id, err := r.Resolve(signOrgScoped(t, k, "acme"))
	if err != nil {
		t.Fatalf("a session token was refused by the agent issuer pin: %v", err)
	}
	if id.Tier != TierAdmin || id.Agent != "" {
		t.Fatalf("session token: tier=%s agent=%q, want admin and no agent", id.Tier, id.Agent)
	}
}

// Only an RS256 resolver accepts an agent token: the hs256 resolver and the
// insecure resolver refuse one, because neither verified it with a key of the set.
func TestAgentTokenRefusedWithoutRS256(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, agentClaims("acme", "fixer", "viewer"))
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := NewHS256Resolver(secret, "").Resolve(signed); err == nil || !strings.Contains(err.Error(), "only an RS256 resolver") {
		t.Fatalf("hs256 resolver: got %v, want a refusal", err)
	}
	if _, err := NewInsecureResolver("").Resolve(signed); err == nil || !strings.Contains(err.Error(), "only an RS256 resolver") {
		t.Fatalf("insecure resolver: got %v, want a refusal", err)
	}
}

// IsAgentToken reads typ from the unverified claims, in any letter case.
func TestIsAgentToken(t *testing.T) {
	k := mustKey(t)
	c := agentClaims("acme", "fixer", "viewer")
	c["typ"] = "AGENT"
	if !IsAgentToken("Bearer "+signAgent(t, k, "", c)) || IsAgentToken(signOrgScoped(t, k, "acme")) || IsAgentToken("not-a-jwt") {
		t.Fatal("IsAgentToken misread typ")
	}
}
