package handoverjwt

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// rfc7638N is the modulus of the example key of RFC 7638 section 3.1.
const rfc7638N = "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw"

// TestKeyIDIsTheRFC7638Thumbprint: the example key of RFC 7638 section 3.1
// has the thumbprint that section prints.
func TestKeyIDIsTheRFC7638Thumbprint(t *testing.T) {
	n, err := base64.RawURLEncoding.DecodeString(rfc7638N)
	if err != nil {
		t.Fatal(err)
	}
	s := &Signer{privateKey: &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}}}
	if got := s.KeyID(); got != "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs" {
		t.Fatalf("KeyID = %s", got)
	}
}

// TestKeyIDInTheHeaderAndTheKeySet: a token of SignCustomClaimsWithKeyID
// names KeyID in its header; PublicJWKWithKeyID is PublicJWK plus that kid;
// SignCustomClaims, which sessions use, still names no key.
func TestKeyIDInTheHeaderAndTheKeySet(t *testing.T) {
	priv, _, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(priv, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	header := func(token string) map[string]any {
		b, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		return out
	}
	withKid, err := s.SignCustomClaimsWithKeyID(jwt.MapClaims{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if h := header(withKid); h["kid"] != s.KeyID() || h["alg"] != "RS256" || h["typ"] != "JWT" || len(h) != 3 {
		t.Fatalf("header %v", h)
	}
	plain, err := s.SignCustomClaims(jwt.MapClaims{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := header(plain)["kid"]; has {
		t.Fatal("SignCustomClaims names a key")
	}
	var a, b map[string]any
	j1, _ := s.PublicJWK()
	j2, _ := s.PublicJWKWithKeyID()
	_ = json.Unmarshal(j1, &a)
	_ = json.Unmarshal(j2, &b)
	if b["kid"] != s.KeyID() || len(b) != len(a)+1 {
		t.Fatalf("PublicJWKWithKeyID %v, PublicJWK %v", b, a)
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("member %s: %v, PublicJWK has %v", k, b[k], v)
		}
	}
}
