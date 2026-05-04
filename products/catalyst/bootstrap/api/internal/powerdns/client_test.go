package powerdns_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
)

// TestCreateZone_Created — happy path: PowerDNS returns 201 Created, the
// client returns nil error, and the request body looks the way the
// PowerDNS API contract expects (trailing-dot zone name, sane defaults
// applied).
func TestCreateZone_Created(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/servers/localhost/zones" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("missing/wrong X-API-Key header: %q", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing/wrong Content-Type: %q", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "omani.works."}`))
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{
		Name:   "omani.works",
		DNSSEC: true,
	})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}

	// Trailing dot must be added when caller omits it.
	if got := gotBody["name"]; got != "omani.works." {
		t.Errorf("expected name to have trailing dot; got %q", got)
	}
	// Default kind = Native when caller omits it.
	if got := gotBody["kind"]; got != "Native" {
		t.Errorf("expected kind Native; got %q", got)
	}
	// Default nameservers = ns1/2/3.<name>.
	ns, ok := gotBody["nameservers"].([]any)
	if !ok || len(ns) != 3 {
		t.Fatalf("expected 3 nameservers; got %v", gotBody["nameservers"])
	}
	if ns[0] != "ns1.omani.works." {
		t.Errorf("expected ns1.omani.works.; got %q", ns[0])
	}
	// DNSSEC pass-through.
	if got := gotBody["dnssec"]; got != true {
		t.Errorf("expected dnssec=true; got %v", got)
	}
}

// TestCreateZone_409Conflict — idempotency: a zone that already exists
// returns ErrZoneAlreadyExists, NOT a generic error. Callers rely on
// errors.Is(err, ErrZoneAlreadyExists) to detect the idempotent case.
func TestCreateZone_409Conflict(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error": "Zone omani.works. already exists"}`))
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if !errors.Is(err, powerdns.ErrZoneAlreadyExists) {
		t.Fatalf("expected ErrZoneAlreadyExists; got %v", err)
	}
}

// TestCreateZone_412PreconditionFailed — the gpgsql-backend bootstrap
// window where PowerDNS returns 412 instead of 409. Same idempotent
// recovery.
func TestCreateZone_412PreconditionFailed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"error": "DNSSEC mid-bootstrap"}`))
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if !errors.Is(err, powerdns.ErrZoneAlreadyExists) {
		t.Fatalf("expected ErrZoneAlreadyExists for 412; got %v", err)
	}
}

// TestCreateZone_5xx — PowerDNS unhappy: surface the error verbatim so
// the caller can distinguish "your input is wrong" from "powerdns is
// down."
func TestCreateZone_5xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "backend unavailable"}`))
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if err == nil {
		t.Fatal("expected error for 500; got nil")
	}
	if errors.Is(err, powerdns.ErrZoneAlreadyExists) {
		t.Fatalf("5xx must NOT collapse to ErrZoneAlreadyExists; got %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error must surface status code; got %v", err)
	}
}

// TestCreateZone_NoBaseURL — input validation: empty BaseURL is a
// programmer error; we fail fast rather than emit a request to "".
func TestCreateZone_NoBaseURL(t *testing.T) {
	t.Parallel()

	c := powerdns.New("", "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if err == nil || !strings.Contains(err.Error(), "BaseURL") {
		t.Fatalf("expected BaseURL error; got %v", err)
	}
}

// TestCreateZone_NoAPIKey — input validation: empty APIKey is a
// programmer error; we fail fast.
func TestCreateZone_NoAPIKey(t *testing.T) {
	t.Parallel()

	c := powerdns.New("http://example", "", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Fatalf("expected APIKey error; got %v", err)
	}
}

// TestCreateZone_NoName — input validation.
func TestCreateZone_NoName(t *testing.T) {
	t.Parallel()

	c := powerdns.New("http://example", "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{})
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name error; got %v", err)
	}
}

// TestCreateZone_CustomNameservers — operator-supplied NS list flows
// through verbatim; default-injection only triggers on empty input.
func TestCreateZone_CustomNameservers(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{
		Name:        "omani.trade",
		Nameservers: []string{"a.iana-servers.net.", "b.iana-servers.net."},
	})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	ns, _ := gotBody["nameservers"].([]any)
	if len(ns) != 2 {
		t.Fatalf("expected 2 nameservers; got %v", gotBody["nameservers"])
	}
	if ns[0] != "a.iana-servers.net." {
		t.Errorf("expected a.iana-servers.net.; got %q", ns[0])
	}
}

// TestCreateZone_CustomServerID — multi-tenant PowerDNS deployments
// override the default "localhost" server ID.
func TestCreateZone_CustomServerID(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "tenant-omantel")
	err := c.CreateZone(context.Background(), powerdns.ZoneSpec{Name: "omani.works"})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	if !strings.Contains(gotPath, "/servers/tenant-omantel/zones") {
		t.Errorf("expected serverID in path; got %q", gotPath)
	}
}

// TestZoneExists_200 — a zone that exists.
func TestZoneExists_200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/zones/omani.works.") {
			t.Errorf("expected trailing-dot path; got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id": "omani.works."}`))
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	exists, err := c.ZoneExists(context.Background(), "omani.works")
	if err != nil {
		t.Fatalf("ZoneExists: %v", err)
	}
	if !exists {
		t.Error("expected exists=true for 200")
	}
}

// TestZoneExists_404 — a zone that doesn't exist returns false, NIL —
// not an error. Callers use this for pre-flight UX hints.
func TestZoneExists_404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := powerdns.New(srv.URL, "test-key", "")
	exists, err := c.ZoneExists(context.Background(), "omani.works")
	if err != nil {
		t.Fatalf("ZoneExists: %v", err)
	}
	if exists {
		t.Error("expected exists=false for 404")
	}
}
