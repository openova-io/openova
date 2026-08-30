package rating

import (
	"context"
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

// DefaultTaxRate is Oman VAT.
const DefaultTaxRate store.Decimal = "0.05"

// Result is one customer's outcome in a run.
type Result struct {
	CustomerID   string   `json:"customer_id"`
	CustomerName string   `json:"customer_name"`
	StatementID  string   `json:"statement_id,omitempty"`
	Lines        int      `json:"lines"`
	Total        string   `json:"total,omitempty"`
	UnpricedSKUs []string `json:"unpriced_skus,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Run rates every (or one) customer's usage for a period into draft
// statements. Customers without a price book are reported, not rated.
func Run(ctx context.Context, st *store.Store, period, customerID string) ([]Result, error) {
	from, to, err := store.PeriodBounds(period)
	if err != nil {
		return nil, err
	}
	customers, err := st.ListCustomers(ctx, store.OperatorScope)
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, c := range customers {
		if customerID != "" && c.ID != customerID {
			continue
		}
		res := Result{CustomerID: c.ID, CustomerName: c.Name}
		if c.PriceBookID == nil {
			res.Error = "no price book assigned"
			results = append(results, res)
			continue
		}
		stmt, unpriced, err := rateCustomer(ctx, st, c, *c.PriceBookID, from, to)
		if err != nil {
			res.Error = err.Error()
			slog.Warn("statement run failed for customer", "customer", c.Slug, "period", period, "error", err)
			results = append(results, res)
			continue
		}
		res.StatementID = stmt.ID
		res.Lines = len(stmt.Lines)
		res.Total = string(stmt.Total)
		res.UnpricedSKUs = unpriced
		results = append(results, res)
	}
	if customerID != "" && len(results) == 0 {
		return nil, store.ErrNotFound
	}
	return results, nil
}

func rateCustomer(ctx context.Context, st *store.Store, c store.Customer, priceBookID string, from, to time.Time) (store.Statement, []string, error) {
	pb, err := st.GetPriceBook(ctx, priceBookID)
	if err != nil {
		return store.Statement{}, nil, fmt.Errorf("price book: %w", err)
	}
	items := map[string]store.PriceItem{}
	for _, it := range pb.Items {
		items[it.SKU] = it
	}
	usage, err := st.UsageForRating(ctx, c.ID, from, to)
	if err != nil {
		return store.Statement{}, nil, err
	}
	lines, unpriced, err := Rate(usage, items, pb.BillStopped)
	if err != nil {
		return store.Statement{}, nil, err
	}
	subtotal, tax, total, err := Totals(lines, DefaultTaxRate)
	if err != nil {
		return store.Statement{}, nil, err
	}
	stmt, err := st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID:  c.ID,
		PeriodStart: from,
		PeriodEnd:   to.AddDate(0, 0, -1),
		Currency:    pb.Currency,
		Subtotal:    subtotal,
		TaxRate:     DefaultTaxRate,
		Tax:         tax,
		Total:       total,
		Lines:       lines,
	})
	return stmt, unpriced, err
}
