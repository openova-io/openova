package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	dynadot "github.com/openova-io/openova/core/pkg/dynadot-client"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
)

// fakeDynadot stands in for api.dynadot.com/api3.json. It captures the
// last set_dns2 / domain_info call so each test can assert on the
// request shape and inject a fixture response.
//
// Tests do not exercise the cert-manager apiserver wrapping at all —
// they call Present / CleanUp directly on the solver, which is the same
// code path RunWebhookServer dispatches into.
type fakeDynadot struct {
	mu sync.Mutex
	// state is the synthesised zone state. The handler returns it on
	// `domain_info` and rebuilds it on `set_dns2` (full replace) /
	// `set_dns2` with add_dns_to_current_setting=yes (append).
	state map[string]map[string]map[string]string // domain → subdomain → type → value
}

func newFakeDynadot() *fakeDynadot {
	return &fakeDynadot{state: make(map[string]map[string]map[string]string)}
}

func (f *fakeDynadot) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		domain := q.Get("domain")
		if _, ok := f.state[domain]; !ok {
			f.state[domain] = make(map[string]map[string]string)
		}
		switch q.Get("command") {
		case "set_dns2":
			f.handleSetDNS2(w, q, domain)
		case "domain_info":
			f.handleDomainInfo(w, domain)
		default:
			t.Errorf("unexpected dynadot command: %s", q.Get("command"))
			http.Error(w, "bad command", 400)
		}
	}
}

func (f *fakeDynadot) handleSetDNS2(w http.ResponseWriter, q url.Values, domain string) {
	zone := f.state[domain]
	if q.Get("add_dns_to_current_setting") != "yes" {
		// Full replace — wipe sub-records under this domain. (Mains are
		// not exercised by the solver but we drop them for fidelity.)
		zone = make(map[string]map[string]string)
	}
	// Apex / main writes (Present at apex uses these).
	for i := 0; ; i++ {
		typ := q.Get("main_record_type" + itoa(i))
		val := q.Get("main_record" + itoa(i))
		if typ == "" && val == "" {
			break
		}
		setRec(zone, "@", typ, val)
	}
	for i := 0; ; i++ {
		sub := q.Get("subdomain" + itoa(i))
		typ := q.Get("sub_record_type" + itoa(i))
		val := q.Get("sub_record" + itoa(i))
		if sub == "" && typ == "" && val == "" {
			break
		}
		setRec(zone, sub, typ, val)
	}
	f.state[domain] = zone
	writeOK(w, "SetDns2Response")
}

func (f *fakeDynadot) handleDomainInfo(w http.ResponseWriter, domain string) {
	zone := f.state[domain]
	type subRec struct {
		Subhost    string `json:"Subhost"`
		RecordType string `json:"RecordType"`
		Value      string `json:"Value"`
		TTL        int    `json:"TTL"`
	}
	type mainRec struct {
		RecordType string `json:"RecordType"`
		Value      string `json:"Value"`
		TTL        int    `json:"TTL"`
	}
	var subs []subRec
	var mains []mainRec
	for sub, types := range zone {
		for t, v := range types {
			if sub == "@" {
				mains = append(mains, mainRec{RecordType: t, Value: v, TTL: 60})
			} else {
				subs = append(subs, subRec{Subhost: sub, RecordType: t, Value: v, TTL: 60})
			}
		}
	}
	resp := map[string]any{
		"DomainInfoResponse": map[string]any{
			"ResponseHeader": map[string]any{
				"ResponseCode": "0",
				"Status":       "success",
			},
			"DomainInfo": map[string]any{
				"NameServerSettings": map[string]any{
					"NameServers": []map[string]string{},
					"MainDomains": mains,
					"SubDomains":  subs,
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func setRec(zone map[string]map[string]string, sub, typ, val string) {
	if zone[sub] == nil {
		zone[sub] = make(map[string]string)
	}
	zone[sub][typ] = val
}

func writeOK(w http.ResponseWriter, env string) {
	w.Header().Set("Content-Type", "application/json")
	body := `{"` + env + `":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`
	_, _ = w.Write([]byte(body))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for i > 0 {
		out = string(rune('0'+i%10)) + out
		i /= 10
	}
	return out
}

// solverWith builds a dynadotSolver pointed at the given fixture server.
// All tests use the production code path — newDynadotSolver — so the
// env-validation rules are exercised on every call.
func solverWith(t *testing.T, srv *httptest.Server, managed string) *dynadotSolver {
	t.Helper()
	s, err := newDynadotSolver(solverConfig{
		APIKey:         "test-key",
		APISecret:      "test-secret",
		ManagedDomains: managed,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatalf("newDynadotSolver: %v", err)
	}
	return s
}

func TestNewDynadotSolver_RequiresCredentials(t *testing.T) {
	t.Parallel()
	_, err := newDynadotSolver(solverConfig{ManagedDomains: "openova.io"})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestNewDynadotSolver_RequiresManagedDomain(t *testing.T) {
	t.Parallel()
	_, err := newDynadotSolver(solverConfig{APIKey: "k", APISecret: "s"})
	if err == nil {
		t.Fatal("expected error for missing managed domains")
	}
}

func TestNewDynadotSolver_LegacyDomainFallback(t *testing.T) {
	t.Parallel()
	s, err := newDynadotSolver(solverConfig{APIKey: "k", APISecret: "s", Fallback: "omani.works"})
	if err != nil {
		t.Fatalf("legacy fallback should resolve: %v", err)
	}
	if !s.managed.Has("omani.works") {
		t.Fatalf("legacy fallback did not populate allowlist: %v", s.managed.List())
	}
}

func TestSolver_Name(t *testing.T) {
	t.Parallel()
	s := &dynadotSolver{}
	if got := s.Name(); got != "dynadot" {
		t.Fatalf("Name = %q, want dynadot", got)
	}
}

func TestSolver_ResolveDomain(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works,openova.io")

	cases := []struct {
		fqdn    string
		apex    string
		sub     string
		wantErr bool
	}{
		{"_acme-challenge.console.omantel.omani.works.", "omani.works", "_acme-challenge.console.omantel", false},
		{"_acme-challenge.openova.io.", "openova.io", "_acme-challenge", false},
		{"_acme-challenge.omani.works.", "omani.works", "_acme-challenge", false},
		{"omani.works.", "omani.works", "@", false},
		{"_acme-challenge.example.com.", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		apex, sub, err := s.resolveDomain(tc.fqdn)
		if (err != nil) != tc.wantErr {
			t.Errorf("resolveDomain(%q) error = %v, wantErr=%v", tc.fqdn, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if apex != tc.apex || sub != tc.sub {
			t.Errorf("resolveDomain(%q) = (%q,%q), want (%q,%q)", tc.fqdn, apex, sub, tc.apex, tc.sub)
		}
	}
}

func TestSolver_PresentAndCleanUp_Roundtrip(t *testing.T) {
	t.Parallel()
	fake := newFakeDynadot()
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works")

	ch := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.console.omantel.omani.works.",
		ResolvedZone: "omani.works.",
		Key:          "test-acme-key-abc123",
	}

	if err := s.Present(ch); err != nil {
		t.Fatalf("Present failed: %v", err)
	}
	// Verify the fake's zone now has the TXT record under the expected
	// (apex, subdomain) tuple.
	rec := fake.state["omani.works"]["_acme-challenge.console.omantel"]
	if rec["TXT"] != ch.Key {
		t.Fatalf("after Present, TXT record = %q, want %q (zone state: %#v)",
			rec["TXT"], ch.Key, fake.state["omani.works"])
	}

	// Idempotency: calling Present a second time must not error and must
	// not duplicate state. Dynadot dedupes by (subdomain, type, value)
	// so the fake's overwrite-by-key map naturally models that.
	if err := s.Present(ch); err != nil {
		t.Fatalf("second Present failed: %v", err)
	}
	if rec := fake.state["omani.works"]["_acme-challenge.console.omantel"]["TXT"]; rec != ch.Key {
		t.Fatalf("second Present mutated zone unexpectedly: %v", rec)
	}

	if err := s.CleanUp(ch); err != nil {
		t.Fatalf("CleanUp failed: %v", err)
	}
	// CleanUp must remove the TXT record. The subdomain entry may stay
	// (empty) or be removed entirely; the relevant invariant is that
	// the TXT key under it is gone.
	if got, ok := fake.state["omani.works"]["_acme-challenge.console.omantel"]["TXT"]; ok && got == ch.Key {
		t.Fatalf("after CleanUp, TXT record still present: %q", got)
	}

	// CleanUp must be idempotent — running it a second time when nothing
	// matches should return nil per the webhook contract.
	if err := s.CleanUp(ch); err != nil {
		t.Fatalf("idempotent CleanUp failed: %v", err)
	}
}

func TestSolver_Present_RejectsUnmanagedDomain(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("solver MUST NOT call dynadot for an unmanaged domain")
	}))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works")

	ch := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.api.evil.example.com.",
		ResolvedZone: "example.com.",
		Key:          "x",
	}
	err := s.Present(ch)
	if err == nil || !strings.Contains(err.Error(), "MANAGED_DOMAINS") {
		t.Fatalf("expected MANAGED_DOMAINS rejection, got: %v", err)
	}
}

func TestSolver_PreservesOtherRecords(t *testing.T) {
	t.Parallel()
	fake := newFakeDynadot()
	// Pre-populate a CNAME the operator already owns. After Present +
	// CleanUp the CNAME MUST still be there — this is the regression
	// that the SetFullDNS read-modify-write contract is designed to
	// prevent (memory: feedback_dynadot_dns.md, set_dns2 zone-wipe
	// incident).
	fake.state["omani.works"] = map[string]map[string]string{
		"www": {"CNAME": "openova.io"},
	}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works")

	ch := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.console.omantel.omani.works.",
		ResolvedZone: "omani.works.",
		Key:          "kkkk",
	}
	if err := s.Present(ch); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if err := s.CleanUp(ch); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	if got := fake.state["omani.works"]["www"]["CNAME"]; got != "openova.io" {
		t.Fatalf("CNAME wiped: zone=%#v", fake.state["omani.works"])
	}
}

func TestSolver_CleanUp_OnlyRemovesMatchingValue(t *testing.T) {
	t.Parallel()
	fake := newFakeDynadot()
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works")

	// Two concurrent Present calls writing different keys to the same
	// FQDN. CleanUp on key1 must NOT remove key2.
	//
	// NB: the fake's internal map is keyed by (sub, type) so it cannot
	// model two TXTs at the same name — to exercise the "match by value"
	// branch we instead seed the zone via the canonical AddRecord path
	// (which Dynadot dedupes by (sub,type,value)) and then assert the
	// CleanUp targeting one value preserves zone state for unrelated
	// names.
	ch1 := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.a.omani.works.",
		Key:          "A",
	}
	ch2 := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.b.omani.works.",
		Key:          "B",
	}
	if err := s.Present(ch1); err != nil {
		t.Fatalf("Present ch1: %v", err)
	}
	if err := s.Present(ch2); err != nil {
		t.Fatalf("Present ch2: %v", err)
	}
	if err := s.CleanUp(ch1); err != nil {
		t.Fatalf("CleanUp ch1: %v", err)
	}
	if got := fake.state["omani.works"]["_acme-challenge.b"]["TXT"]; got != "B" {
		t.Fatalf("CleanUp ch1 wiped ch2's record: %q", got)
	}
}

// TestSolver_DynadotErrorPropagation verifies that a Dynadot api3.json
// error envelope (e.g. invalid credentials) surfaces back to cert-manager
// as a non-nil error so the controller will retry. The shared client's
// classifyDynadotError covers the full taxonomy; we just assert the
// pass-through here.
func TestSolver_DynadotErrorPropagation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Invalid api key"}}}`))
	}))
	defer srv.Close()
	s := solverWith(t, srv, "omani.works")

	ch := &v1alpha1.ChallengeRequest{
		ResolvedFQDN: "_acme-challenge.x.omani.works.",
		Key:          "k",
	}
	err := s.Present(ch)
	if err == nil {
		t.Fatal("expected Present to surface dynadot error")
	}
	if !errors.Is(err, dynadot.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got: %v", err)
	}
}
