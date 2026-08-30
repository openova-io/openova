package openova

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func strPtr(s string) *string { return &s }

// TestBillingHookIdempotentOnStatementID: the hook posts the statement total
// once; a re-issue posts the SAME external_ref (request_id = statement id)
// and billing answers duplicate — no second debit, no error.
func TestBillingHookIdempotentOnStatementID(t *testing.T) {
	var mu sync.Mutex
	var bodies []meteringPayload
	var auths []string
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/billing/metering/record" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var p meteringPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Errorf("bad body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, p)
		auths = append(auths, r.Header.Get("Authorization"))
		dup := seen[p.Metadata.RequestID]
		seen[p.Metadata.RequestID] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ledger_entry_id": "led-1", "balance_after_micro_omr": int64(1000000), "duplicate": dup})
	}))
	defer srv.Close()

	hook := &BillingHook{URL: srv.URL, Token: "test-superadmin-jwt", Metrics: metrics.New()}
	c := store.Customer{ID: "cust-1", Slug: "acme", Kind: "organization", BillingMode: "real", OrgSlug: strPtr("acme")}
	st := store.Statement{ID: "stmt-0001", CustomerID: "cust-1", PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31", Currency: "OMR", Total: "12.345"}

	for i := 0; i < 2; i++ {
		if err := hook.StatementIssued(context.Background(), st, c); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("posts = %d, want 2", len(bodies))
	}
	for i, p := range bodies {
		if p.Metadata.RequestID != "stmt-0001" {
			t.Fatalf("post %d request_id = %q, want the statement id", i, p.Metadata.RequestID)
		}
		if p.CustomerID != "acme" || p.Metadata.TenantID != "acme" {
			t.Fatalf("post %d customer = %q/%q, want the Organization slug", i, p.CustomerID, p.Metadata.TenantID)
		}
		if p.AmountMicroOMR != -12345000 {
			t.Fatalf("post %d amount = %d, want -12345000", i, p.AmountMicroOMR)
		}
		if p.Reason != "usage:chargeback:2026-08" {
			t.Fatalf("post %d reason = %q", i, p.Reason)
		}
		if auths[i] != "Bearer test-superadmin-jwt" {
			t.Fatalf("post %d auth = %q", i, auths[i])
		}
	}
	m := hook.Metrics
	if m.Get("chargeback_billing_hook_total", map[string]string{"result": "ok"}) != 1 ||
		m.Get("chargeback_billing_hook_total", map[string]string{"result": "duplicate"}) != 1 {
		t.Fatal("hook metrics did not record ok + duplicate")
	}
}

// TestBillingHookSkipsInapplicableCustomers: only kind=organization with
// billing_mode=real reaches billing (ADR-0014 D6); zero totals post nothing.
func TestBillingHookSkipsInapplicableCustomers(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hook := &BillingHook{URL: srv.URL, Metrics: metrics.New()}
	st := store.Statement{ID: "stmt-1", CustomerID: "c", PeriodStart: "2026-08-01", Total: "5"}
	cases := []store.Customer{
		{Kind: "external", BillingMode: "real"},
		{Kind: "organization", BillingMode: "chargeback"},
		{Kind: "organization", BillingMode: "showback"},
	}
	for _, c := range cases {
		if err := hook.StatementIssued(context.Background(), st, c); err != nil {
			t.Fatalf("%s/%s: %v", c.Kind, c.BillingMode, err)
		}
	}
	if err := hook.StatementIssued(context.Background(), store.Statement{ID: "stmt-2", PeriodStart: "2026-08-01", Total: "0"}, store.Customer{Kind: "organization", BillingMode: "real"}); err != nil {
		t.Fatalf("zero total: %v", err)
	}
	if calls != 0 {
		t.Fatalf("billing was called %d times, want 0", calls)
	}
}

// TestBillingHookSurfacesServerErrors: a non-2xx answer is an error the
// caller logs — the statement stays issued and a re-issue retries.
func TestBillingHookSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"superadmin or sovereign-admin role required"}`))
	}))
	defer srv.Close()
	hook := &BillingHook{URL: srv.URL, Metrics: metrics.New()}
	err := hook.StatementIssued(context.Background(), store.Statement{ID: "s", PeriodStart: "2026-08-01", Total: "1"}, store.Customer{Kind: "organization", BillingMode: "real", Slug: "acme"})
	if err == nil {
		t.Fatal("want an error on 403")
	}
}

func TestMicroOMR(t *testing.T) {
	cases := []struct {
		in   store.Decimal
		want int64
	}{
		{"0", 0},
		{"", 0},
		{"1", 1000000},
		{"12.345", 12345000},
		{"12.345678", 12345678},
		{"0.000001", 1},
		{"-3.5", -3500000},
		{"7.1234567", 7123456}, // beyond 6 decimals: truncated, never rounded up
	}
	for _, c := range cases {
		got, err := microOMR(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q → %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := microOMR("not-a-number"); err == nil {
		t.Fatal("want error for a non-decimal")
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		profile, override string
		inCluster, want   bool
	}{
		{"sovereign", "", true, true},
		{"sovereign", "", false, false},
		{"operator-central", "", true, false},
		{"operator-central", "true", true, true},
		{"sovereign", "false", true, false},
		{"sovereign", "true", false, false},
	}
	for _, c := range cases {
		got, why := Decide(c.profile, c.override, c.inCluster)
		if got != c.want {
			t.Fatalf("Decide(%q,%q,%v) = %v (%s), want %v", c.profile, c.override, c.inCluster, got, why, c.want)
		}
	}
}
