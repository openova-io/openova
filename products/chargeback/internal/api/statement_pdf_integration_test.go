package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/docs"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The whole download path against a real database and a stand-in renderer:
// issue an invoice, GET it as .pdf, and assert both that a document comes
// back AND that what was posted to the renderer is the invoice the ledger
// holds — the figures, the invoice number, the payment.
func TestIntegrationStatementPDFDownload(t *testing.T) {
	st := testdb.Open(t)

	var posted docs.Request
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &posted)
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.3\nrendered\n%%EOF"))
	}))
	defer renderer.Close()

	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	h := New(Deps{
		Store:   st,
		Keys:    keys,
		Mail:    &recMail{},
		Config:  config.Config{PublicURL: "https://chargeback.t99.omani.works", Profile: "sovereign", OperatorEmails: []string{opEmail}},
		Metrics: metrics.New(),
		Version: "test",
		Docs:    docs.New(renderer.URL, "shared-secret"),
	})
	op := operatorSession()

	// A customer, a draft with one rated line, issued so it takes a number.
	cust := mustJSONDo(t, h, op, "POST", "/api/v1/customers", map[string]any{
		"slug": "acme", "name": "Acme Trading LLC", "admin_email": "finance@acme.omani.homes",
		"charging": "billed", "payment_model": "postpaid", "payment_method": "transfer",
	}, 201)
	customerID := cust["id"].(string)

	draft := draftFor(t, st, customerID, "2026-08-01", "91.740600")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+draft.ID+"/issue", map[string]any{"notify": false}, 200)

	issued := mustDo(t, h, op, "GET", "/api/v1/statements/"+draft.ID, 200)
	invoiceNumber, _ := issued["invoice_number"].(string)
	if invoiceNumber == "" {
		t.Fatal("the statement took no invoice number at issue")
	}

	// ── the download ──────────────────────────────────────────────────────
	rec := do(t, h, op, "GET", "/api/v1/statements/"+draft.ID+".pdf", "")
	if rec.Code != 200 {
		t.Fatalf("download = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("body is not a document: %.20q", rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, invoiceNumber+".pdf") {
		t.Fatalf("Content-Disposition = %q, want an attachment named %s.pdf", cd, invoiceNumber)
	}

	// ── what reached the renderer is the real invoice ─────────────────────
	if posted.Template != docs.TemplateInvoice {
		t.Errorf("posted template = %q", posted.Template)
	}
	if posted.Document.Number != invoiceNumber {
		t.Errorf("posted number = %q, want %q", posted.Document.Number, invoiceNumber)
	}
	if posted.Currency != issued["currency"] {
		t.Errorf("posted currency = %q, statement says %v", posted.Currency, issued["currency"])
	}
	// The comparison is against the STORE, not against the JSON read: the API
	// emits a Decimal as a bare JSON number (store.Decimal.MarshalJSON), so
	// decoding it into map[string]any yields a float64 and the very precision
	// this assertion exists to protect would be lost inside the test itself.
	ledger, err := st.GetStatement(context.Background(), store.OperatorScope, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	for field, pair := range map[string][2]string{
		"total":        {posted.Document.Waterfall.Total, string(ledger.Total)},
		"net subtotal": {posted.Document.Waterfall.NetSubtotal, string(ledger.Subtotal)},
		"tax":          {posted.Document.Waterfall.Tax, string(ledger.Tax)},
		"tax rate":     {posted.Document.Waterfall.TaxRate, string(ledger.TaxRate)},
	} {
		if pair[0] != pair[1] {
			t.Errorf("posted %s = %q, the ledger holds %q — the document must carry the ledger's own digits, unrounded and unreformatted",
				field, pair[0], pair[1])
		}
	}
	if len(posted.Document.Lines) == 0 {
		t.Error("the posted document carries no lines")
	}
	if posted.Document.References.StatementID != draft.ID {
		t.Errorf("posted statement reference = %q", posted.Document.References.StatementID)
	}

	// ── a payment reaches the document ────────────────────────────────────
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+draft.ID+"/payments", map[string]any{
		"amount": "50.000000", "paid_at": time.Now().UTC().Format("2006-01-02"), "reference": "TRF-88213",
	}, 200)
	if rec := do(t, h, op, "GET", "/api/v1/statements/"+draft.ID+".pdf", ""); rec.Code != 200 {
		t.Fatalf("download after payment = %d: %s", rec.Code, rec.Body.String())
	}
	if len(posted.Document.Payments) != 1 || posted.Document.Payments[0].Reference != "TRF-88213" {
		t.Fatalf("the recorded payment did not reach the document: %+v", posted.Document.Payments)
	}
	if posted.Document.Waterfall.Paid == "" || posted.Document.Waterfall.Balance == "" {
		t.Errorf("the settlement figures are missing from the waterfall: %+v", posted.Document.Waterfall)
	}

	// ── the CSV and JSON renditions are untouched ─────────────────────────
	if rec := do(t, h, op, "GET", "/api/v1/statements/"+draft.ID+".csv", ""); rec.Code != 200 ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("the CSV download regressed: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := do(t, h, op, "GET", "/api/v1/statements/"+draft.ID, ""); rec.Code != 200 ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("the JSON read regressed: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A customer principal downloads its OWN invoice and is refused another's —
// the rule that makes this route safe to expose in the console.
func TestIntegrationStatementPDFCustomerScope(t *testing.T) {
	st := testdb.Open(t)

	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.3\nrendered\n%%EOF"))
	}))
	defer renderer.Close()

	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{9}, 32))
	h := New(Deps{
		Store: st, Keys: keys, Mail: &recMail{},
		Config:  config.Config{PublicURL: "https://chargeback.t99.omani.works", Profile: "sovereign", OperatorEmails: []string{opEmail}},
		Metrics: metrics.New(), Version: "test",
		Docs: docs.New(renderer.URL, ""),
	})
	op := operatorSession()

	mine := mustJSONDo(t, h, op, "POST", "/api/v1/customers", map[string]any{
		"slug": "mine", "name": "Mine LLC", "admin_email": "a@mine.example",
		"charging": "billed", "payment_model": "postpaid", "payment_method": "transfer",
	}, 201)["id"].(string)
	theirs := mustJSONDo(t, h, op, "POST", "/api/v1/customers", map[string]any{
		"slug": "theirs", "name": "Theirs LLC", "admin_email": "a@theirs.example",
		"charging": "billed", "payment_model": "postpaid", "payment_method": "transfer",
	}, 201)["id"].(string)

	myStatement := draftFor(t, st, mine, "2026-08-01", "10.000000")
	theirStatement := draftFor(t, st, theirs, "2026-08-01", "20.000000")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+myStatement.ID+"/issue", map[string]any{"notify": false}, 200)
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+theirStatement.ID+"/issue", map[string]any{"notify": false}, 200)

	owner := &store.Session{Email: "a@mine.example", Role: store.RoleCustomerAdmin, CustomerID: &mine,
		ExpiresAt: time.Now().Add(time.Hour)}

	if rec := do(t, h, owner, "GET", "/api/v1/statements/"+myStatement.ID+".pdf", ""); rec.Code != 200 {
		t.Fatalf("a customer could not download its own invoice: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, owner, "GET", "/api/v1/statements/"+theirStatement.ID+".pdf", "")
	if rec.Code != 404 {
		t.Fatalf("a customer downloading another customer's invoice got %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/pdf") {
		t.Fatal("a refusal was served as a document")
	}
}
