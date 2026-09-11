package store

import (
	"context"
	"time"

	"github.com/lib/pq"
)

// Observed Elastic-IP traffic (#6867, DESIGN.md §8).
//
// The oversized-reservation rule needs one thing the inventory cannot give
// it: what actually went out through a pipe over a window, next to what that
// pipe reserved. Both traffic SKUs are read — the billable meter of a
// traffic-billed address and the never-rated metric of a reservation-billed
// one — because the question ("is this pipe far bigger than it needs to be?")
// is the same either way.

// SKUEIPTraffic is the BILLABLE outbound-traffic meter of a traffic-billed
// address or shared pipe (huawei.SKUEIPTraffic writes it; a test pins the
// two equal). It is a meter, not a metric: it is rated like any other line
// as soon as the operator puts a rate on it.
const SKUEIPTraffic = "eip.traffic_gb"

// EIPTrafficWindow is one address's — or one shared pipe's — outbound
// traffic over a window: how many hours were sampled, the total gigabytes,
// and the busiest single hour. The peak is what sizes a pipe: a reservation
// must cover the busiest hour, not the average one.
type EIPTrafficWindow struct {
	CustomerID string
	SourceID   string
	ResourceID string
	Kind       string
	Hours      int
	TotalGB    float64
	PeakGB     float64
}

// EIPTrafficWindows aggregates the hourly traffic samples per resource in
// [from, to). Traffic is a measurement, not money, so it is a float — the
// same rule CPUUtilMeans follows.
func (s *Store) EIPTrafficWindows(ctx context.Context, scope Scope, customerID string, from, to time.Time) ([]EIPTrafficWindow, error) {
	ids, err := scope.Confine(customerID)
	if err != nil {
		return nil, err
	}
	q := `SELECT customer_id::text, source_id::text, resource_id, resource_kind, count(*), sum(quantity)::float8, max(quantity)::float8
	        FROM usage_records
	       WHERE sku IN ('` + SKUEIPTraffic + `', '` + SKUEIPTrafficObserved + `')
	         AND customer_id IS NOT NULL
	         AND window_start >= $1 AND window_start < $2`
	args := []any{from.UTC(), to.UTC()}
	if ids != nil {
		q += ` AND customer_id::text = ANY($3)`
		args = append(args, pq.Array(ids))
	}
	q += ` GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []EIPTrafficWindow{}
	for rows.Next() {
		var t EIPTrafficWindow
		if err := rows.Scan(&t.CustomerID, &t.SourceID, &t.ResourceID, &t.Kind, &t.Hours, &t.TotalGB, &t.PeakGB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
