package rating

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// WHAT A CONTRACT DID (DESIGN.md §15.10).
//
// A statement records the contract it was rated under, its rated lines and
// the discounts that applied; the per-SKU breakdown the run reported
// (Breakdown) is not frozen with it. So the contract page derives each
// period's reading from what IS frozen — the contract's lines against the
// statement's lines — rather than from a second table that would have to be
// kept in step with the first. The figures are the contract's own share: the
// included quantity is the contract line's, the committed head the contract
// line's, and the true-up the named line on the statement. The plan's own
// allowance, where the price book carries one, sits in front of the
// contract's and is the plan's, not the agreement's.

// ContractPeriod is what ONE statement rated under a contract did with it.
type ContractPeriod struct {
	// Period is YYYY-MM; PeriodStart / PeriodEnd the statement's own dates.
	Period      string `json:"period"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	StatementID string `json:"statement_id"`
	// InvoiceNumber is set once the statement was issued.
	InvoiceNumber string `json:"invoice_number,omitempty"`
	// Status is the statement's effective status (overdue included).
	Status   string        `json:"status"`
	Currency string        `json:"currency"`
	Subtotal store.Decimal `json:"subtotal"`
	Discount store.Decimal `json:"discount_total"`
	Total    store.Decimal `json:"total"`
	// Net is the subtotal less the true-up: what the usage rated to after
	// the discounts, which is the figure the floor was compared against.
	Net store.Decimal `json:"net"`
	// TrueUp is the shortfall line the period carried; "0" when it met the
	// floor or the contract has none.
	TrueUp store.Decimal `json:"true_up"`
	// Floor is the monthly floor in force: the figure the period was trued
	// up to when it carried a true-up (the subtotal is that floor exactly,
	// by construction), else the contract's floor as it stands. nil when
	// the contract carries neither a minimum nor a spend commitment.
	Floor       *store.Decimal  `json:"floor,omitempty"`
	Allowances  []AllowanceUse  `json:"allowances"`
	Commitments []CommitmentUse `json:"commitments"`
	Discounts   []DiscountUse   `json:"discounts"`
}

// AllowanceUse is one contract allowance line against the period's metered
// quantity of its SKU.
type AllowanceUse struct {
	SKU  string `json:"sku"`
	Unit string `json:"unit,omitempty"`
	// Quantity is the period's metered total of the SKU; Included the
	// contract line's quantity; Used how much of it the usage consumed; Excess
	// the metered quantity above the included one — what the plan's bands or
	// unit price rated, after whatever allowance the plan itself carries.
	Quantity store.Decimal `json:"quantity"`
	Included store.Decimal `json:"included"`
	Used     store.Decimal `json:"used"`
	Excess   store.Decimal `json:"excess"`
}

// CommitmentUse is one committed-use line against the period's metered
// quantity of its SKU.
type CommitmentUse struct {
	SKU  string `json:"sku"`
	Unit string `json:"unit,omitempty"`
	// Quantity is the period's metered total of the SKU; Committed the
	// contract line's quantity; Delivered how much of the committed head the
	// usage filled (after the contract's own allowance on the same SKU, which
	// comes off first); Shortfall the committed quantity the usage did not
	// reach — it is not invoiced by itself, the floor is the instrument for
	// that; Excess the quantity above the commitment, rated at list.
	Quantity  store.Decimal `json:"quantity"`
	Committed store.Decimal `json:"committed"`
	Delivered store.Decimal `json:"delivered"`
	Shortfall store.Decimal `json:"shortfall"`
	Excess    store.Decimal `json:"excess"`
	// CommittedPrice / DiscountPct echo the line's rate, so the row can say
	// what the head rated at; Amount is what the SKU rated to in the period.
	CommittedPrice *store.Decimal `json:"committed_price,omitempty"`
	DiscountPct    *store.Decimal `json:"discount_pct,omitempty"`
	Amount         store.Decimal  `json:"amount"`
}

// DiscountUse is one discount that applied to the period, from the
// statement's frozen breakdown. FromContract marks the contract's own spend
// commitment percentage (DESIGN.md §15.3a).
type DiscountUse struct {
	DiscountID   string        `json:"discount_id,omitempty"`
	Name         string        `json:"name"`
	Kind         string        `json:"kind,omitempty"`
	Value        store.Decimal `json:"value,omitempty"`
	Amount       store.Decimal `json:"amount"`
	FromContract bool          `json:"from_contract,omitempty"`
}

// ContractPeriods derives one row per statement, in the order given (the
// store hands them newest period first).
func ContractPeriods(c store.Contract, statements []store.Statement) ([]ContractPeriod, error) {
	out := make([]ContractPeriod, 0, len(statements))
	for _, st := range statements {
		row, err := ContractPeriodOf(c, st)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// ContractPeriodOf derives what the contract did in one statement.
func ContractPeriodOf(c store.Contract, st store.Statement) (ContractPeriod, error) {
	row := ContractPeriod{
		Period: periodOf(st.PeriodStart), PeriodStart: st.PeriodStart, PeriodEnd: st.PeriodEnd,
		StatementID: st.ID, InvoiceNumber: st.InvoiceNumber, Status: st.Status, Currency: st.Currency,
		Subtotal: st.Subtotal, Discount: st.DiscountTotal, Total: st.Total,
		Allowances: []AllowanceUse{}, Commitments: []CommitmentUse{}, Discounts: []DiscountUse{},
	}
	if st.EffectiveStatus != "" {
		row.Status = st.EffectiveStatus
	}
	if strings.TrimSpace(string(row.Discount)) == "" {
		row.Discount = "0"
	}
	// The metered quantity and the rated amount of every SKU in the period,
	// summed across sources: the shapes are per SKU per period (§15.7).
	quantity, amount := map[string]*big.Rat{}, map[string]*big.Rat{}
	trueUp := new(big.Rat)
	for _, l := range st.Lines {
		q, err := parseRat(string(l.Quantity))
		if err != nil {
			return row, fmt.Errorf("statement %s line %s quantity: %w", st.ID, l.SKU, err)
		}
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return row, fmt.Errorf("statement %s line %s amount: %w", st.ID, l.SKU, err)
		}
		if l.SKU == TrueUpSKU {
			trueUp.Add(trueUp, a)
			continue
		}
		if quantity[l.SKU] == nil {
			quantity[l.SKU], amount[l.SKU] = new(big.Rat), new(big.Rat)
		}
		quantity[l.SKU].Add(quantity[l.SKU], q)
		amount[l.SKU].Add(amount[l.SKU], a)
	}
	subtotal, err := parseRat(string(st.Subtotal))
	if err != nil {
		return row, fmt.Errorf("statement %s subtotal: %w", st.ID, err)
	}
	row.TrueUp = store.Decimal(roundRat(trueUp, 6))
	row.Net = store.Decimal(roundRat(new(big.Rat).Sub(subtotal, trueUp), 6))
	switch {
	case trueUp.Sign() > 0:
		// The run brought the net to the floor exactly (TrueUp), so the
		// subtotal IS the floor that was in force when the period was rated.
		f := row.Subtotal
		row.Floor = &f
	default:
		row.Floor = c.MonthlyFloor()
	}
	// The contract's allowance per SKU comes off the top before a commitment
	// on the same SKU covers the head of what remains (§15.7 steps 1 and 3).
	allowanceOf := map[string]*big.Rat{}
	for _, it := range c.Items {
		if it.Kind != store.ContractItemAllowance {
			continue
		}
		q, err := parseRat(string(it.Quantity))
		if err != nil {
			return row, fmt.Errorf("contract line %s quantity: %w", it.SKU, err)
		}
		if allowanceOf[it.SKU] == nil {
			allowanceOf[it.SKU] = new(big.Rat)
		}
		allowanceOf[it.SKU].Add(allowanceOf[it.SKU], q)
	}
	zero := new(big.Rat)
	metered := func(sku string) *big.Rat {
		if q := quantity[sku]; q != nil {
			return q
		}
		return zero
	}
	for _, it := range c.Items {
		switch it.Kind {
		case store.ContractItemAllowance:
			included, err := parseRat(string(it.Quantity))
			if err != nil {
				return row, fmt.Errorf("contract line %s quantity: %w", it.SKU, err)
			}
			q := metered(it.SKU)
			used := minRat(included, q)
			row.Allowances = append(row.Allowances, AllowanceUse{
				SKU: it.SKU, Unit: it.Unit,
				Quantity: store.Decimal(roundRat(q, 6)),
				Included: store.Decimal(roundRat(included, 6)),
				Used:     store.Decimal(roundRat(used, 6)),
				Excess:   store.Decimal(roundRat(maxRat(new(big.Rat).Sub(q, included), zero), 6)),
			})
		case store.ContractItemCommitment:
			committed, err := parseRat(string(it.Quantity))
			if err != nil {
				return row, fmt.Errorf("contract line %s quantity: %w", it.SKU, err)
			}
			q := metered(it.SKU)
			billable := new(big.Rat).Set(q)
			if a := allowanceOf[it.SKU]; a != nil {
				billable = maxRat(new(big.Rat).Sub(q, a), zero)
			}
			delivered := minRat(committed, billable)
			use := CommitmentUse{
				SKU: it.SKU, Unit: it.Unit,
				Quantity:       store.Decimal(roundRat(q, 6)),
				Committed:      store.Decimal(roundRat(committed, 6)),
				Delivered:      store.Decimal(roundRat(delivered, 6)),
				Shortfall:      store.Decimal(roundRat(new(big.Rat).Sub(committed, delivered), 6)),
				Excess:         store.Decimal(roundRat(new(big.Rat).Sub(billable, delivered), 6)),
				CommittedPrice: it.CommittedPrice, DiscountPct: it.DiscountPct,
				Amount: "0.000000",
			}
			if a := amount[it.SKU]; a != nil {
				use.Amount = store.Decimal(roundRat(a, 6))
			}
			row.Commitments = append(row.Commitments, use)
		}
	}
	// The discounts that applied, from the breakdown the run froze; the
	// contract's spend commitment is marked as its own.
	spendID := ""
	if sp, ok := c.SpendCommitment(); ok {
		spendID = sp.ID
	}
	if len(st.DiscountDetail) > 0 && string(st.DiscountDetail) != "null" {
		var applied []AppliedDiscount
		if err := json.Unmarshal(st.DiscountDetail, &applied); err != nil {
			return row, fmt.Errorf("statement %s discount detail: %w", st.ID, err)
		}
		for _, d := range applied {
			a, err := parseRat(string(d.Amount))
			if err != nil || a.Sign() <= 0 {
				// A superseded discount (amount 0) did nothing to the period.
				continue
			}
			row.Discounts = append(row.Discounts, DiscountUse{
				DiscountID: d.DiscountID, Name: d.Name, Kind: d.Kind, Value: d.Value, Amount: d.Amount,
				FromContract: spendID != "" && d.DiscountID == spendID,
			})
		}
	}
	return row, nil
}

// periodOf is the YYYY-MM of a statement's period start.
func periodOf(periodStart string) string {
	if len(periodStart) >= 7 {
		return periodStart[:7]
	}
	return periodStart
}

func minRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) <= 0 {
		return new(big.Rat).Set(a)
	}
	return new(big.Rat).Set(b)
}

func maxRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) >= 0 {
		return new(big.Rat).Set(a)
	}
	return new(big.Rat).Set(b)
}
