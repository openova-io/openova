// dashboard_test.go — coverage for the Sovereign Dashboard treemap
// endpoint. The handler emits placeholder data (see dashboard.go header
// for the metrics-server upgrade plan); these tests pin the HTTP shape
// the UI consumes so a future refactor of the data path can't silently
// break the wire contract.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardTreemap_DefaultsAndShape(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/treemap", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out treemapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) == 0 {
		t.Fatalf("expected non-empty items[]")
	}
	if out.TotalCount <= 0 {
		t.Fatalf("expected total_count > 0, got %d", out.TotalCount)
	}
	// Single-layer call → flat list (no children populated).
	for _, it := range out.Items {
		if len(it.Children) != 0 {
			t.Fatalf("single-layer call returned a parent with children: %+v", it)
		}
	}
}

func TestDashboardTreemap_NestedTwoLayers(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?group_by=family,application&color_by=utilization&size_by=cpu_limit",
		nil,
	)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out treemapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) == 0 {
		t.Fatalf("expected at least one parent group")
	}
	parentsWithChildren := 0
	for _, p := range out.Items {
		if len(p.Children) > 0 {
			parentsWithChildren++
		}
	}
	if parentsWithChildren == 0 {
		t.Fatalf("expected at least one parent with children, got 0")
	}
}

func TestDashboardTreemap_RejectsUnknownDimension(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?group_by=widget", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid-group-by") {
		t.Fatalf("expected invalid-group-by error: %s", rec.Body.String())
	}
}

func TestDashboardTreemap_RejectsUnknownColorBy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?color_by=mood", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardTreemap_RejectsUnknownSizeBy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?size_by=carbohydrates", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardTreemap_PercentageInRange(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?group_by=family,application", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	var out treemapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range out.Items {
		if p.Percentage < 0 || p.Percentage > 100 {
			t.Fatalf("parent %s percentage out of range: %f", p.Name, p.Percentage)
		}
		for _, c := range p.Children {
			if c.Percentage < 0 || c.Percentage > 100 {
				t.Fatalf("child %s percentage out of range: %f", c.Name, c.Percentage)
			}
		}
	}
}

