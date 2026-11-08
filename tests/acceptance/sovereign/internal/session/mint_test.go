package session

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestMintVerifiesAndCarriesSessionClaims(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]byte{
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}),
		func() []byte {
			b, _ := x509.MarshalPKCS8PrivateKey(k)
			return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
		}(),
	} {
		key, err := ParseKey(p)
		if err != nil {
			t.Fatalf("ParseKey: %v", err)
		}
		tok, err := Mint(key, Claims{Issuer: "https://console.x.example", Email: "o@example", TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			t.Fatalf("want 3 parts, got %d", len(parts))
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			t.Fatalf("signature does not verify: %v", err)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		_ = json.Unmarshal(raw, &c)
		for _, f := range []string{"jti", "email_verified", "typ", "tier"} {
			if c[f] == nil || c[f] == "" {
				t.Fatalf("claim %s missing — the API 401s without it", f)
			}
		}
		if c["typ"] != "session" || c["iss"] != "https://console.x.example" {
			t.Fatalf("wrong claims: %v", c)
		}
	}
}

func TestParseKeyRejectsGarbage(t *testing.T) {
	if _, err := ParseKey([]byte("not a pem")); err == nil {
		t.Fatal("garbage accepted")
	}
}
