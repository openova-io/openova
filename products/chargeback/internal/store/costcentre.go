package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Cost-centre labelling (DESIGN.md §19).
//
// A cost centre is a LABEL on a customer's spend — a dimension, exactly like
// the region or the tag the explorer already groups by. It is never a party:
// it has no ledger, no invoice, no users and no account, and nothing about
// the price waterfall changes because a line carries one. That is why the
// account-hierarchy need is answered here and not by a second customers
// table with a parent: a sub-account would be a second party, and a second
// party is a second bill.
//
// Three tables and nothing else:
//
//   - cost_centres          a customer's flat list. No parent column exists,
//     so no nesting can be expressed.
//   - cost_centre_rules     the attribution rule: a tag KEY and the VALUE
//     that names a cost centre.
//   - cost_centre_resources the per-RESOURCE override, which beats a rule.
//
// Usage that no override and no rule names is attributed to the named
// UNASSIGNED bucket. It is never dropped and never spread across the named
// centres: an unattributed figure the operator can see is a tagging job; an
// unattributed figure quietly shared out is a report that reconciles and is
// wrong.

// CostCentreUnassigned is the bucket usage that matches no override and no
// rule is attributed to. The parentheses put it outside CostCentreCodeRule,
// so no real code can ever collide with it — the same device the explorer's
// TagUntagged and "(none)" enterprise project use.
const CostCentreUnassigned = "(unassigned)"

// CostCentreUnassignedName is what the bucket is called on a screen.
const CostCentreUnassignedName = "Unassigned"

// CostCentreDimension is the explorer dimension name (group_by / filter).
const CostCentreDimension = "cost_centre"

// CostCentreCodeRule is the shape of a cost-centre code, in the words the
// API reports when a request fails it.
const CostCentreCodeRule = "^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,63}$"

var costCentreCodeRE = regexp.MustCompile(CostCentreCodeRule)

// ValidCostCentreCode reports whether code satisfies CostCentreCodeRule.
func ValidCostCentreCode(code string) bool { return costCentreCodeRE.MatchString(code) }

// DefaultCostCentreRulePriority is the rank a rule takes when the operator
// states none. Rules are matched in (priority, key, value) order, so leaving
// every rule on the default still resolves deterministically.
const DefaultCostCentreRulePriority = 100

// costCentreMigrationSQL is appended at the very END of migrations:
// migrations are positional, so an entry inserted above a database's
// recorded version is silently skipped. Located by content as
// MigrationCostCentres.
const costCentreMigrationSQL = `
-- A cost centre of ONE customer. Flat by construction: there is no parent
-- column, so the model cannot grow a hierarchy by accident. The code is what
-- a tag value matches and what a report and an invoice breakdown print; the
-- name is for people. Deactivating one keeps every figure it ever carried
-- and stops it being chosen for new rules and overrides.
CREATE TABLE IF NOT EXISTS cost_centres (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	code TEXT NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,63}$'),
	name TEXT NOT NULL DEFAULT '',
	active BOOLEAN NOT NULL DEFAULT true,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (customer_id, code)
);
CREATE INDEX IF NOT EXISTS cost_centres_customer_idx ON cost_centres (customer_id);

-- The attribution rule: a resource tagged tag_key = tag_value belongs to
-- this cost centre. One value names at most one centre per customer, which
-- is what the unique key says; two rules on DIFFERENT keys can both match a
-- resource, and priority (then the key, then the value) decides, so the
-- answer never depends on row order.
CREATE TABLE IF NOT EXISTS cost_centre_rules (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	cost_centre_id UUID NOT NULL REFERENCES cost_centres(id) ON DELETE CASCADE,
	tag_key TEXT NOT NULL CHECK (tag_key ~ '^[A-Za-z0-9_.:/@-]{1,128}$'),
	tag_value TEXT NOT NULL,
	priority INT NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 9999),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (customer_id, tag_key, tag_value)
);
CREATE INDEX IF NOT EXISTS cost_centre_rules_match_idx ON cost_centre_rules (customer_id, priority, tag_key, tag_value);

-- The per-resource override. One resource, one cost centre: this is the
-- answer for the resource nobody tagged and for the resource tagged wrongly,
-- and it beats every rule.
CREATE TABLE IF NOT EXISTS cost_centre_resources (
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	resource_id TEXT NOT NULL,
	cost_centre_id UUID NOT NULL REFERENCES cost_centres(id) ON DELETE CASCADE,
	set_by TEXT NOT NULL DEFAULT '',
	set_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (customer_id, resource_id)
);
CREATE INDEX IF NOT EXISTS cost_centre_resources_centre_idx ON cost_centre_resources (cost_centre_id);

-- The breakdown frozen on a statement by the rating run, exactly as the
-- per-rule tax summary is. It is a SHOWBACK dimension: the statement's own
-- subtotal, tax and total are not touched by it and are never recomputed
-- from it.
ALTER TABLE statements ADD COLUMN IF NOT EXISTS cost_centre_lines JSONB;
`

// MigrationCostCentres is the schema_migrations version of the cost-centre
// migration, located by CONTENT like every other one so a migration appended
// after it cannot move this version.
var MigrationCostCentres = func() int {
	for i, m := range migrations {
		if m == costCentreMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// the model
// ---------------------------------------------------------------------------

// CostCentre is one label on a customer's spend. JSON tags are the wire
// contract with ui/src/api/types.ts (CostCentre).
type CostCentre struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Rules and Resources count what points at this centre, so a console can
	// say what deleting one would unattribute.
	Rules     int `json:"rules"`
	Resources int `json:"resources"`
}

// CostCentreInput creates or replaces a cost centre.
type CostCentreInput struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active *bool  `json:"active"`
}

// CostCentreRule maps one tag value onto one cost centre.
type CostCentreRule struct {
	ID           string    `json:"id"`
	CustomerID   string    `json:"customer_id"`
	CostCentreID string    `json:"cost_centre_id"`
	Code         string    `json:"code"`
	Name         string    `json:"name,omitempty"`
	TagKey       string    `json:"tag_key"`
	TagValue     string    `json:"tag_value"`
	Priority     int       `json:"priority"`
	CreatedAt    time.Time `json:"created_at"`
}

// CostCentreRef names a cost centre on a write, by id or by code.
type CostCentreRef struct {
	CostCentreID string `json:"cost_centre_id"`
	Code         string `json:"code"`
}

// CostCentreRuleInput creates or replaces a rule.
type CostCentreRuleInput struct {
	CostCentreRef
	TagKey   string `json:"tag_key"`
	TagValue string `json:"tag_value"`
	Priority *int   `json:"priority"`
}

// CostCentreResource is one per-resource override.
type CostCentreResource struct {
	CustomerID   string    `json:"customer_id"`
	ResourceID   string    `json:"resource_id"`
	CostCentreID string    `json:"cost_centre_id"`
	Code         string    `json:"code"`
	Name         string    `json:"name,omitempty"`
	SetBy        string    `json:"set_by,omitempty"`
	SetAt        time.Time `json:"set_at"`
}

// CostCentreWeight is one cost centre's share of a customer's rated usage in
// a window: what the period's records cost under that centre, priced by each
// record's OWN source book — costPricedExpr, the single definition of what
// one usage record costs. It is the WEIGHT the invoice breakdown is
// apportioned by, and on its own it is the usage report.
type CostCentreWeight struct {
	Code      string  `json:"code"`
	Name      string  `json:"name,omitempty"`
	Amount    Decimal `json:"amount"`
	Resources int     `json:"resources"`
}

// CostCentreLine is one row of a statement's cost-centre breakdown
// (DESIGN.md §19). The figures are the statement's OWN figures apportioned
// by the largest-remainder method, so summing a column over the rows returns
// the statement's figure exactly:
//
//	sum(net) == statement subtotal    sum(tax) == statement tax
//	sum(discount) == discount total   sum(total) == statement total
//
// List is Net + Discount and Total is Net + Tax on every row, so each row is
// internally consistent as well.
type CostCentreLine struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
	// Usage is the weight the apportionment used: the period's priced usage
	// attributed to this centre. It is reported so the share is auditable,
	// and it is NOT one of the exact identities — an invoice may carry a
	// charge that is not usage (a contract true-up), which is apportioned by
	// the same weights.
	Usage    Decimal `json:"usage"`
	List     Decimal `json:"list"`
	Discount Decimal `json:"discount"`
	Net      Decimal `json:"net"`
	Tax      Decimal `json:"tax"`
	Total    Decimal `json:"total"`
}

// ---------------------------------------------------------------------------
// the resolution, as SQL
// ---------------------------------------------------------------------------

// costCentreJoinSQL resolves the cost centre of a usage row aliased `u`, in
// the one order the whole product uses: the per-resource OVERRIDE first,
// then the first matching RULE by (priority, key, value), then nothing —
// which reads as the unassigned bucket.
//
// It is a LEFT JOIN and a LATERAL with LIMIT 1, so it can never multiply a
// usage row: adding it to a query changes what that query may GROUP BY and
// changes no total the query already produced.
const costCentreJoinSQL = `
  LEFT JOIN cost_centre_resources ccr ON ccr.customer_id = u.customer_id AND ccr.resource_id = u.resource_id
  LEFT JOIN LATERAL (
      SELECT r.cost_centre_id
        FROM cost_centre_rules r
       WHERE r.customer_id = u.customer_id
         AND u.labels->'tags'->>r.tag_key = r.tag_value
       ORDER BY r.priority, r.tag_key, r.tag_value
       LIMIT 1) ccm ON ccr.cost_centre_id IS NULL
  LEFT JOIN cost_centres cc ON cc.id = COALESCE(ccr.cost_centre_id, ccm.cost_centre_id)`

// costCentreCodeExpr and costCentreNameExpr read the aliases
// costCentreJoinSQL introduces.
const (
	costCentreCodeExpr = `COALESCE(cc.code, '` + CostCentreUnassigned + `')`
	costCentreNameExpr = `COALESCE(NULLIF(cc.name, ''), cc.code, '` + CostCentreUnassignedName + `')`
)

// ---------------------------------------------------------------------------
// the centres
// ---------------------------------------------------------------------------

const costCentreColumns = `cc.id, cc.customer_id, cc.code, cc.name, cc.active, cc.created_at, cc.updated_at,
	(SELECT count(*) FROM cost_centre_rules r WHERE r.cost_centre_id = cc.id),
	(SELECT count(*) FROM cost_centre_resources o WHERE o.cost_centre_id = cc.id)`

func scanCostCentre(row interface{ Scan(...any) error }) (CostCentre, error) {
	var c CostCentre
	if err := row.Scan(&c.ID, &c.CustomerID, &c.Code, &c.Name, &c.Active, &c.CreatedAt, &c.UpdatedAt, &c.Rules, &c.Resources); err != nil {
		return c, mapErr(err)
	}
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

// ListCostCentres returns a customer's cost centres, in code order.
func (s *Store) ListCostCentres(ctx context.Context, scope Scope, customerID string) ([]CostCentre, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+costCentreColumns+` FROM cost_centres cc WHERE cc.customer_id = $1 ORDER BY cc.code`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostCentre{}
	for rows.Next() {
		c, err := scanCostCentre(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCostCentre reads one by id, within the scope.
func (s *Store) GetCostCentre(ctx context.Context, scope Scope, id string) (CostCentre, error) {
	c, err := scanCostCentre(s.db.QueryRowContext(ctx, `SELECT `+costCentreColumns+` FROM cost_centres cc WHERE cc.id = $1`, id))
	if err != nil {
		return CostCentre{}, err
	}
	if !scope.Allows(c.CustomerID) {
		return CostCentre{}, ErrNotFound
	}
	return c, nil
}

func validateCostCentre(in CostCentreInput) error {
	if !ValidCostCentreCode(in.Code) {
		return fmt.Errorf("%w: code must match %s", ErrInvalid, CostCentreCodeRule)
	}
	if len(in.Name) > 200 {
		return fmt.Errorf("%w: name is at most 200 characters", ErrInvalid)
	}
	return nil
}

// CreateCostCentre adds one to a customer. The code is unique per customer;
// a second one with the same code is a conflict, never a second centre.
func (s *Store) CreateCostCentre(ctx context.Context, customerID string, in CostCentreInput) (CostCentre, error) {
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	if err := validateCostCentre(in); err != nil {
		return CostCentre{}, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO cost_centres (customer_id, code, name, active) VALUES ($1, $2, $3, $4) RETURNING id`,
		customerID, in.Code, in.Name, active).Scan(&id); err != nil {
		return CostCentre{}, mapErr(err)
	}
	return s.GetCostCentre(ctx, OperatorScope, id)
}

// UpdateCostCentre replaces the code, name and active flag of one.
func (s *Store) UpdateCostCentre(ctx context.Context, id string, in CostCentreInput) (CostCentre, error) {
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	if err := validateCostCentre(in); err != nil {
		return CostCentre{}, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	res, err := s.db.ExecContext(ctx, `UPDATE cost_centres SET code = $2, name = $3, active = $4, updated_at = now() WHERE id = $1`, id, in.Code, in.Name, active)
	if err != nil {
		return CostCentre{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CostCentre{}, ErrNotFound
	}
	return s.GetCostCentre(ctx, OperatorScope, id)
}

// DeleteCostCentre removes one with its rules and overrides. The breakdown
// already frozen on an issued invoice keeps naming it: that column stores
// CODES, not a foreign key, exactly as a frozen tax line stores a rule id.
func (s *Store) DeleteCostCentre(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cost_centres WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// the rules
// ---------------------------------------------------------------------------

const costCentreRuleColumns = `r.id, r.customer_id, r.cost_centre_id, cc.code, cc.name, r.tag_key, r.tag_value, r.priority, r.created_at`

const costCentreRuleFrom = ` FROM cost_centre_rules r JOIN cost_centres cc ON cc.id = r.cost_centre_id`

// costCentreRuleOrder is the resolution order itself, so the console lists
// the rules in the order a record is matched against them.
const costCentreRuleOrder = ` ORDER BY r.priority, r.tag_key, r.tag_value`

func scanCostCentreRule(row interface{ Scan(...any) error }) (CostCentreRule, error) {
	var r CostCentreRule
	if err := row.Scan(&r.ID, &r.CustomerID, &r.CostCentreID, &r.Code, &r.Name, &r.TagKey, &r.TagValue, &r.Priority, &r.CreatedAt); err != nil {
		return r, mapErr(err)
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return r, nil
}

// ListCostCentreRules returns a customer's rules in resolution order.
func (s *Store) ListCostCentreRules(ctx context.Context, scope Scope, customerID string) ([]CostCentreRule, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+costCentreRuleColumns+costCentreRuleFrom+` WHERE r.customer_id = $1`+costCentreRuleOrder, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostCentreRule{}
	for rows.Next() {
		r, err := scanCostCentreRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCostCentreRule reads one by id, within the scope.
func (s *Store) GetCostCentreRule(ctx context.Context, scope Scope, id string) (CostCentreRule, error) {
	r, err := scanCostCentreRule(s.db.QueryRowContext(ctx, `SELECT `+costCentreRuleColumns+costCentreRuleFrom+` WHERE r.id = $1`, id))
	if err != nil {
		return CostCentreRule{}, err
	}
	if !scope.Allows(r.CustomerID) {
		return CostCentreRule{}, ErrNotFound
	}
	return r, nil
}

// PutCostCentreRule creates or replaces the rule for (customer, tag key, tag
// value). Re-pointing an existing value at another centre is an UPDATE of
// that one rule rather than a conflict: "team=platform now belongs to
// engineering" is the ordinary edit, and two rows for one value is exactly
// the state the unique key exists to prevent.
func (s *Store) PutCostCentreRule(ctx context.Context, customerID string, in CostCentreRuleInput) (CostCentreRule, error) {
	in.TagKey, in.TagValue = strings.TrimSpace(in.TagKey), strings.TrimSpace(in.TagValue)
	if !ValidTagKey(in.TagKey) {
		return CostCentreRule{}, fmt.Errorf("%w: tag_key must match %s", ErrInvalid, TagKeyRule)
	}
	if len(in.TagValue) > 256 {
		return CostCentreRule{}, fmt.Errorf("%w: tag_value is at most 256 characters", ErrInvalid)
	}
	priority := DefaultCostCentreRulePriority
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 9999 {
		return CostCentreRule{}, fmt.Errorf("%w: priority must be between 0 and 9999", ErrInvalid)
	}
	centre, err := s.costCentreFor(ctx, customerID, in.CostCentreRef)
	if err != nil {
		return CostCentreRule{}, err
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO cost_centre_rules (customer_id, cost_centre_id, tag_key, tag_value, priority)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (customer_id, tag_key, tag_value) DO UPDATE SET cost_centre_id = EXCLUDED.cost_centre_id, priority = EXCLUDED.priority
		RETURNING id`, customerID, centre.ID, in.TagKey, in.TagValue, priority).Scan(&id); err != nil {
		return CostCentreRule{}, mapErr(err)
	}
	return s.GetCostCentreRule(ctx, OperatorScope, id)
}

// DeleteCostCentreRule removes one rule.
func (s *Store) DeleteCostCentreRule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cost_centre_rules WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// costCentreFor resolves the centre a write names, by id or by code, and
// refuses one that is not this customer's or is no longer active. An
// INACTIVE centre still attributes everything already pointed at it — a
// figure does not move because a label was retired — but nothing new may be
// pointed at it.
func (s *Store) costCentreFor(ctx context.Context, customerID string, ref CostCentreRef) (CostCentre, error) {
	var c CostCentre
	var err error
	switch {
	case strings.TrimSpace(ref.CostCentreID) != "":
		c, err = s.GetCostCentre(ctx, OperatorScope, strings.TrimSpace(ref.CostCentreID))
	case strings.TrimSpace(ref.Code) != "":
		c, err = scanCostCentre(s.db.QueryRowContext(ctx, `SELECT `+costCentreColumns+` FROM cost_centres cc WHERE cc.customer_id = $1 AND cc.code = $2`, customerID, strings.TrimSpace(ref.Code)))
	default:
		return CostCentre{}, fmt.Errorf("%w: name the cost centre by cost_centre_id or code", ErrInvalid)
	}
	if err != nil {
		return CostCentre{}, err
	}
	if c.CustomerID != customerID {
		return CostCentre{}, ErrNotFound
	}
	if !c.Active {
		return CostCentre{}, fmt.Errorf("%w: cost centre %s is inactive; reactivate it before pointing anything at it", ErrInvalid, c.Code)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// the per-resource override
// ---------------------------------------------------------------------------

const costCentreResourceColumns = `o.customer_id, o.resource_id, o.cost_centre_id, cc.code, cc.name, o.set_by, o.set_at`

const costCentreResourceFrom = ` FROM cost_centre_resources o JOIN cost_centres cc ON cc.id = o.cost_centre_id`

func scanCostCentreResource(row interface{ Scan(...any) error }) (CostCentreResource, error) {
	var o CostCentreResource
	if err := row.Scan(&o.CustomerID, &o.ResourceID, &o.CostCentreID, &o.Code, &o.Name, &o.SetBy, &o.SetAt); err != nil {
		return o, mapErr(err)
	}
	o.SetAt = o.SetAt.UTC()
	return o, nil
}

// ListCostCentreResources returns a customer's per-resource overrides.
func (s *Store) ListCostCentreResources(ctx context.Context, scope Scope, customerID string) ([]CostCentreResource, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+costCentreResourceColumns+costCentreResourceFrom+` WHERE o.customer_id = $1 ORDER BY cc.code, o.resource_id`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostCentreResource{}
	for rows.Next() {
		o, err := scanCostCentreResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetResourceCostCentre records the override for one resource.
func (s *Store) SetResourceCostCentre(ctx context.Context, customerID, resourceID string, ref CostCentreRef, setBy string) (CostCentreResource, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" || len(resourceID) > 512 {
		return CostCentreResource{}, fmt.Errorf("%w: resource_id is required and is at most 512 characters", ErrInvalid)
	}
	centre, err := s.costCentreFor(ctx, customerID, ref)
	if err != nil {
		return CostCentreResource{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO cost_centre_resources (customer_id, resource_id, cost_centre_id, set_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (customer_id, resource_id) DO UPDATE SET cost_centre_id = EXCLUDED.cost_centre_id, set_by = EXCLUDED.set_by, set_at = now()`,
		customerID, resourceID, centre.ID, setBy); err != nil {
		return CostCentreResource{}, mapErr(err)
	}
	return scanCostCentreResource(s.db.QueryRowContext(ctx, `SELECT `+costCentreResourceColumns+costCentreResourceFrom+` WHERE o.customer_id = $1 AND o.resource_id = $2`, customerID, resourceID))
}

// ClearResourceCostCentre removes the override, so the resource falls back to
// the rules and then to the unassigned bucket.
func (s *Store) ClearResourceCostCentre(ctx context.Context, customerID, resourceID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cost_centre_resources WHERE customer_id = $1 AND resource_id = $2`, customerID, strings.TrimSpace(resourceID))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// the weights
// ---------------------------------------------------------------------------

// costCentreWeightSQL sums what each cost centre's usage costs in a window,
// for ONE customer, priced by each record's own source book — the same
// costPricedExpr the explorer and the rating run use, so a breakdown and a
// bill can never disagree about what a record is worth. Unpriced records
// contribute nothing, and the sampled measurements are excluded exactly as
// they are in rating.
const costCentreWeightSQL = `
SELECT ` + costCentreCodeExpr + ` AS code,
       ` + costCentreNameExpr + ` AS name,
       COALESCE(sum(` + costPricedExpr + `), 0)::numeric(20,6)::text AS amount,
       count(DISTINCT u.resource_id) AS resources
  FROM usage_records u` + costPriceJoinSQL + costCentreJoinSQL + `
 WHERE u.customer_id = $1 AND u.window_start >= $2 AND u.window_start < $3 AND ` + costMeterFilter + costExcludeInternalSQL + `
 GROUP BY 1, 2
 ORDER BY COALESCE(sum(` + costPricedExpr + `), 0) DESC, 1`

// CostCentreWeights is a customer's period usage attributed to cost centres,
// biggest first. Everything no override and no rule named is one row under
// CostCentreUnassigned: the figure is always visible and is never shared out.
func (s *Store) CostCentreWeights(ctx context.Context, scope Scope, customerID string, from, to time.Time) ([]CostCentreWeight, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, costCentreWeightSQL, customerID, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostCentreWeight{}
	for rows.Next() {
		var w CostCentreWeight
		var amount string
		if err := rows.Scan(&w.Code, &w.Name, &amount, &w.Resources); err != nil {
			return nil, mapErr(err)
		}
		w.Amount = Decimal(amount)
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// the frozen breakdown
// ---------------------------------------------------------------------------

func costCentreLinesJSON(lines []CostCentreLine) any {
	if len(lines) == 0 {
		return nil
	}
	b, err := json.Marshal(lines)
	if err != nil {
		return nil
	}
	return b
}

func decodeCostCentreLines(raw []byte) []CostCentreLine {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []CostCentreLine
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// StatementCostCentreLines reads the breakdown frozen on one statement.
func (s *Store) StatementCostCentreLines(ctx context.Context, statementID string) ([]CostCentreLine, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT cost_centre_lines FROM statements WHERE id = $1`, statementID).Scan(&raw); err != nil {
		return nil, mapErr(err)
	}
	return decodeCostCentreLines(raw), nil
}
