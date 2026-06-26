package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

// makeCA returns a self-signed CA cert (the stand-in for the in-cluster
// api-server CA) plus a leaf cert signed by it (the stand-in for the
// api-server's serving cert). The CA is returned PEM-encoded.
func makeCA(t *testing.T) (caPEM []byte, leaf *x509.Certificate) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-cluster-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "kubernetes.default.svc"},
		DNSNames:     []string{"kubernetes.default.svc"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	leaf, err = x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	return caPEM, leaf
}

// TestRootPoolTrustsBothPublicAndClusterCA is the #4407 regression guard:
// the controller shares one http.Client for the in-cluster api-server
// (cluster-CA-signed) and the PUBLIC OIDC issuer (Let's-Encrypt-signed).
// The trust pool must verify a cert chaining to the appended cluster CA
// AND must still carry the public system roots (the old code REPLACED the
// system pool with only the cluster CA → public LE issuer failed with
// `x509: certificate signed by unknown authority`).
func TestRootPoolTrustsBothPublicAndClusterCA(t *testing.T) {
	caPEM, clusterLeaf := makeCA(t)

	// Baseline: how many roots does the system pool carry on its own?
	sysPool, sysErr := x509.SystemCertPool()
	systemRootCount := 0
	if sysErr == nil && sysPool != nil {
		systemRootCount = len(sysPool.Subjects())
	}

	pool := rootPoolFrom(caPEM)
	if pool == nil {
		t.Fatal("rootPoolFrom returned nil with a cluster CA supplied")
	}

	// (1) The appended in-cluster CA must verify its own leaf.
	if _, err := clusterLeaf.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("cluster-CA-signed leaf must verify against the pool: %v", err)
	}

	// (2) The public system roots must be PRESERVED, not replaced. If the
	// host carries system roots, the combined pool must be strictly larger
	// than just the one cluster CA (proving the system roots survived the
	// append). This is the exact property the old `RootCAs = clusterPool`
	// code violated.
	if systemRootCount > 0 {
		clusterOnly := x509.NewCertPool()
		if !clusterOnly.AppendCertsFromPEM(caPEM) {
			t.Fatal("failed to build cluster-only reference pool")
		}
		if got, want := len(pool.Subjects()), len(clusterOnly.Subjects()); got <= want {
			t.Fatalf("combined pool has %d subjects, want strictly more than the %d cluster-only subjects (system roots dropped)", got, want)
		}
		if len(pool.Subjects()) < systemRootCount {
			t.Fatalf("combined pool (%d) lost system roots (had %d)", len(pool.Subjects()), systemRootCount)
		}
	} else {
		t.Log("host has no system root pool; skipping public-root-preservation assertion")
	}
}

// TestRootPoolWithoutClusterCAKeepsSystemRoots covers the off-cluster path
// (no SA CA file): the pool must still be the system pool, so the public
// OIDC issuer remains verifiable.
func TestRootPoolWithoutClusterCAKeepsSystemRoots(t *testing.T) {
	sysPool, sysErr := x509.SystemCertPool()
	pool := rootPoolFrom(nil)
	if sysErr == nil && sysPool != nil {
		if pool == nil {
			t.Fatal("rootPoolFrom(nil) returned nil despite available system roots")
		}
		if len(pool.Subjects()) < len(sysPool.Subjects()) {
			t.Fatalf("off-cluster pool dropped system roots: got %d, want >= %d", len(pool.Subjects()), len(sysPool.Subjects()))
		}
	}
}

// TestBuildAPIClientTLSConfigured confirms the shared client is built with
// a configured (non-empty) RootCAs / TLS-min-version, i.e. the OIDC
// verifier it's handed in main() trusts a real root set.
func TestBuildAPIClientTLSConfigured(t *testing.T) {
	c := buildAPIClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS1.2", tr.TLSClientConfig.MinVersion)
	}
	// On any host with a CA bundle, RootCAs must be a populated pool (the
	// system roots) — never nil-with-no-fallback-and-no-trust.
	if sysPool, err := x509.SystemCertPool(); err == nil && sysPool != nil && len(sysPool.Subjects()) > 0 {
		if tr.TLSClientConfig.RootCAs == nil {
			t.Fatal("RootCAs is nil despite available system roots — public issuer would not verify")
		}
	}
}
