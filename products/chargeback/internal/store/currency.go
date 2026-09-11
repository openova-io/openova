package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Multi-currency conversion (#6867 follow-up, DESIGN.md §3.10).
//
// A price book carries a currency, and customers' books may differ. Every
// cost surface (explorer, summary, resources, anomalies, recommendations,
// allocation, reports, budgets) reports in ONE reporting currency — the
// `currency` of allocation_settings — the way a cloud console reports a
// multi-currency estate in one figure with stored exchange rates.
//
// A rate row says how many units of `code` one unit of the reporting
// currency buys (per_base): 1 OMR = 2.6 USD → USD 2.6. So
//
//	cost_base = cost / per_base(book currency)
//
// with per_base(reporting) = 1 by definition, and NULL — "unconverted" —
// when the book currency has no row. Unconverted records are never summed
// into a total; they are listed per currency (records + cost in that
// currency) so the operator sees exactly what is missing and adds the
// rate. Statements are NOT converted: a statement is issued in the
// customer's book currency.

// CurrencyRate is one stored rate (wire shape ui/src/api/types.ts CurrencyRate).
type CurrencyRate struct {
	Code      string    `json:"code"`
	PerBase   Decimal   `json:"per_base"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UnconvertedCurrency is the usage of one book currency that has no rate:
// how many records and what they cost in THAT currency. Explore and summary
// carry a list of these instead of silently mixing them into the total.
type UnconvertedCurrency struct {
	Currency string  `json:"currency"`
	Records  int     `json:"records"`
	Cost     Decimal `json:"cost"`
}

// ErrReportingCurrency is returned when a rate is written for the reporting
// currency itself: its rate is 1 by definition and never stored.
var ErrReportingCurrency = fmt.Errorf("%w: reporting currency", ErrInvalid)

// DefaultReportingCurrency is the reporting currency before an operator
// sets one (the allocation_settings default).
const DefaultReportingCurrency = "OMR"

// reportingCurrencySQL is the reporting currency as a SQL scalar: the
// allocation_settings row, or the default when the row is missing (the
// same fallback GetAllocationSettings applies). Uncorrelated, so Postgres
// evaluates it once per query, not per row.
const reportingCurrencySQL = `COALESCE((SELECT a.currency FROM allocation_settings a WHERE a.id = 1), '` + DefaultReportingCurrency + `')`

// costRateExpr is per_base for a usage record's book currency, given the
// aliases costPriceJoinSQL introduces (`b` the book, `x` its rate row):
// 1 for the reporting currency, the stored rate otherwise, NULL when there
// is none. Dividing a cost by it yields the cost in the reporting currency.
const costRateExpr = `CASE WHEN COALESCE(b.currency, '') = ` + reportingCurrencySQL + ` THEN 1 ELSE x.per_base END`

// ReportingCurrency reads the reporting currency (allocation_settings.currency).
func (s *Store) ReportingCurrency(ctx context.Context) (string, error) {
	var cur string
	if err := s.db.QueryRowContext(ctx, `SELECT `+reportingCurrencySQL).Scan(&cur); err != nil {
		return "", mapErr(err)
	}
	return cur, nil
}

// NormalizeCurrencyCode upper-cases and trims a code; ok reports whether it
// is three letters.
func NormalizeCurrencyCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	return code, currencyShape.MatchString(code)
}

func scanCurrencyRate(row interface{ Scan(...any) error }) (CurrencyRate, error) {
	var r CurrencyRate
	var per string
	if err := row.Scan(&r.Code, &per, &r.Source, &r.UpdatedAt); err != nil {
		return r, mapErr(err)
	}
	r.PerBase = Decimal(per)
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

// ListCurrencyRates returns every stored rate, by code.
func (s *Store) ListCurrencyRates(ctx context.Context) ([]CurrencyRate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, per_base::text, source, updated_at FROM currency_rates ORDER BY code`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CurrencyRate{}
	for rows.Next() {
		r, err := scanCurrencyRate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCurrencyRate returns one stored rate, or ErrNotFound.
func (s *Store) GetCurrencyRate(ctx context.Context, code string) (CurrencyRate, error) {
	code, ok := NormalizeCurrencyCode(code)
	if !ok {
		return CurrencyRate{}, ErrNotFound
	}
	return scanCurrencyRate(s.db.QueryRowContext(ctx, `SELECT code, per_base::text, source, updated_at FROM currency_rates WHERE code = $1`, code))
}

// PutCurrencyRate creates or replaces the rate of a currency. The code must
// be three letters (ErrInvalid), per_base a number > 0 (ErrInvalid), and the
// code must not be the reporting currency (ErrReportingCurrency), whose
// rate is 1 by definition. An empty source is recorded as "manual".
func (s *Store) PutCurrencyRate(ctx context.Context, code string, perBase Decimal, source string) (CurrencyRate, error) {
	code, ok := NormalizeCurrencyCode(code)
	if !ok {
		return CurrencyRate{}, fmt.Errorf("%w: code must be a 3-letter currency code", ErrInvalid)
	}
	per := strings.TrimSpace(string(perBase))
	if !decimalShape.MatchString(per) || ratOf(Decimal(per)).Sign() <= 0 {
		return CurrencyRate{}, fmt.Errorf("%w: per_base must be a number > 0", ErrInvalid)
	}
	reporting, err := s.ReportingCurrency(ctx)
	if err != nil {
		return CurrencyRate{}, err
	}
	if code == reporting {
		return CurrencyRate{}, fmt.Errorf("%w %s: its rate is 1 by definition", ErrReportingCurrency, reporting)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO currency_rates (code, per_base, source, updated_at) VALUES ($1, $2::numeric, $3, now())
		ON CONFLICT (code) DO UPDATE SET per_base = EXCLUDED.per_base, source = EXCLUDED.source, updated_at = now()`, code, per, source)
	if err != nil {
		return CurrencyRate{}, mapErr(err)
	}
	return s.GetCurrencyRate(ctx, code)
}

// DeleteCurrencyRate removes a rate; its currency becomes unconverted.
func (s *Store) DeleteCurrencyRate(ctx context.Context, code string) error {
	code, ok := NormalizeCurrencyCode(code)
	if !ok {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM currency_rates WHERE code = $1`, code)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RateToBase is per_base for a currency as the Go side sees it: "1" for
// the reporting currency, the stored rate, or "" when there is none.
// Callers that convert outside SQL (recommendation savings) use ToBase.
func (s *Store) RateToBase(ctx context.Context, currency string) (Decimal, error) {
	code, ok := NormalizeCurrencyCode(currency)
	if !ok {
		return "", nil
	}
	reporting, err := s.ReportingCurrency(ctx)
	if err != nil {
		return "", err
	}
	if code == reporting {
		return "1", nil
	}
	r, err := s.GetCurrencyRate(ctx, code)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.PerBase, nil
}

// ToBase converts an amount in a book currency to the reporting currency:
// amount / perBase, exactly (big.Rat), rendered at 6 decimals. ok is false
// — and the amount returned unchanged — when perBase is empty or not > 0,
// which is what "no rate" looks like on the Go side.
//
// The direction matters and is easy to get backwards: per_base is units of
// the book currency per ONE reporting unit (USD 2.6 per OMR), so a USD
// amount is DIVIDED by it. 26 USD / 2.6 = 10 OMR; multiplying would say
// 67.6, which the unit test refuses.
func ToBase(amount, perBase Decimal) (Decimal, bool) {
	rate := ratOf(perBase)
	if strings.TrimSpace(string(perBase)) == "" || rate.Sign() <= 0 {
		return amount, false
	}
	return decOf(new(big.Rat).Quo(ratOf(amount), rate)), true
}

// unconvertedSQL aggregates, over the filtered CTE f, the priced records
// whose book currency has no rate: per currency, how many and what they
// cost in that currency. cost IS NOT NULL keeps unpriced usage out (that is
// the unpriced list's job); cost_base IS NULL is the missing rate.
//
// The count is sum(records), not count(*): one row of f stands for the usage
// records of a whole day at that grain (costrollup.go), and the operator is
// told how many RECORDS are unconverted, not how many rows the aggregate
// happened to hold.
const unconvertedSQL = `
SELECT currency, sum(records)::bigint, round(sum(cost), 6)::text
  FROM f WHERE cost IS NOT NULL AND cost_base IS NULL
 GROUP BY currency ORDER BY currency`

func scanUnconverted(rows *sql.Rows) ([]UnconvertedCurrency, error) {
	out := []UnconvertedCurrency{}
	for rows.Next() {
		var u UnconvertedCurrency
		var cost string
		if err := rows.Scan(&u.Currency, &u.Records, &cost); err != nil {
			return nil, err
		}
		u.Cost = Decimal(cost)
		out = append(out, u)
	}
	return out, rows.Err()
}
