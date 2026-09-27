package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
)

// One read of the priced ledger for many questions (#6867).
//
// Explore answers one question with five aggregates — the bucketed rows of
// the window, the rows of the compare window, the unpriced SKUs, the
// unconverted currencies and the distinct-resource count. Before this file
// each aggregate was its own statement over its own copy of the priced CTE,
// each preceded by its own freshness read. The overview asks six such
// questions, every active budget one more and the anomaly detector its
// daily series: on hw307 that was 41 copies of `WITH u AS (...), f AS (...)`
// and 41 `SELECT day, count(*) ... FROM cost_rollup_state` per page — 3–8 s
// of database time for one document — though the 41 differed only in the
// window and in the bucket and group expressions.
//
// A CostBatch renders every aggregate as one branch of a UNION ALL over ONE
// `f`, built for the hull of the windows asked about. Postgres materialises
// a CTE that is referenced more than once, so the price join, the
// stopped-instance CASE, the currency division and the cost-centre LATERAL
// run once per statement instead of once per aggregate; each branch keeps
// its own window with `window_start >= $a AND window_start < $b`.
//
// That filter is exact because of two facts already true of the engine: `u`
// is aggregated to the UTC day (the hour only for the hourly chart) by the
// same expression the rollup is built with, and every window the engine is
// asked about is whole UTC days. For a midnight-aligned [a, b) a record's
// day (or hour) lies in [a, b) exactly when its window_start does, and every
// day of [a, b) is wholly inside it — so the rows a branch selects from the
// hull's `u` are the rows a `u` built for [a, b) alone would hold, from the
// same branch (rollup or live) of the same split, since the split is per day
// and the freshness read for the hull answers for every day inside it. A
// window that is not midnight-aligned gets a `u` of its own and no branch
// filter, which is what every window had before.
//
// Nothing about the arithmetic moved: each branch is the SELECT its
// statement used to be, over the same `f`, with the same GROUP BY, and the
// pivot in Go (assembleExplore) is the code Explore always ran. The order
// each statement relied on is restated once, on the whole result: bucket
// then group for the rows; sum(quantity) descending for the unpriced list;
// currency for the unconverted; customer, kind, day for the daily series.

// CostBatch collects explorer questions and answers them all in Run. Queue
// questions with Explore and DailyCostByCustomerKind — each returns the slot
// Run fills — then call Run once. Questions whose filters agree share one
// statement, so a Run is one freshness read plus one statement per distinct
// filter set, whatever the number of questions.
type CostBatch struct {
	s        *Store
	groups   map[string]*costGroup
	order    []string // group keys in the order they were first asked, so the SQL is deterministic
	explores []*batchExplore
	dailies  []*batchDaily
}

// NewCostBatch starts an empty batch.
func (s *Store) NewCostBatch() *CostBatch {
	return &CostBatch{s: s, groups: map[string]*costGroup{}}
}

// costGroup is one statement: the branches whose filters and grain agree,
// over one `f`. hull is the window `u` is built for. exact marks a group
// made of one window that is not midnight-aligned: `u` is built for exactly
// that window and its branches add no window filter.
type costGroup struct {
	q         CostQuery // the filters, from the first question to join
	grain     string
	hull      costRange
	exact     bool
	branches  []*costBranch
	byKey     map[string]*costBranch
	reporting string // the reporting currency, read by the same statement
}

type branchKind int

const (
	branchRows        branchKind = iota // bucket × group: cost, quantity, distinct resources
	branchUnpriced                      // SKUs without a rate, split into unpriced / not sold per use
	branchUnconverted                   // priced records whose book currency has no rate
	branchCount                         // distinct resources of the window
	branchDaily                         // the anomaly detector's (day, customer, kind) series
)

// costBranch is one aggregate over `f` for one window; rows is what came
// back, in the order the statement was asked for.
type costBranch struct {
	tag     string
	kind    branchKind
	win     costRange
	bucket  string // granularity of the bucket column; "" for a window total
	groupBy string
	rows    []batchRow
}

// batchRow is the uniform row every branch projects — three keys, a label,
// two numbers rendered as text, a count and a flag — so the UNION ALL types
// agree. Which of them a branch fills is fixed per kind (render).
type batchRow struct {
	k1, k2, k3, label, n1, n2 sql.NullString
	cnt                       sql.NullInt64
	flag                      sql.NullBool
}

// batchExplore is one queued Explore: its resolved windows and the five
// branches its document is assembled from.
type batchExplore struct {
	q                                       CostQuery
	from, to, prevFrom, prevTo              time.Time
	compareLabel                            string
	cur, prev, unpriced, unconverted, count *costBranch
	dst                                     *ExploreResult
}

// batchDaily is one queued DailyCostByCustomerKind.
type batchDaily struct {
	branch *costBranch
	dst    *[]DailyKindCost
}

// Explore queues one explorer question; the slot returned is filled by Run
// with the document Store.Explore returns. Errors here are the request's
// own — a scope that cannot see the customer, a bad window or group_by —
// and are reported before anything is asked of the database.
func (b *CostBatch) Explore(scope Scope, q CostQuery) (*ExploreResult, error) {
	// A customer principal sees its own rows; a partner principal the rows of
	// its customers, or of the one of them it named. Anything else is
	// ErrNotFound, never someone else's rows.
	if err := q.confine(scope); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("from must be before to")
	}
	if _, _, err := groupExprs(&costArgs{}, q.GroupBy); err != nil {
		return nil, err
	}
	from, to := q.From.UTC(), q.To.UTC()
	prevFrom, prevTo, compareLabel, err := compareWindow(q, from, to)
	if err != nil {
		return nil, err
	}
	e := &batchExplore{q: q, from: from, to: to, prevFrom: prevFrom, prevTo: prevTo, compareLabel: compareLabel, dst: &ExploreResult{}}
	// Only a bucketed read at hour grain needs the hourly ledger; a window
	// total (the compare window, the unpriced list, the resource count) is
	// the same number at either grain, so those ask for the day grain the
	// rollup can serve.
	grain := grainDay
	if q.Granularity == grainHour {
		grain = grainHour
	}
	e.cur = b.branch(q, grain, costBranch{kind: branchRows, win: costRange{from, to}, bucket: q.Granularity, groupBy: q.GroupBy})
	e.prev = b.branch(q, grainDay, costBranch{kind: branchRows, win: costRange{prevFrom, prevTo}, groupBy: q.GroupBy})
	e.unpriced = b.branch(q, grainDay, costBranch{kind: branchUnpriced, win: costRange{from, to}})
	e.unconverted = b.branch(q, grainDay, costBranch{kind: branchUnconverted, win: costRange{from, to}})
	e.count = b.branch(q, grainDay, costBranch{kind: branchCount, win: costRange{from, to}})
	b.explores = append(b.explores, e)
	return e.dst, nil
}

// DailyCostByCustomerKind queues the anomaly detector's series (anomalies.go);
// the slot returned is filled by Run. The scope confines it exactly as it
// confines the explorer, so on the overview it shares the explorer's `f`.
func (b *CostBatch) DailyCostByCustomerKind(scope Scope, customerID string, from, to time.Time) (*[]DailyKindCost, error) {
	ids, err := scope.Confine(customerID)
	if err != nil {
		return nil, err
	}
	if !to.After(from) {
		return nil, fmt.Errorf("from must be before to")
	}
	d := &batchDaily{dst: &[]DailyKindCost{}}
	d.branch = b.branch(CostQuery{CustomerIDs: ids}, grainDay, costBranch{kind: branchDaily, win: costRange{from.UTC(), to.UTC()}})
	b.dailies = append(b.dailies, d)
	return d.dst, nil
}

// branch files want under the group its filters, grain and window belong
// to, returning the branch already there when the same aggregate over the
// same window was asked before (the three month-to-date questions share one
// unpriced list, one unconverted list and one resource count).
func (b *CostBatch) branch(q CostQuery, grain string, want costBranch) *costBranch {
	g := b.group(q, grain, want.win)
	key := fmt.Sprintf("%d|%d|%d|%s|%s", want.kind, want.win.from.Unix(), want.win.to.Unix(), want.bucket, want.groupBy)
	if br, ok := g.byKey[key]; ok {
		return br
	}
	br := &costBranch{kind: want.kind, win: want.win, bucket: want.bucket, groupBy: want.groupBy}
	br.tag = fmt.Sprintf("b%d", len(g.branches))
	g.branches = append(g.branches, br)
	g.byKey[key] = br
	return br
}

// group is the statement a window of q at grain joins, widened to include
// the window; a window that is not whole UTC days gets a group of its own.
func (b *CostBatch) group(q CostQuery, grain string, win costRange) *costGroup {
	key := filterSig(q) + "|" + grain
	exact := !utcDay(win.from).Equal(win.from) || !utcDay(win.to).Equal(win.to)
	if exact {
		key += fmt.Sprintf("|exact|%d|%d", win.from.Unix(), win.to.Unix())
	}
	g, ok := b.groups[key]
	if !ok {
		g = &costGroup{q: q, grain: grain, hull: win, exact: exact, byKey: map[string]*costBranch{}}
		b.groups[key] = g
		b.order = append(b.order, key)
		return g
	}
	if win.from.Before(g.hull.from) {
		g.hull.from = win.from
	}
	if win.to.After(g.hull.to) {
		g.hull.to = win.to
	}
	return g
}

// filterSig names the clauses buildFilteredCTE renders for q — the customer
// set, the internal-source switch, the include and exclude filters — so
// questions that would build the same `f` share one.
func filterSig(q CostQuery) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "internal=%t;customers=%q;", q.IncludeInternal, q.customerSet())
	for _, dim := range sortedKeys(q.Include) {
		if vals := q.Include[dim]; len(vals) > 0 {
			fmt.Fprintf(&sb, "in:%s=%q;", dim, vals)
		}
	}
	for _, dim := range sortedKeys(q.Exclude) {
		if vals := q.Exclude[dim]; len(vals) > 0 {
			fmt.Fprintf(&sb, "ex:%s=%q;", dim, vals)
		}
	}
	return sb.String()
}

// Run answers every queued question: one freshness read over the hull of
// every day-grain window, then one statement per group, concurrently — the
// overview's questions share one, each customer-scoped budget has its own.
// An error is the whole batch's: nothing is filled.
func (b *CostBatch) Run(ctx context.Context) error {
	if len(b.order) == 0 {
		return nil
	}
	// The days the rollup may serve, read once for the hull of every group
	// that reads at day grain. splitWindow consults only the days inside the
	// window it splits, so the superset answers for each group exactly as a
	// read over that group's own hull would. Hour grain never reads the
	// rollup (costWindow), and neither does a batch with the rollup off.
	var covered map[int64]bool
	if b.s.CostRollupEnabled() {
		var hull costRange
		for _, key := range b.order {
			g := b.groups[key]
			if g.grain != grainDay {
				continue
			}
			if hull.to.IsZero() {
				hull = g.hull
				continue
			}
			if g.hull.from.Before(hull.from) {
				hull.from = g.hull.from
			}
			if g.hull.to.After(hull.to) {
				hull.to = g.hull.to
			}
		}
		if !hull.to.IsZero() {
			var err error
			if covered, err = b.s.coveredRollupDays(ctx, hull.from, hull.to); err != nil {
				return err
			}
		}
	}
	errs := make([]error, len(b.order))
	var wg sync.WaitGroup
	for i, key := range b.order {
		wg.Add(1)
		go func(i int, g *costGroup) {
			defer wg.Done()
			errs[i] = b.runGroup(ctx, g, covered)
		}(i, b.groups[key])
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	reporting := b.groups[b.order[0]].reporting
	for _, e := range b.explores {
		in := exploreInputs{reporting: reporting, cur: e.cur.costRows(), prev: e.prev.costRows(),
			unconverted: e.unconverted.unconvertedList(), totalResources: e.count.total()}
		in.unpriced, in.notSold = e.unpriced.unpricedLists()
		*e.dst = assembleExplore(e.q, e.from, e.to, e.prevFrom, e.prevTo, e.compareLabel, in)
	}
	for _, d := range b.dailies {
		*d.dst = d.branch.dailyList()
	}
	return nil
}

// runGroup renders and runs one group's statement and files each row under
// its branch.
func (b *CostBatch) runGroup(ctx context.Context, g *costGroup, covered map[int64]bool) error {
	w := liveWindow(g.hull.from, g.hull.to)
	if g.grain == grainDay && covered != nil {
		w = splitWindow(g.hull.from, g.hull.to, covered)
	}
	sqlText, args, err := renderGroup(g, w)
	if err != nil {
		return err
	}
	rows, err := b.s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	byTag := map[string]*costBranch{}
	for _, br := range g.branches {
		byTag[br.tag] = br
	}
	for rows.Next() {
		var tag string
		var r batchRow
		if err := rows.Scan(&tag, &r.k1, &r.k2, &r.k3, &r.label, &r.n1, &r.n2, &r.cnt, &r.flag); err != nil {
			return err
		}
		if tag == currencyTag {
			g.reporting = r.k1.String
			continue
		}
		br, ok := byTag[tag]
		if !ok {
			return fmt.Errorf("cost batch: unexpected tag %q", tag)
		}
		br.rows = append(br.rows, r)
	}
	return rows.Err()
}

// currencyTag marks the one-row branch that carries the reporting currency,
// so a batch needs no second statement for ReportingCurrency.
const currencyTag = "currency"

// renderGroup is the pure half of runGroup: the CTE for the group's window
// split, then every branch as one arm of a UNION ALL, ordered once for all
// of them. The bucket, group and label expressions are bound through the
// same costArgs as the filters, so a tag key stays a parameter here too.
func renderGroup(g *costGroup, w usageWindow) (string, []any, error) {
	cte, a, err := buildFilteredCTE(g.q, w, g.grain)
	if err != nil {
		return "", nil, err
	}
	parts := []string{`SELECT '` + currencyTag + `'::text AS tag, (` + reportingCurrencySQL + `)::text AS k1, NULL::text AS k2, NULL::text AS k3,
       NULL::text AS label, NULL::text AS n1, NULL::text AS n2, NULL::bigint AS cnt, NULL::boolean AS flag, NULL::numeric AS ord`}
	for _, br := range g.branches {
		parts = append(parts, br.render(a, g))
	}
	return cte + `
SELECT tag, k1, k2, k3, label, n1, n2, cnt, flag FROM (
` + strings.Join(parts, "\nUNION ALL\n") + `
) b ORDER BY tag, ord DESC NULLS LAST, k1, k2, k3`, a.args, nil
}

// render writes the branch as one SELECT over f: the aggregate its own
// statement used to run, with the window as a WHERE clause on
// window_start (none in an exact group), projected onto batchRow's columns.
// ord is the branch's own sort key, read by the statement's ORDER BY and
// never returned.
func (br *costBranch) render(a *costArgs, g *costGroup) string {
	var conds []string
	if !g.exact {
		conds = append(conds, "window_start >= "+a.add(br.win.from)+" AND window_start < "+a.add(br.win.to))
	}
	tag := "'" + br.tag + "'::text AS tag"
	switch br.kind {
	case branchRows:
		groupExpr, labelExpr, _ := groupExprs(a, br.groupBy) // validated when queued
		bucket := "''"
		if br.bucket != "" {
			bucket = bucketExpr(br.bucket)
		}
		// Money is summed as cost_base — the reporting currency — so a group
		// that spans two books in two currencies is one number, not a mix. The
		// sum is read at full precision: converted costs are quotients, and
		// rounding each bucket before adding them up would let a row's total
		// drift from the exact figure by a micro-unit per bucket.
		// assembleExplore accumulates rationals and rounds once, on output.
		return `SELECT ` + tag + `, ` + bucket + ` AS k1, ` + groupExpr + ` AS k2, NULL::text AS k3, min(` + labelExpr + `) AS label,
       COALESCE(sum(cost_base), 0)::text AS n1, sum(quantity)::text AS n2,
       count(DISTINCT resource_id) AS cnt, NULL::boolean AS flag, NULL::numeric AS ord
  FROM f` + whereSQL(conds) + `
 GROUP BY 2, 3`
	case branchUnpriced:
		conds = append(conds, "unit_price IS NULL")
		return `SELECT ` + tag + `, sku AS k1, unit AS k2, NULL::text AS k3, NULL::text AS label,
       round(sum(quantity), 6)::text AS n1, NULL::text AS n2,
       count(DISTINCT resource_id) AS cnt, bool_or(` + costNotSoldPerUseExpr + `) AS flag, sum(quantity) AS ord
  FROM f` + whereSQL(conds) + `
 GROUP BY sku, unit`
	case branchUnconverted:
		// cost IS NOT NULL keeps unpriced usage out (that is the unpriced
		// list's job); cost_base IS NULL is the missing rate. The count is
		// sum(records), not count(*): one row of f stands for the usage
		// records of a whole day at that grain (currency.go, unconvertedSQL).
		conds = append(conds, "cost IS NOT NULL AND cost_base IS NULL")
		return `SELECT ` + tag + `, currency AS k1, NULL::text AS k2, NULL::text AS k3, NULL::text AS label,
       round(sum(cost), 6)::text AS n1, NULL::text AS n2,
       sum(records)::bigint AS cnt, NULL::boolean AS flag, NULL::numeric AS ord
  FROM f` + whereSQL(conds) + `
 GROUP BY currency`
	case branchCount:
		return `SELECT ` + tag + `, NULL::text AS k1, NULL::text AS k2, NULL::text AS k3, NULL::text AS label,
       NULL::text AS n1, NULL::text AS n2,
       count(DISTINCT resource_id) AS cnt, NULL::boolean AS flag, NULL::numeric AS ord
  FROM f` + whereSQL(conds)
	default: // branchDaily
		conds = append(conds, "cost_base IS NOT NULL")
		return `SELECT ` + tag + `, customer_id::text AS k1, resource_kind AS k2, ` + bucketExpr("day") + ` AS k3, min(customer_name) AS label,
       COALESCE(round(sum(cost_base), 6), 0)::text AS n1, NULL::text AS n2,
       NULL::bigint AS cnt, NULL::boolean AS flag, NULL::numeric AS ord
  FROM f` + whereSQL(conds) + `
 GROUP BY 2, 3, 4`
	}
}

func whereSQL(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// The readers: each branch's rows back in the shape its statement scanned.

func (br *costBranch) costRows() []costRow {
	out := make([]costRow, 0, len(br.rows))
	for _, r := range br.rows {
		out = append(out, costRow{bucket: r.k1.String, key: r.k2.String, label: r.label.String,
			cost: Decimal(r.n1.String), qty: Decimal(r.n2.String), resources: int(r.cnt.Int64)})
	}
	return out
}

// unpricedLists splits the SKUs without a rate into the genuinely unpriced
// and the platform meters a platform book deliberately leaves unpriced
// (not sold per use).
func (br *costBranch) unpricedLists() (unpriced, notSold []UnpricedSKU) {
	unpriced, notSold = []UnpricedSKU{}, []UnpricedSKU{}
	for _, r := range br.rows {
		u := UnpricedSKU{SKU: r.k1.String, Unit: r.k2.String, Quantity: Decimal(r.n1.String), Resources: int(r.cnt.Int64)}
		if r.flag.Bool {
			notSold = append(notSold, u)
		} else {
			unpriced = append(unpriced, u)
		}
	}
	return unpriced, notSold
}

func (br *costBranch) unconvertedList() []UnconvertedCurrency {
	out := []UnconvertedCurrency{}
	for _, r := range br.rows {
		out = append(out, UnconvertedCurrency{Currency: r.k1.String, Records: int(r.cnt.Int64), Cost: Decimal(r.n1.String)})
	}
	return out
}

func (br *costBranch) total() int {
	if len(br.rows) == 0 {
		return 0
	}
	return int(br.rows[0].cnt.Int64)
}

func (br *costBranch) dailyList() []DailyKindCost {
	out := []DailyKindCost{}
	for _, r := range br.rows {
		out = append(out, DailyKindCost{Day: r.k3.String, CustomerID: r.k1.String, CustomerName: r.label.String,
			ResourceKind: r.k2.String, Cost: Decimal(r.n1.String)})
	}
	return out
}

// exploreInputs is what one explorer document is assembled from: the
// reporting currency and the answers of the window's branches.
type exploreInputs struct {
	reporting         string
	cur, prev         []costRow
	unpriced, notSold []UnpricedSKU
	unconverted       []UnconvertedCurrency
	totalResources    int
}

// assembleExplore pivots the branch answers into the explorer payload. It
// is the arithmetic Explore always ran, untouched: sums are exact rationals
// until render, so every Decimal on the wire is the exact figure rounded
// ONCE to the 6-decimal scale — the group total is the rounded exact sum,
// not the sum of rounded buckets.
func assembleExplore(q CostQuery, from, to, prevFrom, prevTo time.Time, compareLabel string, in exploreInputs) ExploreResult {
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
		Currency: in.reporting, MixedCurrency: len(in.unconverted) > 0,
		Buckets: buckets, BucketHasData: make([]bool, len(buckets)),
		TotalsByBucket: make([]Decimal, len(buckets)),
		Unpriced:       in.unpriced,
		NotSoldPerUse:  in.notSold,
		Unconverted:    in.unconverted,
		Compare:        CompareWindow{From: prevFrom.Format(bucketFormatDay), To: prevTo.Format(bucketFormatDay), Label: compareLabel},
	}

	// Pivot: one accumulator per group key, values per bucket.
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
	for _, r := range in.cur {
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
	for _, r := range in.prev {
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
	res.Total = CostTotal{Current: decOf(totalCur), Previous: decOf(totalPrev), DeltaPct: deltaPctRat(totalCur, totalPrev), Resources: in.totalResources}

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
	return res
}
