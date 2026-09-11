// Package collections is what happens after an invoice falls due
// (DESIGN.md §9.6–§9.7): the reminder schedule, the escalation, the aging
// report, and the platform suspension hook with its resumption — all of it
// owned by this product ONLY when it is the system of record. With an
// external billing system, collections are theirs; we run nothing here and
// suspend or resume only on an explicit imported command.
package collections

import (
	"math/big"
	"sort"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The aging buckets, by days past due. `current` is not yet due.
const (
	BucketCurrent = "current"
	Bucket1to30   = "1-30"
	Bucket31to60  = "31-60"
	Bucket61to90  = "61-90"
	BucketOver90  = "over-90"
)

// Buckets lists the aging buckets in order.
var Buckets = []string{BucketCurrent, Bucket1to30, Bucket31to60, Bucket61to90, BucketOver90}

// DaysPastDue is how many whole days `now` is past `due` (negative when the
// due date is still ahead). Both are read as UTC calendar days, so an
// invoice due today is 0 days past due for the whole of today.
func DaysPastDue(due, now time.Time) int {
	d := dateOnly(due)
	n := dateOnly(now)
	return int(n.Sub(d).Hours() / 24)
}

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Bucket names the aging bucket for a number of days past due. The
// boundaries are exact: day 0 is current, day 1 opens 1–30, day 31 opens
// 31–60, day 61 opens 61–90, day 91 is over 90.
func Bucket(daysPastDue int) string {
	switch {
	case daysPastDue <= 0:
		return BucketCurrent
	case daysPastDue <= 30:
		return Bucket1to30
	case daysPastDue <= 60:
		return Bucket31to60
	case daysPastDue <= 90:
		return Bucket61to90
	}
	return BucketOver90
}

// AgingRow is one customer's outstanding by bucket.
type AgingRow struct {
	CustomerID   string                   `json:"customer_id"`
	CustomerName string                   `json:"customer_name"`
	CustomerSlug string                   `json:"customer_slug"`
	Currency     string                   `json:"currency"`
	Buckets      map[string]store.Decimal `json:"buckets"`
	Total        store.Decimal            `json:"total"`
	// Overdue is everything past due (the four late buckets) that is being
	// CHASED: a disputed invoice is excluded from it, and from OldestDays,
	// because it is not pursued while the dispute is open (DESIGN.md §16).
	Overdue store.Decimal `json:"overdue"`
	// Disputed is how much of Total the customer is disputing. It is still
	// owed and still in Buckets — a dispute is a pause on chasing, not a
	// credit — and it is named separately so the operator can see why the
	// overdue figure is lower than the bucket totals.
	Disputed store.Decimal `json:"disputed"`
	// OldestDays is how far past due the oldest CHASED open invoice is.
	OldestDays int `json:"oldest_days"`
	Invoices   int `json:"invoices"`
	// DisputedInvoices counts the invoices of this customer under dispute.
	DisputedInvoices int `json:"disputed_invoices"`
	// Suspended reports the platform suspension this product holds.
	Suspended        bool       `json:"suspended"`
	SuspendedAt      *time.Time `json:"suspended_at,omitempty"`
	SuspensionSource string     `json:"suspension_source,omitempty"`
	SuspensionReason string     `json:"suspension_reason,omitempty"`
	// AvailableCredit is what the customer could still apply.
	AvailableCredit store.Decimal `json:"available_credit"`
}

// AgingInvoice is one open invoice as the report lists it.
type AgingInvoice struct {
	StatementID   string        `json:"statement_id"`
	InvoiceNumber string        `json:"invoice_number,omitempty"`
	CustomerID    string        `json:"customer_id"`
	CustomerName  string        `json:"customer_name"`
	Currency      string        `json:"currency"`
	DueAt         time.Time     `json:"due_at"`
	DaysPastDue   int           `json:"days_past_due"`
	Bucket        string        `json:"bucket"`
	Outstanding   store.Decimal `json:"outstanding"`
	Status        string        `json:"status"`
	// DESIGN.md §16 — the customer disputes this invoice, and why. It is
	// listed (the operator must see it) and not chased: no reminder, no
	// escalation, and its amount is out of the overdue figure until the
	// dispute is resolved.
	Disputed      bool   `json:"disputed,omitempty"`
	DisputeReason string `json:"dispute_reason,omitempty"`
}

// AgingReport is GET /collections/aging.
type AgingReport struct {
	AsOf    time.Time                `json:"as_of"`
	Buckets []string                 `json:"buckets"`
	Rows    []AgingRow               `json:"rows"`
	Totals  map[string]store.Decimal `json:"totals"`
	Total   store.Decimal            `json:"total"`
	Overdue store.Decimal            `json:"overdue"`
	// Disputed is the part of Total under dispute and so not chased
	// (DESIGN.md §16), with the count of invoices it came from.
	Disputed         store.Decimal  `json:"disputed"`
	DisputedInvoices int            `json:"disputed_invoices"`
	Invoices         []AgingInvoice `json:"invoices"`
	// Owned says who runs collections on this Sovereign: internal, or the
	// external billing system (in which case the report is informational).
	Owner string `json:"collections_owner"`
}

// customerFacts is what the report needs of a customer beyond its invoices.
type customerFacts struct {
	suspendedAt *time.Time
	source      string
	reason      string
	credit      store.Decimal
}

// BuildAging folds open invoices into the report at `now`. Pure, so the
// bucket boundaries are unit-tested exactly.
func BuildAging(open []store.OpenInvoice, now time.Time, facts map[string]customerFacts, owner string) AgingReport {
	rep := AgingReport{AsOf: now.UTC(), Buckets: Buckets, Totals: map[string]store.Decimal{}, Rows: []AgingRow{}, Invoices: []AgingInvoice{}, Owner: owner}
	rows := map[string]*AgingRow{}
	totals := map[string]*big.Rat{}
	for _, b := range Buckets {
		totals[b] = new(big.Rat)
	}
	grand, overdue, disputed := new(big.Rat), new(big.Rat), new(big.Rat)
	rowSums := map[string]map[string]*big.Rat{}
	rowDisputed := map[string]*big.Rat{}
	for _, inv := range open {
		days := DaysPastDue(inv.DueAt, now)
		bucket := Bucket(days)
		amt := ratOf(inv.Outstanding)
		if amt.Sign() <= 0 {
			continue
		}
		// DESIGN.md §16 — a disputed invoice is REPORTED and not CHASED.
		// It keeps its bucket and its place in the totals, because the
		// money is still owed; what it leaves is the overdue figure, the
		// oldest-days age and the evaluator's reminder schedule.
		inDispute := inv.DisputedAt != nil
		rep.Invoices = append(rep.Invoices, AgingInvoice{StatementID: inv.StatementID, InvoiceNumber: inv.InvoiceNumber, CustomerID: inv.CustomerID, CustomerName: inv.CustomerName,
			Currency: inv.Currency, DueAt: inv.DueAt, DaysPastDue: days, Bucket: bucket, Outstanding: inv.Outstanding, Status: inv.Status,
			Disputed: inDispute, DisputeReason: inv.DisputeReason})
		row, ok := rows[inv.CustomerID]
		if !ok {
			row = &AgingRow{CustomerID: inv.CustomerID, CustomerName: inv.CustomerName, CustomerSlug: inv.CustomerSlug, Currency: inv.Currency, Buckets: map[string]store.Decimal{}}
			if f, ok := facts[inv.CustomerID]; ok {
				row.Suspended = f.suspendedAt != nil
				row.SuspendedAt, row.SuspensionSource, row.SuspensionReason, row.AvailableCredit = f.suspendedAt, f.source, f.reason, f.credit
			}
			if row.AvailableCredit == "" {
				row.AvailableCredit = "0"
			}
			rows[inv.CustomerID] = row
			rowSums[inv.CustomerID] = map[string]*big.Rat{}
			rowDisputed[inv.CustomerID] = new(big.Rat)
			for _, b := range Buckets {
				rowSums[inv.CustomerID][b] = new(big.Rat)
			}
		}
		rowSums[inv.CustomerID][bucket].Add(rowSums[inv.CustomerID][bucket], amt)
		totals[bucket].Add(totals[bucket], amt)
		grand.Add(grand, amt)
		row.Invoices++
		if inDispute {
			rowDisputed[inv.CustomerID].Add(rowDisputed[inv.CustomerID], amt)
			disputed.Add(disputed, amt)
			row.DisputedInvoices++
			rep.DisputedInvoices++
			continue
		}
		if days > 0 {
			overdue.Add(overdue, amt)
		}
		if days > row.OldestDays {
			row.OldestDays = days
		}
	}
	for id, row := range rows {
		sum, late := new(big.Rat), new(big.Rat)
		for _, b := range Buckets {
			v := rowSums[id][b]
			row.Buckets[b] = decOf(v)
			sum.Add(sum, v)
			if b != BucketCurrent {
				late.Add(late, v)
			}
		}
		// What is chased is the late buckets LESS what is under dispute:
		// the money is in Total and in the buckets, and out of Overdue.
		late.Sub(late, rowDisputed[id])
		if late.Sign() < 0 {
			late = new(big.Rat)
		}
		row.Total, row.Overdue, row.Disputed = decOf(sum), decOf(late), decOf(rowDisputed[id])
		rep.Rows = append(rep.Rows, *row)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		c := ratOf(rep.Rows[i].Overdue).Cmp(ratOf(rep.Rows[j].Overdue))
		if c != 0 {
			return c > 0
		}
		return rep.Rows[i].CustomerName < rep.Rows[j].CustomerName
	})
	for _, b := range Buckets {
		rep.Totals[b] = decOf(totals[b])
	}
	rep.Total, rep.Overdue, rep.Disputed = decOf(grand), decOf(overdue), decOf(disputed)
	return rep
}

func ratOf(d store.Decimal) *big.Rat {
	r, ok := new(big.Rat).SetString(string(d))
	if !ok {
		return new(big.Rat)
	}
	return r
}

func decOf(r *big.Rat) store.Decimal { return store.Decimal(r.FloatString(6)) }
