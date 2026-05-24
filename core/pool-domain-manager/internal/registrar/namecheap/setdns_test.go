package namecheap

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// TestSetDNSRecords_HappyPath — verifies the setHosts URL params
// Namecheap's API sees match the records we passed.
func TestSetDNSRecords_HappyPath(t *testing.T) {
	var captured url.Values
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		// canonical setHosts success envelope
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
<Errors/>
<CommandResponse Type="namecheap.domains.dns.setHosts">
<DomainDNSSetHostsResult Domain="example.com" IsSuccess="true"/>
</CommandResponse>
</ApiResponse>`))
	})

	records := []registrar.DNSRecord{
		{Subhost: "", Type: "A", Value: "1.2.3.4", TTL: 3600},
		{Subhost: "www", Type: "CNAME", Value: "example.com."},
		{Subhost: "", Type: "MX", Value: "mail.example.com", Priority: 10},
		{Subhost: "_acme-challenge", Type: "TXT", Value: "abc123"},
	}
	err := a.SetDNSRecords(context.Background(),
		"user:apikey:user:127.0.0.1", "example.com", records)
	if err != nil {
		t.Fatalf("SetDNSRecords: %v", err)
	}

	if got := captured.Get("Command"); got != "namecheap.domains.dns.setHosts" {
		t.Errorf("Command = %q, want setHosts", got)
	}
	if got := captured.Get("SLD"); got != "example" {
		t.Errorf("SLD = %q, want example", got)
	}
	if got := captured.Get("TLD"); got != "com" {
		t.Errorf("TLD = %q, want com", got)
	}
	// 1-indexed per Namecheap convention
	if got := captured.Get("HostName1"); got != "@" {
		t.Errorf("HostName1 = %q, want @", got)
	}
	if got := captured.Get("RecordType1"); got != "A" {
		t.Errorf("RecordType1 = %q, want A", got)
	}
	if got := captured.Get("Address1"); got != "1.2.3.4" {
		t.Errorf("Address1 = %q, want 1.2.3.4", got)
	}
	if got := captured.Get("HostName2"); got != "www" {
		t.Errorf("HostName2 = %q, want www", got)
	}
	if got := captured.Get("RecordType3"); got != "MX" {
		t.Errorf("RecordType3 = %q, want MX", got)
	}
	if got := captured.Get("MXPref3"); got != "10" {
		t.Errorf("MXPref3 = %q, want 10", got)
	}
	if got := captured.Get("HostName4"); got != "_acme-challenge" {
		t.Errorf("HostName4 = %q, want _acme-challenge", got)
	}
}

// TestGetDNSRecords_HappyPath — getHosts XML round-trips into a flat
// []DNSRecord.
func TestGetDNSRecords_HappyPath(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("Command"); got != "namecheap.domains.dns.getHosts" {
			t.Errorf("Command = %q, want getHosts", got)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK">
<Errors/>
<CommandResponse Type="namecheap.domains.dns.getHosts">
<DomainDNSGetHostsResult Domain="example.com">
<host HostId="1" Name="@" Type="A" Address="1.2.3.4" TTL="3600" MXPref="10"/>
<host HostId="2" Name="www" Type="CNAME" Address="example.com." TTL="600"/>
<host HostId="3" Name="@" Type="MX" Address="mail.example.com" TTL="3600" MXPref="10"/>
<host HostId="4" Name="_acme-challenge" Type="TXT" Address="abc" TTL="60"/>
</DomainDNSGetHostsResult>
</CommandResponse>
</ApiResponse>`))
	})
	got, err := a.GetDNSRecords(context.Background(),
		"user:apikey:user:127.0.0.1", "example.com")
	if err != nil {
		t.Fatalf("GetDNSRecords: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d records, want 4", len(got))
	}
	// '@' maps to empty Subhost
	if got[0].Subhost != "" {
		t.Errorf("got[0].Subhost = %q, want empty", got[0].Subhost)
	}
	if got[0].Type != "A" || got[0].Value != "1.2.3.4" {
		t.Errorf("got[0] mismatch: %+v", got[0])
	}
	if got[1].Subhost != "www" || got[1].Type != "CNAME" {
		t.Errorf("got[1] mismatch: %+v", got[1])
	}
	if got[2].Type != "MX" || got[2].Priority != 10 {
		t.Errorf("got[2] MX mismatch: %+v", got[2])
	}
}

// TestSetDNSRecords_NamecheapError — error envelope maps to non-nil error.
func TestSetDNSRecords_NamecheapError(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="ERROR">
<Errors><Error Number="1011102">API Key is invalid</Error></Errors>
</ApiResponse>`))
	})
	err := a.SetDNSRecords(context.Background(),
		"user:apikey:user:127.0.0.1", "example.com",
		[]registrar.DNSRecord{{Subhost: "", Type: "A", Value: "1.2.3.4"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
