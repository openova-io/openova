package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signedJWT signs header and payload with key, RS256.
func signedJWT(t *testing.T, key *rsa.PrivateKey, header, payload string) string {
	t.Helper()
	input := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func agentPayload() string {
	exp := time.Now().Add(10 * time.Minute).Unix()
	b, _ := json.Marshal(map[string]any{"sub": "agent:acme/correlator", "typ": "agent", "agent": "correlator", "org_id": "acme",
		"tier": "viewer", "aud": []string{"model-gateway", "platform-tools", "catalyst-api"}, "exp": exp})
	return string(b)
}

// TestValidateToken_LocalKeyID: a token whose header names the local key
// is verified with the local key and fetches no key set (the config has no
// key-set cache, so a fetch would panic).
func TestValidateToken_LocalKeyID(t *testing.T) {
	key := rsaKey(t)
	cfg := &Config{LocalPublicKey: &key.PublicKey, LocalKeyID: "local-kid"}
	tok := signedJWT(t, key, `{"alg":"RS256","typ":"JWT","kid":"local-kid"}`, agentPayload())
	c, err := cfg.ValidateToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if c.Typ != "agent" || c.Agent != "correlator" || c.Org != "acme" || strings.Join(c.Audience, ",") != "model-gateway,platform-tools,catalyst-api" {
		t.Fatalf("claims %+v", c)
	}
	other := signedJWT(t, rsaKey(t), `{"alg":"RS256","typ":"JWT","kid":"local-kid"}`, agentPayload())
	if _, err := cfg.ValidateToken(context.Background(), other); err == nil {
		t.Fatal("a token of another key that names the local key was accepted")
	}
}

// TestValidateToken_NoKidStillLocal: a session without a key identifier is
// verified with the local key, as before.
func TestValidateToken_NoKidStillLocal(t *testing.T) {
	key := rsaKey(t)
	cfg := &Config{LocalPublicKey: &key.PublicKey, LocalKeyID: "local-kid"}
	exp := time.Now().Add(time.Hour).Unix()
	tok := signedJWT(t, key, `{"alg":"RS256","typ":"JWT"}`, `{"sub":"u-1","email":"a@b.com","exp":`+big.NewInt(exp).String()+`}`)
	if c, err := cfg.ValidateToken(context.Background(), tok); err != nil || c.Email != "a@b.com" || c.Typ != "" || c.Audience != nil {
		t.Fatalf("ValidateToken = %+v, %v", c, err)
	}
}

// TestValidateToken_OtherKidUsesTheKeySet: a token that names another key
// takes the key-set branch, unchanged.
func TestValidateToken_OtherKidUsesTheKeySet(t *testing.T) {
	local, remote := rsaKey(t), rsaKey(t)
	fetched := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "kc-kid", "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(remote.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(remote.E)).Bytes())}}})
	}))
	defer srv.Close()
	cfg := &Config{LocalPublicKey: &local.PublicKey, LocalKeyID: "local-kid", JWKSCache: NewJWKSCache(srv.URL, time.Minute)}
	tok := signedJWT(t, remote, `{"alg":"RS256","typ":"JWT","kid":"kc-kid"}`, agentPayload())
	if _, err := cfg.ValidateToken(context.Background(), tok); err != nil || fetched != 1 {
		t.Fatalf("ValidateToken: %v, key set fetched %d times", err, fetched)
	}
}

// TestClaims_Audience: aud is read as one string or a list; any other shape
// leaves Audience empty and never fails the parse.
func TestClaims_Audience(t *testing.T) {
	for payload, want := range map[string]string{
		`{"sub":"x","aud":"catalyst-api"}`:         "catalyst-api",
		`{"sub":"x","aud":["a","catalyst-api"]}`:   "a,catalyst-api",
		`{"sub":"x"}`:                              "",
		`{"sub":"x","aud":42}`:                     "",
		`{"sub":"x","aud":{"x":1},"typ":"Bearer"}`: "",
	} {
		c, err := parseJWTClaims(makeJWT(t, payload))
		if err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		if got := strings.Join(c.Audience, ","); got != want {
			t.Errorf("%s: Audience %q, want %q", payload, got, want)
		}
	}
}
