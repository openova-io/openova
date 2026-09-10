package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// A re-seed is idempotent per (source, resource, sku, window_start): every
// row the model emits is upserted, and a row it emits no longer is simply
// not touched — so it survives. That is how 11,268 synthetic
// eip.bandwidth_mbps rows stayed on hw307 after the landlord model moved
// from a reservation meter to the traffic meter (DESIGN.md §8, "Traffic, not
// reservation"): the backfill source carried both the old SKU and the new,
// and the daily total for June to August was inflated by a charge the model
// had stopped describing.
//
// So before a seeder-owned source is written, every row of a SKU the current
// model does not emit is removed from THAT source inside THAT window. Only
// there: the source is this tool's (the landlord's backfill source or a
// showcase customer's demo- source — never a real cloud source, whose rows
// the collector owns), and rows outside the window belong to some other
// run's decision. What was removed is logged per SKU so the operator can see
// which meter went away and how many hours of it.
//
// An empty emitted set refuses to delete anything: a model that generates
// nothing has nothing to reconcile the ledger against, and clearing a source
// is the purge's job, not a side effect of an empty window.

// staleSKUs is what one pass removed, rows per SKU.
type staleSKUs map[string]int64

// removeStaleSKUs deletes, from one seeder-owned source and inside the
// window [w.From, w.To), every usage row whose SKU is not in emitted.
func removeStaleSKUs(ctx context.Context, db *sql.DB, sourceID string, w synth.Window, emitted []string) (staleSKUs, error) {
	out := staleSKUs{}
	if len(emitted) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `WITH gone AS (
		DELETE FROM usage_records
		 WHERE source_id = $1
		   AND window_start >= $2 AND window_start < $3
		   AND NOT (sku = ANY($4))
		 RETURNING sku)
		SELECT sku, count(*) FROM gone GROUP BY sku`,
		sourceID, w.From, w.To, pq.Array(emitted))
	if err != nil {
		return out, fmt.Errorf("remove the rows of SKUs the model no longer emits: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sku string
		var n int64
		if err := rows.Scan(&sku, &n); err != nil {
			return out, err
		}
		out[sku] = n
	}
	return out, rows.Err()
}

// emittedSKUs is the distinct SKU set of a generated ledger, sorted.
func emittedSKUs(recs []synth.Record) []string {
	seen := map[string]bool{}
	for _, r := range recs {
		if r.SKU != "" {
			seen[r.SKU] = true
		}
	}
	out := make([]string, 0, len(seen))
	for sku := range seen {
		out = append(out, sku)
	}
	sort.Strings(out)
	return out
}

// Total is the row count across SKUs.
func (s staleSKUs) Total() int64 {
	var n int64
	for _, c := range s {
		n += c
	}
	return n
}

// String lists the SKUs and counts, or says nothing was stale.
func (s staleSKUs) String() string {
	if len(s) == 0 {
		return "no rows of a SKU the model no longer emits"
	}
	skus := make([]string, 0, len(s))
	for sku := range s {
		skus = append(skus, sku)
	}
	sort.Strings(skus)
	parts := make([]string, 0, len(skus))
	for _, sku := range skus {
		parts = append(parts, fmt.Sprintf("%d %s", s[sku], sku))
	}
	return "removed " + strings.Join(parts, ", ") + " row(s) the model no longer emits"
}

// clearStaleSKUs runs the removal for one source ahead of its write and logs
// what went, one line per SKU.
func (s *seeder) clearStaleSKUs(slug, sourceID string, w synth.Window, recs []synth.Record) error {
	stale, err := removeStaleSKUs(s.ctx, s.db, sourceID, w, emittedSKUs(recs))
	if err != nil {
		return err
	}
	skus := make([]string, 0, len(stale))
	for sku := range stale {
		skus = append(skus, sku)
	}
	sort.Strings(skus)
	for _, sku := range skus {
		s.infof("  %s: removed %d %s row(s) the model no longer emits, %s .. %s",
			slug, stale[sku], sku, w.From.Format(time.RFC3339), w.To.Format(time.RFC3339))
	}
	return nil
}
