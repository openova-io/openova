// Package dynadot — integration tests for the Dynadot DNS API client.
//
// Closes ticket #146 — "[L] test: integration test — Dynadot API multi-domain
// DNS write".
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #2, "no mocks where the test
// would otherwise verify real behavior". The dynadot package's job is to
// build a real HTTP request against api.dynadot.com and parse the JSON
// response. We therefore stand up a real httptest.Server that emulates the
// Dynadot API contract (URL params, JSON envelope shape) and exercise the
// full request/response loop through the client. The HTTP transport, query
// encoding, response decoding and error surface paths are real; only the
// server-side logic is the test fixture.
//
// What is NOT mocked:
//   - net/http client behavior (real Client.HTTP transport)
//   - URL building (real url.Values encoding)
//   - JSON response parsing (real encoding/json on the wire bytes)
//   - The AddSovereignRecords loop semantics (six real HTTP requests per call)
//
// What IS substituted: the upstream Dynadot endpoint. This is unavoidable —
// hitting the real API would write to real DNS zones owned by OpenOva and
// cost real money on every test run. Per the package docstring "NEVER run
// exploratory set_dns2 calls — each one wipes all records" — using the real
// endpoint here would be a bigger violation of the inviolable principles
// than substituting it.
package dynadot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// dynadotFakeServer captures every request hitting the simulated Dynadot
// endpoint so tests can assert what the client actually sent.
type dynadotFakeServer struct {
	mu       sync.Mutex
	requests []recordedRequest
	// responder lets each test override how the server responds. Default is
	// "success".
	responder func(rr recordedRequest) (status int, body string)
}

type recordedRequest struct {
	Domain                  string
	Subdomain               string
	Command                 string
	MainRecordType          string
	MainRecord              string
	SubRecordType           string
	SubRecord               string
	AddDNSToCurrentSetting  string
	APIKey                  string
	APISecret               string
	TTL                     string
	RawQuery                string
}

func newDynadotFakeServer() (*httptest.Server, *dynadotFakeServer) {
	f := &dynadotFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rr := recordedRequest{
			Domain:                 q.Get("domain"),
			Subdomain:              q.Get("subdomain0"),
			Command:                q.Get("command"),
			MainRecordType:         q.Get("main_record_type0"),
			MainRecord:             q.Get("main_record0"),
			SubRecordType:          q.Get("sub_record_type0"),
			SubRecord:              q.Get("sub_record0"),
			AddDNSToCurrentSetting: q.Get("add_dns_to_current_setting"),
			APIKey:                 q.Get("key"),
			APISecret:              q.Get("secret"),
			RawQuery:               r.URL.RawQuery,
		}
		// TTL lives on whichever record kind is set.
		if rr.MainRecord != "" {
			rr.TTL = q.Get("main_recordx0")
		} else {
			rr.TTL = q.Get("sub_recordx0")
		}
		f.mu.Lock()
		f.requests = append(f.requests, rr)
		responder := f.responder
		f.mu.Unlock()

		status, body := http.StatusOK, `{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`
		if responder != nil {
			status, body = responder(rr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return srv, f
}

func (f *dynadotFakeServer) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *dynadotFakeServer) setResponder(fn func(rr recordedRequest) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responder = fn
}

// newClientPointingAt overrides the Dynadot endpoint for the duration of one
// test. We do this by wrapping the http.Client with a transport that rewrites
// the host on every outbound request. The package's source file builds the
// URL with "https://api.dynadot.com/api3.json?<params>"; rewriting the host
// keeps the rest of the URL (path + query) intact.
func newClientPointingAt(serverURL, key, secret string) *Client {
	c := New(key, secret)
	c.HTTP.Timeout = 5 * time.Second
	c.HTTP.Transport = &rewriteHostTransport{target: serverURL}
	return c
}

type rewriteHostTransport struct {
	target string
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point at the test server while preserving path+query.
	target, err := splitURL(t.target)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = target.scheme
	req.URL.Host = target.host
	req.Host = target.host
	return http.DefaultTransport.RoundTrip(req)
}

type splitURLResult struct {
	scheme string
	host   string
}

func splitURL(s string) (splitURLResult, error) {
	switch {
	case strings.HasPrefix(s, "https://"):
		return splitURLResult{scheme: "https", host: strings.TrimPrefix(s, "https://")}, nil
	case strings.HasPrefix(s, "http://"):
		return splitURLResult{scheme: "http", host: strings.TrimPrefix(s, "http://")}, nil
	}
	return splitURLResult{}, errors.New("unknown scheme")
}

// -------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------

// TestAddRecord_ApexAndSubdomainEncoding verifies that AddRecord chooses the
// "main_record*" parameter form for apex records and the "subdomain*" form
// for non-apex records — Dynadot's API is sensitive to this distinction.
func TestAddRecord_ApexAndSubdomainEncoding(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	c := newClientPointingAt(srv.URL, "test-key", "test-secret")
	ctx := context.Background()

	// Apex (subdomain "" or "@") should use main_record*.
	if err := c.AddRecord(ctx, "openova.io", Record{Subdomain: "", Type: "A", Value: "1.2.3.4", TTL: 300}); err != nil {
		t.Fatalf("apex AddRecord: %v", err)
	}
	if err := c.AddRecord(ctx, "openova.io", Record{Subdomain: "@", Type: "TXT", Value: "v=spf1 -all", TTL: 300}); err != nil {
		t.Fatalf("@ AddRecord: %v", err)
	}
	// Non-apex should use subdomain*.
	if err := c.AddRecord(ctx, "openova.io", Record{Subdomain: "console", Type: "A", Value: "5.6.7.8", TTL: 300}); err != nil {
		t.Fatalf("subdomain AddRecord: %v", err)
	}

	got := fake.recorded()
	if len(got) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(got))
	}

	// Apex
	if got[0].MainRecord != "1.2.3.4" || got[0].MainRecordType != "A" || got[0].SubRecord != "" {
		t.Errorf("apex A record encoding wrong: %+v", got[0])
	}
	if got[0].Domain != "openova.io" {
		t.Errorf("apex domain wrong: %q", got[0].Domain)
	}
	// @ also goes to main_record*
	if got[1].MainRecord != "v=spf1 -all" || got[1].MainRecordType != "TXT" || got[1].SubRecord != "" {
		t.Errorf("@ TXT encoding wrong: %+v", got[1])
	}
	// Non-apex
	if got[2].Subdomain != "console" || got[2].SubRecord != "5.6.7.8" || got[2].SubRecordType != "A" || got[2].MainRecord != "" {
		t.Errorf("subdomain A record encoding wrong: %+v", got[2])
	}

	// All three must have add_dns_to_current_setting=yes per the
	// "never wipe records" requirement.
	for i, rr := range got {
		if rr.AddDNSToCurrentSetting != "yes" {
			t.Errorf("request %d missing add_dns_to_current_setting=yes (got %q) — would wipe DNS records", i, rr.AddDNSToCurrentSetting)
		}
		if rr.Command != "set_dns2" {
			t.Errorf("request %d wrong command: %q", i, rr.Command)
		}
		if rr.APIKey != "test-key" || rr.APISecret != "test-secret" {
			t.Errorf("request %d auth missing: key=%q secret=%q", i, rr.APIKey, rr.APISecret)
		}
	}
}

// TestAddRecord_DefaultTTL verifies that omitting TTL falls back to 300s.
func TestAddRecord_DefaultTTL(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	c := newClientPointingAt(srv.URL, "k", "s")
	if err := c.AddRecord(context.Background(), "openova.io", Record{Subdomain: "x", Type: "A", Value: "1.1.1.1"}); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	got := fake.recorded()
	if len(got) != 1 || got[0].TTL != "300" {
		t.Errorf("default TTL not applied: %+v", got)
	}
}

// TestAddSovereignRecords_WritesSixRecords is the canonical multi-record
// scenario: provisioning a Sovereign writes wildcard + 5 component records.
func TestAddSovereignRecords_WritesSixRecords(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	c := newClientPointingAt(srv.URL, "k", "s")
	if err := c.AddSovereignRecords(context.Background(), "omani.works", "omantel", "10.20.30.40"); err != nil {
		t.Fatalf("AddSovereignRecords: %v", err)
	}
	got := fake.recorded()
	if len(got) != 6 {
		t.Fatalf("expected 6 records (wildcard + 5 component prefixes), got %d", len(got))
	}

	// Build a set of (subdomain, value) we expect.
	want := map[string]string{
		"*.omantel":       "10.20.30.40",
		"console.omantel": "10.20.30.40",
		"gitea.omantel":   "10.20.30.40",
		"harbor.omantel":  "10.20.30.40",
		"admin.omantel":   "10.20.30.40",
		"api.omantel":     "10.20.30.40",
	}
	have := make(map[string]string)
	for _, rr := range got {
		// All records are non-apex (they're prefixed under the omantel
		// subdomain), so they MUST use sub_record* fields.
		if rr.SubRecord == "" {
			t.Errorf("expected sub_record for %q, got %+v", rr.Subdomain, rr)
			continue
		}
		if rr.SubRecordType != "A" {
			t.Errorf("expected A record for %q, got %s", rr.Subdomain, rr.SubRecordType)
		}
		if rr.Domain != "omani.works" {
			t.Errorf("expected domain omani.works for %q, got %q", rr.Subdomain, rr.Domain)
		}
		have[rr.Subdomain] = rr.SubRecord
	}
	for sub, ip := range want {
		if have[sub] != ip {
			t.Errorf("missing %q -> %q (got %q)", sub, ip, have[sub])
		}
	}
}

// TestMultiDomain_PoolDomainsHaveIndependentRecords exercises the
// multi-domain capability — the same client instance writes records to
// openova.io AND omani.works in sequence, with each request hitting Dynadot
// scoped to the correct `domain=` parameter.
func TestMultiDomain_PoolDomainsHaveIndependentRecords(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	c := newClientPointingAt(srv.URL, "k", "s")
	ctx := context.Background()

	// Sovereign A on openova.io
	if err := c.AddSovereignRecords(ctx, "openova.io", "alpha", "1.1.1.1"); err != nil {
		t.Fatalf("alpha provisioning: %v", err)
	}
	// Sovereign B on omani.works (separate pool domain)
	if err := c.AddSovereignRecords(ctx, "omani.works", "beta", "2.2.2.2"); err != nil {
		t.Fatalf("beta provisioning: %v", err)
	}

	got := fake.recorded()
	if len(got) != 12 {
		t.Fatalf("expected 12 records (6 per Sovereign × 2 Sovereigns), got %d", len(got))
	}
	openovaCount, omaniCount := 0, 0
	for _, rr := range got {
		switch rr.Domain {
		case "openova.io":
			openovaCount++
			if rr.SubRecord != "1.1.1.1" {
				t.Errorf("openova.io record points at wrong IP: %+v", rr)
			}
			if !strings.HasSuffix(rr.Subdomain, "alpha") {
				t.Errorf("openova.io record %q not under alpha subdomain", rr.Subdomain)
			}
		case "omani.works":
			omaniCount++
			if rr.SubRecord != "2.2.2.2" {
				t.Errorf("omani.works record points at wrong IP: %+v", rr)
			}
			if !strings.HasSuffix(rr.Subdomain, "beta") {
				t.Errorf("omani.works record %q not under beta subdomain", rr.Subdomain)
			}
		default:
			t.Errorf("unexpected domain %q in request", rr.Domain)
		}
	}
	if openovaCount != 6 || omaniCount != 6 {
		t.Errorf("uneven record split: openova=%d omani=%d", openovaCount, omaniCount)
	}
}

// TestAddRecord_DynadotErrorIsSurfacedAsGoError exercises the failure path —
// a Dynadot envelope with ResponseCode != 0 and Status != "success" must
// produce an error from AddRecord (so callers fail loudly instead of
// silently silently believing DNS was written).
func TestAddRecord_DynadotErrorIsSurfacedAsGoError(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()
	fake.setResponder(func(rr recordedRequest) (int, string) {
		// Dynadot's actual error envelope shape — code = -1, error = string.
		body, _ := json.Marshal(map[string]any{
			"SetDns2Response": map[string]any{
				"ResponseHeader": map[string]any{
					"ResponseCode": "-1",
					"Status":       "failed",
					"Error":        "domain not found in account",
				},
			},
		})
		return http.StatusOK, string(body)
	})

	c := newClientPointingAt(srv.URL, "k", "s")
	err := c.AddRecord(context.Background(), "not-mine.example", Record{Subdomain: "x", Type: "A", Value: "9.9.9.9"})
	if err == nil {
		t.Fatal("expected error when Dynadot returns failed status, got nil")
	}
	if !strings.Contains(err.Error(), "domain not found") {
		t.Errorf("error should surface Dynadot error string, got %q", err.Error())
	}
}

// TestAddRecord_HTTPErrorSurfaced exercises the 5xx path.
func TestAddRecord_HTTPErrorSurfaced(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()
	fake.setResponder(func(rr recordedRequest) (int, string) {
		return http.StatusInternalServerError, "service unavailable"
	})

	c := newClientPointingAt(srv.URL, "k", "s")
	err := c.AddRecord(context.Background(), "openova.io", Record{Subdomain: "x", Type: "A", Value: "1.1.1.1"})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got %q", err.Error())
	}
}

// TestAddSovereignRecords_FailsFastOnFirstError verifies that if Dynadot
// rejects the first record, the function returns rather than continuing
// (otherwise we'd write a partial record set, which is harder to clean up
// than a clean failure).
func TestAddSovereignRecords_FailsFastOnFirstError(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	var count int
	var mu sync.Mutex
	fake.setResponder(func(rr recordedRequest) (int, string) {
		mu.Lock()
		count++
		current := count
		mu.Unlock()
		if current == 1 {
			body, _ := json.Marshal(map[string]any{
				"SetDns2Response": map[string]any{
					"ResponseHeader": map[string]any{
						"ResponseCode": "-2",
						"Status":       "failed",
						"Error":        "rate limited",
					},
				},
			})
			return http.StatusOK, string(body)
		}
		return http.StatusOK, `{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`
	})

	c := newClientPointingAt(srv.URL, "k", "s")
	err := c.AddSovereignRecords(context.Background(), "openova.io", "alpha", "1.1.1.1")
	if err == nil {
		t.Fatal("expected error from AddSovereignRecords, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected error to surface 'rate limited', got %q", err)
	}
	// Only the first request should have been made.
	if got := len(fake.recorded()); got != 1 {
		t.Errorf("fail-fast violated — expected 1 request, got %d", got)
	}
}

// TestIsManagedDomain_PoolList — the helper that gates whether we attempt
// Dynadot writes at all. Misclassifying a BYO domain as "managed" would
// trigger Dynadot calls against a domain we don't own.
//
// We unset both DYNADOT_MANAGED_DOMAINS and DYNADOT_DOMAIN so the helper
// falls through to its built-in default allowlist.
func TestIsManagedDomain_PoolList(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	t.Setenv("DYNADOT_DOMAIN", "")
	resetManagedDomainsCache()
	t.Cleanup(resetManagedDomainsCache)

	cases := []struct {
		in   string
		want bool
	}{
		{"openova.io", true},
		{"omani.works", true},
		{"OPENOVA.IO", true},      // case-insensitive
		{" openova.io ", true},    // trims whitespace
		{"customer-byo.com", false},
		{"example.org", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsManagedDomain(tc.in); got != tc.want {
			t.Errorf("IsManagedDomain(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestManagedDomains_FromEnvList — production wiring: the K8s secret sets
// DYNADOT_MANAGED_DOMAINS to a comma-separated list. This is the canonical
// path the catalyst-api uses in real deployments.
func TestManagedDomains_FromEnvList(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{
			name: "two domains, simple",
			env:  "openova.io,omani.works",
			want: []string{"openova.io", "omani.works"},
		},
		{
			name: "extra whitespace gets trimmed",
			env:  " openova.io , omani.works , some-pool.example  ",
			want: []string{"openova.io", "omani.works", "some-pool.example"},
		},
		{
			name: "case-insensitive normalisation",
			env:  "OPENOVA.IO,Omani.Works",
			want: []string{"openova.io", "omani.works"},
		},
		{
			name: "empty entries get dropped",
			env:  "openova.io,,omani.works,",
			want: []string{"openova.io", "omani.works"},
		},
		{
			name: "duplicates collapsed, first-seen order preserved",
			env:  "omani.works,openova.io,Omani.Works,OPENOVA.IO",
			want: []string{"omani.works", "openova.io"},
		},
		{
			name: "single-domain comma-separated still works",
			env:  "single-pool.example",
			want: []string{"single-pool.example"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNADOT_MANAGED_DOMAINS", tc.env)
			t.Setenv("DYNADOT_DOMAIN", "")
			resetManagedDomainsCache()
			t.Cleanup(resetManagedDomainsCache)

			got := ManagedDomains()
			if !equalStringSlices(got, tc.want) {
				t.Errorf("ManagedDomains() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestManagedDomains_LegacyDynadotDomainFallback — during a rolling secret
// upgrade, the old key DYNADOT_DOMAIN may still be present alone. The helper
// must still behave (returning that single domain) so in-flight pods don't
// crash.
func TestManagedDomains_LegacyDynadotDomainFallback(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{name: "single legacy value", env: "openova.io", want: []string{"openova.io"}},
		{name: "uppercased gets normalised", env: "OPENOVA.IO", want: []string{"openova.io"}},
		{name: "wraps whitespace", env: "  omani.works  ", want: []string{"omani.works"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
			t.Setenv("DYNADOT_DOMAIN", tc.env)
			resetManagedDomainsCache()
			t.Cleanup(resetManagedDomainsCache)

			got := ManagedDomains()
			if !equalStringSlices(got, tc.want) {
				t.Errorf("ManagedDomains() (legacy fallback) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestManagedDomains_BuiltinDefaultsWhenNoEnv — local dev / unit-test path:
// neither env var is set, so the helper returns its built-in defaults. This
// is the only test that asserts the literal default list — production never
// runs this path (the K8s secret always sets DYNADOT_MANAGED_DOMAINS).
func TestManagedDomains_BuiltinDefaultsWhenNoEnv(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	t.Setenv("DYNADOT_DOMAIN", "")
	resetManagedDomainsCache()
	t.Cleanup(resetManagedDomainsCache)

	got := ManagedDomains()
	want := []string{"openova.io", "omani.works"}
	if !equalStringSlices(got, want) {
		t.Errorf("ManagedDomains() defaults = %v, want %v", got, want)
	}
}

// TestManagedDomains_EnvListBeatsLegacy — when both env vars are set, the
// canonical comma-separated list wins. This protects against accidental
// double-configuration during a migration where someone forgets to remove
// the old key.
func TestManagedDomains_EnvListBeatsLegacy(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "new-pool.example,another.example")
	t.Setenv("DYNADOT_DOMAIN", "legacy.example")
	resetManagedDomainsCache()
	t.Cleanup(resetManagedDomainsCache)

	got := ManagedDomains()
	want := []string{"new-pool.example", "another.example"}
	if !equalStringSlices(got, want) {
		t.Errorf("env list should win over legacy: got %v, want %v", got, want)
	}
}

// TestIsManagedDomain_HonoursEnvList — the main behaviour change for
// multi-domain support: customer-byo.example becomes "managed" when the
// secret advertises it, without any code change.
func TestIsManagedDomain_HonoursEnvList(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "openova.io,custom-pool.example")
	t.Setenv("DYNADOT_DOMAIN", "")
	resetManagedDomainsCache()
	t.Cleanup(resetManagedDomainsCache)

	if !IsManagedDomain("custom-pool.example") {
		t.Error("custom-pool.example should be managed when present in DYNADOT_MANAGED_DOMAINS")
	}
	if !IsManagedDomain("CUSTOM-POOL.EXAMPLE") {
		t.Error("IsManagedDomain should be case-insensitive (uppercase miss)")
	}
	if !IsManagedDomain(" custom-pool.example ") {
		t.Error("IsManagedDomain should trim whitespace")
	}
	if IsManagedDomain("omani.works") {
		t.Error("omani.works should NOT be managed when it's been removed from the env list")
	}
}

// equalStringSlices returns true iff a and b have the same length and the
// same elements in the same order. A small helper to keep the test bodies
// readable.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
