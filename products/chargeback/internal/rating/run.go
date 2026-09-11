package rating

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Stopped-instance policies (price_books.bill_stopped).
const (
	BillStoppedCompute     = "compute"      // stopped hours billed like running hours
	BillStoppedStorageOnly = "storage-only" // stopped hours: no instance charge, volumes still billed
	BillStoppedNone        = "none"         // stopped hours: no instance charge and no charge for volumes attached to it
)

// ErrMixedCurrency is returned when one customer's sources are assigned
// books in different currencies: a statement is issued in ONE currency and
// nothing here converts money, so the run is refused for that customer
// until the operator assigns books of one currency (DESIGN.md §2.9). The
// API answers 400 with the message.
var ErrMixedCurrency = errors.New("mixed currencies")

// ErrNoPriceBook is reported for a customer none of whose sources has a
// price book: there is nothing to rate against.
var ErrNoPriceBook = errors.New("no price book assigned to any of the customer's sources")

// Rate prices aggregated usage against a price book. Usage for SKUs missing
// from the book is reported in unpriced and produces no line.
func Rate(usage []store.RatableUsage, items map[string]store.PriceItem, policy string) ([]store.RatedLine, []string, error) {
	var lines []store.RatedLine
	unpricedSet := map[string]bool{}
	for _, u := range usage {
		item, ok := items[u.SKU]
		if !ok {
			unpricedSet[u.SKU] = true
			continue
		}
		qty := u.Quantity
		switch policy {
		case BillStoppedStorageOnly:
			if strings.HasPrefix(u.SKU, "ecs.") {
				var err error
				if qty, err = Sub(u.Quantity, u.StoppedQuantity); err != nil {
					return nil, nil, err
				}
			}
		case BillStoppedNone:
			if strings.HasPrefix(u.SKU, "ecs.") || strings.HasPrefix(u.SKU, "evs.") {
				var err error
				if qty, err = Sub(u.Quantity, u.StoppedQuantity); err != nil {
					return nil, nil, err
				}
			}
		}
		amount, err := Amount(qty, item.UnitPrice)
		if err != nil {
			return nil, nil, fmt.Errorf("sku %s: %w", u.SKU, err)
		}
		src := u.SourceID
		lines = append(lines, store.RatedLine{
			SourceID:      &src,
			SKU:           u.SKU,
			Quantity:      qty,
			Unit:          u.Unit,
			UnitPrice:     item.UnitPrice,
			Amount:        amount,
			ResourceCount: u.ResourceCount,
		})
	}
	unpriced := make([]string, 0, len(unpricedSet))
	for k := range unpricedSet {
		unpriced = append(unpriced, k)
	}
	sort.Strings(unpriced)
	return lines, unpriced, nil
}

// Totals computes subtotal, tax and total for lines at the given rate.
func Totals(lines []store.RatedLine, taxRate store.Decimal) (subtotal, tax, total store.Decimal, err error) {
	amounts := make([]store.Decimal, len(lines))
	for i, l := range lines {
		amounts[i] = l.Amount
	}
	if subtotal, err = Sum(amounts...); err != nil {
		return
	}
	if tax, err = Tax(subtotal, taxRate); err != nil {
		return
	}
	total, err = Sum(subtotal, tax)
	return
}

// TotalsWithDiscount is Totals with a discount applied to the subtotal BEFORE
// tax (#6862). Tax must be charged on what the customer actually pays: taxing
// the list price and discounting afterwards overcharges VAT on money that was
// never invoiced, which is a compliance problem, not a rounding one.
//
// The returned subtotal is the NET (list minus discount) so
// subtotal + tax == total still holds for every downstream reader.
func TotalsWithDiscount(lines []store.RatedLine, discount store.Decimal, taxRate store.Decimal) (subtotal, tax, total store.Decimal, err error) {
	gross, _, _, err := Totals(lines, taxRate)
	if err != nil {
		return
	}
	if subtotal, err = Sub(gross, discount); err != nil {
		return
	}
	if tax, err = Tax(subtotal, taxRate); err != nil {
		return
	}
	total, err = Sum(subtotal, tax)
	return
}

// DefaultTaxRate is Oman VAT — the Sovereign default when the operator has
// set none (store.DefaultTaxRate is the same figure at the column's scale).
const DefaultTaxRate store.Decimal = "0.05"

// Result is one customer's — or one partner's — outcome in a run.
type Result struct {
	CustomerID   string   `json:"customer_id"`
	CustomerName string   `json:"customer_name"`
	StatementID  string   `json:"statement_id,omitempty"`
	Lines        int      `json:"lines"`
	Total        string   `json:"total,omitempty"`
	UnpricedSKUs []string `json:"unpriced_skus,omitempty"`
	// NotSoldPerUse lists the platform meters a platform book deliberately
	// leaves unpriced (the allocation basis) — reported apart from
	// UnpricedSKUs so nobody is told to "add a rate" for them.
	NotSoldPerUse []string `json:"not_sold_per_use,omitempty"`
	// UnbookedSources names the customer's sources that have usage in the
	// period but no price book; their SKUs are in UnpricedSKUs.
	UnbookedSources []string `json:"unbooked_sources,omitempty"`
	Error           string   `json:"error,omitempty"`
	// The partner keys (DESIGN.md §13), additive. On a customer's
	// result PartnerID is its partner; on a partner's own result — the
	// wholesale or commission statement on its party, whose id is
	// CustomerID — Kind says which and PartnerName who.
	PartnerID   string `json:"partner_id,omitempty"`
	PartnerName string `json:"partner_name,omitempty"`
	Kind        string `json:"statement_kind,omitempty"`

	// The commercial terms (DESIGN.md §15) the period was rated under.
	// ContractID / ContractName name the agreement; AppliedTerms is what the
	// allowances, tiers and commitments did, per SKU; TrueUp is the
	// shortfall line a minimum commitment produced, absent when the period
	// met its minimum.
	ContractID   string      `json:"contract_id,omitempty"`
	ContractName string      `json:"contract_name,omitempty"`
	AppliedTerms []Breakdown `json:"applied_terms,omitempty"`
	TrueUp       string      `json:"true_up,omitempty"`
	// The tax outcome (DESIGN.md §17): the per-rule summary the statement
	// froze, and every determination that was not the plain reading of the
	// rule table — an expired exemption certificate above all, which the
	// operator has to be told about on the run that silently charged tax.
	TaxLines []store.TaxLine `json:"tax_lines,omitempty"`
	TaxAudit []string        `json:"tax_audit,omitempty"`
}

// Run rates every (or one) customer's usage for a period into draft
// statements. A customer's statement is the sum of its sources, each rated
// by ITS OWN book (DESIGN.md §2): a cloud source by its cloud book, a
// platform source by its platform book. Customers with no book on any
// source are reported, not rated. A single-customer run whose sources use
// books of different currencies returns ErrMixedCurrency; in an all-customer
// run that customer carries the message in its Result and the others proceed.
//
// A customer with a PARTNER (DESIGN.md §13) is rated through the
// waterfall: its lines carry list, buy and net, its statement the partner
// keys. After the customer pass the run writes each affected partner's OWN
// statement for the period — wholesale (resell) or commission (agent) — on
// the partner's party, from the lines the customer pass just froze. One run,
// both kinds of statement.
func Run(ctx context.Context, st *store.Store, period, customerID string) ([]Result, error) {
	from, to, err := store.PeriodBounds(period)
	if err != nil {
		return nil, err
	}
	customers, err := st.ListCustomers(ctx, store.OperatorScope)
	if err != nil {
		return nil, err
	}
	// The discount combination rule (DESIGN.md §2.11) is read once per run,
	// so every statement of the run states the same rule.
	settings, err := st.GetBillingSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("billing settings: %w", err)
	}
	rule := settings.DiscountRule
	if rule == "" {
		rule = store.DefaultDiscountRule
	}
	// The tax rules and SKU categories (DESIGN.md §17), read once per run
	// for the same reason: every statement of a run must resolve against
	// one picture of the rule table, not against whatever it said when that
	// customer's turn came round.
	taxEngine, err := st.TaxEngineFor(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("tax rules: %w", err)
	}
	partners := map[string]*partnerContext{}
	var partnerOrder []string
	partnerOf := func(id string) (*partnerContext, error) {
		if pc, ok := partners[id]; ok {
			return pc, nil
		}
		pc, err := loadPartner(ctx, st, id, from, rule)
		if err != nil {
			return nil, err
		}
		partners[id] = pc
		partnerOrder = append(partnerOrder, id)
		return pc, nil
	}
	var results []Result
	for _, c := range customers {
		if customerID != "" && c.ID != customerID {
			continue
		}
		res := Result{CustomerID: c.ID, CustomerName: c.Name}
		var pc *partnerContext
		if c.PartnerID != nil {
			res.PartnerID, res.PartnerName = *c.PartnerID, c.PartnerName
			if pc, err = partnerOf(*c.PartnerID); err != nil {
				res.Error = err.Error()
				results = append(results, res)
				continue
			}
		}
		stmt, detail, err := rateCustomer(ctx, st, c, pc, from, to, settings, taxEngine)
		if err != nil {
			if customerID != "" && errors.Is(err, ErrMixedCurrency) {
				return nil, err
			}
			res.Error = err.Error()
			if !errors.Is(err, ErrNoPriceBook) && !errors.Is(err, ErrMixedCurrency) {
				slog.Warn("statement run failed for customer", "customer", c.Slug, "period", period, "error", err)
			}
			results = append(results, res)
			continue
		}
		res.StatementID = stmt.ID
		res.Lines = len(stmt.Lines)
		res.Total = string(stmt.Total)
		res.UnpricedSKUs = detail.unpriced
		res.NotSoldPerUse = detail.notSold
		res.UnbookedSources = detail.unbooked
		res.Kind = stmt.Kind
		res.ContractID, res.ContractName = detail.contractID, detail.contractName
		res.AppliedTerms, res.TrueUp = detail.terms, detail.trueUp
		res.TaxLines, res.TaxAudit = stmt.TaxLines, detail.taxAudit
		results = append(results, res)
	}
	if customerID != "" && len(results) == 0 {
		return nil, store.ErrNotFound
	}
	// The partner pass: one statement per affected partner, from the lines
	// the customer pass froze (including customers whose statement for the
	// period is already issued and was left untouched).
	for _, id := range partnerOrder {
		pc := partners[id]
		res := Result{PartnerID: pc.partner.ID, PartnerName: pc.partner.Name, CustomerID: pc.party.ID, CustomerName: pc.party.Name}
		stmt, written, err := ratePartner(ctx, st, pc, from, to, settings)
		if err != nil {
			res.Error = err.Error()
			if !errors.Is(err, ErrMixedCurrency) {
				slog.Warn("partner statement run failed", "partner", pc.partner.Slug, "period", period, "error", err)
			}
			results = append(results, res)
			continue
		}
		if !written {
			continue
		}
		res.StatementID, res.Lines, res.Total, res.Kind = stmt.ID, len(stmt.Lines), string(stmt.Total), stmt.Kind
		results = append(results, res)
	}
	return results, nil
}

// rateDetail is what a run reports besides the statement.
type rateDetail struct {
	unpriced, notSold, unbooked []string
	// The commercial terms (DESIGN.md §15): the contract in force, what the
	// shapes did per SKU, and the true-up the minimum commitment produced.
	contractID, contractName, trueUp string
	terms                            []Breakdown
	// taxAudit records every tax determination that was not the plain
	// reading of the rule table — an expired exemption certificate, a
	// reverse-charge finding (DESIGN.md §17).
	taxAudit []string
}

// rateCustomer rates one customer: its usage grouped per source, each
// source's rows priced with that source's book and stopped-instance policy,
// the lines summed into one statement in the one currency the books share.
// settings carry the operator-selected combination rule, read once per run
// so every statement of the run states the same rule (#6867), and the
// Sovereign's default tax rate the customer's own profile overrides
// (DESIGN.md §9.4). pc is the customer's partner context, nil for a direct
// customer.
func rateCustomer(ctx context.Context, st *store.Store, c store.Customer, pc *partnerContext, from, to time.Time, settings store.BillingSettings, taxEngine *store.TaxEngine) (store.Statement, rateDetail, error) {
	var detail rateDetail
	discountRule := settings.DiscountRule
	// The customer's rate: zero when exempt, its own when it has one, else
	// the Sovereign default. Frozen onto the invoice at issue. Since
	// DESIGN.md §17 this is the FALLBACK the rule engine reduces to when a
	// Sovereign has authored no rule and a customer has no country — it is
	// no longer the only answer, but it is still the answer for everyone
	// who was on the single-rate model.
	taxRate := store.EffectiveTaxRate(c, settings)
	sources, err := st.ListSources(ctx, store.OperatorScope, c.ID)
	if err != nil {
		return store.Statement{}, detail, fmt.Errorf("sources: %w", err)
	}
	byID := map[string]store.CostSource{}
	booked := 0
	for _, s := range sources {
		byID[s.ID] = s
		if s.PriceBookID != nil {
			booked++
		}
	}
	if booked == 0 {
		return store.Statement{}, detail, ErrNoPriceBook
	}
	usage, err := st.UsageForRating(ctx, c.ID, from, to)
	if err != nil {
		return store.Statement{}, detail, err
	}
	// Usage per source, in source order (ListSources orders by layer, then
	// location), so the lines of a statement are stable across runs.
	perSource := map[string][]store.RatableUsage{}
	for _, u := range usage {
		perSource[u.SourceID] = append(perSource[u.SourceID], u)
	}
	books := map[string]store.PriceBook{}
	bookOf := map[string]string{} // source id → list book id
	// shapeItems names each SKU's commercial shape, and bookItem the LIST
	// unit price of a (book, SKU) — the figure a partner's retail price is
	// derived from, which a tiered line's own unit price is not.
	shapeItems := map[string]store.PriceItem{}
	currency := ""
	var lines []store.RatedLine
	unpricedSet, notSoldSet := map[string]bool{}, map[string]bool{}
	for _, src := range sources {
		rows := perSource[src.ID]
		if src.PriceBookID == nil {
			if len(rows) > 0 {
				detail.unbooked = append(detail.unbooked, src.Label())
				for _, u := range rows {
					unpricedSet[u.SKU] = true
				}
			}
			continue
		}
		pb, ok := books[*src.PriceBookID]
		if !ok {
			if pb, err = st.GetPriceBook(ctx, *src.PriceBookID); err != nil {
				return store.Statement{}, detail, fmt.Errorf("price book of source %s: %w", src.Label(), err)
			}
			books[pb.ID] = pb
		}
		bookOf[src.ID] = pb.ID
		// The statement's currency is the one every booked source shares;
		// the first booked source (usage or not) sets it.
		if currency == "" {
			currency = pb.Currency
		} else if pb.Currency != currency {
			return store.Statement{}, detail, fmt.Errorf("%w: source %s is priced in %s while another source of %s is priced in %s; a statement is issued in one currency — assign books of one currency", ErrMixedCurrency, src.Label(), pb.Currency, c.Name, currency)
		}
		if len(rows) == 0 {
			continue
		}
		items := map[string]store.PriceItem{}
		pricesPlatformMeter := false
		for _, it := range pb.Items {
			items[it.SKU] = it
			if store.IsPlatformMeter(it.SKU) {
				pricesPlatformMeter = true
			}
			// The commercial shape of a SKU (DESIGN.md §15.1) belongs to the
			// agreement, not to one project: the FIRST booked source in
			// source order that prices the SKU names its shape for the whole
			// statement, so an allowance is the customer's month.
			if _, seen := shapeItems[it.SKU]; !seen {
				shapeItems[it.SKU] = it
			}
		}
		srcLines, unpriced, err := Rate(rows, items, pb.BillStopped)
		if err != nil {
			return store.Statement{}, detail, fmt.Errorf("source %s: %w", src.Label(), err)
		}
		lines = append(lines, srcLines...)
		for _, sku := range unpriced {
			// A platform book that prices none of the k8s.* meters sells
			// plans, not vCPU-hours: those meters are the allocation basis,
			// not a gap in the book.
			if src.Layer == store.LayerPlatform && store.IsPlatformMeter(sku) && !pricesPlatformMeter {
				notSoldSet[sku] = true
				continue
			}
			unpricedSet[sku] = true
		}
	}
	detail.unpriced = sortedKeys(unpricedSet)
	detail.notSold = sortedKeys(notSoldSet)
	// DESIGN.md §15 — the commercial terms, BEFORE the discounts: the
	// allowance comes off the quantity, the tiers price what is left, the
	// commitment reprices the head of it. One engine, one pass, and lines
	// whose SKU carries no shape come back untouched.
	terms, err := LoadTerms(ctx, st, c.ID, from, shapeItems)
	if err != nil {
		return store.Statement{}, detail, err
	}
	if terms.Contract != nil {
		detail.contractID, detail.contractName = terms.Contract.ID, terms.Contract.Name
	}
	// ALWAYS applied, contract or not: an allowance and a tier ladder belong
	// to the PLAN — the price-book item — and a customer that has signed
	// nothing is still on a plan. ApplyTerms no-ops on a SKU whose item
	// carries no shape, which is every item of every book written before §15.
	var appliedTerms []Breakdown
	if lines, appliedTerms, err = ApplyTerms(lines, shapeItems, terms); err != nil {
		return store.Statement{}, detail, err
	}
	detail.terms = appliedTerms
	// #6862 — discounts reduce the subtotal BEFORE tax. Taxing the list price
	// and then discounting would overcharge tax on money the customer never
	// paid.
	discounts, err := st.ActiveDiscountsAt(ctx, c.ID, from)
	if err != nil {
		return store.Statement{}, detail, fmt.Errorf("discounts: %w", err)
	}
	if discountRule == "" {
		discountRule = store.DefaultDiscountRule
	}
	draft := store.StatementDraft{
		CustomerID:   c.ID,
		PeriodStart:  from,
		PeriodEnd:    to.AddDate(0, 0, -1),
		Currency:     currency,
		TaxRate:      taxRate,
		DiscountRule: discountRule,
	}
	if terms.Contract != nil {
		id := terms.Contract.ID
		draft.ContractID = &id
	}
	// The LIST unit price of a (book, SKU): what a partner's retail price is
	// derived from. A line reshaped by a tier or an allowance no longer
	// carries it, so the waterfall reads it from the book itself.
	listUnitPrice := func(bookID, sku string) store.Decimal {
		pb, ok := books[bookID]
		if !ok {
			return ""
		}
		for _, it := range pb.Items {
			if it.SKU == sku {
				return it.UnitPrice
			}
		}
		return ""
	}
	if pc == nil {
		// A direct customer: list, customer discounts, net — as always.
		discountTotal, applied, err := ApplyDiscounts(lines, discounts, discountRule)
		if err != nil {
			return store.Statement{}, detail, err
		}
		draft.Lines, draft.Discount, draft.AppliedDiscounts = lines, discountTotal, applied
	} else {
		// A partner customer: the waterfall (DESIGN.md §13). The
		// customer-facing lines are priced the way the partner's model shows
		// them (retail under resell, list under agent); the customer
		// discounts give the net, the tier the buy, per line.
		customerLines, err := pc.customerLines(lines, bookOf, listUnitPrice)
		if err != nil {
			return store.Statement{}, detail, err
		}
		w, err := pc.waterfall(lines, customerLines, discounts)
		if err != nil {
			return store.Statement{}, detail, err
		}
		pid := pc.partner.ID
		buy, margin := w.buyTotal, w.margin
		draft.Lines, draft.Discount, draft.AppliedDiscounts = w.lines, w.discount, w.applied
		draft.PartnerID, draft.Kind, draft.BuyTotal, draft.MarginTotal = &pid, store.StatementKindCustomer, &buy, &margin
	}
	// DESIGN.md §15.4 — the TRUE-UP, after the discounts and before the tax:
	// a period whose net falls below the contract's monthly minimum carries
	// a named line for the shortfall. Never a silent adjustment of the
	// totals — the customer has to be able to read why it is charged.
	if terms.Contract != nil && terms.Contract.MinimumCommitment != nil {
		line, ok, err := TrueUp(draft.Lines, draft.Discount, *terms.Contract.MinimumCommitment)
		if err != nil {
			return store.Statement{}, detail, err
		}
		if ok {
			if pc != nil {
				// A true-up is the SOVEREIGN's charge under its own
				// contract, not usage the partner resold: it carries no
				// margin, so buy and net are the same figure.
				amount := line.Amount
				line.BuyAmount, line.NetAmount = &amount, &amount
			}
			draft.Lines = append(draft.Lines, line)
			detail.trueUp = string(line.Amount)
		}
	}
	// DESIGN.md §17 — TAX, still the last step, now per RULE. Each line is
	// placed in a tax category, each category resolves to a rule for THIS
	// buyer at the period's last day, and each rule taxes its own base with
	// the discount apportioned across them. A Sovereign with no rules and a
	// customer with no country resolve to one rule at taxRate, which is
	// exactly the arithmetic this line did before §17.
	//
	// The date is the LAST DAY OF THE PERIOD: the rule in force when the
	// supply completed rates the whole period. A rate that changes
	// mid-period is rated by rating two periods, not by splitting a line.
	taxedLines, tax, err := ApplyTax(draft.Lines, draft.Discount, taxEngine, store.TaxPartyOf(c), draft.PeriodEnd, taxRate)
	if err != nil {
		return store.Statement{}, detail, err
	}
	draft.Lines = taxedLines
	draft.Subtotal, draft.Tax, draft.Total = tax.Subtotal, tax.Tax, tax.Total
	draft.TaxRate, draft.TaxLines, draft.TaxAudit = tax.Rate, tax.Lines, tax.Audit
	detail.taxAudit = tax.Audit
	// DESIGN.md §19 — the COST-CENTRE breakdown, after everything. It reads
	// the totals the waterfall just produced and attributes them; it never
	// feeds back into them, which is why it is computed here and not
	// anywhere inside the steps above. The weights are the period's usage
	// under each centre, and what no rule named is one unassigned row.
	weights, err := st.CostCentreWeights(ctx, store.OperatorScope, c.ID, from, to)
	if err != nil {
		return store.Statement{}, detail, fmt.Errorf("cost centres: %w", err)
	}
	if draft.CostCentreLines, err = CostCentreBreakdown(weights, draft.Subtotal, draft.Discount, draft.Tax); err != nil {
		return store.Statement{}, detail, fmt.Errorf("cost centres: %w", err)
	}
	stmt, err := st.WriteDraftStatement(ctx, draft)
	return stmt, detail, err
}

// ratePartner writes a partner's own statement for the period — wholesale
// or commission per its billing model — from the per-line waterfall frozen
// on its customers' statements. written=false when the partner's customers
// have no rated lines in the period: nothing is invoiced for nothing.
func ratePartner(ctx context.Context, st *store.Store, pc *partnerContext, from, to time.Time, settings store.BillingSettings) (store.Statement, bool, error) {
	if pc.party.ID == "" {
		return store.Statement{}, false, fmt.Errorf("partner %s has no party account", pc.partner.Slug)
	}
	lines, currencies, err := st.PartnerPeriodLines(ctx, pc.partner.ID, from)
	if err != nil {
		return store.Statement{}, false, err
	}
	if len(lines) == 0 {
		return store.Statement{}, false, nil
	}
	if len(currencies) > 1 {
		return store.Statement{}, false, fmt.Errorf("%w: the customers of %s are billed in %s; a partner statement is issued in one currency", ErrMixedCurrency, pc.partner.Name, strings.Join(currencies, " and "))
	}
	draft, err := pc.model.partnerStatement(pc, pc.party, lines, currencies[0], from, to, settings)
	if err != nil {
		return store.Statement{}, false, err
	}
	stmt, err := st.WriteDraftStatement(ctx, draft)
	if err != nil {
		return store.Statement{}, false, err
	}
	return stmt, true, nil
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
