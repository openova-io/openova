// sme_consumption_test.go — the B3 showback aggregation (issue #3378
// DoD 3 + §5). Locks the day-one contract: with zero sub-orgs 100% of
// consumption attributes to the parent, broken down per application, with
// per-app percent shares that sum to ~100.
package handler

import (
	"math"
	"testing"
)

func TestAggregateConsumption_ParentSelfShowback(t *testing.T) {
	const parent = "hw130.omantel.biz"
	rows := []podRow{
		// catalyst-api: 500m CPU, 1 GiB mem, no storage.
		{namespace: "catalyst", application: "catalyst-api", cpuReq: 500, memReq: 1 << 30},
		// grafana: 250m CPU, 0.5 GiB mem, 10 GiB storage.
		{namespace: "monitoring", application: "grafana", cpuReq: 250, memReq: 512 << 20, storageLim: 10 << 30},
		// a second grafana pod (same app/ns) — must fold into one app row.
		{namespace: "monitoring", application: "grafana", cpuReq: 250, memReq: 512 << 20},
	}

	resp := aggregateConsumption(rows, parent)

	// Exactly one org row — the parent — and it is flagged parent.
	if len(resp.Orgs) != 1 {
		t.Fatalf("expected exactly the parent org row, got %d orgs", len(resp.Orgs))
	}
	p := resp.Orgs[0]
	if !p.IsParent || p.Org != parent {
		t.Fatalf("first row must be the parent %q, got %q (isParent=%v)", parent, p.Org, p.IsParent)
	}

	// Two distinct apps within the parent.
	if len(p.Apps) != 2 {
		t.Fatalf("expected 2 app rows (catalyst-api + grafana), got %d", len(p.Apps))
	}

	// grafana folded its two pods: 500m CPU total, 1 GiB mem, 10 GiB storage.
	var grafana *appConsumption
	for i := range p.Apps {
		if p.Apps[i].Application == "grafana" {
			grafana = &p.Apps[i]
		}
	}
	if grafana == nil {
		t.Fatal("grafana app row missing")
	}
	if grafana.CPUMilli != 500 {
		t.Errorf("grafana CPUMilli: got %v want 500 (two pods folded)", grafana.CPUMilli)
	}
	if math.Abs(grafana.StorageGiB-10) > 0.001 {
		t.Errorf("grafana StorageGiB: got %v want 10", grafana.StorageGiB)
	}

	// Per-app percents sum to ~100 (the parent owns 100% of the estate).
	var pct float64
	for _, a := range p.Apps {
		pct += a.Percent
	}
	if math.Abs(pct-100) > 0.5 {
		t.Errorf("per-app percents should sum to ~100, got %v", pct)
	}

	// Cost is positive and equals the org total.
	if p.CostUnits <= 0 {
		t.Errorf("parent cost must be positive, got %v", p.CostUnits)
	}
	if math.Abs(p.CostUnits-resp.TotalCostUnits) > 0.01 {
		t.Errorf("single-org estate: parent cost %v should equal total %v", p.CostUnits, resp.TotalCostUnits)
	}
}

func TestAggregateConsumption_EmptyEstateNeverBlank(t *testing.T) {
	// Zero rows ⇒ still exactly one parent row (the §5 never-blank rule).
	resp := aggregateConsumption(nil, "sovereign")
	if len(resp.Orgs) != 1 {
		t.Fatalf("empty estate must still render the parent row, got %d orgs", len(resp.Orgs))
	}
	if !resp.Orgs[0].IsParent {
		t.Errorf("the lone row must be the parent")
	}
	if resp.Orgs[0].Apps == nil {
		t.Errorf("apps must be a non-nil slice so the page renders []")
	}
}
