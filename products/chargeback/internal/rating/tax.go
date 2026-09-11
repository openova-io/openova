package rating

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The LAST step of the waterfall, now per RULE rather than per rate
// (DESIGN.md §17).
//
// Nothing about the order changes: list → commercial terms → discounts →
// true-up → TAX. What changes is that "tax" is no longer one multiplication.
// Each rated line is placed in a tax CATEGORY, each category resolves to a
// RULE for this buyer at this date, and each rule taxes its OWN base.
//
// The discount is a single figure against the whole statement, and tax must
// be charged on what the customer actually pays (#6862). So the discount is
// APPORTIONED across the rules pro rata by their gross amounts, by the
// largest-remainder method, which makes the parts sum to the discount
// EXACTLY — never "about" — and therefore makes
//
//	sum(base) == gross − discount        and        sum(tax) == tax
//
// hold as identities rather than as a rounding coincidence. A statement with
// ONE rule reduces to exactly the arithmetic TotalsWithDiscount has always
// done, which is what keeps every customer with no tax profile unchanged.

// TaxOutcome is what the tax step produced.
type TaxOutcome struct {
	// Lines is the per-rule summary, in a stable order (highest rate first,
	// then rule name). Empty when a single rule covered the statement: a
	// one-rate invoice has always shown its one rate and gains nothing from
	// a summary block of one row.
	Lines []store.TaxLine
	// Subtotal is the NET (gross − discount), Tax the sum of the per-rule
	// amounts, Total their sum.
	Subtotal store.Decimal
	Tax      store.Decimal
	Total    store.Decimal
	// Rate is what the statement's single tax_rate column carries. With one
	// rule it is that rule's rate. With SEVERAL it is the EFFECTIVE rate
	// (tax ÷ net subtotal) — the legacy column cannot hold two rates, and
	// leaving it at one of them, or at zero, would be a lie. Every surface
	// that can show the summary shows the summary; the column is for the
	// readers written before §17.
	Rate store.Decimal
	// Audit is every determination that was not the plain reading of the
	// rule table, de-duplicated across categories.
	Audit []string
	// Notes are the sentences the invoice must carry (the reverse-charge
	// wording, an exemption article), de-duplicated, in summary order.
	Notes []string
}

// taxGroup accumulates one rule's lines.
type taxGroup struct {
	line       store.TaxLine
	gross      *big.Rat
	categories map[string]bool
	order      int
}

// ApplyTax annotates each line with its tax category and rule, splits the
// discount across the rules, and returns the totals. engine may be nil, in
// which case fallbackRate is applied to everything — the pre-§17 behaviour.
func ApplyTax(lines []store.RatedLine, discount store.Decimal, engine *store.TaxEngine, party store.TaxParty, at time.Time, fallbackRate store.Decimal) ([]store.RatedLine, TaxOutcome, error) {
	if engine == nil {
		engine = &store.TaxEngine{DefaultRate: fallbackRate}
	}
	out := make([]store.RatedLine, len(lines))
	copy(out, lines)

	// Resolve ONCE per category: a statement has hundreds of lines and a
	// handful of categories, and the resolution must be identical for every
	// line of a category or the summary cannot be grouped.
	decisions := map[string]store.TaxDecision{}
	groups := map[string]*taxGroup{}
	var order []string
	seenAudit := map[string]bool{}
	var audit []string

	for i, l := range out {
		category := engine.CategoryFor(l.SKU)
		d, ok := decisions[category]
		if !ok {
			d = engine.Resolve(party, category, at)
			decisions[category] = d
			for _, a := range d.Audit {
				if !seenAudit[a] {
					seenAudit[a] = true
					audit = append(audit, a)
				}
			}
		}
		out[i].TaxCategory = category
		out[i].TaxRuleID = d.Rule.RuleID

		key := d.Rule.RuleID + "|" + string(d.Rule.Rate) + "|" + d.Rule.Kind
		g, ok := groups[key]
		if !ok {
			g = &taxGroup{
				line:       store.TaxLine{RuleID: d.Rule.RuleID, RuleName: d.Rule.RuleName, Kind: d.Rule.Kind, Rate: d.Rule.Rate, Note: d.Rule.Note},
				gross:      new(big.Rat),
				categories: map[string]bool{},
				order:      len(order),
			}
			groups[key] = g
			order = append(order, key)
		}
		amount, err := parseRat(string(l.Amount))
		if err != nil {
			return nil, TaxOutcome{}, fmt.Errorf("line %d amount: %w", i, err)
		}
		g.gross.Add(g.gross, amount)
		g.categories[category] = true
	}

	// No lines at all: a period with nothing to bill still has a rate, and
	// it is the rate a line WOULD have been charged at.
	if len(order) == 0 {
		d := engine.Resolve(party, "", at)
		return out, TaxOutcome{Subtotal: "0.000000", Tax: "0.000000", Total: "0.000000", Rate: d.Rule.Rate, Audit: audit}, nil
	}

	ordered := make([]*taxGroup, 0, len(order))
	for _, k := range order {
		ordered = append(ordered, groups[k])
	}

	// The discount, apportioned pro rata by gross, largest remainder last so
	// the parts sum to the discount exactly.
	shares, err := apportion(discount, ordered)
	if err != nil {
		return nil, TaxOutcome{}, err
	}

	subtotal, tax := new(big.Rat), new(big.Rat)
	for i, g := range ordered {
		base := new(big.Rat).Sub(g.gross, shares[i])
		if base.Sign() < 0 {
			base = new(big.Rat)
		}
		g.line.Base = store.Decimal(roundRat(base, 6))
		amount, err := Tax(g.line.Base, g.line.Rate)
		if err != nil {
			return nil, TaxOutcome{}, err
		}
		g.line.Tax = amount
		if len(g.categories) == 1 {
			for c := range g.categories {
				g.line.Category = c
			}
		}
		b, _ := parseRat(string(g.line.Base))
		t, _ := parseRat(string(g.line.Tax))
		subtotal.Add(subtotal, b)
		tax.Add(tax, t)
	}

	// Highest rate first, then rule name, then the order they were met, so
	// the summary block reads the same on every render.
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ratOfDec(ordered[i].line.Rate), ratOfDec(ordered[j].line.Rate)
		if c := b.Cmp(a); c != 0 {
			return c < 0
		}
		if ordered[i].line.RuleName != ordered[j].line.RuleName {
			return ordered[i].line.RuleName < ordered[j].line.RuleName
		}
		return ordered[i].order < ordered[j].order
	})

	res := TaxOutcome{
		Subtotal: store.Decimal(roundRat(subtotal, 6)),
		Tax:      store.Decimal(roundRat(tax, 6)),
		Total:    store.Decimal(roundRat(new(big.Rat).Add(subtotal, tax), 6)),
		Audit:    audit,
	}
	seenNote := map[string]bool{}
	for _, g := range ordered {
		if g.line.Note != "" && !seenNote[g.line.Note] {
			seenNote[g.line.Note] = true
			res.Notes = append(res.Notes, g.line.Note)
		}
	}
	switch len(ordered) {
	case 1:
		// One rule: the statement's rate IS that rule's rate and a summary
		// block of one row says nothing the waterfall does not.
		res.Rate = ordered[0].line.Rate
		if ordered[0].line.RuleID != "" || ordered[0].line.Kind != store.TaxKindStandard {
			// It DID come from a rule (or from a determination), so record
			// it: the rule id on the invoice is the answer to "under what".
			res.Lines = []store.TaxLine{ordered[0].line}
		}
	default:
		res.Lines = make([]store.TaxLine, 0, len(ordered))
		for _, g := range ordered {
			res.Lines = append(res.Lines, g.line)
		}
		res.Rate = effectiveRate(tax, subtotal)
	}
	return out, res, nil
}

// apportion splits total across the groups pro rata by gross. The parts are
// floored to the money scale and the remainder is handed out one unit at a
// time, largest fractional part first — so they sum to total EXACTLY, and a
// group with no gross receives nothing.
func apportion(total store.Decimal, groups []*taxGroup) ([]*big.Rat, error) {
	want, err := parseRat(string(total))
	if err != nil {
		return nil, fmt.Errorf("discount: %w", err)
	}
	shares := make([]*big.Rat, len(groups))
	for i := range shares {
		shares[i] = new(big.Rat)
	}
	if want.Sign() <= 0 {
		return shares, nil
	}
	gross := new(big.Rat)
	for _, g := range groups {
		gross.Add(gross, g.gross)
	}
	if gross.Sign() <= 0 {
		// Nothing to apportion against: the whole discount lands on the
		// first group, which is where a single-group statement puts it.
		shares[0] = want
		return shares, nil
	}
	// unit is the money scale (10^-6): every share is a whole number of
	// units, so the parts can be made to sum exactly.
	unit := big.NewRat(1, 1000000)
	inUnits := new(big.Rat).Quo(want, unit)
	totalUnits := new(big.Int).Quo(inUnits.Num(), inUnits.Denom())
	type rem struct {
		i    int
		frac *big.Rat
	}
	var rems []rem
	assigned := new(big.Int)
	for i, g := range groups {
		exact := new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).SetInt(totalUnits), g.gross), gross)
		floor := new(big.Int).Quo(exact.Num(), exact.Denom())
		shares[i] = new(big.Rat).Mul(new(big.Rat).SetInt(floor), unit)
		assigned.Add(assigned, floor)
		rems = append(rems, rem{i: i, frac: new(big.Rat).Sub(exact, new(big.Rat).SetInt(floor))})
	}
	left := new(big.Int).Sub(totalUnits, assigned)
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac.Cmp(rems[b].frac) > 0 })
	for k := 0; left.Sign() > 0 && k < len(rems); k++ {
		shares[rems[k].i].Add(shares[rems[k].i], unit)
		left.Sub(left, big.NewInt(1))
	}
	// A discount that is not a whole number of units (it always is — every
	// money column is NUMERIC(20,6)) would leave a sliver; it goes to the
	// largest group so the identity still holds.
	if slack := new(big.Rat).Sub(want, sumRats(shares)); slack.Sign() != 0 {
		biggest := 0
		for i := range groups {
			if groups[i].gross.Cmp(groups[biggest].gross) > 0 {
				biggest = i
			}
		}
		shares[biggest].Add(shares[biggest], slack)
	}
	return shares, nil
}

func sumRats(in []*big.Rat) *big.Rat {
	out := new(big.Rat)
	for _, r := range in {
		out.Add(out, r)
	}
	return out
}

func ratOfDec(d store.Decimal) *big.Rat {
	r, err := parseRat(string(d))
	if err != nil {
		return new(big.Rat)
	}
	return r
}

// effectiveRate is tax ÷ subtotal at the tax_rate column's scale.
func effectiveRate(tax, subtotal *big.Rat) store.Decimal {
	if subtotal.Sign() == 0 {
		return "0.0000"
	}
	return store.Decimal(roundRat(new(big.Rat).Quo(tax, subtotal), 4))
}
