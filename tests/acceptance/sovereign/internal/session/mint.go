// Package session mints a catalyst_session cookie value for an owner on a
// Sovereign, the same JWT HandlePinVerify issues
// (products/catalyst/bootstrap/api/internal/handler/auth.go): RS256, signed
// with the Sovereign's OWN handover signer, iss = CATALYST_PIN_ISSUER
// (https://console.<fqdn>). It exists so the suite can run headless when the
// PIN mail path is rate-limited; the preferred input is still a real session
// in CATALYST_SESSION.
package session

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// Claims mirrors the PIN-verify session claims.
type Claims struct {
	Issuer string
	Email  string
	TTL    time.Duration
	Now    time.Time
}

// ParseKey accepts PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY") PEM.
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("session: no PEM block in key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("session: key is neither PKCS#1 nor PKCS#8: %w", err)
	}
	k, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("session: PKCS#8 key is not RSA")
	}
	return k, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jti() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Mint returns the signed compact JWT.
func Mint(key *rsa.PrivateKey, c Claims) (string, error) {
	if c.Issuer == "" || c.Email == "" {
		return "", errors.New("session: issuer and email are required")
	}
	if c.TTL <= 0 {
		c.TTL = 2 * time.Hour
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	body, _ := json.Marshal(map[string]any{
		"iss":            c.Issuer,
		"sub":            c.Email,
		"email":          c.Email,
		"email_verified": true,
		"role":           "openova-user",
		"tier":           "owner",
		"realm_access":   map[string]any{"roles": []string{"catalyst-owner", "catalyst-admin", "sovereign-admins"}},
		"iat":            c.Now.Unix(),
		"exp":            c.Now.Add(c.TTL).Unix(),
		"jti":            jti(),
		"typ":            "session",
	})
	signing := b64(hdr) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("session: sign: %w", err)
	}
	return signing + "." + b64(sig), nil
}
