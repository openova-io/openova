package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// THE FINANCE HANDOVER, end to end against a real database (DESIGN.md §18):
// the journal the books are posted from, the settlement reconciliation, the
// period close and what it forbids afterwards, and who may reach any of it.

type financeEnv struct {
	h  http.Handler
	st *store.Store
	// customer is who the invoices are for; other is a second customer, so a
	// test can hold a DRAFT in a month whose first customer is already
	// invoiced (one statement per customer and period).
	customer store.Customer
	other    store.Customer
}

func setupFinanceAPI(t *testing.T) financeEnv {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{9}, 32))
	h := New(Deps{
		Store: st, Keys: keys, Mail: &recMail{},
		Config:  config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "sovereign", OperatorEmails: []string{opEmail}},
		Metrics: metrics.New(), Version: "test",
	})
	billed := store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{
		Slug: "acme", Name: "ACME LLC", AdminEmail: "ap@acme.example", Kind: "external", Commercial: billed,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateCustomer(context.Background(), store.CustomerInput{
		Slug: "globex", Name: "Globex", AdminEmail: "ap@globex.example", Kind: "external", Commercial: billed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return financeEnv{h: h, st: st, customer: c, other: other}
}

// issuedInvoice writes a draft for the period and issues it, which is what
// posts the invoice on the ledger the journal reads.
func issuedInvoice(t *testing.T, env financeEnv, periodStart, total string) store.Statement {
	t.Helper()
	d := draftFor(t, env.st, env.customer.ID, periodStart, total)
	rec := do(t, env.h, operatorSession(), "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`)
	if rec.Code != 200 {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	st, err := env.st.GetStatement(context.Background(), store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return m
}

func journalOf(t *testing.T, env financeEnv, period string) map[string]any {
	t.Helper()
	rec := do(t, env.h, operatorSession(), "GET", "/api/v1/finance/journal?period="+period, "")
	if rec.Code != 200 {
		t.Fatalf("journal = %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	j, _ := body["journal"].(map[string]any)
	if j == nil {
		t.Fatalf("no journal in %s", rec.Body.String())
	}
	return j
}

func journalCSV(t *testing.T, env financeEnv, period string) string {
	t.Helper()
	rec := do(t, env.h, operatorSession(), "GET", "/api/v1/finance/journal?period="+period+"&format=csv", "")
	if rec.Code != 200 {
		t.Fatalf("journal csv = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content type %s", ct)
	}
	return rec.Body.String()
}

// The journal of a month with an invoice and a payment in it: it balances,
// every line names the account the operator MAPPED, and every line traces
// back to the object it came from.
func TestIntegrationJournalBalancesAndTracesBack(t *testing.T) {
	env := setupFinanceAPI(t)
	ctx := context.Background()
	inv := issuedInvoice(t, env, "2026-08-01", "1000.000000")

	// A payment that settles the invoice in full, inside the same month.
	paid := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if _, err := env.st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID:  env.customer.ID,
		Payment:     store.PaymentInput{Amount: "1000.000000", PaidAt: paid, Method: store.PaymentMethodTransfer, Reference: "BANK-1", Status: store.PaymentReceived, Actor: opEmail},
		Allocations: []store.AllocationInput{{StatementID: inv.ID, Amount: "1000.000000"}},
	}); err != nil {
		t.Fatal(err)
	}

	j := journalOf(t, env, "2026-08")
	if j["balanced"] != true {
		t.Fatalf("the journal does not balance: %+v", j)
	}
	if j["total_debit"] != j["total_credit"] {
		t.Fatalf("debits %v credits %v", j["total_debit"], j["total_credit"])
	}
	lines, _ := j["lines"].([]any)
	if len(lines) == 0 {
		t.Fatal("no journal lines")
	}
	seenReceivableDebit, seenCash := false, false
	for _, raw := range lines {
		l := raw.(map[string]any)
		if l["source_kind"] == "" || l["source_id"] == "" {
			t.Fatalf("a line does not trace back: %+v", l)
		}
		if l["account_code"] == "" {
			t.Fatalf("a line carries no account code: %+v", l)
		}
		if l["account_key"] == store.AccountReceivable && l["debit"] != float64(0) {
			seenReceivableDebit = true
			if l["source_id"] != inv.ID {
				t.Fatalf("the receivable debit does not name the invoice: %+v", l)
			}
		}
		if l["account_key"] == store.AccountCash && l["debit"] != float64(0) {
			seenCash = true
		}
	}
	if !seenReceivableDebit || !seenCash {
		t.Fatalf("expected the invoice's receivable and the payment's cash: %+v", lines)
	}

	csv := journalCSV(t, env, "2026-08")
	if !strings.Contains(csv, inv.InvoiceNumber) || !strings.Contains(csv, "1100") {
		t.Fatalf("the CSV does not carry the invoice number and the mapped receivable code:\n%s", csv)
	}
}

// The operator's account map decides the codes; changing it changes the
// journal, which is the point of the map existing at all.
func TestIntegrationAccountMapDrivesTheCodes(t *testing.T) {
	env := setupFinanceAPI(t)
	issuedInvoice(t, env, "2026-08-01", "500.000000")
	before := journalCSV(t, env, "2026-08")
	if !strings.Contains(before, ",1100,") {
		t.Fatalf("the default receivable code is not in the journal:\n%s", before)
	}
	rec := do(t, env.h, operatorSession(), "PUT", "/api/v1/finance/accounts",
		`{"mappings":[{"key":"receivable","account_code":"41000","description":"Debtors"}]}`)
	if rec.Code != 200 {
		t.Fatalf("put accounts = %d: %s", rec.Code, rec.Body.String())
	}
	after := journalCSV(t, env, "2026-08")
	if !strings.Contains(after, ",41000,") || strings.Contains(after, ",1100,") {
		t.Fatalf("the journal did not follow the account map:\n%s", after)
	}
	// A key this product does not book to is refused by name.
	rec = do(t, env.h, operatorSession(), "PUT", "/api/v1/finance/accounts", `{"mappings":[{"key":"goodwill","account_code":"9"}]}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "goodwill") {
		t.Fatalf("an unknown account key = %d: %s", rec.Code, rec.Body.String())
	}
}

// A statement whose FROZEN tax snapshot carries several rules produces one
// tax-payable line per rule. The snapshot is read as it stands on the
// statement; nothing here recomputes tax.
func TestIntegrationMultiRateTaxSnapshotYieldsOnePayableLinePerRule(t *testing.T) {
	env := setupFinanceAPI(t)
	ctx := context.Background()
	inv := issuedInvoice(t, env, "2026-08-01", "1000.000000")
	// Restate the invoice with tax, and a snapshot naming the two rules the
	// tax was computed at. This is exactly the shape the statement carries.
	if _, err := env.st.DB().ExecContext(ctx, `UPDATE statements SET subtotal = 1000, tax = 55, total = 1055,
		tax_snapshot = $2::jsonb WHERE id = $1`, inv.ID,
		`{"rate":"0.05","customer_name":"ACME LLC","rules":[{"code":"VAT-5","name":"VAT standard","rate":"0.05","tax":"40.000000"},{"code":"EXCISE","name":"Excise","rate":"0.15","tax":"15.000000"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.DB().ExecContext(ctx, `UPDATE account_entries SET amount = 1055 WHERE statement_id = $1 AND kind = 'invoice'`, inv.ID); err != nil {
		t.Fatal(err)
	}
	j := journalOf(t, env, "2026-08")
	if j["balanced"] != true {
		t.Fatalf("multi-rate journal does not balance: %+v", j)
	}
	payable := []map[string]any{}
	for _, raw := range j["lines"].([]any) {
		l := raw.(map[string]any)
		if l["account_key"] == store.AccountTaxPayable {
			payable = append(payable, l)
		}
	}
	if len(payable) != 2 {
		t.Fatalf("expected one payable line per rule, got %d: %+v", len(payable), payable)
	}
	memos := payable[0]["memo"].(string) + "|" + payable[1]["memo"].(string)
	if !strings.Contains(memos, "VAT standard") || !strings.Contains(memos, "Excise") {
		t.Fatalf("the payable lines do not name their rules: %s", memos)
	}
}

// ---------------------------------------------------------------------------
// the period close
// ---------------------------------------------------------------------------

func TestIntegrationCloseRefusesADraftAndAnOpenDisputeByName(t *testing.T) {
	env := setupFinanceAPI(t)
	ctx := context.Background()
	issued := issuedInvoice(t, env, "2026-08-01", "400.000000")
	draft := draftFor(t, env.st, env.other.ID, "2026-08-01", "77.000000")

	rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/close", "{}")
	if rec.Code != 409 {
		t.Fatalf("close over a draft = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), draft.ID) {
		t.Fatalf("the refusal does not name the draft: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "still a draft") {
		t.Fatalf("the refusal does not say why: %s", rec.Body.String())
	}

	// Cancel the draft and open a dispute on the issued invoice instead.
	if _, _, err := env.st.CancelStatementBy(ctx, draft.ID, "not billable", opEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.OpenDispute(ctx, issued.ID, store.DisputeInput{Reason: "the storage line is wrong", Actor: "ap@acme.example"}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/close", "{}")
	if rec.Code != 409 {
		t.Fatalf("close over an open dispute = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "the storage line is wrong") || !strings.Contains(rec.Body.String(), "disputed") {
		t.Fatalf("the refusal does not name the dispute: %s", rec.Body.String())
	}

	// The same two are reported BEFORE the operator presses the button.
	rec = do(t, env.h, operatorSession(), "GET", "/api/v1/finance/periods/2026-08", "")
	if rec.Code != 200 {
		t.Fatalf("period = %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["can_close"] != false {
		t.Fatalf("can_close = %v with an open dispute", body["can_close"])
	}
	if blockers, _ := body["blockers"].([]any); len(blockers) != 1 {
		t.Fatalf("blockers = %+v", body["blockers"])
	}
}

// Once the books are closed: no statement in the period may be issued,
// re-run, cancelled or credited, and the refusal says who closed it.
func TestIntegrationAClosedPeriodRefusesEveryFinancialWrite(t *testing.T) {
	env := setupFinanceAPI(t)
	issued := issuedInvoice(t, env, "2026-08-01", "600.000000")

	rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/close", "{}")
	if rec.Code != 200 {
		t.Fatalf("close = %d: %s", rec.Code, rec.Body.String())
	}
	closed := decodeBody(t, rec)["period"].(map[string]any)
	if closed["status"] != store.PeriodClosed || closed["closed_by"] != opEmail {
		t.Fatalf("closed period = %+v", closed)
	}

	// A second draft in the closed month cannot be issued.
	late := draftFor(t, env.st, env.other.ID, "2026-08-01", "10.000000")
	for _, c := range []struct {
		name, method, path, body string
	}{
		{"issue", "POST", "/api/v1/statements/" + late.ID + "/issue", `{"notify":false}`},
		{"re-run the rating", "POST", "/api/v1/statements/run", `{"period":"2026-08"}`},
		{"cancel", "POST", "/api/v1/statements/" + issued.ID + "/cancel", `{"reason":"changed my mind"}`},
		{"credit", "POST", "/api/v1/statements/" + issued.ID + "/credit-notes", `{"reason":"goodwill","amount":"5.000000"}`},
	} {
		rec := do(t, env.h, operatorSession(), c.method, c.path, c.body)
		if rec.Code != 409 {
			t.Fatalf("%s in a closed period = %d: %s", c.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "closed by "+opEmail) {
			t.Fatalf("%s refusal does not say who closed it: %s", c.name, rec.Body.String())
		}
	}

	// An OPEN period is untouched by any of this.
	other := draftFor(t, env.st, env.customer.ID, "2026-09-01", "10.000000")
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/statements/"+other.ID+"/issue", `{"notify":false}`); rec.Code != 200 {
		t.Fatalf("issuing into an open period = %d: %s", rec.Code, rec.Body.String())
	}

	// Reopening needs settings.manage and a reason, and is audited.
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/reopen", `{"reason":""}`); rec.Code != 400 {
		t.Fatalf("reopen with no reason = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/reopen", `{"reason":"the August storage meter was wrong"}`)
	if rec.Code != 200 {
		t.Fatalf("reopen = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeBody(t, rec)["period"].(map[string]any)
	if p["status"] != store.PeriodReopened || p["reopened_by"] != opEmail || p["reopen_reason"] != "the August storage meter was wrong" {
		t.Fatalf("reopened period = %+v", p)
	}
	found, actor := sovereignAudit(t, env, "finance.period.reopen")
	if !strings.Contains(found, "the August storage meter was wrong") || actor != opEmail {
		t.Fatalf("the reopen is not audited with who and why: %s by %s", found, actor)
	}
	// And after the reopen the same writes are allowed again.
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/statements/"+late.ID+"/issue", `{"notify":false}`); rec.Code != 200 {
		t.Fatalf("issuing after the reopen = %d: %s", rec.Code, rec.Body.String())
	}
}

// THE stability property: a closed period exports byte-identically before
// and after an unrelated LATER-period event.
func TestIntegrationAClosedPeriodsJournalIsByteIdentical(t *testing.T) {
	env := setupFinanceAPI(t)
	ctx := context.Background()
	issuedInvoice(t, env, "2026-08-01", "800.000000")
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/close", "{}"); rec.Code != 200 {
		t.Fatalf("close = %d: %s", rec.Code, rec.Body.String())
	}
	before := journalCSV(t, env, "2026-08")

	// An unrelated September invoice, a September payment, and a September
	// top-up — none of which has anything to do with August.
	sept := issuedInvoice(t, env, "2026-09-01", "250.000000")
	if _, err := env.st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID:  env.customer.ID,
		Payment:     store.PaymentInput{Amount: "250.000000", PaidAt: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), Method: store.PaymentMethodTransfer, Reference: "BANK-SEP", Status: store.PaymentReceived, Actor: opEmail},
		Allocations: []store.AllocationInput{{StatementID: sept.ID, Amount: "250.000000"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID: env.customer.ID,
		Payment:    store.PaymentInput{Amount: "99.000000", PaidAt: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC), Method: store.PaymentMethodTransfer, Reference: "TOPUP-SEP", Status: store.PaymentReceived, Actor: opEmail},
	}); err != nil {
		t.Fatal(err)
	}

	after := journalCSV(t, env, "2026-08")
	if before != after {
		t.Fatalf("a closed period's journal changed after a later event:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	// September's own journal did move, which is what proves the comparison
	// above is not vacuous.
	if sep := journalCSV(t, env, "2026-09"); !strings.Contains(sep, "TOPUP-SEP") && !strings.Contains(sep, sept.InvoiceNumber) {
		t.Fatalf("the September journal is empty, so the stability check proved nothing:\n%s", sep)
	}
}

// ---------------------------------------------------------------------------
// reconciliation
// ---------------------------------------------------------------------------

func uploadSettlement(t *testing.T, env financeEnv, sess *store.Session, query, csv string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "settlement.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	req := httptest.NewRequest("POST", "/api/v1/finance/reconciliation"+query, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if sess != nil {
		req = req.WithContext(withSession(req.Context(), *sess))
	}
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationReconciliationReportsFourBucketsAndJournalsTheFees(t *testing.T) {
	env := setupFinanceAPI(t)
	ctx := context.Background()
	inv := issuedInvoice(t, env, "2026-08-01", "100.000000")
	pay := func(ref, amount string, day int) {
		t.Helper()
		if _, err := env.st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
			CustomerID:  env.customer.ID,
			Payment:     store.PaymentInput{Amount: store.Decimal(amount), PaidAt: time.Date(2026, 8, day, 9, 0, 0, 0, time.UTC), Method: store.PaymentMethodGateway, Gateway: "omantel", Reference: ref, Status: store.PaymentReceived, Actor: opEmail},
			Allocations: []store.AllocationInput{{StatementID: inv.ID, Amount: store.Decimal(amount)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	pay("GW-1", "60.000000", 5)
	pay("GW-2", "40.000000", 6)
	// One the gateway will not name at all.
	if _, err := env.st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID: env.customer.ID,
		Payment:    store.PaymentInput{Amount: "7.000000", PaidAt: time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC), Method: store.PaymentMethodGateway, Gateway: "omantel", Reference: "GW-9", Status: store.PaymentReceived, Actor: opEmail},
	}); err != nil {
		t.Fatal(err)
	}

	csv := "gateway_reference,amount,currency,settled_date,fee\n" +
		"GW-1,60.000000,OMR,2026-08-05,1.500000\n" + // matched
		"GW-2,39.000000,OMR,2026-08-06,1.000000\n" + // amount mismatch
		"GW-8,12.000000,OMR,2026-08-07,0.500000\n" + // missing in the ledger
		"GW-1,60.000000,OMR,2026-08-08,1.500000\n" // a duplicate reference

	rec := uploadSettlement(t, env, operatorSession(), "?gateway=omantel&from=2026-08-01&to=2026-08-31", csv)
	if rec.Code != 201 {
		t.Fatalf("reconcile = %d: %s", rec.Code, rec.Body.String())
	}
	run := decodeBody(t, rec)["run"].(map[string]any)
	want := map[string]float64{"matched": 1, "amount_mismatched": 1, "missing_in_ledger": 1, "missing_in_settlement": 1, "duplicates": 1}
	for k, v := range want {
		if run[k] != v {
			t.Fatalf("%s = %v, want %v (run %+v)", k, run[k], v, run)
		}
	}
	if run["fee_total"] != float64(2.5) {
		t.Fatalf("fee total = %v, want the duplicate NOT counted", run["fee_total"])
	}

	// The stored run reads back with its rows, so the operator can act on it.
	id := run["id"].(string)
	rec = do(t, env.h, operatorSession(), "GET", "/api/v1/finance/reconciliation/"+id, "")
	if rec.Code != 200 {
		t.Fatalf("get run = %d: %s", rec.Code, rec.Body.String())
	}
	stored := decodeBody(t, rec)["run"].(map[string]any)
	lines, _ := stored["lines"].([]any)
	if len(lines) != 5 {
		t.Fatalf("stored lines = %d: %+v", len(lines), lines)
	}
	mismatch := map[string]any{}
	for _, raw := range lines {
		l := raw.(map[string]any)
		if l["bucket"] == store.BucketAmountMismatch {
			mismatch = l
		}
	}
	if mismatch["settled_amount"] != float64(39) || mismatch["ledger_amount"] != float64(40) {
		t.Fatalf("the mismatch does not carry both figures: %+v", mismatch)
	}

	// NOTHING was corrected: the payments are exactly as they were.
	p, err := env.st.GetPayment(ctx, store.OperatorScope, int64(mismatch["payment_id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Amount != "40.000000" || p.Status != store.PaymentReceived {
		t.Fatalf("reconciliation changed a payment: %+v", p)
	}

	// The fees the run recorded are their own journal lines.
	j := journalOf(t, env, "2026-08")
	fees := 0
	for _, raw := range j["lines"].([]any) {
		l := raw.(map[string]any)
		if l["account_key"] == store.AccountGatewayFees {
			fees++
			if l["source_kind"] == "" || l["source_id"] == "" {
				t.Fatalf("a fee line does not trace back: %+v", l)
			}
		}
	}
	if fees != 2 {
		t.Fatalf("expected a journal line per recorded fee, got %d", fees)
	}
	if j["balanced"] != true {
		t.Fatalf("the journal with fees does not balance: %+v", j)
	}
}

// A gateway with no settlement feed says so rather than reporting an empty
// reconciliation, which would read as "everything matched".
func TestIntegrationAGatewayWithNoFeedSaysSo(t *testing.T) {
	env := setupFinanceAPI(t)
	rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/reconciliation?gateway=manual&from=2026-08-01&to=2026-08-31", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "publishes no settlement feed") {
		t.Fatalf("manual gateway fetch = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, env.h, operatorSession(), "POST", "/api/v1/finance/reconciliation?gateway=nowhere", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "no gateway named nowhere") {
		t.Fatalf("unknown gateway = %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// permissions
// ---------------------------------------------------------------------------

// Every finance route is at the Sovereign scope. A customer and a partner
// reach NONE of it; a finance-viewer reads and reconciles but closes
// nothing; only settings.manage closes, reopens and edits the account map.
func TestIntegrationFinancePermissionsPerRoute(t *testing.T) {
	env := setupFinanceAPI(t)
	cid := env.customer.ID
	partner, err := env.st.CreatePartner(context.Background(), store.PartnerInput{Slug: "reseller", Name: "Reseller"})
	if err != nil {
		t.Fatal(err)
	}
	customerOwner := &store.Session{Email: "ap@acme.example", Roles: []store.RoleBinding{{Role: store.RoleCustomerOwner, ScopeKind: store.ScopeKindCustomer, CustomerID: &cid}}}
	partnerOwner := &store.Session{Email: "pm@reseller.example", Roles: []store.RoleBinding{{Role: store.RolePartnerOwner, ScopeKind: store.ScopeKindPartner, PartnerID: &partner.ID}}}
	financeViewer := &store.Session{Email: "cfo@sovereign.example", Roles: []store.RoleBinding{{Role: store.RoleFinanceViewer, ScopeKind: store.ScopeKindSovereign}}}
	billingOperator := &store.Session{Email: "bill@sovereign.example", Roles: []store.RoleBinding{{Role: store.RoleBillingOperator, ScopeKind: store.ScopeKindSovereign}}}

	reads := []struct{ method, path, body string }{
		{"GET", "/api/v1/finance/journal?period=2026-08", ""},
		{"GET", "/api/v1/finance/accounts", ""},
		{"GET", "/api/v1/finance/periods", ""},
		{"GET", "/api/v1/finance/periods/2026-08", ""},
		{"GET", "/api/v1/finance/reconciliations", ""},
		{"POST", "/api/v1/finance/journal/export", `{"period":"2026-08"}`},
	}
	writes := []struct{ method, path, body string }{
		{"PUT", "/api/v1/finance/accounts", `{"mappings":[{"key":"cash","account_code":"1"}]}`},
		{"POST", "/api/v1/finance/periods/2026-08/close", "{}"},
		{"POST", "/api/v1/finance/periods/2026-08/reopen", `{"reason":"x"}`},
	}
	for _, c := range append(append([]struct{ method, path, body string }{}, reads...), writes...) {
		for name, sess := range map[string]*store.Session{"a customer owner": customerOwner, "a partner owner": partnerOwner, "nobody": nil} {
			rec := do(t, env.h, sess, c.method, c.path, c.body)
			wantCode := http.StatusForbidden
			if sess == nil {
				wantCode = http.StatusUnauthorized
			}
			if rec.Code != wantCode {
				t.Fatalf("%s %s as %s = %d, want %d: %s", c.method, c.path, name, rec.Code, wantCode, rec.Body.String())
			}
		}
	}
	// A finance-viewer holds audit.read + metering.read, so it reads and
	// reconciles — and closes nothing.
	for _, c := range reads {
		if rec := do(t, env.h, financeViewer, c.method, c.path, c.body); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Fatalf("%s %s as a finance-viewer = %d: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := uploadSettlement(t, env, financeViewer, "", "gateway_reference,amount\nGW-1,1\n"); rec.Code != 201 {
		t.Fatalf("reconcile as a finance-viewer = %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range writes {
		for name, sess := range map[string]*store.Session{"a finance-viewer": financeViewer, "a billing operator": billingOperator} {
			if rec := do(t, env.h, sess, c.method, c.path, c.body); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s as %s = %d, want 403: %s", c.method, c.path, name, rec.Code, rec.Body.String())
			}
		}
	}
	// A customer principal cannot reconcile either, file or no file.
	if rec := uploadSettlement(t, env, customerOwner, "", "gateway_reference,amount\nGW-1,1\n"); rec.Code != http.StatusForbidden {
		t.Fatalf("reconcile as a customer owner = %d: %s", rec.Code, rec.Body.String())
	}
}

// The journal leaves through the SAME outbox the bills do, so an operator
// receives it the way it already receives every other document.
func TestIntegrationJournalExportsThroughTheCommercialOutbox(t *testing.T) {
	env := setupFinanceAPI(t)
	issuedInvoice(t, env, "2026-08-01", "300.000000")
	rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/journal/export", `{"period":"2026-08"}`)
	if rec.Code != 202 {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	queued, err := env.st.ListOutbox(context.Background(), true, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range queued {
		if e.DocType == "journal" {
			found = true
			if !strings.Contains(e.IdempotencyKey, "2026-08") {
				t.Fatalf("the journal document is not keyed on its period: %s", e.IdempotencyKey)
			}
		}
	}
	if !found {
		t.Fatalf("no journal document reached the outbox: %+v", queued)
	}
	// Delivering the same period twice is one document at the far end.
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/journal/export", `{"period":"2026-08"}`); rec.Code != 202 {
		t.Fatalf("second export = %d: %s", rec.Code, rec.Body.String())
	}
	again, err := env.st.ListOutbox(context.Background(), true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if countJournals(again) != 1 {
		t.Fatalf("a second export queued a second document: %d", countJournals(again))
	}
}

func countJournals(entries []store.OutboxEntry) int {
	n := 0
	for _, e := range entries {
		if e.DocType == "journal" {
			n++
		}
	}
	return n
}

// A journal that cannot be produced is REFUSED, not served short: the
// balance assertion is the gate, and a close over it is refused too.
func TestIntegrationAnUnbalancedMappingRefusesTheExportAndTheClose(t *testing.T) {
	env := setupFinanceAPI(t)
	issuedInvoice(t, env, "2026-08-01", "1000.000000")
	if _, err := env.st.DB().ExecContext(context.Background(), `UPDATE account_mappings SET account_code = '' WHERE key = 'revenue'`); err != nil {
		t.Fatal(err)
	}
	rec := do(t, env.h, operatorSession(), "GET", "/api/v1/finance/journal?period=2026-08", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unbalanced journal = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not balance") || !strings.Contains(rec.Body.String(), "revenue") {
		t.Fatalf("the refusal does not name the difference and the account: %s", rec.Body.String())
	}
	rec = do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-08/close", "{}")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("close over an unbalanced journal = %d: %s", rec.Code, rec.Body.String())
	}
	if p, err := env.st.GetFinancePeriod(context.Background(), "2026-08"); err != nil || p.IsClosed() {
		t.Fatalf("the period was closed on a journal that does not balance: %+v (%v)", p, err)
	}
}

// A malformed period is a 400 everywhere it is accepted.
func TestIntegrationFinanceRefusesAMalformedPeriod(t *testing.T) {
	env := setupFinanceAPI(t)
	for _, path := range []string{
		"/api/v1/finance/journal?period=2026-13",
		"/api/v1/finance/journal?period=2026",
		"/api/v1/finance/periods/last-month",
	} {
		if rec := do(t, env.h, operatorSession(), "GET", path, ""); rec.Code != 400 {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, env.h, operatorSession(), "POST", "/api/v1/finance/periods/2026-99/close", "{}"); rec.Code != 400 {
		t.Fatalf("close a malformed period = %d: %s", rec.Code, rec.Body.String())
	}
}

// sovereignAudit reads the newest Sovereign-wide audit entry of one action
// (customer_id NULL), which is where the finance trail is written.
func sovereignAudit(t *testing.T, env financeEnv, action string) (details, actor string) {
	t.Helper()
	err := env.st.DB().QueryRowContext(context.Background(),
		`SELECT details::text, actor FROM audit_log WHERE action = $1 AND customer_id IS NULL ORDER BY at DESC, id DESC LIMIT 1`, action).
		Scan(&details, &actor)
	if err != nil {
		t.Fatalf("no %s audit entry: %v", action, err)
	}
	return details, actor
}
