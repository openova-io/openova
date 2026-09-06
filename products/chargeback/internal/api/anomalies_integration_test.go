package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Anomalies + recommendations through the API (#6867, DESIGN.md §3.6-3.7),
// with a pinned clock so "last 7 days" and "last 30 days" are deterministic.

var walkNow = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func setupAPIAt(t *testing.T, now time.Time) (http.Handler, *store.Store, *recMail) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	mail := &recMail{}
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := New(Deps{
		Store:    st,
		Keys:     keys,
		Mail:     mail,
		Verifier: &fakeVerifier{},
		Config:   config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "sovereign", OperatorEmails: []string{opEmail}},
		Metrics:  metrics.New(),
		Now:      func() time.Time { return now },
		Version:  "test",
	})
	return h, st, mail
}

func dayAt(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

type walkSeed struct {
	a, b, c                 store.Customer
	srcA, srcA2, srcB, srcC store.CostSource
	bookID                  string
}

// seedWalk builds the ledger both endpoints read:
//
//   - Customer A (Acme, book "list", bill_stopped=compute, active): ECS vm-1
//     all of August, a second ECS vm-2 on 2026-08-20 only (the spike), a
//     flat 100 GB volume, an unpriced nat.small meter, CPU samples, and an
//     inventory holding one positive and one control per resource rule.
//   - Customer B (Bravo, NO price book): a flat EIP meter.
//   - Customer C (Charlie, book "storage", bill_stopped=storage-only,
//     pending): a stopped ECS (not billed → control) and a dormant source.
func seedWalk(t *testing.T, st *store.Store) walkSeed {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", UnitPrice: "0.8"},
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"},
		{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "0.1"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02"},
		{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.005"},
	}, true); err != nil {
		t.Fatal(err)
	}
	storage, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "storage", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "storage-only"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, storage.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", PriceBookID: book.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "charlie", Name: "Charlie", AdminEmail: "c@charlie.example", PriceBookID: storage.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if err := st.SetCustomerStatus(ctx, id, "active"); err != nil {
			t.Fatal(err)
		}
	}
	srcA, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcA2, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-2", "proj-a2")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, b.ID, "huawei-project", "me-east-1", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	srcC, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-1", "proj-c")
	if err != nil {
		t.Fatal(err)
	}
	// Source health: srcA fresh (30 min), srcA2 stale (3 h), srcB fresh,
	// srcC verified but never collected for a pending customer (dormant).
	for _, s := range []store.CostSource{srcA, srcA2, srcB, srcC} {
		if err := st.SetSourceVerified(ctx, s.ID, "dom"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSourceCollected(ctx, srcA.ID, walkNow.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceCollected(ctx, srcA2.ID, walkNow.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceCollected(ctx, srcB.ID, walkNow.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Ledger.
	var recs []store.UsageRecord
	rec := func(cu store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: cu.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	for d := 1; d <= 22; d++ {
		for h := 0; h < 24; h++ {
			at := dayAt(2026, 8, d).Add(time.Duration(h) * time.Hour)
			if !at.Before(walkNow) {
				break
			}
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
			if d == 20 {
				rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "batch-2", "status": "ACTIVE"})
			}
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1", "server_status": "ACTIVE"})
			rec(b, srcB, "eip-b", "eip", "eip", "hour", 1, at, map[string]any{"name": "5.6.7.8"})
			if d == 15 {
				rec(a, srcA, "nat-1", "nat", "nat.small", "hour", 1, at, map[string]any{"name": "nat-1"})
			}
		}
	}
	// CPU samples for the last 7 days: vm-running idle, vm-busy busy, vm-few
	// with too few samples.
	for k := 0; k < 168; k++ {
		at := walkNow.Add(-168 * time.Hour).Add(time.Duration(k) * time.Hour)
		rec(a, srcA, "vm-running", "ecs", "ecs.cpu_util", "pct-hour-avg", 4.2, at, map[string]any{"name": "web-1"})
		rec(a, srcA, "vm-busy", "ecs", "ecs.cpu_util", "pct-hour-avg", 55, at, map[string]any{"name": "db-1"})
		if k < 10 {
			rec(a, srcA, "vm-few", "ecs", "ecs.cpu_util", "pct-hour-avg", 2, at, map[string]any{"name": "new-1"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// Inventory.
	inv := func(src store.CostSource, items ...store.InventoryUpsert) {
		for i := range items {
			items[i].SeenAt = walkNow
		}
		if _, err := st.UpsertInventory(ctx, src.ID, items); err != nil {
			t.Fatal(err)
		}
	}
	inv(srcA,
		store.InventoryUpsert{ResourceID: "vm-stopped", Kind: "ecs", Name: "batch-2", Attrs: map[string]any{"flavor": "m7n.xlarge.8", "status": "SHUTOFF", "vcpus": 4}},
		store.InventoryUpsert{ResourceID: "vm-running", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"flavor": "m7n.2xlarge.8", "status": "ACTIVE", "vcpus": 8}},
		store.InventoryUpsert{ResourceID: "vm-busy", Kind: "ecs", Name: "db-1", Attrs: map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}},
		store.InventoryUpsert{ResourceID: "vm-few", Kind: "ecs", Name: "new-1", Attrs: map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}},
		store.InventoryUpsert{ResourceID: "vol-unattached", Kind: "evs", Name: "vol-orphan", Attrs: map[string]any{"size_gb": 100, "volume_type": "SSD", "status": "available"}},
		store.InventoryUpsert{ResourceID: "vol-attached", Kind: "evs", Name: "vol-web", Attrs: map[string]any{"size_gb": 200, "volume_type": "SSD", "status": "in-use", "attached_to": "vm-running"}},
		store.InventoryUpsert{ResourceID: "eip-down", Kind: "eip", Name: "1.2.3.4", Attrs: map[string]any{"public_ip_address": "1.2.3.4", "bandwidth_mbps": 5, "bandwidth_name": "bw-1", "status": "DOWN", "type": "5_bgp"}},
		store.InventoryUpsert{ResourceID: "eip-active", Kind: "eip", Name: "1.2.3.5", Attrs: map[string]any{"public_ip_address": "1.2.3.5", "bandwidth_mbps": 5, "status": "ACTIVE", "type": "5_bgp"}},
		// A deleted stopped instance is not live: control.
		store.InventoryUpsert{ResourceID: "vm-deleted", Kind: "ecs", Name: "gone", Attrs: map[string]any{"flavor": "m7n.xlarge.8", "status": "SHUTOFF"}},
	)
	if _, err := st.MarkInventoryDeleted(ctx, srcA.ID, []string{"ecs"}, []string{"vm-stopped", "vm-running", "vm-busy", "vm-few"}, walkNow); err != nil {
		t.Fatal(err)
	}
	inv(srcC, store.InventoryUpsert{ResourceID: "vm-c-stopped", Kind: "ecs", Name: "old-1", Attrs: map[string]any{"flavor": "m7n.xlarge.8", "status": "SHUTOFF"}})
	return walkSeed{a: a, b: b, c: c, srcA: srcA, srcA2: srcA2, srcB: srcB, srcC: srcC, bookID: book.ID}
}

func rowsOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, ok := doc["rows"].([]any)
	if !ok {
		t.Fatalf("no rows array in %v", doc)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestIntegrationAnomaliesEndpointsAndSummary(t *testing.T) {
	h, st, mail := setupAPIAt(t, walkNow)
	s := seedWalk(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// Default window = the last 30 days; the spike on 08-20 is the only row.
	doc := op.must("GET", "/api/v1/anomalies", 200)
	if doc["from"] != "2026-07-24" || doc["to"] != "2026-08-23" {
		t.Fatalf("window = %v..%v", doc["from"], doc["to"])
	}
	rows := rowsOf(t, doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	r := rows[0]
	if r["day"] != "2026-08-20" || r["customer_id"] != s.a.ID || r["customer_name"] != "Acme" || r["dimension"] != "kind" || r["key"] != "ecs" || r["label"] != "Elastic Cloud Server" {
		t.Fatalf("row = %v", r)
	}
	if r["expected"] != 12.0 || r["actual"] != 24.0 || r["impact"] != 12.0 || r["score"] != 99.0 {
		t.Fatalf("numbers = %v/%v/%v/%v", r["expected"], r["actual"], r["impact"], r["score"])
	}
	drivers := r["drivers"].([]any)
	if len(drivers) != 2 {
		t.Fatalf("drivers = %v", drivers)
	}
	if d := drivers[0].(map[string]any); d["kind"] != "resource" || d["key"] != "vm-2" || d["label"] != "batch-2" || d["delta"] != 12.0 {
		t.Fatalf("driver = %v", d)
	}
	// An explicit window that excludes the spike day is empty; the day right
	// after it is not flagged (it is below its baseline).
	if rows := rowsOf(t, op.must("GET", "/api/v1/anomalies?from=2026-08-21&to=2026-08-23", 200)); len(rows) != 0 {
		t.Fatalf("08-21.. = %v", rows)
	}
	// Bad windows.
	for _, bad := range []string{"?from=2026-08-23&to=2026-08-01", "?from=today", "?from=2025-01-01&to=2026-08-23"} {
		if rec, _ := op.do("GET", "/api/v1/anomalies"+bad, "", nil); rec.Code != 400 {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
	// The overview and both summaries carry the same row (last 7 days).
	for _, path := range []string{"/api/v1/overview", "/api/v1/cost/summary", "/api/v1/customers/" + s.a.ID + "/cost/summary"} {
		sum := op.must("GET", path, 200)
		an, ok := sum["anomalies"].([]any)
		if !ok || len(an) != 1 {
			t.Fatalf("%s anomalies = %v", path, sum["anomalies"])
		}
		if a := an[0].(map[string]any); a["day"] != "2026-08-20" || a["impact"] != 12.0 || len(a["drivers"].([]any)) != 2 {
			t.Fatalf("%s anomaly = %v", path, a)
		}
	}
	// B's summary has none: the spike is A's.
	if sum := op.must("GET", "/api/v1/customers/"+s.b.ID+"/cost/summary", 200); len(sum["anomalies"].([]any)) != 0 {
		t.Fatalf("B summary anomalies = %v", sum["anomalies"])
	}

	// Customer lens: B sees its own (empty) list, cannot read A, cannot list all.
	cb := &client{t: t, h: h}
	cb.signIn("b@bravo.example", mail)
	if rows := rowsOf(t, cb.must("GET", "/api/v1/customers/"+s.b.ID+"/anomalies", 200)); len(rows) != 0 {
		t.Fatalf("B anomalies = %v", rows)
	}
	cb.must("GET", "/api/v1/customers/"+s.a.ID+"/anomalies", 404)
	cb.must("GET", "/api/v1/anomalies", 403)
	// A's admin sees the spike through the customer route.
	ca := &client{t: t, h: h}
	ca.signIn("a@acme.example", mail)
	if rows := rowsOf(t, ca.must("GET", "/api/v1/customers/"+s.a.ID+"/anomalies", 200)); len(rows) != 1 || rows[0]["key"] != "ecs" {
		t.Fatalf("A anomalies = %v", rows)
	}
	// Signed out: 401.
	anon := &client{t: t, h: h}
	anon.must("GET", "/api/v1/anomalies", 401)
}

func TestIntegrationRecommendationsEveryRuleOnceAndScoped(t *testing.T) {
	h, st, mail := setupAPIAt(t, walkNow)
	s := seedWalk(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	doc := op.must("GET", "/api/v1/recommendations", 200)
	rows := rowsOf(t, doc)
	count := map[string]int{}
	byType := map[string]map[string]any{}
	for _, r := range rows {
		count[r["type"].(string)]++
		byType[r["type"].(string)] = r
	}
	for _, typ := range []string{"stopped-instance-billed", "unattached-volume", "unbound-eip", "low-cpu-utilisation", "unpriced-sku", "stale-source", "no-price-book"} {
		if count[typ] != 1 {
			t.Errorf("%s fired %d times", typ, count[typ])
		}
	}
	if len(rows) != 7 {
		t.Fatalf("rows = %d: %v", len(rows), rows)
	}
	if doc["total_monthly_saving"] != 689.85 || doc["currency"] != "OMR" {
		t.Fatalf("total/currency = %v/%v", doc["total_monthly_saving"], doc["currency"])
	}
	// The positives, with the exact savings from the rate card.
	if r := byType["stopped-instance-billed"]; r["resource_id"] != "vm-stopped" || r["customer_id"] != s.a.ID || r["monthly_saving"] != 365.0 || r["kind"] != "ecs" {
		t.Errorf("stopped = %v", r)
	}
	if r := byType["unattached-volume"]; r["resource_id"] != "vol-unattached" || r["monthly_saving"] != 73.0 {
		t.Errorf("volume = %v", r)
	}
	if r := byType["unbound-eip"]; r["resource_id"] != "eip-down" || r["monthly_saving"] != 32.85 || r["evidence"].(map[string]any)["status"] != "DOWN" {
		t.Errorf("eip = %v", r)
	}
	if r := byType["low-cpu-utilisation"]; r["resource_id"] != "vm-running" || r["monthly_saving"] != 219.0 || r["evidence"].(map[string]any)["suggested_flavor"] != "m7n.xlarge.8" || r["evidence"].(map[string]any)["samples"] != 168.0 {
		t.Errorf("low-cpu = %v", r)
	}
	if r := byType["unpriced-sku"]; r["customer_id"] != s.a.ID || r["evidence"].(map[string]any)["sku"] != "nat.small" || r["evidence"].(map[string]any)["quantity_30d"] != 24.0 || r["monthly_saving"] != 0.0 || r["severity"] != "high" {
		t.Errorf("unpriced = %v", r)
	}
	if r := byType["stale-source"]; r["evidence"].(map[string]any)["source_id"] != s.srcA2.ID || r["evidence"].(map[string]any)["reason"] != "stale" || r["severity"] != "medium" {
		t.Errorf("stale = %v", r)
	}
	if r := byType["no-price-book"]; r["customer_id"] != s.b.ID || r["severity"] != "high" {
		t.Errorf("no-book = %v", r)
	}
	// Sorted by saving desc.
	if rows[0]["type"] != "stopped-instance-billed" || rows[1]["type"] != "low-cpu-utilisation" || rows[2]["type"] != "unattached-volume" || rows[3]["type"] != "unbound-eip" {
		t.Fatalf("order = %v %v %v %v", rows[0]["type"], rows[1]["type"], rows[2]["type"], rows[3]["type"])
	}
	// Controls never appear: running ECS, attached volume, bound EIP, busy
	// and under-sampled instances, the deleted stopped instance, C's
	// stopped instance under storage-only, C's dormant source.
	for _, r := range rows {
		switch r["resource_id"] {
		case "vm-busy", "vm-few", "vm-deleted", "vol-attached", "eip-active", "vm-c-stopped":
			t.Errorf("control fired: %v", r)
		case "vm-running":
			if r["type"] != "low-cpu-utilisation" {
				t.Errorf("running instance fired %v", r)
			}
		}
		if r["customer_id"] == s.c.ID {
			t.Errorf("Charlie must have no rows: %v", r)
		}
	}

	// Operator, one customer: A's six; B's no-price-book is not among them.
	rowsA := rowsOf(t, op.must("GET", "/api/v1/customers/"+s.a.ID+"/recommendations", 200))
	if len(rowsA) != 6 {
		t.Fatalf("A rows = %d", len(rowsA))
	}
	for _, r := range rowsA {
		if r["customer_id"] != s.a.ID {
			t.Fatalf("A's list carries %v", r)
		}
	}

	// Customer lens: B gets exactly its own no-price-book row and nothing of
	// A, and cannot read A's list at all.
	cb := &client{t: t, h: h}
	cb.signIn("b@bravo.example", mail)
	docB := cb.must("GET", "/api/v1/customers/"+s.b.ID+"/recommendations", 200)
	rowsB := rowsOf(t, docB)
	if len(rowsB) != 1 || rowsB[0]["type"] != "no-price-book" || rowsB[0]["customer_id"] != s.b.ID {
		t.Fatalf("B rows = %v", rowsB)
	}
	if docB["total_monthly_saving"] != 0.0 {
		t.Fatalf("B total = %v", docB["total_monthly_saving"])
	}
	cb.must("GET", "/api/v1/customers/"+s.a.ID+"/recommendations", 404)
	cb.must("GET", "/api/v1/recommendations", 403)
	anon := &client{t: t, h: h}
	anon.must("GET", "/api/v1/recommendations", 401)
}
