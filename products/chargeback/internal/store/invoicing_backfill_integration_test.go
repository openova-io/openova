package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The invoice backfill (the 0.1.27 gap). The invoicing migration mapped the
// customers onto charging = 'billed' but never numbered the statements that
// were already issued, so every invoice issued before it carried no number,
// no terms and no due date. This test stands a database at the version
// BEFORE the backfill, writes exactly those rows, applies the migration and
// asserts every clause — then proves the batch is idempotent and that the
// live sequence continues after the numbers it dealt.
func TestIntegrationBackfillNumbersInvoicesIssuedBeforeInvoicing(t *testing.T) {
	db := openPAYGUnmigrated(t, "invoice_backfill_migration")
	ctx := context.Background()
	st := store.New(db)
	if err := st.MigrateUpTo(ctx, store.MigrationBackfillIssuedInvoices-1); err != nil {
		t.Fatalf("migrate to the pre-backfill shape: %v", err)
	}

	// Two billed customers (one with a standing purchase order) and one
	// informational, exactly as the invoicing migration leaves them.
	corp, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "nizwa-fintech", Name: "Nizwa Fintech", AdminEmail: "ap@nizwa.example",
		Kind: "organization", OrgSlug: "nizwa-fintech", PORef: "PO-2026-118",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}})
	if err != nil {
		t.Fatal(err)
	}
	recharge, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "ops-dept", Name: "Ops department", AdminEmail: "ops@dept.example",
		Kind: "organization", OrgSlug: "ops-dept",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodInternal}})
	if err != nil {
		t.Fatal(err)
	}
	show, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "show", Name: "Informational", AdminEmail: "s@show.example"})
	if err != nil {
		t.Fatal(err)
	}

	// The rows the pre-invoicing code wrote: issued, with issued_at, and
	// none of the invoice fields. Inserted by SQL because nothing in the
	// store can produce an issued statement without a number any more.
	issuedAt := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	insert := func(customerID, period, status string, at time.Time) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO statements (customer_id, period_start, period_end, currency, subtotal, tax_rate, tax, total, status, issued_at)
			VALUES ($1, $2::date, ($2::date + interval '1 month' - interval '1 day')::date, 'OMR', 100, 0, 0, 100, $3, $4) RETURNING id`,
			customerID, period, status, at).Scan(&id); err != nil {
			t.Fatalf("insert statement: %v", err)
		}
		return id
	}
	later := insert(corp.ID, "2026-08-01", "issued", issuedAt("2026-09-01T10:00:00Z"))
	earlier := insert(recharge.ID, "2026-08-01", "issued", issuedAt("2026-09-01T09:00:00Z"))
	lastYear := insert(corp.ID, "2025-07-01", "issued", issuedAt("2025-08-01T09:00:00Z"))
	informational := insert(show.ID, "2026-08-01", "issued", issuedAt("2026-09-01T08:00:00Z"))
	// A year whose counter already moved: the backfill must CONTINUE it, and
	// a statement that already carries a number is not touched.
	if _, err := db.ExecContext(ctx, `INSERT INTO invoice_sequences (year, last_value) VALUES (2024, 7)`); err != nil {
		t.Fatal(err)
	}
	numbered := insert(recharge.ID, "2024-05-01", "paid", issuedAt("2024-06-01T09:00:00Z"))
	if _, err := db.ExecContext(ctx, `UPDATE statements SET invoice_number = 'INV-2024-00007', payment_terms_days = 30, due_at = issued_at + interval '30 days' WHERE id = $1`, numbered); err != nil {
		t.Fatal(err)
	}
	oldYear := insert(corp.ID, "2024-05-01", "sent", issuedAt("2024-06-02T09:00:00Z"))

	get := func(id string) store.Statement {
		t.Helper()
		s, err := st.GetStatement(ctx, store.OperatorScope, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return s
	}
	if got := get(later); got.InvoiceNumber != "" || got.DueAt != nil || got.PaymentTermsDays != nil {
		t.Fatalf("the seeded invoice is already numbered before the migration under test — the test proves nothing: %+v", got)
	}

	// The migration under test.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("apply the backfill: %v", err)
	}

	terms := func(s store.Statement) int {
		if s.PaymentTermsDays == nil {
			return -1
		}
		return *s.PaymentTermsDays
	}
	want := []struct {
		id, number, po string
		issuedAt       string
	}{
		{earlier, "INV-2026-00001", "", "2026-09-01T09:00:00Z"},
		{later, "INV-2026-00002", "PO-2026-118", "2026-09-01T10:00:00Z"},
		{lastYear, "INV-2025-00001", "PO-2026-118", "2025-08-01T09:00:00Z"},
		{oldYear, "INV-2024-00008", "PO-2026-118", "2024-06-02T09:00:00Z"},
	}
	for _, w := range want {
		got := get(w.id)
		if got.InvoiceNumber != w.number {
			t.Errorf("%s: invoice_number = %q, want %q", w.issuedAt, got.InvoiceNumber, w.number)
		}
		if got.PORef != w.po {
			t.Errorf("%s: po_reference = %q, want the customer's %q", w.number, got.PORef, w.po)
		}
		if terms(got) != store.DefaultPaymentTermsDays {
			t.Errorf("%s: payment_terms_days = %d, want the customer's %d", w.number, terms(got), store.DefaultPaymentTermsDays)
		}
		wantDue := issuedAt(w.issuedAt).Add(30 * 24 * time.Hour)
		if got.DueAt == nil || !got.DueAt.Equal(wantDue) {
			t.Errorf("%s: due_at = %v, want issued_at + 30 days = %v", w.number, got.DueAt, wantDue)
		}
		if got.IssuedAt == nil || !got.IssuedAt.Equal(issuedAt(w.issuedAt)) {
			t.Errorf("%s: issued_at moved to %v", w.number, got.IssuedAt)
		}
	}
	if got := get(numbered); got.InvoiceNumber != "INV-2024-00007" {
		t.Errorf("an invoice that already had a number was renumbered to %q", got.InvoiceNumber)
	}
	if got := get(informational); got.InvoiceNumber != "" || got.DueAt != nil || got.PaymentTermsDays != nil || got.PORef != "" {
		t.Errorf("an informational customer's statement is not an invoice and must stay unnumbered: %+v", got)
	}
	seq := func(year int) int64 {
		t.Helper()
		var v int64
		if err := db.QueryRowContext(ctx, `SELECT last_value FROM invoice_sequences WHERE year = $1`, year).Scan(&v); err != nil {
			t.Fatalf("sequence %d: %v", year, err)
		}
		return v
	}
	if got := seq(2026); got != 2 {
		t.Errorf("invoice_sequences[2026] = %d after the backfill, want 2", got)
	}
	if got := seq(2025); got != 1 {
		t.Errorf("invoice_sequences[2025] = %d after the backfill, want 1", got)
	}
	if got := seq(2024); got != 8 {
		t.Errorf("invoice_sequences[2024] = %d after the backfill, want 7 + 1 = 8", got)
	}

	// Idempotent: a second run finds nothing.
	if n, err := st.BackfillIssuedInvoices(ctx); err != nil || n != 0 {
		t.Fatalf("second backfill = %d, %v; want 0, nil", n, err)
	}
	if got := get(later); got.InvoiceNumber != "INV-2026-00002" {
		t.Errorf("a second run renumbered %q", got.InvoiceNumber)
	}

	// The live path continues AFTER the backfilled numbers: a real issue of
	// a new draft takes the next number of the current year.
	from, _ := time.Parse("2006-01-02", "2026-07-01")
	draft, err := st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID: corp.ID, PeriodStart: from, PeriodEnd: from.AddDate(0, 1, -1), Currency: "OMR",
		Subtotal: "10.000000", TaxRate: "0", Tax: "0", Total: "10.000000",
		Lines: []store.RatedLine{{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "1", UnitPrice: "10.000000", Amount: "10.000000", ResourceCount: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := st.IssueStatement(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	year := time.Now().UTC().Year()
	next := store.InvoiceNumberFor(store.DefaultInvoicePrefix, year, 1)
	if year == 2026 {
		next = "INV-2026-00003"
	}
	if issued.InvoiceNumber != next {
		t.Errorf("the first live issue after the backfill took %q, want %q (the sequence must continue)", issued.InvoiceNumber, next)
	}
	if issued.PORef != "PO-2026-118" || terms(issued) != 30 || issued.DueAt == nil {
		t.Errorf("the live issue lost its invoice fields: %+v", issued)
	}
}
