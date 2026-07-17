package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// TestBuildResolver_DegradedWhenRS256PubkeyAbsent is the #5114 core: verify=rs256
// with NO pubkey must NOT return an error out of buildResolver (main() turns that
// into log.Fatalf → the pod CrashLoops → the harbor-prewarm cutover settle-gate
// #4982 FATALs the sovereignty cutover at step-3). It must instead return a
// working DEGRADED resolver that keeps the process up (pod reaches Ready) and
// rejects every bearer (fail-closed for auth).
func TestBuildResolver_DegradedWhenRS256PubkeyAbsent(t *testing.T) {
	t.Setenv("OPENOVA_MCP_VERIFY", "rs256")
	t.Setenv("OPENOVA_MCP_RS256_PUBKEY_PEM", "") // absent — the live hw258 case
	t.Setenv("OPENOVA_MCP_CONTEXT", "")
	t.Setenv("OPENOVA_MCP_EXPECTED_ISSUER", "")
	t.Setenv("OPENOVA_MCP_ORG_SCOPE", "")

	r, err := buildResolver()
	if err != nil {
		t.Fatalf("buildResolver MUST NOT exit when the rs256 pubkey is absent "+
			"(that CrashLoops the pod / trips the cutover settle-gate); got err %v", err)
	}
	if r == nil {
		t.Fatal("buildResolver returned a nil resolver")
	}
	if degraded, _ := r.Degraded(); !degraded {
		t.Fatal("resolver should be in DEGRADED mode when the rs256 pubkey is absent")
	}
	// A degraded resolver rejects every bearer → empty tools/list + 401 tools/call.
	if _, err := r.Resolve("any.bearer.value"); err == nil {
		t.Fatal("degraded resolver must reject bearers (fail-closed for auth)")
	}
}

// TestBuildResolver_DegradedWhenRS256PubkeyUnparseable proves a genuinely
// unparseable pubkey (non-RSA JWK / garbage) still degrades instead of
// crashing (#5114 posture). NOTE (#5167): a VALID RSA JWK is no longer the
// unparseable case — it is now the PRIMARY fresh-prov path (the
// `catalyst-handover-jwt-public` mirror), pinned ACTIVE by
// TestBuildResolver_RS256ActiveWithJWK below.
func TestBuildResolver_DegradedWhenRS256PubkeyUnparseable(t *testing.T) {
	for name, payload := range map[string]string{
		"non-RSA JWK":    `{"kty":"EC","crv":"P-256","x":"a","y":"b"}`,
		"garbage n":      `{"kty":"RSA","n":"%%%not-base64url%%%","e":"AQAB"}`,
		"missing n/e":    `{"kty":"RSA"}`,
		"not JSON / PEM": `this is neither a pem nor a jwk`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENOVA_MCP_VERIFY", "rs256")
			t.Setenv("OPENOVA_MCP_RS256_PUBKEY_PEM", payload)
			t.Setenv("OPENOVA_MCP_CONTEXT", "")
			t.Setenv("OPENOVA_MCP_EXPECTED_ISSUER", "")
			t.Setenv("OPENOVA_MCP_ORG_SCOPE", "")

			r, err := buildResolver()
			if err != nil {
				t.Fatalf("buildResolver must DEGRADE (not exit) on an unparseable pubkey; got %v", err)
			}
			if degraded, _ := r.Degraded(); !degraded {
				t.Fatal("resolver should be DEGRADED when the rs256 pubkey is unparseable")
			}
		})
	}
}

// TestBuildResolver_RS256ActiveWithPubkey is the positive path: a valid PKIX PEM
// makes rs256 verify ACTIVE. A token signed by the matching private key resolves;
// a token signed by a DIFFERENT key is rejected — proving the signature is really
// checked (not silently accepted).
func TestBuildResolver_RS256ActiveWithPubkey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	t.Setenv("OPENOVA_MCP_VERIFY", "rs256")
	t.Setenv("OPENOVA_MCP_RS256_PUBKEY_PEM", string(pemBytes))
	t.Setenv("OPENOVA_MCP_CONTEXT", "")
	t.Setenv("OPENOVA_MCP_EXPECTED_ISSUER", "")
	t.Setenv("OPENOVA_MCP_ORG_SCOPE", "")

	r, err := buildResolver()
	if err != nil {
		t.Fatalf("buildResolver with a valid pubkey: %v", err)
	}
	if degraded, reason := r.Degraded(); degraded {
		t.Fatalf("resolver should NOT be degraded with a valid pubkey; got %q", reason)
	}

	// A session token signed by the matching private key resolves.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-1", "org_id": "demo", "tier": "org-admin", "typ": "session",
	})
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	id, err := r.Resolve(signed)
	if err != nil {
		t.Fatalf("a valid RS256 token must resolve: %v", err)
	}
	if id.Context != identity.ContextOrganization || id.OrgID != "demo" {
		t.Fatalf("unexpected identity: context=%s org=%s", id.Context, id.OrgID)
	}

	// A token signed by a DIFFERENT key must be rejected (rs256 verify active).
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen other key: %v", err)
	}
	badTok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-1", "org_id": "demo", "tier": "org-admin", "typ": "session",
	})
	badSigned, err := badTok.SignedString(other)
	if err != nil {
		t.Fatalf("sign bad: %v", err)
	}
	if _, err := r.Resolve(badSigned); err == nil {
		t.Fatal("a token signed by a NON-trusted key must be rejected (rs256 verify active)")
	}
}

// TestBuildResolver_RS256ActiveWithJWK is the #5167 fresh-prov path: the ONLY
// verify-pubkey Secret a Sovereign seeds is the JWK mirror
// `catalyst-handover-jwt-public` (key `public.jwk`, an RSA JWK). Feeding that
// JWK into OPENOVA_MCP_RS256_PUBKEY_PEM must make rs256 verify ACTIVE (not
// DEGRADED): a token signed by the matching private key resolves, a token
// signed by a different key is rejected.
func TestBuildResolver_RS256ActiveWithJWK(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	// Build the RSA JWK exactly the way the platform's mirror does:
	// base64url-without-padding n + e (RFC 7518 §6.3).
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	jwk := fmt.Sprintf(`{"alg":"RS256","e":%q,"kty":"RSA","n":%q,"use":"sig"}`, e, n)

	t.Setenv("OPENOVA_MCP_VERIFY", "rs256")
	t.Setenv("OPENOVA_MCP_RS256_PUBKEY_PEM", jwk)
	t.Setenv("OPENOVA_MCP_CONTEXT", "")
	t.Setenv("OPENOVA_MCP_EXPECTED_ISSUER", "")
	t.Setenv("OPENOVA_MCP_ORG_SCOPE", "")

	r, err := buildResolver()
	if err != nil {
		t.Fatalf("buildResolver with a valid RSA JWK: %v", err)
	}
	if degraded, reason := r.Degraded(); degraded {
		t.Fatalf("resolver must be ACTIVE with a valid RSA JWK (the catalyst-handover-jwt-public format, #5167); got DEGRADED %q", reason)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-1", "org_id": "demo", "tier": "org-admin", "typ": "session",
	})
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	id, err := r.Resolve(signed)
	if err != nil {
		t.Fatalf("a valid RS256 token must resolve against the JWK-loaded key: %v", err)
	}
	if id.Context != identity.ContextOrganization || id.OrgID != "demo" {
		t.Fatalf("unexpected identity: context=%s org=%s", id.Context, id.OrgID)
	}

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen other key: %v", err)
	}
	badSigned, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-1", "org_id": "demo", "tier": "org-admin", "typ": "session",
	}).SignedString(other)
	if err != nil {
		t.Fatalf("sign bad: %v", err)
	}
	if _, err := r.Resolve(badSigned); err == nil {
		t.Fatal("a token signed by a NON-trusted key must be rejected (JWK-loaded rs256 verify active)")
	}
}

// TestBuildResolver_DegradedWhenHS256SecretAbsent — the hs256 secret is an
// optional secretKeyRef too, so an absent secret must degrade, never crash.
func TestBuildResolver_DegradedWhenHS256SecretAbsent(t *testing.T) {
	t.Setenv("OPENOVA_MCP_VERIFY", "hs256")
	t.Setenv("OPENOVA_MCP_HS256_SECRET", "")
	t.Setenv("OPENOVA_MCP_CONTEXT", "")
	t.Setenv("OPENOVA_MCP_EXPECTED_ISSUER", "")
	t.Setenv("OPENOVA_MCP_ORG_SCOPE", "")

	r, err := buildResolver()
	if err != nil {
		t.Fatalf("buildResolver must DEGRADE (not exit) when the hs256 secret is absent; got %v", err)
	}
	if degraded, _ := r.Degraded(); !degraded {
		t.Fatal("resolver should be DEGRADED when the hs256 secret is absent")
	}
}
