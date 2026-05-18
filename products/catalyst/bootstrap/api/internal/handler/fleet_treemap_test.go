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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
