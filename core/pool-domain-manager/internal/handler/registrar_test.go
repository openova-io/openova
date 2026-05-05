package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// fakeAdapter is a programmable Registrar for handler-level tests.
type fakeAdapter struct {
	name        string
	validateErr error
	setErr      error
	getErr      error
	gotToken    string
	gotDomain   string
	gotNS       []string
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) ValidateToken(ctx context.Context, token, domain string) error {
	f.gotToken = token
	f.gotDomain = domain
	return f.validateErr
}
func (f *fakeAdapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	f.gotToken = token
	f.gotDomain = domain
	f.gotNS = ns
	return f.setErr
}
func (f *fakeAdapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.gotNS, nil
}

// captureLogger emits all log records into an in-memory buffer so tests
// can assert what did (and did NOT) appear in operator logs.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

func newTestHandler(t *testing.T, adapter *fakeAdapter) (*Handler, *bytes.Buffer) {
	t.Helper()
	log, buf := captureLogger()
	h := &Handler{Log: log}
	h.SetRegistry(registrar.Registry{adapter.name: adapter})
	return h, buf
}

func doSetNS(t *testing.T, h *Handler, registrarName string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/registrar/{registrar}", func(r chi.Router) {
			r.Post("/set-ns", h.SetNS)
		})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/registrar/"+registrarName+"/set-ns", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func doValidate(t *testing.T, h *Handler, registrarName string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/registrar/{registrar}", func(r chi.Router) {
			r.Post("/validate", h.Validate)
		})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/registrar/"+registrarName+"/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const supersecretToken = "totally-secret-token-7Hf83KjzQ2"

func TestSetNSHappy(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, logBuf := newTestHandler(t, a)
	body := `{"domain":"example.com","token":"` + supersecretToken + `","nameservers":["ns1.openova.io","ns2.openova.io"]}`
	rec := doSetNS(t, h, "cloudflare", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp SetNSResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success || resp.Registrar != "cloudflare" || resp.Domain != "example.com" {
		t.Fatalf("resp = %+v", resp)
	}
	if strings.Join(resp.Nameservers, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("ns = %v", resp.Nameservers)
	}
	if a.gotToken != supersecretToken {
		t.Fatalf("adapter did not receive token (got %q)", a.gotToken)
	}
	// Token MUST NOT appear in logs.
	if strings.Contains(logBuf.String(), supersecretToken) {
		t.Fatalf("LEAKED token in logs: %s", logBuf.String())
	}
}

func TestSetNSUnsupportedRegistrar(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "fakehost", `{"domain":"x.com","token":"t","nameservers":["a"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSBadJSON(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare", `not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSMissingFields(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	cases := []string{
		`{"token":"t","nameservers":["a"]}`,
		`{"domain":"x.com","nameservers":["a"]}`,
		`{"domain":"x.com","token":"t","nameservers":[]}`,
	}
	for _, c := range cases {
		rec := doSetNS(t, h, "cloudflare", c)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %q → status %d, want 422", c, rec.Code)
		}
	}
}

func TestSetNSInvalidToken(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrInvalidToken}
	h, logBuf := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"`+supersecretToken+`","nameservers":["a","b"]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(logBuf.String(), supersecretToken) {
		t.Fatalf("token leaked in logs: %s", logBuf.String())
	}
}

func TestSetNSRateLimited(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrRateLimited}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSDomainNotInAccount(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrDomainNotInAccount}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSAPIUnavailable(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrAPIUnavailable}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSWriteFailsAfterValidate(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", setErr: registrar.ErrAPIUnavailable}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetNSReadbackFailsButWriteSucceeded(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", getErr: errors.New("readback timeout")}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("readback failure must NOT fail the write; status = %d", rec.Code)
	}
	var resp SetNSResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Fatal("expected success despite readback failure")
	}
	// Falls back to the supplied list.
	if strings.Join(resp.Nameservers, ",") != "a,b" {
		t.Fatalf("ns = %v", resp.Nameservers)
	}
}

func TestSetNSNoRegistry(t *testing.T) {
	log, _ := captureLogger()
	h := &Handler{Log: log}
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"t","nameservers":["a","b"]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNormaliseNS(t *testing.T) {
	got := normaliseNS([]string{" NS1.OPENOVA.io", "ns2.openova.io", "", "ns1.openova.io  "})
	want := []string{"ns1.openova.io", "ns2.openova.io"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got = %v, want %v", got, want)
	}
	if normaliseNS(nil) != nil {
		t.Fatal("nil input should return nil")
	}
}

func TestPropagationHint(t *testing.T) {
	for _, n := range []string{"cloudflare", "godaddy", "namecheap", "ovh", "dynadot"} {
		if propagationHint(n) == "" {
			t.Errorf("empty hint for %q", n)
		}
	}
	if propagationHint("unknown") == "" {
		t.Error("unknown registrar must still get a generic hint")
	}
}

// ── /validate (#169) ──────────────────────────────────────────────────

func TestValidateHappy(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, logBuf := newTestHandler(t, a)
	rec := doValidate(t, h, "cloudflare",
		`{"domain":"example.com","token":"`+supersecretToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Valid || resp.Registrar != "cloudflare" || resp.Domain != "example.com" {
		t.Fatalf("resp = %+v", resp)
	}
	if a.gotToken != supersecretToken {
		t.Fatalf("adapter did not receive token (got %q)", a.gotToken)
	}
	// Crucial — Validate MUST NOT call SetNameservers.
	if a.gotNS != nil {
		t.Fatalf("Validate accidentally flipped NS: %v", a.gotNS)
	}
	if strings.Contains(logBuf.String(), supersecretToken) {
		t.Fatalf("LEAKED token in logs: %s", logBuf.String())
	}
}

func TestValidateInvalidToken(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrInvalidToken}
	h, _ := newTestHandler(t, a)
	rec := doValidate(t, h, "cloudflare",
		`{"domain":"x.com","token":"`+supersecretToken+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestValidateDomainNotInAccount(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare", validateErr: registrar.ErrDomainNotInAccount}
	h, _ := newTestHandler(t, a)
	rec := doValidate(t, h, "cloudflare",
		`{"domain":"x.com","token":"t"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestValidateUnsupportedRegistrar(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doValidate(t, h, "fakehost",
		`{"domain":"x.com","token":"t"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestValidateMissingFields(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	for _, body := range []string{
		`{"token":"t"}`,
		`{"domain":"x.com"}`,
	} {
		rec := doValidate(t, h, "cloudflare", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %q → status %d, want 422", body, rec.Code)
		}
	}
}

func TestValidateBadJSON(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doValidate(t, h, "cloudflare", `not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestValidateResponseDoesNotEchoToken(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doValidate(t, h, "cloudflare",
		`{"domain":"x.com","token":"`+supersecretToken+`"}`)
	body, _ := io.ReadAll(rec.Body)
	if bytes.Contains(body, []byte(supersecretToken)) {
		t.Fatalf("response body leaks token: %s", string(body))
	}
}

// Defensive: make sure when the body is huge, the handler doesn't echo
// anything sensitive in the response.
func TestSetNSResponseDoesNotEchoToken(t *testing.T) {
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	rec := doSetNS(t, h, "cloudflare",
		`{"domain":"x.com","token":"`+supersecretToken+`","nameservers":["a","b"]}`)
	body, _ := io.ReadAll(rec.Body)
	if bytes.Contains(body, []byte(supersecretToken)) {
		t.Fatalf("response body leaks token: %s", string(body))
	}
}

// ── Glue-record path (issue #900) ───────────────────────────────────────
//
// fakeGlueAdapter extends fakeAdapter with the registrar.GlueRegistrar
// surface. The HTTP handler's type-assertion on adapter.(GlueRegistrar)
// MUST succeed for instances of this struct, exercising the SetNS code
// path that pre-registers glue before the set_ns flip.
type fakeGlueAdapter struct {
	fakeAdapter
	registerErr error
	registered  []glueCall
}

type glueCall struct {
	host string
	ip   string
}

func (f *fakeGlueAdapter) RegisterGlueRecord(_ context.Context, _, host, ip string) error {
	if f.registerErr != nil {
		return f.registerErr
	}
	f.registered = append(f.registered, glueCall{host: host, ip: ip})
	return nil
}
func (f *fakeGlueAdapter) GetGlueRecord(_ context.Context, _, host string) (string, error) {
	for _, g := range f.registered {
		if g.host == host {
			return g.ip, nil
		}
	}
	return "", nil
}

func newGlueTestHandler(t *testing.T, adapter *fakeGlueAdapter) *Handler {
	t.Helper()
	log, _ := captureLogger()
	h := &Handler{Log: log}
	h.SetRegistry(registrar.Registry{adapter.name: adapter})
	return h
}

// TestSetNSGluePreRegistersBeforeFlip — when the request body carries a
// non-empty glueIP and the adapter implements GlueRegistrar, the
// handler MUST call RegisterGlueRecord for each NS hostname BEFORE
// invoking SetNameservers. Verifies the canonical issue #900 unblock
// path: register first, flip second.
func TestSetNSGluePreRegistersBeforeFlip(t *testing.T) {
	a := &fakeGlueAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newGlueTestHandler(t, a)
	body := `{
	  "domain":"customer-acme.tld",
	  "token":"` + supersecretToken + `",
	  "nameservers":["ns1.otech103.omani.works","ns2.otech103.omani.works"],
	  "glueIP":"203.0.113.42"
	}`
	rec := doSetNS(t, h, "dynadot", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(a.registered) != 2 {
		t.Fatalf("expected 2 glue registrations (one per NS), got %d (%+v)", len(a.registered), a.registered)
	}
	for i, exp := range []glueCall{
		{"ns1.otech103.omani.works", "203.0.113.42"},
		{"ns2.otech103.omani.works", "203.0.113.42"},
	} {
		if a.registered[i] != exp {
			t.Errorf("registration[%d] = %+v, want %+v", i, a.registered[i], exp)
		}
	}
}

// TestSetNSGlueRegisterFails — when RegisterGlueRecord returns a typed
// registrar error, the handler MUST surface the same status code as a
// SetNameservers failure (e.g. 401 for ErrInvalidToken) and MUST NOT
// proceed to set_ns. Verifies the fail-fast contract — a failed glue
// pre-step never leaves the customer's NS in an intermediate state.
func TestSetNSGlueRegisterFails(t *testing.T) {
	a := &fakeGlueAdapter{
		fakeAdapter: fakeAdapter{name: "dynadot"},
		registerErr: registrar.ErrInvalidToken,
	}
	h := newGlueTestHandler(t, a)
	body := `{
	  "domain":"customer-acme.tld",
	  "token":"t",
	  "nameservers":["ns1.otech103.omani.works"],
	  "glueIP":"203.0.113.42"
	}`
	rec := doSetNS(t, h, "dynadot", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401 (ErrInvalidToken passthrough)", rec.Code, rec.Body.String())
	}
	// SetNameservers MUST NOT have been called — gotNS is still the
	// fakeAdapter zero-value (nil), proving the flip was skipped.
	if a.gotNS != nil {
		t.Errorf("SetNameservers was called despite glue register failure; gotNS=%+v", a.gotNS)
	}
}

// TestSetNSGlueSkipsInBailiwick — when an NS hostname IS a child of the
// customer's own domain, glue pre-registration is a no-op (the customer
// resolves it via their zone's own A records). The handler MUST skip
// RegisterGlueRecord for that host, and call it only for out-of-bailiwick
// hosts.
func TestSetNSGlueSkipsInBailiwick(t *testing.T) {
	a := &fakeGlueAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newGlueTestHandler(t, a)
	// First NS is in-bailiwick (child of customer-acme.tld);
	// second NS is out-of-bailiwick (omani.works subzone).
	body := `{
	  "domain":"customer-acme.tld",
	  "token":"t",
	  "nameservers":["ns.customer-acme.tld","ns1.otech103.omani.works"],
	  "glueIP":"203.0.113.42"
	}`
	rec := doSetNS(t, h, "dynadot", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(a.registered) != 1 {
		t.Fatalf("expected 1 glue registration (out-of-bailiwick only), got %d (%+v)", len(a.registered), a.registered)
	}
	if a.registered[0].host != "ns1.otech103.omani.works" {
		t.Errorf("registered host = %q, want ns1.otech103.omani.works (in-bailiwick should be skipped)", a.registered[0].host)
	}
}

// TestSetNSGlueOmittedSkipsRegister — when the request body has NO
// glueIP field, the handler MUST NOT touch RegisterGlueRecord even
// when the adapter implements GlueRegistrar. Backward-compat: existing
// pdmFlipNS callers that pre-date #900 keep working unchanged.
func TestSetNSGlueOmittedSkipsRegister(t *testing.T) {
	a := &fakeGlueAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newGlueTestHandler(t, a)
	body := `{
	  "domain":"customer-acme.tld",
	  "token":"t",
	  "nameservers":["ns1.otech103.omani.works","ns2.otech103.omani.works"]
	}`
	rec := doSetNS(t, h, "dynadot", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(a.registered) != 0 {
		t.Errorf("expected no glue registrations when glueIP omitted, got %d", len(a.registered))
	}
}

// TestSetNSGlueIgnoredForNonGlueAdapter — when the adapter does NOT
// implement GlueRegistrar (cloudflare/godaddy/etc), the handler MUST
// skip the glue path entirely even if glueIP is supplied. The set_ns
// flow proceeds as normal.
func TestSetNSGlueIgnoredForNonGlueAdapter(t *testing.T) {
	// Plain fakeAdapter does NOT embed glue methods.
	a := &fakeAdapter{name: "cloudflare"}
	h, _ := newTestHandler(t, a)
	body := `{
	  "domain":"customer-acme.tld",
	  "token":"t",
	  "nameservers":["ns1.otech103.omani.works"],
	  "glueIP":"203.0.113.42"
	}`
	rec := doSetNS(t, h, "cloudflare", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestIsInBailiwick — pure unit coverage for the in-/out-of-bailiwick
// classifier the SetNS handler uses to skip glue registration for NS
// hostnames that the customer's own DNS resolves.
func TestIsInBailiwick(t *testing.T) {
	cases := []struct {
		nsHost, zone string
		want         bool
	}{
		{"ns.customer-acme.tld", "customer-acme.tld", true},
		{"ns1.sub.customer-acme.tld", "customer-acme.tld", true},
		{"customer-acme.tld", "customer-acme.tld", true},
		{"ns1.otech103.omani.works", "customer-acme.tld", false},
		{"NS1.OTECH103.omani.works", "customer-acme.tld", false},
		{"ns.customer-acme.tld.", "customer-acme.tld.", true},
		{"", "customer-acme.tld", false},
		{"ns.x.com", "", false},
		{"customer-acme.tld.evil.com", "customer-acme.tld", false},
	}
	for _, tc := range cases {
		got := isInBailiwick(tc.nsHost, tc.zone)
		if got != tc.want {
			t.Errorf("isInBailiwick(%q, %q) = %v, want %v", tc.nsHost, tc.zone, got, tc.want)
		}
	}
}
