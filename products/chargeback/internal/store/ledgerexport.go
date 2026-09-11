package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The FINANCE HANDOVER (DESIGN.md §18, EPIC #6867) — the three things an
// operator's finance department needs before it will accept a figure this
// product produced: a JOURNAL it can post, a RECONCILIATION against what the
// gateway actually settled, and a PERIOD CLOSE that makes last month stop
// moving.
//
// Nothing here rates, prices or collects anything. It reads what already
// happened — the append-only account ledger of §9, the invoices behind it,
// the allocations that applied money to them — and renders it as double-entry
// lines against accounts THE OPERATOR maps. The account codes are data, never
// strings in the emitter: a finance department that posts to 1100 and one
// that posts to 41000 are the same code and a different row.
//
// This file is the persistence half:
//
//   - `account_mappings` — key → account code, with sensible defaults seeded
//     on migration and editable with settings.manage.
//   - `finance_periods` — one row per closed month, who closed it and why it
//     was reopened, and the journal TOTALS the close asserted.
//   - `finance_journal_lines` — the journal of a CLOSED period, stored at
//     close. A closed period's export is served from here, so it is
//     byte-identical however much later the reader asks for it.
//   - `finance_reconciliations` + `finance_reconciliation_lines` — one
//     settlement run and its four buckets. Nothing is auto-corrected: the
//     run REPORTS, and a human decides.
//
// The pure double-entry emitter is internal/finance; it never touches SQL.

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// financeMigrationSQL is one transaction, idempotent against a database that
// already carries the shape. APPENDED AT THE VERY END of the migrations
// slice: migrations are positional, so an entry inserted above a database's
// recorded version is silently skipped. MigrationFinance locates it by
// CONTENT so a migration appended after it cannot move this version.
const financeMigrationSQL = `
-- The operator's chart of accounts, as a map from the key this product knows
-- to the code the finance system posts to. Keys are ours and fixed; codes,
-- names and the map itself are the operator's.
CREATE TABLE IF NOT EXISTS account_mappings (
	key TEXT PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_.-]{0,63}$'),
	account_code TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by TEXT NOT NULL DEFAULT ''
);

` + accountMappingSeedSQL + `
-- A closed month. status open is never stored — the absence of a row IS open
-- — so a period reads closed only because somebody closed it.
CREATE TABLE IF NOT EXISTS finance_periods (
	period TEXT PRIMARY KEY CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
	status TEXT NOT NULL DEFAULT 'closed' CHECK (status IN ('closed','reopened')),
	closed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	closed_by TEXT NOT NULL DEFAULT '',
	total_debit NUMERIC(20,6) NOT NULL DEFAULT 0,
	total_credit NUMERIC(20,6) NOT NULL DEFAULT 0,
	lines INT NOT NULL DEFAULT 0,
	reopened_at TIMESTAMPTZ,
	reopened_by TEXT NOT NULL DEFAULT '',
	reopen_reason TEXT NOT NULL DEFAULT ''
);

-- The journal a close froze. Written once, inside the closing transaction,
-- and read verbatim afterwards: that is what makes a closed period's export
-- stable against every later event.
CREATE TABLE IF NOT EXISTS finance_journal_lines (
	period TEXT NOT NULL REFERENCES finance_periods(period) ON DELETE CASCADE,
	seq INT NOT NULL,
	entry_date DATE NOT NULL,
	event_kind TEXT NOT NULL,
	account_key TEXT NOT NULL,
	account_code TEXT NOT NULL DEFAULT '',
	account_name TEXT NOT NULL DEFAULT '',
	debit NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (debit >= 0),
	credit NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (credit >= 0),
	currency TEXT NOT NULL DEFAULT '',
	customer_id UUID,
	customer_slug TEXT NOT NULL DEFAULT '',
	customer_name TEXT NOT NULL DEFAULT '',
	source_kind TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	reference TEXT NOT NULL DEFAULT '',
	memo TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (period, seq)
);

-- One reconciliation run against a gateway's settlement file or feed.
CREATE TABLE IF NOT EXISTS finance_reconciliations (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	gateway TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT 'file' CHECK (source IN ('file','gateway')),
	file_name TEXT NOT NULL DEFAULT '',
	from_date DATE,
	to_date DATE,
	currency TEXT NOT NULL DEFAULT '',
	matched INT NOT NULL DEFAULT 0,
	mismatched INT NOT NULL DEFAULT 0,
	missing_in_ledger INT NOT NULL DEFAULT 0,
	missing_in_settlement INT NOT NULL DEFAULT 0,
	duplicates INT NOT NULL DEFAULT 0,
	settled_total NUMERIC(20,6) NOT NULL DEFAULT 0,
	ledger_total NUMERIC(20,6) NOT NULL DEFAULT 0,
	fee_total NUMERIC(20,6) NOT NULL DEFAULT 0,
	ran_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	ran_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS finance_reconciliations_ran_idx ON finance_reconciliations (ran_at DESC);

-- Every line of a run, in its bucket. Nothing here corrects anything: a row
-- is a statement of what the two sides say, for a human to act on.
CREATE TABLE IF NOT EXISTS finance_reconciliation_lines (
	id BIGSERIAL PRIMARY KEY,
	run_id UUID NOT NULL REFERENCES finance_reconciliations(id) ON DELETE CASCADE,
	bucket TEXT NOT NULL CHECK (bucket IN ('matched','amount-mismatch','missing-in-ledger','missing-in-settlement','duplicate')),
	gateway_reference TEXT NOT NULL DEFAULT '',
	settled_amount NUMERIC(20,6),
	ledger_amount NUMERIC(20,6),
	difference NUMERIC(20,6),
	fee NUMERIC(20,6) NOT NULL DEFAULT 0,
	currency TEXT NOT NULL DEFAULT '',
	settled_date DATE,
	payment_id BIGINT,
	customer_id UUID,
	customer_name TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS finance_reconciliation_lines_run_idx ON finance_reconciliation_lines (run_id, bucket, id);
`

// accountMappingSeedSQL is the DEFAULT chart of accounts. It is applied by
// the migration and does nothing on conflict, so an operator's edit is never
// overwritten by a later migration run. AccountMappingSeedSQL exposes it so a
// test database that truncated the table starts from the same defaults a
// Sovereign does.
const accountMappingSeedSQL = `
INSERT INTO account_mappings (key, account_code, description, updated_by) VALUES
	('receivable',        '1100', 'Trade receivables',                    'migration'),
	('cash',              '1000', 'Cash and bank',                        'migration'),
	('gateway_clearing',  '1010', 'Payment gateway clearing',             'migration'),
	('customer_advances', '2100', 'Customer advances and account credit', 'migration'),
	('tax_payable',       '2200', 'Tax payable',                          'migration'),
	('revenue',           '4000', 'Revenue',                              'migration'),
	('discounts',         '4800', 'Discounts and allowances',             'migration'),
	('credit_notes',      '4900', 'Credit notes',                         'migration'),
	('write_offs',        '6100', 'Receivables written off',              'migration'),
	('gateway_fees',      '6200', 'Payment gateway fees',                 'migration'),
	('commission',        '6300', 'Partner commission',                   'migration')
ON CONFLICT (key) DO NOTHING;
`

// AccountMappingSeedSQL is the default chart of accounts as SQL.
func AccountMappingSeedSQL() string { return accountMappingSeedSQL }

// MigrationFinance is the schema_migrations version of the finance-handover
// migration, located by CONTENT like every other one so a migration appended
// after it cannot move this version.
var MigrationFinance = func() int {
	for i, m := range migrations {
		if m == financeMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// the account map
// ---------------------------------------------------------------------------

// The account keys this product books to. They are FIXED — the emitter names
// a key, the operator maps it to a code — except `revenue.<service>`, which
// an operator adds one of per service it wants revenue split by.
const (
	AccountReceivable       = "receivable"
	AccountCash             = "cash"
	AccountGatewayClearing  = "gateway_clearing"
	AccountCustomerAdvances = "customer_advances"
	AccountTaxPayable       = "tax_payable"
	AccountRevenue          = "revenue"
	AccountDiscounts        = "discounts"
	AccountCreditNotes      = "credit_notes"
	AccountWriteOffs        = "write_offs"
	AccountGatewayFees      = "gateway_fees"
	AccountCommission       = "commission"
)

// AccountKeys lists the fixed keys in posting order, for the console's table
// and for the check that an edit names a key this product actually books to.
var AccountKeys = []string{
	AccountReceivable, AccountCash, AccountGatewayClearing, AccountCustomerAdvances,
	AccountTaxPayable, AccountRevenue, AccountDiscounts, AccountCreditNotes,
	AccountWriteOffs, AccountGatewayFees, AccountCommission,
}

// RevenueKeyPrefix is what a per-service revenue key starts with:
// `revenue.ecs`, `revenue.k8s`. An unmapped service falls back to `revenue`.
const RevenueKeyPrefix = AccountRevenue + "."

// AccountMapping is one row of the operator's chart of accounts.
type AccountMapping struct {
	Key         string    `json:"key"`
	AccountCode string    `json:"account_code"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

// ValidAccountKey reports whether a key is one this product books to: a fixed
// key, or a per-service revenue key.
func ValidAccountKey(key string) bool {
	for _, k := range AccountKeys {
		if k == key {
			return true
		}
	}
	if !strings.HasPrefix(key, RevenueKeyPrefix) {
		return false
	}
	svc := key[len(RevenueKeyPrefix):]
	if svc == "" || len(svc) > 40 {
		return false
	}
	for _, r := range svc {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// ListAccountMappings returns the map in posting order: the fixed keys first,
// then the per-service revenue keys by name.
func (s *Store) ListAccountMappings(ctx context.Context) ([]AccountMapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, account_code, description, updated_at, updated_by FROM account_mappings`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	byKey := map[string]AccountMapping{}
	var extra []AccountMapping
	for rows.Next() {
		var m AccountMapping
		if err := rows.Scan(&m.Key, &m.AccountCode, &m.Description, &m.UpdatedAt, &m.UpdatedBy); err != nil {
			return nil, err
		}
		m.UpdatedAt = m.UpdatedAt.UTC()
		byKey[m.Key] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AccountMapping, 0, len(byKey))
	for _, k := range AccountKeys {
		if m, ok := byKey[k]; ok {
			out = append(out, m)
			delete(byKey, k)
		} else {
			out = append(out, AccountMapping{Key: k})
		}
	}
	for _, m := range byKey {
		extra = append(extra, m)
	}
	sortMappings(extra)
	return append(out, extra...), nil
}

func sortMappings(ms []AccountMapping) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].Key < ms[j].Key })
}

// PutAccountMappings upserts the given rows and returns the whole map. An
// unknown key is refused by name rather than stored: a code posted to a key
// nothing books to would never appear in a journal, and finding that out at
// year end is exactly the surprise this product is meant to remove.
func (s *Store) PutAccountMappings(ctx context.Context, in []AccountMapping, actor string) ([]AccountMapping, error) {
	for _, m := range in {
		key := strings.ToLower(strings.TrimSpace(m.Key))
		if !ValidAccountKey(key) {
			return nil, fmt.Errorf("%w: %q is not an account key this product books to; the fixed keys are %s, and a per-service revenue key is revenue.<service>", ErrInvalid, m.Key, strings.Join(AccountKeys, ", "))
		}
		if len(strings.TrimSpace(m.AccountCode)) > 64 {
			return nil, fmt.Errorf("%w: the account code for %s is longer than 64 characters", ErrInvalid, key)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, m := range in {
		key := strings.ToLower(strings.TrimSpace(m.Key))
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_mappings (key, account_code, description, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (key) DO UPDATE SET account_code = EXCLUDED.account_code, description = EXCLUDED.description, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			key, strings.TrimSpace(m.AccountCode), strings.TrimSpace(m.Description), actor); err != nil {
			return nil, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListAccountMappings(ctx)
}

// ---------------------------------------------------------------------------
// the period's events
// ---------------------------------------------------------------------------

// Journal event kinds — what HAPPENED, before any account is named. The
// emitter in internal/finance turns each into its pair of lines.
const (
	EventInvoice        = "invoice"
	EventPayment        = "payment"
	EventTopUp          = "top_up"
	EventAdvanceApplied = "advance_applied"
	EventCreditNote     = "credit_note"
	EventWriteOff       = "write_off"
	EventRefund         = "refund"
	EventGatewayFee     = "gateway_fee"
)

// ServiceAmount is one service's share of an invoice's LIST revenue.
type ServiceAmount struct {
	Service string  `json:"service"`
	Amount  Decimal `json:"amount"`
}

// TaxRule is one rate an invoice's tax was computed at, as the invoice's own
// frozen tax snapshot records it. A snapshot with no rule list is one rule:
// the snapshot's single rate for the whole tax. Nothing here recomputes tax —
// the invoice froze it, and the journal posts what the invoice says.
type TaxRule struct {
	// Code names the rule for the finance system; empty reads as the rate.
	Code string  `json:"code,omitempty"`
	Name string  `json:"name,omitempty"`
	Rate Decimal `json:"rate"`
	Tax  Decimal `json:"tax"`
}

// JournalEvent is one financial fact of a period, with everything the
// emitter needs to book it and everything a reader needs to trace it back.
type JournalEvent struct {
	Kind         string    `json:"kind"`
	At           time.Time `json:"at"`
	CustomerID   string    `json:"customer_id,omitempty"`
	CustomerSlug string    `json:"customer_slug,omitempty"`
	CustomerName string    `json:"customer_name,omitempty"`
	Currency     string    `json:"currency"`
	// Amount is the event's headline figure: an invoice's total, a payment's
	// amount, a credit note's total, a refund, a fee.
	Amount Decimal `json:"amount"`

	// Where it came from. At least one of these is always set, which is what
	// makes every journal line traceable to the object that produced it.
	StatementID   string `json:"statement_id,omitempty"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	PaymentID     int64  `json:"payment_id,omitempty"`
	CreditNoteID  string `json:"credit_note_id,omitempty"`
	CreditNoteNo  string `json:"credit_note_number,omitempty"`
	RunID         string `json:"reconciliation_id,omitempty"`
	Reference     string `json:"reference,omitempty"`
	Memo          string `json:"memo,omitempty"`

	// An invoice: the LIST revenue per service, what discounts took off it,
	// and the tax rules it was issued under. Revenue − Discount + Tax equals
	// Amount, which is what lets the emitter balance without apportioning.
	Revenue       []ServiceAmount `json:"revenue,omitempty"`
	DiscountTotal Decimal         `json:"discount_total,omitempty"`
	TaxRules      []TaxRule       `json:"tax_rules,omitempty"`
	StatementKind string          `json:"statement_kind,omitempty"`

	// A payment: how the money arrived, and how much of it settled an
	// invoice in THIS period. The remainder is account credit.
	Method    string  `json:"method,omitempty"`
	Gateway   string  `json:"gateway,omitempty"`
	Allocated Decimal `json:"allocated,omitempty"`

	// A credit note: what it took off the invoice, and what became credit
	// on the account because the invoice was already settled that far.
	Applied   Decimal `json:"applied,omitempty"`
	Unapplied Decimal `json:"unapplied,omitempty"`

	// A refund: whether the money it sends back had settled an invoice
	// (`payment`) or was sitting as account credit (`top_up`).
	RefundOf string `json:"refund_of,omitempty"`
}

// financePeriodBounds is PeriodBounds with the store's own ErrInvalid on a
// malformed period, so the API answers 400 for one without a special case.
func financePeriodBounds(period string) (from, to time.Time, err error) {
	from, to, err = PeriodBounds(strings.TrimSpace(period))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: period must be YYYY-MM", ErrInvalid)
	}
	return from, to, nil
}

// JournalEvents reads every financial fact of a period, oldest first. It is
// the ONLY query behind the journal, and it reads append-only material: the
// §9 account ledger, the invoices it points at, and the allocations that
// applied money to them.
func (s *Store) JournalEvents(ctx context.Context, period string) ([]JournalEvent, error) {
	from, to, err := financePeriodBounds(period)
	if err != nil {
		return nil, err
	}
	out := []JournalEvent{}
	ledger, err := s.ledgerEvents(ctx, period, from, to)
	if err != nil {
		return nil, err
	}
	out = append(out, ledger...)
	advances, err := s.advanceEvents(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out = append(out, advances...)
	fees, err := s.feeEvents(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out = append(out, fees...)
	sortEvents(out)
	return out, nil
}

// eventRank orders the kinds within one instant so the journal reads the way
// the money moved: the invoice, then what settled it, then what reduced it.
func eventRank(kind string) int {
	switch kind {
	case EventInvoice:
		return 0
	case EventTopUp:
		return 1
	case EventPayment:
		return 2
	case EventAdvanceApplied:
		return 3
	case EventCreditNote:
		return 4
	case EventWriteOff:
		return 5
	case EventRefund:
		return 6
	}
	return 7
}

// sortEvents is a deterministic total order — the instant, then the kind,
// then the object's own id. A stable order is what makes two exports of the
// same period byte-identical.
func sortEvents(evs []JournalEvent) {
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if ra, rb := eventRank(a.Kind), eventRank(b.Kind); ra != rb {
			return ra < rb
		}
		if a.PaymentID != b.PaymentID {
			return a.PaymentID < b.PaymentID
		}
		if a.StatementID != b.StatementID {
			return a.StatementID < b.StatementID
		}
		if a.CreditNoteID != b.CreditNoteID {
			return a.CreditNoteID < b.CreditNoteID
		}
		return a.Reference < b.Reference
	})
}

// ledgerPeriodExpr is WHICH MONTH a ledger row belongs to (DESIGN.md §18.1).
//
// Revenue is recognised in the month it was EARNED and cash in the month it
// MOVED, which is the ordinary accrual split every finance department works
// in: an invoice for August usage issued on 2 September is August revenue,
// and the payment that settles it in October is October cash. So a row tied
// to a statement — the invoice, a credit note, a write-off — takes the
// statement's BILLING period, and everything else takes the day the money
// moved. Each event's own debit and credit stay together either way, so
// every period still balances on its own.
const ledgerPeriodExpr = `CASE WHEN e.kind IN ('invoice','credit_note','write_off') AND st.period_start IS NOT NULL
		THEN to_char(st.period_start, 'YYYY-MM')
		ELSE to_char(e.entered_at AT TIME ZONE 'UTC', 'YYYY-MM') END`

// ledgerEvents reads the §9 account ledger for the period and fills in what
// each kind needs: an invoice's revenue split and tax rules, a payment's
// same-period allocation, a credit note's applied and unapplied halves.
func (s *Store) ledgerEvents(ctx context.Context, period string, from, to time.Time) ([]JournalEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.kind, e.entered_at, e.customer_id, c.slug, c.name, e.amount::text, e.currency,
		       COALESCE(e.statement_id::text, ''), COALESCE(st.invoice_number, st.external_invoice_ref, ''),
		       COALESCE(e.payment_id, 0), COALESCE(e.credit_note_id::text, ''), COALESCE(n.number, ''),
		       e.reference, e.note,
		       COALESCE(st.subtotal::text, ''), COALESCE(st.tax::text, ''), COALESCE(st.discount_total::text, ''),
		       COALESCE(st.statement_kind, ''), st.tax_snapshot,
		       COALESCE(p.method, ''), COALESCE(p.gateway, ''),
		       COALESCE(n.total::text, '')
		  FROM account_entries e
		  JOIN customers c ON c.id = e.customer_id
		  LEFT JOIN statements st ON st.id = e.statement_id
		  LEFT JOIN credit_notes n ON n.id = e.credit_note_id
		  LEFT JOIN payments p ON p.id = e.payment_id
		 WHERE `+ledgerPeriodExpr+` = $1
		 ORDER BY e.entered_at, e.id`, period)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []JournalEvent{}
	for rows.Next() {
		var ev JournalEvent
		var amount, subtotal, tax, discount, noteTotal string
		var snapshot []byte
		if err := rows.Scan(&ev.Kind, &ev.At, &ev.CustomerID, &ev.CustomerSlug, &ev.CustomerName, &amount, &ev.Currency,
			&ev.StatementID, &ev.InvoiceNumber, &ev.PaymentID, &ev.CreditNoteID, &ev.CreditNoteNo,
			&ev.Reference, &ev.Memo,
			&subtotal, &tax, &discount, &ev.StatementKind, &snapshot,
			&ev.Method, &ev.Gateway, &noteTotal); err != nil {
			return nil, err
		}
		ev.At = ev.At.UTC()
		// The ledger signs its amounts (a credit is negative); the journal
		// speaks in debits and credits, so every event carries the magnitude
		// and its kind says which side it lands on.
		ev.Amount = absDec(Decimal(amount))
		switch ev.Kind {
		case EntryInvoice:
			ev.Kind = EventInvoice
			if ev.Revenue, err = s.invoiceRevenue(ctx, ev.StatementID); err != nil {
				return nil, err
			}
			ev.DiscountTotal = Decimal(discount)
			ev.TaxRules = taxRulesOf(snapshot, Decimal(tax))
			// A statement with no rated lines still has a subtotal; book it
			// as unclassified revenue rather than losing it.
			if len(ev.Revenue) == 0 && !isZeroDec(Decimal(subtotal)) {
				ev.Revenue = []ServiceAmount{{Amount: addDec(Decimal(subtotal), Decimal(discount))}}
			}
		case EntryPayment:
			ev.Kind = EventPayment
			if ev.Allocated, err = s.allocatedInWindow(ctx, ev.PaymentID, from, to); err != nil {
				return nil, err
			}
		case EntryTopUp:
			ev.Kind = EventTopUp
		case EntryCreditNote, EntryWriteOff:
			if ev.Kind == EntryWriteOff {
				ev.Kind = EventWriteOff
			} else {
				ev.Kind = EventCreditNote
			}
			applied, err := s.creditNoteApplied(ctx, ev.CreditNoteID)
			if err != nil {
				return nil, err
			}
			total := Decimal(noteTotal)
			if isZeroDec(total) {
				total = ev.Amount
			}
			ev.Applied = applied
			ev.Unapplied = decOf(new(big.Rat).Sub(ratOf(total), ratOf(applied)))
		case EntryRefund:
			ev.Kind = EventRefund
			kind, err := s.paymentLedgerKind(ctx, ev.PaymentID)
			if err != nil {
				return nil, err
			}
			ev.RefundOf = kind
		default:
			continue
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// invoiceRevenue is an invoice's LIST revenue per service — the rated lines
// grouped by the SKU's first segment, which is the split every other surface
// of this product groups by.
func (s *Store) invoiceRevenue(ctx context.Context, statementID string) ([]ServiceAmount, error) {
	if statementID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CASE WHEN position('.' in sku) > 1 THEN split_part(sku, '.', 1) ELSE sku END AS service,
		       sum(amount)::numeric(20,6)::text
		  FROM rated_lines WHERE statement_id = $1 GROUP BY 1 ORDER BY 1`, statementID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ServiceAmount{}
	for rows.Next() {
		var sa ServiceAmount
		var amt string
		if err := rows.Scan(&sa.Service, &amt); err != nil {
			return nil, err
		}
		sa.Amount = Decimal(amt)
		out = append(out, sa)
	}
	return out, rows.Err()
}

// allocatedInWindow is how much of a payment settled invoices INSIDE the
// window. A later allocation belongs to the period it was made in, as an
// advance-credit application — which is what keeps each period's journal
// local to itself and a closed one stable.
func (s *Store) allocatedInWindow(ctx context.Context, paymentID int64, from, to time.Time) (Decimal, error) {
	if paymentID == 0 {
		return "0", nil
	}
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(amount), 0)::numeric(20,6)::text FROM invoice_allocations
		WHERE payment_id = $1 AND allocated_at >= $2 AND allocated_at < $3`, paymentID, from, to).Scan(&v); err != nil {
		return "0", mapErr(err)
	}
	return Decimal(v), nil
}

// creditNoteApplied is what a credit note actually took off its invoice.
func (s *Store) creditNoteApplied(ctx context.Context, creditNoteID string) (Decimal, error) {
	if creditNoteID == "" {
		return "0", nil
	}
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(amount), 0)::numeric(20,6)::text FROM invoice_allocations WHERE credit_note_id = $1`, creditNoteID).Scan(&v); err != nil {
		return "0", mapErr(err)
	}
	return Decimal(v), nil
}

// paymentLedgerKind says how a payment first landed: against an invoice
// (`payment`) or as account credit (`top_up`). A refund reverses whichever
// it was.
func (s *Store) paymentLedgerKind(ctx context.Context, paymentID int64) (string, error) {
	if paymentID == 0 {
		return EntryPayment, nil
	}
	var kind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM account_entries WHERE payment_id = $1 AND kind IN ('payment','top_up') ORDER BY id LIMIT 1`, paymentID).Scan(&kind)
	if err == sql.ErrNoRows {
		return EntryPayment, nil
	}
	if err != nil {
		return "", mapErr(err)
	}
	return kind, nil
}

// advanceEvents are the applications of account credit to an invoice that
// happened in the window but whose money arrived in an earlier one: a
// top-up's allocation, whenever it is made, and a payment's allocation made
// after the period its payment landed in.
func (s *Store) advanceEvents(ctx context.Context, from, to time.Time) ([]JournalEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.allocated_at, a.amount::text, p.id, p.customer_id, c.slug, c.name,
		       a.statement_id, COALESCE(st.invoice_number, st.external_invoice_ref, ''), st.currency, e.kind, e.entered_at, COALESCE(p.reference, '')
		  FROM invoice_allocations a
		  JOIN payments p ON p.id = a.payment_id
		  JOIN customers c ON c.id = p.customer_id
		  JOIN statements st ON st.id = a.statement_id
		  JOIN account_entries e ON e.payment_id = p.id AND e.kind IN ('payment','top_up')
		 WHERE a.payment_id IS NOT NULL AND a.allocated_at >= $1 AND a.allocated_at < $2
		 ORDER BY a.allocated_at, a.id`, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []JournalEvent{}
	for rows.Next() {
		var id int64
		var at, entered time.Time
		var amount, kind string
		var ev JournalEvent
		if err := rows.Scan(&id, &at, &amount, &ev.PaymentID, &ev.CustomerID, &ev.CustomerSlug, &ev.CustomerName,
			&ev.StatementID, &ev.InvoiceNumber, &ev.Currency, &kind, &entered, &ev.Reference); err != nil {
			return nil, err
		}
		// A payment recorded against an invoice already books the receivable
		// for what it settled in its own period; only what it applies LATER
		// is an advance being consumed.
		if kind == EntryPayment && !entered.UTC().Before(from) && entered.UTC().Before(to) {
			continue
		}
		ev.Kind = EventAdvanceApplied
		ev.At = at.UTC()
		ev.Amount = Decimal(amount)
		ev.Memo = "account credit applied to " + ev.InvoiceNumber
		out = append(out, ev)
	}
	return out, rows.Err()
}

// feeEvents are the gateway fees a reconciliation run recorded in the
// window. A fee is a real cost of collecting and gets its own journal lines;
// it is discovered by reconciliation, never by us guessing.
func (s *Store) feeEvents(ctx context.Context, from, to time.Time) ([]JournalEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.run_id::text, l.gateway_reference, l.fee::text, l.currency, l.settled_date, COALESCE(l.payment_id, 0),
		       COALESCE(l.customer_id::text, ''), COALESCE(c.slug, ''), COALESCE(l.customer_name, ''), r.gateway
		  FROM finance_reconciliation_lines l
		  JOIN finance_reconciliations r ON r.id = l.run_id
		  LEFT JOIN customers c ON c.id = l.customer_id
		 WHERE l.fee <> 0 AND l.settled_date >= $1::date AND l.settled_date < $2::date
		 ORDER BY l.settled_date, l.id`, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []JournalEvent{}
	for rows.Next() {
		var ev JournalEvent
		var fee string
		var settled time.Time
		if err := rows.Scan(&ev.RunID, &ev.Reference, &fee, &ev.Currency, &settled, &ev.PaymentID,
			&ev.CustomerID, &ev.CustomerSlug, &ev.CustomerName, &ev.Gateway); err != nil {
			return nil, err
		}
		ev.Kind = EventGatewayFee
		ev.At = settled.UTC()
		ev.Amount = Decimal(fee)
		ev.Memo = "settlement fee on " + ev.Reference
		out = append(out, ev)
	}
	return out, rows.Err()
}

// taxRulesOf reads an invoice's frozen tax snapshot for the rules its tax was
// computed at. It is deliberately TOLERANT of the snapshot's shape: a
// snapshot with a `rules` (or `lines`) array yields one rule per entry, and
// one without yields the single rate the snapshot always carried. Nothing
// here recomputes tax and nothing here depends on a rule engine — the
// invoice froze the figures, and the journal posts them.
func taxRulesOf(snapshot []byte, tax Decimal) []TaxRule {
	if isZeroDec(tax) {
		return nil
	}
	single := []TaxRule{{Tax: tax}}
	if len(snapshot) == 0 {
		return single
	}
	var raw struct {
		Rate  Decimal `json:"rate"`
		Rules []struct {
			Code   string  `json:"code"`
			Name   string  `json:"name"`
			Rate   Decimal `json:"rate"`
			Tax    Decimal `json:"tax"`
			Amount Decimal `json:"amount"`
		} `json:"rules"`
		Lines []struct {
			Code   string  `json:"code"`
			Name   string  `json:"name"`
			Rate   Decimal `json:"rate"`
			Tax    Decimal `json:"tax"`
			Amount Decimal `json:"amount"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(snapshot, &raw); err != nil {
		return single
	}
	single[0].Rate = raw.Rate
	entries := raw.Rules
	if len(entries) == 0 {
		entries = raw.Lines
	}
	if len(entries) == 0 {
		return single
	}
	out := make([]TaxRule, 0, len(entries))
	for _, e := range entries {
		t := e.Tax
		if strings.TrimSpace(string(t)) == "" {
			t = e.Amount
		}
		out = append(out, TaxRule{Code: e.Code, Name: e.Name, Rate: e.Rate, Tax: t})
	}
	// The rules must account for exactly the tax the invoice froze; the
	// difference (a rounding remainder, or a snapshot written by a reader
	// that rounded differently) lands on the last rule so the journal
	// balances against the invoice rather than against the snapshot.
	sum := new(big.Rat)
	for _, r := range out {
		sum.Add(sum, ratOf(r.Tax))
	}
	if diff := new(big.Rat).Sub(ratOf(tax), sum); diff.Sign() != 0 {
		last := &out[len(out)-1]
		last.Tax = decOf(new(big.Rat).Add(ratOf(last.Tax), diff))
	}
	return out
}

// absDec is |d| — the ledger signs its amounts, the journal does not.
func absDec(d Decimal) Decimal {
	r := ratOf(d)
	if r.Sign() < 0 {
		r.Neg(r)
	}
	return decOf(r)
}

// ---------------------------------------------------------------------------
// the period
// ---------------------------------------------------------------------------

// Period statuses. `open` is never a stored row — the absence of one is open.
const (
	PeriodOpen     = "open"
	PeriodClosed   = "closed"
	PeriodReopened = "reopened"
)

// FinancePeriod is one month's close state.
type FinancePeriod struct {
	Period      string     `json:"period"`
	Status      string     `json:"status"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	ClosedBy    string     `json:"closed_by,omitempty"`
	TotalDebit  Decimal    `json:"total_debit,omitempty"`
	TotalCredit Decimal    `json:"total_credit,omitempty"`
	Lines       int        `json:"lines,omitempty"`
	ReopenedAt  *time.Time `json:"reopened_at,omitempty"`
	ReopenedBy  string     `json:"reopened_by,omitempty"`
	Reason      string     `json:"reopen_reason,omitempty"`
}

// IsClosed reports whether the period is closed to financial change. A
// reopened period is open again — that is the whole point of reopening it.
func (p FinancePeriod) IsClosed() bool { return p.Status == PeriodClosed }

// GetFinancePeriod reads one period's state; a month nobody closed reads
// open, with no row behind it.
func (s *Store) GetFinancePeriod(ctx context.Context, period string) (FinancePeriod, error) {
	if _, _, err := PeriodBounds(period); err != nil {
		return FinancePeriod{}, err
	}
	p := FinancePeriod{Period: period, Status: PeriodOpen}
	var closedAt time.Time
	var reopenedAt sql.NullTime
	var debit, credit string
	err := s.db.QueryRowContext(ctx, `SELECT status, closed_at, closed_by, total_debit::text, total_credit::text, lines, reopened_at, reopened_by, reopen_reason
		FROM finance_periods WHERE period = $1`, period).
		Scan(&p.Status, &closedAt, &p.ClosedBy, &debit, &credit, &p.Lines, &reopenedAt, &p.ReopenedBy, &p.Reason)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	c := closedAt.UTC()
	p.ClosedAt, p.TotalDebit, p.TotalCredit = &c, Decimal(debit), Decimal(credit)
	p.ReopenedAt = timePtr(reopenedAt)
	return p, nil
}

// ListFinancePeriods returns every period that was ever closed, newest first.
func (s *Store) ListFinancePeriods(ctx context.Context, limit int) ([]FinancePeriod, error) {
	if limit <= 0 || limit > 240 {
		limit = 36
	}
	rows, err := s.db.QueryContext(ctx, `SELECT period, status, closed_at, closed_by, total_debit::text, total_credit::text, lines, reopened_at, reopened_by, reopen_reason
		FROM finance_periods ORDER BY period DESC LIMIT $1`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []FinancePeriod{}
	for rows.Next() {
		var p FinancePeriod
		var closedAt time.Time
		var reopenedAt sql.NullTime
		var debit, credit string
		if err := rows.Scan(&p.Period, &p.Status, &closedAt, &p.ClosedBy, &debit, &credit, &p.Lines, &reopenedAt, &p.ReopenedBy, &p.Reason); err != nil {
			return nil, err
		}
		c := closedAt.UTC()
		p.ClosedAt, p.TotalDebit, p.TotalCredit = &c, Decimal(debit), Decimal(credit)
		p.ReopenedAt = timePtr(reopenedAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PeriodBlocker is one thing standing between a period and its close, named
// so the operator can go and deal with it.
type PeriodBlocker struct {
	Kind          string `json:"kind"` // draft-statement | open-dispute
	ID            string `json:"id"`
	CustomerID    string `json:"customer_id,omitempty"`
	CustomerName  string `json:"customer_name,omitempty"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// Blocker kinds.
const (
	BlockerDraftStatement = "draft-statement"
	BlockerOpenDispute    = "open-dispute"
)

// PeriodBlockers lists the drafts and open disputes of a period. A draft is
// a bill nobody decided on yet and an open dispute is a figure the customer
// says is wrong; closing over either would freeze a number that is still
// moving.
func (s *Store) PeriodBlockers(ctx context.Context, period string) ([]PeriodBlocker, error) {
	if _, _, err := PeriodBounds(period); err != nil {
		return nil, err
	}
	out := []PeriodBlocker{}
	rows, err := s.db.QueryContext(ctx, `SELECT st.id::text, st.customer_id::text, c.name, COALESCE(st.invoice_number, '')
		FROM statements st JOIN customers c ON c.id = st.customer_id
		WHERE st.status = 'draft' AND left(st.period_start::text, 7) = $1 ORDER BY c.name, st.id`, period)
	if err != nil {
		return nil, mapErr(err)
	}
	for rows.Next() {
		b := PeriodBlocker{Kind: BlockerDraftStatement}
		if err := rows.Scan(&b.ID, &b.CustomerID, &b.CustomerName, &b.InvoiceNumber); err != nil {
			rows.Close()
			return nil, err
		}
		b.Detail = "statement " + b.ID + " for " + b.CustomerName + " is still a draft"
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT d.id::text, d.customer_id::text, c.name, COALESCE(st.invoice_number, ''), d.reason
		FROM statement_disputes d JOIN statements st ON st.id = d.statement_id JOIN customers c ON c.id = d.customer_id
		WHERE d.status = 'open' AND left(st.period_start::text, 7) = $1 ORDER BY c.name, d.id`, period)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		b := PeriodBlocker{Kind: BlockerOpenDispute}
		var reason string
		if err := rows.Scan(&b.ID, &b.CustomerID, &b.CustomerName, &b.InvoiceNumber, &reason); err != nil {
			return nil, err
		}
		label := b.InvoiceNumber
		if label == "" {
			label = "an invoice"
		}
		b.Detail = customerLabel(b.CustomerName) + " disputes " + label + ": " + reason
		out = append(out, b)
	}
	return out, rows.Err()
}

// c is a tiny helper that keeps an empty customer name from reading as a
// sentence starting with a space.
func customerLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "a customer"
	}
	return name
}

// StoredJournalLine is one line of a CLOSED period's frozen journal.
type StoredJournalLine struct {
	Seq          int     `json:"seq"`
	Date         string  `json:"date"`
	EventKind    string  `json:"event"`
	AccountKey   string  `json:"account_key"`
	AccountCode  string  `json:"account_code"`
	AccountName  string  `json:"account_name,omitempty"`
	Debit        Decimal `json:"debit"`
	Credit       Decimal `json:"credit"`
	Currency     string  `json:"currency"`
	CustomerID   string  `json:"customer_id,omitempty"`
	CustomerSlug string  `json:"customer_slug,omitempty"`
	CustomerName string  `json:"customer_name,omitempty"`
	SourceKind   string  `json:"source_kind,omitempty"`
	SourceID     string  `json:"source_id,omitempty"`
	Reference    string  `json:"reference,omitempty"`
	Memo         string  `json:"memo,omitempty"`
}

// ClosePeriod stamps a period closed and FREEZES the journal it was closed
// on, in one transaction. The caller has already emitted and balanced the
// lines — the balance assertion lives in internal/finance, with the emitter
// that produced them — and closing writes exactly what it was given.
func (s *Store) ClosePeriod(ctx context.Context, period string, lines []StoredJournalLine, totalDebit, totalCredit Decimal, actor string) (FinancePeriod, error) {
	if _, _, err := PeriodBounds(period); err != nil {
		return FinancePeriod{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinancePeriod{}, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM finance_periods WHERE period = $1 FOR UPDATE`, period).Scan(&status)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return FinancePeriod{}, mapErr(err)
	case status == PeriodClosed:
		return FinancePeriod{}, fmt.Errorf("%w: %s is already closed", ErrConflict, period)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO finance_periods (period, status, closed_at, closed_by, total_debit, total_credit, lines)
		VALUES ($1, 'closed', now(), $2, $3::numeric, $4::numeric, $5)
		ON CONFLICT (period) DO UPDATE SET status = 'closed', closed_at = now(), closed_by = EXCLUDED.closed_by,
			total_debit = EXCLUDED.total_debit, total_credit = EXCLUDED.total_credit, lines = EXCLUDED.lines,
			reopened_at = NULL, reopened_by = '', reopen_reason = ''`,
		period, actor, string(totalDebit), string(totalCredit), len(lines)); err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM finance_journal_lines WHERE period = $1`, period); err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("finance_journal_lines", "period", "seq", "entry_date", "event_kind", "account_key", "account_code", "account_name",
		"debit", "credit", "currency", "customer_id", "customer_slug", "customer_name", "source_kind", "source_id", "reference", "memo"))
	if err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	for _, l := range lines {
		var customer any
		if l.CustomerID != "" {
			customer = l.CustomerID
		}
		if _, err := stmt.ExecContext(ctx, period, l.Seq, l.Date, l.EventKind, l.AccountKey, l.AccountCode, l.AccountName,
			zeroIfEmpty(l.Debit), zeroIfEmpty(l.Credit), l.Currency, customer, l.CustomerSlug, l.CustomerName, l.SourceKind, l.SourceID, l.Reference, l.Memo); err != nil {
			stmt.Close()
			return FinancePeriod{}, mapErr(err)
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return FinancePeriod{}, mapErr(err)
	}
	if err := stmt.Close(); err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return FinancePeriod{}, err
	}
	return s.GetFinancePeriod(ctx, period)
}

func zeroIfEmpty(d Decimal) string {
	if strings.TrimSpace(string(d)) == "" {
		return "0"
	}
	return string(d)
}

// ReopenPeriod lifts the close, recording who lifted it and why. The frozen
// journal is KEPT: a reopened period's export is derived live again, and the
// lines the close asserted stay readable as what the books said at the time.
func (s *Store) ReopenPeriod(ctx context.Context, period, reason, actor string) (FinancePeriod, error) {
	if _, _, err := PeriodBounds(period); err != nil {
		return FinancePeriod{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return FinancePeriod{}, fmt.Errorf("%w: reopening a closed period needs a reason", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE finance_periods SET status = 'reopened', reopened_at = now(), reopened_by = $2, reopen_reason = $3
		WHERE period = $1 AND status = 'closed'`, period, actor, strings.TrimSpace(reason))
	if err != nil {
		return FinancePeriod{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		p, gerr := s.GetFinancePeriod(ctx, period)
		if gerr != nil {
			return FinancePeriod{}, gerr
		}
		return FinancePeriod{}, fmt.Errorf("%w: %s is not closed, so there is nothing to reopen", ErrConflict, p.Period)
	}
	return s.GetFinancePeriod(ctx, period)
}

// StoredJournal returns a closed period's frozen lines in posting order.
func (s *Store) StoredJournal(ctx context.Context, period string) ([]StoredJournalLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, to_char(entry_date, 'YYYY-MM-DD'), event_kind, account_key, account_code, account_name,
		debit::text, credit::text, currency, COALESCE(customer_id::text, ''), customer_slug, customer_name, source_kind, source_id, reference, memo
		FROM finance_journal_lines WHERE period = $1 ORDER BY seq`, period)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []StoredJournalLine{}
	for rows.Next() {
		var l StoredJournalLine
		var debit, credit string
		if err := rows.Scan(&l.Seq, &l.Date, &l.EventKind, &l.AccountKey, &l.AccountCode, &l.AccountName,
			&debit, &credit, &l.Currency, &l.CustomerID, &l.CustomerSlug, &l.CustomerName, &l.SourceKind, &l.SourceID, &l.Reference, &l.Memo); err != nil {
			return nil, err
		}
		l.Debit, l.Credit = Decimal(debit), Decimal(credit)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// reconciliation runs
// ---------------------------------------------------------------------------

// Reconciliation buckets — the four a settlement run reports, plus the
// duplicate a file that names the same gateway reference twice lands in.
const (
	BucketMatched             = "matched"
	BucketAmountMismatch      = "amount-mismatch"
	BucketMissingInLedger     = "missing-in-ledger"
	BucketMissingInSettlement = "missing-in-settlement"
	BucketDuplicate           = "duplicate"
)

// ReconciliationBuckets lists the buckets in report order.
var ReconciliationBuckets = []string{BucketMatched, BucketAmountMismatch, BucketMissingInLedger, BucketMissingInSettlement, BucketDuplicate}

// Reconciliation sources.
const (
	ReconcileFromFile    = "file"
	ReconcileFromGateway = "gateway"
)

// ReconciliationLine is one settlement line in its bucket.
type ReconciliationLine struct {
	ID               int64    `json:"id,omitempty"`
	Bucket           string   `json:"bucket"`
	GatewayReference string   `json:"gateway_reference,omitempty"`
	SettledAmount    *Decimal `json:"settled_amount,omitempty"`
	LedgerAmount     *Decimal `json:"ledger_amount,omitempty"`
	Difference       *Decimal `json:"difference,omitempty"`
	Fee              Decimal  `json:"fee"`
	Currency         string   `json:"currency,omitempty"`
	SettledDate      string   `json:"settled_date,omitempty"`
	PaymentID        int64    `json:"payment_id,omitempty"`
	CustomerID       string   `json:"customer_id,omitempty"`
	CustomerName     string   `json:"customer_name,omitempty"`
	Detail           string   `json:"detail,omitempty"`
}

// ReconciliationRun is one settlement run and its four buckets.
type ReconciliationRun struct {
	ID                  string               `json:"id"`
	Gateway             string               `json:"gateway,omitempty"`
	Source              string               `json:"source"`
	FileName            string               `json:"file_name,omitempty"`
	From                string               `json:"from,omitempty"`
	To                  string               `json:"to,omitempty"`
	Currency            string               `json:"currency,omitempty"`
	Matched             int                  `json:"matched"`
	Mismatched          int                  `json:"amount_mismatched"`
	MissingInLedger     int                  `json:"missing_in_ledger"`
	MissingInSettlement int                  `json:"missing_in_settlement"`
	Duplicates          int                  `json:"duplicates"`
	SettledTotal        Decimal              `json:"settled_total"`
	LedgerTotal         Decimal              `json:"ledger_total"`
	FeeTotal            Decimal              `json:"fee_total"`
	RanAt               time.Time            `json:"ran_at"`
	RanBy               string               `json:"ran_by,omitempty"`
	Lines               []ReconciliationLine `json:"lines,omitempty"`
}

// SettledPayment is one payment as reconciliation sees it: the gateway
// reference it carries, what we recorded, and who it belongs to.
type SettledPayment struct {
	PaymentID    int64
	Reference    string
	Amount       Decimal
	Currency     string
	PaidAt       time.Time
	Gateway      string
	CustomerID   string
	CustomerName string
}

// GatewayPayments lists the settled payments a reconciliation run compares
// against: received payments in the window, optionally of one gateway.
func (s *Store) GatewayPayments(ctx context.Context, gateway string, from, to time.Time) ([]SettledPayment, error) {
	q := `SELECT p.id, COALESCE(p.reference, ''), p.amount::text, COALESCE(st.currency, ''), p.paid_at, COALESCE(p.gateway, ''), p.customer_id::text, c.name
		FROM payments p JOIN customers c ON c.id = p.customer_id LEFT JOIN statements st ON st.id = p.statement_id
		WHERE p.status = 'received' AND p.paid_at >= $1 AND p.paid_at < $2`
	args := []any{from, to}
	if g := strings.ToLower(strings.TrimSpace(gateway)); g != "" {
		q += ` AND lower(p.gateway) = $3`
		args = append(args, g)
	}
	q += ` ORDER BY p.paid_at, p.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SettledPayment{}
	for rows.Next() {
		var p SettledPayment
		var amount string
		if err := rows.Scan(&p.PaymentID, &p.Reference, &amount, &p.Currency, &p.PaidAt, &p.Gateway, &p.CustomerID, &p.CustomerName); err != nil {
			return nil, err
		}
		p.Amount, p.PaidAt = Decimal(amount), p.PaidAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveReconciliation stores a run and its lines in one transaction and
// returns it as stored. It CORRECTS NOTHING: no payment is created, amended
// or reallocated by a run — the four buckets are a report a human acts on.
func (s *Store) SaveReconciliation(ctx context.Context, run ReconciliationRun) (ReconciliationRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReconciliationRun{}, err
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO finance_reconciliations
		(gateway, source, file_name, from_date, to_date, currency, matched, mismatched, missing_in_ledger, missing_in_settlement, duplicates, settled_total, ledger_total, fee_total, ran_by)
		VALUES ($1, $2, $3, NULLIF($4,'')::date, NULLIF($5,'')::date, $6, $7, $8, $9, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15) RETURNING id::text`,
		run.Gateway, run.Source, run.FileName, run.From, run.To, run.Currency,
		run.Matched, run.Mismatched, run.MissingInLedger, run.MissingInSettlement, run.Duplicates,
		zeroIfEmpty(run.SettledTotal), zeroIfEmpty(run.LedgerTotal), zeroIfEmpty(run.FeeTotal), run.RanBy).Scan(&id); err != nil {
		return ReconciliationRun{}, mapErr(err)
	}
	for _, l := range run.Lines {
		var customer any
		if l.CustomerID != "" {
			customer = l.CustomerID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO finance_reconciliation_lines
			(run_id, bucket, gateway_reference, settled_amount, ledger_amount, difference, fee, currency, settled_date, payment_id, customer_id, customer_name, detail)
			VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7::numeric, $8, NULLIF($9,'')::date, NULLIF($10,0), $11, $12, $13)`,
			id, l.Bucket, l.GatewayReference, nullDec(l.SettledAmount), nullDec(l.LedgerAmount), nullDec(l.Difference), zeroIfEmpty(l.Fee),
			l.Currency, l.SettledDate, l.PaymentID, customer, l.CustomerName, l.Detail); err != nil {
			return ReconciliationRun{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ReconciliationRun{}, err
	}
	return s.GetReconciliation(ctx, id)
}

// GetReconciliation reads one run with its lines.
func (s *Store) GetReconciliation(ctx context.Context, id string) (ReconciliationRun, error) {
	var r ReconciliationRun
	var from, to sql.NullString
	var settled, ledger, fee string
	if err := s.db.QueryRowContext(ctx, `SELECT id::text, gateway, source, file_name, to_char(from_date, 'YYYY-MM-DD'), to_char(to_date, 'YYYY-MM-DD'), currency,
		matched, mismatched, missing_in_ledger, missing_in_settlement, duplicates, settled_total::text, ledger_total::text, fee_total::text, ran_at, ran_by
		FROM finance_reconciliations WHERE id = $1`, id).
		Scan(&r.ID, &r.Gateway, &r.Source, &r.FileName, &from, &to, &r.Currency,
			&r.Matched, &r.Mismatched, &r.MissingInLedger, &r.MissingInSettlement, &r.Duplicates, &settled, &ledger, &fee, &r.RanAt, &r.RanBy); err != nil {
		return ReconciliationRun{}, mapErr(err)
	}
	r.From, r.To = from.String, to.String
	r.SettledTotal, r.LedgerTotal, r.FeeTotal = Decimal(settled), Decimal(ledger), Decimal(fee)
	r.RanAt = r.RanAt.UTC()
	lines, err := s.reconciliationLines(ctx, r.ID)
	if err != nil {
		return ReconciliationRun{}, err
	}
	r.Lines = lines
	return r, nil
}

func (s *Store) reconciliationLines(ctx context.Context, runID string) ([]ReconciliationLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, bucket, gateway_reference, settled_amount::text, ledger_amount::text, difference::text, fee::text, currency,
		COALESCE(to_char(settled_date, 'YYYY-MM-DD'), ''), COALESCE(payment_id, 0), COALESCE(customer_id::text, ''), customer_name, detail
		FROM finance_reconciliation_lines WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReconciliationLine{}
	for rows.Next() {
		var l ReconciliationLine
		var settled, ledger, diff sql.NullString
		var fee string
		if err := rows.Scan(&l.ID, &l.Bucket, &l.GatewayReference, &settled, &ledger, &diff, &fee, &l.Currency,
			&l.SettledDate, &l.PaymentID, &l.CustomerID, &l.CustomerName, &l.Detail); err != nil {
			return nil, err
		}
		l.Fee = Decimal(fee)
		l.SettledAmount, l.LedgerAmount, l.Difference = decPtr(settled), decPtr(ledger), decPtr(diff)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListReconciliations returns the runs without their lines, newest first.
func (s *Store) ListReconciliations(ctx context.Context, limit int) ([]ReconciliationRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, gateway, source, file_name, to_char(from_date, 'YYYY-MM-DD'), to_char(to_date, 'YYYY-MM-DD'), currency,
		matched, mismatched, missing_in_ledger, missing_in_settlement, duplicates, settled_total::text, ledger_total::text, fee_total::text, ran_at, ran_by
		FROM finance_reconciliations ORDER BY ran_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReconciliationRun{}
	for rows.Next() {
		var r ReconciliationRun
		var from, to sql.NullString
		var settled, ledger, fee string
		if err := rows.Scan(&r.ID, &r.Gateway, &r.Source, &r.FileName, &from, &to, &r.Currency,
			&r.Matched, &r.Mismatched, &r.MissingInLedger, &r.MissingInSettlement, &r.Duplicates, &settled, &ledger, &fee, &r.RanAt, &r.RanBy); err != nil {
			return nil, err
		}
		r.From, r.To = from.String, to.String
		r.SettledTotal, r.LedgerTotal, r.FeeTotal = Decimal(settled), Decimal(ledger), Decimal(fee)
		r.RanAt = r.RanAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
