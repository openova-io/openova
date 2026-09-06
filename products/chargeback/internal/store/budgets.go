package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Budgets (#6867, DESIGN.md §3.5).
//
// A budget caps a calendar month's cost for one customer, or — when
// customer_id is NULL — for the whole Sovereign. A global budget is the
// operator's own instrument: it is never listed to a customer principal, whose
// scope shows exactly the budgets that name its customer.

// Budget is one monthly cap with alert thresholds. JSON tags are the wire
// contract with ui/src/api/types.ts (Budget).
type Budget struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CustomerID   *string   `json:"customer_id"`
	CustomerName *string   `json:"customer_name,omitempty"`
	Amount       Decimal   `json:"amount"`
	Currency     string    `json:"currency"`
	Period       string    `json:"period"`
	Thresholds   []int     `json:"thresholds"`
	NotifyEmails []string  `json:"notify_emails"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// BudgetAlert records that a threshold was crossed in a period. The unique
// key (budget, period, threshold) is what makes the hourly evaluator
// idempotent: the second evaluation finds the row and sends nothing.
type BudgetAlert struct {
	ID        int64     `json:"id"`
	BudgetID  string    `json:"budget_id"`
	Period    string    `json:"period"`
	Threshold int       `json:"threshold"`
	Actual    Decimal   `json:"actual"`
	At        time.Time `json:"at"`
}

// BudgetInput creates or replaces a budget. Validation (name, amount ≥ 0,
// currency, threshold range, email shape, customer existence) is the API's
// job; the CHECK constraints are the last line.
type BudgetInput struct {
	Name         string
	CustomerID   *string
	Amount       Decimal
	Currency     string
	Period       string
	Thresholds   []int
	NotifyEmails []string
	Active       bool
}

// budgetColumns is the shared projection so a new field cannot be added to
// one query and forgotten in another. The customer name is joined for display.
const budgetColumns = `b.id, b.name, b.customer_id, c.name, b.amount::text, b.currency, b.period, b.thresholds, b.notify_emails, b.active, b.created_at, b.updated_at`

const budgetFrom = ` FROM budgets b LEFT JOIN customers c ON c.id = b.customer_id`

func scanBudget(row interface{ Scan(...any) error }) (Budget, error) {
	var b Budget
	var cust, custName sql.NullString
	var amount string
	var thresholds pq.Int64Array
	var emails pq.StringArray
	if err := row.Scan(&b.ID, &b.Name, &cust, &custName, &amount, &b.Currency, &b.Period, &thresholds, &emails, &b.Active, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return b, mapErr(err)
	}
	b.CustomerID = strPtr(cust)
	b.CustomerName = strPtr(custName)
	b.Amount = Decimal(amount)
	b.Thresholds = make([]int, 0, len(thresholds))
	for _, t := range thresholds {
		b.Thresholds = append(b.Thresholds, int(t))
	}
	b.NotifyEmails = []string(emails)
	if b.NotifyEmails == nil {
		b.NotifyEmails = []string{}
	}
	b.CreatedAt = b.CreatedAt.UTC()
	b.UpdatedAt = b.UpdatedAt.UTC()
	return b, nil
}

func int64s(v []int) pq.Int64Array {
	out := make(pq.Int64Array, 0, len(v))
	for _, x := range v {
		out = append(out, int64(x))
	}
	return out
}

func (s *Store) queryBudgets(ctx context.Context, where string, args ...any) ([]Budget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+budgetColumns+budgetFrom+where+` ORDER BY b.created_at DESC, b.id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Budget{}
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListBudgets returns the budgets the scope may see: every budget for the
// operator; for a customer scope only the budgets naming that customer. A
// global budget (customer_id NULL) belongs to the operator and is never
// shown to a customer.
func (s *Store) ListBudgets(ctx context.Context, scope Scope) ([]Budget, error) {
	if scope.Operator {
		return s.queryBudgets(ctx, "")
	}
	if scope.CustomerID == "" {
		return nil, ErrNotFound
	}
	return s.queryBudgets(ctx, ` WHERE b.customer_id::text = $1`, scope.CustomerID)
}

// GetBudget returns one budget, or ErrNotFound when it does not exist or lies
// outside the scope (a global budget is outside every customer scope).
func (s *Store) GetBudget(ctx context.Context, scope Scope, id string) (Budget, error) {
	b, err := scanBudget(s.db.QueryRowContext(ctx, `SELECT `+budgetColumns+budgetFrom+` WHERE b.id::text = $1`, id))
	if err != nil {
		return Budget{}, err
	}
	if !scope.Operator {
		if b.CustomerID == nil || !scope.Allows(*b.CustomerID) {
			return Budget{}, ErrNotFound
		}
	}
	return b, nil
}

func normBudgetInput(in BudgetInput) BudgetInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "OMR"
	}
	if in.Period == "" {
		in.Period = "monthly"
	}
	if in.Thresholds == nil {
		in.Thresholds = []int{}
	}
	if in.NotifyEmails == nil {
		in.NotifyEmails = []string{}
	}
	if strings.TrimSpace(string(in.Amount)) == "" {
		in.Amount = "0"
	}
	return in
}

// CreateBudget stores a budget and returns it with the customer name joined.
func (s *Store) CreateBudget(ctx context.Context, in BudgetInput) (Budget, error) {
	in = normBudgetInput(in)
	var id string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO budgets (name, customer_id, amount, currency, period, thresholds, notify_emails, active)
		 VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8) RETURNING id`,
		in.Name, nullStr(in.CustomerID), string(in.Amount), in.Currency, in.Period,
		int64s(in.Thresholds), pq.StringArray(in.NotifyEmails), in.Active).Scan(&id)
	if err != nil {
		return Budget{}, mapErr(err)
	}
	return s.GetBudget(ctx, OperatorScope, id)
}

// UpdateBudget replaces every editable field of a budget.
func (s *Store) UpdateBudget(ctx context.Context, id string, in BudgetInput) (Budget, error) {
	in = normBudgetInput(in)
	res, err := s.db.ExecContext(ctx,
		`UPDATE budgets SET name = $2, customer_id = $3, amount = $4::numeric, currency = $5, period = $6,
		        thresholds = $7, notify_emails = $8, active = $9, updated_at = now()
		  WHERE id::text = $1`,
		id, in.Name, nullStr(in.CustomerID), string(in.Amount), in.Currency, in.Period,
		int64s(in.Thresholds), pq.StringArray(in.NotifyEmails), in.Active)
	if err != nil {
		return Budget{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Budget{}, ErrNotFound
	}
	return s.GetBudget(ctx, OperatorScope, id)
}

// DeleteBudget removes a budget and, through the FK cascade, its alerts.
func (s *Store) DeleteBudget(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM budgets WHERE id::text = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActiveBudgets returns every active budget — what the hourly evaluator
// walks. Operator-scoped by nature: alerts go to the configured recipients,
// not to a session.
func (s *Store) ListActiveBudgets(ctx context.Context) ([]Budget, error) {
	return s.queryBudgets(ctx, ` WHERE b.active`)
}

// RecordBudgetAlert marks a threshold crossed for a period. It reports
// whether THIS call inserted the row: a second call for the same
// (budget, period, threshold) hits the unique key, does nothing and returns
// false, so the caller mails and audits exactly once per crossing.
func (s *Store) RecordBudgetAlert(ctx context.Context, budgetID, period string, threshold int, actual Decimal) (bool, error) {
	if strings.TrimSpace(string(actual)) == "" {
		actual = "0"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO budget_alerts (budget_id, period, threshold, actual) VALUES ($1, $2, $3, $4::numeric)
		 ON CONFLICT (budget_id, period, threshold) DO NOTHING`,
		budgetID, period, threshold, string(actual))
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ListBudgetAlerts returns a budget's recorded crossings, newest first.
func (s *Store) ListBudgetAlerts(ctx context.Context, budgetID string) ([]BudgetAlert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, budget_id, period, threshold, actual::text, at FROM budget_alerts WHERE budget_id::text = $1 ORDER BY at DESC, id DESC`, budgetID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []BudgetAlert{}
	for rows.Next() {
		var a BudgetAlert
		var actual string
		if err := rows.Scan(&a.ID, &a.BudgetID, &a.Period, &a.Threshold, &actual, &a.At); err != nil {
			return nil, err
		}
		a.Actual = Decimal(actual)
		a.At = a.At.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}
