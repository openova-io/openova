// Package token applies the token rules T1 to T10 of the open-banking resource server.
//
// Check runs the rules in order on every call to a resource path. The first failure ends
// the call and is returned as an *Error that names the rule and the HTTP status. Verify
// runs rules T1 to T5 alone, for a listener that needs only a verified token of the realm.
// There is no observe mode and no setting that weakens a rule.
//
//	T1  The algorithm is in the profile's signingAlgorithms. Refused before any key is fetched.
//	T2  The signature verifies against the realm's key set (cached; refreshed on an unknown
//	    key identifier, at most once a minute).
//	T3  iss equals the configured issuer, an https address.
//	T4  aud contains the configured audience.
//	T5  exp is present and in the future; iat and nbf, when present, are at most 60 s ahead.
//	T6  cnf carries x5t#S256 and it equals the thumbprint of the verified client certificate.
//	T7  The client identifier names an application whose partner is approved and whose pins
//	    contain that thumbprint.
//	T8  The scope holds every scope the document assigns to the operation.
//	T9  Customer operations: the consent claim is present; the consent exists, belongs to the
//	    client, is authorised and not expired, is of the operation's family, grants a needed
//	    permission, and holds the account or consent the call addresses.
//	T10 Client-credentials operations: no consent claim, and the operation's product is in
//	    the application's products.
//
// T1 to T6 fail with 401, T7 to T10 with 403. A 401 answer carries the header
// WWW-Authenticate with the value Challenge. An error that is not an *Error means the
// check could not run (a key set or a lookup failed), not that a rule failed.
package token

import (
	"context"
	"crypto"
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

	"github.com/openova-io/openova/products/fingate/internal/profile"
)

// ConsentClaim is the claim that carries the consent identifier when the profile's
// consentCarrier is intent-claim.
const ConsentClaim = "openbanking_intent_id"

// Challenge is the value of the header WWW-Authenticate on a 401 answer.
const Challenge = `Bearer error="invalid_token"`

// The statuses of a failed rule.
const (
	StatusUnauthorized = 401 // rules T1 to T6
	StatusForbidden    = 403 // rules T7 to T10
)

// Skew is how far ahead of the clock iat and nbf may be (rule T5).
const Skew = 60 * time.Second

// RefreshInterval is the shortest time between two fetches of the key set (rule T2).
const RefreshInterval = time.Minute

// Error is a failed rule.
type Error struct {
	Rule   string // T1 to T10
	Status int    // StatusUnauthorized or StatusForbidden
	Reason string
}

func (e *Error) Error() string { return "token: rule " + e.Rule + ": " + e.Reason }

func fail(rule, format string, a ...any) *Error {
	status := StatusForbidden
	switch rule {
	case "T1", "T2", "T3", "T4", "T5", "T6":
		status = StatusUnauthorized
	}
	return &Error{Rule: rule, Status: status, Reason: fmt.Sprintf(format, a...)}
}

// Grant says how the documents let an operation be called.
type Grant int

const (
	// GrantClient is an operation called with a client-credentials token (TPPOAuth2Security).
	GrantClient Grant = iota + 1
	// GrantCustomer is an operation a customer consented to (PSUOAuth2Security).
	GrantCustomer
)

// Operation describes the operation a call addresses.
type Operation struct {
	Grant   Grant
	Scopes  []string // the scopes the document lists for the operation; the token must hold each
	Product string   // accounts, payments, funds-confirmation, vrp or events (rule T10)

	// For GrantCustomer only (rule T9).
	ConsentFamily string   // account-access, payment, funds-confirmation or vrp
	Permissions   []string // the consent must grant one of them; empty: no permission is needed
	AccountID     string   // the account the call addresses; empty: none
	ConsentID     string   // the consent the path or the body names; empty: none
}

// Request is one call to a resource path.
type Request struct {
	JWS        string // the compact JWS of the Authorization header, without "Bearer "
	Thumbprint string // Thumbprint of the client certificate verified on this connection
	Operation  Operation
}

// Claims are the verified claims of a token.
type Claims struct {
	Issuer    string
	Subject   string
	Audience  []string
	ExpiresAt time.Time
	Scopes    []string
	ClientID  string // client_id, or azp when client_id is absent
	ConsentID string // the consent claim; empty when absent
}

// Application is a partner application as the definitions hold it (rule T7).
type Application struct {
	PartnerState string   // approved, suspended or revoked
	Pins         []string // base64url SHA-256 of each pinned certificate
	Products     []string
}

// Consent is a stored consent as rule T9 reads it.
type Consent struct {
	ClientID    string
	Family      string
	State       string    // authorised is the only state that passes
	ExpiresAt   time.Time // zero: no expiry
	Permissions []string
	Accounts    []string
}

// Partners finds a partner application by its client identifier, <partner id>.<application id>.
type Partners interface {
	Application(ctx context.Context, clientID string) (Application, bool, error)
}

// Consents finds a consent by its identifier.
type Consents interface {
	Consent(ctx context.Context, id string) (Consent, bool, error)
}

// KeySource returns the realm's key set, a JSON Web Key Set document.
type KeySource func(ctx context.Context) ([]byte, error)

// Config configures a Checker.
type Config struct {
	Profile  *profile.Profile // its security.signingAlgorithms and security.consentCarrier are read
	Issuer   string           // the realm's issuer, an https address (rule T3)
	Audience string           // the resource server sets https://<parameters.host> (rule T4)
	Keys     KeySource        // rule T2
	Partners Partners         // rule T7; needed by Check, not by Verify
	Consents Consents         // rule T9; needed by Check, not by Verify
	Now      func() time.Time // nil: time.Now
}

// Checker applies the rules. It is safe for concurrent use.
type Checker struct {
	cfg        Config
	algorithms []string

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// New checks the configuration and returns a Checker.
func New(cfg Config) (*Checker, error) {
	if cfg.Profile == nil {
		return nil, errors.New("token: no profile")
	}
	algs := cfg.Profile.Security.SigningAlgorithms
	if len(algs) == 0 {
		return nil, errors.New("token: the profile names no signing algorithm")
	}
	for _, a := range algs {
		if a != "PS256" {
			return nil, fmt.Errorf("token: signing algorithm %q is not supported; supported: PS256", a)
		}
	}
	if c := cfg.Profile.Security.ConsentCarrier; c != "intent-claim" {
		return nil, fmt.Errorf("token: consent carrier %q is not supported; supported: intent-claim", c)
	}
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("token: issuer %q is not an https address", cfg.Issuer)
	}
	if cfg.Audience == "" {
		return nil, errors.New("token: no audience")
	}
	if cfg.Keys == nil {
		return nil, errors.New("token: no key source")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Checker{cfg: cfg, algorithms: slices.Clone(algs), keys: map[string]*rsa.PublicKey{}}, nil
}

// Thumbprint returns the base64url SHA-256 of a DER certificate, the form of x5t#S256.
func Thumbprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// HTTPKeySource fetches the key set from address with client.
func HTTPKeySource(client *http.Client, address string) KeySource {
	return func(ctx context.Context) ([]byte, error) {
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
			return nil, fmt.Errorf("key set %s: status %d", address, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
}

// Check applies rules T1 to T10 in order. The first failure ends the call.
func (c *Checker) Check(ctx context.Context, r Request) (Claims, error) {
	op := r.Operation
	if c.cfg.Partners == nil || c.cfg.Consents == nil {
		return Claims{}, errors.New("token: Check needs Partners and Consents")
	}
	if len(op.Scopes) == 0 || op.Product == "" ||
		(op.Grant != GrantClient && op.Grant != GrantCustomer) ||
		(op.Grant == GrantCustomer && op.ConsentFamily == "") {
		return Claims{}, errors.New("token: the operation descriptor is incomplete")
	}
	cl, raw, err := c.verify(ctx, r.JWS)
	if err != nil {
		return Claims{}, err
	}
	// T6
	var cnf map[string]json.RawMessage
	if err := decodeClaim(raw, "cnf", &cnf); err != nil || cnf == nil {
		return Claims{}, fail("T6", "cnf is missing or not an object")
	}
	var x5t string
	if err := decodeClaim(cnf, "x5t#S256", &x5t); err != nil || x5t == "" {
		return Claims{}, fail("T6", "cnf carries no x5t#S256")
	}
	if r.Thumbprint == "" || x5t != r.Thumbprint {
		return Claims{}, fail("T6", "cnf x5t#S256 is not the thumbprint of the client certificate")
	}
	// T7
	var azp string
	_ = decodeClaim(raw, "azp", &azp)
	if cl.ClientID == "" {
		return Claims{}, fail("T7", "the token names no client")
	}
	if azp != "" && azp != cl.ClientID {
		return Claims{}, fail("T7", "client_id %q and azp %q differ", cl.ClientID, azp)
	}
	app, found, err := c.cfg.Partners.Application(ctx, cl.ClientID)
	if err != nil {
		return Claims{}, fmt.Errorf("token: partner lookup: %w", err)
	}
	if !found {
		return Claims{}, fail("T7", "client %q is no partner application", cl.ClientID)
	}
	if app.PartnerState != "approved" {
		return Claims{}, fail("T7", "the partner of client %q is %q", cl.ClientID, app.PartnerState)
	}
	if !slices.Contains(app.Pins, r.Thumbprint) {
		return Claims{}, fail("T7", "the certificate is not pinned for client %q", cl.ClientID)
	}
	// T8
	for _, s := range op.Scopes {
		if !slices.Contains(cl.Scopes, s) {
			return Claims{}, fail("T8", "scope %q is missing", s)
		}
	}
	_, hasConsent := raw[ConsentClaim]
	if op.Grant == GrantClient {
		// T10
		if hasConsent {
			return Claims{}, fail("T10", "a client-credentials operation was called with a consent claim")
		}
		if !slices.Contains(app.Products, op.Product) {
			return Claims{}, fail("T10", "product %q is not granted to client %q", op.Product, cl.ClientID)
		}
		return cl, nil
	}
	// T9
	if cl.ConsentID == "" {
		return Claims{}, fail("T9", "the consent claim is missing")
	}
	con, found, err := c.cfg.Consents.Consent(ctx, cl.ConsentID)
	if err != nil {
		return Claims{}, fmt.Errorf("token: consent lookup: %w", err)
	}
	switch {
	case !found:
		return Claims{}, fail("T9", "consent %q does not exist", cl.ConsentID)
	case con.ClientID != cl.ClientID:
		return Claims{}, fail("T9", "consent %q belongs to another client", cl.ConsentID)
	case con.State != "authorised":
		return Claims{}, fail("T9", "consent %q is %q", cl.ConsentID, con.State)
	case !con.ExpiresAt.IsZero() && !c.cfg.Now().Before(con.ExpiresAt):
		return Claims{}, fail("T9", "consent %q has expired", cl.ConsentID)
	case con.Family != op.ConsentFamily:
		return Claims{}, fail("T9", "consent %q is of family %q, the operation needs %q", cl.ConsentID, con.Family, op.ConsentFamily)
	case len(op.Permissions) > 0 && !slices.ContainsFunc(op.Permissions, func(p string) bool { return slices.Contains(con.Permissions, p) }):
		return Claims{}, fail("T9", "consent %q grants none of the permissions %q", cl.ConsentID, op.Permissions)
	case op.AccountID != "" && !slices.Contains(con.Accounts, op.AccountID):
		return Claims{}, fail("T9", "account %q is not in consent %q", op.AccountID, cl.ConsentID)
	case op.ConsentID != "" && op.ConsentID != cl.ConsentID:
		return Claims{}, fail("T9", "the call names consent %q, the token carries %q", op.ConsentID, cl.ConsentID)
	}
	return cl, nil
}

// Verify applies rules T1 to T5 alone.
func (c *Checker) Verify(ctx context.Context, compact string) (Claims, error) {
	cl, _, err := c.verify(ctx, compact)
	return cl, err
}

func (c *Checker) verify(ctx context.Context, compact string) (Claims, map[string]json.RawMessage, error) {
	// T1: read the token without any key, then check the algorithm.
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return Claims{}, nil, fail("T1", "the token is not a compact JWS")
	}
	var seg [3][]byte
	for i, p := range parts {
		b, err := base64.RawURLEncoding.DecodeString(p)
		if err != nil {
			return Claims{}, nil, fail("T1", "part %d of the token is not base64url", i+1)
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
		return Claims{}, nil, fail("T1", "the header or the claims are not a JSON object")
	}
	if header.Crit != nil {
		return Claims{}, nil, fail("T1", "the header has crit, which is not supported")
	}
	if !slices.Contains(c.algorithms, header.Alg) {
		return Claims{}, nil, fail("T1", "algorithm %q is not allowed", header.Alg)
	}
	// T2
	if header.Kid == "" {
		return Claims{}, nil, fail("T2", "the header names no key")
	}
	key, err := c.key(ctx, header.Kid)
	if err != nil {
		return Claims{}, nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPSS(key, crypto.SHA256, digest[:], seg[2], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		return Claims{}, nil, fail("T2", "the signature does not verify")
	}
	var cl Claims
	// T3
	if decodeClaim(raw, "iss", &cl.Issuer) != nil || cl.Issuer != c.cfg.Issuer {
		return Claims{}, nil, fail("T3", "iss %q is not the issuer", cl.Issuer)
	}
	// T4
	var one string
	if decodeClaim(raw, "aud", &one) == nil && one != "" {
		cl.Audience = []string{one}
	} else if decodeClaim(raw, "aud", &cl.Audience) != nil {
		cl.Audience = nil
	}
	if !slices.Contains(cl.Audience, c.cfg.Audience) {
		return Claims{}, nil, fail("T4", "aud %q does not contain %q", cl.Audience, c.cfg.Audience)
	}
	// T5
	now := c.cfg.Now()
	exp, ok, err := numericDate(raw, "exp")
	if err != nil || !ok {
		return Claims{}, nil, fail("T5", "exp is missing or not a number")
	}
	if !now.Before(exp) {
		return Claims{}, nil, fail("T5", "the token expired at %s", exp.UTC().Format(time.RFC3339))
	}
	for _, name := range []string{"iat", "nbf"} {
		t, ok, err := numericDate(raw, name)
		if err != nil {
			return Claims{}, nil, fail("T5", "%s is not a number", name)
		}
		if ok && t.After(now.Add(Skew)) {
			return Claims{}, nil, fail("T5", "%s is more than %s ahead", name, Skew)
		}
	}
	cl.ExpiresAt = exp
	// Values that later rules read. A claim of the wrong type counts as absent.
	_ = decodeClaim(raw, "sub", &cl.Subject)
	var scope string
	_ = decodeClaim(raw, "scope", &scope)
	cl.Scopes = strings.Fields(scope)
	_ = decodeClaim(raw, "client_id", &cl.ClientID)
	if cl.ClientID == "" {
		_ = decodeClaim(raw, "azp", &cl.ClientID)
	}
	_ = decodeClaim(raw, ConsentClaim, &cl.ConsentID)
	return cl, raw, nil
}

// key returns the key kid. On an unknown kid it fetches the key set again, at most once
// per RefreshInterval.
func (c *Checker) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	now := c.cfg.Now()
	if !c.fetched.IsZero() && now.Sub(c.fetched) < RefreshInterval {
		return nil, fail("T2", "key %q is unknown", kid)
	}
	c.fetched = now
	doc, err := c.cfg.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: key set: %w", err)
	}
	keys, err := parseKeySet(doc)
	if err != nil {
		return nil, fmt.Errorf("token: key set: %w", err)
	}
	c.keys = keys
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fail("T2", "key %q is unknown", kid)
}

// parseKeySet keeps the RSA signature keys of at least 2048 bits that have a key identifier.
func parseKeySet(doc []byte) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || k.Kid == "" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
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
	}
	return keys, nil
}

// decodeClaim decodes the claim name into v. A missing claim leaves v as it is.
func decodeClaim(raw map[string]json.RawMessage, name string, v any) error {
	b, ok := raw[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(b, v)
}

// numericDate reads a NumericDate claim. ok is false when the claim is absent.
func numericDate(raw map[string]json.RawMessage, name string) (time.Time, bool, error) {
	b, present := raw[name]
	if !present {
		return time.Time{}, false, nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e12 {
		return time.Time{}, true, fmt.Errorf("%s is not a NumericDate", name)
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true, nil
}
