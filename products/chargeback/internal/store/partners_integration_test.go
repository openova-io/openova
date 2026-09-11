package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The partner waterfall, end to end and to the last decimal (DESIGN.md
// §13, EPIC #6867):
//
//	list 100  − customer 10 %  → customer NET   90
//	list 100  − tier     30 %  → partner BUY    70
//	                             MARGIN         20   (derived, never entered)
//
// Both billing models are first-class here: the RESELL partner is invoiced a
// wholesale statement summing its customers at the buy price, and the AGENT
// partner is credited a commission statement of exactly the margin.

const partnerSKU = "ecs.m7n.xlarge.8"

// seedPartnerUsage gives a customer one hour of one SKU in August 2026, so a
// unit price of 100 rates to exactly 100.000000.
func seedPartnerUsage(t *testing.T, st *store.Store, customerID, sourceID string) {
	t.Helper()
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := st.UpsertUsage(context.Background(), []store.UsageRecord{{
		CustomerID: customerID, SourceID: sourceID, ResourceID: "vm-" + customerID[:8], ResourceKind: "ecs",
		SKU: partnerSKU, Quantity: "1.000000", Unit: "instance-hour",
		WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215",
	}}); err != nil {
		t.Fatal(err)
	}
}

// resultFor picks one customer's result out of a run.
func resultFor(results []rating.Result, customerID string) rating.Result {
	for _, r := range results {
		if r.CustomerID == customerID {
			return r
		}
	}
	return rating.Result{}
}

func TestIntegrationPartnerWaterfallResellAndAgent(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	// ONE list price per SKU — the Sovereign's list book.
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC list 2026", Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: partnerSKU, Unit: "instance-hour", UnitPrice: "100"}}, true); err != nil {
		t.Fatal(err)
	}

	// One tier, 30 % off list: the partner BUY step.
	gold, err := st.CreatePartnerTier(ctx, "Gold", "30 % off list")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceTierDiscounts(ctx, gold.ID, []store.DiscountInput{{Name: "Gold 30 %", Kind: "percent", Value: "30"}}); err != nil {
		t.Fatal(err)
	}

	// A RESELL partner and an AGENT partner, both on the same tier.
	resellCo, err := st.CreatePartner(ctx, store.PartnerInput{Slug: "resell-co", Name: "Resell Co", TierID: &gold.ID, BillTo: store.BillToPartner, ContactEmail: "ap@resell.example"})
	if err != nil {
		t.Fatal(err)
	}
	if resellCo.PartyCustomerID == "" {
		t.Fatal("a partner must be created with its own party account")
	}
	agentCo, err := st.CreatePartner(ctx, store.PartnerInput{Slug: "agent-co", Name: "Agent Co", TierID: &gold.ID, BillTo: store.BillToCustomer, ContactEmail: "ap@agent.example"})
	if err != nil {
		t.Fatal(err)
	}

	// One end customer each, 10 % off its own bill.
	mk := func(slug, name, email, partnerID string) store.Customer {
		t.Helper()
		c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: name, AdminEmail: email, StartDate: "2026-08-01"})
		if err != nil {
			t.Fatal(err)
		}
		pid := partnerID
		if c, err = st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PartnerID: &pid}); err != nil {
			t.Fatal(err)
		}
		if c.PartnerID == nil || *c.PartnerID != partnerID {
			t.Fatalf("%s was not assigned to its partner: %+v", slug, c.PartnerID)
		}
		src := mkSource(t, st, c.ID, "proj-"+slug)
		assignBook(t, st, src.ID, book.ID)
		seedPartnerUsage(t, st, c.ID, src.ID)
		if _, err := st.CreateDiscount(ctx, store.DiscountInput{CustomerID: &c.ID, Name: name + " 10 %", Kind: "percent", Value: "10"}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	alpha := mk("alpha", "Alpha", "a@alpha.example", resellCo.ID)
	beta := mk("beta", "Beta", "b@beta.example", agentCo.ID)

	// A partner's party is an ACCOUNT, not a customer: it is never in the
	// directory and never rated as one.
	customers, err := st.ListCustomers(ctx, store.OperatorScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(customers) != 2 {
		t.Fatalf("the directory holds %d rows, want the two end customers only (a party is not a customer)", len(customers))
	}

	// ONE run produces the customer statements AND the partner statements.
	results, err := rating.Run(ctx, st, "2026-08", "")
	if err != nil {
		t.Fatal(err)
	}

	// --- the end customer of the RESELL partner ------------------------
	stA, err := st.GetStatement(ctx, store.OperatorScope, resultFor(results, alpha.ID).StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if stA.Subtotal != "90.000000" || stA.DiscountTotal != "10.000000" {
		t.Fatalf("alpha: list 100 − 10 %% = net 90; got subtotal %s discount %s", stA.Subtotal, stA.DiscountTotal)
	}
	if stA.BuyTotal == nil || *stA.BuyTotal != "70.000000" {
		t.Fatalf("alpha: list 100 − tier 30 %% = buy 70; got %v", stA.BuyTotal)
	}
	if stA.MarginTotal == nil || *stA.MarginTotal != "20.000000" {
		t.Fatalf("alpha: margin = net 90 − buy 70 = 20; got %v", stA.MarginTotal)
	}
	if stA.PartnerID == nil || *stA.PartnerID != resellCo.ID || stA.Kind != store.StatementKindCustomer {
		t.Fatalf("alpha statement partner keys = %v %q", stA.PartnerID, stA.Kind)
	}
	if len(stA.Lines) != 1 {
		t.Fatalf("alpha lines = %d", len(stA.Lines))
	}
	l := stA.Lines[0]
	if l.ListAmount == nil || *l.ListAmount != "100.000000" || l.NetAmount == nil || *l.NetAmount != "90.000000" || l.BuyAmount == nil || *l.BuyAmount != "70.000000" {
		t.Fatalf("alpha line waterfall: list %v net %v buy %v", l.ListAmount, l.NetAmount, l.BuyAmount)
	}
	// Tax is charged on what the customer pays, as everywhere else.
	if stA.Tax != "4.500000" || stA.Total != "94.500000" {
		t.Fatalf("alpha tax/total = %s / %s, want 4.5 / 94.5", stA.Tax, stA.Total)
	}

	// --- the RESELL partner's own wholesale statement -------------------
	whole, err := st.ListPartnerStatements(ctx, resellCo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) != 1 {
		t.Fatalf("resell partner statements = %d, want one wholesale statement", len(whole))
	}
	w, err := st.GetStatement(ctx, store.OperatorScope, whole[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != store.StatementKindWholesale || w.CustomerID != resellCo.PartyCustomerID {
		t.Fatalf("wholesale statement kind %q on customer %s, want %s on the party %s", w.Kind, w.CustomerID, store.StatementKindWholesale, resellCo.PartyCustomerID)
	}
	// Every partner customer's lines at LIST, less the tier: 100 − 30 = 70.
	if w.Subtotal != "70.000000" || w.DiscountTotal != "30.000000" {
		t.Fatalf("wholesale: list 100 − tier 30 = 70; got subtotal %s discount %s", w.Subtotal, w.DiscountTotal)
	}
	if w.BuyTotal == nil || *w.BuyTotal != "70.000000" || w.MarginTotal == nil || *w.MarginTotal != "20.000000" {
		t.Fatalf("wholesale buy/margin = %v / %v, want 70 / 20", w.BuyTotal, w.MarginTotal)
	}
	if len(w.Lines) != 1 || w.Lines[0].EndCustomerID == nil || *w.Lines[0].EndCustomerID != alpha.ID {
		t.Fatalf("the wholesale lines are not grouped by end customer: %+v", w.Lines)
	}
	if w.Lines[0].Amount != "100.000000" {
		t.Fatalf("a wholesale line is the LIST amount before the tier; got %s", w.Lines[0].Amount)
	}

	// --- the AGENT partner ----------------------------------------------
	stB, err := st.GetStatement(ctx, store.OperatorScope, resultFor(results, beta.ID).StatementID)
	if err != nil {
		t.Fatal(err)
	}
	// The end customer is rated at OUR books under the agent model.
	if stB.Subtotal != "90.000000" || stB.Lines[0].Amount != "100.000000" {
		t.Fatalf("beta: an agent's customer is billed at our list; got subtotal %s line %s", stB.Subtotal, stB.Lines[0].Amount)
	}
	if stB.BuyTotal == nil || *stB.BuyTotal != "70.000000" || stB.MarginTotal == nil || *stB.MarginTotal != "20.000000" {
		t.Fatalf("beta buy/margin = %v / %v, want 70 / 20", stB.BuyTotal, stB.MarginTotal)
	}
	comm, err := st.ListPartnerStatements(ctx, agentCo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comm) != 1 {
		t.Fatalf("agent partner statements = %d, want one commission statement", len(comm))
	}
	cs, err := st.GetStatement(ctx, store.OperatorScope, comm[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Kind != store.StatementKindCommission || cs.CustomerID != agentCo.PartyCustomerID {
		t.Fatalf("commission statement kind %q on %s", cs.Kind, cs.CustomerID)
	}
	// commission = customer net 90 − partner buy 70 = 20, one line per end customer.
	if cs.Subtotal != "20.000000" || cs.Total != "20.000000" {
		t.Fatalf("commission = net 90 − buy 70 = 20; got subtotal %s total %s", cs.Subtotal, cs.Total)
	}
	if len(cs.Lines) != 1 || cs.Lines[0].SKU != rating.CommissionSKU || cs.Lines[0].Amount != "20.000000" {
		t.Fatalf("commission lines = %+v", cs.Lines)
	}
	if cs.Lines[0].EndCustomerID == nil || *cs.Lines[0].EndCustomerID != beta.ID {
		t.Fatalf("a commission line names the end customer it was earned on; got %v", cs.Lines[0].EndCustomerID)
	}

	// Issuing the commission statement CREDITS the partner's account: money
	// we owe it, on the one ledger every party shares.
	if _, err := st.IssueStatement(ctx, cs.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListAccountEntries(ctx, store.OperatorScope, agentCo.PartyCustomerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != store.EntryCommission || string(entries[0].Amount) != "-20.000000" {
		t.Fatalf("partner ledger = %+v, want one commission credit of -20", entries)
	}
	if entries[0].StatementID != cs.ID {
		t.Fatalf("the commission entry does not reference its statement: %+v", entries[0])
	}
	bal, err := st.GetAccountBalance(ctx, store.OperatorScope, agentCo.PartyCustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if string(bal.Balance) != "-20.000000" {
		t.Fatalf("partner balance = %s, want -20 (in credit)", bal.Balance)
	}

	// --- the margin report ----------------------------------------------
	rep, err := st.MarginReport(ctx, resellCo.ID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("margin rows = %+v", rep.Rows)
	}
	row := rep.Rows[0]
	if row.CustomerID != alpha.ID || row.Service != "ecs" || string(row.Net) != "90.000000" || string(row.Buy) != "70.000000" || string(row.Margin) != "20.000000" {
		t.Fatalf("margin row = %+v, want alpha/ecs 90/70/20", row)
	}
	if row.MarginPct == nil || *row.MarginPct < 22.2 || *row.MarginPct > 22.3 {
		t.Fatalf("margin %% = %v, want 20/90 ≈ 22.22", row.MarginPct)
	}
	if string(rep.Totals.Margin) != "20.000000" || rep.Currency != "OMR" {
		t.Fatalf("margin totals = %+v", rep.Totals)
	}
	// A partner never sees another partner's margin: the report is per
	// partner, and the agent's report carries only its own customer.
	other, err := st.MarginReport(ctx, agentCo.ID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Rows) != 1 || other.Rows[0].CustomerID != beta.ID {
		t.Fatalf("the agent's margin report leaked another partner's customers: %+v", other.Rows)
	}
}

// A resell partner's DERIVED retail book: materialised from the list book by
// the partner's rule, read-only, re-derived on a list change — and the end
// customer is then shown the retail price while the buy price is unmoved.
func TestIntegrationPartnerDerivedRetailBookPricesTheEndCustomer(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC list 2026", Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: partnerSKU, Unit: "instance-hour", UnitPrice: "100"}}, true); err != nil {
		t.Fatal(err)
	}
	gold, err := st.CreatePartnerTier(ctx, "Gold", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceTierDiscounts(ctx, gold.ID, []store.DiscountInput{{Name: "Gold 30 %", Kind: "percent", Value: "30"}}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePartner(ctx, store.PartnerInput{Slug: "resell-co", Name: "Resell Co", TierID: &gold.ID, BillTo: store.BillToPartner})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "alpha", Name: "Alpha", AdminEmail: "a@alpha.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PartnerID: &p.ID}); err != nil {
		t.Fatal(err)
	}
	src := mkSource(t, st, c.ID, "proj-alpha")
	assignBook(t, st, src.ID, book.ID)
	seedPartnerUsage(t, st, c.ID, src.ID)

	// Without a rule there is no retail book: the end customer sees list.
	books, err := rating.DeriveRetailBooks(ctx, st, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("a partner with no retail rule has no derived book; got %d", len(books))
	}

	// base = buy (70), markup 5 % → 73.5.
	if _, err := st.PutRetailRule(ctx, p.ID, store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "5"}); err != nil {
		t.Fatal(err)
	}
	books, err = rating.DeriveRetailBooks(ctx, st, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || len(books[0].Book.Items) != 1 {
		t.Fatalf("derived books = %+v", books)
	}
	derived := books[0].Book
	if !derived.DerivedFromRule || derived.PartnerID == nil || *derived.PartnerID != p.ID || derived.DerivedFromBookID == nil || *derived.DerivedFromBookID != book.ID {
		t.Fatalf("the derived book does not say what it derives from: %+v", derived)
	}
	if string(derived.Items[0].UnitPrice) != "73.50000000" {
		t.Fatalf("retail = %s, want buy 70 + 5 %% = 73.50000000", derived.Items[0].UnitPrice)
	}
	if len(books[0].BelowBuy) != 0 {
		t.Fatalf("73.5 is above the buy price: %+v", books[0].BelowBuy)
	}
	if derived.Scope != book.Scope || derived.Currency != book.Currency {
		t.Fatalf("a derived book follows its list book's scope and currency: %+v", derived)
	}

	// Deriving twice is idempotent — one book per list book, not two.
	if books, err = rating.DeriveRetailBooks(ctx, st, p.ID); err != nil || len(books) != 1 || books[0].Book.ID != derived.ID {
		t.Fatalf("re-derivation made a second book: %+v %v", books, err)
	}

	// The end customer is now rated at the RETAIL price, and the partner's
	// buy price is unmoved: 73.5 net (no customer discount) against buy 70.
	results, err := rating.Run(ctx, st, "2026-08", "")
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, resultFor(results, c.ID).StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if stmt.Subtotal != "73.500000" {
		t.Fatalf("the end customer is billed at the retail book: subtotal %s, want 73.5", stmt.Subtotal)
	}
	if stmt.BuyTotal == nil || *stmt.BuyTotal != "70.000000" || stmt.MarginTotal == nil || *stmt.MarginTotal != "3.500000" {
		t.Fatalf("buy/margin = %v / %v, want 70 / 3.5", stmt.BuyTotal, stmt.MarginTotal)
	}
	if stmt.Lines[0].ListUnitPrice == nil || *stmt.Lines[0].ListUnitPrice != "100.00000000" {
		t.Fatalf("the line keeps the LIST unit price beside the retail one: %v", stmt.Lines[0].ListUnitPrice)
	}

	// A list-price change re-derives the retail book: 200 − 30 % = 140, + 5 % = 147.
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: partnerSKU, Unit: "instance-hour", UnitPrice: "200"}}, true); err != nil {
		t.Fatal(err)
	}
	books, err = rating.DeriveRetailBooks(ctx, st, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(books[0].Book.Items[0].UnitPrice) != "147.00000000" {
		t.Fatalf("after the list change the retail price is %s, want 147.00000000", books[0].Book.Items[0].UnitPrice)
	}

	// An AGENT partner has no retail book at all, and switching the model
	// removes the one it had.
	if _, err := st.UpdatePartner(ctx, p.ID, store.PartnerPatch{BillTo: strPtrT(store.BillToCustomer)}); err != nil {
		t.Fatal(err)
	}
	if books, err = rating.DeriveRetailBooks(ctx, st, p.ID); err != nil || len(books) != 0 {
		t.Fatalf("an agent partner keeps no retail book: %+v %v", books, err)
	}
	left, err := st.DerivedBooks(ctx, p.ID)
	if err != nil || len(left) != 0 {
		t.Fatalf("the derived book survived the switch to the agent model: %+v %v", left, err)
	}
}

func strPtrT(s string) *string { return &s }

// The partner scope: a binding at partner:<id> expands to the partner's
// customers PLUS its own party, and to nothing else.
func TestIntegrationPartnerScopeExpansion(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	one, err := st.CreatePartner(ctx, store.PartnerInput{Slug: "one", Name: "One", ContactEmail: "ap@one.example"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.CreatePartner(ctx, store.PartnerInput{Slug: "two", Name: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "mine", Name: "Mine", AdminEmail: "m@mine.example"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "theirs", Name: "Theirs", AdminEmail: "t@theirs.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateCustomer(ctx, mine.ID, store.CustomerPatch{PartnerID: &one.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateCustomer(ctx, theirs.ID, store.CustomerPatch{PartnerID: &two.ID}); err != nil {
		t.Fatal(err)
	}

	ids, err := st.ExpandPartnerScope(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != one.PartyCustomerID || ids[1] != mine.ID {
		t.Fatalf("partner scope = %v, want [party, mine]", ids)
	}
	scope := store.CustomersScope(ids)
	if !scope.Allows(mine.ID) || !scope.Allows(one.PartyCustomerID) {
		t.Fatal("a partner scope must reach its own customer and its party")
	}
	if scope.Allows(theirs.ID) || scope.Allows(two.PartyCustomerID) {
		t.Fatal("a partner scope reached another partner's rows")
	}
	if scope.Operator {
		t.Fatal("a partner scope is not the operator scope")
	}
	// The customer directory a partner principal reads is exactly its own.
	list, err := st.ListCustomers(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("a partner's customer directory = %+v, want only Mine (a party is not a customer)", list)
	}

	// The contact email of a partner holds partner-owner on it, and nothing
	// on the other partner.
	users, err := st.PartnerUsers(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].SubjectEmail != "ap@one.example" || users[0].Role != store.RolePartnerOwner || users[0].PartnerID == nil || *users[0].PartnerID != one.ID {
		t.Fatalf("partner users = %+v", users)
	}
	if u, _ := st.PartnerUsers(ctx, two.ID); len(u) != 0 {
		t.Fatalf("the other partner has users it was never granted: %+v", u)
	}

	// A customer's party row can never itself be assigned to a partner.
	if _, err := st.UpdateCustomer(ctx, one.PartyCustomerID, store.CustomerPatch{PartnerID: &two.ID}); err == nil {
		t.Fatal("a partner's own account was assigned to another partner")
	}
}

// A tier discount is a discount of the ONE engine: it never reaches a
// customer's list, and never reduces a customer's bill.
func TestIntegrationTierDiscountsNeverReachACustomersBill(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	tier, err := st.CreatePartnerTier(ctx, "Gold", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceTierDiscounts(ctx, tier.ID, []store.DiscountInput{{Name: "Gold 30 %", Kind: "percent", Value: "30"}}); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDiscount(ctx, store.DiscountInput{Name: "Launch 10 %", Kind: "percent", Value: "10"}); err != nil {
		t.Fatal(err)
	}

	// The customer's own list carries its campaign and NOT the tier.
	mine, err := st.ListDiscounts(ctx, store.OperatorScope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Name != "Launch 10 %" {
		t.Fatalf("the customer's discounts = %+v, want the campaign only", mine)
	}
	if active, _ := st.ActiveDiscountsAt(ctx, c.ID, time.Now().UTC()); len(active) != 1 {
		t.Fatalf("a tier discount reached a customer's rating: %+v", active)
	}
	all, err := st.ListAllDiscounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("the operator discount list carries a tier discount: %+v", all)
	}
	// It IS on its tier, with the tier named.
	td, err := st.TierDiscounts(ctx, tier.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(td) != 1 || td[0].TierID == nil || *td[0].TierID != tier.ID || td[0].TierName != "Gold" {
		t.Fatalf("tier discounts = %+v", td)
	}
	// A tier discount is a percent off list: a fixed amount has no meaning.
	if _, err := st.ReplaceTierDiscounts(ctx, tier.ID, []store.DiscountInput{{Name: "flat", Kind: "fixed", Value: "5"}}); err == nil {
		t.Fatal("a fixed tier discount was accepted")
	}
	if _, err := st.ReplaceTierDiscounts(ctx, tier.ID, []store.DiscountInput{{Name: "too much", Kind: "percent", Value: "120"}}); err == nil {
		t.Fatal("a 120 % tier discount was accepted")
	}
}
