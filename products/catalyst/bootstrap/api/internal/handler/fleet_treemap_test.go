// fleet_treemap_test.go — pure-function coverage for the
// fleet-treemap value projection helpers (TBD-E14).
//
// The HTTP handler itself depends on the same Deployment + dynamic-client
// plumbing as fleet_test.go and is exercised end-to-end by that fixture's
// /fleet/sovereigns happy path. Here we lock the math + token validation
// in pure-function tests so the wire-shape stays under-quality-control
// regardless of the surrounding fixture.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func TestFleetTreemapSizeValueAppsDefault(t *testing.T) {
	got := fleetTreemapSizeValue(fleetSovereignSummary{}, "apps")
	if got != 1 {
		t.Fatalf("apps default floor = %v, want 1", got)
	}
}

func TestFleetTreemapSizeValueAgeRecent(t *testing.T) {
	now := time.Now().UTC()
	s := fleetSovereignSummary{CreatedAt: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)}
	got := fleetTreemapSizeValue(s, "age")
	if got < 4 || got > 6 {
		t.Fatalf("age=%v days for 5d ago, want ~5", got)
	}
}

func TestFleetTreemapSizeValueAgeMissing(t *testing.T) {
	got := fleetTreemapSizeValue(fleetSovereignSummary{CreatedAt: ""}, "age")
	if got != 1 {
		t.Fatalf("age missing → floor 1, got %v", got)
	}
}

func TestFleetTreemapColorValueHealthMap(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		health string
		want   *float64
	}{
		{"green", float64Ptr(100)},
		{"yellow", float64Ptr(50)},
		{"red", float64Ptr(0)},
		{"unknown", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := fleetTreemapColorValue(fleetSovereignSummary{Health: c.health}, "health", now)
		if (got == nil) != (c.want == nil) {
			t.Fatalf("health=%q got=%v want=%v", c.health, got, c.want)
		}
		if got != nil && *got != *c.want {
			t.Fatalf("health=%q got=%v want=%v", c.health, *got, *c.want)
		}
	}
}

func TestFleetTreemapColorValueAgeClamps(t *testing.T) {
	// 0 days → 0% (allow tolerance for elapsed wall-clock between
	// CreatedAt parse and now in case the format strips sub-seconds).
	now := time.Now().UTC()
	s := fleetSovereignSummary{CreatedAt: now.Format(time.RFC3339)}
	got := fleetTreemapColorValue(s, "age", now)
	if got == nil || *got < 0 || *got > 1 {
		var v float64
		if got != nil {
			v = *got
		}
		t.Fatalf("age=0 want pct≈0, got=%v", v)
	}
	// AgeNormaliseDays * 10 days → clamped to 100%
	s.CreatedAt = now.Add(-time.Duration(AgeNormaliseDays*10) * 24 * time.Hour).Format(time.RFC3339)
	got = fleetTreemapColorValue(s, "age", now)
	if got == nil || *got != 100 {
		var v float64
		if got != nil {
			v = *got
		}
		t.Fatalf("very old Sov want pct=100, got=%v", v)
	}
}

func TestFleetTreemapDisplayName(t *testing.T) {
	if got := fleetTreemapDisplayName(fleetSovereignSummary{ID: "abc", FQDN: "foo.example"}); got != "foo.example" {
		t.Fatalf("FQDN preferred, got %q", got)
	}
	if got := fleetTreemapDisplayName(fleetSovereignSummary{ID: "abc"}); got != "abc" {
		t.Fatalf("ID fallback, got %q", got)
	}
}

func TestHandleFleetTreemapValidatesSizeBy(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap?size_by=carbohydrates", nil)
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
	// writeJSON enrichErrorBody adds a numeric status field, so the
	// body is heterogeneous — decode into map[string]any.
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if errStr, _ := body["error"].(string); !strings.Contains(errStr, "invalid-size-by") {
		t.Fatalf("error token = %q (body=%+v)", errStr, body)
	}
}

func TestHandleFleetTreemapValidatesColorBy(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap?color_by=mood", nil)
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestHandleFleetTreemapEmptyFleet(t *testing.T) {
	// No deployments registered → collectFleetSovereigns returns empty
	// → response is a well-shaped empty envelope (not a 500).
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap", nil)
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	var body fleetTreemapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalCount != 0 || len(body.Items) != 0 {
		t.Fatalf("expected empty envelope, got %+v", body)
	}
}

func float64Ptr(v float64) *float64 { return &v }

// TestFleetTreemapAuthorizedAllowsCatalystOwner — regression test for
// TBD-E14b / #1766. Wave 30 mothership treemap watch caught a 401 for
// callers whose JWT had `realm_access.roles=[catalyst-owner]` and an
// empty `tier` claim — the previous handler had no role-based fallback
// when the Tier claim was empty (PIN-flow JWTs don't populate it).
// This test locks in the role-based path so the regression cannot
// recur.
func TestFleetTreemapAuthorizedAllowsCatalystOwner(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{
			name:   "nil claims (sovereign-mode passthrough)",
			claims: nil,
			want:   true,
		},
		{
			name:   "owner tier",
			claims: &auth.Claims{Tier: "owner"},
			want:   true,
		},
		{
			name:   "admin tier",
			claims: &auth.Claims{Tier: "admin"},
			want:   true,
		},
		{
			name: "catalyst-owner realm role only (PIN-flow JWT shape)",
			claims: &auth.Claims{
				RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
			},
			want: true,
		},
		{
			name: "catalyst-admin realm role only",
			claims: &auth.Claims{
				RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}},
			},
			want: true,
		},
		{
			name: "mothership-admin realm role only",
			claims: &auth.Claims{
				RealmAccess: auth.RealmAccess{Roles: []string{"mothership-admin"}},
			},
			want: true,
		},
		{
			name: "viewer tier + no privileged role → denied",
			claims: &auth.Claims{
				Tier:        "viewer",
				RealmAccess: auth.RealmAccess{Roles: []string{"openova-user"}},
			},
			want: false,
		},
		{
			name: "developer tier + no privileged role → denied",
			claims: &auth.Claims{
				Tier:        "developer",
				RealmAccess: auth.RealmAccess{Roles: []string{"openova-user"}},
			},
			want: false,
		},
		{
			name:   "empty claims (no tier, no roles) → denied",
			claims: &auth.Claims{},
			want:   false,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := fleetTreemapAuthorized(c.claims); got != c.want {
				t.Fatalf("fleetTreemapAuthorized(%+v) = %v, want %v", c.claims, got, c.want)
			}
		})
	}
}

// TestHandleFleetTreemapCatalystOwnerReturns200 — end-to-end regression
// for the Wave 30 401. With a `catalyst-owner` realm role on the
// request claims, the handler MUST return 200 (well-shaped envelope),
// NOT 401/403.
func TestHandleFleetTreemapCatalystOwnerReturns200(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Email:       "owner@openova.io",
		Sub:         "test-owner-sub",
		RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
	})
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("catalyst-owner status=%d want 200 (body=%s)", w.Code, w.Body.String())
	}
	var body fleetTreemapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Empty fleet → well-shaped empty envelope (not nil items).
	if body.Items == nil {
		t.Fatalf("items must be non-nil even for empty fleet; got %+v", body)
	}
}

// TestHandleFleetTreemapUnprivilegedReturns403 — locks in that
// callers without Sovereign-owner / -admin tier OR an equivalent
// realm role are denied with 403 (not 200).
func TestHandleFleetTreemapUnprivilegedReturns403(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Email:       "viewer@openova.io",
		Sub:         "test-viewer-sub",
		Tier:        "viewer",
		RealmAccess: auth.RealmAccess{Roles: []string{"openova-user"}},
	})
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req.WithContext(ctx))
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d want 403 (body=%s)", w.Code, w.Body.String())
	}
}

/* ── #5613 — Organization layer ───────────────────────────────────── */

// TestFleetTreemapLayersParam — pure-function coverage for the
// `layers`/`group_by` alias parsing that gates the #5613 org projection.
func TestFleetTreemapLayersParam(t *testing.T) {
	cases := []struct {
		name             string
		layers, groupBy  string
		want             []string
	}{
		{"empty both", "", "", nil},
		{"layers only", "organization", "", []string{"organization"}},
		{"group_by alias", "", "organization", []string{"organization"}},
		{"layers wins over group_by", "organization", "sovereign", []string{"organization"}},
		{"multi-token trims", " organization , kind ", "", []string{"organization", "kind"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fleetTreemapLayers(c.layers, c.groupBy)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %+v want %+v", got, c.want)
				}
			}
		})
	}
}

// TestFleetOrgItemsForSovereign_SeparatesCustomerFromPlatform — pure-
// function proof of the #5613 attribution math: a customer-Org row and
// an infra-namespace row (no Org label) must land in two DIFFERENT
// buckets — the real Org and the synthetic platformOrg sentinel — using
// the EXACT SAME orgForRow resolver org_consumption.go's showback
// aggregation uses (not a re-implementation, not the billing path).
func TestFleetOrgItemsForSovereign_SeparatesCustomerFromPlatform(t *testing.T) {
	rows := []podRow{
		{namespace: "uatco", application: "wordpress", org: "uatco", cpuReq: 100, memReq: 256 << 20},
		{namespace: "kube-system", application: "coredns", org: "", cpuReq: 50, memReq: 64 << 20},
	}
	infra := infraNamespaceSet("")
	items := fleetOrgItemsForSovereign(rows, "hw292.omani.works", infra)

	if len(items) < 2 {
		t.Fatalf("expected >=2 items (customer Org + Platform overhead), got %d: %+v", len(items), items)
	}
	byName := map[string]fleetTreemapItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	uatco, ok := byName["uatco"]
	if !ok {
		t.Fatalf("expected a 'uatco' customer Org item; got %+v", items)
	}
	if uatco.Count != 1 {
		t.Errorf("uatco count = %d, want 1 (only the wordpress pod)", uatco.Count)
	}
	platform, ok := byName["Platform overhead"]
	if !ok {
		t.Fatalf("expected a 'Platform overhead' item; got %+v", items)
	}
	if platform.Count != 1 {
		t.Errorf("platform overhead count = %d, want 1 (only the coredns pod)", platform.Count)
	}
}

// TestHandleFleetTreemapOrganizationLayer — the falsifiable end-to-end
// proof for #5613. Builds a fake fleet with ONE Sovereign whose live
// estate holds:
//
//	(a) a customer namespace ("uatco") carrying the
//	    openova.io/organization label with a WordPress-shaped pod, and
//	(b) an infra namespace ("kube-system", in defaultInfraNamespaces)
//	    with no Org label, holding a coredns-shaped pod.
//
// Before the fix, HandleFleetTreemap ignored `layers`/`group_by`
// entirely and always returned ONE item — the Sovereign itself —
// regardless of the query string, exactly matching the issue's repro
// (`{"items":[{...1 Sovereign...}],"total_count":1}`). This test MUST
// FAIL against that pre-fix behaviour and PASS against the fix; see the
// session report for the before/after `go test` run proving
// falsifiability (temporarily reverting fleet_treemap.go's handler
// change).
func TestHandleFleetTreemapOrganizationLayer(t *testing.T) {
	h := newDashHandlerWithCache(t, "hw292dep", false,
		mkDashNamespaceOrg("uatco", "uatco"),
		mkDashNamespaceOrg("kube-system", ""), // infra namespace, no org label
		mkDashPodOnNode("uatco", "wordpress-1", "wordpress", ""),
		mkDashPodOnNode("kube-system", "coredns-1", "coredns", ""),
	)
	dep := &Deployment{
		ID:        "hw292dep",
		Status:    "ready",
		Request:   provisioner.Request{SovereignFQDN: "hw292.omani.works"},
		StartedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	}
	h.deployments.Store(dep.ID, dep)

	for _, qs := range []string{
		"layers=organization",
		"layers=organization,kind",
		"group_by=organization",
	} {
		t.Run(qs, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap?"+qs, nil)
			w := httptest.NewRecorder()
			h.HandleFleetTreemap(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d want 200 (body=%s)", w.Code, w.Body.String())
			}
			var body fleetTreemapResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			// THE #5613 assertion: NOT the pre-fix single-Sovereign
			// total_count:1 shape. Customer estate must separate from
			// platform overhead.
			if body.TotalCount < 2 {
				t.Fatalf("organization layer must separate customer estate from platform overhead: got total_count=%d items=%+v (this is the exact pre-fix #5613 repro shape: one Sovereign item, total_count:1)", body.TotalCount, body.Items)
			}
			names := map[string]bool{}
			for _, it := range body.Items {
				names[it.Name] = true
			}
			if !names["uatco"] {
				t.Errorf("expected a customer Org item named 'uatco'; got %+v", body.Items)
			}
			if !names["Platform overhead"] {
				t.Errorf("expected a 'Platform overhead' item; got %+v", body.Items)
			}
		})
	}
}

// TestHandleFleetTreemapDefaultLayerUnchanged — no `layers`/`group_by`
// param must keep the pre-#5613 single-row-per-Sovereign behaviour
// (backward compatibility for every existing caller).
func TestHandleFleetTreemapDefaultLayerUnchanged(t *testing.T) {
	h := newDashHandlerWithCache(t, "hw292dep", false,
		mkDashNamespaceOrg("uatco", "uatco"),
		mkDashPodOnNode("uatco", "wordpress-1", "wordpress", ""),
	)
	dep := &Deployment{
		ID:      "hw292dep",
		Status:  "ready",
		Request: provisioner.Request{SovereignFQDN: "hw292.omani.works"},
	}
	h.deployments.Store(dep.ID, dep)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/treemap", nil)
	w := httptest.NewRecorder()
	h.HandleFleetTreemap(w, req)
	var body fleetTreemapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalCount != 1 || len(body.Items) != 1 || body.Items[0].Name != "hw292.omani.works" {
		t.Fatalf("default (no layers param) must stay the single Sovereign row; got %+v", body)
	}
}
