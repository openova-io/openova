package commercial

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The export document must carry EXACT money and leave the invoice number to
// the billing system (DESIGN.md §8.10).
func TestBuildInvoiceDocument(t *testing.T) {
	issued := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	due := issued.AddDate(0, 0, 45)
	terms := 45
	src := "src-1"
	st := store.Statement{
		ID: "1a2b3c4d-0000-0000-0000-000000000000", CustomerID: "c-1",
		PeriodStart: "2026-05-01", PeriodEnd: "2026-05-31", Currency: "OMR",
		Subtotal: "1000.100000", TaxRate: "0.05", Tax: "50.005000", Total: "1050.105000",
		DiscountTotal: "10.000000", Paid: "0.000000", Status: store.StatusDraft,
		IssuedAt: &issued, DueAt: &due, PaymentTermsDays: &terms, PORef: "PO-7788",
		Lines: []store.RatedLine{{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "744.000000",
			UnitPrice: "1.344220", Amount: "1000.100000", ResourceCount: 2, SourceID: &src}},
	}
	c := store.Customer{Slug: "omantel-corp", Name: "Corporate", ExternalAccountID: "BA-99001"}

	doc, err := BuildInvoiceDocument(st, c)
	if err != nil {
		t.Fatal(err)
	}
	if doc.BillNo != "" {
		t.Errorf("billNo = %q, want empty — the billing system numbers its own invoices", doc.BillNo)
	}
	if doc.IdempotencyKey != st.ID || doc.ID != st.ID {
		t.Errorf("id / idempotency key = %q / %q", doc.ID, doc.IdempotencyKey)
	}
	if doc.BillingAccount.ID != "BA-99001" || doc.BillingAccount.Slug != "omantel-corp" {
		t.Errorf("billingAccount = %+v", doc.BillingAccount)
	}
	if doc.PaymentDueDate != due.Format(time.RFC3339) || doc.PaymentTermsDays != 45 || doc.PurchaseOrder != "PO-7788" {
		t.Errorf("terms = %q / %d / %q", doc.PaymentDueDate, doc.PaymentTermsDays, doc.PurchaseOrder)
	}
	// Exact money, unit and all, on every total and every line.
	for _, c := range []struct {
		name string
		m    Money
		want string
	}{
		{"taxExcludedAmount", doc.TaxExcludedAmount, "1000.100000"},
		{"taxAmount", doc.TaxAmount, "50.005000"},
		{"taxIncludedAmount", doc.TaxIncludedAmount, "1050.105000"},
		{"amountDue", doc.AmountDue, "1050.105000"},
		{"remainingAmount", doc.RemainingAmount, "1050.105000"},
		{"discountTotal", doc.DiscountTotal, "10.000000"},
	} {
		if string(c.m.Value) != c.want || c.m.Unit != "OMR" {
			t.Errorf("%s = %+v, want %s OMR", c.name, c.m, c.want)
		}
	}
	if len(doc.RatedProductUsage) != 1 {
		t.Fatalf("rated usage = %+v", doc.RatedProductUsage)
	}
	u := doc.RatedProductUsage[0]
	if u.ProductRef != "ecs.s6.large.2" || string(u.UsageQuantity) != "744.000000" || string(u.RatingUnitPrice.Value) != "1.344220" || u.SourceRef != "src-1" {
		t.Errorf("rated usage = %+v", u)
	}
	// It marshals as JSON with money as bare numbers, never strings.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	amount, _ := back["amountDue"].(map[string]any)
	if v, ok := amount["value"].(float64); !ok || v != 1050.105 {
		t.Errorf("amountDue.value = %v, want a JSON number", amount["value"])
	}

	// A customer with no billing account cannot be exported: the bill would
	// arrive somewhere it cannot be attributed.
	if _, err := BuildInvoiceDocument(st, store.Customer{Slug: "nowhere"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no external_account_id = %v, want ErrInvalid", err)
	}
}

// The import signature is over the RAW body, in constant time, and an
// unconfigured secret is never a way in.
func TestImportSignature(t *testing.T) {
	body := []byte(`{"external_ref":"r","state":"paid"}`)
	sig := Sign("s3cret", body)
	if err := VerifySignature("s3cret", sig, body); err != nil {
		t.Fatalf("a correct signature = %v", err)
	}
	if err := VerifySignature("s3cret", sig, append(body, ' ')); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a changed body = %v, want ErrBadSignature", err)
	}
	if err := VerifySignature("other", sig, body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a different secret = %v, want ErrBadSignature", err)
	}
	if err := VerifySignature("s3cret", "", body); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("no signature = %v, want ErrUnsigned", err)
	}
	if err := VerifySignature("", sig, body); !errors.Is(err, ErrImportNotConfigured) {
		t.Fatalf("no secret = %v, want ErrImportNotConfigured", err)
	}
}

// The billing system's vocabulary maps onto the three statuses an import may
// set — and `overdue` is not one of them, because overdue is derived here.
func TestMapState(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"sent":          store.StatusSent,
		"Validated":     store.StatusSent,
		"partiallyPaid": store.StatusSent,
		"overdue":       store.StatusSent,
		"paid":          store.StatusPaid,
		"SETTLED":       store.StatusPaid,
		"void":          store.StatusCancelled,
		"written_off":   store.StatusCancelled,
	}
	for in, want := range cases {
		got, err := MapState(in)
		if err != nil || got != want {
			t.Errorf("MapState(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := MapState("in-dispute"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("an unknown state must be refused, got %v", err)
	}
}

func TestParsePaidAtAndBackoff(t *testing.T) {
	day, err := ParsePaidAt("2026-06-19")
	if err != nil || day.Format("2006-01-02") != "2026-06-19" {
		t.Fatalf("a day = %v (err %v)", day, err)
	}
	if _, err := ParsePaidAt("19/06/2026"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("a malformed date = %v", err)
	}
	if now, err := ParsePaidAt(""); err != nil || now.IsZero() {
		t.Fatalf("an empty date must be now: %v (err %v)", now, err)
	}
	// Backoff doubles from a minute and stops at the cap, so a far end that
	// is down all afternoon is retried steadily rather than hammered.
	if d := store.OutboxBackoff(1); d != time.Minute {
		t.Errorf("first retry = %v", d)
	}
	if d := store.OutboxBackoff(4); d != 8*time.Minute {
		t.Errorf("fourth retry = %v", d)
	}
	if d := store.OutboxBackoff(30); d != store.OutboxBackoffCap {
		t.Errorf("a long outage = %v, want the cap", d)
	}
}
