package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The neutralisation deletes rows from a REAL customer's ledger, so its
// refusals matter as much as its one permitted delete. Three addresses stand
// for the three charge modes the collector can record; a synthetic address
// that says traffic must be skipped because it is not real, and another real
// customer's traffic-billed address must be skipped because the operation is
// the landlord's. A second pass and a purge afterwards show what is
// idempotent and what is deliberately not reversed.

func addInventory(t *testing.T, db *sql.DB, sourceID, resourceID, attrs string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO resource_inventory (source_id, resource_id, kind, name, attrs, first_seen, last_seen)
		VALUES ($1, $2, 'eip', $2, $3::jsonb, '2026-09-02T10:00:00Z', '2026-09-10T12:00:00Z')`, sourceID, resourceID, attrs)
}

func addUsage(t *testing.T, db *sql.DB, customerID, sourceID, resourceID, sku, unit string, qty float64, at time.Time, labels string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, labels)
		VALUES ($1, $2, $3, 'eip', $4, $5, $6, $7, $8, $9::jsonb)`, customerID, sourceID, resourceID, sku, qty, unit, at, at.Add(time.Hour), labels)
}

func countUsage(t *testing.T, db *sql.DB, sourceID, resourceID, sku string) int {
	t.Helper()
	return scalar[int](t, db, `SELECT count(*) FROM usage_records WHERE source_id = $1 AND resource_id = $2 AND sku = $3`, sourceID, resourceID, sku)
}

func countAudits(t *testing.T, db *sql.DB, customerID string) int {
	t.Helper()
	return scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE customer_id = $1 AND actor = 'seed-history' AND action = $2`, customerID, neutraliseAction)
}

// TestNeutraliseRemovesOnlyTrafficBilledReservations — an address marked
// traffic loses its reservation rows and keeps its eip and eip.traffic_gb
// rows; an address marked bandwidth keeps everything; an address with no
// charge mode keeps everything; so do a synthetic address and another
// customer's. One audit entry records the removal, a second pass adds
// nothing, and a purge afterwards reverses none of it.
func TestNeutraliseRemovesOnlyTrafficBilledReservations(t *testing.T) {
	_, db := openDB(t)
	operatorBookID, _ := repairFixture(t, db)
	ctx := context.Background()

	landlord := addCustomer(t, db, synth.LandlordDefaultSlug, operatorBookID)
	real := addSource(t, db, landlord, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", operatorBookID)
	backfill := addSource(t, db, landlord, synth.LandlordSourceName(synth.LandlordDefaultSlug), operatorBookID)
	other := addCustomer(t, db, "acmewalk307", operatorBookID)
	otherSrc := addSource(t, db, other, "1b2c3d4e5f60718293a4b5c6d7e8f90a", operatorBookID)

	const (
		traffic   = `{"bandwidth_mbps": 300, "bandwidth_charge_mode": "traffic", "bandwidth_share_type": "PER", "status": "ACTIVE"}`
		bandwidth = `{"bandwidth_mbps": 100, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER", "status": "ACTIVE"}`
		unknown   = `{"bandwidth_mbps": 100, "status": "ACTIVE"}`
		synthetic = `{"bandwidth_mbps": 300, "bandwidth_charge_mode": "traffic", "bandwidth_share_type": "PER", "synthetic": "true"}`
	)
	addInventory(t, db, real, "eip-traffic", traffic)
	addInventory(t, db, real, "eip-bandwidth", bandwidth)
	addInventory(t, db, real, "eip-unknown", unknown)
	addInventory(t, db, backfill, "eip-synthetic", synthetic)
	addInventory(t, db, otherSrc, "eip-other", traffic)

	// Three hours of the three charges on every address, the way the ledger
	// looked once 0.1.26 had rolled over rows the old collector wrote: the
	// address fee, the reservation, and the traffic meter.
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	const hours = 3
	addAll := func(customerID, sourceID, resourceID, labels string) {
		for h := 0; h < hours; h++ {
			at := start.Add(time.Duration(h) * time.Hour)
			addUsage(t, db, customerID, sourceID, resourceID, "eip", "hour", 1, at, labels)
			addUsage(t, db, customerID, sourceID, resourceID, synth.EIPReservationSKU, "mbps-hour", 300, at, labels)
			addUsage(t, db, customerID, sourceID, resourceID, synth.EIPTrafficSKU, synth.EIPTrafficUnit, 0.1, at, labels)
		}
	}
	addAll(landlord, real, "eip-traffic", `{}`)
	addAll(landlord, real, "eip-bandwidth", `{}`)
	addAll(landlord, real, "eip-unknown", `{}`)
	addAll(landlord, backfill, "eip-synthetic", `{"synthetic": "true"}`)
	addAll(other, otherSrc, "eip-other", `{}`)

	n, err := neutraliseReservations(ctx, db, landlord)
	if err != nil {
		t.Fatalf("neutralise: %v", err)
	}
	if n.Rows != hours || n.Addresses != 1 {
		t.Fatalf("removed %d rows on %d addresses, want %d rows on exactly the one traffic-billed address: %s", n.Rows, n.Addresses, hours, n)
	}
	if end := start.Add(hours * time.Hour); !n.From.Equal(start) || !n.To.Equal(end) {
		t.Fatalf("removed window %s .. %s, want %s .. %s", n.From.Format(time.RFC3339), n.To.Format(time.RFC3339), start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	if n.LeftBandwidth != 1 || n.LeftUnknown != 1 {
		t.Fatalf("left alone %d bandwidth-billed and %d unknown-mode addresses, want 1 and 1", n.LeftBandwidth, n.LeftUnknown)
	}

	check := func(when string) {
		t.Helper()
		for _, w := range []struct {
			sourceID, id string
			reservation  int
			why          string
		}{
			{real, "eip-traffic", 0, "the cloud bills it by traffic"},
			{real, "eip-bandwidth", hours, "the cloud really reserves its pipe"},
			{real, "eip-unknown", hours, "its charge mode is unknown and it keeps billing as before"},
			{otherSrc, "eip-other", hours, "it belongs to another customer"},
		} {
			if got := countUsage(t, db, w.sourceID, w.id, synth.EIPReservationSKU); got != w.reservation {
				t.Errorf("%s: %s has %d reservation rows, want %d — %s", when, w.id, got, w.reservation, w.why)
			}
			if got := countUsage(t, db, w.sourceID, w.id, "eip"); got != hours {
				t.Errorf("%s: %s has %d address-fee rows, want all %d", when, w.id, got, hours)
			}
			if got := countUsage(t, db, w.sourceID, w.id, synth.EIPTrafficSKU); got != hours {
				t.Errorf("%s: %s has %d traffic rows, want all %d", when, w.id, got, hours)
			}
		}
	}
	check("after the neutralisation")
	if got := countUsage(t, db, backfill, "eip-synthetic", synth.EIPReservationSKU); got != hours {
		t.Errorf("the synthetic address lost %d reservation rows; only REAL rows are neutralised, synthetic ones belong to the purge", hours-got)
	}

	// The record of it: one entry, on the landlord, saying what and why,
	// carrying no synthetic mark.
	if got := countAudits(t, db, landlord); got != 1 {
		t.Fatalf("%d audit entries, want exactly one", got)
	}
	var rows, sku, mark sql.NullString
	if err := db.QueryRow(`SELECT details->>'rows', details->>'sku', details->>'synthetic' FROM audit_log WHERE customer_id = $1 AND action = $2`,
		landlord, neutraliseAction).Scan(&rows, &sku, &mark); err != nil {
		t.Fatalf("read the audit entry: %v", err)
	}
	if rows.String != "3" || sku.String != synth.EIPReservationSKU {
		t.Errorf("audit entry says rows=%q sku=%q, want 3 and %s", rows.String, sku.String, synth.EIPReservationSKU)
	}
	if mark.Valid {
		t.Errorf("the audit entry carries a synthetic mark (%q); a purge would remove the record of a real correction", mark.String)
	}

	// Idempotent: nothing left to remove, and no second entry.
	again, err := neutraliseReservations(ctx, db, landlord)
	if err != nil {
		t.Fatalf("second neutralise: %v", err)
	}
	if again.Rows != 0 || again.Addresses != 0 {
		t.Fatalf("a second pass removed %d rows on %d addresses", again.Rows, again.Addresses)
	}
	if again.LeftBandwidth != 1 || again.LeftUnknown != 1 {
		t.Fatalf("a second pass reports %d bandwidth-billed and %d unknown-mode addresses left alone, want 1 and 1", again.LeftBandwidth, again.LeftUnknown)
	}
	if got := countAudits(t, db, landlord); got != 1 {
		t.Fatalf("%d audit entries after a second pass, want still one", got)
	}

	// A purge afterwards removes exactly the synthetic rows — the backfill
	// address, its nine rows and its demo- source — and reverses nothing:
	// the real rows are not put back and the audit entry stays.
	counts, err := purge(ctx, db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.Usage != 3*hours || counts.Inventory != 1 || counts.Sources != 1 || counts.Customers != 0 {
		t.Fatalf("purge removed %d usage rows, %d inventory rows, %d sources, %d customers; want %d, 1, 1, 0",
			counts.Usage, counts.Inventory, counts.Sources, counts.Customers, 3*hours)
	}
	check("after the purge")
	if got := countAudits(t, db, landlord); got != 1 {
		t.Fatalf("the purge removed the neutralisation's audit entry (%d left); it is the record of a real correction", got)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM customers WHERE id = $1`, landlord); got != 1 {
		t.Fatal("the purge removed the landlord customer")
	}
}
