package store_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The same claim as store/dberr_test.go, but with Postgres writing the error
// rather than a literal: a real violation, tripped through the store methods
// the console calls, must come back classified as before and carrying nothing
// of the schema.

var uuidInText = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// mustNotLeak fails if the message carries a table name, a column name, an
// identifier, a UUID or the driver's own "Key (…)=(…)" shape. The assertions
// are on ABSENCE: a leak wearing a friendly prefix still fails here.
func mustNotLeak(t *testing.T, err error, extra ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("no error at all — the violation did not happen, so this proves nothing")
	}
	msg := err.Error()
	banned := append([]string{
		"Key", "=(", "cost_centres", "customer_id", "price_books", "budgets",
		"_key", "_idx", "_uniq", "_fkey",
	}, extra...)
	for _, b := range banned {
		if strings.Contains(msg, b) {
			t.Errorf("the message leaks %q: %s", b, msg)
		}
	}
	if uuidInText.MatchString(msg) {
		t.Errorf("the message carries a UUID: %s", msg)
	}
}

// TestDuplicateCostCentreCodeSaysSoWithoutTheSchema is the live defect end to
// end: the second ENG is refused, and the refusal is readable.
func TestDuplicateCostCentreCodeSaysSoWithoutTheSchema(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "owner@acme.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCostCentre(ctx, c.ID, store.CostCentreInput{Code: "ENG", Name: "Engineering"}); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCostCentre(ctx, c.ID, store.CostCentreInput{Code: "ENG", Name: "Engineering again"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second ENG = %v, want ErrConflict", err)
	}
	mustNotLeak(t, err, c.ID)
	if want := "conflict: that cost-centre code is already used by this customer"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// A reference to a customer that is not there is still ErrNotFound, and the
// message names neither the table it looked in nor the id it looked for.
func TestMissingReferenceSaysSoWithoutTheSchema(t *testing.T) {
	st := testdb.Open(t)
	absent := "9692021e-ce13-4bbd-9429-292e5f9218cd"
	_, err := st.CreateCostCentre(context.Background(), absent, store.CostCentreInput{Code: "ENG", Name: "Engineering"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cost centre for an absent customer = %v, want ErrNotFound", err)
	}
	mustNotLeak(t, err, "customers")
}

// A check constraint the operator can trip from the budgets screen.
func TestRefusedValueSaysSoWithoutTheConstraintName(t *testing.T) {
	st := testdb.Open(t)
	_, err := st.CreateBudget(context.Background(), store.BudgetInput{Name: "over", Amount: "-1", Active: true})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a negative budget = %v, want ErrConflict", err)
	}
	mustNotLeak(t, err)
	if want := "conflict: a budget cannot be negative"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// A store method that refuses in its own words keeps those words.
func TestPublicPriceBookConflictKeepsItsOwnSentence(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	first, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC 2026"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "Negotiated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPriceBookPublic(ctx, first.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = st.SetPriceBookPublic(ctx, second.ID, true)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second public book = %v, want ErrConflict", err)
	}
	if want := `conflict: "NC 2026" is already the public price book; withdraw it first`; err.Error() != want {
		t.Errorf("the method's own sentence was degraded: %q, want %q", err.Error(), want)
	}
}
