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
// This test exercises the built-in-defaults path (env vars unset), which
// must include the wizard's well-known pool domains as a defensive
// last-resort fallback.
func TestIsManagedDomain_PoolList(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	t.Setenv("DYNADOT_DOMAIN", "")
	ResetManagedDomains()
	defer ResetManagedDomains()

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

// TestIsManagedDomain_FromMultiDomainEnv exercises the canonical multi-domain
// path: DYNADOT_MANAGED_DOMAINS supplies the set, replacing the built-in
// defaults. This is the path that lets ops add a third pool domain
// (e.g. acme.io) without rebuilding the binary.
func TestIsManagedDomain_FromMultiDomainEnv(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "openova.io, omani.works,acme.io")
	t.Setenv("DYNADOT_DOMAIN", "")
	ResetManagedDomains()
	defer ResetManagedDomains()

	for _, d := range []string{"openova.io", "omani.works", "acme.io", "ACME.IO"} {
		if !IsManagedDomain(d) {
			t.Errorf("IsManagedDomain(%q) = false; expected true with DYNADOT_MANAGED_DOMAINS set", d)
		}
	}
	// A domain not in the set is rejected — proves the env var is the
	// source of truth, not the in-binary defaults.
	if IsManagedDomain("rogue.example") {
		t.Error("IsManagedDomain(rogue.example) should be false")
	}

	got := ManagedDomains()
	want := []string{"acme.io", "omani.works", "openova.io"} // sorted
	if len(got) != len(want) {
		t.Fatalf("ManagedDomains length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ManagedDomains[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestIsManagedDomain_FromLegacySingleDomain — the backward-compat path:
// when the secret only has the legacy `domain` key, DYNADOT_DOMAIN is the
// only env var set, and the resolver falls through to it.
func TestIsManagedDomain_FromLegacySingleDomain(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	t.Setenv("DYNADOT_DOMAIN", "openova.io")
	ResetManagedDomains()
	defer ResetManagedDomains()

	if !IsManagedDomain("openova.io") {
		t.Error("legacy single-domain path: openova.io should be managed")
	}
	// omani.works is NOT in the legacy single-domain set — proves the
	// fallback is "exact match", not "include the defaults too".
	if IsManagedDomain("omani.works") {
		t.Error("legacy single-domain path: omani.works should NOT be managed when only DYNADOT_DOMAIN=openova.io")
	}
}

// TestManagedDomains_TableDriven is the canonical multi-domain spec for #110.
// Each row is one resolution-order scenario; together they assert that the
// runtime configuration surface (DYNADOT_MANAGED_DOMAINS canonical →
// DYNADOT_DOMAIN legacy → built-in defaults) behaves as documented in the
// dynadot.go package comment.
//
// This complements the focused TestIsManagedDomain_* tests above by giving
// a single row-per-scenario matrix that's easy to extend when (e.g.) a new
// pool domain is added.
func TestManagedDomains_TableDriven(t *testing.T) {
	type queryCase struct {
		domain string
		want   bool
	}
	cases := []struct {
		name        string
		envMulti    string
		envLegacy   string
		wantSet     []string // sorted
		queries     []queryCase
	}{
		{
			name:     "canonical_multi_domain_env_list",
			envMulti: "openova.io,omani.works,acme.io",
			wantSet:  []string{"acme.io", "omani.works", "openova.io"},
			queries: []queryCase{
				{"openova.io", true},
				{"omani.works", true},
				{"acme.io", true},
				{"customer-byo.com", false},
			},
		},
		{
			name:     "canonical_multi_domain_whitespace_separated",
			envMulti: "openova.io  omani.works\tacme.io",
			wantSet:  []string{"acme.io", "omani.works", "openova.io"},
			queries: []queryCase{
				{"acme.io", true},
				{"openova.io", true},
			},
		},
		{
			name:     "case_insensitive_lookup_and_storage",
			envMulti: "OPENOVA.IO, OMANI.WORKS",
			wantSet:  []string{"omani.works", "openova.io"},
			queries: []queryCase{
				{"OPENOVA.IO", true},
				{"openova.io", true},
				{"Omani.Works", true},
			},
		},
		{
			name:     "whitespace_trimmed_in_query",
			envMulti: "openova.io",
			wantSet:  []string{"openova.io"},
			queries: []queryCase{
				{"  openova.io  ", true},
				{"\topenova.io\n", true},
			},
		},
		{
			name:      "legacy_single_domain_fallback",
			envMulti:  "",
			envLegacy: "openova.io",
			wantSet:   []string{"openova.io"},
			queries: []queryCase{
				{"openova.io", true},
				// legacy path is exact-set, NOT defaults-augmented:
				{"omani.works", false},
			},
		},
		{
			name:      "defaults_fallback_when_neither_env_set",
			envMulti:  "",
			envLegacy: "",
			wantSet:   []string{"omani.works", "openova.io"},
			queries: []queryCase{
				{"openova.io", true},
				{"omani.works", true},
				{"customer-byo.com", false},
			},
		},
		{
			name:      "canonical_takes_precedence_over_legacy",
			envMulti:  "acme.io,beta.io",
			envLegacy: "openova.io", // ignored when DYNADOT_MANAGED_DOMAINS is non-empty
			wantSet:   []string{"acme.io", "beta.io"},
			queries: []queryCase{
				{"acme.io", true},
				{"beta.io", true},
				{"openova.io", false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNADOT_MANAGED_DOMAINS", tc.envMulti)
			t.Setenv("DYNADOT_DOMAIN", tc.envLegacy)
			ResetManagedDomains()
			defer ResetManagedDomains()

			gotSet := ManagedDomains()
			if len(gotSet) != len(tc.wantSet) {
				t.Fatalf("ManagedDomains() = %v, want %v", gotSet, tc.wantSet)
			}
			for i := range tc.wantSet {
				if gotSet[i] != tc.wantSet[i] {
					t.Errorf("ManagedDomains()[%d] = %q, want %q", i, gotSet[i], tc.wantSet[i])
				}
			}
			for _, q := range tc.queries {
				if got := IsManagedDomain(q.domain); got != q.want {
					t.Errorf("IsManagedDomain(%q) = %v, want %v", q.domain, got, q.want)
				}
			}
		})
	}
}

// TestAddSovereignRecords_AllUseAddDNSToCurrentSetting verifies the
// "never wipe records" rule applies on every iteration of the loop —
// regression guard against #110 / feedback_dynadot_dns.md.
func TestAddSovereignRecords_AllUseAddDNSToCurrentSetting(t *testing.T) {
	srv, fake := newDynadotFakeServer()
	defer srv.Close()

	c := newClientPointingAt(srv.URL, "k", "s")
	if err := c.AddSovereignRecords(context.Background(), "omani.works", "omantel", "10.20.30.40"); err != nil {
		t.Fatalf("AddSovereignRecords: %v", err)
	}
	got := fake.recorded()
	if len(got) != 6 {
		t.Fatalf("expected 6 records, got %d", len(got))
	}
	for i, rr := range got {
		if rr.AddDNSToCurrentSetting != "yes" {
			t.Errorf("request %d (%s) missing add_dns_to_current_setting=yes — would WIPE existing DNS", i, rr.Subdomain)
		}
		if rr.Command != "set_dns2" {
			t.Errorf("request %d wrong command: %q", i, rr.Command)
		}
	}
}

// TestSplitDomainsList covers the parser edge cases.
func TestSplitDomainsList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"openova.io,omani.works", []string{"openova.io", "omani.works"}},
		{"openova.io omani.works", []string{"openova.io", "omani.works"}},
		{" openova.io , omani.works ", []string{"openova.io", "omani.works"}},
		{"OPENOVA.IO, OMANI.WORKS", []string{"openova.io", "omani.works"}},
		{"openova.io,openova.io", []string{"openova.io"}}, // dedupe
		{",,, ,", nil},
		{"", nil},
	}
	for _, tc := range cases {
		got := splitDomainsList(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitDomainsList(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitDomainsList(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
