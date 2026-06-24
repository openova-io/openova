package main

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

// signJWT builds a minimal RS256 JWT for tests.
func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signingInput := enc(hb) + "." + enc(cb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + enc(sig)
}

// jwksServer stands up an OIDC discovery + JWKS endpoint backed by key.
func jwksServer(t *testing.T, key *rsa.PrivateKey, kid string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   base,
			"jwks_uri": base + "/protocol/openid-connect/certs",
		})
	})
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": n, "e": e},
			},
		})
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	return srv
}

func TestVerifyValidToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const kid = "test-kid"
	srv := jwksServer(t, key, kid)
	defer srv.Close()

	v := newJWTVerifier(srv.URL, "openclaw", srv.Client())
	tok := signJWT(t, key, kid, map[string]any{
		"iss": srv.URL,
		"sub": "user-abc-123",
		"azp": "openclaw",
		"exp": time.Now().Add(time.Hour).Unix(),
		"nbf": time.Now().Add(-time.Minute).Unix(),
	})
	claims, err := v.verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if claims.Subject != "user-abc-123" {
		t.Fatalf("sub = %q, want user-abc-123", claims.Subject)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const kid = "k"
	srv := jwksServer(t, key, kid)
	defer srv.Close()
	v := newJWTVerifier(srv.URL, "openclaw", srv.Client())
	tok := signJWT(t, key, kid, map[string]any{
		"iss": srv.URL, "sub": "u", "azp": "openclaw",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const kid = "k"
	srv := jwksServer(t, key, kid)
	defer srv.Close()
	v := newJWTVerifier(srv.URL, "openclaw", srv.Client())
	tok := signJWT(t, key, kid, map[string]any{
		"iss": "https://evil.example/realms/x", "sub": "u", "azp": "openclaw",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected issuer-mismatch token to be rejected")
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const kid = "k"
	srv := jwksServer(t, key, kid)
	defer srv.Close()
	v := newJWTVerifier(srv.URL, "openclaw", srv.Client())
	tok := signJWT(t, key, kid, map[string]any{
		"iss": srv.URL, "sub": "u", "azp": "some-other-client", "aud": "account",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected audience-mismatch token to be rejected")
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const kid = "k"
	srv := jwksServer(t, key, kid)
	defer srv.Close()
	v := newJWTVerifier(srv.URL, "openclaw", srv.Client())
	tok := signJWT(t, key, kid, map[string]any{
		"iss": srv.URL, "sub": "u", "azp": "openclaw",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	// Flip a character in the MIDDLE of the signature segment to a
	// different valid base64url char — guaranteed to change the decoded
	// signature bytes (flipping the final char can land in trailing
	// padding bits that decode to the same value).
	parts := strings.Split(tok, ".")
	sig := []byte(parts[2])
	mid := len(sig) / 2
	if sig[mid] == 'A' {
		sig[mid] = 'B'
	} else {
		sig[mid] = 'A'
	}
	tampered := parts[0] + "." + parts[1] + "." + string(sig)
	if _, err := v.verify(context.Background(), tampered); err == nil {
		t.Fatal("expected tampered signature to be rejected")
	}
}

func TestAudienceMatches(t *testing.T) {
	cases := []struct {
		name   string
		claims jwtClaims
		want   bool
	}{
		{"azp match", jwtClaims{AZP: "openclaw"}, true},
		{"aud string match", jwtClaims{Audience: "openclaw"}, true},
		{"aud array match", jwtClaims{Audience: []any{"account", "openclaw"}}, true},
		{"no match", jwtClaims{AZP: "x", Audience: []any{"account"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := audienceMatches(tc.claims, "openclaw"); got != tc.want {
				t.Fatalf("audienceMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodNameSanitization(t *testing.T) {
	cases := map[string]string{
		"7283eb4a-19e5-4e86-9066-d4aa26762064": "openclaw-user-7283eb4a-19e5-4e86-9066-d4aa26762064",
		"User_With.Bad@Chars":                  "openclaw-user-user-with-bad-chars",
	}
	for in, want := range cases {
		if got := podName(in); got != want {
			t.Fatalf("podName(%q) = %q, want %q", in, got, want)
		}
	}
	// RFC1123 label constraints: lowercase, <=63 chars, no leading/trailing dash.
	long := strings.Repeat("a", 200)
	got := podName(long)
	if len(got) > 63 {
		t.Fatalf("podName too long: %d chars", len(got))
	}
	if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
		t.Fatalf("podName has dangling dash: %q", got)
	}
}

func TestInjectIdleAnnotation(t *testing.T) {
	manifest := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: openclaw-user-x\n  labels:\n    a: b\nspec:\n  containers: []\n"
	out := injectIdleAnnotation(manifest, "2026-06-24T00:00:00Z")
	if !strings.Contains(out, "catalyst.openova.io/openclaw-last-seen") {
		t.Fatalf("annotation not injected:\n%s", out)
	}
	if !strings.Contains(out, "2026-06-24T00:00:00Z") {
		t.Fatalf("timestamp not present:\n%s", out)
	}
	// Idempotent — re-injecting must not duplicate.
	out2 := injectIdleAnnotation(out, "2026-06-25T00:00:00Z")
	if strings.Count(out2, "openclaw-last-seen") != 1 {
		t.Fatalf("annotation duplicated on re-inject:\n%s", out2)
	}
}

func TestRenderTemplateSubstitutes(t *testing.T) {
	s := &podSpawner{
		cfg:      &config{},
		template: "metadata:\n  name: openclaw-user-${USER_UUID}\n  labels:\n    secret: ${SECRET_NAME}\n",
	}
	out := s.renderTemplate("abc-123")
	if !strings.Contains(out, "openclaw-user-abc-123") {
		t.Fatalf("USER_UUID not substituted:\n%s", out)
	}
	if !strings.Contains(out, "newapi-key-abc-123") {
		t.Fatalf("SECRET_NAME not substituted:\n%s", out)
	}
}

func TestHealthzAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	healthzHandler(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}
}
