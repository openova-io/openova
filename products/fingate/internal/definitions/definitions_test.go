package definitions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const pin = "pZGm1Av0IEBKARczz7exkNYsZb8LzaMrV7J32a2fFG4"

const good = `profile: rw-4.0.0
host: api.bank.example
ledger: {target: "dns:///ledger-adapter:8443"}
products: [accounts]
partners:
  - id: acme-pay
    legalName: Acme Pay Ltd
    contacts: [dev@acmepay.example]
    state: approved
    applications:
      - id: wallet
        clientAuth: tls_client_auth
        redirectUris: ["https://wallet.acmepay.example/callback"]
        jwksUri: https://wallet.acmepay.example/jwks.json
        certificates: [{sha256: "` + pin + `", notAfter: "2027-11-12"}]
        products: [accounts]
  - id: other-bank
    legalName: Other
    state: suspended
    applications:
      - id: app
        clientAuth: private_key_jwt
        redirectUris: ["https://other.example/cb"]
        products: []
`

func TestParseReadsTheExample(t *testing.T) {
	d, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if d.Profile != "rw-4.0.0" || d.Host != "api.bank.example" || d.Ledger.Target != "dns:///ledger-adapter:8443" {
		t.Fatalf("parsed %+v", d)
	}
	app, ok := d.Application("acme-pay.wallet")
	if !ok || app.PartnerState != "approved" || !slices.Equal(app.Pins, []string{pin}) || !slices.Equal(app.Products, []string{"accounts"}) {
		t.Fatalf("acme-pay.wallet: %+v %v", app, ok)
	}
	if app, ok := d.Application("other-bank.app"); !ok || app.PartnerState != "suspended" || len(app.Pins) != 0 {
		t.Fatalf("other-bank.app: %+v %v", app, ok)
	}
	for _, id := range []string{"acme-pay", "acme-pay.other", "nobody.wallet", "acme-pay.wallet.x", ""} {
		if _, ok := d.Application(id); ok {
			t.Errorf("client %q was found", id)
		}
	}
}

func TestServes(t *testing.T) {
	d, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Serves("accounts") || d.Serves("payments") {
		t.Fatal("products [accounts] must serve accounts and nothing else")
	}
	none, err := Parse([]byte("profile: rw-4.0.0\nhost: h.example\nledger: {target: \"dns:///l:8443\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !none.Serves("accounts") || none.Serves("payments") {
		t.Fatal("without products every released family is served, and only those")
	}
	empty, err := Parse([]byte("profile: rw-4.0.0\nhost: h.example\nledger: {target: \"dns:///l:8443\"}\nproducts: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Serves("accounts") {
		t.Fatal("products [] serves no family")
	}
}

// TestParseRefusesEveryBrokenRule changes the example once per rule and expects a refusal
// that names the rule.
func TestParseRefusesEveryBrokenRule(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"unknown top-level field", "host: api.bank.example", "host: api.bank.example\nextra: 1", "field extra not found"},
		{"profile pattern", "profile: rw-4.0.0", "profile: RW", "profile"},
		{"profile missing", "profile: rw-4.0.0\n", "", "profile"},
		{"host not a host name", "host: api.bank.example", "host: \"api bank\"", "host"},
		{"ledger target pattern", "dns:///ledger-adapter:8443", "https://ledger", "ledger.target"},
		{"unknown ledger field", "ledger: {target:", "ledger: {port: 1, target:", "field port not found"},
		{"product not released", "products: [accounts]\npartners", "products: [payments]\npartners", "products: \"payments\""},
		{"partner id pattern", "id: acme-pay", "id: Acme", "partners[0]: id"},
		{"partner id unique", "id: other-bank", "id: acme-pay", "not unique"},
		{"legal name empty", "legalName: Other", "legalName: \"\"", "legalName"},
		{"contact not an address", "dev@acmepay.example", "not-an-address", "contact"},
		{"partner state", "state: suspended", "state: paused", "state \"paused\""},
		{"applications missing", "    state: suspended\n    applications:\n      - id: app\n        clientAuth: private_key_jwt\n        redirectUris: [\"https://other.example/cb\"]\n        products: []\n", "    state: suspended\n", "applications is missing"},
		{"application id pattern", "- id: wallet", "- id: W", "applications[0]: id"},
		{"client auth", "clientAuth: private_key_jwt", "clientAuth: secret", "clientAuth"},
		{"redirect empty", "redirectUris: [\"https://other.example/cb\"]", "redirectUris: []", "redirectUris is empty"},
		{"redirect not https", "https://other.example/cb", "http://other.example/cb", "redirect address"},
		{"jwks not https", "jwksUri: https://", "jwksUri: http://", "jwksUri"},
		{"pin form", pin, "short", "sha256"},
		{"notAfter form", "2027-11-12", "12.11.2027", "notAfter"},
		{"application product not released", "        products: []", "        products: [vrp]", "products: \"vrp\""},
		{"application products missing", "        products: []\n", "", "products is missing"},
		{"unknown application field", "clientAuth: tls_client_auth", "clientAuth: tls_client_auth\n        secret: x", "field secret not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(good, c.from) {
				t.Fatalf("the example does not contain %q", c.from)
			}
			_, err := Parse([]byte(strings.Replace(good, c.from, c.to, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error naming %q, got %v", c.want, err)
			}
		})
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("an empty document was accepted")
	}
}

func TestParseRefusesTooManyPartners(t *testing.T) {
	var b strings.Builder
	b.WriteString("profile: rw-4.0.0\nhost: h.example\nledger: {target: \"dns:///l:8443\"}\npartners:\n")
	for i := 0; i <= MaxPartners; i++ {
		fmt.Fprintf(&b, "  - {id: p%04d, legalName: x, state: approved, applications: []}\n", i)
	}
	if _, err := Parse([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "at most 1000") {
		t.Fatalf("want a refusal of 1001 partners, got %v", err)
	}
}

func TestHolderAnswersThePartnerLookup(t *testing.T) {
	var h Holder
	if _, _, err := h.Application(context.Background(), "acme-pay.wallet"); !errors.Is(err, ErrNoDefinitions) {
		t.Fatalf("before the first Set: want ErrNoDefinitions, got %v", err)
	}
	d, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	h.Set(d)
	if app, ok, err := h.Application(context.Background(), "acme-pay.wallet"); err != nil || !ok || app.PartnerState != "approved" {
		t.Fatalf("after Set: %+v %v %v", app, ok, err)
	}
	suspended, err := Parse([]byte(strings.Replace(good, "state: approved", "state: suspended", 1)))
	if err != nil {
		t.Fatal(err)
	}
	h.Set(suspended)
	if app, _, _ := h.Application(context.Background(), "acme-pay.wallet"); app.PartnerState != "suspended" {
		t.Fatalf("a new Set takes effect on the next lookup: %+v", app)
	}
}
