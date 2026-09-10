package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// Neutralising reservations the cloud never billed (founder direction
// 2026-09-10, after the 0.1.26 collector rolled on hw307).
//
// Until 0.1.26 the collector could not read an Elastic IP's charge mode, so
// it billed every address its reserved size — eip.bandwidth_mbps × hours —
// exactly as DESIGN.md §8.2 describes. On hw307 every one of the landlord's
// six billable addresses turned out to be bandwidth_charge_mode = traffic:
// the cloud bills their OUTBOUND GIGABYTES and reserves no pipe at all. The
// ≈494 OMR a day those rows added up to was therefore a charge the cloud
// never made, and on the chart it was a band through the whole series with
// a cliff at the hour the new collector stopped writing it.
//
// The rows are removed, not re-rated: there is nothing to re-rate them AS.
// The selection is deliberately narrow, and every clause is a refusal:
//
//   - only usage rows of the landlord customer being backfilled;
//   - only rows of an address (kind eip) whose CURRENT inventory row says
//     bandwidth_charge_mode = traffic. An address on charge mode `bandwidth`
//     really does reserve its pipe and keeps every row; an address whose
//     charge mode is absent — an older gateway that does not publish the
//     bandwidths API — is left exactly as it is, for the same reason the
//     collector keeps billing it: under-billing an address because its shape
//     is unknown is as wrong as over-billing one;
//   - only real rows: neither the address nor the row may carry the
//     synthetic mark, and the source may not be one this tool made. (The
//     backfill emits no reservation for a traffic-billed address in the first
//     place, so this clause guards a state that should not exist.)
//   - only eip.bandwidth_mbps. The address fee (eip) and the traffic meter
//     (eip.traffic_gb) are the cloud's real charges and stay.
//
// What was removed is written once to the landlord's audit trail — how many
// rows, over which window, on how many addresses, and why — as the product's
// own record of the correction. That entry carries NO synthetic mark, so a
// later --purge leaves it, and the purge does not bring the rows back either:
// the neutralisation corrects the REAL ledger and is deliberately not
// reversed by purge, because the rows were never billable. A statement the
// operator issued over those hours is a financial record and stands; its
// rated lines are not touched.

// neutraliseAction is the audit_log action the correction is recorded under.
const neutraliseAction = "eip.reservation.neutralise"

// neutralised is what one pass removed, and what it refused.
type neutralised struct {
	Rows      int64
	Addresses int64
	// From and To bound the removed rows, [From, To); zero when Rows is 0.
	From, To time.Time
	// LeftBandwidth and LeftUnknown count the customer's real addresses that
	// were refused: charge mode `bandwidth`, and no charge mode at all.
	LeftBandwidth int64
	LeftUnknown   int64
}

func (n neutralised) String() string {
	left := fmt.Sprintf("%d bandwidth-billed and %d unknown-mode address(es) left alone", n.LeftBandwidth, n.LeftUnknown)
	if n.Rows == 0 {
		return "no " + synth.EIPReservationSKU + " rows on a traffic-billed address; " + left
	}
	return fmt.Sprintf("%d %s row(s) removed over %s .. %s on %d traffic-billed address(es); %s",
		n.Rows, synth.EIPReservationSKU, n.From.Format(time.RFC3339), n.To.Format(time.RFC3339), n.Addresses, left)
}

// neutraliseReservations removes the eip.bandwidth_mbps usage rows of every
// real, traffic-billed address of the customer and records the removal on
// its audit trail, in one transaction. A pass that finds nothing writes
// nothing — not even an audit entry — so a nightly re-run does not stack
// entries.
func neutraliseReservations(ctx context.Context, db *sql.DB, customerID string) (neutralised, error) {
	var n neutralised
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return n, err
	}
	defer tx.Rollback()

	const realAddress = `i.kind = 'eip'
		  AND coalesce(i.attrs->>'` + synth.LabelKey + `', '') <> '` + synth.LabelValue + `'
		  AND (s.` + synth.SQLSourcePredicate + `) IS NOT TRUE`
	const mode = `coalesce(i.attrs->>'` + synth.EIPChargeModeAttr + `', '')`

	// The refusals, counted first so the log can name them.
	if err := tx.QueryRowContext(ctx, `SELECT
		  count(*) FILTER (WHERE `+mode+` = '`+synth.EIPChargeModeBandwidth+`'),
		  count(*) FILTER (WHERE `+mode+` NOT IN ('`+synth.EIPChargeModeTraffic+`', '`+synth.EIPChargeModeBandwidth+`'))
		FROM resource_inventory i JOIN cost_sources s ON s.id = i.source_id
		WHERE s.customer_id = $1 AND `+realAddress, customerID).Scan(&n.LeftBandwidth, &n.LeftUnknown); err != nil {
		return n, fmt.Errorf("count the addresses left alone: %w", err)
	}

	var from, to sql.NullTime
	if err := tx.QueryRowContext(ctx, `WITH gone AS (
		DELETE FROM usage_records u
		 USING resource_inventory i, cost_sources s
		 WHERE u.customer_id = $1
		   AND s.id = u.source_id
		   AND i.source_id = u.source_id AND i.resource_id = u.resource_id
		   AND `+realAddress+`
		   AND `+mode+` = '`+synth.EIPChargeModeTraffic+`'
		   AND coalesce(u.labels->>'`+synth.LabelKey+`', '') <> '`+synth.LabelValue+`'
		   AND u.sku = '`+synth.EIPReservationSKU+`'
		 RETURNING u.resource_id, u.window_start, u.window_end)
		SELECT count(*), count(DISTINCT resource_id), min(window_start), max(window_end) FROM gone`,
		customerID).Scan(&n.Rows, &n.Addresses, &from, &to); err != nil {
		return n, fmt.Errorf("remove the reservation rows: %w", err)
	}
	if n.Rows == 0 {
		return n, tx.Commit()
	}
	n.From, n.To = from.Time.UTC(), to.Time.UTC()

	details, err := json.Marshal(map[string]any{
		"sku":         synth.EIPReservationSKU,
		"rows":        n.Rows,
		"addresses":   n.Addresses,
		"from":        n.From.Format(time.RFC3339),
		"to":          n.To.Format(time.RFC3339),
		"charge_mode": synth.EIPChargeModeTraffic,
		"reason": "these addresses are billed by outbound traffic (" + synth.EIPChargeModeAttr + " = " + synth.EIPChargeModeTraffic +
			"); the reservation rows were recorded before the collector could read the charge mode and describe a charge the cloud never made",
	})
	if err != nil {
		return n, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (customer_id, actor, action, details) VALUES ($1, 'seed-history', $2, $3)`,
		customerID, neutraliseAction, details); err != nil {
		return n, fmt.Errorf("record the neutralisation: %w", err)
	}
	return n, tx.Commit()
}
