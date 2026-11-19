// Package auth checks the bearer token of a call to cortex and derives the caller's principals.
//
// A token of the Organization's realm is accepted when every check holds, in this order:
//
//	format     a compact JWS of three base64url parts whose header and claims are JSON objects,
//	           with no crit header
//	algorithm  the header's alg is RS256, PS256 or ES256
//	signature  the header's kid names a key of the realm's key set of a type that fits alg, and
//	           the signature verifies with it. The key set is cached; an unknown kid fetches it
//	           again, at most once per RefreshInterval
//	issuer     iss equals the configured issuer, the value identity.issuerURL
//	audience   aud, a string or a list, contains Audience
//	expiry     exp is present and in the future
//
// The first check that fails ends the call with an *Error; there is no other way to identify a
// caller. The principals of an accepted token are user:<sub>, user:<email> when email_verified
// is true, one group:<value> per entry of the groups claim, and org:members.
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Audience is the value the aud claim of a token for cortex contains.
const Audience = "cortex"

// RefreshInterval is the shortest time between a fetch of the key set that succeeded and the next.
const RefreshInterval = time.Minute

// FetchTimeout bounds one fetch of the key set, which runs detached from the call that asked.
const FetchTimeout = 30 * time.Second

// Algorithms are the signature algorithms a token may use.
var Algorithms = []string{"RS256", "PS256", "ES256"}

// Error is a token that failed one check. Check is format, algorithm, signature, issuer,
// audience or expiry.
type Error struct {
	Check  string
	Reason string
}

func (e *Error) Error() string { return "auth: " + e.Check + ": " + e.Reason }

func fail(check, format string, a ...any) *Error {
	return &Error{Check: check, Reason: fmt.Sprintf(format, a...)}
}

// Caller is the identity of an accepted token.
type Caller struct {
	Subject    string
	Principals []string
}

// KeySource returns the realm's key set, a JSON Web Key Set document.
type KeySource func(ctx context.Context) ([]byte, error)

// Config configures a Checker.
type Config struct {
	Issuer string           // the realm's issuer, an https address
	Keys   KeySource        // the realm's key set
	Now    func() time.Time // nil: time.Now
}

// Checker checks tokens. It is safe for concurrent use.
type Checker struct {
	cfg Config

	mu       sync.Mutex
	keys     map[string]any // kid -> *rsa.PublicKey or *ecdsa.PublicKey
	algs     map[string]string
	fetched  time.Time     // the end of the last fetch that succeeded
	fetchErr error         // the error of the last fetch
	flight   chan struct{} // closed when the fetch in flight ends; nil while none runs
}

// New checks the configuration and returns a Checker.
func New(cfg Config) (*Checker, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("auth: issuer %q is not an https address", cfg.Issuer)
	}
	if cfg.Keys == nil {
		return nil, errors.New("auth: no key source")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Checker{cfg: cfg}, nil
}

// DiscoveryKeySource reads the issuer's OpenID configuration and then the key set it names.
// The configuration must name the same issuer.
func DiscoveryKeySource(client *http.Client, issuer string) KeySource {
	return func(ctx context.Context) ([]byte, error) {
		doc, err := get(ctx, client, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
		if err != nil {
			return nil, err
		}
		var meta struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := json.Unmarshal(doc, &meta); err != nil {
			return nil, fmt.Errorf("auth: the OpenID configuration: %w", err)
		}
		if meta.Issuer != issuer || !strings.HasPrefix(meta.JWKSURI, "https://") {
			return nil, fmt.Errorf("auth: the OpenID configuration names issuer %q and key set %q", meta.Issuer, meta.JWKSURI)
		}
		return get(ctx, client, meta.JWKSURI)
	}
}

func get(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: %s answered %d", address, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// Check applies the checks in order and returns the caller of an accepted token.
func (c *Checker) Check(ctx context.Context, compact string) (Caller, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return Caller{}, fail("format", "the token is not a compact JWS")
	}
	var seg [3][]byte
	for i, p := range parts {
		b, err := base64.RawURLEncoding.DecodeString(p)
		if err != nil {
			return Caller{}, fail("format", "part %d is not base64url", i+1)
		}
		seg[i] = b
	}
	var header struct {
		Alg  string          `json:"alg"`
		Kid  string          `json:"kid"`
		Crit json.RawMessage `json:"crit"`
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(seg[0], &header) != nil || json.Unmarshal(seg[1], &raw) != nil || raw == nil {
		return Caller{}, fail("format", "the header or the claims are not a JSON object")
	}
	if header.Crit != nil {
		return Caller{}, fail("format", "the header has crit")
	}
	if !slices.Contains(Algorithms, header.Alg) {
		return Caller{}, fail("algorithm", "%q is not one of %v", header.Alg, Algorithms)
	}
	key, err := c.key(ctx, header.Kid, header.Alg)
	if err != nil {
		return Caller{}, err
	}
	if !verify(header.Alg, key, []byte(parts[0]+"."+parts[1]), seg[2]) {
		return Caller{}, fail("signature", "the signature does not verify")
	}
	var iss string
	if claim(raw, "iss", &iss) != nil || iss != c.cfg.Issuer {
		return Caller{}, fail("issuer", "iss %q is not the issuer", iss)
	}
	var aud []string
	var one string
	if claim(raw, "aud", &one) == nil && one != "" {
		aud = []string{one}
	} else if claim(raw, "aud", &aud) != nil {
		aud = nil
	}
	if !slices.Contains(aud, Audience) {
		return Caller{}, fail("audience", "aud %q does not contain %q", aud, Audience)
	}
	var exp float64
	if _, ok := raw["exp"]; !ok || claim(raw, "exp", &exp) != nil || math.IsNaN(exp) || math.Abs(exp) > 1e12 {
		return Caller{}, fail("expiry", "exp is missing or not a number")
	}
	if sec, frac := math.Modf(exp); !c.cfg.Now().Before(time.Unix(int64(sec), int64(frac*1e9))) {
		return Caller{}, fail("expiry", "the token has expired")
	}
	return principals(raw), nil
}

// principals derives the caller from the claims of an accepted token. A claim of the wrong
// type counts as absent.
func principals(raw map[string]json.RawMessage) Caller {
	var cl Caller
	var email string
	var verified bool
	var groups []string
	_ = claim(raw, "sub", &cl.Subject)
	_ = claim(raw, "email", &email)
	_ = claim(raw, "email_verified", &verified)
	_ = claim(raw, "groups", &groups)
	if cl.Subject != "" {
		cl.Principals = append(cl.Principals, "user:"+cl.Subject)
	}
	if verified && email != "" && email != cl.Subject {
		cl.Principals = append(cl.Principals, "user:"+email)
	}
	for _, g := range groups {
		if g != "" && !slices.Contains(cl.Principals, "group:"+g) {
			cl.Principals = append(cl.Principals, "group:"+g)
		}
	}
	cl.Principals = append(cl.Principals, "org:members")
	return cl
}

func claim(raw map[string]json.RawMessage, name string, v any) error {
	if b, ok := raw[name]; ok {
		return json.Unmarshal(b, v)
	}
	return nil
}

// key returns the key kid for alg. An unknown kid fetches the key set again, at most once per
// RefreshInterval after a fetch that succeeded. One fetch runs at a time and serves every caller
// that waits for it; a known kid never waits, and a caller that goes away stops waiting only.
func (c *Checker) key(ctx context.Context, kid, alg string) (any, error) {
	if kid == "" {
		return nil, fail("signature", "the header names no key")
	}
	c.mu.Lock()
	if _, ok := c.keys[kid]; !ok && (c.fetched.IsZero() || c.cfg.Now().Sub(c.fetched) >= RefreshInterval) {
		if c.flight == nil {
			c.flight = make(chan struct{})
			go c.fetch(context.WithoutCancel(ctx), c.flight)
		}
		flight := c.flight
		c.mu.Unlock()
		select {
		case <-flight:
		case <-ctx.Done():
			return nil, fail("signature", "key %q is unknown: %v", kid, ctx.Err())
		}
		c.mu.Lock()
	}
	k, ok := c.keys[kid]
	pinned, fetchErr := c.algs[kid], c.fetchErr
	c.mu.Unlock()
	if !ok && fetchErr != nil {
		return nil, fail("signature", "key %q is unknown; the key set cannot be read: %v", kid, fetchErr)
	} else if !ok {
		return nil, fail("signature", "key %q is unknown", kid)
	}
	_, isEC := k.(*ecdsa.PublicKey)
	if isEC != (alg == "ES256") || (pinned != "" && pinned != alg) {
		return nil, fail("signature", "key %q does not sign with %s", kid, alg)
	}
	return k, nil
}

// fetch reads the key set within FetchTimeout and closes done. A set that cannot be read keeps
// the keys known so far and does not start the RefreshInterval.
func (c *Checker) fetch(ctx context.Context, done chan struct{}) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	doc, err := c.cfg.Keys(ctx)
	var keys map[string]any
	var algs map[string]string
	if err == nil {
		keys, algs, err = parseKeySet(doc)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetchErr, c.flight = err, nil; err == nil {
		c.keys, c.algs, c.fetched = keys, algs, c.cfg.Now()
	}
	close(done)
}

// parseKeySet keeps the signature keys with a key identifier: RSA keys of at least 2048 bits
// and EC keys on P-256.
func parseKeySet(doc []byte) (map[string]any, map[string]string, error) {
	var set struct {
		Keys []struct{ Kty, Use, Kid, Alg, N, E, Crv, X, Y string } `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, nil, err
	}
	keys, algs := map[string]any{}, map[string]string{}
	b64 := base64.RawURLEncoding.DecodeString
	for _, k := range set.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := b64(k.N)
			e, err2 := b64(k.E)
			if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
				continue
			}
			pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n)}
			for _, b := range e {
				pub.E = pub.E<<8 | int(b)
			}
			if pub.N.BitLen() < 2048 || pub.E < 3 {
				continue
			}
			keys[k.Kid] = pub
		case "EC":
			x, err1 := b64(k.X)
			y, err2 := b64(k.Y)
			if k.Crv != "P-256" || err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue
			}
			keys[k.Kid] = pub
		default:
			continue
		}
		algs[k.Kid] = k.Alg
	}
	return keys, algs, nil
}

// verify checks the signature sig of signed with key under alg.
func verify(alg string, key any, signed, sig []byte) bool {
	digest := sha256.Sum256(signed)
	switch k := key.(type) {
	case *rsa.PublicKey:
		if alg == "PS256" {
			return rsa.VerifyPSS(k, crypto.SHA256, digest[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) == nil
	case *ecdsa.PublicKey:
		if len(sig) != 64 {
			return false
		}
		return ecdsa.Verify(k, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	}
	return false
}
