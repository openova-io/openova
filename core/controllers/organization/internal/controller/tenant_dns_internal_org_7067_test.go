package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"

	"github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

func TestTenantDNSParent(t *testing.T) {
	cases := []struct {
		name       string
		org        orgapi.Organization
		wantParent string
		wantZones  []string
	}{
		{"customer pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "customer", SovereignRef: "hw307.omani.works", TenantPublic: orgapi.OrganizationTenantPublic{ParentDomain: "omani.rest"}}}, "omani.rest", []string{"omani.rest"}},
		{"internal, no pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "internal", SovereignRef: "hw307.omani.works"}}, "hw307.omani.works", []string{"hw307.omani.works", "omani.works"}},
		{"customer, no pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "customer", SovereignRef: "hw307.omani.works"}}, "", nil},
		{"internal, bad sovereign", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "internal", SovereignRef: "localhost"}}, "", nil},
	}
	for _, c := range cases {
		p, z := tenantDNSParent(&c.org)
		if p != c.wantParent || !reflect.DeepEqual(z, c.wantZones) {
			t.Errorf("%s: parent=%q zones=%q want %q %q", c.name, p, z, c.wantParent, c.wantZones)
		}
	}
}

// zoneServer is a PowerDNS stub that holds the zones in held and records every
// request: a GET of a zone answers 200 when held and 404 otherwise (the answer
// of the central PowerDNS, measured 2026-11-15 on 5.0.3), a PATCH answers 204.
type zoneServer struct {
	held map[string]bool

	mu       sync.Mutex
	gets     []string
	patches  []string
	patchSet map[string][]string // zone -> rrset names of the PATCH
}

func (s *zoneServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	s.patchSet = map[string][]string{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zone := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/localhost/zones/")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			s.gets = append(s.gets, zone)
			if s.held[zone] {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"name":"` + zone + `"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not Found"))
		case http.MethodPatch:
			s.patches = append(s.patches, zone)
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				RRSets []struct {
					Name string `json:"name"`
				} `json:"rrsets"`
			}
			_ = json.Unmarshal(raw, &body)
			for _, rs := range body.RRSets {
				s.patchSet[zone] = append(s.patchSet[zone], rs.Name)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
}

func internalOrg() *orgapi.Organization {
	o := &orgapi.Organization{}
	o.Name = "hw307-omani-works"
	o.Spec.Slug = "hw307-omani-works"
	o.Spec.Kind = "internal"
	o.Spec.SovereignRef = "hw307.omani.works"
	return o
}

// A Sovereign whose name was allocated from a pool has a zone of its own. The
// Organization's records must go into it: written into the parent, below the
// delegation, they are never served (live hw307 2026-11-15, J1.6).
func TestReconcileTenantDNS_InternalOrgWritesIntoTheSovereignZone(t *testing.T) {
	s := &zoneServer{held: map[string]bool{"hw307.omani.works.": true, "omani.works.": true}}
	srv := s.start(t)
	defer srv.Close()
	r := &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: srv.URL, PoolPowerDNSAPIKey: "k", TenantConsoleLBIPv4: "5.37.79.3"}

	if _, err := r.reconcileTenantDNS(context.Background(), internalOrg()); err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if want := []string{"hw307.omani.works."}; !reflect.DeepEqual(s.patches, want) {
		t.Fatalf("PATCHed zones %v, want %v", s.patches, want)
	}
	if want := []string{"console.hw307-omani-works.hw307.omani.works.", "*.hw307-omani-works.hw307.omani.works."}; !reflect.DeepEqual(s.patchSet["hw307.omani.works."], want) {
		t.Fatalf("rrsets %v, want %v", s.patchSet["hw307.omani.works."], want)
	}

	if _, err := r.teardownTenantDNS(context.Background(), internalOrg()); err != nil {
		t.Fatalf("teardownTenantDNS: %v", err)
	}
	if want := []string{"hw307.omani.works.", "hw307.omani.works."}; !reflect.DeepEqual(s.patches, want) {
		t.Fatalf("teardown PATCHed zones %v, want %v", s.patches, want)
	}
}

// A Sovereign name given any other way has no zone of its own: the records go
// into the parent, as before (live hw307 2026-11-11).
func TestReconcileTenantDNS_InternalOrgFallsBackToTheParentZone(t *testing.T) {
	s := &zoneServer{held: map[string]bool{"omani.works.": true}}
	srv := s.start(t)
	defer srv.Close()
	r := &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: srv.URL, PoolPowerDNSAPIKey: "k", TenantConsoleLBIPv4: "5.37.79.3"}

	if _, err := r.reconcileTenantDNS(context.Background(), internalOrg()); err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if want := []string{"hw307.omani.works."}; !reflect.DeepEqual(s.gets, want) {
		t.Fatalf("GET zones %v, want %v (the parent is the last choice and is not asked)", s.gets, want)
	}
	if want := []string{"omani.works."}; !reflect.DeepEqual(s.patches, want) {
		t.Fatalf("PATCHed zones %v, want %v", s.patches, want)
	}
}

// A customer Organization has one zone, its pool parent: no GET is made.
func TestReconcileTenantDNS_CustomerOrgAsksNoZone(t *testing.T) {
	s := &zoneServer{held: map[string]bool{"omani.rest.": true}}
	srv := s.start(t)
	defer srv.Close()
	r := &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: srv.URL, PoolPowerDNSAPIKey: "k", TenantConsoleLBIPv4: "5.37.79.3"}

	if _, err := r.reconcileTenantDNS(context.Background(), orgWithTenantPublic("omani.rest", "acme")); err != nil {
		t.Fatalf("reconcileTenantDNS: %v", err)
	}
	if len(s.gets) != 0 {
		t.Fatalf("GET zones %v, want none", s.gets)
	}
	if want := []string{"omani.rest."}; !reflect.DeepEqual(s.patches, want) {
		t.Fatalf("PATCHed zones %v, want %v", s.patches, want)
	}
}

// Any answer but 200 or 404 is an error: nothing is written, the caller
// requeues.
func TestReconcileTenantDNS_ZoneLookupErrorWritesNothing(t *testing.T) {
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := &Reconciler{Log: logr.Discard(), PoolPowerDNSURL: srv.URL, PoolPowerDNSAPIKey: "k", TenantConsoleLBIPv4: "5.37.79.3"}

	if _, err := r.reconcileTenantDNS(context.Background(), internalOrg()); err == nil {
		t.Fatal("reconcileTenantDNS: want an error when the zone lookup answers 500")
	}
	if patched {
		t.Fatal("a PATCH was sent although the zone lookup failed")
	}
}
