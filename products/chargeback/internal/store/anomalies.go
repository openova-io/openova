package store

import (
	"context"
	"fmt"
	"time"
)

// Anomaly inputs (#6867, DESIGN.md §3.6).
//
// Both readers sit on the same priced CTE as the explorer (filteredCTE +
// costBaseSQL) and sum cost_base — the reporting currency — so a flagged
// day's "actual" is exactly the number the explorer shows for that day,
// and the stopped-instance policy applies the same way. Unpriced and
// unconverted records are excluded: they have no cost to spike.

// DailyKindCost is one (day, customer, kind) cost total.
type DailyKindCost struct {
	Day          string
	CustomerID   string
	CustomerName string
	ResourceKind string
	Cost         Decimal
}

// DailyCostByCustomerKind returns the priced daily cost per (customer,
// resource kind) for window_start in [from, to), ordered by customer, kind,
// day. Days with no priced records for a pair are absent, not zero. The scope
// confines it exactly as it confines the explorer: one customer, a partner's
// set, or every customer for the operator. It is a CostBatch of one
// (costbatch.go); the overview queues it on the explorer's batch instead.
func (s *Store) DailyCostByCustomerKind(ctx context.Context, scope Scope, customerID string, from, to time.Time) ([]DailyKindCost, error) {
	b := s.NewCostBatch()
	res, err := b.DailyCostByCustomerKind(scope, customerID, from, to)
	if err != nil {
		return nil, err
	}
	if err := b.Run(ctx); err != nil {
		return nil, err
	}
	return *res, nil
}

// Driver is one SKU or resource whose change explains a flagged day.
type Driver struct {
	Kind  string  `json:"kind"` // sku | resource
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Delta Decimal `json:"delta"` // the day's cost − the mean daily cost of the 7 days before
}

const (
	driverLookbackDays = 7
	maxDrivers         = 5
)

// DayDrivers explains one (customer, kind, day): per SKU and per resource,
// the day's cost minus the mean daily cost over the 7 calendar days before
// it (a resource absent on a day cost nothing that day), the top 5 by
// absolute delta. Zero deltas explain nothing and are left out. It explains
// ONE customer, so the scope must resolve to exactly one: the operator and a
// partner both have to name it.
func (s *Store) DayDrivers(ctx context.Context, scope Scope, customerID, kind, day string) ([]Driver, error) {
	ids, err := scope.Confine(customerID)
	if err != nil {
		return nil, err
	}
	if len(ids) != 1 {
		return nil, fmt.Errorf("customer is required")
	}
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, fmt.Errorf("day must be YYYY-MM-DD: %w", err)
	}
	q := CostQuery{CustomerIDs: ids, Include: map[string][]string{"kind": {kind}}}
	cte, a, err := s.filteredCTE(ctx, q, d.AddDate(0, 0, -driverLookbackDays), d.AddDate(0, 0, 1), grainDay)
	if err != nil {
		return nil, err
	}
	dayArg := a.add(day)
	delta := fmt.Sprintf(`COALESCE(sum(cost) FILTER (WHERE day = %s), 0) - COALESCE(sum(cost) FILTER (WHERE day < %s), 0) / %d`, dayArg, dayArg, driverLookbackDays)
	rows, err := s.db.QueryContext(ctx, cte+`,
d AS (SELECT `+bucketExpr("day")+` AS day, sku, resource_id, min(resource_label) AS resource_label, sum(cost_base) AS cost
        FROM f WHERE cost_base IS NOT NULL GROUP BY 1, 2, 3),
x AS (SELECT 'sku' AS kind, sku AS key, sku AS label, `+delta+` AS delta FROM d GROUP BY sku
      UNION ALL
      SELECT 'resource', resource_id, min(resource_label), `+delta+` FROM d GROUP BY resource_id)
SELECT kind, key, label, round(delta, 6)::text FROM x WHERE round(delta, 6) <> 0
 ORDER BY abs(delta) DESC, kind, key LIMIT `+fmt.Sprint(maxDrivers), a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Driver{}
	for rows.Next() {
		var dr Driver
		var delta string
		if err := rows.Scan(&dr.Kind, &dr.Key, &dr.Label, &delta); err != nil {
			return nil, err
		}
		dr.Delta = Decimal(delta)
		out = append(out, dr)
	}
	return out, rows.Err()
}
