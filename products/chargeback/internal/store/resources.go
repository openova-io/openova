package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Resources (#6867, DESIGN.md §2.3 / §3.4): the inventory joined with what
// each resource cost in a window.
//
// Cost per resource is computed with costPricedExpr — the same expression
// the explorer and the rating run use — so a resource's cost is the slice
// of the bill it caused, and the resources of a customer sum to the
// customer's explorer total for the same window.

// ResourceQuery selects, filters, sorts and pages the resource list.
type ResourceQuery struct {
	From, To   time.Time
	CustomerID string
	Kind       string
	Region     string
	Status     string // live | stopped | deleted | all
	Q          string // matches name or resource_id, case-insensitive
	Sort       string // cost | name | kind | first_seen | last_seen
	Order      string // asc | desc
	Limit      int
	Offset     int
}

// ResourceLine is one (sku, unit) of a resource in the window.
type ResourceLine struct {
	SKU      string  `json:"sku"`
	Unit     string  `json:"unit"`
	Quantity Decimal `json:"quantity"`
	Cost     Decimal `json:"cost"`
}

// ResourceRow is one inventory row with its cost in the window.
type ResourceRow struct {
	SourceID     string          `json:"source_id"`
	ResourceID   string          `json:"resource_id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Region       string          `json:"region"`
	CustomerID   string          `json:"customer_id"`
	CustomerName string          `json:"customer_name"`
	Status       string          `json:"status"` // live | stopped | deleted
	FirstSeen    time.Time       `json:"first_seen"`
	LastSeen     time.Time       `json:"last_seen"`
	DeletedAt    *time.Time      `json:"deleted_at"`
	Cost         Decimal         `json:"cost"`
	Currency     string          `json:"currency"`
	Lines        []ResourceLine  `json:"lines"`
	Attrs        json.RawMessage `json:"attrs,omitempty"`
}

// ResourceList is a page of rows with the totals of the whole filtered set.
type ResourceList struct {
	Rows          []ResourceRow `json:"rows"`
	Total         int           `json:"total"`
	SumCost       Decimal       `json:"sum_cost"`
	Limit         int           `json:"limit"`
	Offset        int           `json:"offset"`
	Currency      string        `json:"currency"`
	MixedCurrency bool          `json:"mixed_currency"`
}

// DailyCost is one day of a resource's cost; HasData is false for days the
// ledger has no record of, so the chart can say "no data" instead of "0".
type DailyCost struct {
	Day     string  `json:"day"`
	Cost    Decimal `json:"cost"`
	HasData bool    `json:"has_data"`
}

// ResourceDetail is a row plus its daily series, attributes, the status /
// flavor transitions the collector recorded, and the newest raw records.
type ResourceDetail struct {
	ResourceRow
	From          string           `json:"from"`
	To            string           `json:"to"`
	Daily         []DailyCost      `json:"daily"`
	Transitions   []map[string]any `json:"transitions"`
	RecordsRecent []UsageRecord    `json:"records_recent"`
}

// ResourceSorts lists the accepted sort keys.
func ResourceSorts() []string { return []string{"cost", "name", "kind", "first_seen", "last_seen"} }

// ResourceStatuses lists the accepted status filters.
func ResourceStatuses() []string { return []string{"live", "stopped", "deleted", "all"} }

const (
	DefaultResourceLimit = 50
	MaxResourceLimit     = 500
)

// resourceCostCTE aggregates cost per resource in the window with the shared
// priced expression. $1/$2 are the window; an optional customer clause is
// appended by the caller before the GROUP BY.
const resourceCostCTE = `rc AS (
  SELECT u.source_id, u.resource_id,
         sum(` + costPricedExpr + `) AS cost,
         max(NULLIF(u.region, '')) AS region
    FROM usage_records u` + costPriceJoinSQL + `
   WHERE u.window_start >= $1 AND u.window_start < $2 AND ` + costMeterFilter

// resourceBaseCTE is every inventory row with its customer, region, status
// and window cost. Region comes from the source, else from the records.
const resourceBaseCTE = `base AS (
  SELECT i.source_id::text AS source_id, i.resource_id, i.kind, i.name,
         COALESCE(NULLIF(s.region, ''), rc.region, '') AS region,
         c.id::text AS customer_id, c.name AS customer_name,
         CASE WHEN i.deleted_at IS NOT NULL THEN 'deleted'
              WHEN upper(COALESCE(i.attrs->>'status', '')) IN ('SHUTOFF','STOPPED','SHUTDOWN')
                OR upper(COALESCE(i.attrs->>'server_status', '')) IN ('SHUTOFF','STOPPED','SHUTDOWN') THEN 'stopped'
              ELSE 'live' END AS status,
         i.first_seen, i.last_seen, i.deleted_at,
         COALESCE(rc.cost, 0) AS cost,
         COALESCE(b.currency, '') AS currency,
         i.attrs
    FROM resource_inventory i
    JOIN cost_sources s ON s.id = i.source_id
    JOIN customers c ON c.id = s.customer_id
    LEFT JOIN price_books b ON b.id = c.price_book_id
    LEFT JOIN rc ON rc.source_id = i.source_id AND rc.resource_id = i.resource_id
)`

// escapeLike makes q a literal substring pattern for ILIKE.
func escapeLike(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// resourceRowsSQL builds the CTEs and the filtered SELECT over base. The
// projection ends with the three window aggregates (total, sum_cost, the
// currency of the set) so one round-trip answers the page and its totals.
func resourceRowsSQL(q ResourceQuery) (string, []any, error) {
	a := &costArgs{}
	a.add(q.From)
	a.add(q.To)
	var sb strings.Builder
	sb.WriteString("WITH " + resourceCostCTE)
	if q.CustomerID != "" {
		sb.WriteString(" AND u.customer_id::text = " + a.add(q.CustomerID))
	}
	sb.WriteString(" GROUP BY u.source_id, u.resource_id), " + resourceBaseCTE)
	sb.WriteString(`
SELECT source_id, resource_id, kind, name, region, customer_id, customer_name, status,
       first_seen, last_seen, deleted_at, round(cost, 6)::text, currency, attrs,
       count(*) OVER (), round(COALESCE(sum(cost) OVER (), 0), 6)::text,
       COALESCE(min(NULLIF(currency, '')) OVER (), '') || ',' || COALESCE(max(NULLIF(currency, '')) OVER (), '')
  FROM base WHERE true`)
	if q.CustomerID != "" {
		sb.WriteString(" AND customer_id = " + a.add(q.CustomerID))
	}
	if q.Kind != "" {
		sb.WriteString(" AND kind = " + a.add(q.Kind))
	}
	if q.Region != "" {
		sb.WriteString(" AND region = " + a.add(q.Region))
	}
	switch q.Status {
	case "", "all":
	case "live", "stopped", "deleted":
		sb.WriteString(" AND status = " + a.add(q.Status))
	default:
		return "", nil, fmt.Errorf("status must be one of %s", strings.Join(ResourceStatuses(), ", "))
	}
	if strings.TrimSpace(q.Q) != "" {
		p := a.add(escapeLike(strings.TrimSpace(q.Q)))
		sb.WriteString(" AND (name ILIKE " + p + " OR resource_id ILIKE " + p + ")")
	}
	dir := "DESC"
	switch strings.ToLower(q.Order) {
	case "", "desc":
	case "asc":
		dir = "ASC"
	default:
		return "", nil, fmt.Errorf("order must be asc or desc")
	}
	// Every sort ends on (kind, name, resource_id) so paging is stable.
	switch q.Sort {
	case "", "cost":
		sb.WriteString(" ORDER BY cost " + dir + ", kind, lower(name), resource_id")
	case "name":
		sb.WriteString(" ORDER BY lower(name) " + dir + ", kind, resource_id")
	case "kind":
		sb.WriteString(" ORDER BY kind " + dir + ", lower(name), resource_id")
	case "first_seen":
		sb.WriteString(" ORDER BY first_seen " + dir + ", kind, lower(name), resource_id")
	case "last_seen":
		sb.WriteString(" ORDER BY last_seen " + dir + ", kind, lower(name), resource_id")
	default:
		return "", nil, fmt.Errorf("sort must be one of %s", strings.Join(ResourceSorts(), ", "))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultResourceLimit
	}
	if limit > MaxResourceLimit {
		limit = MaxResourceLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	sb.WriteString(" LIMIT " + a.add(limit) + " OFFSET " + a.add(offset))
	return sb.String(), a.args, nil
}

func scanResourceRow(rows *sql.Rows, withAttrs bool) (ResourceRow, int, string, string, error) {
	var r ResourceRow
	var deleted sql.NullTime
	var cost, sumCost, currencies string
	var attrs []byte
	var total int
	if err := rows.Scan(&r.SourceID, &r.ResourceID, &r.Kind, &r.Name, &r.Region, &r.CustomerID, &r.CustomerName, &r.Status,
		&r.FirstSeen, &r.LastSeen, &deleted, &cost, &r.Currency, &attrs, &total, &sumCost, &currencies); err != nil {
		return r, 0, "", "", err
	}
	r.FirstSeen, r.LastSeen = r.FirstSeen.UTC(), r.LastSeen.UTC()
	r.DeletedAt = timePtr(deleted)
	r.Cost = Decimal(cost)
	r.Lines = []ResourceLine{}
	if withAttrs {
		r.Attrs = attrs
	}
	return r, total, sumCost, currencies, nil
}

// ListResources returns a page of inventory rows with their window cost and
// per-SKU lines, plus the count and cost sum of the whole filtered set.
// A customer principal is confined to its own rows whatever it asked for.
func (s *Store) ListResources(ctx context.Context, scope Scope, q ResourceQuery) (ResourceList, error) {
	if !scope.Operator {
		if scope.CustomerID == "" {
			return ResourceList{}, ErrNotFound
		}
		q.CustomerID = scope.CustomerID
	}
	if !q.To.After(q.From) {
		return ResourceList{}, fmt.Errorf("from must be before to")
	}
	q.From, q.To = q.From.UTC(), q.To.UTC()
	sqlText, args, err := resourceRowsSQL(q)
	if err != nil {
		return ResourceList{}, err
	}
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return ResourceList{}, mapErr(err)
	}
	defer rows.Close()
	out := ResourceList{Rows: []ResourceRow{}, SumCost: "0.000000", Limit: q.Limit, Offset: q.Offset}
	if out.Limit <= 0 {
		out.Limit = DefaultResourceLimit
	}
	if out.Limit > MaxResourceLimit {
		out.Limit = MaxResourceLimit
	}
	if out.Offset < 0 {
		out.Offset = 0
	}
	var keys []string
	for rows.Next() {
		r, total, sumCost, currencies, err := scanResourceRow(rows, false)
		if err != nil {
			return ResourceList{}, err
		}
		out.Total, out.SumCost = total, Decimal(sumCost)
		// currencies is "min,max" over the filtered set: one currency when
		// they agree, mixed when they do not (a sum across currencies is
		// meaningless and the flag says so).
		if lo, hi, _ := strings.Cut(currencies, ","); lo != "" {
			out.Currency = lo
			out.MixedCurrency = hi != "" && hi != lo
		}
		out.Rows = append(out.Rows, r)
		keys = append(keys, r.SourceID+"/"+r.ResourceID)
	}
	if err := rows.Err(); err != nil {
		return ResourceList{}, err
	}
	if len(keys) > 0 {
		lines, err := s.resourceLines(ctx, q.From, q.To, keys)
		if err != nil {
			return ResourceList{}, err
		}
		for i := range out.Rows {
			if l, ok := lines[out.Rows[i].SourceID+"/"+out.Rows[i].ResourceID]; ok {
				out.Rows[i].Lines = l
			}
		}
	}
	return out, nil
}

// resourceLines aggregates (sku, unit) quantity and cost for the given
// "source_id/resource_id" keys. A source id is a UUID and never contains a
// slash, so the key is unambiguous even for resource ids that do.
func (s *Store) resourceLines(ctx context.Context, from, to time.Time, keys []string) (map[string][]ResourceLine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT u.source_id::text, u.resource_id, u.sku, u.unit, round(sum(u.quantity), 6)::text,
       round(COALESCE(sum(`+costPricedExpr+`), 0), 6)::text
  FROM usage_records u`+costPriceJoinSQL+`
 WHERE u.window_start >= $1 AND u.window_start < $2 AND `+costMeterFilter+`
   AND u.source_id::text || '/' || u.resource_id = ANY($3)
 GROUP BY u.source_id, u.resource_id, u.sku, u.unit`, from, to, pq.Array(keys))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string][]ResourceLine{}
	for rows.Next() {
		var src, res, qty, cost string
		var l ResourceLine
		if err := rows.Scan(&src, &res, &l.SKU, &l.Unit, &qty, &cost); err != nil {
			return nil, err
		}
		l.Quantity, l.Cost = Decimal(qty), Decimal(cost)
		out[src+"/"+res] = append(out[src+"/"+res], l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k := range out {
		ls := out[k]
		sort.SliceStable(ls, func(i, j int) bool {
			if c := ratOf(ls[i].Cost).Cmp(ratOf(ls[j].Cost)); c != 0 {
				return c > 0
			}
			return ls[i].SKU < ls[j].SKU
		})
	}
	return out, nil
}

// GetResource returns one resource with its daily cost series over
// [from, to), attributes, transitions and the newest 48 raw records. A row
// outside the scope reads as ErrNotFound so ids are not confirmed.
func (s *Store) GetResource(ctx context.Context, scope Scope, sourceID, resourceID string, from, to time.Time) (ResourceDetail, error) {
	if !scope.Operator && scope.CustomerID == "" {
		return ResourceDetail{}, ErrNotFound
	}
	if !to.After(from) {
		return ResourceDetail{}, fmt.Errorf("from must be before to")
	}
	from, to = from.UTC(), to.UTC()
	a := &costArgs{}
	a.add(from)
	a.add(to)
	sqlText := "WITH " + resourceCostCTE + " AND u.source_id::text = " + a.add(sourceID) + " AND u.resource_id = " + a.add(resourceID) +
		" GROUP BY u.source_id, u.resource_id), " + resourceBaseCTE + `
SELECT source_id, resource_id, kind, name, region, customer_id, customer_name, status,
       first_seen, last_seen, deleted_at, round(cost, 6)::text, currency, attrs,
       1, round(cost, 6)::text, currency
  FROM base WHERE source_id = ` + a.add(sourceID) + ` AND resource_id = ` + a.add(resourceID)
	rows, err := s.db.QueryContext(ctx, sqlText, a.args...)
	if err != nil {
		return ResourceDetail{}, mapErr(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ResourceDetail{}, err
		}
		return ResourceDetail{}, ErrNotFound
	}
	row, _, _, _, err := scanResourceRow(rows, true)
	if err != nil {
		return ResourceDetail{}, err
	}
	rows.Close()
	if !scope.Allows(row.CustomerID) {
		return ResourceDetail{}, ErrNotFound
	}
	d := ResourceDetail{ResourceRow: row, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Transitions: []map[string]any{}, RecordsRecent: []UsageRecord{}}

	key := row.SourceID + "/" + row.ResourceID
	lines, err := s.resourceLines(ctx, from, to, []string{key})
	if err != nil {
		return ResourceDetail{}, err
	}
	if l, ok := lines[key]; ok {
		d.Lines = l
	}

	// Daily series over uniform day buckets.
	daily := map[string]Decimal{}
	dr, err := s.db.QueryContext(ctx, `
SELECT to_char(u.window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD'), round(COALESCE(sum(`+costPricedExpr+`), 0), 6)::text
  FROM usage_records u`+costPriceJoinSQL+`
 WHERE u.source_id::text = $1 AND u.resource_id = $2 AND u.window_start >= $3 AND u.window_start < $4 AND `+costMeterFilter+`
 GROUP BY 1 ORDER BY 1`, sourceID, resourceID, from, to)
	if err != nil {
		return ResourceDetail{}, mapErr(err)
	}
	for dr.Next() {
		var day, cost string
		if err := dr.Scan(&day, &cost); err != nil {
			dr.Close()
			return ResourceDetail{}, err
		}
		daily[day] = Decimal(cost)
	}
	if err := dr.Err(); err != nil {
		dr.Close()
		return ResourceDetail{}, err
	}
	dr.Close()
	buckets := Buckets(from, to, "day")
	d.Daily = make([]DailyCost, 0, len(buckets))
	for _, b := range buckets {
		if v, ok := daily[b]; ok {
			d.Daily = append(d.Daily, DailyCost{Day: b, Cost: v, HasData: true})
		} else {
			d.Daily = append(d.Daily, DailyCost{Day: b, Cost: "0.000000"})
		}
	}

	// Transitions live inside attrs (the collector appends them there).
	if len(row.Attrs) > 0 {
		var attrs struct {
			Transitions []map[string]any `json:"transitions"`
		}
		if err := json.Unmarshal(row.Attrs, &attrs); err == nil && attrs.Transitions != nil {
			d.Transitions = attrs.Transitions
		}
	}

	recs, err := s.recentUsageRecords(ctx, sourceID, resourceID, 48)
	if err != nil {
		return ResourceDetail{}, err
	}
	d.RecordsRecent = recs
	return d, nil
}

// recentUsageRecords returns the newest n records of a resource, newest first.
func (s *Store) recentUsageRecords(ctx context.Context, sourceID, resourceID string, n int) ([]UsageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer_id, source_id, resource_id, resource_kind, sku, quantity::text, unit, window_start, window_end, region, labels, raw_ref, collected_at
		FROM usage_records WHERE source_id::text = $1 AND resource_id = $2 ORDER BY window_start DESC, sku LIMIT $3`, sourceID, resourceID, n)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UsageRecord{}
	for rows.Next() {
		var r UsageRecord
		var q string
		var labels []byte
		var rawRef sql.NullString
		if err := rows.Scan(&r.ID, &r.CustomerID, &r.SourceID, &r.ResourceID, &r.ResourceKind, &r.SKU, &q, &r.Unit, &r.WindowStart, &r.WindowEnd, &r.Region, &labels, &rawRef, &r.CollectedAt); err != nil {
			return nil, err
		}
		r.Quantity = Decimal(q)
		r.Labels = labels
		r.RawRef = rawRef.String
		r.WindowStart, r.WindowEnd, r.CollectedAt = r.WindowStart.UTC(), r.WindowEnd.UTC(), r.CollectedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
