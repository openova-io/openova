package recommend

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// One positive and one control per rule. Every rule must fire exactly once
// on this fixture and never on its control.

var now = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

func fixture() Input {
	acme := store.CustomerBook{CustomerID: "c-a", CustomerName: "Acme", Status: "active", HasBook: true, BookName: "list", Currency: "OMR", BillStopped: "compute",
		Rates: map[string]store.Decimal{
			"ecs.m7n.2xlarge.8":  "0.80000000",
			"ecs.m7n.xlarge.8":   "0.50000000",
			"ecs.s6.large.2":     "0.10000000",
			"evs.ssd.gb":         "0.00100000",
			"eip":                "0.02000000",
			"eip.bandwidth_mbps": "0.00500000",
		}}
	bravo := store.CustomerBook{CustomerID: "c-b", CustomerName: "Bravo", Status: "active", HasBook: false, Rates: map[string]store.Decimal{}}
	charlie := store.CustomerBook{CustomerID: "c-c", CustomerName: "Charlie", Status: "pending", HasBook: true, BookName: "storage", Currency: "OMR", BillStopped: "storage-only",
		Rates: map[string]store.Decimal{"ecs.m7n.xlarge.8": "0.50000000"}}
	res := func(cid, name, src, id, kind string, attrs map[string]any) store.LiveResource {
		cname := map[string]string{"c-a": "Acme", "c-b": "Bravo", "c-c": "Charlie"}[cid]
		return store.LiveResource{CustomerID: cid, CustomerName: cname, SourceID: src, ResourceID: id, Kind: kind, Name: name, Attrs: attrs}
	}
	return Input{
		Now:   now,
		Books: []store.CustomerBook{acme, bravo, charlie},
		Resources: []store.LiveResource{
			// stopped-instance-billed: positive, and the running control.
			res("c-a", "batch-2", "src-a", "vm-stopped", "ecs", map[string]any{"flavor": "m7n.xlarge.8", "status": "SHUTOFF", "vcpus": 4.0}),
			res("c-a", "web-1", "src-a", "vm-running", "ecs", map[string]any{"flavor": "m7n.2xlarge.8", "status": "ACTIVE", "vcpus": 8.0}),
			// A stopped instance under a storage-only book is not billed: control.
			res("c-c", "old-1", "src-c", "vm-c-stopped", "ecs", map[string]any{"flavor": "m7n.xlarge.8", "status": "SHUTOFF"}),
			// low-cpu controls: busy, and too few samples.
			res("c-a", "db-1", "src-a", "vm-busy", "ecs", map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}),
			res("c-a", "new-1", "src-a", "vm-few", "ecs", map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}),
			// unattached-volume: positive and attached control.
			res("c-a", "vol-orphan", "src-a", "vol-unattached", "evs", map[string]any{"size_gb": 100.0, "volume_type": "SSD", "status": "available"}),
			res("c-a", "vol-web", "src-a", "vol-attached", "evs", map[string]any{"size_gb": 200.0, "volume_type": "SSD", "status": "in-use", "attached_to": "vm-running"}),
			// unbound-eip: positive and bound control.
			res("c-a", "1.2.3.4", "src-a", "eip-down", "eip", map[string]any{"public_ip_address": "1.2.3.4", "bandwidth_mbps": 5.0, "bandwidth_name": "bw-1", "status": "DOWN", "type": "5_bgp"}),
			res("c-a", "1.2.3.5", "src-a", "eip-active", "eip", map[string]any{"public_ip_address": "1.2.3.5", "bandwidth_mbps": 5.0, "status": "ACTIVE", "type": "5_bgp"}),
		},
		CPUUtil: []store.CPUUtilMean{
			{CustomerID: "c-a", SourceID: "src-a", ResourceID: "vm-running", Samples: 168, Mean: 4.2},
			{CustomerID: "c-a", SourceID: "src-a", ResourceID: "vm-busy", Samples: 168, Mean: 55},
			{CustomerID: "c-a", SourceID: "src-a", ResourceID: "vm-few", Samples: 10, Mean: 2},
			// A sample for a resource no longer in inventory is ignored.
			{CustomerID: "c-a", SourceID: "src-a", ResourceID: "vm-gone", Samples: 168, Mean: 1},
		},
		Unpriced: []store.CustomerUnpricedSKU{
			{CustomerID: "c-a", CustomerName: "Acme", SKU: "nat.small", Unit: "hour", Quantity: "24.000000", Resources: 1},
			// Controls: a priced SKU, the metric, and a customer with no book.
			{CustomerID: "c-a", CustomerName: "Acme", SKU: "eip", Unit: "hour", Quantity: "720.000000", Resources: 3},
			{CustomerID: "c-a", CustomerName: "Acme", SKU: "ecs.cpu_util", Unit: "pct-hour-avg", Quantity: "9000.000000", Resources: 3},
			{CustomerID: "c-b", CustomerName: "Bravo", SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", Quantity: "720.000000", Resources: 1},
		},
		Sources: []store.SourceHealth{
			{SourceID: "src-a", CustomerID: "c-a", CustomerName: "Acme", CustomerStatus: "active", Kind: "huawei-project", Region: "me-east-1", ProjectID: "proj-a", Status: "verified", LastCollectedAt: tp(now.Add(-30 * time.Minute))},
			{SourceID: "src-a-stale", CustomerID: "c-a", CustomerName: "Acme", CustomerStatus: "active", Kind: "huawei-project", Region: "me-east-2", ProjectID: "proj-a2", Status: "verified", LastCollectedAt: tp(now.Add(-3 * time.Hour))},
			// A verified source of a pending customer is dormant, not stale.
			{SourceID: "src-c", CustomerID: "c-c", CustomerName: "Charlie", CustomerStatus: "pending", Kind: "huawei-project", Region: "me-east-1", ProjectID: "proj-c", Status: "verified"},
			// Pending sources were never collected: control.
			{SourceID: "src-a-pending", CustomerID: "c-a", CustomerName: "Acme", CustomerStatus: "active", Kind: "huawei-project", Region: "me-east-1", ProjectID: "proj-new", Status: "pending"},
		},
	}
}

func byType(rows []Recommendation) map[string][]Recommendation {
	out := map[string][]Recommendation{}
	for _, r := range rows {
		out[r.Type] = append(out[r.Type], r)
	}
	return out
}

func TestEveryRuleFiresOnceAndNotOnItsControl(t *testing.T) {
	rows := Evaluate(fixture())
	bt := byType(rows)
	for _, typ := range []string{TypeStoppedInstanceBilled, TypeUnattachedVolume, TypeUnboundEIP, TypeLowCPU, TypeUnpricedSKU, TypeStaleSource, TypeNoPriceBook} {
		if n := len(bt[typ]); n != 1 {
			t.Errorf("%s fired %d times: %+v", typ, n, bt[typ])
		}
	}
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		switch r.ResourceID {
		case "vm-running", "vm-c-stopped", "vm-busy", "vm-few", "vm-gone", "vol-attached", "eip-active":
			if r.Type != TypeLowCPU || r.ResourceID != "vm-running" {
				t.Errorf("control %s produced %+v", r.ResourceID, r)
			}
		}
	}

	// stopped-instance-billed: ecs.m7n.xlarge.8 at 0.5/h × 730 h.
	s := bt[TypeStoppedInstanceBilled][0]
	if s.ResourceID != "vm-stopped" || s.ResourceName != "batch-2" || s.Kind != "ecs" || s.MonthlySaving != "365.000000" || s.Currency != "OMR" || s.Severity != SeverityHigh {
		t.Errorf("stopped = %+v", s)
	}
	if s.Evidence["bill_stopped"] != "compute" || s.Evidence["status"] != "SHUTOFF" {
		t.Errorf("stopped evidence = %+v", s.Evidence)
	}
	// unattached-volume: 100 GB × 0.001/GB-h × 730.
	v := bt[TypeUnattachedVolume][0]
	if v.ResourceID != "vol-unattached" || v.MonthlySaving != "73.000000" || v.Evidence["size_gb"] != 100.0 {
		t.Errorf("volume = %+v", v)
	}
	// unbound-eip: (0.02 + 5 × 0.005) × 730 = 32.85.
	e := bt[TypeUnboundEIP][0]
	if e.ResourceID != "eip-down" || e.MonthlySaving != "32.850000" || e.Evidence["status"] != "DOWN" || e.Evidence["bandwidth_mbps"] != 5.0 {
		t.Errorf("eip = %+v", e)
	}
	// low-cpu: m7n.2xlarge.8 (0.8) → m7n.xlarge.8 (0.5): 0.3 × 730 = 219, exact, not an estimate.
	c := bt[TypeLowCPU][0]
	if c.ResourceID != "vm-running" || c.MonthlySaving != "219.000000" || c.Evidence["suggested_flavor"] != "m7n.xlarge.8" || c.Evidence["samples"] != 168 {
		t.Errorf("low-cpu = %+v", c)
	}
	if _, est := c.Evidence["estimate"]; est {
		t.Errorf("low-cpu with a priced smaller SKU must not be an estimate: %+v", c.Evidence)
	}
	if c.Evidence["cpu_mean_7d_pct"] != 4.2 {
		t.Errorf("cpu evidence = %+v", c.Evidence)
	}
	// unpriced-sku: only nat.small; priced eip, the metric and the bookless
	// customer's usage are not rows.
	u := bt[TypeUnpricedSKU][0]
	if u.CustomerID != "c-a" || u.Evidence["sku"] != "nat.small" || u.Evidence["quantity_30d"] != store.Decimal("24.000000") || u.Evidence["unit"] != "hour" || u.Evidence["resources"] != 1 || u.MonthlySaving != "0.000000" || u.Severity != SeverityHigh {
		t.Errorf("unpriced = %+v", u)
	}
	// stale-source: only the 3-hour-old one.
	st := bt[TypeStaleSource][0]
	if st.Evidence["source_id"] != "src-a-stale" || st.Evidence["reason"] != "stale" || st.Evidence["age_minutes"] != 180 || st.Severity != SeverityMedium {
		t.Errorf("stale = %+v", st)
	}
	// no-price-book: Bravo only.
	nb := bt[TypeNoPriceBook][0]
	if nb.CustomerID != "c-b" || nb.Severity != SeverityHigh || nb.ResourceID != "" {
		t.Errorf("no-book = %+v", nb)
	}

	// Sorted by saving desc, then the zero-saving rows by severity and type.
	wantOrder := []string{TypeStoppedInstanceBilled, TypeLowCPU, TypeUnattachedVolume, TypeUnboundEIP, TypeNoPriceBook, TypeUnpricedSKU, TypeStaleSource}
	for i, typ := range wantOrder {
		if rows[i].Type != typ {
			t.Fatalf("row %d = %s, want %s", i, rows[i].Type, typ)
		}
	}
	if got := Total(rows); got != "689.850000" {
		t.Fatalf("total = %s", got)
	}
	if Currency(rows, nil) != "OMR" {
		t.Fatalf("currency = %q", Currency(rows, nil))
	}
	// Stable ids.
	if s.ID != "stopped-instance-billed:c-a:vm-stopped" || nb.ID != "no-price-book:c-b" || st.ID != "stale-source:src-a-stale" || u.ID != "unpriced-sku:c-a:nat.small" {
		t.Fatalf("ids = %s %s %s %s", s.ID, nb.ID, st.ID, u.ID)
	}
}

func TestWireShape(t *testing.T) {
	rows := Evaluate(fixture())
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	if err := json.Unmarshal(b, &docs); err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		for _, k := range []string{"id", "type", "severity", "customer_id", "customer_name", "title", "detail", "monthly_saving", "currency"} {
			if _, ok := d[k]; !ok {
				t.Errorf("row %v lacks %q", d["id"], k)
			}
		}
		if _, isNum := d["monthly_saving"].(float64); !isNum {
			t.Errorf("monthly_saving must be a JSON number: %v", d["monthly_saving"])
		}
		if d["type"] == TypeNoPriceBook {
			if _, has := d["resource_id"]; has {
				t.Errorf("customer-level row carries resource_id: %v", d)
			}
		}
		if d["type"] == TypeUnboundEIP && d["kind"] != "eip" {
			t.Errorf("eip row kind = %v", d["kind"])
		}
	}
}

func TestLowCPUEstimatesWhenTheSmallerSKUIsUnpriced(t *testing.T) {
	in := Input{
		Now:   now,
		Books: []store.CustomerBook{{CustomerID: "c-a", CustomerName: "Acme", HasBook: true, Currency: "OMR", BillStopped: "compute", Rates: map[string]store.Decimal{"ecs.s6.large.2": "0.10000000"}}},
		Resources: []store.LiveResource{
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-1", Kind: "ecs", Name: "x", Attrs: map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}},
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-2", Kind: "ecs", Name: "y", Attrs: map[string]any{"flavor": "s6.small.1", "status": "ACTIVE"}},
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-3", Kind: "ecs", Name: "z", Attrs: map[string]any{"flavor": "c7.xlarge.2", "status": "ACTIVE"}},
		},
		CPUUtil: []store.CPUUtilMean{
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-1", Samples: 48, Mean: 9.99},
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-2", Samples: 48, Mean: 1},
			{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-3", Samples: 48, Mean: 1},
		},
	}
	rows := byType(Evaluate(in))[TypeLowCPU]
	if len(rows) != 2 {
		t.Fatalf("want vm-1 (estimate) and vm-3 (unpriced), got %+v", rows)
	}
	var est, unpriced *Recommendation
	for i := range rows {
		switch rows[i].ResourceID {
		case "vm-1":
			est = &rows[i]
		case "vm-3":
			unpriced = &rows[i]
		}
	}
	// 0.1 / 2 × 730 = 36.5, flagged as an estimate.
	if est == nil || est.MonthlySaving != "36.500000" || est.Evidence["estimate"] != true || est.Evidence["suggested_flavor"] != "s6.medium.2" {
		t.Fatalf("estimate row = %+v", est)
	}
	// The current flavor is unpriced: no saving can be computed, say so.
	if unpriced == nil || unpriced.MonthlySaving != "0.000000" || unpriced.Evidence["unpriced"] != true {
		t.Fatalf("unpriced row = %+v", unpriced)
	}
	// vm-2 is already the smallest size: nothing to step down to.
	for _, r := range rows {
		if r.ResourceID == "vm-2" {
			t.Fatalf("smallest flavor recommended a step down: %+v", r)
		}
	}
	// 10.0 % exactly is not below the threshold.
	in.CPUUtil = []store.CPUUtilMean{{CustomerID: "c-a", SourceID: "s", ResourceID: "vm-1", Samples: 48, Mean: 10}}
	if got := byType(Evaluate(in))[TypeLowCPU]; len(got) != 0 {
		t.Fatalf("10.0 %% flagged: %+v", got)
	}
}

func TestStepDownLadder(t *testing.T) {
	cases := map[string]string{
		"m7n.2xlarge.8":  "m7n.xlarge.8",
		"m7n.xlarge.8":   "m7n.large.8",
		"s6.large.2":     "s6.medium.2",
		"s6.medium.2":    "s6.small.2",
		"c7.4xlarge.2":   "c7.2xlarge.2",
		"c7.3xlarge.2":   "c7.2xlarge.2",
		"c7.6xlarge.2":   "c7.4xlarge.2",
		"c7.12xlarge.4":  "c7.8xlarge.4",
		"c7.24xlarge.4":  "c7.16xlarge.4",
		"c7.32xlarge.4":  "c7.16xlarge.4",
		"c7n.16xlarge.4": "c7n.8xlarge.4",
		"c7.large":       "c7.medium",
	}
	for in, want := range cases {
		got, ok := StepDown(in)
		if !ok || got != want {
			t.Errorf("StepDown(%s) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"s6.small.1", "", "unknown", "bare-metal", "kc1.small.2"} {
		if got, ok := StepDown(in); ok {
			t.Errorf("StepDown(%s) = %q, want none", in, got)
		}
	}
}

func TestStaleSourceReasons(t *testing.T) {
	src := func(id, status, cstatus, lastErr string, at *time.Time) store.SourceHealth {
		return store.SourceHealth{SourceID: id, CustomerID: "c", CustomerName: "C", CustomerStatus: cstatus, Kind: "huawei-project", Region: "r", ProjectID: "p", Status: status, LastError: lastErr, LastCollectedAt: at}
	}
	in := Input{Now: now, Sources: []store.SourceHealth{
		src("fresh", "verified", "active", "", tp(now.Add(-119*time.Minute))),
		src("stale", "verified", "active", "", tp(now.Add(-121*time.Minute))),
		src("never", "verified", "active", "", nil),
		src("error", "verified", "active", "APIGW.0301: signature fail", tp(now.Add(-10*time.Minute))),
		src("failed", "failed", "active", "APIGW.0301", nil),
		src("pending", "pending", "active", "", nil),
		src("dormant", "verified", "suspended", "", nil),
	}}
	rows := byType(Evaluate(in))[TypeStaleSource]
	got := map[string]string{}
	for _, r := range rows {
		got[r.Evidence["source_id"].(string)] = r.Evidence["reason"].(string)
	}
	want := map[string]string{"stale": "stale", "never": "never-collected", "error": "error", "failed": "failed"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("%s: reason %q want %q", id, got[id], reason)
		}
	}
	for _, r := range rows {
		if r.Evidence["source_id"] == "error" && r.Evidence["last_error"] != "APIGW.0301: signature fail" {
			t.Errorf("error evidence = %+v", r.Evidence)
		}
	}
}

func TestEmptyInputIsEmptyNotNil(t *testing.T) {
	rows := Evaluate(Input{Now: now})
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	if Total(rows) != "0.000000" || Currency(rows, nil) != "" {
		t.Fatalf("total/currency = %s/%q", Total(rows), Currency(rows, nil))
	}
	b, _ := json.Marshal(rows)
	if string(b) != "[]" {
		t.Fatalf("json = %s", b)
	}
}
