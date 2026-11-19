package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const issuer = "https://auth.acme.example/realms/acme"

var (
	keysOnce sync.Once
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
)

func testKeys(t *testing.T) {
	t.Helper()
	keysOnce.Do(func() {
		rsaKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		ecKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	})
	if rsaKey == nil || ecKey == nil {
		t.Fatal("no test keys")
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// keySet returns the key set with the RSA key as kid "r" and the EC key as kid "e".
func keySet(t *testing.T, extra ...map[string]string) []byte {
	testKeys(t)
	x, y := make([]byte, 32), make([]byte, 32)
	ecKey.X.FillBytes(x)
	ecKey.Y.FillBytes(y)
	keys := []map[string]string{
		{"kty": "RSA", "kid": "r", "use": "sig", "n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(rsaKey.E)).Bytes())},
		{"kty": "EC", "kid": "e", "crv": "P-256", "x": b64(x), "y": b64(y)},
	}
	doc, _ := json.Marshal(map[string]any{"keys": append(keys, extra...)})
	return doc
}

// mint signs claims with alg under kid; RS256 and PS256 use the RSA key, ES256 the EC key.
func mint(t *testing.T, alg, kid string, claims map[string]any) string {
	testKeys(t)
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	signed := b64(h) + "." + b64(c)
	digest := sha256.Sum256([]byte(signed))
	var sig []byte
	var err error
	switch alg {
	case "PS256":
		sig, err = rsa.SignPSS(rand.Reader, rsaKey, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case "ES256":
		var r, s *big.Int
		r, s, err = ecdsa.Sign(rand.Reader, ecKey, digest[:])
		if err == nil {
			sig = make([]byte, 64)
			r.FillBytes(sig[:32])
			s.FillBytes(sig[32:])
		}
	default:
		sig, err = rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func good(more map[string]any) map[string]any {
	c := map[string]any{"iss": issuer, "aud": []string{"account", Audience}, "exp": now.Add(time.Minute).Unix(), "sub": "u-1"}
	for k, v := range more {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func checker(t *testing.T) (*Checker, *int) {
	fetches := new(int)
	c, err := New(Config{Issuer: issuer, Now: func() time.Time { return now },
		Keys: func(context.Context) ([]byte, error) { *fetches++; return keySet(t), nil }})
	if err != nil {
		t.Fatal(err)
	}
	return c, fetches
}

// refused asserts that token fails exactly the check want.
func refused(t *testing.T, c *Checker, token, want string) {
	t.Helper()
	_, err := c.Check(context.Background(), token)
	var e *Error
	if !errors.As(err, &e) || e.Check != want {
		t.Errorf("want a failure of check %s, got %v", want, err)
	}
}

func TestTokenFormat(t *testing.T) {
	c, _ := checker(t)
	ok := mint(t, "RS256", "r", good(nil))
	parts := strings.Split(ok, ".")
	for _, bad := range []string{"", "a.b", ok + ".x", parts[0] + ".%%%." + parts[2], b64([]byte("[]")) + "." + parts[1] + "." + parts[2],
		withHeader(ok, map[string]any{"alg": "RS256", "kid": "r", "crit": []string{"exp"}})} {
		refused(t, c, bad, "format")
	}
}

// withHeader replaces the header of token.
func withHeader(token string, header map[string]any) string {
	h, _ := json.Marshal(header)
	return b64(h) + token[strings.Index(token, "."):]
}

func TestTokenAlgorithm(t *testing.T) {
	c, _ := checker(t)
	for _, alg := range []string{"none", "HS256", "RS384", "ES384", ""} {
		refused(t, c, withHeader(mint(t, "RS256", "r", good(nil)), map[string]any{"alg": alg, "kid": "r"}), "algorithm")
	}
	for alg, kid := range map[string]string{"RS256": "r", "PS256": "r", "ES256": "e"} {
		if _, err := c.Check(context.Background(), mint(t, alg, kid, good(nil))); err != nil {
			t.Errorf("%s: %v", alg, err)
		}
	}
}

func TestTokenSignature(t *testing.T) {
	c, _ := checker(t)
	ok := strings.Split(mint(t, "RS256", "r", good(nil)), ".")
	other := strings.Split(mint(t, "RS256", "r", good(map[string]any{"sub": "u-2"})), ".")
	refused(t, c, ok[0]+"."+other[1]+"."+ok[2], "signature")        // the claims were changed
	refused(t, c, mint(t, "RS256", "", good(nil)), "signature")     // no kid
	refused(t, c, mint(t, "RS256", "gone", good(nil)), "signature") // unknown kid
	refused(t, c, mint(t, "ES256", "r", good(nil)), "signature")    // an RSA key named for ES256
	refused(t, c, mint(t, "RS256", "e", good(nil)), "signature")    // an EC key named for RS256
	pinned, _ := New(Config{Issuer: issuer, Now: func() time.Time { return now }, Keys: func(context.Context) ([]byte, error) {
		return []byte(strings.Replace(string(keySet(t)), `"kid":"r"`, `"alg":"PS256","kid":"r"`, 1)), nil
	}})
	refused(t, pinned, mint(t, "RS256", "r", good(nil)), "signature") // the key names another algorithm
	if _, err := pinned.Check(context.Background(), mint(t, "PS256", "r", good(nil))); err != nil {
		t.Errorf("PS256 with a PS256 key: %v", err)
	}

	// Keys the set holds that no token may use: RSA under 2048 bits, EC on P-384, use enc.
	const weakBits = 1024 // under the 2048 bits a signing key needs: the checker must refuse it
	small, _ := rsa.GenerateKey(rand.Reader, weakBits)
	x, y := make([]byte, 32), make([]byte, 32)
	ecKey.X.FillBytes(x)
	ecKey.Y.FillBytes(y)
	set := keySet(t, map[string]string{"kty": "RSA", "kid": "small", "n": b64(small.N.Bytes()), "e": "AQAB"},
		map[string]string{"kty": "EC", "kid": "p384", "crv": "P-384", "x": b64(x), "y": b64(y)},
		map[string]string{"kty": "RSA", "kid": "enc", "use": "enc", "n": b64(rsaKey.N.Bytes()), "e": "AQAB"})
	odd, _ := New(Config{Issuer: issuer, Now: func() time.Time { return now }, Keys: func(context.Context) ([]byte, error) { return set, nil }})
	if _, err := odd.Check(context.Background(), mint(t, "RS256", "r", good(nil))); err != nil {
		t.Fatal(err)
	}
	rsaKey, small = small, rsaKey
	weak := mint(t, "RS256", "small", good(nil))
	rsaKey = small
	refused(t, odd, weak, "signature")
	refused(t, odd, mint(t, "ES256", "p384", good(nil)), "signature")
	refused(t, odd, mint(t, "RS256", "enc", good(nil)), "signature")
}

func TestTokenKeyRefresh(t *testing.T) {
	c, fetches := checker(t)
	for i := 0; i < 3; i++ {
		if _, err := c.Check(context.Background(), mint(t, "RS256", "r", good(nil))); err != nil {
			t.Fatal(err)
		}
	}
	refused(t, c, mint(t, "RS256", "new", good(nil)), "signature")
	if *fetches != 1 {
		t.Fatalf("the key set was fetched %d times; a known key and an unknown key within a minute fetch it once", *fetches)
	}
	now = now.Add(RefreshInterval)
	defer func() { now = now.Add(-RefreshInterval) }()
	refused(t, c, mint(t, "RS256", "new", good(map[string]any{"exp": now.Add(time.Minute).Unix()})), "signature")
	if *fetches != 2 {
		t.Fatalf("after a minute an unknown key fetched the key set %d times in all, want 2", *fetches)
	}

	// The realm answers a key set that is not JSON, then one with a rotated key "new".
	testKeys(t)
	rotated := keySet(t, map[string]string{"kty": "RSA", "kid": "new", "n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(rsaKey.E)).Bytes())})
	answers := [][]byte{keySet(t), []byte("<html>busy</html>"), rotated}
	at := now
	rc, err := New(Config{Issuer: issuer, Now: func() time.Time { return at },
		Keys: func(context.Context) ([]byte, error) { a := answers[0]; answers = answers[1:]; return a, nil }})
	if err != nil {
		t.Fatal(err)
	}
	token := func(kid string) string {
		return mint(t, "RS256", kid, good(map[string]any{"exp": at.Add(time.Minute).Unix()}))
	}
	if _, err := rc.Check(context.Background(), token("r")); err != nil {
		t.Fatal(err)
	}
	at = at.Add(RefreshInterval)
	refused(t, rc, token("new"), "signature")
	if _, err := rc.Check(context.Background(), token("r")); err != nil {
		t.Errorf("a key set that is not JSON dropped the known keys: %v", err)
	}
	if _, err := rc.Check(context.Background(), token("new")); err != nil || len(answers) != 0 {
		t.Errorf("a failed fetch used up the refresh: %v, %d answers left", err, len(answers))
	}

	// A caller with an unknown kid that goes away does not stop the next caller from loading
	// the key set, and a known kid is verified while a fetch is in flight.
	started, release, calls := make(chan bool, 1), make(chan bool), 0
	fc, _ := New(Config{Issuer: issuer, Now: func() time.Time { return at }, Keys: func(ctx context.Context) ([]byte, error) {
		if calls++; calls > 1 {
			started <- true
			<-release
			return rotated, nil
		}
		return keySet(t), ctx.Err()
	}})
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fc.Check(gone, token("gone")); err == nil {
		t.Error("an unknown kid was accepted")
	}
	if _, err := fc.Check(context.Background(), token("r")); err != nil {
		t.Errorf("a caller that went away kept the key set from loading: %v", err)
	}
	at = at.Add(RefreshInterval)
	known, rotatedTok, result, got := token("r"), token("new"), make(chan error, 1), make(chan error, 1)
	go func() { _, err := fc.Check(context.Background(), rotatedTok); result <- err }()
	<-started
	go func() { _, err := fc.Check(context.Background(), known); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("a known kid waited for the fetch in flight")
	}
	close(release)
	if err := <-result; err != nil {
		t.Errorf("the rotated key after the fetch in flight: %v", err)
	}
}

func TestTokenIssuer(t *testing.T) {
	c, _ := checker(t)
	for _, iss := range []any{nil, "https://auth.other.example/realms/acme", issuer + "/", 7} {
		refused(t, c, mint(t, "RS256", "r", good(map[string]any{"iss": iss})), "issuer")
	}
}

func TestTokenAudience(t *testing.T) {
	c, _ := checker(t)
	for _, aud := range []any{nil, "account", []string{"account", "openova-mcp"}, 1} {
		refused(t, c, mint(t, "RS256", "r", good(map[string]any{"aud": aud})), "audience")
	}
	if _, err := c.Check(context.Background(), mint(t, "RS256", "r", good(map[string]any{"aud": Audience}))); err != nil {
		t.Errorf("aud as one string: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	c, _ := checker(t)
	for _, exp := range []any{nil, now.Unix(), now.Add(-time.Second).Unix(), "tomorrow"} {
		refused(t, c, mint(t, "RS256", "r", good(map[string]any{"exp": exp})), "expiry")
	}
}

func TestTokenPrincipals(t *testing.T) {
	c, _ := checker(t)
	got, err := c.Check(context.Background(), mint(t, "RS256", "r", good(map[string]any{
		"email": "ann@acme.example", "email_verified": true, "groups": []string{"hr-admins", "board", "hr-admins"}})))
	want := []string{"user:u-1", "user:ann@acme.example", "group:hr-admins", "group:board", "org:members"}
	if err != nil || !reflect.DeepEqual(got.Principals, want) || got.Subject != "u-1" {
		t.Fatalf("got %+v, %v; want %v", got, err, want)
	}
	got, err = c.Check(context.Background(), mint(t, "RS256", "r", good(map[string]any{"email": "ann@acme.example", "email_verified": false, "groups": "board"})))
	if err != nil || !reflect.DeepEqual(got.Principals, []string{"user:u-1", "org:members"}) {
		t.Fatalf("an unverified e-mail and a groups claim of the wrong type gave %+v, %v", got, err)
	}
}

func TestDiscoveryKeySource(t *testing.T) {
	var named string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/acme/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": named, "jwks_uri": "https://" + r.Host + "/certs"})
		case "/certs":
			_, _ = w.Write(keySet(t))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	iss := srv.URL + "/realms/acme"
	named = iss
	doc, err := DiscoveryKeySource(srv.Client(), iss)(context.Background())
	if err != nil || !strings.Contains(string(doc), `"kid":"e"`) {
		t.Fatalf("got %s, %v", doc, err)
	}
	named = "https://elsewhere.example/realms/acme"
	if _, err := DiscoveryKeySource(srv.Client(), iss)(context.Background()); err == nil {
		t.Fatal("a configuration that names another issuer was accepted")
	}
	if _, err := New(Config{Issuer: "http://auth.acme.example", Keys: DiscoveryKeySource(srv.Client(), iss)}); err == nil {
		t.Fatal("an http issuer was accepted")
	}
	if _, err := New(Config{Issuer: issuer}); err == nil {
		t.Fatal("a checker without a key source was accepted")
	}
}
