package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/core/services/billing/packages"
)

// ladderBSS serves a 0.1.61-shaped document: the four packages at the
// workbook's prices and a dr_topology level feature — single region on S/M/L,
// active-passive on XL. The topology is the package's, never a surcharge.
func ladderBSS(t *testing.T) *httptest.Server {
	t.Helper()
	const doc = `{"currency":"OMR","price_book":"OpenOva plans","prices_as_of":"2026-10-10",
  "groups":[{"key":"capacity","name":"Capacity"},{"key":"resilience","name":"Resilience"}],
  "floor":[],
  "packages":[
    {"sku":"plan.s","name":"S","price_month":"2.490"},
    {"sku":"plan.m","name":"M","price_month":"4.490"},
    {"sku":"plan.l","name":"L","price_month":"7.990"},
    {"sku":"plan.xl","name":"XL","price_month":"13.990"}],
  "features":[
    {"key":"backup","name":"Backup","group":"features","kind":"boolean","cells":{
      "plan.s":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.xl":{"state":"included"}}},
    {"key":"dr_topology","name":"DR topology","group":"resilience","kind":"level","levels":["single region","active-passive"],"cells":{
      "plan.s":{"state":"included","level":0,"included_from":"plan.xl"},
      "plan.m":{"state":"included","level":0,"included_from":"plan.xl"},
      "plan.l":{"state":"included","level":0,"included_from":"plan.xl"},
      "plan.xl":{"state":"included","level":1}}}
  ]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
}

// XL carries active-passive: the topology costs nothing beside the package.
func TestPriceOrder_ActivePassiveOnXL_IsIncludedNotSurcharged(t *testing.T) {
	bss := ladderBSS(t)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}
	p, err := h.priceOrder(context.Background(), pricingRequest{PlanID: "xl", PackageSKU: "plan.xl", Topology: topologyActiveHotStandby})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	if p.TopologyBaisa != 0 {
		t.Fatalf("topology on XL must be included (0 baisa), got %d", p.TopologyBaisa)
	}
	if p.TotalBaisa != 13990 {
		t.Fatalf("total must be the package alone (13990), got %d", p.TotalBaisa)
	}
}

// S does not carry active-passive: refused like an add-on the package does
// not offer — the storefront locks the choice, billing is the authority.
func TestPriceOrder_ActivePassiveOnS_IsRefusedNotSurcharged(t *testing.T) {
	bss := ladderBSS(t)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}
	_, err := h.priceOrder(context.Background(), pricingRequest{PlanID: "s", PackageSKU: "plan.s", Topology: topologyActiveHotStandby})
	var refused *packages.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected a packages.RefusedError, got %v", err)
	}
	if refused.PackageSKU != "plan.s" {
		t.Fatalf("refusal must name the package, got %+v", refused)
	}
}

// Single region on S stays free, as before.
func TestPriceOrder_SingleRegionOnS_IsFree(t *testing.T) {
	bss := ladderBSS(t)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}
	p, err := h.priceOrder(context.Background(), pricingRequest{PlanID: "s", PackageSKU: "plan.s", Topology: topologySingleRegion})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	if p.TopologyBaisa != 0 || p.TotalBaisa != 2490 {
		t.Fatalf("S single region: topology %d total %d", p.TopologyBaisa, p.TotalBaisa)
	}
}

// A v1 document (no dr_topology feature) says nothing about the topology, so
// billing keeps its own surcharge — the existing fakeBSS fixture pins that.
func TestPriceOrder_V1Document_KeepsTheSurcharge(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}
	p, err := h.priceOrder(context.Background(), pricingRequest{PlanID: "m", PackageSKU: "plan.m", Topology: topologyActiveHotStandby})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	if p.TopologyBaisa != topologySurchargeBaisa(topologyActiveHotStandby) {
		t.Fatalf("v1 document: surcharge must be billing's own, got %d", p.TopologyBaisa)
	}
}
