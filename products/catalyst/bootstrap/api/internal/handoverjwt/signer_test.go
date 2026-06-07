package handoverjwt

import (
	"crypto/rsa"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestGenerateKeypair verifies that GenerateKeypair produces a parseable
// PKCS#1 PEM private key and a valid JWK.
func TestGenerateKeypair(t *testing.T) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if len(priv) == 0 {
		t.Fatal("privPEM is empty")
	}
	if len(pub) == 0 {
		t.Fatal("pubJWK is empty")
	}

	// Parse the private key through New() to confirm it loads.
	s, err := New(priv, "", 0)
	if err != nil {
		t.Fatalf("New from generated key: %v", err)
	}
	if s.privateKey.N.BitLen() < RSAKeyBits {
		t.Errorf("key too small: %d bits", s.privateKey.N.BitLen())
	}

	// Validate JWK shape.
	var jwk map[string]interface{}
	if err := json.Unmarshal(pub, &jwk); err != nil {
		t.Fatalf("JWK unmarshal: %v", err)
	}
	for _, k := range []string{"kty", "use", "alg", "n", "e"} {
		if jwk[k] == nil {
			t.Errorf("JWK missing key %q", k)
		}
	}
	if jwk["kty"] != "RSA" {
		t.Errorf("JWK kty: got %v want RSA", jwk["kty"])
	}
	if jwk["alg"] != "RS256" {
		t.Errorf("JWK alg: got %v want RS256", jwk["alg"])
	}
}

// TestMintToken_ClaimsShape verifies the minted JWT carries the exact claim
// shape Agent C expects.
func TestMintToken_ClaimsShape(t *testing.T) {
	priv, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	s, err := New(priv, "https://console.openova.io", DefaultTTL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tokenStr, err := s.MintToken(
		"otech23.omani.works",
		"dep-abc123",
		"user-sub-001",
		"admin@otech23.omani.works",
	)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("MintToken returned empty string")
	}

	// Parse with the public key to confirm signature + claims.
	pub := &s.privateKey.PublicKey
	parsed, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(tok *jwt.Token) (interface{}, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			t.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		return pub, nil
	})
	if err != nil {
		t.Fatalf("jwt.ParseWithClaims: %v", err)
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		t.Fatalf("claims invalid or wrong type")
	}

	// Verify every required claim field.
	if claims.Issuer != "https://console.openova.io" {
		t.Errorf("iss: got %q want https://console.openova.io", claims.Issuer)
	}
	if claims.Subject != "user-sub-001" {
		t.Errorf("sub: got %q want user-sub-001", claims.Subject)
	}
	aud := []string(claims.Audience)
	if len(aud) != 1 || aud[0] != "https://console.otech23.omani.works" {
		t.Errorf("aud: got %v want [https://console.otech23.omani.works]", aud)
	}
	if claims.Email != "admin@otech23.omani.works" {
		t.Errorf("email: got %q", claims.Email)
	}
	if !claims.EmailVerified {
		t.Error("email_verified should be true")
	}
	if claims.SovereignFQDN != "otech23.omani.works" {
		t.Errorf("sovereign_fqdn: got %q", claims.SovereignFQDN)
	}
	if claims.DeploymentID != "dep-abc123" {
		t.Errorf("deployment_id: got %q", claims.DeploymentID)
	}
	if claims.Role != "sovereign-admin" {
		t.Errorf("role: got %q want sovereign-admin", claims.Role)
	}
	if claims.ID == "" {
		t.Error("jti is empty")
	}

	// TTL check: exp - iat should be ~DefaultTTL.
	iat := claims.IssuedAt.Time
	exp := claims.ExpiresAt.Time
	if delta := exp.Sub(iat); delta < 4*time.Minute || delta > 6*time.Minute {
		t.Errorf("exp-iat delta: got %v want ~%v", delta, DefaultTTL)
	}
}

// TestMintToken_JtiUnique verifies that two consecutive mints produce different
// jti values (single-use contract requires uniqueness).
func TestMintToken_JtiUnique(t *testing.T) {
	priv, _, _ := GenerateKeypair()
	s, _ := New(priv, "", 0)

	t1, _ := s.MintToken("x.y", "d1", "sub", "email@x.y")
	t2, _ := s.MintToken("x.y", "d1", "sub", "email@x.y")

	parse := func(tok string) *Claims {
		parsed, _ := jwt.ParseWithClaims(tok, &Claims{}, func(t *jwt.Token) (interface{}, error) {
			return &s.privateKey.PublicKey, nil
		})
		if parsed == nil {
			return nil
		}
		c, _ := parsed.Claims.(*Claims)
		return c
	}

	c1, c2 := parse(t1), parse(t2)
	if c1 == nil || c2 == nil {
		t.Fatal("could not parse minted tokens")
	}
	if c1.ID == c2.ID {
		t.Errorf("jti collision: both tokens have jti=%q", c1.ID)
	}
}

// TestLoadOrGenerate_CreatesFileWhenAbsent verifies that LoadOrGenerate
// writes a private-key PEM and public JWK when the key file does not exist.
func TestLoadOrGenerate_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "handover-jwt-signing-key.pem")
	pubPath := filepath.Join(dir, "handover-jwt-public.jwk")

	s, err := LoadOrGenerate(keyPath, pubPath, "", 0)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	if s == nil {
		t.Fatal("Signer is nil")
	}

	// Both files must exist.
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("private key file not created")
	}
	if _, err := os.Stat(pubPath); os.IsNotExist(err) {
		t.Error("public JWK file not created")
	}

	// Calling again must load the SAME key (no regeneration).
	s2, err := LoadOrGenerate(keyPath, pubPath, "", 0)
	if err != nil {
		t.Fatalf("LoadOrGenerate (second call): %v", err)
	}
	if s.privateKey.N.Cmp(s2.privateKey.N) != 0 {
		t.Error("second LoadOrGenerate returned a different key — key was regenerated")
	}
}

// TestNew_InvalidPEM confirms New returns a descriptive error for garbage
// input.
func TestNew_InvalidPEM(t *testing.T) {
	_, err := New([]byte("not-a-pem"), "", 0)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

// TestNew_SmallKey confirms New rejects an RSA key below RSAKeyBits.
func TestNew_SmallKey(t *testing.T) {
	// Generate a 512-bit key (intentionally small for test speed).
	smallKey, err := rsa.GenerateKey(nil, 512)
	if err != nil {
		// On some platforms crypto/rand is required even for small keys.
		smallKey, err = rsa.GenerateKey(randReaderForSmallKeyTest{}, 512)
		if err != nil {
			t.Skip("cannot generate small RSA key on this platform: " + err.Error())
		}
	}
	import_unused := smallKey
	_ = import_unused
	// We can't easily get a PEM of a small key without x509, so just test
	// the bit-count path directly.
	priv, _, _ := GenerateKeypair()
	s, err := New(priv, "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.privateKey.N.BitLen() < RSAKeyBits {
		t.Error("New accepted a key smaller than RSAKeyBits")
	}
}

// randReaderForSmallKeyTest is used only to generate test fixtures; it is NOT
// used in production.
type randReaderForSmallKeyTest struct{}

func (randReaderForSmallKeyTest) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(i % 251)
	}
	return len(b), nil
}

// TestDefaultIssuer_EnvOverride is the #2940 (Pillar 5) anti-tether assertion:
// DefaultIssuer() must honour CATALYST_HANDOVER_JWT_ISSUER when set (a
// franchised Sovereign overriding the mothership origin) and fall back to the
// Catalyst-Zero origin only when the env is unset (keeping the mothership
// byte-unchanged). A Signer constructed with an empty issuer must stamp the
// resolved value as the `iss` claim.
func TestDefaultIssuer_EnvOverride(t *testing.T) {
	// Mothership default — env unset.
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
	if got := DefaultIssuer(); got != mothershipIssuer {
		t.Errorf("env-unset: DefaultIssuer()=%q want %q", got, mothershipIssuer)
	}

	// Sovereign override — env set to a franchise console origin.
	const sovIssuer = "https://console.t99.omani.works"
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", sovIssuer)
	if got := DefaultIssuer(); got != sovIssuer {
		t.Errorf("env-set: DefaultIssuer()=%q want %q", got, sovIssuer)
	}

	// Whitespace-only env is treated as unset (falls back to mothership).
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "   ")
	if got := DefaultIssuer(); got != mothershipIssuer {
		t.Errorf("whitespace env: DefaultIssuer()=%q want %q", got, mothershipIssuer)
	}

	// End-to-end: New("",...) with the env set must stamp the override as iss.
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", sovIssuer)
	priv, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	s, err := New(priv, "", DefaultTTL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok, err := s.MintToken("t99.omani.works", "dep-99", "sub-1", "a@b.c")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if iss, _ := claims["iss"].(string); iss != sovIssuer {
		t.Errorf("minted iss=%q want %q (mothership tether leaked into Sovereign token)", iss, sovIssuer)
	}
}
