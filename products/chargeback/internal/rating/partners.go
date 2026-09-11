package rating

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The partner waterfall (DESIGN.md §Partners, EPIC #6867).
//
// ONE list price per SKU — the Sovereign's list book assigned to the source.
// Two independent discount steps come off it, both decided by the one
// combination engine (DiscountBySKU) and never by a typed markup per SKU:
//
//	list ─ customer discounts ─▶ CUSTOMER NET   what the end customer pays
//	list ─ partner tier       ─▶ PARTNER BUY    what the partner pays us
//	MARGIN = customer net − partner buy          derived per line, never entered
//
// A partner is billed by one of two MODELS, selected per partner and carried
// here as a value (billingModel), so resell and agent are not conditionals
// scattered through the run:
//
//   - resell (bill_to = partner): the end customer is rated at the partner's
//     DERIVED retail book (materialised from the list book by the partner's
//     retail rule) for informational showback; the partner is invoiced the
//     WHOLESALE statement — every customer line at list, less the tier
//     discounts, grouped by end customer.
//   - agent (bill_to = customer): the end customer is rated at our books and
//     invoiced by us; the partner is credited a COMMISSION statement — per
//     end customer, customer net minus partner buy (or commission_pct of the
//     net when the partner has no tier).
//
// Every figure below is exact (big.Rat) and rounded once, at the edge.

// billingModel is how a partner is billed. It decides three things and
// nothing else: which book prices an end customer's source, what the buy is
// when the partner has no tier, and what the partner's own statement is.
type billingModel interface {
	name() string
	// customerUnitPrice is the unit price an end customer is shown for a SKU
	// of a source priced by the given list book.
	customerUnitPrice(pc *partnerContext, listBookID, sku string, listUnitPrice *big.Rat) *big.Rat
	// noTierBuy is the partner buy for one SKU when the partner has no tier:
	// list under resell (the partner earns its markup only), net less the
	// commission under agent.
	noTierBuy(pc *partnerContext, list, net *big.Rat) *big.Rat
	// partnerStatement turns the period's per-line waterfall into the
	// partner's own statement on its party.
	partnerStatement(pc *partnerContext, party store.Customer, lines []store.RatedLine, currency string, from, to time.Time, settings store.BillingSettings) (store.StatementDraft, error)
}

// Models by partners.bill_to.
var billingModels = map[string]billingModel{
	store.BillToPartner:  resell{},
	store.BillToCustomer: agent{},
}

// ErrUnknownBillingModel is reported for a bill_to the run does not know.
var ErrUnknownBillingModel = errors.New("unknown partner billing model")

// partnerContext is what a run knows about one partner, loaded once per
// run: the model, the tier discounts live at the period start, the derived
// retail books by list book, and the combination rule.
type partnerContext struct {
	partner store.Partner
	party   store.Customer
	model   billingModel
	tier    []store.Discount
	retail  map[string]store.PriceBook
	rule    string
}

// loadPartner loads a partner's rating context as of `at`.
func loadPartner(ctx context.Context, st *store.Store, partnerID string, at time.Time, rule string) (*partnerContext, error) {
	p, err := st.GetPartner(ctx, partnerID)
	if err != nil {
		return nil, fmt.Errorf("partner %s: %w", partnerID, err)
	}
	model, ok := billingModels[p.BillTo]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBillingModel, p.BillTo)
	}
	pc := &partnerContext{partner: p, model: model, retail: map[string]store.PriceBook{}, rule: rule}
	if p.PartyCustomerID != "" {
		if pc.party, err = st.GetCustomer(ctx, store.OperatorScope, p.PartyCustomerID); err != nil {
			return nil, fmt.Errorf("partner %s party: %w", p.Slug, err)
		}
	}
	if p.TierID != nil {
		if pc.tier, err = st.ActiveTierDiscountsAt(ctx, *p.TierID, at); err != nil {
			return nil, fmt.Errorf("partner %s tier: %w", p.Slug, err)
		}
	}
	books, err := st.DerivedBooks(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("partner %s retail books: %w", p.Slug, err)
	}
	for _, b := range books {
		if b.DerivedFromBookID != nil {
			pc.retail[*b.DerivedFromBookID] = b
		}
	}
	return pc, nil
}

// ---------------------------------------------------------------------------
// the two models
// ---------------------------------------------------------------------------

type resell struct{}

func (resell) name() string { return store.BillToPartner }

// customerUnitPrice under resell is the derived retail price; a SKU the
// derived book does not (yet) carry is shown at list.
func (resell) customerUnitPrice(pc *partnerContext, listBookID, sku string, listUnitPrice *big.Rat) *big.Rat {
	if b, ok := pc.retail[listBookID]; ok {
		for _, it := range b.Items {
			if it.SKU == sku {
				if r, err := parseRat(string(it.UnitPrice)); err == nil {
					return r
				}
			}
		}
	}
	return listUnitPrice
}

func (resell) noTierBuy(_ *partnerContext, list, _ *big.Rat) *big.Rat { return new(big.Rat).Set(list) }

// partnerStatement under resell is the WHOLESALE statement: every customer
// line at LIST, grouped by end customer, less the tier discounts through the
// combination engine — so the statement shows list, the tier, and the buy
// exactly as a customer statement shows list, discounts and net.
func (resell) partnerStatement(pc *partnerContext, party store.Customer, lines []store.RatedLine, currency string, from, to time.Time, settings store.BillingSettings) (store.StatementDraft, error) {
	wholesale := make([]store.RatedLine, 0, len(lines))
	netTotal := new(big.Rat)
	for _, l := range lines {
		w := store.RatedLine{
			SourceID: l.SourceID, SKU: l.SKU, Quantity: l.Quantity, Unit: l.Unit, ResourceCount: l.ResourceCount,
			EndCustomerID: l.EndCustomerID, EndCustomerName: l.EndCustomerName,
			ListUnitPrice: l.ListUnitPrice, ListAmount: l.ListAmount, BuyAmount: l.BuyAmount, NetAmount: l.NetAmount,
		}
		w.UnitPrice, w.Amount = l.UnitPrice, l.Amount
		if l.ListUnitPrice != nil {
			w.UnitPrice = *l.ListUnitPrice
		}
		if l.ListAmount != nil {
			w.Amount = *l.ListAmount
		}
		if l.NetAmount != nil {
			netTotal.Add(netTotal, ratOf(*l.NetAmount))
		} else {
			netTotal.Add(netTotal, ratOf(l.Amount))
		}
		wholesale = append(wholesale, w)
	}
	discount, applied, err := ApplyDiscounts(wholesale, pc.tier, pc.rule)
	if err != nil {
		return store.StatementDraft{}, err
	}
	taxRate := store.EffectiveTaxRate(party, settings)
	subtotal, tax, total, err := TotalsWithDiscount(wholesale, discount, taxRate)
	if err != nil {
		return store.StatementDraft{}, err
	}
	buy := ratOf(subtotal)
	margin := new(big.Rat).Sub(netTotal, buy)
	buyD, marginD := store.Decimal(roundRat(buy, 6)), store.Decimal(roundRat(margin, 6))
	pid := pc.partner.ID
	return store.StatementDraft{
		CustomerID: party.ID, PeriodStart: from, PeriodEnd: to.AddDate(0, 0, -1), Currency: currency,
		Subtotal: subtotal, TaxRate: taxRate, Tax: tax, Total: total, Lines: wholesale,
		Discount: discount, AppliedDiscounts: applied, DiscountRule: pc.rule,
		PartnerID: &pid, Kind: store.StatementKindWholesale, BuyTotal: &buyD, MarginTotal: &marginD,
	}, nil
}

type agent struct{}

func (agent) name() string { return store.BillToCustomer }

// customerUnitPrice under agent is the list price: we invoice the customer
// at our books.
func (agent) customerUnitPrice(_ *partnerContext, _, _ string, listUnitPrice *big.Rat) *big.Rat {
	return listUnitPrice
}

// noTierBuy under agent is the net less the commission percentage: the
// partner keeps commission_pct of what the customer pays.
func (agent) noTierBuy(pc *partnerContext, _, net *big.Rat) *big.Rat {
	if pc.partner.CommissionPct == nil {
		return new(big.Rat).Set(net)
	}
	pct := ratOf(*pc.partner.CommissionPct)
	commission := new(big.Rat).Quo(new(big.Rat).Mul(net, pct), big.NewRat(100, 1))
	return new(big.Rat).Sub(net, commission)
}

// CommissionSKU is the SKU of a commission statement's lines (one per end
// customer).
const CommissionSKU = "commission"

// partnerStatement under agent is the COMMISSION statement: one line per end
// customer, customer net minus partner buy. It is money we owe the partner,
// posted as a credit on its party at issue (store.EntryCommission); it
// carries no tax of ours — the partner invoices its commission with its own.
func (agent) partnerStatement(pc *partnerContext, party store.Customer, lines []store.RatedLine, currency string, from, to time.Time, _ store.BillingSettings) (store.StatementDraft, error) {
	type acc struct {
		id, name       string
		list, buy, net *big.Rat
		resources      int
	}
	byCustomer := map[string]*acc{}
	var order []string
	for _, l := range lines {
		id := l.CustomerID
		if l.EndCustomerID != nil {
			id = *l.EndCustomerID
		}
		a := byCustomer[id]
		if a == nil {
			a = &acc{id: id, name: l.EndCustomerName, list: new(big.Rat), buy: new(big.Rat), net: new(big.Rat)}
			byCustomer[id] = a
			order = append(order, id)
		}
		a.resources += l.ResourceCount
		a.list.Add(a.list, ratOf(coalesce(l.ListAmount, l.Amount)))
		a.net.Add(a.net, ratOf(coalesce(l.NetAmount, l.Amount)))
		a.buy.Add(a.buy, ratOf(coalesce(l.BuyAmount, l.Amount)))
	}
	sort.SliceStable(order, func(i, j int) bool { return byCustomer[order[i]].name < byCustomer[order[j]].name })
	out := make([]store.RatedLine, 0, len(order))
	buyTotal, commissionTotal := new(big.Rat), new(big.Rat)
	for _, id := range order {
		a := byCustomer[id]
		commission := new(big.Rat).Sub(a.net, a.buy)
		if commission.Sign() < 0 {
			commission = new(big.Rat)
		}
		c := store.Decimal(roundRat(commission, 6))
		cid := a.id
		list, buy, net := store.Decimal(roundRat(a.list, 6)), store.Decimal(roundRat(a.buy, 6)), store.Decimal(roundRat(a.net, 6))
		out = append(out, store.RatedLine{
			SKU: CommissionSKU, Unit: "invoice", Quantity: "1", UnitPrice: c, Amount: c, ResourceCount: a.resources,
			EndCustomerID: &cid, EndCustomerName: a.name, ListAmount: &list, BuyAmount: &buy, NetAmount: &net,
		})
		buyTotal.Add(buyTotal, a.buy)
		commissionTotal.Add(commissionTotal, commission)
	}
	subtotal, tax, total, err := Totals(out, "0")
	if err != nil {
		return store.StatementDraft{}, err
	}
	buyD, marginD := store.Decimal(roundRat(buyTotal, 6)), store.Decimal(roundRat(commissionTotal, 6))
	pid := pc.partner.ID
	return store.StatementDraft{
		CustomerID: party.ID, PeriodStart: from, PeriodEnd: to.AddDate(0, 0, -1), Currency: currency,
		Subtotal: subtotal, TaxRate: "0", Tax: tax, Total: total, Lines: out, DiscountRule: pc.rule,
		PartnerID: &pid, Kind: store.StatementKindCommission, BuyTotal: &buyD, MarginTotal: &marginD,
	}, nil
}

func coalesce(p *store.Decimal, d store.Decimal) store.Decimal {
	if p != nil && strings.TrimSpace(string(*p)) != "" {
		return *p
	}
	return d
}

func ratOf(d store.Decimal) *big.Rat {
	r, err := parseRat(string(d))
	if err != nil {
		return new(big.Rat)
	}
	return r
}

// ---------------------------------------------------------------------------
// the per-customer waterfall
// ---------------------------------------------------------------------------

// waterfall is the outcome of rating one partner customer's period: the
// customer-facing lines with the per-line list / buy / net figures, the
// customer discount the engine took, and the totals.
type waterfall struct {
	lines            []store.RatedLine
	discount         store.Decimal
	applied          []AppliedDiscount
	buyTotal, margin store.Decimal
}

// customerLines prices the list lines the way the partner's model shows
// them to the end customer: the derived retail price under resell, list
// under agent. bookOf names the list book of each line's source.
func (pc *partnerContext) customerLines(listLines []store.RatedLine, bookOf map[string]string) ([]store.RatedLine, error) {
	out := make([]store.RatedLine, len(listLines))
	for i, l := range listLines {
		listUP, err := parseRat(string(l.UnitPrice))
		if err != nil {
			return nil, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		bookID := ""
		if l.SourceID != nil {
			bookID = bookOf[*l.SourceID]
		}
		up := pc.model.customerUnitPrice(pc, bookID, l.SKU, listUP)
		qty, err := parseRat(string(l.Quantity))
		if err != nil {
			return nil, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		c := l
		c.UnitPrice = store.Decimal(roundRat(up, 8))
		c.Amount = store.Decimal(roundRat(new(big.Rat).Mul(qty, up), 6))
		lup, lam := l.UnitPrice, l.Amount
		c.ListUnitPrice, c.ListAmount = &lup, &lam
		out[i] = c
	}
	return out, nil
}

// waterfall applies the two discount steps to one customer's period:
// customer discounts on the customer-facing lines (→ net), tier discounts on
// the list lines (→ buy), both through DiscountBySKU, then allocates each
// SKU's figures to its lines so the parts sum to the whole and margin is
// net − buy on every line.
func (pc *partnerContext) waterfall(listLines, customerLines []store.RatedLine, customerDiscounts []store.Discount) (waterfall, error) {
	var w waterfall
	discount, applied, perSKUDiscount, err := applyDiscounts(customerLines, customerDiscounts, pc.rule)
	if err != nil {
		return w, err
	}
	w.discount = store.Decimal(roundRat(discount, 6))
	w.applied = applied
	// Per-SKU bases.
	custBase, listBase := skuBases(customerLines), skuBases(listLines)
	// The tier decides the buy per SKU; no tier → the model's rule.
	var tierTaken map[string]*big.Rat
	if len(pc.tier) > 0 {
		if _, tierTaken, err = DiscountBySKU(listLines, pc.tier, pc.rule); err != nil {
			return w, err
		}
	}
	netBySKU, buyBySKU := map[string]*big.Rat{}, map[string]*big.Rat{}
	for sku, base := range custBase {
		net := new(big.Rat).Set(base)
		if d := perSKUDiscount[sku]; d != nil {
			net.Sub(net, d)
		}
		netBySKU[sku] = net
	}
	for sku, base := range listBase {
		var buy *big.Rat
		if tierTaken != nil {
			buy = new(big.Rat).Set(base)
			if t := tierTaken[sku]; t != nil {
				buy.Sub(buy, t)
			}
		} else {
			net := netBySKU[sku]
			if net == nil {
				net = new(big.Rat).Set(base)
			}
			buy = pc.model.noTierBuy(pc, base, net)
		}
		buyBySKU[sku] = buy
	}
	// Allocate each SKU's net and buy to its lines, in proportion to the
	// line's share of the SKU, rounded so the parts sum to the SKU figure.
	w.lines = make([]store.RatedLine, len(customerLines))
	copy(w.lines, customerLines)
	netParts := allocateBySKU(customerLines, netBySKU, func(l store.RatedLine) store.Decimal { return l.Amount })
	buyParts := allocateBySKU(listLines, buyBySKU, func(l store.RatedLine) store.Decimal { return l.Amount })
	buyTotal, netTotal := new(big.Rat), new(big.Rat)
	for i := range w.lines {
		n, b := netParts[i], buyParts[i]
		w.lines[i].NetAmount, w.lines[i].BuyAmount = &n, &b
		netTotal.Add(netTotal, ratOf(n))
		buyTotal.Add(buyTotal, ratOf(b))
	}
	w.buyTotal = store.Decimal(roundRat(buyTotal, 6))
	w.margin = store.Decimal(roundRat(new(big.Rat).Sub(netTotal, buyTotal), 6))
	return w, nil
}

// skuBases sums line amounts per SKU.
func skuBases(lines []store.RatedLine) map[string]*big.Rat {
	out := map[string]*big.Rat{}
	for _, l := range lines {
		if out[l.SKU] == nil {
			out[l.SKU] = new(big.Rat)
		}
		out[l.SKU].Add(out[l.SKU], ratOf(l.Amount))
	}
	return out
}

// allocateBySKU splits each SKU's figure across its lines in proportion to
// weight, rounded to 6 decimals by largest remainder so the lines of a SKU
// sum exactly to the SKU's rounded figure. A SKU whose lines weigh zero
// gets an even split.
func allocateBySKU(lines []store.RatedLine, bySKU map[string]*big.Rat, weight func(store.RatedLine) store.Decimal) []store.Decimal {
	out := make([]store.Decimal, len(lines))
	idx := map[string][]int{}
	for i, l := range lines {
		idx[l.SKU] = append(idx[l.SKU], i)
	}
	for sku, is := range idx {
		total := bySKU[sku]
		if total == nil {
			total = new(big.Rat)
		}
		weights := make([]*big.Rat, len(is))
		sum := new(big.Rat)
		for k, i := range is {
			weights[k] = ratOf(weight(lines[i]))
			sum.Add(sum, weights[k])
		}
		parts := allocate(total, weights, sum)
		for k, i := range is {
			out[i] = parts[k]
		}
	}
	return out
}

// allocate divides total across weights (largest-remainder at 6 decimals).
func allocate(total *big.Rat, weights []*big.Rat, sum *big.Rat) []store.Decimal {
	n := len(weights)
	out := make([]store.Decimal, n)
	if n == 0 {
		return out
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil))
	target := floorRat(new(big.Rat).Mul(total, scale)) // total in micro-units, exact share below
	// Exact shares in micro-units, floored, remainders kept.
	floors := make([]*big.Int, n)
	rems := make([]*big.Rat, n)
	assigned := new(big.Int)
	for i, w := range weights {
		var share *big.Rat
		if sum.Sign() == 0 {
			share = new(big.Rat).Quo(new(big.Rat).Mul(total, scale), big.NewRat(int64(n), 1))
		} else {
			share = new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).Mul(total, scale), w), sum)
		}
		f := floorRat(share)
		floors[i] = f
		rems[i] = new(big.Rat).Sub(share, new(big.Rat).SetInt(f))
		assigned.Add(assigned, f)
	}
	// Round the SKU total half-up to micro-units, then hand out the missing
	// units to the largest remainders.
	rounded := roundedMicro(new(big.Rat).Mul(total, scale))
	_ = target
	missing := new(big.Int).Sub(rounded, assigned)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]].Cmp(rems[order[b]]) > 0 })
	for _, i := range order {
		if missing.Sign() <= 0 {
			break
		}
		floors[i].Add(floors[i], big.NewInt(1))
		missing.Sub(missing, big.NewInt(1))
	}
	for missing.Sign() < 0 { // a negative total: take units back from the smallest remainders
		for k := n - 1; k >= 0 && missing.Sign() < 0; k-- {
			floors[order[k]].Sub(floors[order[k]], big.NewInt(1))
			missing.Add(missing, big.NewInt(1))
		}
	}
	for i, f := range floors {
		out[i] = store.Decimal(roundRat(new(big.Rat).Quo(new(big.Rat).SetInt(f), scale), 6))
	}
	return out
}

func floorRat(r *big.Rat) *big.Int {
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if r.Sign() < 0 && new(big.Int).Mul(q, r.Denom()).Cmp(r.Num()) != 0 {
		q.Sub(q, big.NewInt(1))
	}
	return q
}

func roundedMicro(scaled *big.Rat) *big.Int {
	half := big.NewRat(1, 2)
	if scaled.Sign() < 0 {
		return new(big.Int).Neg(floorRat(new(big.Rat).Add(new(big.Rat).Neg(scaled), half)))
	}
	return floorRat(new(big.Rat).Add(scaled, half))
}

// ---------------------------------------------------------------------------
// the derived retail book
// ---------------------------------------------------------------------------

// BelowBuyLine is a derived retail price below the partner's own buy price
// — the partner would resell at a loss. Reported, never refused: the rule is
// the partner's to set.
type BelowBuyLine struct {
	SKU    string        `json:"sku"`
	Unit   string        `json:"unit"`
	List   store.Decimal `json:"list_unit_price"`
	Buy    store.Decimal `json:"buy_unit_price"`
	Retail store.Decimal `json:"retail_unit_price"`
}

// DerivedBook is one materialised retail book with what it derives from and
// its below-buy lines.
type DerivedBook struct {
	Book         store.PriceBook `json:"book"`
	ListBookID   string          `json:"list_book_id"`
	ListBookName string          `json:"list_book_name"`
	BelowBuy     []BelowBuyLine  `json:"below_buy"`
}

// DeriveRetailBooks materialises a resell partner's retail books — one per
// list book its customers' sources are assigned to — from its retail rule
// and its tier (DESIGN.md §Partners), and removes derived books whose list
// book no partner customer uses any more. An agent partner, or a partner
// without a rule, has no retail book: any left over is removed.
//
// Per SKU: buy = list less the tier through the combination engine (the same
// DiscountBySKU that rates a statement); base = list or buy per the rule;
// retail = base × (1 + markup), the markup being the most specific override
// (sku, then service, then the rule's default). Unit prices are rounded to
// 8 decimals like every unit price in a book.
func DeriveRetailBooks(ctx context.Context, st *store.Store, partnerID string) ([]DerivedBook, error) {
	p, err := st.GetPartner(ctx, partnerID)
	if err != nil {
		return nil, err
	}
	rule, hasRule, err := st.GetRetailRule(ctx, partnerID)
	if err != nil {
		return nil, err
	}
	if !hasRule || p.BillTo != store.BillToPartner {
		return []DerivedBook{}, st.DeleteDerivedBooks(ctx, partnerID, nil)
	}
	var tier []store.Discount
	if p.TierID != nil {
		if tier, err = st.ActiveTierDiscountsAt(ctx, *p.TierID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	lists, err := st.ListBooksForPartner(ctx, partnerID)
	if err != nil {
		return nil, err
	}
	settings, err := st.GetBillingSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := []DerivedBook{}
	keep := []string{}
	for _, list := range lists {
		items, below, err := deriveItems(list, tier, rule, settings.DiscountRule)
		if err != nil {
			return nil, fmt.Errorf("derive %s for %s: %w", list.Name, p.Slug, err)
		}
		name := p.Name + " · retail · " + list.Name
		desc := fmt.Sprintf("Derived for partner %s from %s by its retail rule (%s + %s %%); read-only — edit the rule, the tier or the list book.", p.Name, list.Name, rule.Base, rule.MarkupPct)
		book, err := st.UpsertDerivedBook(ctx, partnerID, list, name, desc, items)
		if err != nil {
			return nil, err
		}
		keep = append(keep, list.ID)
		out = append(out, DerivedBook{Book: book, ListBookID: list.ID, ListBookName: list.Name, BelowBuy: below})
	}
	if err := st.DeleteDerivedBooks(ctx, partnerID, keep); err != nil {
		return nil, err
	}
	return out, nil
}

// deriveItems computes one derived book's items from a list book.
func deriveItems(list store.PriceBook, tier []store.Discount, rule store.RetailRule, combination string) ([]store.PriceItem, []BelowBuyLine, error) {
	// One unit-line per SKU: amount = the list unit price, so the engine's
	// per-SKU reduction is exactly the discount off one unit.
	unitLines := make([]store.RatedLine, 0, len(list.Items))
	for _, it := range list.Items {
		unitLines = append(unitLines, store.RatedLine{SKU: it.SKU, Quantity: "1", Unit: it.Unit, UnitPrice: it.UnitPrice, Amount: it.UnitPrice})
	}
	var taken map[string]*big.Rat
	if len(tier) > 0 {
		var err error
		if _, taken, err = DiscountBySKU(unitLines, tier, combination); err != nil {
			return nil, nil, err
		}
	}
	hundred := big.NewRat(100, 1)
	items := make([]store.PriceItem, 0, len(list.Items))
	below := []BelowBuyLine{}
	for _, it := range list.Items {
		listUP, err := parseRat(string(it.UnitPrice))
		if err != nil {
			return nil, nil, fmt.Errorf("sku %s: %w", it.SKU, err)
		}
		buy := new(big.Rat).Set(listUP)
		if t := taken[it.SKU]; t != nil {
			buy.Sub(buy, t)
		}
		base := listUP
		if rule.Base == store.RetailBaseBuy {
			base = buy
		}
		markup := markupFor(rule, it.SKU)
		retail := new(big.Rat).Mul(base, new(big.Rat).Add(big.NewRat(1, 1), new(big.Rat).Quo(markup, hundred)))
		if retail.Sign() < 0 {
			retail = new(big.Rat)
		}
		retailD := store.Decimal(roundRat(retail, 8))
		items = append(items, store.PriceItem{SKU: it.SKU, Unit: it.Unit, UnitPrice: retailD, Description: it.Description})
		if retail.Cmp(buy) < 0 {
			below = append(below, BelowBuyLine{SKU: it.SKU, Unit: it.Unit, List: it.UnitPrice, Buy: store.Decimal(roundRat(buy, 8)), Retail: retailD})
		}
	}
	return items, below, nil
}

// markupFor picks the markup for a SKU: the sku override, else the service
// override (the SKU's first segment — ecs, evs, eip, plan …), else the
// rule's default. Most specific wins, like the discount engine.
func markupFor(rule store.RetailRule, sku string) *big.Rat {
	service := ServiceOf(sku)
	var svc *store.RetailOverride
	for i := range rule.Overrides {
		o := &rule.Overrides[i]
		switch {
		case o.Scope == "sku" && o.Key == sku:
			return ratOf(o.MarkupPct)
		case o.Scope == "service" && strings.EqualFold(o.Key, service):
			svc = o
		}
	}
	if svc != nil {
		return ratOf(svc.MarkupPct)
	}
	return ratOf(rule.MarkupPct)
}

// ServiceOf is a SKU's service: its first dot-separated segment, the same
// split the margin report groups by (ecs.s6.large.2 → ecs, plan.s → plan).
func ServiceOf(sku string) string {
	if i := strings.IndexByte(sku, '.'); i > 0 {
		return sku[:i]
	}
	return sku
}
