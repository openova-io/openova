package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Allocation (#6867, ADR-0014 D3 case 3, DESIGN.md §2.8 / §3.8).
//
// The Sovereign's cloud bill is one number; the platform SKUs
// (k8s.vcpu / k8s.mem_gb / k8s.pvc_gb) say who consumed the platform that
// bill paid for. Allocation splits the bill across those consumers:
//
//	weight  = vcpu_hours × w.vcpu + mem_gib_hours × w.mem_gib + pvc_gb_hours × w.pvc_gb
//	share   = weight / Σ weight            (the shares of one window sum to 1)
//	cost    = pool × share                 (exact, 6 decimals, Σ cost == pool)
//	margin  = rated revenue − cost         (what the Organization is billed, minus what it cost)
//
// Everything that used to be a constant here — the weights, what happens to
// the Sovereign's own footprint, where the pool comes from — is now a row in
// allocation_settings the operator edits. The basis is still ONE declared
// formula, because a split whose basis is not stated is a number nobody can
// check.

// ErrInvalid is returned for settings that fail validation. The message
// after the sentinel names the field.
var ErrInvalid = errors.New("invalid")

// AllocationWeights weights the three platform meters.
type AllocationWeights struct {
	VCPU   float64 `json:"vcpu"`
	MemGiB float64 `json:"mem_gib"`
	PVCGB  float64 `json:"pvc_gb"`
}

// AllocationSettings is the single editable row (id = 1).
type AllocationSettings struct {
	Weights        AllocationWeights `json:"weights"`
	OverheadPolicy string            `json:"overhead_policy"` // separate | distribute
	Pool           string            `json:"pool"`            // sovereign-cost | manual
	ManualAmount   Decimal           `json:"manual_amount"`
	Currency       string            `json:"currency"`
	// SovereignCustomerID names the customer whose rated cloud cost is the
	// pool. Nil = resolve it: the one customer with a verified huawei-project
	// source, when there is exactly one.
	SovereignCustomerID *string   `json:"sovereign_customer_id"`
	UpdatedAt           time.Time `json:"updated_at"`
}

const (
	OverheadSeparate   = "separate"
	OverheadDistribute = "distribute"
	PoolSovereignCost  = "sovereign-cost"
	PoolManual         = "manual"
)

var currencyShape = regexp.MustCompile(`^[A-Z]{3}$`)

// Normalize trims and upper-cases the enumerations and the currency.
func (a *AllocationSettings) Normalize() {
	a.OverheadPolicy = strings.ToLower(strings.TrimSpace(a.OverheadPolicy))
	a.Pool = strings.ToLower(strings.TrimSpace(a.Pool))
	a.Currency = strings.ToUpper(strings.TrimSpace(a.Currency))
	a.ManualAmount = Decimal(strings.TrimSpace(string(a.ManualAmount)))
	if a.ManualAmount == "" {
		a.ManualAmount = "0"
	}
	if a.SovereignCustomerID != nil {
		v := strings.TrimSpace(*a.SovereignCustomerID)
		if v == "" {
			a.SovereignCustomerID = nil
		} else {
			a.SovereignCustomerID = &v
		}
	}
}

// Validate reports the first rule the settings break, wrapped in ErrInvalid.
// Existence of SovereignCustomerID is checked by the store, not here.
func (a AllocationSettings) Validate() error {
	for _, w := range []struct {
		name string
		v    float64
	}{{"vcpu", a.Weights.VCPU}, {"mem_gib", a.Weights.MemGiB}, {"pvc_gb", a.Weights.PVCGB}} {
		if math.IsNaN(w.v) || math.IsInf(w.v, 0) || w.v < 0 {
			return fmt.Errorf("%w: weights.%s must be a finite number >= 0", ErrInvalid, w.name)
		}
	}
	if a.Weights.VCPU == 0 && a.Weights.MemGiB == 0 && a.Weights.PVCGB == 0 {
		return fmt.Errorf("%w: at least one weight must be > 0, or nothing can be allocated", ErrInvalid)
	}
	switch a.OverheadPolicy {
	case OverheadSeparate, OverheadDistribute:
	default:
		return fmt.Errorf("%w: overhead_policy must be separate or distribute", ErrInvalid)
	}
	switch a.Pool {
	case PoolSovereignCost, PoolManual:
	default:
		return fmt.Errorf("%w: pool must be sovereign-cost or manual", ErrInvalid)
	}
	if !decimalShape.MatchString(string(a.ManualAmount)) || ratOf(a.ManualAmount).Sign() < 0 {
		return fmt.Errorf("%w: manual_amount must be a number >= 0", ErrInvalid)
	}
	if !currencyShape.MatchString(a.Currency) {
		return fmt.Errorf("%w: currency must be a 3-letter code", ErrInvalid)
	}
	return nil
}

// DefaultAllocationSettings is the basis before an operator edits it: the
// three meters weighted equally, the Sovereign's footprint on its own line,
// the pool resolved from the Sovereign's rated cloud cost.
func DefaultAllocationSettings() AllocationSettings {
	return AllocationSettings{
		Weights:        AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1},
		OverheadPolicy: OverheadSeparate, Pool: PoolSovereignCost,
		ManualAmount: "0.000000", Currency: "OMR",
	}
}

// GetAllocationSettings reads the single settings row. A missing row (the
// migration seeds it, but a cascade from customers can remove it) reads as
// the defaults — a single-row configuration is never "not found".
func (s *Store) GetAllocationSettings(ctx context.Context) (AllocationSettings, error) {
	var a AllocationSettings
	var weights []byte
	var manual string
	var sov sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT weights, overhead_policy, pool, manual_amount::text, currency, sovereign_customer_id, updated_at
		FROM allocation_settings WHERE id = 1`).Scan(&weights, &a.OverheadPolicy, &a.Pool, &manual, &a.Currency, &sov, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultAllocationSettings(), nil
	}
	if err != nil {
		return a, mapErr(err)
	}
	if err := json.Unmarshal(weights, &a.Weights); err != nil {
		return a, fmt.Errorf("allocation weights: %w", err)
	}
	a.ManualAmount = Decimal(manual)
	a.SovereignCustomerID = strPtr(sov)
	a.UpdatedAt = a.UpdatedAt.UTC()
	return a, nil
}

// UpdateAllocationSettings validates and replaces the settings row. A
// SovereignCustomerID that names no customer returns ErrNotFound.
func (s *Store) UpdateAllocationSettings(ctx context.Context, in AllocationSettings) (AllocationSettings, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return AllocationSettings{}, err
	}
	if in.SovereignCustomerID != nil {
		if _, err := s.GetCustomer(ctx, OperatorScope, *in.SovereignCustomerID); err != nil {
			return AllocationSettings{}, err
		}
	}
	weights, err := json.Marshal(in.Weights)
	if err != nil {
		return AllocationSettings{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE allocation_settings SET weights = $1, overhead_policy = $2, pool = $3, manual_amount = $4,
		currency = $5, sovereign_customer_id = $6, updated_at = now() WHERE id = 1`,
		weights, in.OverheadPolicy, in.Pool, string(in.ManualAmount), in.Currency, nullStr(in.SovereignCustomerID))
	if err != nil {
		return AllocationSettings{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// The migration seeds the row; a missing one is a wiped table.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO allocation_settings (id, weights, overhead_policy, pool, manual_amount, currency, sovereign_customer_id)
			VALUES (1, $1, $2, $3, $4, $5, $6)`, weights, in.OverheadPolicy, in.Pool, string(in.ManualAmount), in.Currency, nullStr(in.SovereignCustomerID)); err != nil {
			return AllocationSettings{}, mapErr(err)
		}
	}
	return s.GetAllocationSettings(ctx)
}

// AllocationRow is one line of the allocation view: a tenant Organization's
// share or the platform-overhead line.
//
// Hours and weight are exact decimals; Share is a ratio (display-only float).
// AllocatedCost is the row's slice of the pool, RatedRevenue what the
// customer's price book rates its usage at in the same window, Margin the
// difference. MarginPct is nil when there is no revenue to divide by.
type AllocationRow struct {
	CustomerID    string   `json:"customer_id"`
	CustomerSlug  string   `json:"customer_slug"`
	CustomerName  string   `json:"customer_name"`
	Tier          string   `json:"tier"` // "organization" | "platform-overhead"
	VCPUHours     Decimal  `json:"vcpu_hours"`
	MemGiBHours   Decimal  `json:"mem_gib_hours"`
	PVCGBHours    Decimal  `json:"pvc_gb_hours"`
	Weight        Decimal  `json:"weight"`
	Share         float64  `json:"share"`
	AllocatedCost Decimal  `json:"allocated_cost"`
	RatedRevenue  Decimal  `json:"rated_revenue"`
	Margin        Decimal  `json:"margin"`
	MarginPct     *float64 `json:"margin_pct"`
}

// AllocationPool is where the money being split came from.
type AllocationPool struct {
	Source       string  `json:"source"` // sovereign-cost | manual | unresolved
	Amount       Decimal `json:"amount"`
	Currency     string  `json:"currency"`
	CustomerID   *string `json:"customer_id"`
	CustomerName *string `json:"customer_name,omitempty"`
	// Note explains an unresolved pool: what the operator has to set.
	Note string `json:"note,omitempty"`
}

// AllocationTotals sums the rows.
type AllocationTotals struct {
	Allocated Decimal `json:"allocated"`
	Revenue   Decimal `json:"revenue"`
	Margin    Decimal `json:"margin"`
}

// AllocationResult is the allocation document (DESIGN.md §3.8). The two
// counters at the end predate the money columns and stay for the page that
// reads them.
type AllocationResult struct {
	From             string             `json:"from"`
	To               string             `json:"to"`
	Settings         AllocationSettings `json:"settings"`
	Pool             AllocationPool     `json:"pool"`
	Rows             []AllocationRow    `json:"rows"`
	ShareTotal       float64            `json:"share_total"`
	Totals           AllocationTotals   `json:"totals"`
	OrganizationRows int                `json:"organization_rows"`
	PlatformOverhead int                `json:"platform_overhead"`
}

// PlatformSKUKinds are the resource kinds the platform collector writes.
// They are the BASIS of the split, so they are excluded from the pool — the
// pool is the cloud bill, never the consumption it is being split by.
var PlatformSKUKinds = []string{"k8s-pod", "k8s-pvc"}

// ratOfFloat converts a weight exactly from its shortest decimal form, so
// 0.1 weighs 1/10 and not the binary neighbour of it.
func ratOfFloat(f float64) *big.Rat {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'f', -1, 64))
	if !ok {
		return new(big.Rat)
	}
	return r
}

// splitAllocation applies the weights, the overhead policy and the pool to
// the raw basis rows. It is pure so the arithmetic can be pinned without a
// database (allocation_split_test.go).
//
// Under "distribute" the platform-overhead rows are dropped and the shares
// renormalised over what remains, which is how the Sovereign's own footprint
// ends up spread across the Organizations in proportion to their weights.
// Under "separate" it keeps its own line.
//
// Each row's cost is pool × share rounded to 6 decimals; the rounding
// residual (at most half a micro-unit per row) goes to the heaviest row, so
// the rows sum to the pool EXACTLY. A split that loses even a micro-unit
// between the bill and the rows is a split the operator cannot reconcile.
func splitAllocation(rows []AllocationRow, w AllocationWeights, policy string, pool *big.Rat) ([]AllocationRow, float64) {
	wv, wm, wp := ratOfFloat(w.VCPU), ratOfFloat(w.MemGiB), ratOfFloat(w.PVCGB)
	out := make([]AllocationRow, 0, len(rows))
	weights := make([]*big.Rat, 0, len(rows))
	total := new(big.Rat)
	for _, r := range rows {
		if policy == OverheadDistribute && r.Tier == "platform-overhead" {
			continue
		}
		wt := new(big.Rat).Mul(ratOf(r.VCPUHours), wv)
		wt.Add(wt, new(big.Rat).Mul(ratOf(r.MemGiBHours), wm))
		wt.Add(wt, new(big.Rat).Mul(ratOf(r.PVCGBHours), wp))
		r.Weight = decOf(wt)
		r.Share = 0
		r.AllocatedCost = "0.000000"
		out = append(out, r)
		weights = append(weights, wt)
		total.Add(total, wt)
	}
	// A zero total means nothing ran in the window. Leaving every share at 0
	// is correct and honest: inventing an even split would put numbers on a
	// screen that no measurement supports.
	if total.Sign() == 0 {
		return out, 0
	}
	shareSum := new(big.Rat)
	allocated := new(big.Rat)
	heaviest := 0
	for i := range out {
		share := new(big.Rat).Quo(weights[i], total)
		shareSum.Add(shareSum, share)
		out[i].Share, _ = share.Float64()
		cost := decOf(new(big.Rat).Mul(pool, share))
		out[i].AllocatedCost = cost
		allocated.Add(allocated, ratOf(cost))
		if weights[i].Cmp(weights[heaviest]) > 0 {
			heaviest = i
		}
	}
	if residual := new(big.Rat).Sub(ratOf(decOf(pool)), allocated); residual.Sign() != 0 {
		out[heaviest].AllocatedCost = decOf(new(big.Rat).Add(ratOf(out[heaviest].AllocatedCost), residual))
	}
	st, _ := shareSum.Float64()
	return out, st
}

// allocationBasis reads the platform meters per (customer, tier) in the window.
func (s *Store) allocationBasis(ctx context.Context, from, to time.Time) ([]AllocationRow, error) {
	const q = `
SELECT u.customer_id,
       c.slug,
       c.name,
       CASE WHEN COALESCE(u.labels->>'tier', '') = 'platform-overhead'
            THEN 'platform-overhead' ELSE 'organization' END AS tier,
       COALESCE(round(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.vcpu'), 6),   0)::text,
       COALESCE(round(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.mem_gb'), 6), 0)::text,
       COALESCE(round(sum(u.quantity) FILTER (WHERE u.sku = 'k8s.pvc_gb'), 6), 0)::text
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
	for rows.Next() {
		var r AllocationRow
		var v, m, p string
		if err := rows.Scan(&r.CustomerID, &r.CustomerSlug, &r.CustomerName, &r.Tier, &v, &m, &p); err != nil {
			return nil, err
		}
		r.VCPUHours, r.MemGiBHours, r.PVCGBHours = Decimal(v), Decimal(m), Decimal(p)
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolveSovereignCustomer finds the pool customer when the settings do not
// name one: the single customer holding a verified huawei-project source.
// Zero or several candidates is "unresolved" with a note saying what to set;
// guessing among several would bill the wrong Sovereign's footprint.
func (s *Store) resolveSovereignCustomer(ctx context.Context) (id, name, note string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name FROM customers c
		WHERE EXISTS (SELECT 1 FROM cost_sources s WHERE s.customer_id = c.id AND s.kind = 'huawei-project' AND s.status = 'verified')
		ORDER BY c.slug`)
	if err != nil {
		return "", "", "", mapErr(err)
	}
	defer rows.Close()
	var ids, names []string
	for rows.Next() {
		var i, n string
		if err := rows.Scan(&i, &n); err != nil {
			return "", "", "", err
		}
		ids, names = append(ids, i), append(names, n)
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}
	switch len(ids) {
	case 1:
		return ids[0], names[0], "", nil
	case 0:
		return "", "", "No customer has a verified huawei-project source, so the Sovereign's cloud cost cannot be found. Set sovereign_customer_id in the allocation settings, or set pool to manual with a manual_amount.", nil
	default:
		return "", "", fmt.Sprintf("%d customers have verified huawei-project sources (%s); set sovereign_customer_id in the allocation settings to say which one is the Sovereign.", len(ids), strings.Join(names, ", ")), nil
	}
}

// Allocation returns the per-Organization + platform-overhead split of the
// Sovereign's cloud cost in [from, to), per the stored settings.
//
// Rows are tiered by the `tier` label the platform collector stamps: records
// carrying "platform-overhead" are the Sovereign's own footprint, everything
// else is tenant consumption.
func (s *Store) Allocation(ctx context.Context, scope Scope, from, to time.Time) (AllocationResult, error) {
	if !scope.Operator {
		return AllocationResult{}, ErrNotFound
	}
	if !to.After(from) {
		return AllocationResult{}, fmt.Errorf("from must be before to")
	}
	from, to = from.UTC(), to.UTC()
	settings, err := s.GetAllocationSettings(ctx)
	if err != nil {
		return AllocationResult{}, err
	}
	res := AllocationResult{
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02"),
		Settings: settings,
		Pool:     AllocationPool{Source: settings.Pool, Amount: "0.000000", Currency: settings.Currency},
		Rows:     []AllocationRow{},
		Totals:   AllocationTotals{Allocated: "0.000000", Revenue: "0.000000", Margin: "0.000000"},
	}

	// The pool.
	poolCustomer := ""
	if settings.SovereignCustomerID != nil {
		c, err := s.GetCustomer(ctx, OperatorScope, *settings.SovereignCustomerID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return AllocationResult{}, err
		}
		if err == nil {
			poolCustomer = c.ID
			res.Pool.CustomerID, res.Pool.CustomerName = &c.ID, &c.Name
		}
	}
	switch settings.Pool {
	case PoolManual:
		res.Pool.Amount = decOf(ratOf(settings.ManualAmount))
	default:
		if poolCustomer == "" {
			id, name, note, err := s.resolveSovereignCustomer(ctx)
			if err != nil {
				return AllocationResult{}, err
			}
			if id == "" {
				res.Pool.Source = "unresolved"
				res.Pool.Note = note
			} else {
				poolCustomer = id
				res.Pool.CustomerID, res.Pool.CustomerName = &id, &name
			}
		}
		if poolCustomer != "" {
			ex, err := s.Explore(ctx, OperatorScope, CostQuery{
				From: from, To: to, Granularity: "month", GroupBy: "none", Metric: "cost",
				CustomerID: poolCustomer,
				Exclude:    map[string][]string{"kind": PlatformSKUKinds},
			})
			if err != nil {
				return AllocationResult{}, err
			}
			res.Pool.Amount = decOf(ratOf(ex.Total.Current))
			if ex.Currency != "" {
				res.Pool.Currency = ex.Currency
			}
		}
	}

	// The basis, split.
	basis, err := s.allocationBasis(ctx, from, to)
	if err != nil {
		return AllocationResult{}, err
	}
	rows, shareTotal := splitAllocation(basis, settings.Weights, settings.OverheadPolicy, ratOf(res.Pool.Amount))
	res.ShareTotal = shareTotal

	// Revenue: what each customer's price book rates its usage at, one query
	// grouped by customer. The pool customer's own rated usage IS the cost
	// being split, never revenue, so its rows carry 0.
	revenue := map[string]Decimal{}
	if len(rows) > 0 {
		ex, err := s.Explore(ctx, OperatorScope, CostQuery{From: from, To: to, Granularity: "month", GroupBy: "customer", Metric: "cost", Limit: 0})
		if err != nil {
			return AllocationResult{}, err
		}
		for _, g := range ex.Groups {
			revenue[g.Key] = g.Total
		}
	}
	allocated, rev, margin := new(big.Rat), new(big.Rat), new(big.Rat)
	for i := range rows {
		r := &rows[i]
		r.RatedRevenue = "0.000000"
		if r.Tier == "organization" && r.CustomerID != poolCustomer {
			if v, ok := revenue[r.CustomerID]; ok {
				r.RatedRevenue = decOf(ratOf(v))
			}
		}
		m := new(big.Rat).Sub(ratOf(r.RatedRevenue), ratOf(r.AllocatedCost))
		r.Margin = decOf(m)
		if rv := ratOf(r.RatedRevenue); rv.Sign() != 0 {
			pct, _ := new(big.Rat).Mul(new(big.Rat).Quo(m, rv), big.NewRat(100, 1)).Float64()
			r.MarginPct = &pct
		}
		allocated.Add(allocated, ratOf(r.AllocatedCost))
		rev.Add(rev, ratOf(r.RatedRevenue))
		margin.Add(margin, m)
		if r.Tier == "platform-overhead" {
			res.PlatformOverhead++
		} else {
			res.OrganizationRows++
		}
	}
	res.Rows = rows
	res.Totals = AllocationTotals{Allocated: decOf(allocated), Revenue: decOf(rev), Margin: decOf(margin)}
	return res, nil
}
