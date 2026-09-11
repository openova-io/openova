package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The public flag and the designation move together; only one cloud book can
// be public; a platform book never can; the resolver reads the designation
// first and the flag second; withdrawing clears both (DESIGN.md §11).
func TestIntegrationPublicPriceBookDesignation(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if store.MigrationEstimates <= store.MigrationRoleBindings {
		t.Fatalf("estimates migration %d must come after the access migration %d", store.MigrationEstimates, store.MigrationRoleBindings)
	}
	list, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC list 2026"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, list.ID, []store.PriceItem{{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "0.10000000"}}, true); err != nil {
		t.Fatal(err)
	}
	negotiated, err := st.ClonePriceBook(ctx, list.ID, "Acme negotiated")
	if err != nil {
		t.Fatal(err)
	}
	if negotiated.Public {
		t.Fatal("a clone must never inherit the public flag")
	}
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.PublicPriceBook(ctx); !errors.Is(err, store.ErrNoPublicPriceBook) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no designation must read as ErrNoPublicPriceBook (a not-found), got %v", err)
	}
	pb, err := st.SetPriceBookPublic(ctx, list.ID, true)
	if err != nil || !pb.Public {
		t.Fatalf("make public = %+v err=%v", pb, err)
	}
	var designated sql.NullString
	if err := st.DB().QueryRowContext(ctx, `SELECT public_price_book_id FROM billing_settings WHERE id = 1`).Scan(&designated); err != nil || !designated.Valid || designated.String != list.ID {
		t.Fatalf("designation = %+v err=%v, want %s", designated, err, list.ID)
	}
	// A second public book is refused naming the current one; the flag on
	// the first is untouched.
	if _, err := st.SetPriceBookPublic(ctx, negotiated.ID, true); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second public book = %v, want ErrConflict", err)
	}
	if _, err := st.SetPriceBookPublic(ctx, plans.ID, true); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("platform book public = %v, want ErrInvalid", err)
	}
	got, err := st.PublicPriceBook(ctx)
	if err != nil || got.ID != list.ID || len(got.Items) != 1 || !got.Public {
		t.Fatalf("public book = %+v err=%v", got, err)
	}
	// The partial unique index is the backstop behind the store's check.
	if _, err := st.DB().ExecContext(ctx, `UPDATE price_books SET public = true WHERE id = $1`, negotiated.ID); err == nil {
		t.Fatal("the unique index must refuse a second public book written directly")
	}
	// Idempotent: making the public book public again is fine.
	if _, err := st.SetPriceBookPublic(ctx, list.ID, true); err != nil {
		t.Fatalf("re-publish = %v", err)
	}
	// Withdraw: flag and designation both clear; the resolver finds nothing.
	pb, err = st.SetPriceBookPublic(ctx, list.ID, false)
	if err != nil || pb.Public {
		t.Fatalf("withdraw = %+v err=%v", pb, err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT public_price_book_id FROM billing_settings WHERE id = 1`).Scan(&designated); err != nil || designated.Valid {
		t.Fatalf("designation after withdraw = %+v err=%v", designated, err)
	}
	if _, err := st.PublicPriceBook(ctx); !errors.Is(err, store.ErrNoPublicPriceBook) {
		t.Fatalf("after withdraw = %v", err)
	}
	// The designation alone (settings row) also resolves — an operator who
	// wrote it by hand gets the book it names.
	if _, err := st.DB().ExecContext(ctx, `UPDATE billing_settings SET public_price_book_id = $1 WHERE id = 1`, negotiated.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.PublicPriceBook(ctx); err != nil || got.ID != negotiated.ID {
		t.Fatalf("designation-only resolve = %+v err=%v", got, err)
	}
	// Deleting the designated book clears the designation (ON DELETE SET NULL).
	if _, err := st.DeletePriceBook(ctx, negotiated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublicPriceBook(ctx); !errors.Is(err, store.ErrNoPublicPriceBook) {
		t.Fatalf("after deleting the designated book = %v", err)
	}
}

// updated_at dates the public catalog: it moves when the header changes and
// when any item is added, changed or removed.
func TestIntegrationPriceBookUpdatedAtFollowsItems(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "Dated"})
	if err != nil {
		t.Fatal(err)
	}
	if pb.UpdatedAt.IsZero() || pb.UpdatedAt.Before(pb.CreatedAt) {
		t.Fatalf("fresh book updated_at = %v (created %v)", pb.UpdatedAt, pb.CreatedAt)
	}
	step := func(label string, before time.Time, do func() error) time.Time {
		t.Helper()
		time.Sleep(5 * time.Millisecond)
		if err := do(); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		after, err := st.GetPriceBook(ctx, pb.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !after.UpdatedAt.After(before) {
			t.Fatalf("%s did not move updated_at: %v → %v", label, before, after.UpdatedAt)
		}
		return after.UpdatedAt
	}
	at := pb.UpdatedAt
	at = step("add item", at, func() error {
		_, err := st.AddPriceItem(ctx, pb.ID, store.PriceItem{SKU: "eip", Unit: "hour", UnitPrice: "0.00500000"})
		return err
	})
	at = step("patch item", at, func() error {
		p := store.Decimal("0.00600000")
		_, err := st.UpdatePriceItem(ctx, pb.ID, "eip", store.PriceItemPatch{UnitPrice: &p})
		return err
	})
	at = step("rename book", at, func() error {
		_, err := st.UpdatePriceBook(ctx, pb.ID, store.PriceBookInput{Name: "Dated 2"})
		return err
	})
	_ = step("delete item", at, func() error { return st.DeletePriceItem(ctx, pb.ID, "eip") })
}

// An estimate round-trips with its lines and totals, is valid for 30 days,
// becomes a lead only when an address was left, and the Leads list carries
// exactly those.
func TestIntegrationEstimatesRoundTripAndLeads(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC list 2026"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	draft := store.EstimateDraft{
		Lines:    []store.EstimateLine{{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "2", Hours: "730", Months: 1, RatedQuantity: "1460.000000", UnitPrice: "0.10000000", Amount: "146.000000"}},
		Currency: "OMR", Region: "me-east-215-a",
		Subtotal: "146.000000", TaxRate: "0.0500", Tax: "7.300000", Total: "153.300000", Monthly: "153.300000", Yearly: "1839.600000",
		PriceBook:  store.EstimateBook{ID: pb.ID, Name: pb.Name, UpdatedAt: pb.UpdatedAt},
		ClientHash: "abc", Now: now,
	}
	anon, err := st.CreateEstimate(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if anon.ID == "" || anon.Lead || anon.ContactEmail != "" || !anon.ListPrices {
		t.Fatalf("anonymous estimate = %+v", anon)
	}
	if !anon.ValidUntil.Equal(now.Add(30*24*time.Hour)) || !anon.CreatedAt.Equal(now) {
		t.Fatalf("validity = %v..%v", anon.CreatedAt, anon.ValidUntil)
	}
	got, err := st.GetEstimate(ctx, anon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Amount != "146.000000" || got.Lines[0].RatedQuantity != "1460.000000" || got.Lines[0].Unit != "instance-hour" {
		t.Fatalf("lines = %+v", got.Lines)
	}
	if got.Subtotal != "146.000000" || got.Tax != "7.300000" || got.Total != "153.300000" || got.Monthly != "153.300000" || got.Yearly != "1839.600000" || got.TaxRate != "0.0500" {
		t.Fatalf("totals = %+v", got)
	}
	if got.PriceBook.ID != pb.ID || got.PriceBook.Name != pb.Name || !got.PriceBook.UpdatedAt.Equal(pb.UpdatedAt) || got.Region != "me-east-215-a" || got.Currency != "OMR" {
		t.Fatalf("book/region = %+v", got)
	}
	draft.ContactEmail = " Buyer@Example.com "
	lead, err := st.CreateEstimate(ctx, draft)
	if err != nil || !lead.Lead || lead.ContactEmail != "buyer@example.com" {
		t.Fatalf("lead = %+v err=%v", lead, err)
	}
	leads, err := st.ListLeads(ctx, 0)
	if err != nil || len(leads) != 1 || leads[0].ID != lead.ID || leads[0].ContactEmail != "buyer@example.com" {
		t.Fatalf("leads = %+v err=%v", leads, err)
	}
	if _, err := st.GetEstimate(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing estimate = %v", err)
	}
	if _, err := st.GetEstimate(ctx, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("malformed id = %v", err)
	}
	// Deleting the book keeps the estimate and its book name (SET NULL).
	if _, err := st.DeletePriceBook(ctx, pb.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := st.GetEstimate(ctx, anon.ID); err != nil || again.PriceBook.ID != "" || again.PriceBook.Name != "NC list 2026" {
		t.Fatalf("estimate after book delete = %+v err=%v", again, err)
	}
}

// The region list degrades to the sources' and usage's regions when the
// capacity module's table is absent, and reads that table when present.
func TestIntegrationEstimateRegionsFallback(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if regions, err := st.EstimateRegions(ctx); err != nil || len(regions) != 0 {
		t.Fatalf("empty Sovereign regions = %v err=%v", regions, err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "ops@acme.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertSource(ctx, c.ID, store.SourceKindFile, "me-east-215-b", "acme-file"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertSource(ctx, c.ID, store.SourceKindFile, "me-east-215-a", "acme-file-2"); err != nil {
		t.Fatal(err)
	}
	regions, err := st.EstimateRegions(ctx)
	if err != nil || len(regions) != 2 || regions[0] != "me-east-215-a" || regions[1] != "me-east-215-b" {
		t.Fatalf("regions = %v err=%v", regions, err)
	}
	// The regions a sovereign-admin defined in capacity management (§11) are
	// authoritative and win over what the sources happen to carry. The column
	// read here is capacity's own `code`: when this test asserted a table of
	// its own making with a `region` column, the product fell through to the
	// fallback forever and nothing noticed.
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO capacity_regions (code, name) VALUES ('cap-1', 'Capacity One'), ('cap-2', 'Capacity Two')`); err != nil {
		t.Fatal(err)
	}
	regions, err = st.EstimateRegions(ctx)
	if err != nil || len(regions) != 2 || regions[0] != "cap-1" || regions[1] != "cap-2" {
		t.Fatalf("regions from capacity_regions = %v err=%v", regions, err)
	}
	// With every defined region removed the source/ledger answer returns —
	// a Sovereign that never opened the Capacity page still offers regions.
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM capacity_regions`); err != nil {
		t.Fatal(err)
	}
	regions, err = st.EstimateRegions(ctx)
	if err != nil || len(regions) != 2 || regions[0] != "me-east-215-a" {
		t.Fatalf("fallback after the defined regions are removed = %v err=%v", regions, err)
	}
}
