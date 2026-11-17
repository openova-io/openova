package authz

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
	"github.com/openova-io/openova/products/fingate/internal/profile"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

const (
	issuer   = "https://sso.bank.example/realms/open-banking"
	clientID = "acme-pay.wallet"
	base     = "/internal/v1/authorisations/"
	request  = `{"Data":{"Permissions":["ReadAccountsBasic","ReadBalances"]},"Risk":{}}`
	alice    = `{"identity":{"scheme":"kit","subject":"alice","attributes":{}}}`
)

var signKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// mint signs a token as the consent step does: PS256, valid for 60 seconds. A value of change
// replaces a claim; a nil value removes it.
func mint(t *testing.T, now time.Time, change map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": issuer, "aud": Audience, "sub": Subject, "azp": Subject, "iat": now.Unix(), "exp": now.Add(MaxLifetime).Unix()}
	for k, v := range change {
		if claims[k] = v; v == nil {
			delete(claims, k)
		}
	}
	h, _ := json.Marshal(map[string]string{"alg": "PS256", "typ": "JWT", "kid": "k1"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	d := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPSS(rand.Reader, signKey(), crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// fixture is a Server over the memory store and the in-memory ledger of package ledgertest
// (scheme kit: alice is cust-alice with acc-1001 and acc-1002, bob is cust-bob with acc-2001).
// The consents c1 and c2 await a decision; c2 has no expiry.
type fixture struct {
	srv        *Server
	store      *store.Memory
	now        time.Time
	logs       []string
	keysErr    error  // when set, the key set cannot be fetched
	stopLedger func() // closes the connection to the ledger: every later call of the port fails
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	conn, stop, err := ledgertest.Serve(ledgertest.NewMemory(ledgertest.FullCapabilities()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); stop() })
	p, err := profile.LoadDir("../../profiles/rw-4.0.0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), stopLedger: func() { _ = conn.Close() }}
	clock := func() time.Time { return f.now }
	f.store = store.NewMemory(clock)
	pub := signKey().PublicKey
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": "k1",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}}})
	f.srv, err = New(Config{Profile: p, Issuer: issuer, Keys: func(context.Context) ([]byte, error) { return jwks, f.keysErr },
		Store: f.store, Ledger: ledgerclient.New(conn, time.Second), Now: clock,
		Logf: func(format string, a ...any) { f.logs = append(f.logs, fmt.Sprintf(format, a...)) }})
	if err != nil {
		t.Fatal(err)
	}
	f.create(t, store.Consent{ID: "c1", Family: consent.AccountAccess, ExpiresAt: f.now.Add(30 * 24 * time.Hour)})
	f.create(t, store.Consent{ID: "c2", Family: consent.AccountAccess})
	return f
}

func (f *fixture) create(t *testing.T, c store.Consent) {
	t.Helper()
	c.ClientID, c.Request = clientID, json.RawMessage(request)
	if _, err := f.store.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

// call sends one request with the token jws; "" sends none, "ok" a token the consent step would send.
func (f *fixture) call(t *testing.T, method, path, jws, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "https://fingate-api.open-banking.svc:8081"+path, strings.NewReader(body))
	if jws == "ok" {
		jws = mint(t, f.now, nil)
	}
	if jws != "" {
		req.Header.Set("Authorization", "Bearer "+jws)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	return w
}

// want checks the status, the whole body and the two headers of an answer.
func want(t *testing.T, name string, w *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if got := strings.TrimSpace(w.Body.String()); w.Code != status || got != body ||
		w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s: status %d, body %s; want %d, %s, as application/json with no-store", name, w.Code, got, status, body)
	}
}

func code(c string) string { return `{"error":"` + c + `"}` }

func (f *fixture) state(t *testing.T, id string) store.Consent {
	t.Helper()
	c, ok, err := f.store.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("consent %s: found %v, error %v", id, ok, err)
	}
	return c
}

func TestEveryCallNeedsTheTokenOfTheConsentStep(t *testing.T) {
	f := newFixture(t)
	calls := [][3]string{{"GET", base + "c1?client_id=" + clientID, ""}, {"POST", base + "c1/customer", alice}, {"GET", "/healthz", ""}}
	refused := map[string]string{
		"no token":                       "",
		"not a token":                    "abc",
		"a token of the resource server": mint(t, f.now, map[string]any{"aud": "https://api.bank.example"}),
		"another subject":                mint(t, f.now, map[string]any{"sub": clientID}),
		"no subject":                     mint(t, f.now, map[string]any{"sub": nil}),
		"another issuer":                 mint(t, f.now, map[string]any{"iss": "https://sso.bank.example/realms/other"}),
		"an ended token":                 mint(t, f.now.Add(-2*time.Minute), nil),
		"a lifetime of five minutes":     mint(t, f.now, map[string]any{"exp": f.now.Add(5 * time.Minute).Unix()}),
	}
	for name, jws := range refused {
		for _, c := range calls {
			w := f.call(t, c[0], c[1], jws, c[2])
			want(t, name+": "+c[0]+" "+c[1], w, 401, code("invalid_token"))
			if w.Header().Get("WWW-Authenticate") != token.Challenge {
				t.Fatalf("%s: WWW-Authenticate is %q", name, w.Header().Get("WWW-Authenticate"))
			}
		}
	}
	if c := f.state(t, "c1"); c.State != consent.Awaiting || len(f.logs) != len(refused)*len(calls) {
		t.Fatalf("after the refused calls the consent is %s and %d lines were logged, want awaiting and %d", c.State, len(f.logs), len(refused)*len(calls))
	}
	// A token whose end is at the limit is accepted; a key set that cannot be fetched is not a refused token.
	limit := mint(t, f.now, map[string]any{"exp": f.now.Add(MaxLifetime + token.Skew).Unix()})
	if w := f.call(t, "GET", base+"c1?client_id="+clientID, limit, ""); w.Code != 200 {
		t.Fatalf("a token that ends at the limit: status %d", w.Code)
	}
	g := newFixture(t)
	g.keysErr = errors.New("no route to the realm")
	want(t, "no key set", g.call(t, "GET", base+"c1?client_id="+clientID, "ok", ""), 500, code("internal"))
}

func TestOnlyTheCallsOfTheAPIAreRouted(t *testing.T) {
	f := newFixture(t)
	q := "?client_id=" + clientID
	for _, c := range [][2]string{{"GET", "/healthz"}, {"GET", "/internal/v1/authorisations"}, {"DELETE", base + "c1"},
		{"POST", base + "c1"}, {"GET", base + "c1/customer"}, {"POST", base + "c1/customer/x"},
		// What the mux alone would serve as GET or answer with a redirect is no route either.
		{"HEAD", base + "c1" + q}, {"GET", base + "c1/" + q}, {"GET", "/internal/v1//authorisations/c1" + q},
		{"GET", base + "./c1" + q}, {"POST", base + "x/../c1/customer"}} {
		want(t, c[0]+" "+c[1], f.call(t, c[0], c[1], "ok", alice), 404, code("no-route"))
	}
	// The same consent is served on the exact routes, so the refusals above are about the route.
	if w := f.call(t, "GET", base+"c1"+q, "ok", ""); w.Code != 200 {
		t.Fatalf("the read call on its exact route: status %d", w.Code)
	}
	if w := f.call(t, "POST", base+"c1/customer", "ok", alice); w.Code != 200 {
		t.Fatalf("the customer call on its exact route: status %d", w.Code)
	}
}

func TestReadShowsAnAwaitingConsentToItsClient(t *testing.T) {
	f := newFixture(t)
	f.create(t, store.Consent{ID: "p1", Family: consent.Payment})
	if _, err := f.store.Apply(context.Background(), "c2", consent.Reject, consent.System, "test"); err != nil {
		t.Fatal(err)
	}
	q := "?client_id=" + clientID
	for _, c := range []struct {
		name, path string
		status     int
		body       string
	}{
		{"the consent", base + "c1" + q, 200, `{"clientId":"acme-pay.wallet","consentId":"c1","expiresAt":"2026-12-01T09:00:00Z","family":"account-access",` +
			`"state":"awaiting","summary":{"permissions":["ReadAccountsBasic","ReadBalances"]}}`},
		{"unknown consent", base + "c9" + q, 404, code("consent-not-found")},
		{"another client", base + "c1?client_id=other.app", 403, code("client-mismatch")},
		{"no client", base + "c1", 403, code("client-mismatch")},
		{"another client of a decided consent", base + "c2?client_id=other.app", 403, code("client-mismatch")},
		{"a decided consent", base + "c2" + q, 409, code("not-awaiting")},
		{"a family without a summary", base + "p1" + q, 500, code("internal")},
	} {
		want(t, c.name, f.call(t, "GET", c.path, "ok", ""), c.status, c.body)
	}
	g := newFixture(t)
	want(t, "no expiry", g.call(t, "GET", base+"c2"+q, "ok", ""), 200, `{"clientId":"acme-pay.wallet","consentId":"c2","expiresAt":"",`+
		`"family":"account-access","state":"awaiting","summary":{"permissions":["ReadAccountsBasic","ReadBalances"]}}`)
}

func TestCustomerResolvesTheIdentityAndListsTheAccounts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Apply(context.Background(), "c2", consent.Reject, consent.System, "test"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, id, body string
		status         int
		want           string
	}{
		{"alice", "c1", alice, 200, `{"accounts":[{"id":"acc-1001","displayName":"Main","maskedNumber":"****5678","currency":"GBP"},` +
			`{"id":"acc-1002","displayName":"Savings","maskedNumber":"****4321","currency":"GBP"}],"customerId":"cust-alice"}`},
		{"unknown subject", "c1", `{"identity":{"scheme":"kit","subject":"mallory"}}`, 404, code("customer-unknown")},
		{"unknown scheme", "c1", `{"identity":{"scheme":"other","subject":"alice"}}`, 404, code("customer-unknown")},
		{"unknown consent", "c9", alice, 404, code("consent-not-found")},
		{"a decided consent", "c2", alice, 409, code("not-awaiting")},
		{"not JSON", "c1", `identity`, 400, code("bad-request")},
		{"no identity", "c1", `{}`, 400, code("bad-request")},
		{"no subject", "c1", `{"identity":{"scheme":"kit"}}`, 400, code("bad-request")},
		{"an attribute that is no string", "c1", `{"identity":{"scheme":"kit","subject":"alice","attributes":{"a":1}}}`, 400, code("bad-request")},
		{"an unknown member", "c1", `{"identity":{"scheme":"kit","subject":"alice"},"customerId":"cust-bob"}`, 400, code("bad-request")},
		{"two values", "c1", alice + alice, 400, code("bad-request")},
	} {
		want(t, c.name, f.call(t, "POST", base+c.id+"/customer", "ok", c.body), c.status, c.want)
	}
	f.stopLedger()
	want(t, "no ledger", f.call(t, "POST", base+"c1/customer", "ok", alice), 502, code("ledger-unavailable"))
	if got := [3]string{mask("40400412345678"), mask("1234"), mask("")}; got != [3]string{"****5678", "", ""} {
		t.Fatalf("mask: %q", got)
	}
	if c := f.state(t, "c1"); c.State != consent.Awaiting || len(c.History) != 1 {
		t.Fatalf("after the calls the consent is %s with %d history entries, want awaiting with 1", c.State, len(c.History))
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	f := newFixture(t)
	for name, change := range map[string]func(*Config){
		"no store":         func(c *Config) { c.Store = nil },
		"no ledger":        func(c *Config) { c.Ledger = nil },
		"no profile":       func(c *Config) { c.Profile = nil },
		"no key source":    func(c *Config) { c.Keys = nil },
		"a plain issuer":   func(c *Config) { c.Issuer = "http://sso.bank.example/realms/open-banking" },
		"no issuer at all": func(c *Config) { c.Issuer = "" },
	} {
		cfg := f.srv.cfg
		change(&cfg)
		if _, err := New(cfg); err == nil || !strings.HasPrefix(err.Error(), "authz: ") {
			t.Fatalf("%s: New returned %v", name, err)
		}
	}
}
