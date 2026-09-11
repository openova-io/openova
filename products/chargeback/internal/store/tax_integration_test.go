package store_test

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Tax rules by country and category, end to end through the database
// (DESIGN.md §17). The unit tests in internal/rating prove the arithmetic;
// what is proved HERE is that the rules a console writes are the rules a
// rating run reads, that the summary is written to the statement, and that
// the invoice FREEZES it with the rule ids at issue.

func taxSettings(t *testing.T, st *store.Store, country string) {
	t.Helper()
	ctx := context.Background()
	cur, err := st.GetBillingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := cur
	next.TaxCountry = country
	next.LegalName = "Sovereign Cloud Operator LLC"
	next.TaxRegistrationNumber = "OM1100012345"
	if _, err := st.UpdateBillingSettings(ctx, next); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.UpdateBillingSettings(context.Background(), store.DefaultBillingSettings()); err != nil {
			t.Errorf("reset billing settings: %v", err)
		}
	})
}

// seedTaxRun stands up a customer with one source, a book pricing a compute
// SKU and a storage SKU, and a month of usage of both.
func seedTaxRun(t *testing.T, st *store.Store, slug string) store.Customer {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list-" + slug, Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "1"},
		{SKU: "evs.ssd", Unit: "gb-hour", UnitPrice: "1"},
	}, true); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: strings.ToUpper(slug), AdminEmail: slug + "@example.test",
		StartDate: "2026-08-01", Commercial: postpaidTransfer()})
	if err != nil {
		t.Fatal(err)
	}
	src := mkSource(t, st, c.ID, "p-"+slug)
	assignBook(t, st, src.ID, book.ID)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for h := 0; h < 400; h++ { // 400 × 1 = 400.000000 of compute
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8",
			Quantity: "1.000000", Unit: "instance-hour", WindowStart: aug.Add(time.Duration(h) * time.Hour), WindowEnd: aug.Add(time.Duration(h+1) * time.Hour), Region: "me-east-215"})
	}
	for h := 0; h < 120; h++ { // 120 × 1 = 120.000000 of storage
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: "vol-1", ResourceKind: "evs", SKU: "evs.ssd",
			Quantity: "1.000000", Unit: "gb-hour", WindowStart: aug.Add(time.Duration(h) * time.Hour), WindowEnd: aug.Add(time.Duration(h+1) * time.Hour), Region: "me-east-215"})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return c
}

func ratOfDec(t *testing.T, d store.Decimal) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(string(d))
	if !ok {
		t.Fatalf("%q is not a number", d)
	}
	return r
}

// TWO RATES ON ONE INVOICE, through the database: the rules are written, the
// SKU families are categorised, a run rates the period, and the statement
// carries a per-rule summary whose amounts sum EXACTLY to its tax.
func TestIntegrationTwoRatesOnOneStatementAndTheSnapshotRecordsTheRuleID(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	taxSettings(t, st, "OM")
	c := seedTaxRun(t, st, "tworate")

	std, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "Oman VAT standard", Country: "om", Rate: "0.05", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16"})
	if err != nil {
		t.Fatal(err)
	}
	zero, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "Zero-rated storage", Country: "OM", Category: "storage", Rate: "0", Kind: store.TaxKindZeroRated,
		Note: "Zero-rated supply under the Executive Regulation.", EffectiveFrom: "2021-04-16"})
	if err != nil {
		t.Fatal(err)
	}
	if std.Country != "OM" {
		t.Errorf("country was not normalised: %q", std.Country)
	}
	if _, err := st.PutTaxCategory(ctx, store.TaxCategoryRule{SKU: "evs.*", Category: "storage"}); err != nil {
		t.Fatal(err)
	}

	// The customer is registered in Oman.
	country, business := "OM", true
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{TaxCountry: &country, TaxBusiness: &business}); err != nil {
		t.Fatal(err)
	}

	if _, err := rating.Run(ctx, st, "2026-08", c.ID); err != nil {
		t.Fatal(err)
	}
	drafts, err := st.ListStatements(ctx, store.OperatorScope, c.ID)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("statements = %+v err=%v", drafts, err)
	}
	draft, err := st.GetStatement(ctx, store.OperatorScope, drafts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.TaxLines) != 2 {
		t.Fatalf("the draft carries %d tax rows, want one per rule: %+v", len(draft.TaxLines), draft.TaxLines)
	}
	sum := new(big.Rat)
	byRule := map[string]store.TaxLine{}
	for _, l := range draft.TaxLines {
		sum.Add(sum, ratOfDec(t, l.Tax))
		byRule[l.RuleID] = l
	}
	if got := store.Decimal(sum.FloatString(6)); got != draft.Tax {
		t.Fatalf("the statement's tax %s is not the sum of its per-rule amounts %s", draft.Tax, got)
	}
	// 400 of compute at 5 % = 20; 120 of storage zero-rated = 0.
	if row := byRule[std.ID]; string(row.Base) != "400.000000" || string(row.Tax) != "20.000000" {
		t.Errorf("standard row = %+v, want base 400.000000 tax 20.000000", row)
	}
	if row := byRule[zero.ID]; string(row.Base) != "120.000000" || string(row.Tax) != "0.000000" || row.Kind != store.TaxKindZeroRated {
		t.Errorf("zero-rated row = %+v", row)
	}
	if string(draft.Subtotal) != "520.000000" || string(draft.Tax) != "20.000000" || string(draft.Total) != "540.000000" {
		t.Fatalf("waterfall = %s / %s / %s", draft.Subtotal, draft.Tax, draft.Total)
	}
	// Each LINE carries the category and the rule that priced it — what an
	// e-invoice needs per line.
	for _, l := range draft.Lines {
		switch {
		case strings.HasPrefix(l.SKU, "evs."):
			if l.TaxCategory != "storage" || l.TaxRuleID != zero.ID {
				t.Errorf("storage line = category %q rule %q", l.TaxCategory, l.TaxRuleID)
			}
		default:
			if l.TaxCategory != "" || l.TaxRuleID != std.ID {
				t.Errorf("compute line = category %q rule %q", l.TaxCategory, l.TaxRuleID)
			}
		}
	}

	// ISSUE freezes the summary onto the snapshot, WITH THE RULE IDS — the
	// answer to "under what rule was this zero-rated", years later, against
	// a rules table that has since been edited.
	issued, _, err := st.IssueStatementOnce(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap := issued.TaxSnapshot
	if snap == nil || len(snap.Lines) != 2 {
		t.Fatalf("the snapshot carries no per-rule summary: %+v", snap)
	}
	ids := map[string]bool{}
	for _, l := range snap.Lines {
		ids[l.RuleID] = true
	}
	if !ids[std.ID] || !ids[zero.ID] {
		t.Fatalf("the snapshot does not name both rules: %+v", snap.Lines)
	}
	if snap.SellerCountry != "OM" || snap.CustomerCountry != "OM" {
		t.Errorf("the snapshot did not record the two countries: %+v", snap)
	}

	// DELETING the rule does NOT change the invoice: what is frozen is a
	// record of what happened, not a foreign key.
	if err := st.DeleteTaxRule(ctx, zero.ID); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetStatement(ctx, store.OperatorScope, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.TaxSnapshot.Lines) != 2 || string(again.Tax) != "20.000000" {
		t.Fatalf("deleting a rule changed an issued invoice: tax %s snapshot %+v", again.Tax, again.TaxSnapshot.Lines)
	}
}

// A customer with NO tax profile on a Sovereign with NO rules is rated
// exactly as it was before §17: one rate, no summary.
func TestIntegrationNoProfileKeepsTheSingleRate(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := seedTaxRun(t, st, "plain")

	if _, err := rating.Run(ctx, st, "2026-08", c.ID); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListStatements(ctx, store.OperatorScope, c.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("statements = %+v err=%v", list, err)
	}
	s := list[0]
	// The seeded default rate is Oman VAT: 520 × 0.05 = 26.
	if string(s.TaxRate) != "0.0500" || string(s.Tax) != "26.000000" || string(s.Total) != "546.000000" {
		t.Fatalf("single-rate statement = rate %s tax %s total %s", s.TaxRate, s.Tax, s.Total)
	}
	if len(s.TaxLines) != 0 {
		t.Fatalf("a customer with no profile got a summary block: %+v", s.TaxLines)
	}
	issued, _, err := st.IssueStatementOnce(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.TaxSnapshot == nil || string(issued.TaxSnapshot.Rate) != "0.0500" || len(issued.TaxSnapshot.Lines) != 0 {
		t.Fatalf("snapshot = %+v", issued.TaxSnapshot)
	}
}

// AN EXPIRED EXEMPTION CERTIFICATE, through the database: the standard rate
// is charged and the audit on the snapshot says why.
func TestIntegrationExpiredCertificateFallsBackAndIsAudited(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	taxSettings(t, st, "OM")
	c := seedTaxRun(t, st, "expired")
	if _, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "Oman VAT standard", Country: "OM", Rate: "0.05", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16"}); err != nil {
		t.Fatal(err)
	}
	country, exempt, reason := "OM", true, "Free-zone establishment"
	number, expiry := "EX-2024-117", "2026-06-30"
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{
		TaxCountry: &country, TaxExempt: &exempt, TaxExemptReason: &reason,
		TaxExemptionNumber: &number, TaxExemptionExpiresOn: &expiry,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rating.Run(ctx, st, "2026-08", c.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListStatements(ctx, store.OperatorScope, c.ID)
	if len(list) != 1 {
		t.Fatalf("statements = %+v", list)
	}
	if string(list[0].Tax) != "26.000000" {
		t.Fatalf("tax = %s — an EXPIRED certificate must not exempt anything", list[0].Tax)
	}
	issued, _, err := st.IssueStatementOnce(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	snap := issued.TaxSnapshot
	if snap == nil {
		t.Fatal("no snapshot")
	}
	if snap.CustomerExemptionNumber != "EX-2024-117" || snap.CustomerExemptionExpiresOn != "2026-06-30" {
		t.Errorf("the snapshot did not record the certificate as it stood: %+v", snap)
	}
	if len(snap.Lines) != 1 || snap.Lines[0].Kind != store.TaxKindStandard {
		t.Fatalf("summary = %+v, want one standard row", snap.Lines)
	}
	// The AUDIT is durable: the customer was charged despite holding an
	// exemption, and the reason survives on the invoice itself.
	if len(snap.Audit) != 1 {
		t.Fatalf("the snapshot carries no determination audit: %+v", snap.Audit)
	}
	for _, want := range []string{"EX-2024-117", "2026-06-30", "NOT given"} {
		if !strings.Contains(snap.Audit[0], want) {
			t.Errorf("the frozen audit %q does not mention %q", snap.Audit[0], want)
		}
	}
	if len(issued.TaxAudit) != 1 || issued.TaxAudit[0] != snap.Audit[0] {
		t.Errorf("the statement's own audit and the snapshot's disagree: %+v vs %+v", issued.TaxAudit, snap.Audit)
	}
}

// The rules table refuses what it must: a duplicate key, a non-standard kind
// carrying a rate, a bad country, a validity that runs backwards.
func TestIntegrationTaxRuleValidationAndConflicts(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	first, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "Standard", Country: "OM", Rate: "0.05", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "Duplicate", Country: "OM", Rate: "0.10", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second rule for the same country and start date = %v, want ErrConflict", err)
	}
	for _, bad := range []store.TaxRule{
		{Name: "No country", Rate: "0.05", Kind: store.TaxKindStandard},
		{Name: "Three letters", Country: "OMN", Rate: "0.05", Kind: store.TaxKindStandard},
		{Name: "Exempt with a rate", Country: "AE", Rate: "0.05", Kind: store.TaxKindExempt},
		{Name: "Unknown kind", Country: "AE", Rate: "0", Kind: "maybe"},
		{Name: "Rate above one", Country: "AE", Rate: "5", Kind: store.TaxKindStandard},
		{Name: "Backwards", Country: "AE", Rate: "0.05", Kind: store.TaxKindStandard, EffectiveFrom: "2026-01-01", EffectiveTo: "2025-01-01"},
		{Country: "AE", Rate: "0.05", Kind: store.TaxKindStandard},
	} {
		if _, err := st.CreateTaxRule(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%q was accepted (err=%v)", bad.Name, err)
		}
	}
	// A rule CAN be superseded by one that starts later.
	if _, err := st.CreateTaxRule(ctx, store.TaxRule{Name: "New rate", Country: "OM", Rate: "0.10", Kind: store.TaxKindStandard, EffectiveFrom: "2027-01-01"}); err != nil {
		t.Fatalf("a later rule was refused: %v", err)
	}
	rules, err := st.ListTaxRules(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v err=%v", rules, err)
	}
	upd, err := st.UpdateTaxRule(ctx, first.ID, store.TaxRule{Name: "Standard, renamed", Country: "OM", Rate: "0.05", Kind: store.TaxKindStandard,
		Note: "Standard-rated supply.", EffectiveFrom: "2021-04-16", EffectiveTo: "2027-01-01"})
	if err != nil || upd.Name != "Standard, renamed" || upd.EffectiveTo != "2027-01-01" || upd.Note == "" {
		t.Fatalf("update = %+v err=%v", upd, err)
	}
	if err := st.DeleteTaxRule(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTaxRule(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted rule = %v", err)
	}
	if err := st.DeleteTaxRule(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting twice = %v", err)
	}
}

// A SKU is placed by an exact row first, then by the LONGEST matching
// prefix, and a SKU nothing matches is the default category.
func TestIntegrationTaxCategoryPlacement(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	for _, c := range []store.TaxCategoryRule{
		{SKU: "evs.*", Category: "storage"},
		{SKU: "evs.ssd.gold.*", Category: "premium-storage"},
		{SKU: "k8s.vcpu", Category: "compute"},
	} {
		if _, err := st.PutTaxCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := st.GetBillingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := st.TaxEngineFor(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	for sku, want := range map[string]string{
		"k8s.vcpu":          "compute",
		"evs.ssd":           "storage",
		"evs.ssd.gold.x":    "premium-storage",
		"ecs.m7n.xlarge.8":  "",
		"k8s.vcpu.reserved": "",
	} {
		if got := engine.CategoryFor(sku); got != want {
			t.Errorf("CategoryFor(%q) = %q, want %q", sku, got, want)
		}
	}
	// Re-placing a SKU replaces it; deleting returns it to the default.
	if _, err := st.PutTaxCategory(ctx, store.TaxCategoryRule{SKU: "k8s.vcpu", Category: "compute-reserved"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTaxCategory(ctx, "evs.*"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTaxCategory(ctx, "evs.*"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting twice = %v", err)
	}
	engine, err = st.TaxEngineFor(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := engine.CategoryFor("k8s.vcpu"); got != "compute-reserved" {
		t.Errorf("after replacement = %q", got)
	}
	if got := engine.CategoryFor("evs.ssd"); got != "" {
		t.Errorf("after deleting the family row = %q, want the default category", got)
	}
	if _, err := st.PutTaxCategory(ctx, store.TaxCategoryRule{SKU: "", Category: "x"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("an empty SKU was accepted")
	}
	if _, err := st.PutTaxCategory(ctx, store.TaxCategoryRule{SKU: "x", Category: ""}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("an empty category was accepted")
	}
}

// The e-invoice archive: one row per statement, replaced on a re-issue,
// carrying the document and its HASH and never a key.
func TestIntegrationEInvoiceArchive(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := seedTaxRun(t, st, "archive")
	if _, err := rating.Run(ctx, st, "2026-08", c.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListStatements(ctx, store.OperatorScope, c.ID)
	issued, _, err := st.IssueStatementOnce(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEInvoice(ctx, issued.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a statement with no profile configured has an e-invoice: %v", err)
	}
	rec, err := st.PutEInvoice(ctx, store.EInvoiceRecord{
		StatementID: issued.ID,
		Document:    []byte(`{"id":"INV-2026-00001"}`),
		XML:         "<Invoice/>",
		EInvoiceState: store.EInvoiceState{
			Profile: "oman", InvoiceNumber: issued.InvoiceNumber, State: store.EInvoiceArchived,
			Hash: "abc123", Signature: "c2ln", SignatureAlgorithm: "ECDSA-SHA256", KeyID: "einvoice-2026",
			QRPayload: "AQID", SubmitReason: "no accredited provider is configured",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != store.EInvoiceArchived || rec.Hash != "abc123" || rec.BuiltAt.IsZero() {
		t.Fatalf("archived record = %+v", rec)
	}
	// It reaches the statement document, so a console can show the state
	// without a second call.
	again, err := st.GetStatement(ctx, store.OperatorScope, issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.EInvoice == nil || again.EInvoice.State != store.EInvoiceArchived || again.EInvoice.QRPayload != "AQID" {
		t.Fatalf("the statement does not carry its e-invoice state: %+v", again.EInvoice)
	}
	// A re-issue REPLACES the row rather than piling up copies of the same
	// document.
	rec.State, rec.SubmitReference = store.EInvoiceSubmitted, "ASP-99"
	now := time.Now().UTC()
	rec.SubmittedAt = &now
	if _, err := st.PutEInvoice(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM einvoice_documents WHERE statement_id = $1`, issued.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("archive rows = %d err=%v, want exactly one", n, err)
	}
	final, err := st.GetEInvoice(ctx, issued.ID)
	if err != nil || final.State != store.EInvoiceSubmitted || final.SubmitReference != "ASP-99" || final.SubmittedAt == nil {
		t.Fatalf("replaced record = %+v err=%v", final, err)
	}
}
