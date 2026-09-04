package store

import (
	"context"
	"time"
)

// AllocationRow is one line of the Sovereign allocation view (ADR-0014 D3
// case 3): either a tenant Organization's share or the platform-overhead
// line. Weight is the raw allocation basis; Share is that basis normalised
// across every row in the window, so the shares of one query sum to 1.
type AllocationRow struct {
	CustomerID   string  `json:"customer_id"`
	CustomerSlug string  `json:"customer_slug"`
	CustomerName string  `json:"customer_name"`
	Tier         string  `json:"tier"` // "organization" | "platform-overhead"
	VCPUHours    float64 `json:"vcpu_hours"`
	MemGiBHours  float64 `json:"mem_gib_hours"`
	PVCGBHours   float64 `json:"pvc_gb_hours"`
	Weight       float64 `json:"weight"`
	Share        float64 `json:"share"`
}

// Allocation returns the per-Organization + platform-overhead split of the
// Sovereign's platform consumption in [from, to).
//
// The weight is vCPU-hours + GiB-hours + PVC-GB-hours. That is deliberately a
// SINGLE declared basis rather than a per-resource cost model: the cloud bill
// this splits is one number, and a split whose basis is not stated is a
// number nobody can check. Callers multiply Share by the collected cloud cost
// to get each row's currency figure, so the rows always reconcile to the
// total — see AllocationSharesSumToOne, which is the property that makes the
// split trustworthy (#6850).
//
// Rows are tiered by the `tier` label the platform collector stamps: records
// carrying "platform-overhead" are the Sovereign's own footprint, everything
// else is tenant consumption.
func (s *Store) Allocation(ctx context.Context, scope Scope, from, to time.Time) ([]AllocationRow, error) {
	if !scope.Operator {
		return nil, ErrNotFound
	}
	const q = `
SELECT u.customer_id,
       c.slug,
       c.name,
       CASE WHEN COALESCE(u.labels->>'tier', '') = 'platform-overhead'
            THEN 'platform-overhead' ELSE 'organization' END AS tier,
       COALESCE(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.vcpu'),   0)::float8,
       COALESCE(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.mem_gb'), 0)::float8,
       COALESCE(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.pvc_gb'), 0)::float8
  FROM usage_records u
  JOIN customers c ON c.id = u.customer_id
 WHERE u.window_start >= $1 AND u.window_start < $2
   AND u.sku IN ('k8s.vcpu', 'k8s.mem_gb', 'k8s.pvc_gb')
 GROUP BY u.customer_id, c.slug, c.name, tier
 ORDER BY tier, c.slug`
	rows, err := s.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := []AllocationRow{}
	var total float64
	for rows.Next() {
		var r AllocationRow
		if err := rows.Scan(&r.CustomerID, &r.CustomerSlug, &r.CustomerName, &r.Tier,
			&r.VCPUHours, &r.MemGiBHours, &r.PVCGBHours); err != nil {
			return nil, err
		}
		r.Weight = r.VCPUHours + r.MemGiBHours + r.PVCGBHours
		total += r.Weight
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A zero total means nothing ran in the window. Leaving Share at 0 is
	// correct and honest: inventing an even split across rows would put
	// numbers on a screen that no measurement supports.
	if total > 0 {
		for i := range out {
			out[i].Share = out[i].Weight / total
		}
	}
	return out, nil
}
