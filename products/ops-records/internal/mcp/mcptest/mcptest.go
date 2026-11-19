// Package mcptest signs agent tokens for tests, as catalyst-api's signer does, and serves its
// key set. It is imported by tests only.
package mcptest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"testing"
	"time"
)

// Issuer is the iss of the tokens Token signs.
const Issuer = "https://auth.sovereign.test"

// Signer holds one RSA key.
type Signer struct {
	Key *rsa.PrivateKey
	Kid string // the RFC 7638 thumbprint of the public key
}

// NewSigner makes a signer with a new 2048-bit key.
func NewSigner(t testing.TB) *Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return &Signer{Key: key, Kid: base64.RawURLEncoding.EncodeToString(sum[:])}
}

// KeySet answers the key set route with the signer's public key.
func (s *Signer) KeySet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256",
		"kid": s.Kid, "n": base64.RawURLEncoding.EncodeToString(s.Key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.Key.E)).Bytes())}}})
}

// Claims returns the claims of a valid agent token of organization and agent at tier viewer,
// for one hour from now.
func Claims(organization, agent string) map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": Issuer, "aud": []string{"model-gateway", "platform-tools", "catalyst-api"},
		"sub": "agent:" + organization + "/" + agent, "typ": "agent", "org": organization, "org_id": organization,
		"agent": agent, "tier": "viewer", "role": "openova-user", "email": agent + "@" + organization + ".agents.invalid",
		"iat": now, "exp": now + 3600, "jti": "00000000-0000-4000-8000-000000000000"}
}

// Token signs claims RS256 with header kid; an empty kid writes none.
func (s *Signer) Token(t testing.TB, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	part := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signed := part(header) + "." + part(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}
