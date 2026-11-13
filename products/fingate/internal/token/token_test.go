package token

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/profile"
)

const (
	issuer   = "https://sso.bank.example/realms/open-banking"
	audience = "https://api.bank.example"
	client   = "acme-pay.wallet"
)

var (
	keyOnce            sync.Once
	realmKey, roguekey *rsa.PrivateKey
	pin                = Thumbprint([]byte("the client certificate"))
	epoch              = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
)

func signingKeys(t *testing.T) {
	t.Helper()
	keyOnce.Do(func() {
		var err1, err2 error
		realmKey, err1 = rsa.GenerateKey(rand.Reader, 2048)
		roguekey, err2 = rsa.GenerateKey(rand.Reader, 2048)
		if err := errors.Join(err1, err2); err != nil {
			panic(err)
		}
	})
}

func jwk(kid string, k *rsa.PublicKey) map[string]any {
	return map[string]any{"kty": "RSA", "use": "sig", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}
}

type partners map[string]Application

func (p partners) Application(_ context.Context, id string) (Application, bool, error) {
	a, ok := p[id]
	return a, ok, nil
}

type consents struct {
	m       map[string]Consent
	lookups int
}

func (c *consents) Consent(_ context.Context, id string) (Consent, bool, error) {
	c.lookups++
	v, ok := c.m[id]
	return v, ok, nil
}

// world is one realm, one partner application and one consent, at a clock the test moves.
type world struct {
	now      time.Time
	fetches  int
	keySet   []map[string]any
	partners partners
	consents *consents
	checker  *Checker
}

func newWorld(t *testing.T) *world {
	t.Helper()
	signingKeys(t)
	w := &world{now: epoch, keySet: []map[string]any{jwk("k1", &realmKey.PublicKey)}}
	w.partners = partners{client: {PartnerState: "approved", Pins: []string{pin}, Products: []string{"accounts", "events"}}}
	w.consents = &consents{m: map[string]Consent{"aac-1": {ClientID: client, Family: "account-access",
		State: "authorised", ExpiresAt: epoch.Add(24 * time.Hour),
		Permissions: []string{"ReadAccountsBasic"}, Accounts: []string{"acc-1"}}}}
	p := &profile.Profile{Security: profile.Security{SigningAlgorithms: []string{"PS256"}, ConsentCarrier: "intent-claim"}}
	c, err := New(Config{Profile: p, Issuer: issuer, Audience: audience, Partners: w.partners, Consents: w.consents,
		Now: func() time.Time { return w.now },
		Keys: func(context.Context) ([]byte, error) {
			w.fetches++
			return json.Marshal(map[string]any{"keys": w.keySet})
		}})
	if err != nil {
		t.Fatal(err)
	}
	w.checker = c
	return w
}

func (w *world) header() map[string]any {
	return map[string]any{"alg": "PS256", "kid": "k1", "typ": "JWT"}
}

func (w *world) claims() map[string]any {
	return map[string]any{"iss": issuer, "aud": []string{audience}, "sub": "customer-1",
		"exp": w.now.Add(5 * time.Minute).Unix(), "iat": w.now.Unix(), "nbf": w.now.Unix(),
		"scope": "openid accounts", "client_id": client, "cnf": map[string]any{"x5t#S256": pin},
		ConsentClaim: "aac-1"}
}

func customerOp() Operation {
	return Operation{Grant: GrantCustomer, Scopes: []string{"accounts"}, Product: "accounts",
		ConsentFamily: "account-access", Permissions: []string{"ReadAccountsBasic", "ReadAccountsDetail"}, AccountID: "acc-1"}
}

func clientOp() Operation {
	return Operation{Grant: GrantClient, Scopes: []string{"accounts"}, Product: "accounts"}
}

func segment(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func mint(key *rsa.PrivateKey, header, claims map[string]any) string {
	input := segment(header) + "." + segment(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		panic(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// check runs Check with the given header, claims and operation, signed by the realm key.
func (w *world) check(header, claims map[string]any, op Operation) error {
	_, err := w.checker.Check(context.Background(), Request{JWS: mint(realmKey, header, claims), Thumbprint: pin, Operation: op})
	return err
}

func wantPass(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("want the call accepted, got %v", err)
	}
}

func wantRule(t *testing.T, err error, rule string) {
	t.Helper()
	status := map[string]int{"T1": 401, "T2": 401, "T3": 401, "T4": 401, "T5": 401, "T6": 401,
		"T7": 403, "T8": 403, "T9": 403, "T10": 403}[rule]
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want rule %s, got %v", rule, err)
	}
	if e.Rule != rule || e.Status != status {
		t.Fatalf("want rule %s with status %d, got rule %s with status %d (%s)", rule, status, e.Rule, e.Status, e.Reason)
	}
}

// edit returns a copy of m with the keys of set replaced and the keys of del removed.
func edit(m map[string]any, set map[string]any, del ...string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range set {
		out[k] = v
	}
	for _, k := range del {
		delete(out, k)
	}
	return out
}

func TestRuleT1(t *testing.T) {
	w := newWorld(t)
	unsigned := segment(map[string]any{"alg": "none"}) + "." + segment(w.claims()) + "."
	for name, compact := range map[string]string{
		"none":       unsigned,
		"HS256":      mint(realmKey, edit(w.header(), map[string]any{"alg": "HS256"}), w.claims()),
		"RS256":      mint(realmKey, edit(w.header(), map[string]any{"alg": "RS256"}), w.claims()),
		"no alg":     mint(realmKey, edit(w.header(), nil, "alg"), w.claims()),
		"crit":       mint(realmKey, edit(w.header(), map[string]any{"crit": []string{"b64"}}), w.claims()),
		"two parts":  segment(w.header()) + "." + segment(w.claims()),
		"not base64": "***." + segment(w.claims()) + ".x",
	} {
		_, err := w.checker.Check(context.Background(), Request{JWS: compact, Thumbprint: pin, Operation: customerOp()})
		wantRule(t, err, "T1")
		if w.fetches != 0 {
			t.Fatalf("%s: the key set was fetched %d times before the algorithm was refused", name, w.fetches)
		}
	}
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
}

func TestRuleT2(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
	_, err := w.checker.Check(context.Background(), Request{JWS: mint(roguekey, w.header(), w.claims()), Thumbprint: pin, Operation: customerOp()})
	wantRule(t, err, "T2")
	parts := strings.Split(mint(realmKey, w.header(), w.claims()), ".")
	tampered := parts[0] + "." + segment(edit(w.claims(), map[string]any{"sub": "customer-2"})) + "." + parts[2]
	_, err = w.checker.Check(context.Background(), Request{JWS: tampered, Thumbprint: pin, Operation: customerOp()})
	wantRule(t, err, "T2")
	wantRule(t, w.check(edit(w.header(), nil, "kid"), w.claims(), customerOp()), "T2")
	// An unknown key fetches the set again at most once a minute.
	w.keySet = append(w.keySet, jwk("k2", &roguekey.PublicKey))
	rotated := edit(w.header(), map[string]any{"kid": "k2"})
	_, err = w.checker.Check(context.Background(), Request{JWS: mint(roguekey, rotated, w.claims()), Thumbprint: pin, Operation: customerOp()})
	wantRule(t, err, "T2")
	if w.fetches != 1 {
		t.Fatalf("an unknown key within a minute of the last fetch fetched the key set: %d fetches, want 1", w.fetches)
	}
	w.now = w.now.Add(RefreshInterval)
	_, err = w.checker.Check(context.Background(), Request{JWS: mint(roguekey, rotated, w.claims()), Thumbprint: pin, Operation: customerOp()})
	wantPass(t, err)
	if w.fetches != 2 {
		t.Fatalf("want 2 fetches of the key set, got %d", w.fetches)
	}
}

func TestRuleT3(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"iss": "https://sso.bank.example/realms/other"}), customerOp()), "T3")
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "iss"), customerOp()), "T3")
	if _, err := New(Config{Profile: w.checker.cfg.Profile, Issuer: "http://sso.bank.example/realms/open-banking",
		Audience: audience, Keys: w.checker.cfg.Keys}); err == nil {
		t.Fatal("New accepted an issuer that is not an https address")
	}
}

func TestRuleT4(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), edit(w.claims(), map[string]any{"aud": audience}), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"aud": []string{"https://other.example"}}), customerOp()), "T4")
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "aud"), customerOp()), "T4")
}

func TestRuleT5(t *testing.T) {
	w := newWorld(t)
	now := w.now.Unix()
	wantPass(t, w.check(w.header(), edit(w.claims(), map[string]any{"iat": now + 59, "nbf": now + 59}), customerOp()))
	for _, set := range []map[string]any{{"exp": now}, {"exp": now - 1}, {"exp": "tomorrow"}, {"iat": now + 61}, {"nbf": now + 61}} {
		wantRule(t, w.check(w.header(), edit(w.claims(), set), customerOp()), "T5")
	}
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "exp"), customerOp()), "T5")
}

func TestRuleT6(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "cnf"), customerOp()), "T6")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"cnf": map[string]any{"jkt": pin}}), customerOp()), "T6")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"cnf": map[string]any{"x5t#S256": Thumbprint([]byte("another certificate"))}}), customerOp()), "T6")
	_, err := w.checker.Check(context.Background(), Request{JWS: mint(realmKey, w.header(), w.claims()), Operation: customerOp()})
	wantRule(t, err, "T6")
}

func TestRuleT7(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), edit(w.claims(), map[string]any{"azp": client}, "client_id"), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"client_id": "acme-pay.other"}), customerOp()), "T7")
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "client_id"), customerOp()), "T7")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"azp": "acme-pay.other"}), customerOp()), "T7")
	for _, app := range []Application{
		{PartnerState: "suspended", Pins: []string{pin}, Products: []string{"accounts"}},
		{PartnerState: "revoked", Pins: []string{pin}, Products: []string{"accounts"}},
		{PartnerState: "approved", Pins: []string{Thumbprint([]byte("an old certificate"))}, Products: []string{"accounts"}},
	} {
		w.partners[client] = app
		wantRule(t, w.check(w.header(), w.claims(), customerOp()), "T7")
	}
	if w.consents.lookups != 1 {
		t.Fatalf("a refused client reached the consent lookup: %d lookups, want 1", w.consents.lookups)
	}
}

func TestRuleT8(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"scope": "openid payments"}), customerOp()), "T8")
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, "scope"), customerOp()), "T8")
	events := Operation{Grant: GrantClient, Scopes: []string{"accounts", "payments", "fundsconfirmations"}, Product: "events"}
	client := edit(w.claims(), nil, ConsentClaim)
	wantPass(t, w.check(w.header(), edit(client, map[string]any{"scope": "accounts payments fundsconfirmations"}), events))
	wantRule(t, w.check(w.header(), edit(client, map[string]any{"scope": "accounts payments"}), events), "T8")
}

func TestRuleT9(t *testing.T) {
	w := newWorld(t)
	wantPass(t, w.check(w.header(), w.claims(), customerOp()))
	wantRule(t, w.check(w.header(), edit(w.claims(), nil, ConsentClaim), customerOp()), "T9")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{ConsentClaim: "aac-unknown"}), customerOp()), "T9")
	op := customerOp()
	op.AccountID = "acc-2"
	wantRule(t, w.check(w.header(), w.claims(), op), "T9")
	op = customerOp()
	op.Permissions = []string{"ReadTransactionsDetail"}
	wantRule(t, w.check(w.header(), w.claims(), op), "T9")
	op = customerOp()
	op.ConsentFamily = "payment"
	wantRule(t, w.check(w.header(), w.claims(), op), "T9")
	op = customerOp()
	op.ConsentID = "aac-2"
	wantRule(t, w.check(w.header(), w.claims(), op), "T9")
	good := w.consents.m["aac-1"]
	for _, bad := range []func(c *Consent){
		func(c *Consent) { c.ClientID = "acme-pay.other" },
		func(c *Consent) { c.State = "awaiting" },
		func(c *Consent) { c.State = "revoked" },
		func(c *Consent) { c.ExpiresAt = epoch },
	} {
		c := good
		bad(&c)
		w.consents.m["aac-1"] = c
		wantRule(t, w.check(w.header(), w.claims(), customerOp()), "T9")
	}
}

func TestRuleT10(t *testing.T) {
	w := newWorld(t)
	client := edit(w.claims(), nil, ConsentClaim)
	wantPass(t, w.check(w.header(), client, clientOp()))
	wantRule(t, w.check(w.header(), w.claims(), clientOp()), "T10")
	payments := Operation{Grant: GrantClient, Scopes: []string{"payments"}, Product: "payments"}
	wantRule(t, w.check(w.header(), edit(client, map[string]any{"scope": "payments"}), payments), "T10")
	if w.consents.lookups != 0 {
		t.Fatalf("a client-credentials operation looked up a consent %d times", w.consents.lookups)
	}
}

func TestFirstFailureEndsTheCall(t *testing.T) {
	w := newWorld(t)
	wantRule(t, w.check(edit(w.header(), map[string]any{"alg": "RS256"}), edit(w.claims(), map[string]any{"iss": "https://other.example"}), customerOp()), "T1")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"iss": "https://other.example", "scope": "payments"}), customerOp()), "T3")
	wantRule(t, w.check(w.header(), edit(w.claims(), map[string]any{"scope": "payments"}, "cnf"), customerOp()), "T6")
}

func TestVerifyAppliesT1ToT5(t *testing.T) {
	w := newWorld(t)
	cl, err := w.checker.Verify(context.Background(), mint(realmKey, w.header(), edit(w.claims(), nil, "cnf")))
	wantPass(t, err)
	if cl.ClientID != client || cl.ConsentID != "aac-1" || cl.Subject != "customer-1" || len(cl.Scopes) != 2 {
		t.Fatalf("unexpected claims %+v", cl)
	}
	_, err = w.checker.Verify(context.Background(), mint(realmKey, w.header(), edit(w.claims(), nil, "exp")))
	wantRule(t, err, "T5")
}

func TestNewRefusesAWeakConfiguration(t *testing.T) {
	keys := KeySource(func(context.Context) ([]byte, error) { return nil, nil })
	ok := profile.Security{SigningAlgorithms: []string{"PS256"}, ConsentCarrier: "intent-claim"}
	for name, cfg := range map[string]Config{
		"no profile":     {Issuer: issuer, Audience: audience, Keys: keys},
		"RS256":          {Profile: &profile.Profile{Security: profile.Security{SigningAlgorithms: []string{"RS256"}, ConsentCarrier: "intent-claim"}}, Issuer: issuer, Audience: audience, Keys: keys},
		"no algorithm":   {Profile: &profile.Profile{Security: profile.Security{ConsentCarrier: "intent-claim"}}, Issuer: issuer, Audience: audience, Keys: keys},
		"other carrier":  {Profile: &profile.Profile{Security: profile.Security{SigningAlgorithms: []string{"PS256"}, ConsentCarrier: "authorization-details"}}, Issuer: issuer, Audience: audience, Keys: keys},
		"no audience":    {Profile: &profile.Profile{Security: ok}, Issuer: issuer, Keys: keys},
		"no key source":  {Profile: &profile.Profile{Security: ok}, Issuer: issuer, Audience: audience},
		"issuer no host": {Profile: &profile.Profile{Security: ok}, Issuer: "https://", Audience: audience, Keys: keys},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted the configuration", name)
		}
	}
}

func TestShippedProfileIsAccepted(t *testing.T) {
	p, err := profile.LoadDir("../../profiles/rw-4.0.0")
	if err != nil {
		t.Fatal(err)
	}
	keys := KeySource(func(context.Context) ([]byte, error) { return nil, nil })
	if _, err := New(Config{Profile: p, Issuer: issuer, Audience: audience, Keys: keys}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPKeySource(t *testing.T) {
	signingKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/certs" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"keys": []any{jwk("k1", &realmKey.PublicKey),
			map[string]any{"kty": "RSA", "use": "enc", "kid": "e1", "n": "AQAB", "e": "AQAB"}}})
	}))
	defer srv.Close()
	doc, err := HTTPKeySource(srv.Client(), srv.URL+"/certs")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := parseKeySet(doc)
	if err != nil || len(keys) != 1 || keys["k1"] == nil || keys["k1"].E != realmKey.E {
		t.Fatalf("want the one signature key k1, got %v, %v", keys, err)
	}
	if _, err := HTTPKeySource(srv.Client(), srv.URL+"/missing")(context.Background()); err == nil {
		t.Fatal("a 404 key set was accepted")
	}
}

func TestLookupFailureIsNoRule(t *testing.T) {
	w := newWorld(t)
	w.checker.cfg.Keys = func(context.Context) ([]byte, error) { return nil, errors.New("realm unreachable") }
	_, err := w.checker.Check(context.Background(), Request{JWS: mint(realmKey, w.header(), w.claims()), Thumbprint: pin, Operation: customerOp()})
	var e *Error
	if err == nil || errors.As(err, &e) {
		t.Fatalf("an unreachable key set must be an error that is not a rule, got %v", err)
	}
}
