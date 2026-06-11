package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// stubPowerDNSClient is a minimal powerdnsZoneClient for republish-dns tests.
// PatchRRSets records every call and optionally returns an error.
type stubPowerDNSClient struct {
	// patches accumulates every (zone, rrsets) call in order.
	patches []stubPDNSPatch
	// errForZone maps zone name → error to return; absent zone → nil.
	errForZone map[string]error
}

type stubPDNSPatch struct {
	Zone   string
	RRSets []powerdns.RRSet
}

func (s *stubPowerDNSClient) CreateZone(_ context.Context, _ powerdns.ZoneSpec) error { return nil }
func (s *stubPowerDNSClient) ZoneExists(_ context.Context, _ string) (bool, error)    { return true, nil }
func (s *stubPowerDNSClient) PatchRRSets(_ context.Context, zone string, rrsets []powerdns.RRSet) error {
	s.patches = append(s.patches, stubPDNSPatch{Zone: zone, RRSets: rrsets})
	if s.errForZone != nil {
		if err, ok := s.errForZone[zone]; ok {
			return err
		}
	}
	return nil
}

// makeDepForRepublish builds a ready Deployment whose Result contains the
// given FQDN, LB IP, and (optionally) parent domains.
func makeDepForRepublish(id, fqdn, lbIP string, parentDomains []string) *Deployment {
	closedCh := make(chan provisioner.Event)
	closedDone := make(chan struct{})
	close(closedCh)
	close(closedDone)

	pds := make([]provisioner.ParentDomain, 0, len(parentDomains))
	for _, pd := range parentDomains {
		pds = append(pds, provisioner.ParentDomain{Name: pd})
	}

	return &Deployment{
		ID:     id,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: fqdn,
			ParentDomains: pds,
		},
		Result: &provisioner.Result{
			SovereignFQDN:  fqdn,
			LoadBalancerIP: lbIP,
		},
		eventsCh: closedCh,
		done:     closedDone,
	}
}

// doRepublish fires HandleRepublishDNS via httptest with chi routing and
// returns the response recorder.
func doRepublish(h *Handler, depID string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/deployments/"+depID+"/republish-dns", nil)

	r := chi.NewRouter()
	r.Post("/internal/deployments/{id}/republish-dns", h.HandleRepublishDNS)
	r.ServeHTTP(rr, req)
	return rr
}

// TestRepublishDNS_PublishesAllCanonicalSubdomains verifies that a successful
// call writes exactly len(CanonicalSovereignSubdomains)+1 rrsets (subdomains +
// apex) into the correct parent zone and returns them in the response body.
func TestRepublishDNS_PublishesAllCanonicalSubdomains(t *testing.T) {
	t.Parallel()

	const (
		depID = "c986326a77d391d4"
		fqdn  = "hw126.omantel.biz"
		lbIP  = "1.2.3.4"
	)

	stub := &stubPowerDNSClient{}
	h := &Handler{log: slog.Default()}
	h.deployments.Store(depID, makeDepForRepublish(depID, fqdn, lbIP, []string{"omantel.biz"}))
	h.powerdnsZoneClient = stub

	rr := doRepublish(h, depID)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — body: %s", rr.Code, rr.Body.String())
	}

	var resp dnsRepublishResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the response carries all 16 canonical subdomains.
	if resp.PublishedCount != len(CanonicalSovereignSubdomains) {
		t.Errorf("publishedCount: want %d, got %d", len(CanonicalSovereignSubdomains), resp.PublishedCount)
	}
	if resp.DeploymentID != depID {
		t.Errorf("deploymentId: want %q, got %q", depID, resp.DeploymentID)
	}
	if resp.LBIP != lbIP {
		t.Errorf("lbIP: want %q, got %q", lbIP, resp.LBIP)
	}
	if len(resp.ParentZones) != 1 || resp.ParentZones[0] != "omantel.biz" {
		t.Errorf("parentZones: want [omantel.biz], got %v", resp.ParentZones)
	}

	// Verify that PatchRRSets was called once (one parent zone) with
	// len(CanonicalSovereignSubdomains)+1 rrsets (subdomains + apex).
	if len(stub.patches) != 1 {
		t.Fatalf("PatchRRSets call count: want 1, got %d", len(stub.patches))
	}
	wantRRSetCount := len(CanonicalSovereignSubdomains) + 1 // +1 for apex
	if got := len(stub.patches[0].RRSets); got != wantRRSetCount {
		t.Errorf("rrset count: want %d (subdomains+apex), got %d", wantRRSetCount, got)
	}
	if stub.patches[0].Zone != "omantel.biz" {
		t.Errorf("zone patched: want %q, got %q", "omantel.biz", stub.patches[0].Zone)
	}

	// Verify that all CanonicalSovereignSubdomains appear in the published list.
	publishedSet := make(map[string]struct{}, len(resp.Published))
	for _, s := range resp.Published {
		publishedSet[s] = struct{}{}
	}
	for _, sub := range CanonicalSovereignSubdomains {
		if _, ok := publishedSet[sub]; !ok {
			t.Errorf("subdomain %q missing from published list", sub)
		}
	}
	// Spot-check the #3265 additions specifically.
	for _, required := range []string{"newapi", "pdns-admin"} {
		if _, ok := publishedSet[required]; !ok {
			t.Errorf("#3265 subdomain %q missing from published list", required)
		}
	}
}

// TestRepublishDNS_404FallbackToParentOfFQDN verifies the 404-retry path: when
// dep.Request.ParentDomains[0].Name equals the Sovereign FQDN itself (the
// back-compat synthesis Validate performs when SovereignPoolDomain is absent),
// the first PatchRRSets call targets the wrong "zone" (the FQDN itself, e.g.
// "t126.omani.works") and PowerDNS returns 404 because the authoritative zone
// is the tail-labels parent ("omani.works").  The handler retries against the
// derived parent and succeeds.
func TestRepublishDNS_404FallbackToParentOfFQDN(t *testing.T) {
	t.Parallel()

	const (
		depID = "abc123"
		fqdn  = "t126.omani.works"
		lbIP  = "5.6.7.8"
	)

	// Explicitly set ParentDomains to the FQDN itself (the back-compat synthesis
	// shape: dep.Request.ParentDomains[0].Name == fqdn).  The stub returns 404
	// when asked to PATCH the zone named after the FQDN, triggering the fallback
	// to "omani.works".
	stub := &stubPowerDNSClient{
		errForZone: map[string]error{
			fqdn: fmt.Errorf("powerdns returned status 404"),
		},
	}
	h := &Handler{log: slog.Default()}
	// ParentDomains = [fqdn] — the back-compat synthesis shape.
	h.deployments.Store(depID, makeDepForRepublish(depID, fqdn, lbIP, []string{fqdn}))
	h.powerdnsZoneClient = stub

	rr := doRepublish(h, depID)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — body: %s", rr.Code, rr.Body.String())
	}

	// Two PatchRRSets calls expected: first attempt on "t126.omani.works" (fails
	// 404), second on "omani.works" (succeeds).
	if len(stub.patches) != 2 {
		t.Fatalf("PatchRRSets call count: want 2 (original + fallback), got %d", len(stub.patches))
	}
	if stub.patches[0].Zone != fqdn {
		t.Errorf("first patch zone: want %q, got %q", fqdn, stub.patches[0].Zone)
	}
	derivedParent := "omani.works"
	if stub.patches[1].Zone != derivedParent {
		t.Errorf("fallback patch zone: want %q, got %q", derivedParent, stub.patches[1].Zone)
	}

	var resp dnsRepublishResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.ParentZones) != 1 || resp.ParentZones[0] != derivedParent {
		t.Errorf("parentZones in response: want [%q], got %v", derivedParent, resp.ParentZones)
	}
}

// TestRepublishDNS_404NotFound verifies that a missing deployment id returns 404.
func TestRepublishDNS_404NotFound(t *testing.T) {
	t.Parallel()
	h := &Handler{log: slog.Default()}
	rr := doRepublish(h, "nonexistent-id")
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

// TestRepublishDNS_NilResult verifies that a deployment whose Result is nil
// returns 422 (no Phase-0 result yet).
func TestRepublishDNS_NilResult(t *testing.T) {
	t.Parallel()

	closedCh := make(chan provisioner.Event)
	closedDone := make(chan struct{})
	close(closedCh)
	close(closedDone)

	dep := &Deployment{
		ID:       "dep-nil-result",
		Status:   "failed",
		eventsCh: closedCh,
		done:     closedDone,
	}
	h := &Handler{log: slog.Default()}
	h.deployments.Store("dep-nil-result", dep)
	h.powerdnsZoneClient = &stubPowerDNSClient{}

	rr := doRepublish(h, "dep-nil-result")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", rr.Code)
	}
}

// TestRepublishDNS_PowerDNSNotWired verifies that when powerdnsZoneClient is
// nil the handler returns 503.
func TestRepublishDNS_PowerDNSNotWired(t *testing.T) {
	t.Parallel()
	h := &Handler{log: slog.Default()}
	h.deployments.Store("dep-no-pdns",
		makeDepForRepublish("dep-no-pdns", "t99.omani.works", "1.1.1.1", []string{"omani.works"}),
	)
	// h.powerdnsZoneClient intentionally left nil.

	rr := doRepublish(h, "dep-no-pdns")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
}

// TestRepublishDNS_PatchError verifies that a PatchRRSets failure returns 502.
func TestRepublishDNS_PatchError(t *testing.T) {
	t.Parallel()

	stub := &stubPowerDNSClient{
		errForZone: map[string]error{
			"omani.works": fmt.Errorf("powerdns returned status 500"),
		},
	}
	h := &Handler{log: slog.Default()}
	h.deployments.Store("dep-patch-err",
		makeDepForRepublish("dep-patch-err", "t100.omani.works", "2.2.2.2", []string{"omani.works"}),
	)
	h.powerdnsZoneClient = stub

	rr := doRepublish(h, "dep-patch-err")
	if rr.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "partial-failure") {
		t.Errorf("want 'partial-failure' in response body, got: %s", body)
	}
}

// TestRepublishDNS_ReconstructsInputsFromPersisted is the table test the spec
// asks for: verifies that the handler correctly reconstructs sovereignFQDN,
// lbIP, and parent zones from the persisted Deployment record for each shape.
func TestRepublishDNS_ReconstructsInputsFromPersisted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		fqdn          string
		lbIP          string
		parentDomains []string // what's in dep.Request.ParentDomains
		wantZone      string   // the zone PatchRRSets should be called against
		wantLBIP      string   // the IP all A records should point to
		wantSubCount  int      // expected number of subdomains in the A records
	}{
		{
			name:          "explicit-parent-domain",
			fqdn:          "t111.omani.works",
			lbIP:          "77.42.11.95",
			parentDomains: []string{"omani.works"},
			wantZone:      "omani.works",
			wantLBIP:      "77.42.11.95",
			wantSubCount:  len(CanonicalSovereignSubdomains),
		},
		{
			name:          "no-parent-domains-derived-from-fqdn",
			fqdn:          "t112.omani.works",
			lbIP:          "88.53.22.96",
			parentDomains: nil, // empty → backstop derives from FQDN
			wantZone:      "omani.works",
			wantLBIP:      "88.53.22.96",
			wantSubCount:  len(CanonicalSovereignSubdomains),
		},
		{
			name:          "multi-parent-domains",
			fqdn:          "t113.omani.works",
			lbIP:          "99.64.33.97",
			parentDomains: []string{"omani.works", "omantel.biz"},
			wantZone:      "omani.works", // first zone; test checks both below
			wantLBIP:      "99.64.33.97",
			wantSubCount:  len(CanonicalSovereignSubdomains),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubPowerDNSClient{}
			h := &Handler{log: slog.Default()}
			depID := "dep-" + tc.name
			h.deployments.Store(depID, makeDepForRepublish(depID, tc.fqdn, tc.lbIP, tc.parentDomains))
			h.powerdnsZoneClient = stub

			rr := doRepublish(h, depID)
			if rr.Code != http.StatusOK {
				t.Fatalf("want 200, got %d — body: %s", rr.Code, rr.Body.String())
			}

			// Verify response body fields.
			var resp dnsRepublishResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.LBIP != tc.wantLBIP {
				t.Errorf("lbIP: want %q, got %q", tc.wantLBIP, resp.LBIP)
			}
			if resp.PublishedCount != tc.wantSubCount {
				t.Errorf("publishedCount: want %d, got %d", tc.wantSubCount, resp.PublishedCount)
			}

			// For multi-parent case ensure both zones are patched.
			if len(tc.parentDomains) == 2 {
				if len(stub.patches) != 2 {
					t.Fatalf("multi-parent: want 2 PatchRRSets calls, got %d", len(stub.patches))
				}
				zones := map[string]bool{stub.patches[0].Zone: true, stub.patches[1].Zone: true}
				for _, pd := range tc.parentDomains {
					if !zones[pd] {
						t.Errorf("parent zone %q not patched; got zones: %v", pd, stub.patches)
					}
				}
			}

			// Verify each patch uses the correct LB IP for all A records.
			for _, patch := range stub.patches {
				for _, rr := range patch.RRSets {
					if rr.Type == "A" {
						if len(rr.Records) != 1 || rr.Records[0].Content != tc.wantLBIP {
							t.Errorf("rrset %q: want IP %q, got %v", rr.Name, tc.wantLBIP, rr.Records)
						}
					}
				}
			}
		})
	}
}
