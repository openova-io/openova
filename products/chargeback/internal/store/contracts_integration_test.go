package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Contracts and commercial terms against a real database (DESIGN.md §15):
// the lifecycle, the shapes reaching a rated statement, the true-up line, the
// renewals-due list, the renewal step and the SLA credit note.

const (
	tieredSKU = "object_gib"
	commitSKU = "ecs.m7n.2xlarge.8"
)

func mustCustomer(t *testing.T, st *store.Store, slug, name string) store.Customer {
	t.Helper()
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{Slug: slug, Name: name, AdminEmail: slug + "@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seedTermsBook is a book with the three shapes on it: a tiered SKU, an
// allowance-bearing SKU and a flat one for the commitment.
func seedTermsBook(t *testing.T, st *store.Store) store.PriceBook {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "Terms 2026", Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	upTo := func(s string) *store.Decimal { d := store.Decimal(s); return &d }
	allowance := store.Decimal("50")
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: tieredSKU, Unit: "gb-month", UnitPrice: "0.010", TierMode: store.TierModeGraduated, Tiers: []store.PriceTier{
			{UpTo: upTo("10240"), Price: "0.010"}, {UpTo: upTo("102400"), Price: "0.008"}, {Price: "0.006"},
		}},
		{SKU: "eip.traffic_gb", Unit: "gb", UnitPrice: "0.05", Allowance: &allowance},
		{SKU: commitSKU, Unit: "instance-hour", UnitPrice: "0.50"},
	}, true); err != nil {
		t.Fatal(err)
	}
	return book
}

// TestIntegrationPriceItemShapesRoundTrip — the shapes SURVIVE the database.
// A tier ladder or an allowance that reads back empty would rate every
// statement at the flat price and nobody would see it.
func TestIntegrationPriceItemShapesRoundTrip(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	book := seedTermsBook(t, st)

	got, err := st.GetPriceBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.PriceItem{}
	for _, it := range got.Items {
		by[it.SKU] = it
	}
	tiered := by[tieredSKU]
	if tiered.TierMode != store.TierModeGraduated || len(tiered.Tiers) != 3 {
		t.Fatalf("tiered item came back as %+v", tiered)
	}
	if tiered.Tiers[0].UpTo == nil || string(*tiered.Tiers[0].UpTo) != "10240" || string(tiered.Tiers[2].Price) != "0.006" {
		t.Fatalf("tier ladder = %+v", tiered.Tiers)
	}
	if tiered.Tiers[2].UpTo != nil {
		t.Fatalf("the last band must be unbounded, got up_to = %s", *tiered.Tiers[2].UpTo)
	}
	if a := by["eip.traffic_gb"].Allowance; a == nil || string(*a) != "50.000000" {
		t.Fatalf("allowance came back as %v", a)
	}

	// A PATCH that names neither keeps both: an operator editing a
	// description must not silently drop a pricing ladder.
	desc := "object storage"
	if _, err := st.UpdatePriceItem(ctx, book.ID, tieredSKU, store.PriceItemPatch{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetPriceItem(ctx, book.ID, tieredSKU)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Tiers) != 3 || again.TierMode != store.TierModeGraduated {
		t.Fatalf("a description edit dropped the ladder: %+v", again)
	}

	// An empty band list clears the mode with the bands.
	none := []store.PriceTier{}
	cleared, err := st.UpdatePriceItem(ctx, book.ID, tieredSKU, store.PriceItemPatch{Tiers: &none})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Tiers) != 0 || cleared.TierMode != store.TierModeNone {
		t.Fatalf("clearing the bands left %+v", cleared)
	}
}

// TestIntegrationContractLifecycle — create, patch, items, scope and delete.
func TestIntegrationContractLifecycle(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	acme := mustCustomer(t, st, "acme", "ACME LLC")
	globex := mustCustomer(t, st, "globex", "Globex")

	minimum := store.Decimal("1000")
	c, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: acme.ID, Name: strp("ACME 2026"), StartsOn: strp("2026-01-01"), TermMonths: intp(12),
		MinimumCommitment: &minimum, Currency: strp("OMR"), Status: strp(store.ContractActive), AutoRenew: boolp(true),
		RenewalNoticeDays: intp(60), PORef: strp("PO-4411"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A 12-month term from 1 January ends on 31 December, never 1 January
	// of the next year: a term must not overlap its own renewal.
	if c.EndsOn != "2026-12-31" {
		t.Fatalf("ends_on = %s, want 2026-12-31", c.EndsOn)
	}
	if c.RenewalDate != "2027-01-01" || c.NoticeFrom != "2026-11-01" {
		t.Fatalf("renewal %s / notice from %s, want 2027-01-01 / 2026-11-01", c.RenewalDate, c.NoticeFrom)
	}

	// The lines: a committed-use line and a contract allowance.
	items, err := st.PutContractItems(ctx, c.ID, []store.ContractItem{
		{Kind: store.ContractItemCommitment, SKU: commitSKU, Unit: "instance-hour", Quantity: "7440", DiscountPct: decp("30")},
		{Kind: store.ContractItemAllowance, SKU: "eip.traffic_gb", Unit: "gb", Quantity: "100", Rollover: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	// A commitment at LIST is refused: it is not a commitment.
	if _, err := st.PutContractItems(ctx, c.ID, []store.ContractItem{{Kind: store.ContractItemCommitment, SKU: commitSKU, Quantity: "10"}}); err == nil {
		t.Fatal("a committed-use line with neither a price nor a percentage was accepted")
	}
	// …and the refusal did not half-apply: the two lines are still there.
	if again, err := st.GetContract(ctx, store.OperatorScope, c.ID); err != nil || len(again.Items) != 2 {
		t.Fatalf("the refused replacement damaged the list: %d items (err=%v)", len(again.Items), err)
	}

	// A patch that moves the term re-derives the end date.
	moved, err := st.UpdateContract(ctx, c.ID, store.ContractInput{TermMonths: intp(24)})
	if err != nil {
		t.Fatal(err)
	}
	if moved.EndsOn != "2027-12-31" {
		t.Fatalf("a 24-month term from 2026-01-01 ends %s, want 2027-12-31", moved.EndsOn)
	}
	// Clearing the minimum is a real edit, not an omission.
	cleared, err := st.UpdateContract(ctx, c.ID, store.ContractInput{ClearMinimum: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MinimumCommitment != nil {
		t.Fatalf("minimum = %v, want cleared", cleared.MinimumCommitment)
	}

	// SCOPE: another customer's principal cannot read it, and the contract
	// does not appear in its list.
	if _, err := st.GetContract(ctx, store.CustomerScope(globex.ID), c.ID); err != store.ErrNotFound {
		t.Fatalf("a foreign customer scope read the contract: %v", err)
	}
	mine, err := st.ListContracts(ctx, store.CustomerScope(acme.ID), store.ContractFilter{})
	if err != nil || len(mine) != 1 {
		t.Fatalf("acme's own list = %d (err=%v)", len(mine), err)
	}
	theirs, err := st.ListContracts(ctx, store.CustomerScope(globex.ID), store.ContractFilter{})
	if err != nil || len(theirs) != 0 {
		t.Fatalf("globex saw %d of acme's contracts (err=%v)", len(theirs), err)
	}

	if err := st.DeleteContract(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteContract(ctx, c.ID); err != store.ErrNotFound {
		t.Fatalf("deleting twice = %v, want not found", err)
	}
}

// TestIntegrationRatedStatementCarriesTheShapesAndTheTrueUp — the whole
// chain, on a real run: the shapes reach the rated lines, the true-up is a
// LINE, the subtotal is the minimum and the statement names its contract.
func TestIntegrationRatedStatementCarriesTheShapesAndTheTrueUp(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	book := seedTermsBook(t, st)
	acme := mustCustomer(t, st, "acme", "ACME LLC")
	src, _, err := st.UpsertSource(ctx, acme.ID, "huawei-project", "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	usage := []store.UsageRecord{
		// 51,200 GiB of object storage: graduated = 430.08.
		{CustomerID: acme.ID, SourceID: src.ID, ResourceID: "bucket-1", ResourceKind: "obs", SKU: tieredSKU,
			Quantity: "51200", Unit: "gb-month", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215"},
		// 150 GB of traffic against a 50 GB plan allowance: 100 × 0.05 = 5.00.
		{CustomerID: acme.ID, SourceID: src.ID, ResourceID: "eip-1", ResourceKind: "eip", SKU: "eip.traffic_gb",
			Quantity: "150", Unit: "gb", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215"},
		// 10,000 instance-hours against a 7,440-hour commitment at 30 % off:
		// 7,440 × 0.35 + 2,560 × 0.50 = 3,884.00 (5,000.00 at list).
		{CustomerID: acme.ID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: commitSKU,
			Quantity: "10000", Unit: "instance-hour", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215"},
	}
	if _, err := st.UpsertUsage(ctx, usage); err != nil {
		t.Fatal(err)
	}

	// Without a contract: the plan's allowance and tiers still apply — they
	// belong to the book — but the commitment and the minimum do not.
	results, err := rating.Run(ctx, st, "2026-08", acme.ID)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	// 430.08 + 5.00 + 5,000.00 = 5,435.08.
	if string(stmt.Subtotal) != "5435.080000" {
		t.Fatalf("without a contract the subtotal is %s, want 5435.080000", stmt.Subtotal)
	}
	if stmt.ContractID != nil {
		t.Fatalf("a statement rated without a contract names one: %v", stmt.ContractID)
	}

	// Now the contract: the commitment and a minimum far above the period.
	minimum := store.Decimal("6000")
	c, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: acme.ID, Name: strp("ACME 2026"), StartsOn: strp("2026-01-01"), TermMonths: intp(12),
		MinimumCommitment: &minimum, Currency: strp("OMR"), Status: strp(store.ContractActive),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutContractItems(ctx, c.ID, []store.ContractItem{
		{Kind: store.ContractItemCommitment, SKU: commitSKU, Unit: "instance-hour", Quantity: "7440", DiscountPct: decp("30")},
	}); err != nil {
		t.Fatal(err)
	}

	results, err = rating.Run(ctx, st, "2026-08", acme.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := results[0]
	if res.ContractID != c.ID || res.ContractName != "ACME 2026" {
		t.Fatalf("the run does not name the contract: %+v", res)
	}
	stmt, err = st.GetStatement(ctx, store.OperatorScope, res.StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if stmt.ContractID == nil || *stmt.ContractID != c.ID || stmt.ContractName != "ACME 2026" {
		t.Fatalf("the statement does not name the contract: %v / %q", stmt.ContractID, stmt.ContractName)
	}
	byLine := map[string]store.RatedLine{}
	for _, l := range stmt.Lines {
		byLine[l.SKU] = l
	}
	if got := string(byLine[tieredSKU].Amount); got != "430.080000" {
		t.Fatalf("the tiered line = %s, want 430.080000", got)
	}
	if got := string(byLine["eip.traffic_gb"].Amount); got != "5.000000" {
		t.Fatalf("the allowance line = %s, want 5.000000", got)
	}
	if got := string(byLine[commitSKU].Amount); got != "3884.000000" {
		t.Fatalf("the committed line = %s, want 3884.000000", got)
	}
	// 430.08 + 5.00 + 3,884.00 = 4,319.08 — a true-up of 1,680.92 brings the
	// period to the 6,000 minimum, and the tax follows the minimum.
	trueUp, ok := byLine[rating.TrueUpSKU]
	if !ok {
		t.Fatalf("no %s line on a period below the minimum: %+v", rating.TrueUpSKU, stmt.Lines)
	}
	if string(trueUp.Amount) != "1680.920000" || trueUp.Unit != rating.TrueUpUnit {
		t.Fatalf("true-up = %s %s, want 1680.920000 per %s", trueUp.Amount, trueUp.Unit, rating.TrueUpUnit)
	}
	if string(stmt.Subtotal) != "6000.000000" || string(stmt.Tax) != "300.000000" || string(stmt.Total) != "6300.000000" {
		t.Fatalf("subtotal/tax/total = %s / %s / %s, want 6000 / 300 / 6300", stmt.Subtotal, stmt.Tax, stmt.Total)
	}
	if res.TrueUp != "1680.920000" {
		t.Fatalf("the run reports true_up = %q", res.TrueUp)
	}
	// The run explains what each shape did, per SKU.
	shapes := map[string]rating.Breakdown{}
	for _, b := range res.AppliedTerms {
		shapes[b.SKU] = b
	}
	if got := shapes["eip.traffic_gb"]; string(got.AllowanceUsed) != "50.000000" {
		t.Fatalf("the allowance breakdown = %+v", got)
	}
	if got := shapes[commitSKU]; string(got.Committed) != "7440.000000" || string(got.Excess) != "2560.000000" {
		t.Fatalf("the commitment breakdown = %+v", got)
	}

	// A minimum the period MEETS raises nothing at all.
	low := store.Decimal("1000")
	if _, err := st.UpdateContract(ctx, c.ID, store.ContractInput{MinimumCommitment: &low}); err != nil {
		t.Fatal(err)
	}
	results, err = rating.Run(ctx, st, "2026-08", acme.ID)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err = st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range stmt.Lines {
		if l.SKU == rating.TrueUpSKU {
			t.Fatalf("a period of 4,319.08 against a 1,000 minimum raised a true-up of %s", l.Amount)
		}
	}
	if string(stmt.Subtotal) != "4319.080000" {
		t.Fatalf("subtotal = %s, want 4319.080000", stmt.Subtotal)
	}
}

// TestIntegrationRenewalsDueAndTheRenewalStep — the notice window, the
// auto-renewal and the expiry.
func TestIntegrationRenewalsDueAndTheRenewalStep(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	acme := mustCustomer(t, st, "acme", "ACME LLC")
	globex := mustCustomer(t, st, "globex", "Globex")

	renewing, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: acme.ID, Name: strp("ACME renewing"), StartsOn: strp("2026-01-01"), TermMonths: intp(12),
		Currency: strp("OMR"), Status: strp(store.ContractActive), AutoRenew: boolp(true), RenewalNoticeDays: intp(30),
	})
	if err != nil {
		t.Fatal(err)
	}
	ending, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: globex.ID, Name: strp("Globex ending"), StartsOn: strp("2026-01-01"), TermMonths: intp(12),
		Currency: strp("OMR"), Status: strp(store.ContractActive), AutoRenew: boolp(false), RenewalNoticeDays: intp(30),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 60 days out: neither is due. Both end 2026-12-31 with 30 days' notice,
	// so the window opens on 2026-12-01.
	due, err := st.ListContracts(ctx, store.OperatorScope, store.ContractFilter{RenewalsDueOn: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("%d contracts due 60 days before the window", len(due))
	}
	// The first day of the window: both.
	if due, err = st.ListContracts(ctx, store.OperatorScope, store.ContractFilter{RenewalsDueOn: "2026-12-01"}); err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("%d contracts due on the first day of the notice window, want 2", len(due))
	}
	// The end date itself is still inside the window.
	if due, err = st.ListContracts(ctx, store.OperatorScope, store.ContractFilter{RenewalsDueOn: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("%d contracts due on the end date, want 2", len(due))
	}
	// A customer principal sees only its own.
	if due, err = st.ListContracts(ctx, store.CustomerScope(acme.ID), store.ContractFilter{RenewalsDueOn: "2026-12-01"}); err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != renewing.ID {
		t.Fatalf("acme's renewals-due list = %+v", due)
	}

	// The day after the term ends: one renews, the other expires.
	rep, err := st.RunContractRenewals(ctx, time.Date(2027, 1, 2, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Renewed) != 1 || rep.Renewed[0] != renewing.ID {
		t.Fatalf("renewed = %v, want [%s]", rep.Renewed, renewing.ID)
	}
	if len(rep.Expired) != 1 || rep.Expired[0] != ending.ID {
		t.Fatalf("expired = %v, want [%s]", rep.Expired, ending.ID)
	}
	got, err := st.GetContract(ctx, store.OperatorScope, renewing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartsOn != "2027-01-01" || got.EndsOn != "2027-12-31" {
		t.Fatalf("the renewed term is %s → %s, want 2027-01-01 → 2027-12-31", got.StartsOn, got.EndsOn)
	}
	if got.Status != store.ContractActive || got.RenewalCount != 1 || got.RenewedAt == nil {
		t.Fatalf("the renewed contract = %+v", got)
	}
	if expired, err := st.GetContract(ctx, store.OperatorScope, ending.ID); err != nil || expired.Status != store.ContractExpired {
		t.Fatalf("the ended contract is %q (err=%v), want expired", expired.Status, err)
	}
	// A SECOND pass on the same day changes nothing: the renewal is
	// idempotent, so a restart cannot advance a term twice.
	if rep, err = st.RunContractRenewals(ctx, time.Date(2027, 1, 2, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(rep.Renewed) != 0 || len(rep.Expired) != 0 {
		t.Fatalf("a second pass acted again: %+v", rep)
	}
}

// TestIntegrationSLACreditIsARealCreditNote — an availability breach credited
// through the machinery that already exists: numbered, allocated to the
// invoice, posted to the ledger, and recording what it answers.
func TestIntegrationSLACreditIsARealCreditNote(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	book := seedTermsBook(t, st)
	acme := mustCustomer(t, st, "acme", "ACME LLC")
	src, _, err := st.UpsertSource(ctx, acme.ID, "huawei-project", "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{
		CustomerID: acme.ID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: commitSKU,
		Quantity: "2000", Unit: "instance-hour", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215",
	}}); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: acme.ID, Name: strp("ACME 2026"), StartsOn: strp("2026-01-01"), TermMonths: intp(12),
		Currency: strp("OMR"), Status: strp(store.ContractActive),
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := rating.Run(ctx, st, "2026-08", acme.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 2,000 × 0.50 = 1,000.00 plus 5 % tax = 1,050.00.
	stmt, err := st.IssueStatement(ctx, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stmt.Total) != "1050.000000" {
		t.Fatalf("invoice total = %s, want 1050.000000", stmt.Total)
	}

	// A 10 % SLA credit on 99.2 % measured availability.
	note, err := st.IssueSLACredit(ctx, c.ID, store.SLACreditInput{
		StatementID: stmt.ID, Pct: "10", Availability: "99.2", Actor: "ops@nc.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(note.Total) != "105.000000" {
		t.Fatalf("SLA credit = %s, want 105.000000 (10 %% of 1,050)", note.Total)
	}
	if note.Number == "" {
		t.Fatal("the SLA credit was not numbered: it is not on the credit-note sequence")
	}
	if note.Reason == "" || !strings.Contains(note.Reason, "99.2") || !strings.Contains(note.Reason, "ACME 2026") {
		t.Fatalf("the reason does not say what it answers: %q", note.Reason)
	}
	// It reduced the invoice, through the one allocation path.
	after, err := st.GetStatement(ctx, store.OperatorScope, stmt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Credited) != "105.000000" {
		t.Fatalf("credited on the invoice = %s, want 105.000000", after.Credited)
	}
	// And it is on the ledger like every other credit note.
	entries, err := st.ListAccountEntries(ctx, store.OperatorScope, acme.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.CreditNoteID == note.ID && e.Kind == store.EntryCreditNote {
			found = true
		}
	}
	if !found {
		t.Fatalf("no credit_note entry on the ledger for %s: %+v", note.Number, entries)
	}

	// The refusals: a percentage outside 0–100, and a statement of another
	// customer than the contract's.
	if _, err := st.IssueSLACredit(ctx, c.ID, store.SLACreditInput{StatementID: stmt.ID, Pct: "0"}); err == nil {
		t.Fatal("a 0 % SLA credit was issued")
	}
	if _, err := st.IssueSLACredit(ctx, c.ID, store.SLACreditInput{StatementID: stmt.ID, Pct: "150"}); err == nil {
		t.Fatal("a 150 % SLA credit was issued")
	}
	globex := mustCustomer(t, st, "globex", "Globex")
	other, err := st.CreateContract(ctx, store.ContractInput{
		CustomerID: globex.ID, Name: strp("Globex 2026"), StartsOn: strp("2026-01-01"), Currency: strp("OMR"), Status: strp(store.ContractActive),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueSLACredit(ctx, other.ID, store.SLACreditInput{StatementID: stmt.ID, Pct: "10"}); err == nil {
		t.Fatal("a contract credited another customer's invoice")
	}
}

func boolp(b bool) *bool           { return &b }
func decp(s string) *store.Decimal { d := store.Decimal(s); return &d }
