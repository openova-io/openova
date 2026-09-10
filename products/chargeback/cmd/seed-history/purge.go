package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// purgeCounts is what one purge removed.
type purgeCounts struct {
	Usage      int64
	Inventory  int64
	RatedLines int64
	Statements int64
	Discounts  int64
	Budgets    int64
	Sources    int64
	Audit      int64
	Customers  int64
}

// purge removes everything seed-history created and nothing else.
//
// Every statement below is anchored on one of the four selectors in
// internal/synth/purge.go, which TestPurgeSelectorsMatchOnlySynthetic pins
// against real slugs and names taken from live databases. Two things are
// deliberately NOT purged:
//
//   - the price books. "National Cloud list 2026" and "OpenOva plans" are
//     shared with real customers — on hw307 the first one priced the real
//     August statement — and this command never re-prices or removes them.
//   - anything whose slug, name or label does not carry the mark, however
//     demo-looking it is. "demo", "demonstration" and "hw307-demo" all
//     survive, by test.
//   - the LANDLORD customer itself and everything of its own: its real cloud
//     source, its real usage, and any statement or rated line it has. The
//     backfill (landlord.go) writes only usage, inventory and one demo- source
//     onto a real customer, so that is exactly what comes back off. A
//     statement the operator issued over that window is a financial record and
//     stands; rated_lines.source_id is ON DELETE SET NULL, so removing the
//     backfill source cannot break one.
//
// Statements are deleted directly rather than through DELETE /customers/{id},
// which refuses while an issued statement exists. That guard protects real
// financial records; these are synthetic bills for customers that never
// existed, and leaving them would make the customer undeletable forever.
func purge(ctx context.Context, db *sql.DB) (purgeCounts, error) {
	var c purgeCounts
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()

	const synthCustomers = `SELECT id FROM customers WHERE ` + synth.SQLCustomerPredicate

	steps := []struct {
		name  string
		sql   string
		count *int64
	}{
		// Usage and inventory first: both carry the mark themselves, so a row
		// is removed even if it somehow hangs off a customer that does not.
		{"usage records", `DELETE FROM usage_records WHERE ` + synth.SQLUsagePredicate, &c.Usage},
		{"inventory", `DELETE FROM resource_inventory WHERE ` + synth.SQLInventoryPredicate, &c.Inventory},
		{"rated lines", `DELETE FROM rated_lines WHERE customer_id IN (` + synthCustomers + `)`, &c.RatedLines},
		{"statements", `DELETE FROM statements WHERE customer_id IN (` + synthCustomers + `)`, &c.Statements},
		// Discounts and budgets are matched by NAME so the global campaign,
		// which belongs to no customer, is removed too.
		{"discounts", `DELETE FROM discounts WHERE ` + synth.SQLNamePredicate, &c.Discounts},
		{"budgets", `DELETE FROM budgets WHERE ` + synth.SQLNamePredicate, &c.Budgets},
		// Sources are reached two ways. A showcase customer's sources go with
		// the customer. The LANDLORD backfill's source hangs off a REAL
		// customer, so it is reached by its own demo- name instead — without
		// that clause a purge would strand three months of synthetic rows on
		// a live ledger. `NOT internal` keeps the Sovereign's own platform
		// source out of reach whatever it is called.
		{"sources", `DELETE FROM cost_sources WHERE customer_id IN (` + synthCustomers + `)
			OR (NOT internal AND ` + synth.SQLSourcePredicate + `)`, &c.Sources},
		// audit_log.customer_id carries NO foreign key, so deleting the
		// customer does NOT cascade to it — measured: a purge left 111 audit
		// rows pointing at customers that no longer existed. They have to go
		// explicitly, and BEFORE the customers they are matched through.
		// The second clause catches rows orphaned by an earlier purge that
		// ran without this step, which can no longer be reached by join. The
		// third catches the entries for the GLOBAL campaign, which belongs to
		// no customer and so is reachable only by its name — `details->>'name'`
		// is the discount/budget name, never a customer slug, and a price-book
		// entry's name ("National Cloud list 2026") cannot match the prefix.
		{"audit entries", `DELETE FROM audit_log WHERE customer_id IN (` + synthCustomers + `)
			OR details->>'` + synth.LabelKey + `' = '` + synth.LabelValue + `'
			OR details->>'name' LIKE 'demo: _%'`, &c.Audit},
		// Last: the customers themselves, cascading users and invites.
		{"customers", `DELETE FROM customers WHERE ` + synth.SQLCustomerPredicate, &c.Customers},
	}
	for _, s := range steps {
		res, err := tx.ExecContext(ctx, s.sql)
		if err != nil {
			return c, fmt.Errorf("purge %s: %w", s.name, err)
		}
		n, _ := res.RowsAffected()
		*s.count = n
	}
	return c, tx.Commit()
}
