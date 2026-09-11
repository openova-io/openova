package store

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Cost engine (#6867, DESIGN.md §3.1).
//
// Cost is computed at query time: usage_records joined, through its SOURCE,
// to the price book assigned to that source (DESIGN.md §2 — a book is
// assigned per source, never per customer, so a cloud book can only ever
// rate cloud SKUs and a platform book platform SKUs), with the book's
// stopped-instance policy applied exactly as the rating run applies it.
// There is no rollup table — a price change is visible immediately and the
// explorer can never disagree with a statement for the same window
// (TestIntegrationExploreReconcilesWithStatement pins this).
//
// The Sovereign's own platform footprint sits on the internal source
// (cost_sources.internal). Every customer-facing query here leaves it out;
// only Allocation opts in, through CostQuery.IncludeInternal.
//
// The sampled measurements (metric_skus.go — the CPU-utilisation sample and
// the observed traffic of a reservation-billed address) are metrics, not
// meters: they are excluded from every cost and usage aggregate here, as
// they are in rating.

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
	// The cloud's enterprise project (Huawei's cost-centre grouping), read
	// off labels.enterprise_project; records without one group as "(none)".
	"enterprise_project": {expr: "enterprise_project", label: "enterprise_project"},
	// The customer's OWN cost centre (DESIGN.md §19), resolved from the
	// per-resource override and then the tag rules; records neither names
	// group as "(unassigned)". It is a dimension like any other here, which
	// is what makes group-by, filtering and the CSV export work on it
	// without a second code path.
	CostCentreDimension: {expr: "cost_centre", label: "cost_centre_name"},
}

// CostDimensions lists the valid STATIC group_by / filter dimensions. Tag
// dimensions (`tag:<key>`) are dynamic — see IsTagDimension.
func CostDimensions() []string {
	out := make([]string, 0, len(costDims))
	for k := range costDims {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Tag dimensions (EPIC #6867 follow-up). Cloud consoles group and filter
// cost by resource tag; here the dimension is `tag:<key>`, where <key> is a
// tag key the collectors stored under labels.tags (cloud resource tags, or
// the app.kubernetes.io/* labels of a pod on the Sovereign's own cluster).
//
// The key is user input that ends up next to SQL. It is validated against
// tagKeyRE AND passed as a bind parameter — never interpolated — so a key
// that fails validation is refused before any SQL is built, and one that
// passes cannot carry a quote into the query text.

// TagDimensionPrefix introduces a tag dimension name.
const TagDimensionPrefix = "tag:"

// TagKeyRule is the validation rule a tag key must satisfy, in the words the
// API reports when a request fails it.
const TagKeyRule = "^[A-Za-z0-9_.:/@-]{1,128}$"

var tagKeyRE = regexp.MustCompile(TagKeyRule)

// TagUntagged is the group records without the tag fall into.
const TagUntagged = "(untagged)"

// IsTagDimension reports whether name is a `tag:<key>` dimension with a valid
// key, and returns the key. A `tag:` name with an invalid key is NOT a
// dimension (ok=false): the caller rejects it, it never reaches SQL.
func IsTagDimension(name string) (key string, ok bool) {
	key, found := strings.CutPrefix(name, TagDimensionPrefix)
	if !found || !tagKeyRE.MatchString(key) {
		return "", false
	}
	return key, true
}

// ValidTagKey reports whether key alone satisfies TagKeyRule.
func ValidTagKey(key string) bool { return tagKeyRE.MatchString(key) }

// tagExpr is the SQL for the value of one tag key on a tags jsonb column,
// with the key bound as a parameter: COALESCE(<col>->>$n, '(untagged)').
func tagExpr(a *costArgs, col, key string) string {
	return "COALESCE(" + col + "->>" + a.add(key) + ", '" + TagUntagged + "')"
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
	case "bandwidth":
		return "Shared bandwidth"
	case PlanKind:
		return "Subscription plan"
	case "":
		return "(none)"
	}
	return kind
}

// CostQuery selects a window, a grain, a grouping and filters.
type CostQuery struct {
	From, To    time.Time
	Granularity string // hour | day | month
	GroupBy     string // none | a costDims key
	Metric      string // cost | usage
	Include     map[string][]string
	Exclude     map[string][]string
	// CompareFrom/CompareTo is the window `previous` and `delta_pct` are
	// measured against. Both zero = the window of the same length
	// immediately before From (the automatic previous period). A custom
	// window may be any length; it is reported as-is in ExploreResult.Compare.
	CompareFrom, CompareTo time.Time
	// Limit keeps the top-N groups by total and folds the rest into Other.
	// 0 = every group.
	Limit int
	// CustomerID narrows to one customer (the customer-lens endpoints); the
	// scope confines it for non-operators regardless of what was asked.
	CustomerID string
	// CustomerIDs narrows to a SET of customers — a partner principal's
	// customers and party (DESIGN.md §13.5). Set by confine from the scope,
	// never by a caller's parameter. CustomerID is the one-element case of
	// the same predicate; customerSet resolves the two into the one set the
	// SQL filters on.
	CustomerIDs []string
	// IncludeInternal admits the Sovereign's own internal platform source
	// (cost_sources.internal), which every customer-facing query excludes.
	// Only Allocation sets it: the overhead row is that source's usage.
	IncludeInternal bool
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

// CostTotal is the window total against the compare window.
type CostTotal struct {
	Current   Decimal  `json:"current"`
	Previous  Decimal  `json:"previous"`
	DeltaPct  *float64 `json:"delta_pct"`
	Resources int      `json:"resources"`
}

// UnpricedSKU is usage that carries no rate in its source's price book — or,
// in ExploreResult.NotSoldPerUse, a platform meter the platform book
// deliberately does not price.
type UnpricedSKU struct {
	SKU       string  `json:"sku"`
	Unit      string  `json:"unit"`
	Quantity  Decimal `json:"quantity"`
	Resources int     `json:"resources"`
}

// CompareWindow is the half-open window every `previous` value in the result
// was summed over. Label is "previous period" for the automatic window of
// equal length before From, "custom" when the caller chose it.
type CompareWindow struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

const (
	CompareLabelPrevious = "previous period"
	CompareLabelCustom   = "custom"
)

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
	// NotSoldPerUse lists the k8s.* platform meters recorded on platform
	// sources whose book prices none of them: the allocation basis, not
	// unpriced revenue (DESIGN.md §2.5). Never in Unpriced.
	NotSoldPerUse []UnpricedSKU `json:"not_sold_per_use"`
	Compare       CompareWindow `json:"compare"`
	// Unconverted lists, per book currency without a rate, the priced
	// records that were left OUT of every total (DESIGN.md §3.10). Currency
	// above is always the reporting currency; MixedCurrency is true exactly
	// when this list is non-empty.
	Unconverted []UnconvertedCurrency `json:"unconverted"`
}

// costPriceJoinSQL joins a usage_records row aliased `u` to its source
// (`s`), the source's customer (`c`, NULL for the internal source), the
// SOURCE's price book (`b`), the book's rate for the SKU (`p`, NULL when
// unpriced) and the exchange rate of the book's currency (`x`, NULL when
// none is stored). costPricedExpr and costBaseExpr read those aliases.
// Callers add `AND NOT s.internal` (costExcludeInternalSQL) unless they mean
// to see the Sovereign's own footprint.
const costPriceJoinSQL = `
  JOIN cost_sources s ON s.id = u.source_id
  LEFT JOIN customers c ON c.id = s.customer_id
  LEFT JOIN price_books b ON b.id = s.price_book_id
  LEFT JOIN price_items p ON p.price_book_id = s.price_book_id AND p.sku = u.sku
  LEFT JOIN currency_rates x ON x.code = b.currency`

// costExcludeInternalSQL keeps the internal platform source out of a query
// built on costPriceJoinSQL.
const costExcludeInternalSQL = ` AND NOT s.internal`

// costNotSoldPerUseExpr is true for a record of the filtered CTE `f` that is
// a k8s.* platform meter on a platform source whose book prices NONE of the
// platform meters: "not sold per use" (the allocation basis), never
// "unpriced". Requires the CTE columns layer, book_id and sku.
const costNotSoldPerUseExpr = `(layer = 'platform' AND book_id IS NOT NULL
         AND sku IN (` + platformMeterSKUListSQL + `)
         AND NOT EXISTS (SELECT 1 FROM price_items pm WHERE pm.price_book_id = book_id AND pm.sku IN (` + platformMeterSKUListSQL + `)))`

// platformMeterSKUListSQL is PlatformMeterSKUs as a SQL literal list, so the
// "not sold per use" test and the pay-per-use rate card can never disagree
// about which meters are the platform meters.
const platformMeterSKUListSQL = `'` + SKUVCPU + `', '` + SKUMem + `', '` + SKUPVC + `'`

// costPricedExpr is the cost of ONE usage record after its source's
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

// costBaseExpr is costPricedExpr in the REPORTING currency: the record's
// cost divided by its book currency's per_base (currency.go). NULL when the
// record is unpriced, and NULL when the book currency has no rate — an
// unconverted record, which every aggregate here leaves out of its sum and
// reports through unconvertedSQL instead. Postgres numeric division keeps
// at least 16 significant digits, so the per-record quotients summed over
// a month agree with the exact rational total at the 6-decimal scale.
const costBaseExpr = `(` + costPricedExpr + `) / (` + costRateExpr + `)`

// costMeterFilter excludes the sampled measurements, which are metrics and
// never meters — the same exclusion the rating run applies, from the same
// one list (metric_skus.go).
const costMeterFilter = `u.` + metricSKUFilter

// costBaseSQL is the priced ledger: every record in the window with the unit
// price its SOURCE's book carries for the SKU (NULL = unpriced), the cost
// after the book's stopped-instance policy in the BOOK currency, and
// cost_base — the same cost in the reporting currency (NULL when the book
// currency has no rate) — plus the source's layer, book and internal flag.
// Placeholders $1/$2 are the window; the scope/filter clauses (and the
// internal-source exclusion) are appended by the builder.
const costBaseSQL = `
SELECT u.customer_id, COALESCE(c.slug, '') AS customer_slug, COALESCE(c.name, '') AS customer_name,
       u.source_id,
       COALESCE(NULLIF(s.project_id, ''), s.kind) AS source_label,
       s.layer, s.internal, s.price_book_id AS book_id,
       u.resource_id, u.resource_kind, u.sku, u.unit, u.region, u.window_start, u.quantity,
       CASE WHEN s.internal THEN 'platform-overhead' ELSE COALESCE(NULLIF(u.labels->>'tier', ''), 'organization') END AS tier,
       COALESCE(u.labels->>'namespace', '') AS namespace,
       COALESCE(NULLIF(u.labels->>'name', ''), u.resource_id) AS resource_label,
       u.labels->'tags' AS tags,
       COALESCE(NULLIF(u.labels->>'enterprise_project', ''), '(none)') AS enterprise_project,
       ` + costCentreCodeExpr + ` AS cost_centre,
       ` + costCentreNameExpr + ` AS cost_centre_name,
       p.unit_price,
       COALESCE(b.currency, '') AS currency,
       ` + costPricedExpr + ` AS cost,
       ` + costBaseExpr + ` AS cost_base
  FROM usage_records u` + costPriceJoinSQL + costCentreJoinSQL + `
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
	if !q.IncludeInternal {
		sb.WriteString(costExcludeInternalSQL)
	}
	if ids := q.customerSet(); ids != nil {
		sb.WriteString(" AND u.customer_id::text = ANY(" + a.add(pq.Array(ids)) + ")")
	}
	// Filters reference the base columns through the same expressions the
	// CTE projects, so include/exclude and group-by can never disagree on
	// what a dimension means.
	dimCol := map[string]string{
		"customer":           "u.customer_id::text",
		"source":             "u.source_id::text",
		"kind":               "u.resource_kind",
		"sku":                "u.sku",
		"region":             "u.region",
		"resource":           "u.resource_id",
		"tier":               "CASE WHEN s.internal THEN 'platform-overhead' ELSE COALESCE(NULLIF(u.labels->>'tier', ''), 'organization') END",
		"namespace":          "COALESCE(u.labels->>'namespace', '')",
		"enterprise_project": "COALESCE(NULLIF(u.labels->>'enterprise_project', ''), '(none)')",
		// The cost centre reads the aliases costCentreJoinSQL introduces,
		// which the CTE's FROM already carries — the same expression the
		// projection uses, so a filter and a group-by can never disagree
		// about which centre a record belongs to.
		CostCentreDimension: costCentreCodeExpr,
	}
	// column resolves a filter dimension to its expression; a tag dimension
	// binds its key as a parameter (never text in the query).
	column := func(dim string) (string, error) {
		if col, ok := dimCol[dim]; ok {
			return col, nil
		}
		if key, ok := IsTagDimension(dim); ok {
			return tagExpr(a, "u.labels->'tags'", key), nil
		}
		return "", fmt.Errorf("unknown dimension %q", dim)
	}
	// Deterministic clause order (map iteration is not), so two identical
	// queries build identical SQL — a prepared-statement cache would thank us.
	for _, dim := range sortedKeys(q.Include) {
		vals := q.Include[dim]
		col, err := column(dim)
		if err != nil {
			return "", nil, err
		}
		if len(vals) == 0 {
			continue
		}
		sb.WriteString(" AND " + col + " = ANY(" + a.add(pq.Array(vals)) + ")")
	}
	for _, dim := range sortedKeys(q.Exclude) {
		vals := q.Exclude[dim]
		col, err := column(dim)
		if err != nil {
			return "", nil, err
		}
		if len(vals) == 0 {
			continue
		}
		sb.WriteString(" AND NOT (" + col + " = ANY(" + a.add(pq.Array(vals)) + "))")
	}
	sb.WriteString(")")
	return sb.String(), a, nil
}

// Bucket label formats per grain. Hour buckets are `YYYY-MM-DDTHH` in UTC —
// the calendar date plus the 24-hour clock, sortable and unambiguous, and
// what bucketExpr's to_char produces for the same window_start.
const (
	bucketFormatHour  = "2006-01-02T15"
	bucketFormatDay   = "2006-01-02"
	bucketFormatMonth = "2006-01"
)

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// groupExprs resolves the group_by dimension to (group, label) expressions
// over the filtered CTE. "none"/"" groups everything into one row.
func groupExprs(a *costArgs, groupBy string) (groupExpr, labelExpr string, err error) {
	if groupBy == "none" || groupBy == "" {
		return "''", "''", nil
	}
	if d, ok := costDims[groupBy]; ok {
		return d.expr, d.label, nil
	}
	if key, ok := IsTagDimension(groupBy); ok {
		e := tagExpr(a, "tags", key)
		return e, e, nil
	}
	return "", "", fmt.Errorf("unknown group_by %q", groupBy)
}

func bucketExpr(granularity string) string {
	switch granularity {
	case "month":
		return "to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM')"
	case "hour":
		return `to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24')`
	}
	return "to_char(window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
}

// Buckets enumerates every bucket label in [from, to) at the grain, so the
// chart axis is uniform even where the ledger has no rows.
func Buckets(from, to time.Time, granularity string) []string {
	var out []string
	from, to = from.UTC(), to.UTC()
	switch granularity {
	case "month":
		t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
		for t.Before(to) {
			out = append(out, t.Format(bucketFormatMonth))
			t = t.AddDate(0, 1, 0)
		}
		return out
	case "hour":
		t := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), 0, 0, 0, time.UTC)
		for t.Before(to) {
			out = append(out, t.Format(bucketFormatHour))
			t = t.Add(time.Hour)
		}
		return out
	}
	t := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for t.Before(to) {
		out = append(out, t.Format(bucketFormatDay))
		t = t.AddDate(0, 0, 1)
	}
	return out
}

type costRow struct {
	bucket, key, label string
	cost, qty          Decimal
	resources          int
}

func (s *Store) queryCostRows(ctx context.Context, q CostQuery, from, to time.Time, withBucket bool) ([]costRow, error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return nil, err
	}
	groupExpr, labelExpr, err := groupExprs(a, q.GroupBy)
	if err != nil {
		return nil, err
	}
	bucket := "''"
	if withBucket {
		bucket = bucketExpr(q.Granularity)
	}
	// Money is summed as cost_base — the reporting currency — so a group
	// that spans two books in two currencies is one number, not a mix. The
	// sum is read at full precision: converted costs are quotients, and
	// rounding each bucket before adding them up would let a row's total
	// drift from the exact figure by a micro-unit per bucket. Explore
	// accumulates rationals and rounds once, on output.
	sqlText := cte + `
SELECT ` + bucket + ` AS bucket, ` + groupExpr + ` AS grp, min(` + labelExpr + `),
       COALESCE(sum(cost_base), 0)::text, sum(quantity)::text,
       count(DISTINCT resource_id)
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
		if err := rows.Scan(&r.bucket, &r.key, &r.label, &cost, &qty, &r.resources); err != nil {
			return nil, err
		}
		r.cost, r.qty = Decimal(cost), Decimal(qty)
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryUnpriced lists the SKUs in the window that carry no rate in their
// source's book, split into the genuinely unpriced and the platform meters
// a platform book deliberately leaves unpriced (not sold per use).
func (s *Store) queryUnpriced(ctx context.Context, q CostQuery, from, to time.Time) (unpriced, notSold []UnpricedSKU, err error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, cte+`
SELECT sku, unit, round(sum(quantity), 6)::text, count(DISTINCT resource_id), bool_or(`+costNotSoldPerUseExpr+`)
  FROM f WHERE unit_price IS NULL GROUP BY sku, unit ORDER BY sum(quantity) DESC`, a.args...)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	unpriced, notSold = []UnpricedSKU{}, []UnpricedSKU{}
	for rows.Next() {
		var u UnpricedSKU
		var qty string
		var ns bool
		if err := rows.Scan(&u.SKU, &u.Unit, &qty, &u.Resources, &ns); err != nil {
			return nil, nil, err
		}
		u.Quantity = Decimal(qty)
		if ns {
			notSold = append(notSold, u)
		} else {
			unpriced = append(unpriced, u)
		}
	}
	return unpriced, notSold, rows.Err()
}

// queryUnconverted lists, per book currency without a rate, the priced
// records of the window that no total includes.
func (s *Store) queryUnconverted(ctx context.Context, q CostQuery, from, to time.Time) ([]UnconvertedCurrency, error) {
	cte, a, err := filteredCTE(q, from, to)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, cte+unconvertedSQL, a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanUnconverted(rows)
}

// Explore aggregates cost (or usage) over the window, pivoted by bucket and
// group, with a compare window for `previous`: the caller's CompareFrom/To
// when set, else the window of the same length immediately before From.
//
// Every money figure is in the reporting currency (Currency); records whose
// book currency has no stored rate are excluded from every sum and listed
// in Unconverted, with MixedCurrency set — never silently added.
func (s *Store) Explore(ctx context.Context, scope Scope, q CostQuery) (ExploreResult, error) {
	// A customer principal sees its own rows; a partner principal the rows of
	// its customers, or of the one of them it named. Anything else is
	// ErrNotFound, never someone else's rows.
	if err := q.confine(scope); err != nil {
		return ExploreResult{}, err
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
	prevFrom, prevTo, compareLabel, err := compareWindow(q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}

	cur, err := s.queryCostRows(ctx, q, from, to, true)
	if err != nil {
		return ExploreResult{}, err
	}
	prev, err := s.queryCostRows(ctx, q, prevFrom, prevTo, false)
	if err != nil {
		return ExploreResult{}, err
	}
	unpriced, notSold, err := s.queryUnpriced(ctx, q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}
	unconverted, err := s.queryUnconverted(ctx, q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}
	reporting, err := s.ReportingCurrency(ctx)
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
		From: from.Format(bucketFormatDay), To: to.Format(bucketFormatDay),
		Granularity: q.Granularity, GroupBy: q.GroupBy, Metric: q.Metric,
		Currency: reporting, MixedCurrency: len(unconverted) > 0,
		Buckets: buckets, BucketHasData: make([]bool, len(buckets)),
		TotalsByBucket: make([]Decimal, len(buckets)),
		Unpriced:       unpriced,
		NotSoldPerUse:  notSold,
		Unconverted:    unconverted,
		Compare:        CompareWindow{From: prevFrom.Format(bucketFormatDay), To: prevTo.Format(bucketFormatDay), Label: compareLabel},
	}

	// Pivot: one accumulator per group key, values per bucket. Sums are
	// exact rationals until render, so every Decimal on the wire is the
	// exact figure rounded ONCE to the 6-decimal scale — the group total is
	// the rounded exact sum, not the sum of rounded buckets.
	type acc struct {
		key, label      string
		total, previous *big.Rat
		values          []*big.Rat
		resources       int
	}
	newAcc := func(key, label string) *acc {
		a := &acc{key: key, label: label, total: new(big.Rat), previous: new(big.Rat), values: make([]*big.Rat, len(buckets))}
		for i := range a.values {
			a.values[i] = new(big.Rat)
		}
		if q.GroupBy == "kind" {
			a.label = KindLabel(key)
		}
		return a
	}
	groups := map[string]*acc{}
	order := []string{}
	totalsByBucket := make([]*big.Rat, len(buckets))
	for i := range totalsByBucket {
		totalsByBucket[i] = new(big.Rat)
	}
	for _, r := range cur {
		g, ok := groups[r.key]
		if !ok {
			g = newAcc(r.key, r.label)
			groups[r.key] = g
			order = append(order, r.key)
		}
		i, ok := bucketIdx[r.bucket]
		if !ok {
			continue
		}
		v := ratOf(value(r))
		g.values[i].Add(g.values[i], v)
		g.total.Add(g.total, v)
		totalsByBucket[i].Add(totalsByBucket[i], v)
		res.BucketHasData[i] = true
		// Distinct resources per bucket summed over buckets over-counts a
		// resource alive on many days; take the max bucket count instead,
		// which is the number alive at the busiest moment of the window.
		if r.resources > g.resources {
			g.resources = r.resources
		}
	}
	for _, r := range prev {
		g, ok := groups[r.key]
		if !ok {
			// A group present last period and absent now still belongs in
			// the comparison: it is the "went to zero" row.
			g = newAcc(r.key, r.label)
			groups[r.key] = g
			order = append(order, r.key)
		}
		g.previous.Add(g.previous, ratOf(value(r)))
	}

	all := make([]*acc, 0, len(order))
	for _, k := range order {
		all = append(all, groups[k])
	}
	sort.SliceStable(all, func(i, j int) bool {
		if c := all[i].total.Cmp(all[j].total); c != 0 {
			return c > 0
		}
		return all[i].key < all[j].key
	})

	totalCur, totalPrev := new(big.Rat), new(big.Rat)
	for _, g := range all {
		totalCur.Add(totalCur, g.total)
		totalPrev.Add(totalPrev, g.previous)
	}
	for i := range res.TotalsByBucket {
		res.TotalsByBucket[i] = decOf(totalsByBucket[i])
	}

	// Distinct resources for the whole window, not the sum of per-group
	// maxima (a resource carries several SKUs and would be counted once per
	// SKU group).
	totalResources, err := s.countResources(ctx, q, from, to)
	if err != nil {
		return ExploreResult{}, err
	}
	res.Total = CostTotal{Current: decOf(totalCur), Previous: decOf(totalPrev), DeltaPct: deltaPctRat(totalCur, totalPrev), Resources: totalResources}

	// Top-N folds the tail into Other before anything is rounded.
	var other *acc
	if q.Limit > 0 && len(all) > q.Limit {
		other = &acc{key: "other", label: "Other", total: new(big.Rat), previous: new(big.Rat), values: make([]*big.Rat, len(buckets))}
		for i := range other.values {
			other.values[i] = new(big.Rat)
		}
		for _, g := range all[q.Limit:] {
			other.total.Add(other.total, g.total)
			other.previous.Add(other.previous, g.previous)
			other.resources += g.resources
			for i := range g.values {
				other.values[i].Add(other.values[i], g.values[i])
			}
		}
		all = all[:q.Limit]
	}
	render := func(a *acc) CostGroup {
		g := CostGroup{Key: a.key, Label: a.label, Total: decOf(a.total), Previous: decOf(a.previous), Resources: a.resources, Values: make([]Decimal, len(a.values))}
		for i, v := range a.values {
			g.Values[i] = decOf(v)
		}
		g.Share = shareRat(a.total, totalCur)
		g.DeltaPct = deltaPctRat(a.total, a.previous)
		return g
	}
	res.Groups = make([]CostGroup, 0, len(all))
	for _, a := range all {
		res.Groups = append(res.Groups, render(a))
	}
	if other != nil {
		o := render(other)
		res.Other = &o
	}
	return res, nil
}

// compareWindow resolves the window `previous` is summed over. With no
// CompareFrom/CompareTo it is the same-length window ending at from; a custom
// window must be complete (both ends) and non-empty, and may overlap the
// current window or differ in length — the caller asked for exactly that.
func compareWindow(q CostQuery, from, to time.Time) (time.Time, time.Time, string, error) {
	if q.CompareFrom.IsZero() && q.CompareTo.IsZero() {
		return from.Add(-to.Sub(from)), from, CompareLabelPrevious, nil
	}
	if q.CompareFrom.IsZero() || q.CompareTo.IsZero() {
		return time.Time{}, time.Time{}, "", fmt.Errorf("compare_from and compare_to must be given together")
	}
	cf, ct := q.CompareFrom.UTC(), q.CompareTo.UTC()
	if !ct.After(cf) {
		return time.Time{}, time.Time{}, "", fmt.Errorf("compare_from must be before compare_to")
	}
	return cf, ct, CompareLabelCustom, nil
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

// customerSet is the customer id set this query filters on — nil for an
// unfiltered operator read. CustomerID is the one-element case: a reader
// that names one customer and a partner scope that spans several go through
// the SAME `customer_id = ANY(...)` predicate, so there is one customer
// filter in the product and not one per kind of principal.
func (q CostQuery) customerSet() []string {
	if len(q.CustomerIDs) > 0 {
		return q.CustomerIDs
	}
	if q.CustomerID != "" {
		return []string{q.CustomerID}
	}
	return nil
}

// confine narrows a query to the customers the scope may read (Scope.Confine):
// the one customer of a customer principal, the set of a partner principal,
// every customer for the operator. A customer the query already named is
// INTERSECTED with the scope, never replaced by it — so a partner reading one
// of its own customers sees that customer alone, not its whole book.
func (q *CostQuery) confine(scope Scope) error {
	ids, err := scope.Confine(q.CustomerID)
	if err != nil {
		return err
	}
	q.CustomerID, q.CustomerIDs = "", ids
	return nil
}

// scopeCustomerClause is that same predicate for a scoped count that does not
// go through the explorer CTE: `col = ANY($1)` over Scope.Confine. An empty
// clause is the operator asking across every customer, and only that.
func scopeCustomerClause(scope Scope, customerID, col string) (string, []any, error) {
	ids, err := scope.Confine(customerID)
	if err != nil {
		return "", nil, err
	}
	if ids == nil {
		return "", nil, nil
	}
	return ` AND ` + col + `::text = ANY($1)`, []any{pq.Array(ids)}, nil
}

// LiveResourceCount counts inventory rows not marked deleted, inside the
// scope (optionally one customer).
func (s *Store) LiveResourceCount(ctx context.Context, scope Scope, customerID string) (int, error) {
	// A disabled source collects nothing more, so its inventory is history,
	// not live estate; its recorded cost still shows in the explorer.
	q := `SELECT count(*) FROM resource_inventory i JOIN cost_sources s ON s.id = i.source_id
		WHERE i.deleted_at IS NULL AND NOT s.internal AND s.status <> '` + StatusDisabled + `'`
	clause, args, err := scopeCustomerClause(scope, customerID, "s.customer_id")
	if err != nil {
		return 0, err
	}
	q += clause
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// LastCollectedAt is the newest collection time across the scope's sources
// (the internal source is not a customer's source and is left out).
func (s *Store) LastCollectedAt(ctx context.Context, scope Scope, customerID string) (*time.Time, error) {
	q := `SELECT max(last_collected_at) FROM cost_sources WHERE NOT internal`
	clause, args, err := scopeCustomerClause(scope, customerID, "customer_id")
	if err != nil {
		return nil, err
	}
	q += clause
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
// what the filter pickers offer. One query, one UNION per dimension. The
// static dimensions are always listed; a tag dimension is listed (under its
// `tag:<key>` name) when the query groups or filters by it.
func (s *Store) DimensionValues(ctx context.Context, scope Scope, q CostQuery) (map[string][]DimensionValue, error) {
	if err := q.confine(scope); err != nil {
		return nil, err
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
	out := map[string][]DimensionValue{}
	for dim := range costDims {
		out[dim] = []DimensionValue{}
	}
	for _, dim := range q.TagDimensions() {
		key, _ := IsTagDimension(dim)
		// Both the dimension name and the key are bound, not spliced.
		e := tagExpr(a, "tags", key)
		parts = append(parts, `SELECT `+a.add(dim)+`::text AS dim, `+e+` AS key, min(`+e+`) AS label FROM f GROUP BY 2`)
		out[dim] = []DimensionValue{}
	}
	rows, err := s.db.QueryContext(ctx, cte+" "+strings.Join(parts, " UNION ALL ")+" ORDER BY 1, 3", a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
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

// TagDimensions lists the distinct tag dimensions (`tag:<key>`, valid keys
// only) the query groups or filters by, sorted.
func (q CostQuery) TagDimensions() []string {
	seen := map[string]bool{}
	add := func(dim string) {
		if _, ok := IsTagDimension(dim); ok {
			seen[dim] = true
		}
	}
	add(q.GroupBy)
	for dim := range q.Include {
		add(dim)
	}
	for dim := range q.Exclude {
		add(dim)
	}
	out := make([]string, 0, len(seen))
	for dim := range seen {
		out = append(out, dim)
	}
	sort.Strings(out)
	return out
}

// TagKeys lists the distinct tag keys present on the records in the window
// (scoped and filtered like the explorer) — what the "group by tag" picker
// offers. A record whose tags label is not an object contributes nothing.
func (s *Store) TagKeys(ctx context.Context, scope Scope, q CostQuery) ([]string, error) {
	if err := q.confine(scope); err != nil {
		return nil, err
	}
	cte, a, err := filteredCTE(q, q.From.UTC(), q.To.UTC())
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, cte+`
SELECT DISTINCT k FROM f, jsonb_object_keys(CASE WHEN jsonb_typeof(f.tags) = 'object' THEN f.tags ELSE '{}'::jsonb END) k ORDER BY 1`, a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
