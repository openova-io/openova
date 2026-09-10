package main

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The upsert the seeder writes through keeps every row it does not mention,
// so a SKU the model stops emitting survives a re-seed — 11,268 synthetic
// eip.bandwidth_mbps rows did on hw307 after the traffic-model re-seed. The
// removal runs ahead of the write and must take exactly those rows: the
// stale SKU on THIS source inside THIS window, and nothing outside the
// window, nothing of a SKU still emitted, nothing on another source.

// seedThrough writes a model's records the way writeUsageMonth does (store
// upsert), one hourly row per SKU per hour of the window.
func seedThrough(t *testing.T, st *store.Store, customerID, sourceID string, w synth.Window, skus ...string) []synth.Record {
	t.Helper()
	var recs []synth.Record
	var rows []store.UsageRecord
	for at := w.From; at.Before(w.To); at = at.Add(time.Hour) {
		for _, sku := range skus {
			r := synth.Record{ResourceID: "eip-1", ResourceKind: "eip", SKU: sku, Unit: "hour", Quantity: 1, Start: at, End: at.Add(time.Hour), Region: "me-east-215-a"}
			recs = append(recs, r)
			rows = append(rows, store.UsageRecord{
				CustomerID: customerID, SourceID: sourceID, ResourceID: r.ResourceID, ResourceKind: r.ResourceKind,
				SKU: r.SKU, Quantity: store.Decimal(strconv.FormatFloat(r.Quantity, 'f', 6, 64)), Unit: r.Unit,
				WindowStart: r.Start, WindowEnd: r.End, Region: r.Region, Labels: []byte(`{"synthetic":"true"}`), RawRef: "seed-history",
			})
		}
	}
	if _, err := st.UpsertUsage(context.Background(), rows); err != nil {
		t.Fatalf("seed %v: %v", skus, err)
	}
	return recs
}

func countSKU(t *testing.T, db *sql.DB, sourceID, sku string, w synth.Window) int {
	t.Helper()
	return scalar[int](t, db, `SELECT count(*) FROM usage_records WHERE source_id = $1 AND sku = $2 AND window_start >= $3 AND window_start < $4`,
		sourceID, sku, w.From, w.To)
}

func TestReseedRemovesRowsOfSKUsTheModelNoLongerEmits(t *testing.T) {
	st, db := openDB(t)
	operatorBookID, _ := repairFixture(t, db)
	ctx := context.Background()

	landlord := addCustomer(t, db, synth.LandlordDefaultSlug, operatorBookID)
	backfill := addSource(t, db, landlord, synth.LandlordSourceName(synth.LandlordDefaultSlug), operatorBookID)
	real := addSource(t, db, landlord, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", operatorBookID)

	// The backfill window, and one day on either side of it.
	window := synth.Window{From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)}
	before := synth.Window{From: window.From.Add(-24 * time.Hour), To: window.From}
	after := synth.Window{From: window.To, To: window.To.Add(24 * time.Hour)}
	const (
		skuA = synth.EIPReservationSKU // the meter the old model emitted
		skuB = synth.EIPTrafficSKU     // the meter the new one emits
		fee  = "eip"                   // emitted by both
	)
	hours := int(window.To.Sub(window.From) / time.Hour)

	// First seed: the old model, A + fee, inside the window — and A rows the
	// window does not cover, on the same source, from some other run.
	seedThrough(t, st, landlord, backfill, window, skuA, fee)
	seedThrough(t, st, landlord, backfill, before, skuA)
	seedThrough(t, st, landlord, backfill, after, skuA)
	// The real source carries A too: the collector's rows, never this tool's.
	seedThrough(t, st, landlord, real, window, skuA)

	// Re-seed: the new model emits B + fee. The removal runs first, exactly
	// as applyLandlord does, then the write.
	next := seedThroughRecords(window, skuB, fee)
	stale, err := removeStaleSKUs(ctx, db, backfill, window, emittedSKUs(next))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := stale[skuA]; got != int64(hours) || len(stale) != 1 {
		t.Fatalf("removed %v, want exactly %d %s rows", stale, hours, skuA)
	}
	seedThrough(t, st, landlord, backfill, window, skuB, fee)

	if got := countSKU(t, db, backfill, skuA, window); got != 0 {
		t.Errorf("%d %s rows survive in the window on the backfill source; the re-seed must remove them", got, skuA)
	}
	if got := countSKU(t, db, backfill, fee, window); got != hours {
		t.Errorf("%d %s rows in the window, want all %d — a SKU both models emit is not stale", got, fee, hours)
	}
	if got := countSKU(t, db, backfill, skuB, window); got != hours {
		t.Errorf("%d %s rows in the window, want %d written by the re-seed", got, skuB, hours)
	}
	if got := countSKU(t, db, backfill, skuA, before) + countSKU(t, db, backfill, skuA, after); got != 48 {
		t.Errorf("%d %s rows outside the window, want all 48 untouched", got, skuA)
	}
	if got := countSKU(t, db, real, skuA, window); got != hours {
		t.Errorf("%d %s rows on the REAL source, want all %d — only the seeder-owned source is cleared", got, skuA, hours)
	}

	// A second pass over the same model removes nothing.
	again, err := removeStaleSKUs(ctx, db, backfill, window, emittedSKUs(next))
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.Total() != 0 {
		t.Errorf("second pass removed %v, want nothing", again)
	}
	// An empty model refuses to clear the source — that is the purge's job.
	none, err := removeStaleSKUs(ctx, db, backfill, window, nil)
	if err != nil {
		t.Fatalf("empty model: %v", err)
	}
	if none.Total() != 0 || countSKU(t, db, backfill, skuB, window) != hours {
		t.Errorf("an empty emitted set deleted rows: %v", none)
	}
}

// seedThroughRecords is the model's output without writing it.
func seedThroughRecords(w synth.Window, skus ...string) []synth.Record {
	var recs []synth.Record
	for at := w.From; at.Before(w.To); at = at.Add(time.Hour) {
		for _, sku := range skus {
			recs = append(recs, synth.Record{ResourceID: "eip-1", ResourceKind: "eip", SKU: sku, Unit: "hour", Quantity: 1, Start: at, End: at.Add(time.Hour)})
		}
	}
	return recs
}

func TestEmittedSKUsIsTheDistinctSortedSet(t *testing.T) {
	recs := []synth.Record{{SKU: "evs.ssd.gb"}, {SKU: "eip"}, {SKU: "evs.ssd.gb"}, {SKU: ""}, {SKU: "ecs.m7n.xlarge.8"}}
	got := emittedSKUs(recs)
	want := []string{"ecs.m7n.xlarge.8", "eip", "evs.ssd.gb"}
	if len(got) != len(want) {
		t.Fatalf("emitted = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emitted = %v, want %v", got, want)
		}
	}
	if s := (staleSKUs{"eip.bandwidth_mbps": 11268, "elb": 2}).String(); s != "removed 11268 eip.bandwidth_mbps, 2 elb row(s) the model no longer emits" {
		t.Fatalf("String() = %q", s)
	}
}
