package dynadot

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// TestSetDNSRecords_HappyPath — verifies the URL params Dynadot's
// set_dns2 sees match the records we passed. Root-vs-subdomain split
// uses the main_record_* vs sub_record_* arrays per api3.json.
func TestSetDNSRecords_HappyPath(t *testing.T) {
	var captured url.Values
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		// canonical set_dns2 success envelope
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseCode":0,"Status":"success"}}`))
	})

	records := []registrar.DNSRecord{
		{Subhost: "", Type: "A", Value: "1.2.3.4", TTL: 3600},
		{Subhost: "www", Type: "CNAME", Value: "example.com.", TTL: 600},
		{Subhost: "", Type: "MX", Value: "mail.example.com", Priority: 10},
		{Subhost: "_acme-challenge", Type: "TXT", Value: "abc123def"},
	}
	if err := a.SetDNSRecords(context.Background(), "key:secret", "example.com", records); err != nil {
		t.Fatalf("SetDNSRecords: %v", err)
	}

	if got := captured.Get("command"); got != "set_dns2" {
		t.Errorf("command = %q, want set_dns2", got)
	}
	if got := captured.Get("domain"); got != "example.com" {
		t.Errorf("domain = %q, want example.com", got)
	}
	// Root A
	if got := captured.Get("main_record_type0"); got != "A" {
		t.Errorf("main_record_type0 = %q, want A", got)
	}
	if got := captured.Get("main_record0"); got != "1.2.3.4" {
		t.Errorf("main_record0 = %q, want 1.2.3.4", got)
	}
	if got := captured.Get("main_record_ttl0"); got != "3600" {
		t.Errorf("main_record_ttl0 = %q, want 3600", got)
	}
	// Root MX (after the A) — index 1 in main_record_*
	if got := captured.Get("main_record_type1"); got != "MX" {
		t.Errorf("main_record_type1 = %q, want MX", got)
	}
	if got := captured.Get("main_mx_priority1"); got != "10" {
		t.Errorf("main_mx_priority1 = %q, want 10", got)
	}
	// Subdomain CNAME — first sub_record_* slot (index 0)
	if got := captured.Get("sub_record0"); got != "www" {
		t.Errorf("sub_record0 = %q, want www", got)
	}
	if got := captured.Get("sub_record_type0"); got != "CNAME" {
		t.Errorf("sub_record_type0 = %q, want CNAME", got)
	}
	// Subdomain TXT — second sub_record_* slot (index 1)
	if got := captured.Get("sub_record1"); got != "_acme-challenge" {
		t.Errorf("sub_record1 = %q, want _acme-challenge", got)
	}
	if got := captured.Get("sub_record_value1"); got != "abc123def" {
		t.Errorf("sub_record_value1 = %q, want abc123def", got)
	}
}

// TestSetDNSRecords_DynadotError — error envelope maps to ErrInvalidToken.
func TestSetDNSRecords_DynadotError(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"SetDns2Response":{"ResponseCode":"-1","Status":"error","Error":"login failed"}}`))
	})
	err := a.SetDNSRecords(context.Background(), "key:secret", "example.com",
		[]registrar.DNSRecord{{Subhost: "", Type: "A", Value: "1.2.3.4"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// TestGetDNSRecords_HappyPath — domain_info response with both main +
// subdomain records gets parsed into a flat []DNSRecord. Subhost ""
// for main records, populated for subdomain records.
func TestGetDNSRecords_HappyPath(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("command"); got != "domain_info" {
			t.Errorf("command = %q, want domain_info", got)
		}
		_, _ = w.Write([]byte(`{
			"DomainInfoResponse": {
				"ResponseCode": 0,
				"Status": "success",
				"DomainInfoContent": {
					"NameServerSettings": {
						"MainDomains": [
							{"RecordType": "A", "Value": "1.2.3.4", "Ttl": 3600, "MxPriority": 0},
							{"RecordType": "MX", "Value": "mail.example.com", "Ttl": 3600, "MxPriority": 10}
						],
						"Subdomains": [
							{"Subhost": "www", "RecordType": "CNAME", "Value": "example.com.", "Ttl": 600, "MxPriority": 0},
							{"Subhost": "_acme-challenge", "RecordType": "TXT", "Value": "abc", "Ttl": 60, "MxPriority": 0}
						]
					}
				}
			}
		}`))
	})
	got, err := a.GetDNSRecords(context.Background(), "key:secret", "example.com")
	if err != nil {
		t.Fatalf("GetDNSRecords: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d records, want 4", len(got))
	}
	// First two are MainDomains → empty Subhost
	if got[0].Subhost != "" || got[0].Type != "A" || got[0].Value != "1.2.3.4" {
		t.Errorf("MainDomains[0] mismatch: %+v", got[0])
	}
	if got[1].Type != "MX" || got[1].Priority != 10 {
		t.Errorf("MainDomains[1] MX/priority mismatch: %+v", got[1])
	}
	// Next two are Subdomains → populated Subhost
	if got[2].Subhost != "www" || got[2].Type != "CNAME" {
		t.Errorf("Subdomains[0] mismatch: %+v", got[2])
	}
	if got[3].Subhost != "_acme-challenge" || got[3].Type != "TXT" {
		t.Errorf("Subdomains[1] mismatch: %+v", got[3])
	}
}

// TestGetDNSRecords_EmptyZone — a freshly-created domain with no
// records should return an empty (non-nil) slice + no error.
func TestGetDNSRecords_EmptyZone(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"DomainInfoResponse": {
				"ResponseCode": 0,
				"Status": "success",
				"DomainInfoContent": {
					"NameServerSettings": {
						"MainDomains": [],
						"Subdomains": []
					}
				}
			}
		}`))
	})
	got, err := a.GetDNSRecords(context.Background(), "key:secret", "example.com")
	if err != nil {
		t.Fatalf("GetDNSRecords: %v", err)
	}
	if got == nil {
		t.Fatalf("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 records, got %d", len(got))
	}
}
