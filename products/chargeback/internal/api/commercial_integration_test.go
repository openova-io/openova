package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

const importSecret = "shared-with-the-billing-system"

type commercialEnv struct {
	h         http.Handler
	st        *store.Store
	mail      *recMail
	hook      *recHook
	exporter  *commercial.Recorder
	deliverer *commercial.Deliverer
}

func setupCommercialAPI(t *testing.T) commercialEnv {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	mail := &recMail{}
	hook := &recHook{}
	exporter := &commercial.Recorder{}
	deliverer := &commercial.Deliverer{Store: st, Exporter: exporter}
	h := New(Deps{
		Store:         st,
		Keys:          keys,
		Mail:          mail,
		Config:        config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}},
		Metrics:       metrics.New(),
		Version:       "test",
		StatementHook: hook,
		Commercial:    commercial.NewSelector(st, exporter),
		Deliverer:     deliverer,
		Importer:      &commercial.Importer{Store: st, Secret: importSecret},
	})
	return commercialEnv{h: h, st: st, mail: mail, hook: hook, exporter: exporter, deliverer: deliverer}
}

// setProvider flips the Sovereign-level setting.
func setProvider(t *testing.T, st *store.Store, provider string) {
	t.Helper()
	if _, err := st.UpdateBillingSettings(context.Background(), store.BillingSettings{
		DiscountRule: store.DefaultDiscountRule, InvoicePrefix: store.DefaultInvoicePrefix, CommercialProvider: provider,
	}); err != nil {
		t.Fatalf("set commercial_provider=%s: %v", provider, err)
	}
}

// postSigned sends an HMAC-signed import.
func postSigned(t *testing.T, h http.Handler, body any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/commercial/import/invoice-status", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(commercial.SignatureHeader, commercial.Sign(secret, raw))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The SAME scripted history — create a customer, rate a period, issue it —
// run under both settings, so what changes is only who owns the invoice.
func TestIntegrationTheSameHistoryUnderBothCommercialProviders(t *testing.T) {
	env := setupCommercialAPI(t)
	ctx := context.Background()
	op := operatorSession()

	// ── internal: this product invoices ──────────────────────────────────
	setProvider(t, env.st, store.ProviderInternal)
	inhouse, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "inhouse", Name: "In-house", AdminEmail: "ap@inhouse.example",
		Commercial: postpaidTransferCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	d1 := draftFor(t, env.st, inhouse.ID, "2026-05-01", "1200.000000")
	issued := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d1.ID+"/issue", map[string]any{"notify": false}, 200)
	if issued["invoice_number"] == nil || issued["external_invoice_ref"] != nil {
		t.Fatalf("internal mode must number its own invoice and hold no external ref: %+v", issued)
	}
	// The whole lifecycle is ours.
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d1.ID+"/send", map[string]any{}, 200)
	paid := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d1.ID+"/payments", map[string]any{"amount": "1200.000000", "paid_at": "2026-06-10", "reference": "TRF-1"}, 200)
	if stDoc, _ := paid["statement"].(map[string]any); stDoc["status"] != store.StatusPaid {
		t.Fatalf("internal payment did not settle the invoice: %+v", paid["statement"])
	}
	if len(env.exporter.Docs()) != 0 {
		t.Fatalf("internal mode must export nothing: %+v", env.exporter.Docs())
	}

	// ── external: the operator's billing system invoices ─────────────────
	setProvider(t, env.st, store.ProviderExternal)
	corp, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "omantel-corp", Name: "Corporate", AdminEmail: "ap@corp.example",
		Commercial: postpaidTransferCommercial(), PORef: "PO-7788", ExternalAccountID: "BA-99001"})
	if err != nil {
		t.Fatal(err)
	}
	d2 := draftFor(t, env.st, corp.ID, "2026-05-01", "1000.100000")
	out := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", map[string]any{"notify": false}, 200)
	if out["status"] != store.StatusIssued {
		t.Fatalf("external issue = %+v", out)
	}
	// NO invoice number is ever assigned by us in external mode.
	if out["invoice_number"] != nil {
		t.Fatalf("external mode must not number the invoice: %v", out["invoice_number"])
	}
	// Issuing did not deliver — it queued.
	if len(env.exporter.Docs()) != 0 {
		t.Fatalf("issuing must not deliver synchronously: %+v", env.exporter.Docs())
	}
	// Two rows: the TMF678 bill and, beside it, the TMF635 rated usage
	// (DESIGN.md §9.1) — same outbox, same idempotency key.
	outbox := mustDo(t, env.h, op, "GET", "/api/v1/commercial/outbox", 200)
	if outbox["pending"] != float64(2) || outbox["commercial_provider"] != store.ProviderExternal {
		t.Fatalf("outbox = %+v", outbox)
	}

	// The delivery loop pushes both, and the export document carries EXACT money.
	if n, err := env.deliverer.DeliverDue(ctx); err != nil || n != 2 {
		t.Fatalf("delivery pass = %d (err %v)", n, err)
	}
	if usage := env.exporter.OfType("rated-usage"); len(usage) != 1 || usage[0].IdempotencyKey != d2.ID {
		t.Fatalf("the rated usage must leave beside the bill, keyed on the statement: %+v", usage)
	}
	doc, ok := env.exporter.Last()
	if !ok {
		t.Fatal("nothing was exported")
	}
	if doc.BillNo != "" {
		t.Errorf("the export must leave billNo to the billing system: %q", doc.BillNo)
	}
	if doc.BillingAccount.ID != "BA-99001" {
		t.Errorf("billingAccount.id = %q, want the customer's external account", doc.BillingAccount.ID)
	}
	if string(doc.TaxIncludedAmount.Value) != "1000.100000" || doc.TaxIncludedAmount.Unit != "OMR" {
		t.Errorf("taxIncludedAmount = %+v, want the exact total", doc.TaxIncludedAmount)
	}
	if doc.PurchaseOrder != "PO-7788" || doc.IdempotencyKey != d2.ID {
		t.Errorf("purchase order / idempotency key = %q / %q", doc.PurchaseOrder, doc.IdempotencyKey)
	}
	if len(doc.RatedProductUsage) != 1 || string(doc.RatedProductUsage[0].TaxExcludedRatingAmount.Value) != "1000.100000" {
		t.Errorf("rated usage = %+v", doc.RatedProductUsage)
	}
	// The reference the far end answered with is on the statement now.
	after := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d2.ID, 200)
	ref, _ := after["external_invoice_ref"].(string)
	if ref == "" {
		t.Fatalf("the delivered reference must be recorded: %+v", after)
	}

	// The lifecycle is theirs: our endpoints refuse.
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/send", map[string]any{}},
		{"/payments", map[string]any{"amount": "10.000000", "reference": "X"}},
		{"/cancel", map[string]any{"reason": "no"}},
	} {
		got := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d2.ID+c.path, c.body, 409)
		if msg, _ := got["error"].(string); !strings.Contains(msg, "owned by the external billing system") {
			t.Errorf("POST %s in external mode = %q", c.path, msg)
		}
	}

	// An imported paid status flips the statement to paid, with the imported
	// amount booked as a payment.
	rec := postSigned(t, env.h, commercial.InvoiceStatusImport{
		ExternalRef: ref, State: "paid", PaidAmount: "1000.100000", PaidAt: "2026-06-19", Reference: "BANK-88213",
	}, importSecret)
	if rec.Code != 200 {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	var imported map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &imported)
	if imported["status"] != store.StatusPaid || imported["paid_total"] != float64(1000.1) || imported["balance"] != float64(0) {
		t.Fatalf("after the import = %+v", imported)
	}
	// The payment it booked is a real row with the external reference on it.
	pays := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d2.ID+"/payments", 200)
	list, _ := pays["payments"].([]any)
	if len(list) != 1 {
		t.Fatalf("payments = %+v", pays["payments"])
	}
	p, _ := list[0].(map[string]any)
	if p["reference"] != "BANK-88213" || p["gateway"] != "external" {
		t.Fatalf("imported payment = %+v", p)
	}
	// Importing the same thing again changes nothing.
	if rec := postSigned(t, env.h, commercial.InvoiceStatusImport{ExternalRef: ref, State: "paid", PaidAmount: "1000.100000", Reference: "BANK-88213"}, importSecret); rec.Code != 200 {
		t.Fatalf("repeat import = %d: %s", rec.Code, rec.Body.String())
	}
	pays = mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d2.ID+"/payments", 200)
	if list, _ := pays["payments"].([]any); len(list) != 1 {
		t.Fatalf("a repeated import booked a second payment: %+v", pays["payments"])
	}
}

// Issuing succeeds while the billing system is down, and a document that
// failed three times is delivered exactly once when it comes back.
func TestIntegrationOutboxDeliversExactlyOnceAfterFailures(t *testing.T) {
	env := setupCommercialAPI(t)
	ctx := context.Background()
	op := operatorSession()
	setProvider(t, env.st, store.ProviderExternal)
	env.exporter.FailTimes = 3

	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "down", Name: "Down", AdminEmail: "ap@down.example",
		Commercial: postpaidTransferCommercial(), ExternalAccountID: "BA-1"})
	if err != nil {
		t.Fatal(err)
	}
	d := draftFor(t, env.st, c.ID, "2026-04-01", "50.000000")

	// The far end is down, and issuing still succeeds: the bill is raised
	// here and queued for them.
	issued := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	if issued["status"] != store.StatusIssued {
		t.Fatalf("issue while the billing system is down = %+v", issued)
	}

	// Three failed attempts, each recorded with its error and the bill never
	// delivered. The TMF635 rated-usage row queued beside it (DESIGN.md
	// §9.1) is not what the far end refuses, so the first pass delivers
	// that one row and nothing else.
	for i := 1; i <= 3; i++ {
		want := 0
		if i == 1 {
			want = 1
		}
		if n, err := env.deliverer.DeliverDue(ctx); err != nil || n != want {
			t.Fatalf("attempt %d delivered %d (err %v), want %d", i, n, err, want)
		}
		// Backoff pushes the next attempt out; the test drives it by hand.
		if _, err := env.st.RequeueOutbox(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := env.st.ListOutbox(ctx, true, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox = %+v (err %v)", entries, err)
	}
	if entries[0].Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", entries[0].Attempts)
	}
	if env.exporter.Attempts() != 3 || len(env.exporter.Docs()) != 0 {
		t.Fatalf("three attempts, nothing delivered: attempts=%d docs=%d", env.exporter.Attempts(), len(env.exporter.Docs()))
	}

	// It comes back: one more pass delivers, and the row is done.
	if n, err := env.deliverer.DeliverDue(ctx); err != nil || n != 1 {
		t.Fatalf("recovery pass = %d (err %v)", n, err)
	}
	pending, err := env.st.ListOutbox(ctx, true, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("still pending: %+v (err %v)", pending, err)
	}
	// Four attempts, ONE delivery, and it carried the statement id as its
	// idempotency key — which is what makes at-least-once delivery one bill
	// at the far end.
	if env.exporter.Attempts() != 4 {
		t.Fatalf("attempts = %d, want 4", env.exporter.Attempts())
	}
	docs := env.exporter.Docs()
	if len(docs) != 1 || docs[0].IdempotencyKey != d.ID {
		t.Fatalf("delivered = %d documents, keys must all be %s: %+v", len(docs), d.ID, docs)
	}
	// Further passes deliver nothing: delivered is delivered.
	if n, err := env.deliverer.DeliverDue(ctx); err != nil || n != 0 {
		t.Fatalf("a delivered row must not be delivered again: %d (err %v)", n, err)
	}
	if env.exporter.Attempts() != 4 {
		t.Fatalf("a delivered row must not be re-sent: attempts = %d", env.exporter.Attempts())
	}
	// And the operator's Retry refuses it rather than sending a second bill.
	if _, err := env.st.RequeueOutbox(ctx, 1); !store.IsConflict(err) {
		t.Fatalf("retrying a delivered document = %v, want a conflict", err)
	}
}

// The import is authenticated by an HMAC over the raw body, and nothing else.
func TestIntegrationInvoiceStatusImportRequiresASignature(t *testing.T) {
	env := setupCommercialAPI(t)
	setProvider(t, env.st, store.ProviderExternal)
	body := commercial.InvoiceStatusImport{ExternalRef: "whatever", State: "paid"}

	if rec := postSigned(t, env.h, body, ""); rec.Code != 401 {
		t.Fatalf("unsigned import = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postSigned(t, env.h, body, "the-wrong-secret"); rec.Code != 401 {
		t.Fatalf("wrongly signed import = %d: %s", rec.Code, rec.Body.String())
	}
	// Correctly signed, but naming an invoice nobody has: 404, not a write.
	if rec := postSigned(t, env.h, body, importSecret); rec.Code != 404 {
		t.Fatalf("signed import for an unknown ref = %d: %s", rec.Code, rec.Body.String())
	}
	// In internal mode the import is refused outright: we are the system of
	// record, and a second writer on the same ledger is the thing to avoid.
	setProvider(t, env.st, store.ProviderInternal)
	if rec := postSigned(t, env.h, body, importSecret); rec.Code != 409 {
		t.Fatalf("import in internal mode = %d: %s", rec.Code, rec.Body.String())
	}
}

// In external mode the commercial fields are read-only: the operator's
// billing system owns them. The external account id stays writable, because
// only we know which of our customers is which over there.
func TestIntegrationCommercialFieldsAreReadOnlyInExternalMode(t *testing.T) {
	env := setupCommercialAPI(t)
	op := operatorSession()
	setProvider(t, env.st, store.ProviderExternal)

	created := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers", map[string]any{
		"slug": "ro", "name": "Read only", "admin_email": "ap@ro.example", "external_account_id": "BA-42",
	}, 201)
	if created["external_account_id"] != "BA-42" {
		t.Fatalf("external_account_id must stay writable: %+v", created)
	}
	id, _ := created["id"].(string)
	for _, body := range []map[string]any{
		{"charging": store.ChargingBilled},
		{"payment_model": store.PaymentModelPostpaid},
		{"payment_method": store.PaymentMethodTransfer},
		{"gateway_name": store.GatewayStripe},
		{"po_reference": "PO-1"},
		{"payment_terms_days": 45},
	} {
		got := mustJSONDo(t, env.h, op, "PATCH", "/api/v1/customers/"+id, body, 400)
		if msg, _ := got["error"].(string); !strings.Contains(msg, "read-only here") {
			t.Errorf("PATCH %v = %q", body, msg)
		}
	}
	// Everything else still saves.
	mustJSONDo(t, env.h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"name": "Read only Ltd", "external_account_id": "BA-43"}, 200)
	// And in internal mode they are writable again.
	setProvider(t, env.st, store.ProviderInternal)
	mustJSONDo(t, env.h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"charging": store.ChargingBilled, "payment_model": store.PaymentModelPostpaid, "payment_method": store.PaymentMethodTransfer}, 200)
}

// The csvfile exporter writes one file per document, named by the
// idempotency key, so a redelivery overwrites rather than duplicating.
func TestCSVFileExporterWritesOneFilePerDocument(t *testing.T) {
	dir := t.TempDir()
	e := commercial.NewCSVFileExporter(dir)
	doc := commercial.InvoiceDocument{
		ID:                "1a2b3c4d-0000-0000-0000-000000000000",
		IdempotencyKey:    "1a2b3c4d-0000-0000-0000-000000000000",
		BillDate:          "2026-06-01T00:00:00Z",
		BillingPeriod:     commercial.Period{StartDateTime: "2026-05-01", EndDateTime: "2026-05-31"},
		BillingAccount:    commercial.BillingAccountRef{ID: "BA-1", Name: "Corporate", Slug: "omantel-corp"},
		TaxExcludedAmount: commercial.Money{Value: "1000.100000", Unit: "OMR"},
		TaxIncludedAmount: commercial.Money{Value: "1050.105000", Unit: "OMR"},
		TaxAmount:         commercial.Money{Value: "50.005000", Unit: "OMR"},
		AmountDue:         commercial.Money{Value: "1050.105000", Unit: "OMR"},
		RemainingAmount:   commercial.Money{Value: "1050.105000", Unit: "OMR"},
		DiscountTotal:     commercial.Money{Value: "0.000000", Unit: "OMR"},
		PurchaseOrder:     "PO-7788",
		PaymentTermsDays:  45,
		RatedProductUsage: []commercial.RatedUsage{{
			ProductRef: "ecs.s6.large.2", UsageQuantity: "744.000000", UnitOfMeasure: "instance-hour",
			RatingUnitPrice:         commercial.Money{Value: "1.344220", Unit: "OMR"},
			TaxExcludedRatingAmount: commercial.Money{Value: "1000.100000", Unit: "OMR"},
			ResourceCount:           1,
		}},
	}
	ref, err := e.Deliver(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "2026-05-omantel-corp-1a2b3c4d" {
		t.Fatalf("ref = %q", ref)
	}
	// Delivering again overwrites the same file: one bill, not two.
	if _, err := e.Deliver(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != ref+".csv" {
		t.Fatalf("directory = %+v (err %v)", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ref+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	// Exact money, as written, never a formatted number.
	for _, want := range []string{"1000.100000", "1050.105000", "50.005000", "1.344220", "BA-1", "PO-7788", ref} {
		if !strings.Contains(text, want) {
			t.Errorf("the CSV must carry %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "\n") < 7 {
		t.Errorf("expected a header, a line row and five total rows:\n%s", text)
	}
}

// postpaidTransferCommercial is the Omantel corporate position.
func postpaidTransferCommercial() store.Commercial {
	return store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
}
