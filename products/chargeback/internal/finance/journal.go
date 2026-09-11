// Package finance is the FINANCE HANDOVER (DESIGN.md §18, EPIC #6867): the
// double-entry journal an operator's finance department posts, the
// reconciliation of a gateway's settlement file against what this product
// recorded, and the arithmetic a period close asserts before it freezes a
// month.
//
// It is PURE. No database, no HTTP, no clock: events in, journal lines out,
// and a balance assertion that REFUSES rather than warns. The store reads the
// events (internal/store/ledgerexport.go) and the API serves them
// (internal/api/finance.go); neither decides an account code, because account
// codes are the operator's data and never a string in an emitter.
//
// # The rule the whole file exists for
//
// Total debits equal total credits, in every currency and overall, asserted
// before an export is written. A batch that does not balance is an error
// naming the difference — it is never exported with a note attached, because
// a figure a finance department cannot post is worse than no figure.
package finance

import (
	"encoding/csv"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Account is one mapped account: the key this product books to, the code the
// operator posts to, and the name that code carries on their chart.
type Account struct {
	Key  string
	Code string
	Name string
}

// Accounts is the operator's chart of accounts, keyed by our key.
type Accounts map[string]Account

// AccountsOf builds the lookup from the stored map.
func AccountsOf(rows []store.AccountMapping) Accounts {
	out := Accounts{}
	for _, m := range rows {
		if strings.TrimSpace(m.AccountCode) == "" {
			continue
		}
		out[m.Key] = Account{Key: m.Key, Code: strings.TrimSpace(m.AccountCode), Name: strings.TrimSpace(m.Description)}
	}
	return out
}

// revenueKey is the account key an invoice's revenue for one service books
// to: `revenue.<service>` when the operator mapped that service, else the
// catch-all `revenue`. A statement kind of `commission` books to the
// commission account instead — the Sovereign is paying a partner, not
// earning from a customer.
func (a Accounts) revenueKey(service, statementKind string) string {
	if statementKind == store.StatementKindCommission {
		return store.AccountCommission
	}
	if service != "" {
		key := store.RevenueKeyPrefix + strings.ToLower(service)
		if _, ok := a[key]; ok {
			return key
		}
	}
	return store.AccountRevenue
}

// cashKey is where money that MOVED lands: a gateway's clearing account when
// a gateway collected it, cash for a transfer or an internal recharge.
func (a Accounts) cashKey(method, gateway string) string {
	if method != store.PaymentMethodGateway {
		return store.AccountCash
	}
	if g := strings.ToLower(strings.TrimSpace(gateway)); g == "" || g == "manual" {
		return store.AccountCash
	}
	if _, ok := a[store.AccountGatewayClearing]; ok {
		return store.AccountGatewayClearing
	}
	return store.AccountCash
}

// Line is one journal line. It always carries the object it came from —
// SourceKind plus SourceID — so any figure in the export traces back to the
// statement, payment or credit note that produced it.
type Line struct {
	Seq          int           `json:"seq"`
	Date         string        `json:"date"`
	EventKind    string        `json:"event"`
	AccountKey   string        `json:"account_key"`
	AccountCode  string        `json:"account_code"`
	AccountName  string        `json:"account_name,omitempty"`
	Debit        store.Decimal `json:"debit"`
	Credit       store.Decimal `json:"credit"`
	Currency     string        `json:"currency"`
	CustomerID   string        `json:"customer_id,omitempty"`
	CustomerSlug string        `json:"customer_slug,omitempty"`
	CustomerName string        `json:"customer_name,omitempty"`
	SourceKind   string        `json:"source_kind"`
	SourceID     string        `json:"source_id"`
	Reference    string        `json:"reference,omitempty"`
	Memo         string        `json:"memo,omitempty"`
}

// Source kinds a line traces back to.
const (
	SourceStatement      = "statement"
	SourcePayment        = "payment"
	SourceCreditNote     = "credit_note"
	SourceReconciliation = "reconciliation"
)

// CurrencyTotal is one currency's side of the batch.
type CurrencyTotal struct {
	Currency string        `json:"currency"`
	Debit    store.Decimal `json:"debit"`
	Credit   store.Decimal `json:"credit"`
}

// Batch is a period's journal: the lines, the totals the balance assertion
// checked, and the account keys the mapping had no code for — which is
// almost always WHY a batch failed to balance, so it is reported beside the
// difference rather than left for the operator to deduce.
type Batch struct {
	Period      string          `json:"period"`
	Lines       []Line          `json:"lines"`
	TotalDebit  store.Decimal   `json:"total_debit"`
	TotalCredit store.Decimal   `json:"total_credit"`
	ByCurrency  []CurrencyTotal `json:"by_currency"`
	Unmapped    []string        `json:"unmapped_account_keys,omitempty"`
	// Balanced is always true on a Batch a caller received: Emit refuses an
	// unbalanced one. It is carried so the console can show the check as a
	// figure rather than as a claim.
	Balanced bool `json:"balanced"`
}

// ErrUnbalanced is the refusal. Every caller treats it as fatal: an
// unbalanced journal is not exported, not stored and never closes a period.
type ErrUnbalanced struct {
	Period      string
	Debit       store.Decimal
	Credit      store.Decimal
	Difference  store.Decimal
	Currency    string
	Unmapped    []string
	LineCount   int
	MappedCount int
}

func (e *ErrUnbalanced) Error() string {
	where := "the journal for " + e.Period
	if e.Currency != "" {
		where += " in " + e.Currency
	}
	msg := fmt.Sprintf("%s does not balance: debits %s, credits %s, a difference of %s across %d lines",
		where, e.Debit, e.Credit, e.Difference, e.LineCount)
	if len(e.Unmapped) > 0 {
		msg += "; no account is mapped for " + strings.Join(e.Unmapped, ", ")
	}
	return msg
}

// Is lets callers match the refusal with errors.Is against store.ErrInvalid,
// so an HTTP layer answers 400 for it without knowing this type.
func (e *ErrUnbalanced) Is(target error) bool { return target == store.ErrInvalid }

// Emit turns a period's events into the journal, asserts that it balances,
// and refuses if it does not.
//
// The event-to-journal table (DESIGN.md §18.2):
//
//	invoice issued      Dr receivable (total)         Cr revenue.<service> (list, per service)
//	                    Dr discounts (discount total) Cr tax_payable (per tax rule)
//	payment received    Dr cash|gateway_clearing      Cr receivable (what it settled this period)
//	                                                  Cr customer_advances (the remainder)
//	top-up              Dr cash|gateway_clearing      Cr customer_advances
//	advance applied     Dr customer_advances          Cr receivable
//	credit note         Dr credit_notes (total)       Cr receivable (applied) + customer_advances (unapplied)
//	write-off           Dr write_offs (total)         Cr receivable (applied) + customer_advances (unapplied)
//	refund              Dr receivable|customer_advances  Cr cash|gateway_clearing
//	settlement fee      Dr gateway_fees               Cr gateway_clearing
//
// An invoice books its LIST revenue and the discount as a contra line rather
// than apportioning the discount across services: total = list − discount +
// tax, so both sides are exact with no rounding to spread.
func Emit(period string, events []store.JournalEvent, accounts Accounts) (Batch, error) {
	b := Batch{Period: period, Lines: []Line{}}
	unmapped := map[string]bool{}
	e := &emitter{accounts: accounts, unmapped: unmapped}
	for _, ev := range events {
		e.event(ev)
	}
	for i := range e.lines {
		e.lines[i].Seq = i + 1
	}
	b.Lines = e.lines
	for k := range unmapped {
		b.Unmapped = append(b.Unmapped, k)
	}
	sort.Strings(b.Unmapped)

	debit, credit := new(big.Rat), new(big.Rat)
	perCurrency := map[string][2]*big.Rat{}
	for _, l := range b.Lines {
		d, c := rat(l.Debit), rat(l.Credit)
		debit.Add(debit, d)
		credit.Add(credit, c)
		cur := l.Currency
		t, ok := perCurrency[cur]
		if !ok {
			t = [2]*big.Rat{new(big.Rat), new(big.Rat)}
			perCurrency[cur] = t
		}
		t[0].Add(t[0], d)
		t[1].Add(t[1], c)
	}
	b.TotalDebit, b.TotalCredit = dec(debit), dec(credit)
	currencies := make([]string, 0, len(perCurrency))
	for cur := range perCurrency {
		currencies = append(currencies, cur)
	}
	sort.Strings(currencies)
	for _, cur := range currencies {
		t := perCurrency[cur]
		b.ByCurrency = append(b.ByCurrency, CurrencyTotal{Currency: cur, Debit: dec(t[0]), Credit: dec(t[1])})
		if t[0].Cmp(t[1]) != 0 {
			return Batch{}, &ErrUnbalanced{Period: period, Currency: cur, Debit: dec(t[0]), Credit: dec(t[1]),
				Difference: dec(new(big.Rat).Sub(t[0], t[1])), Unmapped: b.Unmapped, LineCount: len(b.Lines)}
		}
	}
	if debit.Cmp(credit) != 0 {
		return Batch{}, &ErrUnbalanced{Period: period, Debit: b.TotalDebit, Credit: b.TotalCredit,
			Difference: dec(new(big.Rat).Sub(debit, credit)), Unmapped: b.Unmapped, LineCount: len(b.Lines)}
	}
	b.Balanced = true
	return b, nil
}

// emitter accumulates lines and remembers every key the mapping had no code
// for, so a refusal can say WHICH account is missing.
type emitter struct {
	accounts Accounts
	unmapped map[string]bool
	lines    []Line
}

// post appends one line. A key with no mapped code posts NOTHING and is
// remembered: the batch then fails to balance by exactly that amount, which
// is the honest outcome — the alternative is inventing an account code.
func (e *emitter) post(ev store.JournalEvent, key string, debit, credit store.Decimal, memo string) {
	if isZero(debit) && isZero(credit) {
		return
	}
	acct, ok := e.accounts[key]
	if !ok || acct.Code == "" {
		e.unmapped[key] = true
		return
	}
	kind, id := sourceOf(ev)
	l := Line{
		Date: ev.At.UTC().Format("2006-01-02"), EventKind: ev.Kind,
		AccountKey: key, AccountCode: acct.Code, AccountName: acct.Name,
		Debit: norm(debit), Credit: norm(credit), Currency: ev.Currency,
		CustomerID: ev.CustomerID, CustomerSlug: ev.CustomerSlug, CustomerName: ev.CustomerName,
		SourceKind: kind, SourceID: id, Reference: reference(ev), Memo: memo,
	}
	e.lines = append(e.lines, l)
}

// sourceOf names the object a line traces back to. Every event has one —
// that is the non-negotiable "any figure can be traced back".
func sourceOf(ev store.JournalEvent) (kind, id string) {
	switch {
	case ev.CreditNoteID != "":
		return SourceCreditNote, ev.CreditNoteID
	case ev.PaymentID != 0 && ev.Kind == store.EventGatewayFee:
		return SourcePayment, fmt.Sprint(ev.PaymentID)
	case ev.Kind == store.EventGatewayFee:
		return SourceReconciliation, ev.RunID
	case ev.PaymentID != 0:
		return SourcePayment, fmt.Sprint(ev.PaymentID)
	case ev.StatementID != "":
		return SourceStatement, ev.StatementID
	}
	return SourceReconciliation, ev.RunID
}

func reference(ev store.JournalEvent) string {
	switch {
	case ev.CreditNoteNo != "":
		return ev.CreditNoteNo
	case ev.InvoiceNumber != "":
		return ev.InvoiceNumber
	}
	return ev.Reference
}

func (e *emitter) event(ev store.JournalEvent) {
	switch ev.Kind {
	case store.EventInvoice:
		e.invoice(ev)
	case store.EventPayment:
		e.payment(ev)
	case store.EventTopUp:
		e.post(ev, e.accounts.cashKey(ev.Method, ev.Gateway), ev.Amount, "", "top-up received")
		e.post(ev, store.AccountCustomerAdvances, "", ev.Amount, "top-up held as account credit")
	case store.EventAdvanceApplied:
		e.post(ev, store.AccountCustomerAdvances, ev.Amount, "", ev.Memo)
		e.post(ev, store.AccountReceivable, "", ev.Amount, ev.Memo)
	case store.EventCreditNote:
		e.creditNote(ev, store.AccountCreditNotes)
	case store.EventWriteOff:
		e.creditNote(ev, store.AccountWriteOffs)
	case store.EventRefund:
		e.refund(ev)
	case store.EventGatewayFee:
		e.post(ev, store.AccountGatewayFees, ev.Amount, "", ev.Memo)
		e.post(ev, store.AccountGatewayClearing, "", ev.Amount, ev.Memo)
	}
}

// invoice: the receivable against the revenue it earned, the discount that
// reduced it and the tax it owes.
func (e *emitter) invoice(ev store.JournalEvent) {
	memo := "invoice " + reference(ev)
	e.post(ev, store.AccountReceivable, ev.Amount, "", memo)
	for _, r := range ev.Revenue {
		e.post(ev, e.accounts.revenueKey(r.Service, ev.StatementKind), "", r.Amount, revenueMemo(r.Service))
	}
	e.post(ev, store.AccountDiscounts, ev.DiscountTotal, "", "discounts on "+reference(ev))
	for _, t := range ev.TaxRules {
		e.post(ev, store.AccountTaxPayable, "", t.Tax, taxMemo(t))
	}
}

func revenueMemo(service string) string {
	if service == "" {
		return "revenue"
	}
	return "revenue, " + service
}

func taxMemo(t store.TaxRule) string {
	switch {
	case t.Name != "":
		return "tax, " + t.Name
	case t.Code != "":
		return "tax, " + t.Code
	case strings.TrimSpace(string(t.Rate)) != "":
		return "tax at rate " + string(t.Rate)
	}
	return "tax"
}

// payment: what it settled goes against the receivable, what it did not is
// account credit the customer still holds.
func (e *emitter) payment(ev store.JournalEvent) {
	allocated := rat(ev.Allocated)
	amount := rat(ev.Amount)
	if allocated.Cmp(amount) > 0 {
		allocated = amount
	}
	remainder := new(big.Rat).Sub(amount, allocated)
	cash := e.accounts.cashKey(ev.Method, ev.Gateway)
	memo := "payment received"
	if ev.InvoiceNumber != "" {
		memo += " against " + ev.InvoiceNumber
	}
	if allocated.Sign() > 0 {
		e.post(ev, cash, dec(allocated), "", memo)
		e.post(ev, store.AccountReceivable, "", dec(allocated), memo)
	}
	if remainder.Sign() > 0 {
		e.post(ev, cash, dec(remainder), "", "payment received, unallocated")
		e.post(ev, store.AccountCustomerAdvances, "", dec(remainder), "held as account credit")
	}
}

// creditNote: a reduction of revenue against whatever the note actually
// reached — the invoice while it still carried a balance, the account for
// the rest.
func (e *emitter) creditNote(ev store.JournalEvent, contra string) {
	memo := "credit note " + reference(ev)
	if contra == store.AccountWriteOffs {
		memo = "written off, " + reference(ev)
	}
	e.post(ev, contra, ev.Amount, "", memo)
	applied, total := rat(ev.Applied), rat(ev.Amount)
	if applied.Cmp(total) > 0 {
		applied = total
	}
	unapplied := new(big.Rat).Sub(total, applied)
	if applied.Sign() > 0 {
		e.post(ev, store.AccountReceivable, "", dec(applied), memo)
	}
	if unapplied.Sign() > 0 {
		e.post(ev, store.AccountCustomerAdvances, "", dec(unapplied), memo+", held as account credit")
	}
}

// refund: money going back out. It reverses whichever account the payment
// landed in — the receivable it had settled (the store re-opens the invoice
// at the same moment), or the account credit it was sitting as.
func (e *emitter) refund(ev store.JournalEvent) {
	target := store.AccountReceivable
	memo := "refund, invoice re-opened"
	if ev.RefundOf == store.EntryTopUp {
		target = store.AccountCustomerAdvances
		memo = "refund of account credit"
	}
	e.post(ev, target, ev.Amount, "", memo)
	e.post(ev, e.accounts.cashKey(ev.Method, ev.Gateway), "", ev.Amount, memo)
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

// CSVHeader is the column order of the journal export. It is FIXED: a
// finance system's import mapping is configured once against these names.
var CSVHeader = []string{
	"period", "seq", "date", "event", "account_key", "account_code", "account_name",
	"debit", "credit", "currency", "customer_slug", "customer_name",
	"source_kind", "source_id", "reference", "memo",
}

// CSV renders the batch. The same batch always renders the same bytes: the
// order is the emitter's, the money is the exact decimal string, and nothing
// is formatted for a human.
func (b Batch) CSV() ([]byte, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	rows := [][]string{CSVHeader}
	for _, l := range b.Lines {
		rows = append(rows, []string{
			b.Period, fmt.Sprint(l.Seq), l.Date, l.EventKind, l.AccountKey, l.AccountCode, l.AccountName,
			string(norm(l.Debit)), string(norm(l.Credit)), l.Currency, l.CustomerSlug, l.CustomerName,
			l.SourceKind, l.SourceID, l.Reference, l.Memo,
		})
	}
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// Stored renders the batch as the rows a close freezes.
func (b Batch) Stored() []store.StoredJournalLine {
	out := make([]store.StoredJournalLine, 0, len(b.Lines))
	for _, l := range b.Lines {
		out = append(out, store.StoredJournalLine{
			Seq: l.Seq, Date: l.Date, EventKind: l.EventKind, AccountKey: l.AccountKey, AccountCode: l.AccountCode, AccountName: l.AccountName,
			Debit: norm(l.Debit), Credit: norm(l.Credit), Currency: l.Currency,
			CustomerID: l.CustomerID, CustomerSlug: l.CustomerSlug, CustomerName: l.CustomerName,
			SourceKind: l.SourceKind, SourceID: l.SourceID, Reference: l.Reference, Memo: l.Memo,
		})
	}
	return out
}

// FromStored rebuilds a batch from the lines a close froze, so a closed
// period renders through exactly the same code as an open one.
func FromStored(period string, rows []store.StoredJournalLine) Batch {
	b := Batch{Period: period, Lines: make([]Line, 0, len(rows)), Balanced: true}
	debit, credit := new(big.Rat), new(big.Rat)
	perCurrency := map[string][2]*big.Rat{}
	for _, r := range rows {
		b.Lines = append(b.Lines, Line{
			Seq: r.Seq, Date: r.Date, EventKind: r.EventKind, AccountKey: r.AccountKey, AccountCode: r.AccountCode, AccountName: r.AccountName,
			Debit: r.Debit, Credit: r.Credit, Currency: r.Currency,
			CustomerID: r.CustomerID, CustomerSlug: r.CustomerSlug, CustomerName: r.CustomerName,
			SourceKind: r.SourceKind, SourceID: r.SourceID, Reference: r.Reference, Memo: r.Memo,
		})
		d, c := rat(r.Debit), rat(r.Credit)
		debit.Add(debit, d)
		credit.Add(credit, c)
		t, ok := perCurrency[r.Currency]
		if !ok {
			t = [2]*big.Rat{new(big.Rat), new(big.Rat)}
			perCurrency[r.Currency] = t
		}
		t[0].Add(t[0], d)
		t[1].Add(t[1], c)
	}
	b.TotalDebit, b.TotalCredit = dec(debit), dec(credit)
	currencies := make([]string, 0, len(perCurrency))
	for cur := range perCurrency {
		currencies = append(currencies, cur)
	}
	sort.Strings(currencies)
	for _, cur := range currencies {
		t := perCurrency[cur]
		b.ByCurrency = append(b.ByCurrency, CurrencyTotal{Currency: cur, Debit: dec(t[0]), Credit: dec(t[1])})
	}
	b.Balanced = debit.Cmp(credit) == 0
	return b
}

// ---------------------------------------------------------------------------
// exact arithmetic
// ---------------------------------------------------------------------------

func rat(d store.Decimal) *big.Rat {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return new(big.Rat)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

// dec renders at the six decimals every money column of this schema carries.
func dec(r *big.Rat) store.Decimal { return store.Decimal(r.FloatString(6)) }

// norm renders a value at the same six decimals, so "0" and "0.000000" are
// one string in the export and two exports of one batch are byte-identical.
func norm(d store.Decimal) store.Decimal { return dec(rat(d)) }

func isZero(d store.Decimal) bool { return rat(d).Sign() == 0 }
