package dynadot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// stubServer fabricates a Dynadot api3.json response. The handler is the
// caller's; this just isolates the BaseURL plumbing tests need.
func stubServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("k", "s")
	c.BaseURL = srv.URL
	return c, srv
}

func TestNew_PanicsOnEmptyCredentials(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty credentials")
		}
	}()
	_ = New("", "")
}

func TestAddRecord_AppendPath(t *testing.T) {
	t.Parallel()
	var captured url.Values
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	})

	err := c.AddRecord(context.Background(), "omani.works", Record{
		Subdomain: "_acme-challenge.x", Type: "TXT", Value: "k", TTL: 60,
	})
	if err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	// Critical: the append flag must be set, otherwise this is the
	// zone-wipe path and the safety contract in doc.go is violated.
	if captured.Get("add_dns_to_current_setting") != "yes" {
		t.Fatalf("AddRecord must set add_dns_to_current_setting=yes; got %v", captured)
	}
	if captured.Get("subdomain0") != "_acme-challenge.x" {
		t.Fatalf("subdomain0 = %q", captured.Get("subdomain0"))
	}
	if captured.Get("sub_record_type0") != "TXT" {
		t.Fatalf("sub_record_type0 = %q", captured.Get("sub_record_type0"))
	}
	if captured.Get("sub_record0") != "k" {
		t.Fatalf("sub_record0 = %q", captured.Get("sub_record0"))
	}
}

func TestAddRecord_ApexPath(t *testing.T) {
	t.Parallel()
	var captured url.Values
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	})

	err := c.AddRecord(context.Background(), "omani.works", Record{
		Subdomain: "@", Type: "A", Value: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("AddRecord apex: %v", err)
	}
	if captured.Get("main_record_type0") != "A" {
		t.Fatalf("apex path missed main_record_type0: %v", captured)
	}
	if captured.Get("main_record0") != "1.2.3.4" {
		t.Fatalf("main_record0 = %q", captured.Get("main_record0"))
	}
}

func TestRemoveSubRecord_PreservesOthers(t *testing.T) {
	t.Parallel()
	// First domain_info, then set_dns2 (full replace). The set_dns2 must
	// NOT carry the add_dns_to_current_setting flag (full-replace path),
	// AND must include the records that were NOT matched.
	var setQuery url.Values
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("command") {
		case "domain_info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"DomainInfoResponse": {
					"ResponseHeader": {"ResponseCode":"0","Status":"success"},
					"DomainInfo": {
						"NameServerSettings": {
							"NameServers": [{"ServerName":"ns1.openova.io"}],
							"MainDomains": [{"RecordType":"A","Value":"1.2.3.4","TTL":60}],
							"SubDomains": [
								{"Subhost":"www","RecordType":"CNAME","Value":"openova.io","TTL":60},
								{"Subhost":"_acme-challenge.x","RecordType":"TXT","Value":"OLD","TTL":60}
							]
						}
					}
				}
			}`))
		case "set_dns2":
			setQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
		default:
			t.Fatalf("unexpected command %q", r.URL.Query().Get("command"))
		}
	})

	err := c.RemoveSubRecord(context.Background(), "omani.works", Record{
		Subdomain: "_acme-challenge.x", Type: "TXT", Value: "OLD",
	})
	if err != nil {
		t.Fatalf("RemoveSubRecord: %v", err)
	}
	if setQuery == nil {
		t.Fatal("expected set_dns2 to be called; got nil")
	}
	if setQuery.Get("add_dns_to_current_setting") == "yes" {
		t.Fatal("RemoveSubRecord must use full-replace path; got append flag")
	}
	// Surviving CNAME must be present in the rewrite.
	found := false
	for k := range setQuery {
		if strings.HasPrefix(k, "subdomain") && setQuery.Get(k) == "www" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CNAME for 'www' missing from full-replace payload: %v", setQuery)
	}
	// Removed TXT must NOT be present.
	for k, v := range setQuery {
		if strings.HasPrefix(k, "sub_record") && len(v) > 0 && v[0] == "OLD" {
			t.Fatalf("RemoveSubRecord left the target TXT in place: %v", setQuery)
		}
	}
}

func TestRemoveSubRecord_NoMatchIsNoop(t *testing.T) {
	t.Parallel()
	calls := 0
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("command") {
		case "domain_info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"DomainInfoResponse": {
					"ResponseHeader": {"ResponseCode":"0","Status":"success"},
					"DomainInfo": {"NameServerSettings": {"SubDomains":[]}}
				}
			}`))
		case "set_dns2":
			t.Fatal("set_dns2 must NOT be called when no record matches")
		}
	})
	err := c.RemoveSubRecord(context.Background(), "omani.works", Record{
		Subdomain: "missing", Type: "TXT", Value: "x",
	})
	if err != nil {
		t.Fatalf("RemoveSubRecord (noop) returned %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one domain_info call, got %d", calls)
	}
}

func TestClassifyDynadotError_TaxonomyCovered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header   respHeader
		wantSent error
		wantNil  bool
	}{
		{respHeader{Status: "success"}, nil, true},
		{respHeader{Status: "error", Error: "Invalid api key"}, ErrInvalidToken, false},
		{respHeader{Status: "error", Error: "Domain not in your account"}, ErrDomainNotInAccount, false},
		{respHeader{Status: "error", Error: "rate limit exceeded"}, ErrRateLimited, false},
		{respHeader{Status: "error", Error: "garbage we have not seen"}, nil, false},
	}
	for _, tc := range cases {
		err := classifyDynadotError(tc.header)
		if tc.wantNil {
			if err != nil {
				t.Errorf("expected nil for %+v; got %v", tc.header, err)
			}
			continue
		}
		if tc.wantSent != nil && !errors.Is(err, tc.wantSent) {
			t.Errorf("classifyDynadotError(%+v) = %v; want errors.Is == %v", tc.header, err, tc.wantSent)
		}
	}
}

func TestManagedDomains_ParseAndLookup(t *testing.T) {
	t.Parallel()
	m := NewManagedDomains(" omani.works , Openova.IO\nomanyx.works\n ")
	if !m.Has("omani.works") || !m.Has("openova.io") || !m.Has("OMANYX.WORKS") {
		t.Fatalf("Has lookup case-insensitive failed: %v", m.List())
	}
	if m.Has("evil.example.com") {
		t.Fatal("Has must reject domain not in list")
	}
	got := m.List()
	want := []string{"omani.works", "omanyx.works", "openova.io"}
	if len(got) != len(want) {
		t.Fatalf("List len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}
