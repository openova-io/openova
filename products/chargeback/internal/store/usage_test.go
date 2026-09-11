package store

import (
	"testing"
	"time"
)

// A batch is ONE statement, and ON CONFLICT DO UPDATE refuses to touch a row
// twice in one command — so a call naming the same (source, resource, sku,
// window_start) twice has to collapse to one record before it is sent. The
// rule is LAST wins, because that is what the loop of single-row upserts it
// replaced did: each exec overwrote the one before.
func TestDedupeUsageKeepsTheLastRecordPerKey(t *testing.T) {
	at := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	recs := []UsageRecord{
		{SourceID: "s1", ResourceID: "r1", SKU: "ecs.x", WindowStart: at, Quantity: "1.000000"},
		{SourceID: "s1", ResourceID: "r2", SKU: "ecs.x", WindowStart: at, Quantity: "2.000000"},
		// The same key as the first, corrected: this is the one that counts.
		{SourceID: "s1", ResourceID: "r1", SKU: "ecs.x", WindowStart: at, Quantity: "9.000000"},
		// A different hour of the same resource is a different record.
		{SourceID: "s1", ResourceID: "r1", SKU: "ecs.x", WindowStart: at.Add(time.Hour), Quantity: "3.000000"},
		// A different source with an identical resource id is not a collision.
		{SourceID: "s2", ResourceID: "r1", SKU: "ecs.x", WindowStart: at, Quantity: "4.000000"},
	}
	got := dedupeUsage(recs)
	if len(got) != 4 {
		t.Fatalf("dedupe gave %d records: %+v", len(got), got)
	}
	// Order is the order of first appearance, with the winner in that slot.
	want := []Decimal{"9.000000", "2.000000", "3.000000", "4.000000"}
	for i, q := range want {
		if got[i].Quantity != q {
			t.Fatalf("record %d = %s, want %s (%+v)", i, got[i].Quantity, q, got)
		}
	}
	// The same instant in a different location is the same window.
	east := time.FixedZone("EAST", 4*3600)
	same := dedupeUsage([]UsageRecord{
		{SourceID: "s1", ResourceID: "r1", SKU: "ecs.x", WindowStart: at, Quantity: "1.000000"},
		{SourceID: "s1", ResourceID: "r1", SKU: "ecs.x", WindowStart: at.In(east), Quantity: "5.000000"},
	})
	if len(same) != 1 || same[0].Quantity != "5.000000" {
		t.Fatalf("the same instant in two zones is one record: %+v", same)
	}
	if len(dedupeUsage(nil)) != 0 {
		t.Fatal("dedupe of nothing is nothing")
	}
}
