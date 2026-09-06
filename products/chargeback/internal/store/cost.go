package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Cost engine (#6867, DESIGN.md §3.1).
//
// Cost is computed at query time: usage_records joined to the customer's
// price book, with the book's stopped-instance policy applied exactly as the
// rating run applies it. There is no rollup table — a price change is visible
// immediately and the explorer can never disagree with a statement for the
// same window (TestIntegrationExploreReconcilesWithStatement pins this).
//
// The CPU-utilisation sample (ecs.cpu_util) is a metric, not a meter: it is
// excluded from every cost and usage aggregate here, as it is in rating.

// Explorer dimensions. The map key is the API name; expr is the column in the
// filtered CTE, label the display column.
type costDim struct {
	expr  string
	label string
}

var costDims = map[string]costDim{
	"customer":  {expr: "customer_id::text", label: "customer_name"},
	"source":    {expr: "source_id::text", label: "source_label"},
	"kind":      {expr: "resource_kind", label: "resource_kind"},
	"sku":       {expr: "sku", label: "sku"},
	"region":    {expr: "region", label: "region"},
	"resource":  {expr: "resource_id", label: "resource_label"},
	"tier":      {expr: "tier", label: "tier"},
	"namespace": {expr: "namespace", label: "namespace"},
}

// CostDimensions lists the valid group_by / filter dimensions.
func CostDimensions() []string {
	out := make([]string, 0, len(costDims))
	for k := range costDims {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KindLabel names a resource kind for people.
func KindLabel(kind string) string {
	switch kind {
	case "ecs":
		return "Elastic Cloud Server"
	case "evs":
		return "Block storage (EVS)"
	case "eip":
		return "Elastic IP"
	case "elb":
		return "Load balancer"
	case "nat":
		return "NAT gateway"
	case "vpc":
		return "VPC"
	case "rds":
		return "Relational DB (RDS)"
	case "dds":
		return "Document DB (DDS)"
	case "gaussdb":
		return "GaussDB"
	case "cbr":
		return "Backup (CBR)"
	case "cce":
		return "Kubernetes cluster (CCE)"
	case "ims":
		return "Images (IMS)"
	case "dns":
		return "DNS"
	case "waf":
		return "Web application firewall"
	case "as":
		return "Auto scaling"
	case "vpcep":
		return "VPC endpoint"
	case "k8s-pod":
		return "Kubernetes pods"
	case "k8s-pvc":
		return "Kubernetes volumes"
	case "":
		return "(none)"
	}
	return kind
}

// CostQuery selects a window, a grain, a grouping and filters.
type CostQuery struct {
	From, To    time.Time
	Granularity string // day | month
	GroupBy     string // none | a costDims key
	Metric      string // cost | usage
	Include     map[string][]string
	Exclude     map[string][]string
	// Limit keeps the top-N groups by total and folds the rest into Other.
	// 0 = every group.
	Limit int
	// CustomerID narrows to one customer (the customer-lens endpoints); the
	// scope forces it for non-operators regardless of what was asked.
	CustomerID string
}

// CostGroup is one line of the explorer table and one series of its chart.
type CostGroup struct {
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	Total     Decimal   `json:"total"`
	Previous  Decimal   `json:"previous"`
	DeltaPct  *float64  `json:"delta_pct"`
	Share     float64   `json:"share"`
	Resources int       `json:"resources"`
	Values    []Decimal `json:"values"`
}

// CostTotal is the window total against the previous window.
type CostTotal struct {
	Current   Decimal  `json:"current"`
	Previous  Decimal  `json:"previous"`
	DeltaPct  *float64 `json:"delta_pct"`
	Resources int      `json:"resources"`
}

// UnpricedSKU is usage that carries no rate in the customer's price book.
type UnpricedSKU struct {
	SKU       string  `json:"sku"`
	Unit      string  `json:"unit"`
	Quantity  Decimal `json:"quantity"`
	Resources int     `json:"resources"`
}

// ExploreResult is the explorer payload (DESIGN.md §3.1). Forecast is added
// by the API layer, which owns the calendar arithmetic.
type ExploreResult struct {
	From           string        `json:"from"`
	To             string        `json:"to"`
	Granularity    string        `json:"granularity"`
	GroupBy        string        `json:"group_by"`
	Metric         string        `json:"metric"`
	Currency       string        `json:"currency"`
	MixedCurrency  bool          `json:"mixed_currency"`
	Buckets        []string      `json:"buckets"`
	BucketHasData  []bool        `json:"bucket_has_data"`
	Groups         []CostGroup   `json:"groups"`
	Other          *CostGroup    `json:"other"`
	Total          CostTotal     `json:"total"`
	TotalsByBucket []Decimal     `json:"totals_by_bucket"`
	Unpriced       []UnpricedSKU `json:"unpriced"`
}

// costPriceJoinSQL joins a usage_records row aliased `u` to its customer
// (`c`), the customer's price book (`b`) and the book's rate for the SKU
// (`p`, NULL when unpriced). costPricedExpr reads those aliases.
const costPriceJoinSQL = `
  JOIN customers c ON c.id = u.customer_id
  LEFT JOIN price_books b ON b.id = c.price_book_id
  LEFT JOIN price_items p ON p.price_book_id = c.price_book_id AND p.sku = u.sku`

// costPricedExpr is the cost of ONE usage record after the customer's
// price book and its stopped-instance policy: NULL when the SKU carries no
// rate, 0 when the policy waives a stopped instance (or its volume), else
// quantity × unit price — exactly what the rating run charges.
//
// It is the single definition every cost surface uses (the explorer via
// costBaseSQL, the per-resource views in resources.go). Two copies of this
// CASE would be two bills that can disagree; factoring it is what makes
// "resource costs sum to the explorer total" a property instead of a hope.
// Requires the aliases costPriceJoinSQL introduces.
const costPricedExpr = `CASE
         WHEN p.unit_price IS NULL THEN NULL
         WHEN (upper(COALESCE(u.labels->>'status', '')) IN ('SHUTOFF','STOPPED','SHUTDOWN')
               OR upper(COALESCE(u.labels->>'server_status', '')) IN ('SHUTOFF','STOPPED','SHUTDOWN'))
              AND ((COALESCE(b.bill_stopped, 'compute') = 'none' AND (u.sku LIKE 'ecs.%' OR u.sku LIKE 'evs.%'))
                   OR (COALESCE(b.bill_stopped, 'compute') = 'storage-only' AND u.sku LIKE 'ecs.%'))
           THEN 0
         ELSE u.quantity * p.unit_price
       END`

// costMeterFilter excludes the CPU-utilisation sample, which is a metric and
// never a meter — the same exclusion the rating run applies.
const costMeterFilter = `u.sku <> 'ecs.cpu_util'`

// costBaseSQL is the priced ledger: every record in the window with the unit
// price its customer's book carries for the SKU (NULL = unpriced) and the
// cost after the book's stopped-instance policy. Placeholders $1/$2 are the
// window; the scope/filter clauses are appended by the builder.
const costBaseSQL = `
SELECT u.customer_id, c.slug AS customer_slug, c.name AS customer_name,
       u.source_id,
       COALESCE(NULLIF(s.project_id, ''), COALESCE(s.kind, '')) AS source_label,
       u.resource_id, u.resource_kind, u.sku, u.unit, u.region, u.window_start, u.quantity,
       COALESCE(NULLIF(u.labels->>'tier', ''), 'organization') AS tier,
       COALESCE(u.labels->>'namespace', '') AS namespace,
       COALESCE(NULLIF(u.labels->>'name', ''), u.resource_id) AS resource_label,
       p.unit_price,
       COALESCE(b.currency, '') AS currency,
       ` + costPricedExpr + ` AS cost
  FROM usage_records u` + costPriceJoinSQL + `
  LEFT JOIN cost_sources s ON s.id = u.source_id
 WHERE u.window_start >= $1 AND u.window_start < $2 AND ` + costMeterFilter

type costArgs struct{ args []any }

func (a *costArgs) add(v any) string {
	a.args = append(a.args, v)
	return fmt.Sprintf("$%d", len(a.args))
}

// filteredCTE builds `WITH f AS (<base> AND <scope> AND <filters>)`.
func filteredCTE(q CostQuery, from, to time.Time) (string, *costArgs, error) {
	a := &costArgs{}
	var sb strings.Builder
	sb.WriteString("WITH f AS (")
	sb.WriteString(costBaseSQL)
	a.add(from)
	a.add(to)
	if q.CustomerID != "" {
		sb.WriteString(" AND u.customer_id::text = " + a.add(q.CustomerID))
	}
	// Filters reference the base columns through the same expressions the
	// CTE projects, so include/exclude and group-by can never disagree on
	// what a dimension means.
	dimCol := map[string]string{
		"customer":  "u.customer_id::text",
		"source":    "u.source_id::text",
		"kind":      "u.resource_kind",
		"sku":       "u.sku",
		"region":    "u.region",
		"resource":  "u.resource_id",
		"tier":      "COALESCE(NULLIF(u.labels->>'tier', ''), 'organization')",
		"namespace": "COALESCE(u.labels->>'namespace', '')",
	}
	for dim, vals := range q.Include {
		col, ok := dimCol[dim]
		if !ok {
			return "", nil, fmt.Errorf("unknown dimension %q", dim)
		}
		if len(vals) == 0 {
			continue
		}
		sb.WriteString(" AND " + col + " = ANY(" + a.add(pq.Array(vals)) + ")")
	}
	for dim, vals := range q.Exclude {
		col, ok := dimCol[dim]
		if !ok {
			return "", nil, fmt.Errorf("unknown dimension %q", dim)
		}
		if len(vals) == 0 {
			continue
		}
		sb.WriteString(" AND NOT (" + col + " = ANY(" + a.add(pq.Array(vals)) + "))")
	}
	sb.WriteString(")")
	return sb.String(), a, nil
}

func bucketExpr(granularity string) string {
	if granularity == "month" {
		return "to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM')"
	}
	return "to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
}

// Buckets enumerates every bucket label in [from, to) at the grain, so the
// chart axis is uniform even where the ledger has no rows.
func Buckets(from, to time.Time, granularity string) []string {
	var out []string
	if granularity == "month" {
		t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
		for t.Before(to) {
			out = append(out, t.Format("2006-01"))
			t = t.AddDate(0, 1, 0)
		}
		return out
	}
	t := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for t.Before(to) {
		out = append(out, t.Format("2006-01-02"))
		t = t.AddDate(0, 0, 1)
	}
	return out
}

type costRow struct {
	bucket, key, label string
	cost, qty          Decimal
	resources          int
	currencies         string
}

func (s *Store) queryCostRows(ctx context.Context, q CostQuery, from, to time.Time, withBucket bool) ([]costRow, error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return nil, err
	}
	groupExpr, labelExpr := "''", "''"
	if q.GroupBy != "none" && q.GroupBy != "" {
		d, ok := costDims[q.GroupBy]
		if !ok {
			return nil, fmt.Errorf("unknown group_by %q", q.GroupBy)
		}
		groupExpr, labelExpr = d.expr, d.label
	}
	bucket := "''"
	if withBucket {
		bucket = bucketExpr(q.Granularity)
	}
	sqlText := cte + `
SELECT ` + bucket + ` AS bucket, ` + groupExpr + ` AS grp, min(` + labelExpr + `),
       COALESCE(round(sum(cost), 6), 0)::text, round(sum(quantity), 6)::text,
       count(DISTINCT resource_id),
       COALESCE(string_agg(DISTINCT currency, ',') FILTER (WHERE currency <> ''), '')
  FROM f GROUP BY 1, 2 ORDER BY 1, 2`
	rows, err := s.db.QueryContext(ctx, sqlText, a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []costRow
	for rows.Next() {
		var r costRow
		var cost, qty string
		if err := rows.Scan(&r.bucket, &r.key, &r.label, &cost, &qty, &r.resources, &r.currencies); err != nil {
			return nil, err
		}
		r.cost, r.qty = Decimal(cost), Decimal(qty)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) queryUnpriced(ctx context.Context, q CostQuery, from, to time.Time) ([]UnpricedSKU, error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, cte+`
SELECT sku, unit, round(sum(quantity), 6)::text, count(DISTINCT resource_id)
  FROM f WHERE unit_price IS NULL GROUP BY sku, unit ORDER BY sum(quantity) DESC`, a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UnpricedSKU{}
	for rows.Next() {
		var u UnpricedSKU
		var qty string
		if err := rows.Scan(&u.SKU, &u.Unit, &qty, &u.Resources); err != nil {
			return nil, err
		}
		u.Quantity = Decimal(qty)
		out = append(out, u)
	}
	return out, rows.Err()
}

// Explore aggregates cost (or usage) over the window, pivoted by bucket and
// group, with the previous window of the same length for comparison.
func (s *Store) Explore(ctx context.Context, scope Scope, q CostQuery) (ExploreResult, error) {
	if !scope.Operator {
		if scope.CustomerID == "" {
			return ExploreResult{}, ErrNotFound
		}
		// A customer principal sees its own rows whatever it asked for.
		q.CustomerID = scope.CustomerID
	}
	if q.Granularity == "" {
		q.Granularity = "day"
	}
	if q.GroupBy == "" {
		q.GroupBy = "none"
	}
	if q.Metric == "" {
		q.Metric = "cost"
	}
	if !q.To.After(q.From) {
		return ExploreResult{}, fmt.Errorf("from must be before to")
	}
	from, to := q.From.UTC(), q.To.UTC()
	prevFrom, prevTo := from.Add(-to.Sub(from)), from

	cur, err := s.queryCostRows(ctx, q, from, to, true)
	if err != nil {
		return ExploreResult{}, err
	}
	prev, err := s.queryCostRows(ctx, q, prevFrom, prevTo, false)
	if err != nil {
		return ExploreResult{}, err
	}
	unpriced, err := s.queryUnpriced(ctx, q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}

	value := func(r costRow) Decimal {
		if q.Metric == "usage" {
			return r.qty
		}
		return r.cost
	}

	buckets := Buckets(from, to, q.Granularity)
	bucketIdx := map[string]int{}
	for i, b := range buckets {
		bucketIdx[b] = i
	}
	res := ExploreResult{
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02"),
		Granularity: q.Granularity, GroupBy: q.GroupBy, Metric: q.Metric,
		Buckets: buckets, BucketHasData: make([]bool, len(buckets)),
		TotalsByBucket: make([]Decimal, len(buckets)),
		Unpriced:       unpriced,
	}
	for i := range res.TotalsByBucket {
		res.TotalsByBucket[i] = "0.000000"
	}

	// Pivot: one CostGroup per key, values per bucket.
	groups := map[string]*CostGroup{}
	order := []string{}
	currencies := map[string]bool{}
	resourcesByGroup := map[string]int{}
	for _, r := range cur {
		g, ok := groups[r.key]
		if !ok {
			g = &CostGroup{Key: r.key, Label: r.label, Total: "0.000000", Previous: "0.000000", Values: make([]Decimal, len(buckets))}
			for i := range g.Values {
				g.Values[i] = "0.000000"
			}
			if q.GroupBy == "kind" {
				g.Label = KindLabel(r.key)
			}
			groups[r.key] = g
			order = append(order, r.key)
		}
		i, ok := bucketIdx[r.bucket]
		if !ok {
			continue
		}
		v := value(r)
		g.Values[i] = addDec(g.Values[i], v)
		g.Total = addDec(g.Total, v)
		res.TotalsByBucket[i] = addDec(res.TotalsByBucket[i], v)
		res.BucketHasData[i] = true
		// Distinct resources per bucket summed over buckets over-counts a
		// resource alive on many days; take the max bucket count instead,
		// which is the number alive at the busiest moment of the window.
		if r.resources > resourcesByGroup[r.key] {
			resourcesByGroup[r.key] = r.resources
		}
		for _, c := range strings.Split(r.currencies, ",") {
			if c != "" {
				currencies[c] = true
			}
		}
	}
	for _, r := range prev {
		if g, ok := groups[r.key]; ok {
			g.Previous = addDec(g.Previous, value(r))
		} else {
			// A group present last period and absent now still belongs in
			// the comparison: it is the "went to zero" row.
			g := &CostGroup{Key: r.key, Label: r.label, Total: "0.000000", Previous: value(r), Values: make([]Decimal, len(buckets))}
			for i := range g.Values {
				g.Values[i] = "0.000000"
			}
			if q.GroupBy == "kind" {
				g.Label = KindLabel(r.key)
			}
			groups[r.key] = g
			order = append(order, r.key)
		}
	}
	for k, n := range resourcesByGroup {
		groups[k].Resources = n
	}

	all := make([]CostGroup, 0, len(order))
	for _, k := range order {
		all = append(all, *groups[k])
	}
	sort.SliceStable(all, func(i, j int) bool {
		ci, cj := ratOf(all[i].Total), ratOf(all[j].Total)
		if c := ci.Cmp(cj); c != 0 {
			return c > 0
		}
		return all[i].Key < all[j].Key
	})

	var totalCur, totalPrev Decimal = "0.000000", "0.000000"
	for _, g := range all {
		totalCur = addDec(totalCur, g.Total)
		totalPrev = addDec(totalPrev, g.Previous)
	}
	for i := range all {
		all[i].Share = shareOf(all[i].Total, totalCur)
		all[i].DeltaPct = deltaPct(all[i].Total, all[i].Previous)
	}

	// Distinct resources for the whole window, not the sum of per-group
	// maxima (a resource carries several SKUs and would be counted once per
	// SKU group).
	totalResources, err := s.countResources(ctx, q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}
	res.Total = CostTotal{Current: totalCur, Previous: totalPrev, DeltaPct: deltaPct(totalCur, totalPrev), Resources: totalResources}

	if q.Limit > 0 && len(all) > q.Limit {
		other := CostGroup{Key: "other", Label: "Other", Total: "0.000000", Previous: "0.000000", Values: make([]Decimal, len(buckets))}
		for i := range other.Values {
			other.Values[i] = "0.000000"
		}
		for _, g := range all[q.Limit:] {
			other.Total = addDec(other.Total, g.Total)
			other.Previous = addDec(other.Previous, g.Previous)
			other.Resources += g.Resources
			for i := range g.Values {
				other.Values[i] = addDec(other.Values[i], g.Values[i])
			}
		}
		other.Share = shareOf(other.Total, totalCur)
		other.DeltaPct = deltaPct(other.Total, other.Previous)
		all = all[:q.Limit]
		res.Other = &other
	}
	res.Groups = all
	if res.Groups == nil {
		res.Groups = []CostGroup{}
	}

	cl := make([]string, 0, len(currencies))
	for c := range currencies {
		cl = append(cl, c)
	}
	sort.Strings(cl)
	if len(cl) > 0 {
		res.Currency = cl[0]
	}
	res.MixedCurrency = len(cl) > 1
	return res, nil
}

func (s *Store) countResources(ctx context.Context, q CostQuery, from, to time.Time) (int, error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, cte+` SELECT count(DISTINCT resource_id) FROM f`, a.args...).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// LiveResourceCount counts inventory rows not marked deleted, inside the
// scope (optionally one customer).
func (s *Store) LiveResourceCount(ctx context.Context, scope Scope, customerID string) (int, error) {
	if !scope.Operator {
		customerID = scope.CustomerID
	}
	q := `SELECT count(*) FROM resource_inventory i JOIN cost_sources s ON s.id = i.source_id WHERE i.deleted_at IS NULL`
	var args []any
	if customerID != "" {
		q += ` AND s.customer_id::text = $1`
		args = append(args, customerID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// LastCollectedAt is the newest collection time across the scope's sources.
func (s *Store) LastCollectedAt(ctx context.Context, scope Scope, customerID string) (*time.Time, error) {
	if !scope.Operator {
		customerID = scope.CustomerID
	}
	q := `SELECT max(last_collected_at) FROM cost_sources`
	var args []any
	if customerID != "" {
		q += ` WHERE customer_id::text = $1`
		args = append(args, customerID)
	}
	var t pq.NullTime
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&t); err != nil {
		return nil, mapErr(err)
	}
	if !t.Valid {
		return nil, nil
	}
	v := t.Time.UTC()
	return &v, nil
}

// DimensionValue is one selectable value of an explorer dimension.
type DimensionValue struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// DimensionValues lists, per dimension, the values present in the window —
// what the filter pickers offer. One query, one UNION per dimension.
func (s *Store) DimensionValues(ctx context.Context, scope Scope, q CostQuery) (map[string][]DimensionValue, error) {
	if !scope.Operator {
		if scope.CustomerID == "" {
			return nil, ErrNotFound
		}
		q.CustomerID = scope.CustomerID
	}
	cte, a, err := filteredCTE(q, q.From.UTC(), q.To.UTC())
	if err != nil {
		return nil, err
	}
	var parts []string
	for dim, d := range costDims {
		parts = append(parts, fmt.Sprintf(`SELECT '%s' AS dim, %s AS key, min(%s) AS label FROM f GROUP BY 2`, dim, d.expr, d.label))
	}
	sort.Strings(parts)
	rows, err := s.db.QueryContext(ctx, cte+" "+strings.Join(parts, " UNION ALL ")+" ORDER BY 1, 3", a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string][]DimensionValue{}
	for dim := range costDims {
		out[dim] = []DimensionValue{}
	}
	for rows.Next() {
		var dim, key, label string
		if err := rows.Scan(&dim, &key, &label); err != nil {
			return nil, err
		}
		if dim == "kind" {
			label = KindLabel(key)
		}
		out[dim] = append(out[dim], DimensionValue{Key: key, Label: label})
	}
	return out, rows.Err()
}
