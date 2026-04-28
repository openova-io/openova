// catalyst-dns — unit tests for the OpenTofu null_resource.dns_pool helper.
//
// Closes #112 — write A and CNAME records for new Sovereign during
// provisioning.
//
// Per docs/INVIOLABLE-PRINCIPLES.md principle #2 ("never compromise quality"):
// these tests verify the binary's full request loop end-to-end, but we
// substitute the upstream Dynadot endpoint with a httptest.NewServer because
// hitting the real Dynadot API would write to live DNS zones and is
// explicitly forbidden by the package docstring ("NEVER run exploratory
// set_dns2 calls — each one wipes all records"). Everything else is real:
// the dynadot.Client builds real HTTP requests, encodes real query
// parameters, parses real JSON responses, and emits the real 6-record set.
package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

// dnsFakeServer captures every Dynadot request the catalyst-dns binary
// produces so the test can assert what got written.
type dnsFakeServer struct {
	mu       sync.Mutex
	requests []capturedRequest
}

type capturedRequest struct {
	Domain                 string
	Subdomain              string
	Command                string
	SubRecordType          string
	SubRecord              string
	SubRecordTTL           string
	AddDNSToCurrentSetting string
	APIKey                 string
	APISecret              string
}

func newDNSFakeServer(t *testing.T) (*httptest.Server, *dnsFakeServer) {
	t.Helper()
	f := &dnsFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.requests = append(f.requests, capturedRequest{
			Domain:                 q.Get("domain"),
			Subdomain:              q.Get("subdomain0"),
			Command:                q.Get("command"),
			SubRecordType:          q.Get("sub_record_type0"),
			SubRecord:              q.Get("sub_record0"),
			SubRecordTTL:           q.Get("sub_recordx0"),
			AddDNSToCurrentSetting: q.Get("add_dns_to_current_setting"),
			APIKey:                 q.Get("key"),
			APISecret:              q.Get("secret"),
		})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	}))
	return srv, f
}

func (f *dnsFakeServer) snapshot() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// rewriteHostTransport sends every outbound request to the test server while
// preserving path + query — the dynadot package builds the URL with
// "https://api.dynadot.com/api3.json?<params>"; we keep params, redirect host.
type rewriteHostTransport struct {
	scheme, host string
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.scheme
	req.URL.Host = t.host
	req.Host = t.host
	return http.DefaultTransport.RoundTrip(req)
}

func clientFactoryPointingAt(serverURL string) func(apiKey, apiSecret string) *dynadot.Client {
	scheme, host, ok := splitURL(serverURL)
	if !ok {
		// Surface the unusable serverURL by returning a factory that builds a
		// broken client — the test will fail with a clear network error.
		return func(apiKey, apiSecret string) *dynadot.Client {
			c := dynadot.New(apiKey, apiSecret)
			c.HTTP.Timeout = 1 * time.Second
			return c
		}
	}
	return func(apiKey, apiSecret string) *dynadot.Client {
		c := dynadot.New(apiKey, apiSecret)
		c.HTTP.Timeout = 5 * time.Second
		c.HTTP.Transport = &rewriteHostTransport{scheme: scheme, host: host}
		return c
	}
}

func splitURL(s string) (scheme, host string, ok bool) {
	if strings.HasPrefix(s, "https://") {
		return "https", strings.TrimPrefix(s, "https://"), true
	}
	if strings.HasPrefix(s, "http://") {
		return "http", strings.TrimPrefix(s, "http://"), true
	}
	return "", "", false
}

// -------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------

// TestRun_WritesSixCanonicalRecords — the headline behaviour.
//
// The OpenTofu null_resource.dns_pool depends on this binary writing
// EXACTLY the 6-record set that AddSovereignRecords specifies, with
// add_dns_to_current_setting=yes on every call (per the auto-memory rule
// "every exploratory call wipes records").
func TestRun_WritesSixCanonicalRecords(t *testing.T) {
	srv, fake := newDNSFakeServer(t)
	defer srv.Close()

	args := runArgs{
		APIKey:    "fake-key",
		APISecret: "fake-secret",
		Domain:    "omani.works",
		Subdomain: "omantel",
		LBIP:      "203.0.113.42",
	}

	msg, err := run(context.Background(), args, clientFactoryPointingAt(srv.URL))
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !strings.Contains(msg, "Wrote 6 A records") {
		t.Errorf("expected success banner mentioning 6 records, got %q", msg)
	}

	got := fake.snapshot()
	if len(got) != 6 {
		t.Fatalf("expected 6 Dynadot POSTs, got %d (records: %+v)", len(got), got)
	}

	// Every request must use the safe-append flag.
	for i, rr := range got {
		if rr.AddDNSToCurrentSetting != "yes" {
			t.Errorf("request %d missing add_dns_to_current_setting=yes (got %q) — would wipe DNS records",
				i, rr.AddDNSToCurrentSetting)
		}
		if rr.Command != "set_dns2" {
			t.Errorf("request %d wrong command: %q", i, rr.Command)
		}
		if rr.Domain != "omani.works" {
			t.Errorf("request %d wrong domain: %q (want omani.works)", i, rr.Domain)
		}
		if rr.SubRecordType != "A" {
			t.Errorf("request %d wrong type: %q (want A)", i, rr.SubRecordType)
		}
		if rr.SubRecord != "203.0.113.42" {
			t.Errorf("request %d wrong IP: %q (want 203.0.113.42)", i, rr.SubRecord)
		}
		if rr.APIKey != "fake-key" || rr.APISecret != "fake-secret" {
			t.Errorf("request %d missing/wrong creds: key=%q secret=%q", i, rr.APIKey, rr.APISecret)
		}
	}

	// All six expected subdomains under "omantel" must be present.
	expectSubdomains := map[string]struct{}{
		"*.omantel":       {},
		"console.omantel": {},
		"gitea.omantel":   {},
		"harbor.omantel":  {},
		"admin.omantel":   {},
		"api.omantel":     {},
	}
	seen := map[string]struct{}{}
	for _, rr := range got {
		seen[rr.Subdomain] = struct{}{}
	}
	for sub := range expectSubdomains {
		if _, ok := seen[sub]; !ok {
			t.Errorf("missing canonical subdomain %q from Dynadot writes (saw %v)", sub, keysOf(seen))
		}
	}
	for sub := range seen {
		if _, ok := expectSubdomains[sub]; !ok {
			t.Errorf("unexpected subdomain %q in Dynadot writes (drift from canonical 6-record set)", sub)
		}
	}
}

// TestRun_RejectsUnmanagedDomain — defence in depth: even if a malicious /
// mistyped DOMAIN env var slips through, the binary refuses rather than
// writing records to a domain we don't own.
func TestRun_RejectsUnmanagedDomain(t *testing.T) {
	srv, fake := newDNSFakeServer(t)
	defer srv.Close()

	args := runArgs{
		APIKey:    "k",
		APISecret: "s",
		Domain:    "not-our-domain.example",
		Subdomain: "x",
		LBIP:      "1.2.3.4",
	}
	_, err := run(context.Background(), args, clientFactoryPointingAt(srv.URL))
	if err == nil {
		t.Fatal("expected error for unmanaged domain, got nil")
	}
	if !strings.Contains(err.Error(), "managed-domain allowlist") {
		t.Errorf("expected allowlist-rejection error, got %q", err)
	}
	if got := len(fake.snapshot()); got != 0 {
		t.Errorf("expected 0 Dynadot calls for unmanaged domain, got %d (DNS would have been written)", got)
	}
}

// TestRun_MissingArgs — each required field surfaces a clear error.
func TestRun_MissingArgs(t *testing.T) {
	srv, _ := newDNSFakeServer(t)
	defer srv.Close()
	factory := clientFactoryPointingAt(srv.URL)

	cases := []struct {
		name      string
		args      runArgs
		wantInErr string
	}{
		{
			name:      "missing API key",
			args:      runArgs{APISecret: "s", Domain: "omani.works", Subdomain: "x", LBIP: "1.2.3.4"},
			wantInErr: "DYNADOT_API_KEY",
		},
		{
			name:      "missing API secret",
			args:      runArgs{APIKey: "k", Domain: "omani.works", Subdomain: "x", LBIP: "1.2.3.4"},
			wantInErr: "DYNADOT_API_SECRET",
		},
		{
			name:      "missing DOMAIN",
			args:      runArgs{APIKey: "k", APISecret: "s", Subdomain: "x", LBIP: "1.2.3.4"},
			wantInErr: "DOMAIN must be set",
		},
		{
			name:      "missing SUBDOMAIN",
			args:      runArgs{APIKey: "k", APISecret: "s", Domain: "omani.works", LBIP: "1.2.3.4"},
			wantInErr: "SUBDOMAIN must be set",
		},
		{
			name:      "missing LB_IP",
			args:      runArgs{APIKey: "k", APISecret: "s", Domain: "omani.works", Subdomain: "x"},
			wantInErr: "LB_IP must be set",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(context.Background(), tc.args, factory)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("expected error containing %q, got %q", tc.wantInErr, err)
			}
		})
	}
}

// TestRun_FailsFastOnDynadotError — when Dynadot rejects the first record,
// the binary must surface that error instead of writing a partial record set.
func TestRun_FailsFastOnDynadotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"-1","Status":"failed","Error":"rate limited"}}}`))
	}))
	defer srv.Close()

	args := runArgs{
		APIKey: "k", APISecret: "s",
		Domain: "omani.works", Subdomain: "omantel", LBIP: "1.2.3.4",
	}
	_, err := run(context.Background(), args, clientFactoryPointingAt(srv.URL))
	if err == nil {
		t.Fatal("expected error from Dynadot failure, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected error to surface 'rate limited', got %q", err)
	}
}

// TestRun_ContextDeadline — the 2-minute timeout from main() is honoured.
// Here we set a 50ms deadline against a server that hangs forever; the
// request must error out cleanly rather than blocking the binary.
func TestRun_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until client gives up
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	args := runArgs{
		APIKey: "k", APISecret: "s",
		Domain: "omani.works", Subdomain: "omantel", LBIP: "1.2.3.4",
	}
	_, err := run(ctx, args, clientFactoryPointingAt(srv.URL))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// Either context.DeadlineExceeded or wrapped client error is acceptable.
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "deadline") &&
		!strings.Contains(strings.ToLower(err.Error()), "context") &&
		!strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Errorf("expected deadline/timeout error, got %q", err)
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
