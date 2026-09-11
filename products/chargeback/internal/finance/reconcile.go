package finance

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// GATEWAY SETTLEMENT RECONCILIATION (DESIGN.md §18.3).
//
// A gateway takes money and, days later, pays a batch into the bank with its
// own list of what is in it. Finance will not sign off a cash figure until
// that list and the ledger agree line for line. This is that comparison, and
// it is a COMPARISON: nothing here creates, amends or reallocates a payment.
// It reports four buckets and a human decides.
//
//	matched                the reference is on both sides for the same amount
//	amount-mismatch        the reference is on both sides for different amounts
//	                       — both figures are reported, neither is believed
//	missing-in-ledger      the gateway settled something we never recorded
//	missing-in-settlement  we recorded a payment the gateway has not settled
//
// A fifth, `duplicate`, catches a settlement file that names one gateway
// reference twice: the second occurrence is REPORTED rather than matched
// against the same payment a second time, which is how a reconciliation
// quietly doubles a cash figure.

// Settlement is one line of a gateway's settlement: what it settled, for how
// much, on what day, and the fee it kept.
type Settlement struct {
	// Reference is the gateway's own id for the collection — the same value
	// a payment carries in `reference`, which is what the two sides join on.
	Reference string        `json:"reference"`
	Amount    store.Decimal `json:"amount"`
	Currency  string        `json:"currency"`
	// SettledAt is the day the money reached the bank.
	SettledAt time.Time `json:"settled_at"`
	// Fee is what the gateway kept out of the amount. Zero when the gateway
	// bills its fees separately.
	Fee store.Decimal `json:"fee"`
	// Line is the file line the row came from, for an error that names it.
	Line int `json:"line,omitempty"`
}

// settlementColumns are the names the parser accepts, per field. The first
// is canonical; the rest are what real gateway exports call them.
var settlementColumns = map[string][]string{
	"reference": {"gateway_reference", "reference", "gateway_ref", "transaction_id", "transaction_reference"},
	"amount":    {"amount", "settled_amount", "gross_amount", "value"},
	"currency":  {"currency", "currency_code"},
	"settled":   {"settled_date", "settled_at", "settlement_date", "date", "value_date"},
	"fee":       {"fee", "fees", "commission", "charge"},
}

// ParseSettlementCSV reads a settlement file: a header row in any column
// order, then one row per settled line. A row that cannot be read is an
// error naming the file line — a settlement file is a cash document, and
// half of one reconciled silently is worse than none.
func ParseSettlementCSV(r io.Reader) ([]Settlement, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: the settlement file has no header row", store.ErrInvalid)
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))] = i
	}
	col := map[string]int{}
	for field, names := range settlementColumns {
		col[field] = -1
		for _, n := range names {
			if i, ok := idx[n]; ok {
				col[field] = i
				break
			}
		}
	}
	for _, need := range []string{"reference", "amount"} {
		if col[need] < 0 {
			return nil, fmt.Errorf("%w: the settlement file has no %s column (accepted: %s)", store.ErrInvalid, need, strings.Join(settlementColumns[need], ", "))
		}
	}
	get := func(rec []string, field string) string {
		i := col[field]
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	out := []Settlement{}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("%w: settlement file line %d: %s", store.ErrInvalid, line, err)
		}
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		s := Settlement{Reference: get(rec, "reference"), Currency: strings.ToUpper(get(rec, "currency")), Line: line}
		if s.Reference == "" {
			return nil, fmt.Errorf("%w: settlement file line %d has no gateway reference", store.ErrInvalid, line)
		}
		amount, err := decimalOf(get(rec, "amount"))
		if err != nil {
			return nil, fmt.Errorf("%w: settlement file line %d: amount %q is not a number", store.ErrInvalid, line, get(rec, "amount"))
		}
		s.Amount = amount
		if raw := get(rec, "fee"); raw != "" {
			fee, err := decimalOf(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: settlement file line %d: fee %q is not a number", store.ErrInvalid, line, raw)
			}
			s.Fee = fee
		} else {
			s.Fee = "0"
		}
		if raw := get(rec, "settled"); raw != "" {
			at, err := parseDay(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: settlement file line %d: settled date %q is not a date (YYYY-MM-DD or RFC3339)", store.ErrInvalid, line, raw)
			}
			s.SettledAt = at
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the settlement file has a header and no lines", store.ErrInvalid)
	}
	return out, nil
}

func decimalOf(s string) (store.Decimal, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return "0", nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", fmt.Errorf("not a number")
	}
	return dec(r), nil
}

func parseDay(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "02/01/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("not a date")
}

// Reconcile compares a gateway's settlement lines with the payments recorded
// in the window and returns the run, unsaved. It changes nothing.
func Reconcile(gateway, source, fileName, from, to string, settlements []Settlement, payments []store.SettledPayment, actor string) store.ReconciliationRun {
	run := store.ReconciliationRun{
		Gateway: strings.ToLower(strings.TrimSpace(gateway)), Source: source, FileName: fileName,
		From: from, To: to, RanBy: actor, Lines: []store.ReconciliationLine{},
	}
	byRef := map[string]store.SettledPayment{}
	for _, p := range payments {
		key := refKey(p.Reference)
		if key == "" {
			continue
		}
		// A reference recorded on two payments is itself a defect; the first
		// wins the match and the second is reported as unsettled rather than
		// matched to the same settlement line.
		if _, seen := byRef[key]; !seen {
			byRef[key] = p
		}
	}
	seenSettlement := map[string]bool{}
	matchedPayments := map[int64]bool{}
	settled, ledger, fees := new(big.Rat), new(big.Rat), new(big.Rat)

	sorted := append([]Settlement{}, settlements...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].SettledAt.Equal(sorted[j].SettledAt) {
			return sorted[i].SettledAt.Before(sorted[j].SettledAt)
		}
		return sorted[i].Line < sorted[j].Line
	})

	for _, s := range sorted {
		key := refKey(s.Reference)
		day := ""
		if !s.SettledAt.IsZero() {
			day = s.SettledAt.UTC().Format("2006-01-02")
		}
		amount := norm(s.Amount)
		line := store.ReconciliationLine{
			GatewayReference: s.Reference, SettledAmount: &amount, Fee: norm(s.Fee),
			Currency: s.Currency, SettledDate: day,
		}
		if seenSettlement[key] {
			line.Bucket = store.BucketDuplicate
			line.Fee = "0.000000"
			line.Detail = "the settlement file names " + s.Reference + " more than once; the later line was not matched again"
			run.Duplicates++
			run.Lines = append(run.Lines, line)
			continue
		}
		seenSettlement[key] = true
		settled.Add(settled, rat(s.Amount))
		p, ok := byRef[key]
		if !ok {
			line.Bucket = store.BucketMissingInLedger
			line.Fee = "0.000000"
			line.Detail = "the gateway settled " + s.Reference + " and no payment carries that reference"
			run.MissingInLedger++
			run.Lines = append(run.Lines, line)
			continue
		}
		matchedPayments[p.PaymentID] = true
		ledgerAmount := norm(p.Amount)
		line.LedgerAmount = &ledgerAmount
		line.PaymentID, line.CustomerID, line.CustomerName = p.PaymentID, p.CustomerID, p.CustomerName
		if line.Currency == "" {
			line.Currency = p.Currency
		}
		ledger.Add(ledger, rat(p.Amount))
		fees.Add(fees, rat(s.Fee))
		if diff := new(big.Rat).Sub(rat(s.Amount), rat(p.Amount)); diff.Sign() != 0 {
			d := dec(diff)
			line.Difference = &d
			line.Bucket = store.BucketAmountMismatch
			line.Detail = fmt.Sprintf("the gateway settled %s and payment %d records %s, a difference of %s", amount, p.PaymentID, ledgerAmount, d)
			run.Mismatched++
			run.Lines = append(run.Lines, line)
			continue
		}
		zero := store.Decimal("0.000000")
		line.Difference = &zero
		line.Bucket = store.BucketMatched
		run.Matched++
		run.Lines = append(run.Lines, line)
	}

	for _, p := range payments {
		if matchedPayments[p.PaymentID] {
			continue
		}
		amount := norm(p.Amount)
		detail := "payment " + fmt.Sprint(p.PaymentID) + " is recorded and the settlement file does not name " + p.Reference
		if refKey(p.Reference) == "" {
			detail = "payment " + fmt.Sprint(p.PaymentID) + " carries no gateway reference, so nothing can be matched to it"
		}
		run.Lines = append(run.Lines, store.ReconciliationLine{
			Bucket: store.BucketMissingInSettlement, GatewayReference: p.Reference, LedgerAmount: &amount,
			Currency: p.Currency, SettledDate: p.PaidAt.UTC().Format("2006-01-02"),
			PaymentID: p.PaymentID, CustomerID: p.CustomerID, CustomerName: p.CustomerName, Detail: detail,
		})
		run.MissingInSettlement++
		ledger.Add(ledger, rat(p.Amount))
	}

	run.SettledTotal, run.LedgerTotal, run.FeeTotal = dec(settled), dec(ledger), dec(fees)
	if run.Currency == "" {
		run.Currency = dominantCurrency(run.Lines)
	}
	return run
}

// refKey normalises a gateway reference for matching: trimmed, case-folded.
// Nothing else — a gateway's reference is an opaque identifier and inventing
// a cleverer comparison is how a false match happens.
func refKey(ref string) string { return strings.ToLower(strings.TrimSpace(ref)) }

func dominantCurrency(lines []store.ReconciliationLine) string {
	counts := map[string]int{}
	for _, l := range lines {
		if l.Currency != "" {
			counts[l.Currency]++
		}
	}
	best, bestN := "", 0
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best
}
