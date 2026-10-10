package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The document's grow contract (DESIGN.md §22.11): packages[].grow is
// present only where the package allows it, with the ceiling and the
// overage rates — the package's own compute rates and the book's flat disk
// and bandwidth, each price_month = unit price × 730 rounded once — and a
// grow-only cell is published optional, grow_only, with no add-on and no
// price, and is never bundled by the step-up rule.
func TestPackagesDocumentGrow(t *testing.T) {
	book := store.PriceBook{Name: store.PlanBookName, Currency: "OMR", AnnualDivisor: 8760, UpdatedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	book.Items = []store.PriceItem{
		{SKU: "plan.s", Unit: store.PlanUnit, UnitPrice: "0.00341096"},
		{SKU: "plan.m", Unit: store.PlanUnit, UnitPrice: "0.00615068"},
		{SKU: "plan.xl", Unit: store.PlanUnit, UnitPrice: "0.01916438"},
		// 0.423 / 8760 and 15.038 / 8760.
		{SKU: "k8s.pvc_gb", Unit: "gb-hour", UnitPrice: "0.00004829"},
		{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.00171667"},
	}
	dr := store.Feature{ID: "f1", Key: "dr_topology", Name: "DR topology", Kind: store.FeatureKindLevel, Group: store.FeatureGroupResilience, Levels: []string{"single region", "active-passive"}}
	standby := store.Feature{ID: "f2", Key: "standby", Name: "Standby", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupResilience}
	lv := func(n int) *int { return &n }
	cells := []store.Entitlement{
		{PlanSKU: "plan.s", FeatureID: "f1", State: store.EntitlementOptional, Level: lv(0), GrowOnly: true, Feature: dr},
		{PlanSKU: "plan.m", FeatureID: "f1", State: store.EntitlementOptional, Level: lv(0), GrowOnly: true, Feature: dr},
		{PlanSKU: "plan.xl", FeatureID: "f1", State: store.EntitlementIncluded, Level: lv(1), Feature: dr},
		{PlanSKU: "plan.m", FeatureID: "f2", State: store.EntitlementOptional, GrowOnly: true, Feature: standby},
		{PlanSKU: "plan.xl", FeatureID: "f2", State: store.EntitlementIncluded, Feature: standby},
	}
	settings := map[string]store.PackageSettings{
		"plan.m":  {PlanSKU: "plan.m", GrowAllowed: true, GrowCeilingVCPU: dc("8"), GrowCeilingMemoryGB: dc("16"), GrowCeilingDiskGB: dc("250"), GrowCeilingBandwidthMbps: dc("1000"), OverageVCPUMonth: dc("1.796"), OverageMemGBMonth: dc("0.337")},
		"plan.xl": {PlanSKU: "plan.xl", GrowAllowed: true, GrowCeilingVCPU: dc("16"), GrowCeilingMemoryGB: dc("32"), GrowCeilingDiskGB: dc("500"), GrowCeilingBandwidthMbps: dc("2000"), OverageVCPUMonth: dc("1.399"), OverageMemGBMonth: dc("0.263")},
		// S has settings and no grow: nothing of grow is published.
		"plan.s": {PlanSKU: "plan.s"},
	}
	doc, err := packagesDocument(book, []store.Feature{dr, standby}, cells, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	var got struct {
		Packages []map[string]json.RawMessage `json:"packages"`
		Features []struct {
			Key   string                     `json:"key"`
			Cells map[string]json.RawMessage `json:"cells"`
		} `json:"features"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	byPlan := map[string]map[string]json.RawMessage{}
	for _, p := range got.Packages {
		var sku string
		_ = json.Unmarshal(p["sku"], &sku)
		byPlan[sku] = p
	}
	if _, has := byPlan["plan.s"]["grow"]; has {
		t.Fatalf("S publishes grow: %s", byPlan["plan.s"]["grow"])
	}
	wantM := `{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[` +
		`{"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.796"},{"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.337"},` +
		`{"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},{"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}`
	if string(byPlan["plan.m"]["grow"]) != wantM {
		t.Fatalf("M grow =\n%s\nwant\n%s", byPlan["plan.m"]["grow"], wantM)
	}
	wantXL := `{"allowed":true,"ceiling":{"vcpu":16,"memory_gb":32,"disk_gb":500,"bandwidth_mbps":2000},"overage_rates":[` +
		`{"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.399"},{"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.263"},` +
		`{"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},{"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}`
	if string(byPlan["plan.xl"]["grow"]) != wantXL {
		t.Fatalf("XL grow = %s", byPlan["plan.xl"]["grow"])
	}
	for _, f := range got.Features {
		switch f.Key {
		case "dr_topology":
			if s := string(f.Cells["plan.s"]); s != `{"state":"optional","included_from":"plan.xl","level":0,"grow_only":true}` {
				t.Fatalf("DR on S = %s", s)
			}
			if s := string(f.Cells["plan.xl"]); s != `{"state":"included","level":1}` {
				t.Fatalf("DR on XL = %s", s)
			}
		case "standby":
			if s := string(f.Cells["plan.m"]); s != `{"state":"optional","included_from":"plan.xl","grow_only":true}` {
				t.Fatalf("grow-only boolean on M = %s", s)
			}
		}
	}
	// The step M → XL bundles nothing: a grow-only cell is not an add-on.
	var step struct {
		Keys []string `json:"bundled_addon_keys"`
	}
	_ = json.Unmarshal(byPlan["plan.m"]["step_up"], &step)
	if len(step.Keys) != 0 {
		t.Fatalf("step-up bundled %v", step.Keys)
	}
}
