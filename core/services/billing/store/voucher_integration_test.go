// Package store — integration tests for the voucher (promo code) lifecycle.
//
// Closes ticket #147 — "[L] test: integration test — voucher issuance via API
// — issue → redeem → Org created path".
//
// Why a real Postgres rather than sqlmock: per docs/INVIOLABLE-PRINCIPLES.md
// principle #2, "no mocks where the test would otherwise verify real
// behavior". The voucher path is concentrated in
// store.RedeemPromoCode which runs a real transaction (BEGIN, SELECT FOR
// UPDATE on promo_codes, COUNT lookup on promo_redemptions, INSERT, UPDATE,
// INSERT into credit_ledger, COMMIT). Mocking the SQL layer means we test
// the literal query strings — not whether the transactional invariants
// (one-redemption-per-customer, redemption cap, soft-deleted codes
// rejected) actually hold under concurrent contention. That distinction
// has bitten this codebase before (#93: counter incremented before order
// was committed), so the integration test runs against a real Postgres.
//
// The test is gated on BILLING_TEST_PG_URL — a connection string to a
// throwaway Postgres. CI populates it via the `postgres` service container
// (.github/workflows/test-billing-integration.yaml). When unset, the test
// is skipped (NOT mocked).
//
// "Org created path": the voucher mechanic produces credit; checkout then
// settles an Order from credit; Order completion publishes tenant.created
// over the event bus, and the tenant service consumes that to call
// CreateTenant. The Org-creation tail is owned by tenant-service tests
// (consumer_test.go covers the consumer side). This test asserts the
// money-side invariant: credits land, balance reflects them, and the same
// code cannot be double-redeemed nor used past its cap — which is what
// gates whether the tenant-create event ever fires.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// integrationDB returns the BILLING_TEST_PG_URL from env, or skips the test
// when absent. We hand back the URL (not a *sql.DB) because every test wants
// its own connection pool with a per-test search_path baked into the
// connection string — sharing a *sql.DB across schemas means goroutines pick
// up arbitrary connections that don't see the test's schema.
func integrationPGURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("BILLING_TEST_PG_URL")
	if url == "" {
		t.Skip("BILLING_TEST_PG_URL not set — skipping integration test (CI provides it via postgres service container)")
	}
	return url
}

// freshSchema creates an isolated schema, then opens a *sql.DB whose
// connection string sets `search_path=<schema>` via PG's `options=-c` —
// every connection in the pool inherits the schema setting, so goroutines
// in concurrency tests don't accidentally run their queries against the
// public schema.
func freshSchema(t *testing.T) (*Store, *sql.DB, func()) {
	t.Helper()
	baseURL := integrationPGURL(t)

	// Open a "control" connection on the base URL to create the schema.
	ctrl, err := sql.Open("postgres", baseURL)
	if err != nil {
		t.Fatalf("open pg ctrl: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ctrl.PingContext(ctx); err != nil {
		t.Fatalf("ping pg: %v", err)
	}
	defer ctrl.Close()

	schema := fmt.Sprintf("voucher_it_%d", time.Now().UnixNano())
	if _, err := ctrl.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %q", schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Build the per-test URL with search_path baked in. lib/pq supports the
	// libpq `options` parameter via the URL query string.
	sep := "?"
	if contains(baseURL, "?") {
		sep = "&"
	}
	scopedURL := fmt.Sprintf("%s%soptions=-c%%20search_path=%s", baseURL, sep, schema)
	db, err := sql.Open("postgres", scopedURL)
	if err != nil {
		t.Fatalf("open scoped pg: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping scoped: %v", err)
	}

	s := New(db)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cleanup := func() {
		db.Close()
		ctrl2, err := sql.Open("postgres", baseURL)
		if err == nil {
			_, _ = ctrl2.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA %q CASCADE", schema))
			ctrl2.Close()
		}
	}
	return s, db, cleanup
}

func mkCustomer(t *testing.T, s *Store, id string) *Customer {
	t.Helper()
	c := &Customer{
		UserID:   "user-" + id,
		TenantID: "tenant-" + id,
		Email:    id + "@example.com",
	}
	if err := s.CreateCustomer(context.Background(), c); err != nil {
		t.Fatalf("create customer %s: %v", id, err)
	}
	return c
}

func mkPromo(t *testing.T, s *Store, code string, credit, maxRedemptions int) {
	t.Helper()
	p := &PromoCode{
		Code:           code,
		CreditOMR:      credit,
		Description:    "integration test " + code,
		Active:         true,
		MaxRedemptions: maxRedemptions,
	}
	if err := s.UpsertPromoCode(context.Background(), p); err != nil {
		t.Fatalf("upsert promo %s: %v", code, err)
	}
}

// TestVoucherLifecycle_IssueRedeemAndCreditApplied is the canonical happy
// path: admin issues a voucher, customer redeems it, credit lands in the
// ledger, and the balance is exactly the voucher value. Asserting the
// balance closes the loop with the checkout flow — Checkout() reads
// GetCreditBalance() to settle orders from credit, so a redeemed voucher
// directly funds the next Order (which on completion publishes
// tenant.created and produces an Org).
func TestVoucherLifecycle_IssueRedeemAndCreditApplied(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust := mkCustomer(t, s, "alpha")
	mkPromo(t, s, "WELCOME50", 50, 0) // 50 OMR, unlimited redemptions

	// 1. Admin's voucher visible in the listing.
	listed, err := s.ListPromoCodes(ctx)
	if err != nil {
		t.Fatalf("list promos: %v", err)
	}
	found := false
	for _, p := range listed {
		if p.Code == "WELCOME50" {
			found = true
			if p.CreditOMR != 50 || !p.Active {
				t.Errorf("listed promo wrong: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("WELCOME50 not in ListPromoCodes output")
	}

	// 2. Pre-redemption balance is zero.
	bal, err := s.GetCreditBalance(ctx, cust.ID)
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("expected pre-redemption balance 0, got %d", bal)
	}

	// 3. Customer redeems voucher.
	credit, err := s.RedeemPromoCode(ctx, cust.ID, "WELCOME50")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if credit != 50 {
		t.Errorf("expected credit=50, got %d", credit)
	}

	// 4. Balance is now 50 OMR.
	bal, err = s.GetCreditBalance(ctx, cust.ID)
	if err != nil {
		t.Fatalf("get balance post-redeem: %v", err)
	}
	if bal != 50 {
		t.Errorf("expected balance=50 after redemption, got %d", bal)
	}

	// 5. Credit ledger has the right entry.
	entries, err := s.ListCreditEntries(ctx, cust.ID, 10)
	if err != nil {
		t.Fatalf("list credit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 credit entry, got %d", len(entries))
	}
	if entries[0].AmountOMR != 50 || entries[0].Reason != "promo:WELCOME50" {
		t.Errorf("credit entry wrong: %+v", entries[0])
	}

	// 6. promo_codes.times_redeemed incremented to 1.
	p, err := s.GetPromoCode(ctx, "WELCOME50")
	if err != nil {
		t.Fatalf("get promo: %v", err)
	}
	if p.TimesRedeemed != 1 {
		t.Errorf("expected times_redeemed=1, got %d", p.TimesRedeemed)
	}
}

// TestVoucherLifecycle_DoubleRedemptionBlocked verifies the per-customer
// redemption guard. Two consecutive RedeemPromoCode calls by the same
// customer must produce credit ONCE.
func TestVoucherLifecycle_DoubleRedemptionBlocked(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust := mkCustomer(t, s, "beta")
	mkPromo(t, s, "ONCE", 25, 0)

	if _, err := s.RedeemPromoCode(ctx, cust.ID, "ONCE"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	_, err := s.RedeemPromoCode(ctx, cust.ID, "ONCE")
	if err == nil {
		t.Fatal("expected second redemption to fail, got nil")
	}
	if !contains(err.Error(), "already redeemed") {
		t.Errorf("error should mention already-redeemed, got %q", err.Error())
	}
	bal, _ := s.GetCreditBalance(ctx, cust.ID)
	if bal != 25 {
		t.Errorf("balance after double-attempt should be 25 (single grant), got %d", bal)
	}
}

// TestVoucherLifecycle_RedemptionCapEnforcedUnderConcurrency exercises the
// FOR UPDATE locking path in RedeemPromoCode. We issue a voucher with a cap
// of N and fire 2N customers at it concurrently — exactly N must succeed.
func TestVoucherLifecycle_RedemptionCapEnforcedUnderConcurrency(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	const cap = 5
	const customers = 12
	mkPromo(t, s, "LIMITED", 10, cap)

	custs := make([]*Customer, customers)
	for i := 0; i < customers; i++ {
		custs[i] = mkCustomer(t, s, fmt.Sprintf("cap%d", i))
	}

	var wg sync.WaitGroup
	results := make(chan error, customers)
	for i := 0; i < customers; i++ {
		wg.Add(1)
		go func(c *Customer) {
			defer wg.Done()
			_, err := s.RedeemPromoCode(ctx, c.ID, "LIMITED")
			results <- err
		}(custs[i])
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != cap {
		t.Errorf("redemption cap violated under concurrency: expected %d successful redemptions, got %d", cap, successes)
	}

	p, _ := s.GetPromoCode(ctx, "LIMITED")
	if p.TimesRedeemed != cap {
		t.Errorf("times_redeemed wrong post-concurrent-redeem: expected %d, got %d", cap, p.TimesRedeemed)
	}
}

// TestVoucherLifecycle_SoftDeletedCodeRejected — once admin retires a
// voucher, subsequent redemption attempts must fail with "not found"
// (NOT a more specific tombstone-leaking error per #91).
func TestVoucherLifecycle_SoftDeletedCodeRejected(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust := mkCustomer(t, s, "gamma")
	mkPromo(t, s, "RETIRED", 30, 0)

	if err := s.DeletePromoCode(ctx, "RETIRED"); err != nil {
		t.Fatalf("delete promo: %v", err)
	}

	_, err := s.RedeemPromoCode(ctx, cust.ID, "RETIRED")
	if err == nil {
		t.Fatal("expected error redeeming soft-deleted promo")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error per #91 (no tombstone leak), got %q", err.Error())
	}

	// And it should not appear in admin listings.
	listed, _ := s.ListPromoCodes(ctx)
	for _, p := range listed {
		if p.Code == "RETIRED" {
			t.Error("soft-deleted promo should not be in ListPromoCodes")
		}
	}
}

// TestVoucherLifecycle_InactiveCodeRejected — admin can deactivate a code
// without deleting it; redemptions must fail with a distinct error so the
// admin UI can show "this is paused, not retired".
func TestVoucherLifecycle_InactiveCodeRejected(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust := mkCustomer(t, s, "delta")
	// Upsert with active=false.
	p := &PromoCode{Code: "PAUSED", CreditOMR: 20, Active: false}
	if err := s.UpsertPromoCode(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	_, err := s.RedeemPromoCode(ctx, cust.ID, "PAUSED")
	if err == nil {
		t.Fatal("expected error redeeming inactive promo")
	}
	if !contains(err.Error(), "not active") {
		t.Errorf("expected 'not active' error, got %q", err.Error())
	}
}

// TestVoucherLifecycle_DifferentCustomersIndependent — issuing one voucher
// to two distinct customers redeems for both.
func TestVoucherLifecycle_DifferentCustomersIndependent(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust1 := mkCustomer(t, s, "epsilon1")
	cust2 := mkCustomer(t, s, "epsilon2")
	mkPromo(t, s, "TWO", 15, 0)

	for _, c := range []*Customer{cust1, cust2} {
		credit, err := s.RedeemPromoCode(ctx, c.ID, "TWO")
		if err != nil {
			t.Fatalf("redeem for %s: %v", c.UserID, err)
		}
		if credit != 15 {
			t.Errorf("credit wrong for %s: %d", c.UserID, credit)
		}
		bal, _ := s.GetCreditBalance(ctx, c.ID)
		if bal != 15 {
			t.Errorf("balance wrong for %s: %d", c.UserID, bal)
		}
	}

	p, _ := s.GetPromoCode(ctx, "TWO")
	if p.TimesRedeemed != 2 {
		t.Errorf("times_redeemed wrong after two customers: %d", p.TimesRedeemed)
	}
}

// TestVoucherLifecycle_OrgPathPrerequisitesMet asserts the post-redemption
// state that the rest of the Org-creation chain depends on:
//   - Customer exists with TenantID set (the future Org's stable identifier)
//   - Credit balance > 0 (Checkout will short-circuit Stripe and produce
//     a 'paid' Order — which is what fires tenant.created downstream)
// This is the precondition the integration claim "voucher → Org created" hangs
// on. The Org row itself is in the tenant service's database (separate
// service); tenant_test.go (consumer_test.go) covers the event handler side.
func TestVoucherLifecycle_OrgPathPrerequisitesMet(t *testing.T) {
	// integration: real Postgres via BILLING_TEST_PG_URL
	
	s, _, cleanup := freshSchema(t)
	defer cleanup()

	ctx := context.Background()
	cust := mkCustomer(t, s, "zeta")
	mkPromo(t, s, "ORGSEED", 100, 0)

	if _, err := s.RedeemPromoCode(ctx, cust.ID, "ORGSEED"); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// Precondition 1: customer's TenantID is the slug-stable Org pointer.
	got, err := s.GetCustomerByUserID(ctx, cust.UserID)
	if err != nil || got == nil {
		t.Fatalf("get customer: %v (got=%+v)", err, got)
	}
	if got.TenantID == "" {
		t.Errorf("customer TenantID empty — Org-creation chain has no target")
	}

	// Precondition 2: balance covers the cheapest plan (used by Checkout to
	// short-circuit Stripe). 100 OMR is enough for any seed plan.
	bal, _ := s.GetCreditBalance(ctx, cust.ID)
	if bal < 1 {
		t.Errorf("balance must be > 0 to fund the Org-creating Order, got %d", bal)
	}
}

// contains is strings.Contains without the import, kept inline so this file
// stays minimal-deps.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
