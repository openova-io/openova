package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// fakeDNSAdapter is the fakeAdapter + DNSRegistrar conformance.
type fakeDNSAdapter struct {
	fakeAdapter
	dnsErr     error
	gotRecords []registrar.DNSRecord
	storedSet  []registrar.DNSRecord
}

func (f *fakeDNSAdapter) SetDNSRecords(ctx context.Context, token, domain string, records []registrar.DNSRecord) error {
	f.gotToken = token
	f.gotDomain = domain
	f.gotRecords = records
	if f.dnsErr != nil {
		return f.dnsErr
	}
	f.storedSet = records
	return nil
}

func (f *fakeDNSAdapter) GetDNSRecords(ctx context.Context, token, domain string) ([]registrar.DNSRecord, error) {
	return f.storedSet, nil
}

// Compile-time conformance check.
var _ registrar.DNSRegistrar = (*fakeDNSAdapter)(nil)

func doSetDNS(t *testing.T, h *Handler, registrarName string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/registrar/{registrar}", func(r chi.Router) {
			r.Post("/set-dns", h.SetDNS)
		})
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/registrar/"+registrarName+"/set-dns", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// newDNSTestHandler wires a fakeDNSAdapter into a Handler.
func newDNSTestHandler(t *testing.T, adapter *fakeDNSAdapter) *Handler {
	t.Helper()
	log, _ := captureLogger()
	h := &Handler{Log: log}
	h.SetRegistry(registrar.Registry{adapter.name: adapter})
	return h
}

func TestSetDNS_HappyPath(t *testing.T) {
	a := &fakeDNSAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newDNSTestHandler(t, a)

	body := `{
		"domain": "example.com",
		"token": "` + supersecretToken + `",
		"records": [
			{"subhost": "", "type": "A", "value": "1.2.3.4", "ttl": 3600},
			{"subhost": "www", "type": "CNAME", "value": "example.com."},
			{"subhost": "", "type": "MX", "value": "mail.example.com", "priority": 10},
			{"subhost": "_acme-challenge", "type": "TXT", "value": "abc123"}
		]
	}`
	rec := doSetDNS(t, h, "dynadot", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(a.gotRecords) != 4 {
		t.Errorf("adapter saw %d records, want 4", len(a.gotRecords))
	}
	if a.gotDomain != "example.com" {
		t.Errorf("adapter saw domain %q, want example.com", a.gotDomain)
	}
}

func TestSetDNS_UnsupportedRegistrar_501(t *testing.T) {
	// fakeAdapter (without DNSRegistrar mixin) → 501 Not Implemented.
	a := &fakeAdapter{name: "godaddy"}
	log, _ := captureLogger()
	h := &Handler{Log: log}
	h.SetRegistry(registrar.Registry{a.name: a})

	body := `{"domain":"example.com","token":"x","records":[{"type":"A","value":"1.2.3.4"}]}`
	rec := doSetDNS(t, h, "godaddy", body)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want 501", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "set-dns-not-supported") {
		t.Errorf("body missing error key, got %s", rec.Body.String())
	}
}

func TestSetDNS_UnknownRegistrar_404(t *testing.T) {
	a := &fakeDNSAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newDNSTestHandler(t, a)
	body := `{"domain":"example.com","token":"x","records":[{"type":"A","value":"1.2.3.4"}]}`
	rec := doSetDNS(t, h, "neverheard", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSetDNS_BadRecordType_422(t *testing.T) {
	a := &fakeDNSAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newDNSTestHandler(t, a)
	body := `{"domain":"example.com","token":"x","records":[{"type":"SRV","value":"x"}]}`
	rec := doSetDNS(t, h, "dynadot", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid-records") {
		t.Errorf("body missing error key, got %s", rec.Body.String())
	}
}

func TestSetDNS_EmptyRecords_422(t *testing.T) {
	a := &fakeDNSAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newDNSTestHandler(t, a)
	body := `{"domain":"example.com","token":"x","records":[]}`
	rec := doSetDNS(t, h, "dynadot", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestSetDNS_MissingDomain_422(t *testing.T) {
	a := &fakeDNSAdapter{fakeAdapter: fakeAdapter{name: "dynadot"}}
	h := newDNSTestHandler(t, a)
	body := `{"domain":"","token":"x","records":[{"type":"A","value":"1.2.3.4"}]}`
	rec := doSetDNS(t, h, "dynadot", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}
