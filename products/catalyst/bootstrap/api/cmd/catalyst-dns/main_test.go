// catalyst-dns — main_test.go
//
// Closes ticket #112 ("[G] dns: write A and CNAME records for new Sovereign
// during provisioning"). Asserts that running the binary against a mocked
// Dynadot endpoint produces exactly the canonical 6-record set the
// OpenTofu hetzner module's null_resource.dns_pool depends on:
//
//   *.<SUBDOMAIN>.<DOMAIN>      A → <LB_IP>
//   console.<SUBDOMAIN>.<DOMAIN> A → <LB_IP>
//   gitea.<SUBDOMAIN>.<DOMAIN>   A → <LB_IP>
//   harbor.<SUBDOMAIN>.<DOMAIN>  A → <LB_IP>
//   admin.<SUBDOMAIN>.<DOMAIN>   A → <LB_IP>
//   api.<SUBDOMAIN>.<DOMAIN>     A → <LB_IP>
//
// All requests must carry add_dns_to_current_setting=yes so we never wipe
// the zone (per feedback_dynadot_dns.md).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 ("never compromise quality"), the
// HTTP client, URL encoding, and JSON parsing are all the REAL package
// code paths — only the upstream Dynadot endpoint is substituted with a
// httptest.Server. Hitting api.dynadot.com would write real records and
// burn a real API quota every test run, which is precisely the failure
// mode the never-mock principle is designed to PREVENT in this case.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

// recordedRequest captures the relevant query params Dynadot's set_dns2
// endpoint expects.
type recordedRequest struct {
	Domain                 string
	Subdomain              string
	Command                string
	SubRecordType          string
	SubRecord              string
	SubTTL                 string
	AddDNSToCurrentSetting string
	APIKey                 string
	APISecret              string
}

type fakeDynadot struct {
	mu       sync.Mutex
	requests []recordedRequest
	failNth  int // if >0, fail the Nth request (1-indexed)
	failMsg  string
}

func newFakeDynadot() (*httptest.Server, *fakeDynadot) {
	f := &fakeDynadot{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			Domain:                 q.Get("domain"),
			Subdomain:              q.Get("subdomain0"),
			Command:                q.Get("command"),
			SubRecordType:          q.Get("sub_record_type0"),
			SubRecord:              q.Get("sub_record0"),
			SubTTL:                 q.Get("sub_recordx0"),
			AddDNSToCurrentSetting: q.Get("add_dns_to_current_setting"),
			APIKey:                 q.Get("key"),
			APISecret:              q.Get("secret"),
		})
		idx := len(f.requests)
		shouldFail := f.failNth > 0 && idx == f.failNth
		failMsg := f.failMsg
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if shouldFail {
			body, _ := json.Marshal(map[string]any{
				"SetDns2Response": map[string]any{
					"ResponseHeader": map[string]any{
						"ResponseCode": "-1",
						"Status":       "failed",
						"Error":        failMsg,
					},
				},
			})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	}))
	return srv, f
}

func (f *fakeDynadot) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// rewriteHostTransport redirects requests intended for api.dynadot.com to
// our httptest.Server while preserving the path + query string.
type rewriteHostTransport struct {
	scheme string
	host   string
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.scheme
	req.URL.Host = t.host
	req.Host = t.host
	return http.DefaultTransport.RoundTrip(req)
}

func clientPointingAt(srvURL, key, secret string) *dynadot.Client {
	c := dynadot.New(key, secret)
	c.HTTP.Timeout = 5 * time.Second

	scheme, host := "https", ""
	switch {
	case strings.HasPrefix(srvURL, "https://"):
		scheme = "https"
		host = strings.TrimPrefix(srvURL, "https://")
	case strings.HasPrefix(srvURL, "http://"):
		scheme = "http"
		host = strings.TrimPrefix(srvURL, "http://")
	default:
		panic("unknown scheme in test server URL: " + srvURL)
	}
	c.HTTP.Transport = &rewriteHostTransport{scheme: scheme, host: host}
	return c
}

// withManagedDomain ensures dynadot.IsManagedDomain returns true for the
// test domain regardless of which env vars CI happens to set.
func withManagedDomain(t *testing.T, domain string) {
	t.Helper()
	t.Setenv("DYNADOT_MANAGED_DOMAINS", domain)
	t.Setenv("DYNADOT_DOMAIN", "")
	dynadot.ResetManagedDomains()
	t.Cleanup(dynadot.ResetManagedDomains)
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

// TestRun_WritesSixCanonicalARecords is the headline assertion for #112:
// running catalyst-dns against a mocked Dynadot endpoint MUST produce six
// HTTP POSTs covering the wildcard + 5 component records, all carrying
// add_dns_to_current_setting=yes.
func TestRun_WritesSixCanonicalARecords(t *testing.T) {
	srv, fake := newFakeDynadot()
	defer srv.Close()
	withManagedDomain(t, "omani.works")

	client := clientPointingAt(srv.URL, "test-key", "test-secret")
	in := inputs{
		APIKey:    "test-key",
		APISecret: "test-secret",
		Domain:    "omani.works",
		Subdomain: "omantel",
		LBIP:      "203.0.113.42",
	}

	var stdout bytes.Buffer
	if err := run(context.Background(), client, in, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := fake.recorded()
	if len(got) != 6 {
		t.Fatalf("expected 6 Dynadot POSTs (wildcard + 5 components), got %d", len(got))
	}

	expectedSubdomains := []string{
		"*.omantel",
		"console.omantel",
		"gitea.omantel",
		"harbor.omantel",
		"admin.omantel",
		"api.omantel",
	}
	have := map[string]recordedRequest{}
	for _, rr := range got {
		have[rr.Subdomain] = rr
	}
	for _, sub := range expectedSubdomains {
		rr, ok := have[sub]
		if !ok {
			t.Errorf("missing subdomain %q in recorded requests", sub)
			continue
		}
		if rr.SubRecordType != "A" {
			t.Errorf("%s: expected A record, got %q", sub, rr.SubRecordType)
		}
		if rr.SubRecord != "203.0.113.42" {
			t.Errorf("%s: expected value 203.0.113.42, got %q", sub, rr.SubRecord)
		}
		if rr.Domain != "omani.works" {
			t.Errorf("%s: expected domain omani.works, got %q", sub, rr.Domain)
		}
		if rr.Command != "set_dns2" {
			t.Errorf("%s: expected set_dns2 command, got %q", sub, rr.Command)
		}
		if rr.AddDNSToCurrentSetting != "yes" {
			t.Errorf("%s: missing add_dns_to_current_setting=yes (would WIPE zone!)", sub)
		}
		if rr.APIKey != "test-key" || rr.APISecret != "test-secret" {
			t.Errorf("%s: auth params missing or wrong: key=%q secret=%q", sub, rr.APIKey, rr.APISecret)
		}
	}

	if !strings.Contains(stdout.String(), "Wrote 6 A records") {
		t.Errorf("stdout missing success message: %q", stdout.String())
	}
}

// TestRun_NeverWipesZone — the strict regression guard for the cardinal
// rule (feedback_dynadot_dns.md): every request emitted by catalyst-dns
// must carry add_dns_to_current_setting=yes. A regression that drops the
// flag on any iteration would silently delete the zone.
func TestRun_NeverWipesZone(t *testing.T) {
	srv, fake := newFakeDynadot()
	defer srv.Close()
	withManagedDomain(t, "openova.io")

	client := clientPointingAt(srv.URL, "k", "s")
	in := inputs{APIKey: "k", APISecret: "s", Domain: "openova.io", Subdomain: "alpha", LBIP: "1.2.3.4"}

	if err := run(context.Background(), client, in, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for i, rr := range fake.recorded() {
		if rr.AddDNSToCurrentSetting != "yes" {
			t.Errorf("request %d (%s): add_dns_to_current_setting=%q — MUST be 'yes' to avoid wiping zone", i, rr.Subdomain, rr.AddDNSToCurrentSetting)
		}
	}
}

// TestRun_ValidationErrors — each missing input surfaces a clear error;
// no Dynadot calls happen when validation fails (so the OpenTofu module
// gets a fast, deterministic failure mode).
func TestRun_ValidationErrors(t *testing.T) {
	withManagedDomain(t, "omani.works")
	srv, fake := newFakeDynadot()
	defer srv.Close()

	cases := []struct {
		name    string
		in      inputs
		wantSub string
	}{
		{"missing_api_key", inputs{APISecret: "s", Domain: "omani.works", Subdomain: "alpha", LBIP: "1.2.3.4"}, "DYNADOT_API_KEY"},
		{"missing_api_secret", inputs{APIKey: "k", Domain: "omani.works", Subdomain: "alpha", LBIP: "1.2.3.4"}, "DYNADOT_API_KEY"},
		{"missing_domain", inputs{APIKey: "k", APISecret: "s", Subdomain: "alpha", LBIP: "1.2.3.4"}, "DOMAIN"},
		{"missing_subdomain", inputs{APIKey: "k", APISecret: "s", Domain: "omani.works", LBIP: "1.2.3.4"}, "SUBDOMAIN"},
		{"missing_lb_ip", inputs{APIKey: "k", APISecret: "s", Domain: "omani.works", Subdomain: "alpha"}, "LB_IP"},
		{"unmanaged_domain", inputs{APIKey: "k", APISecret: "s", Domain: "rogue.example", Subdomain: "alpha", LBIP: "1.2.3.4"}, "managed-domain allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(fake.recorded())
			client := clientPointingAt(srv.URL, "k", "s")
			err := run(context.Background(), client, tc.in, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q; want it to contain %q", err.Error(), tc.wantSub)
			}
			// No Dynadot calls should have happened.
			if got := len(fake.recorded()); got != before {
				t.Errorf("validation error should not call Dynadot — request count went %d → %d", before, got)
			}
		})
	}
}

// TestRun_FailsFastOnDynadotError — when Dynadot rejects a record mid-loop,
// run returns the error promptly and does NOT keep writing the remaining
// records (so we don't leave a partially-applied zone behind).
func TestRun_FailsFastOnDynadotError(t *testing.T) {
	srv, fake := newFakeDynadot()
	defer srv.Close()
	withManagedDomain(t, "openova.io")
	fake.mu.Lock()
	fake.failNth = 1
	fake.failMsg = "rate limited"
	fake.mu.Unlock()

	client := clientPointingAt(srv.URL, "k", "s")
	in := inputs{APIKey: "k", APISecret: "s", Domain: "openova.io", Subdomain: "alpha", LBIP: "1.1.1.1"}
	err := run(context.Background(), client, in, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error from Dynadot rejection, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should surface Dynadot error string, got %q", err)
	}
	if got := len(fake.recorded()); got != 1 {
		t.Errorf("expected fail-fast after first request — got %d requests", got)
	}
}

// TestRun_NeverHitsRealDynadot is a paranoia-test: it proves the test
// harness substitutes the endpoint correctly. If a future refactor breaks
// the host rewrite, this test surfaces the regression by demonstrating
// that any unintercepted call to api.dynadot.com would be visible to a
// guarded transport.
func TestRun_NeverHitsRealDynadot(t *testing.T) {
	withManagedDomain(t, "omani.works")

	// 1. The happy path through clientPointingAt MUST hit the test server
	//    and never the real Dynadot host.
	srv, fake := newFakeDynadot()
	defer srv.Close()
	client := clientPointingAt(srv.URL, "k", "s")
	in := inputs{APIKey: "k", APISecret: "s", Domain: "omani.works", Subdomain: "alpha", LBIP: "1.1.1.1"}
	if err := run(context.Background(), client, in, &bytes.Buffer{}); err != nil {
		t.Fatalf("happy path through test server failed (rewrite broken?): %v", err)
	}
	if got := len(fake.recorded()); got != 6 {
		t.Errorf("expected 6 requests on test server, got %d (rewrite may be broken)", got)
	}

	// 2. A transport that refuses non-loopback hosts proves a missing
	//    rewrite would fail-loud rather than silently hit api.dynadot.com.
	guarded := dynadot.New("k", "s")
	guarded.HTTP = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.Host, "127.0.0.1") && !strings.HasPrefix(req.URL.Host, "localhost") {
			return nil, errors.New("test attempted to reach " + req.URL.String() + " — would hit real Dynadot")
		}
		return nil, errors.New("unreachable")
	}), Timeout: 2 * time.Second}
	err := guarded.AddRecord(context.Background(), "omani.works", dynadot.Record{Subdomain: "x", Type: "A", Value: "9.9.9.9"})
	if err == nil || !strings.Contains(err.Error(), "would hit real Dynadot") {
		t.Errorf("guard transport should have refused real Dynadot host, got err=%v", err)
	}
}

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestReadInputsFromEnv reads the documented env-var contract and ensures
// each value lands in the right struct field. Cheap belt-and-braces test
// to catch a typo in the env-var name.
func TestReadInputsFromEnv(t *testing.T) {
	t.Setenv("DYNADOT_API_KEY", "key123")
	t.Setenv("DYNADOT_API_SECRET", "sec456")
	t.Setenv("DOMAIN", "omani.works")
	t.Setenv("SUBDOMAIN", "omantel")
	t.Setenv("LB_IP", "203.0.113.42")

	got := readInputsFromEnv()
	want := inputs{
		APIKey:    "key123",
		APISecret: "sec456",
		Domain:    "omani.works",
		Subdomain: "omantel",
		LBIP:      "203.0.113.42",
	}
	if got != want {
		t.Errorf("readInputsFromEnv() = %+v, want %+v", got, want)
	}
}
