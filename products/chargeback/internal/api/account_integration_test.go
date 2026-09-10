package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/adapter/openova"
	"github.com/openova-io/openova/products/chargeback/internal/collections"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The account, payments, credit notes, collections and enforcement over the
// API (DESIGN.md §9), under BOTH commercial providers.

type accountEnv struct {
	h        http.Handler
	st       *store.Store
	mail     *recMail
	exporter *commercial.Recorder
	plat     *platform.Fake
	gateway  *recGateway
	deliver  *commercial.Deliverer
}

// recGateway is a registered gateway that settles a CHECKOUT synchronously
// and records every request, so the test can prove which purpose reached
// the seam.
type recGateway struct{ requests []settle.Request }

func (g *recGateway) RequestSettlement(_ context.Context, req settle.Request) (settle.Result, error) {
	g.requests = append(g.requests, req)
	if req.IsCheckout() {
		return settle.Result{Outcome: settle.Settled, Gateway: "omantel", Reference: "OMT-" + req.IntentID[:8]}, nil
	}
	return settle.Result{Outcome: settle.Pending, Gateway: "omantel", Reference: "OMT-INV", PayURL: "https://pay.omantel.example/x"}, nil
}

func (g *recGateway) ConfirmSettlement(_ context.Context, c settle.Confirmation) (settle.Payment, error) {
	return settle.Normalise(c, "omantel")
}

// callbackSecret is what the test gateway signs its callbacks with; the
// scheme is the billing hook's (HMAC-SHA256 over the raw body).
const callbackSecret = "omantel-callback-secret"

func (g *recGateway) VerifyCallback(r *http.Request) (settle.Confirmation, error) {
	hook := &openova.BillingHook{CallbackSecret: callbackSecret}
	conf, err := hook.VerifyCallback(r)
	if err != nil {
		return conf, err
	}
	conf.GatewayName, conf.Actor = "omantel", "gateway:omantel"
	return conf, nil
}

func (g *recGateway) purposes() []string {
	out := []string{}
	for _, r := range g.requests {
		out = append(out, r.Purpose)
	}
	return out
}

func setupAccountAPI(t *testing.T) accountEnv {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{9}, 32))
	mail := &recMail{}
	exporter := &commercial.Recorder{}
	plat := &platform.Fake{}
	gw := &recGateway{}
	reg := settle.NewRegistry()
	reg.Register("omantel", gw)
	sel := commercial.NewSelector(st, exporter)
	enf := &collections.Enforcer{Store: st, Platform: plat}
	deliverer := &commercial.Deliverer{Store: st, Exporter: exporter}
	h := New(Deps{
		Store: st, Keys: keys, Mail: mail,
		Config:     config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}},
		Metrics:    metrics.New(),
		Version:    "test",
		Settlement: reg,
		Commercial: sel,
		Deliverer:  deliverer,
		Importer:   &commercial.Importer{Store: st, Secret: importSecret, Enforcer: enf},
		Enforcer:   enf,
	})
	return accountEnv{h: h, st: st, mail: mail, exporter: exporter, plat: plat, gateway: gw, deliver: deliverer}
}

func postSignedTo(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(commercial.SignatureHeader, commercial.Sign(importSecret, raw))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func intp(v int) *int { return &v }

// The whole account over the API in INTERNAL mode: a split payment, credit
// on account, an explicit application, a credit note, the ledger and the
// aging report — every figure exact.
func TestIntegrationAccountOverTheAPI(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "corp", Name: "Corp", AdminEmail: "ap@corp.example", Commercial: postpaidTransferCommercial(), PaymentTermsDays: intp(0)})
	if err != nil {
		t.Fatal(err)
	}
	a := draftFor(t, env.st, c.ID, "2026-01-01", "300.000000")
	b := draftFor(t, env.st, c.ID, "2026-02-01", "200.000000")
	for _, d := range []store.Statement{a, b} {
		mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	}
	acct := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if num(acct["balance"]) != 500 || num(acct["outstanding"]) != 500 || num(acct["available_credit"]) != 0 || acct["account_owner"] != "internal" {
		t.Fatalf("account after two invoices = %+v", acct)
	}
	// A 600 payment split 300 + 150, the rest credit.
	p := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/payments", map[string]any{
		"amount": "600.000000", "reference": "TRF-600",
		"allocations": []map[string]any{{"statement_id": a.ID, "amount": "300.000000"}, {"statement_id": b.ID, "amount": "150.000000"}},
	}, 201)
	if num(p["allocated"]) != 450 || num(p["unallocated"]) != 150 || p["purpose"] != "collection" {
		t.Fatalf("payment = %+v", p)
	}
	pid := fmt.Sprint(int64(num(p["id"])))
	// The remainder, allocated automatically oldest-due-first, settles b.
	p = mustJSONDo(t, env.h, op, "POST", "/api/v1/payments/"+pid+"/allocate", map[string]any{"auto": true}, 200)
	if num(p["unallocated"]) != 100 {
		t.Fatalf("after auto-allocate = %+v", p)
	}
	stB := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+b.ID, 200)
	if stB["status"] != store.StatusPaid || num(stB["paid_total"]) != 200 {
		t.Fatalf("b = %+v", stB)
	}
	// A third invoice, a credit note on it, then the credit applied to it.
	cc := draftFor(t, env.st, c.ID, "2026-03-01", "400.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+cc.ID+"/issue", map[string]any{"notify": false}, 200)
	note := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+cc.ID+"/credit-notes", map[string]any{"reason": "outage credit", "amount": "50.000000"}, 201)
	if num(note["total"]) != 50 || num(note["applied"]) != 50 || !strings.HasPrefix(note["number"].(string), "CN-") {
		t.Fatalf("credit note = %+v", note)
	}
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+cc.ID+"/credit-notes", map[string]any{"reason": "too much", "amount": "351.000000"}, 409)
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+cc.ID+"/credit-notes", map[string]any{"amount": "1"}, 400)
	applied := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/account/apply-credit", map[string]any{"statement_ids": []string{cc.ID}}, 200)
	if got, _ := applied["applied"].(map[string]any); num(got[cc.ID]) != 100 {
		t.Fatalf("applied = %+v", applied)
	}
	stC := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+cc.ID, 200)
	if num(stC["balance"]) != 250 || num(stC["credited_total"]) != 50 || num(stC["paid_total"]) != 100 || stC["tax_snapshot"] == nil {
		t.Fatalf("c = balance %v credited %v paid %v snapshot %v", stC["balance"], stC["credited_total"], stC["paid_total"], stC["tax_snapshot"])
	}
	acct = mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if num(acct["balance"]) != 250 || num(acct["outstanding"]) != 250 || num(acct["available_credit"]) != 0 {
		t.Fatalf("account = %+v", acct)
	}
	entries, _ := acct["entries"].([]any)
	kinds := []string{}
	for _, e := range entries {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "invoice,invoice,payment,invoice,credit_note" {
		t.Fatalf("ledger = %v", kinds)
	}
	// The aging report: 250 current (due on receipt today, 0 days).
	aging := mustDo(t, env.h, op, "GET", "/api/v1/collections/aging", 200)
	rows, _ := aging["rows"].([]any)
	if len(rows) != 1 || num(aging["total"]) != 250 || num(aging["overdue"]) != 0 {
		t.Fatalf("aging = %+v", aging)
	}
	// A customer principal reads its own account and nobody else's.
	cust := &store.Session{Email: "ap@corp.example", Role: store.RoleCustomerAdmin, CustomerID: &c.ID}
	mustDo(t, env.h, cust, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	mustJSONDo(t, env.h, cust, "POST", "/api/v1/customers/"+c.ID+"/payments", map[string]any{"amount": "1"}, 403)
	other := "22222222-2222-2222-2222-222222222222"
	mustDo(t, env.h, cust, "GET", "/api/v1/customers/"+other+"/account", 404)
}

// CHECKOUT vs COLLECTION (founder refinement (a)): a checkout intent reaches
// the gateway in EVERY mode and its settled money lands as account credit;
// a collection intent reaches the gateway internally and is refused with
// 409 externally, where only the settled status is imported.
func TestIntegrationCheckoutReachesTheGatewayInEveryModeCollectionOnlyInternally(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "sme", Name: "SME", AdminEmail: "ap@sme.example", ExternalAccountID: "BA-77",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: "omantel"}})
	if err != nil {
		t.Fatal(err)
	}
	// ── internal ────────────────────────────────────────────────────────
	d := draftFor(t, env.st, c.ID, "2026-01-01", "120.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	col := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/payment-intents", map[string]any{"purpose": "collection", "statement_id": d.ID}, 201)
	if col["status"] != store.IntentPending || col["pay_url"] == nil || num(col["amount"]) != 120 {
		t.Fatalf("collection intent = %+v", col)
	}
	chk := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "50.000000"}, 201)
	if chk["status"] != store.IntentSettled || num(chk["payment_id"]) == 0 {
		t.Fatalf("checkout intent = %+v", chk)
	}
	acct := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if num(acct["available_credit"]) != 50 {
		t.Fatalf("a settled checkout is credit on the account: %+v", acct)
	}
	// Issue itself requested a collection too (the pre-seam hook path).
	if got := env.gateway.purposes(); strings.Join(got, ",") != "collection,collection,checkout" {
		t.Fatalf("gateway saw %v", got)
	}
	if len(env.exporter.Envelopes()) != 0 {
		t.Fatal("internal mode exports nothing")
	}

	// ── external ────────────────────────────────────────────────────────
	setProvider(t, env.st, store.ProviderExternal)
	env.gateway.requests = nil
	d2 := draftFor(t, env.st, c.ID, "2026-02-01", "80.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", map[string]any{"notify": false}, 200)
	// Issue never called the gateway for the invoice.
	if len(env.gateway.requests) != 0 {
		t.Fatalf("external issue must not ask the gateway for an invoice: %v", env.gateway.purposes())
	}
	rec := do(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/payment-intents", `{"purpose":"collection","statement_id":"`+d2.ID+`"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "external billing system") {
		t.Fatalf("collection intent in external mode = %d %s, want 409", rec.Code, rec.Body.String())
	}
	mustJSONDo(t, env.h, op, "POST", "/api/v1/payments/1/allocate", map[string]any{"auto": true}, 409)
	chk = mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "30.000000"}, 201)
	if chk["status"] != store.IntentSettled {
		t.Fatalf("checkout in external mode = %+v", chk)
	}
	if got := env.gateway.purposes(); strings.Join(got, ",") != "checkout" {
		t.Fatalf("external mode: gateway saw %v, want the checkout only", got)
	}
	// The checkout's money and the account it landed on are exported to the
	// billing system through the SAME outbox — TMF676 and TMF666.
	if n, err := env.deliver.DeliverDue(ctx); err != nil || n < 2 {
		t.Fatalf("delivery = %d (err %v)", n, err)
	}
	pays := env.exporter.OfType(external.DocPayment)
	var payDoc external.PaymentDocument
	if len(pays) != 1 || json.Unmarshal(pays[0].Document, &payDoc) != nil || payDoc.Purpose != "checkout" || string(payDoc.Amount.Value) != "30.000000" || payDoc.BillingAccount.ID != "BA-77" {
		t.Fatalf("payment export = %+v", pays)
	}
	if accts := env.exporter.OfType(external.DocAccount); len(accts) != 1 || !strings.Contains(string(accts[0].Document), `"receivableBalance"`) {
		t.Fatalf("account export = %+v", accts)
	}
	// And the invoice's settled status comes back as an import.
	var invoiceRef string
	for _, e := range env.exporter.Docs() {
		if e.ID == d2.ID {
			invoiceRef = commercial.ExportRef(e)
		}
	}
	if invoiceRef == "" {
		t.Fatal("the external issue must have exported the bill")
	}
	rec = postSignedTo(t, env.h, "/api/v1/commercial/import/invoice-status", map[string]any{"external_ref": invoiceRef, "state": "paid", "paid_amount": "80.000000", "reference": "BSS-1"})
	if rec.Code != 200 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	st2 := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d2.ID, 200)
	if st2["status"] != store.StatusPaid || num(st2["paid_total"]) != 80 {
		t.Fatalf("after import = %+v", st2)
	}
}

// The summary-charge variant (founder refinement (b)): WE number the invoice
// and produce its document, the outbox exports ONE summary line quoting
// that number, and the billing system reports back against it.
func TestIntegrationSummaryChargeVariantNumbersHereAndExportsOneLine(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	if _, err := env.st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "INV", CommercialProvider: store.ProviderExternal, ExternalIngest: store.IngestSummaryCharge}); err != nil {
		t.Fatal(err)
	}
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "erp-corp", Name: "ERP Corp", AdminEmail: "ap@erp.example", ExternalAccountID: "BA-ERP-1", Commercial: postpaidTransferCommercial(),
		Tax: store.TaxProfile{TaxRegistrationNumber: "OM3300000003"}})
	if err != nil {
		t.Fatal(err)
	}
	d := draftFor(t, env.st, c.ID, "2026-05-01", "1000.100000")
	out := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	number, _ := out["invoice_number"].(string)
	if !strings.HasPrefix(number, "INV-") {
		t.Fatalf("summary-charge mode numbers the invoice here: %+v", out)
	}
	if n, err := env.deliver.DeliverDue(ctx); err != nil || n != 2 {
		t.Fatalf("delivery = %d (err %v)", n, err)
	}
	if len(env.exporter.Docs()) != 0 {
		t.Fatalf("no TMF678 bill leaves in summary-charge mode: %+v", env.exporter.Docs())
	}
	sums := env.exporter.OfType(external.DocSummaryCharge)
	if len(sums) != 1 || sums[0].IdempotencyKey != d.ID {
		t.Fatalf("summary charges = %+v", sums)
	}
	var doc external.SummaryChargeDocument
	if err := json.Unmarshal(sums[0].Document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Reference != number || doc.BillingAccount.ID != "BA-ERP-1" || string(doc.TaxIncluded.Value) != "1000.100000" || doc.Period.StartDateTime != "2026-05-01" || doc.CustomerTaxNumber != "OM3300000003" {
		t.Fatalf("summary charge = %+v", doc)
	}
	if usage := env.exporter.OfType(external.DocRatedUsage); len(usage) != 1 || !strings.Contains(string(usage[0].Document), number) {
		t.Fatalf("the rated usage goes beside it, quoting the number: %+v", usage)
	}
	// Our own lifecycle endpoints are still theirs; the settled status
	// comes back quoting OUR number.
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/send", map[string]any{}, 409)
	rec := postSignedTo(t, env.h, "/api/v1/commercial/import/payment-status", map[string]any{"external_account_id": "BA-ERP-1", "invoice_number": number, "amount": "1000.100000", "reference": "ERP-PAY-1", "status": "settled", "paid_at": "2026-06-10"})
	if rec.Code != 200 {
		t.Fatalf("payment import = %d %s", rec.Code, rec.Body.String())
	}
	st := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d.ID, 200)
	if st["status"] != store.StatusPaid {
		t.Fatalf("after the payment import = %+v", st)
	}
	// A repeated report books nothing twice.
	rec = postSignedTo(t, env.h, "/api/v1/commercial/import/payment-status", map[string]any{"external_account_id": "BA-ERP-1", "invoice_number": number, "amount": "1000.100000", "reference": "ERP-PAY-1", "status": "settled"})
	if rec.Code != 200 {
		t.Fatalf("repeat import = %d", rec.Code)
	}
	acct := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if pays, _ := acct["payments"].([]any); len(pays) != 1 {
		t.Fatalf("payments = %+v", acct["payments"])
	}
	// The imported balance and the explicit enforcement command.
	rec = postSignedTo(t, env.h, "/api/v1/commercial/import/account-balance", map[string]any{"external_account_id": "BA-ERP-1", "balance": "-12.500000", "as_of": "2026-06-11"})
	if rec.Code != 200 {
		t.Fatalf("balance import = %d %s", rec.Code, rec.Body.String())
	}
	acct = mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if num(acct["external_balance"]) != -12.5 || acct["account_owner"] != "external" {
		t.Fatalf("external balance = %+v", acct)
	}
}

// Enforcement in external mode is executed by us ONLY on an explicit
// imported command — a payment status never implies it — and the daily
// evaluator runs nothing there.
func TestIntegrationExternalEnforcementIsExplicitOnly(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	setProvider(t, env.st, store.ProviderExternal)
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "org-x", Name: "Org X", AdminEmail: "ap@x.example", Kind: "organization", OrgSlug: "org-x", ExternalAccountID: "BA-X", Commercial: postpaidTransferCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	// A failed payment status changes nothing at the platform.
	rec := postSignedTo(t, env.h, "/api/v1/commercial/import/payment-status", map[string]any{"external_account_id": "BA-X", "amount": "10", "reference": "F1", "status": "failed"})
	if rec.Code != 200 || len(env.plat.Recorded()) != 0 {
		t.Fatalf("a failed payment must never suspend: %d %+v", rec.Code, env.plat.Recorded())
	}
	// The evaluator is a no-op in external mode.
	run := mustJSONDo(t, env.h, op, "POST", "/api/v1/collections/run", map[string]any{}, 200)
	if run["skipped"] != true {
		t.Fatalf("collections run in external mode = %+v", run)
	}
	// The explicit command does it, and is on the trail.
	rec = postSignedTo(t, env.h, "/api/v1/commercial/import/enforcement", map[string]any{"external_account_id": "BA-X", "action": "suspend", "reason": "unpaid in the BSS"})
	if rec.Code != 200 {
		t.Fatalf("enforcement import = %d %s", rec.Code, rec.Body.String())
	}
	if calls := env.plat.Recorded(); len(calls) != 1 || calls[0].Action != "suspend" || calls[0].Slug != "org-x" {
		t.Fatalf("platform = %+v", calls)
	}
	trail := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/suspensions", 200)
	if list, _ := trail["suspensions"].([]any); len(list) != 1 || list[0].(map[string]any)["source"] != store.SuspendSourceImport {
		t.Fatalf("trail = %+v", trail)
	}
	acct := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/account", 200)
	if acct["suspension"] == nil {
		t.Fatalf("the suspension is visible on the account: %+v", acct)
	}
	rec = postSignedTo(t, env.h, "/api/v1/commercial/import/enforcement", map[string]any{"external_account_id": "BA-X", "action": "resume"})
	if rec.Code != 200 || len(env.plat.Recorded()) != 2 {
		t.Fatalf("resume = %d %+v", rec.Code, env.plat.Recorded())
	}
	// Unsigned is refused before anything is decoded.
	req := httptest.NewRequest("POST", "/api/v1/commercial/import/enforcement", strings.NewReader(`{"external_account_id":"BA-X","action":"suspend"}`))
	w := httptest.NewRecorder()
	env.h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unsigned enforcement = %d", w.Code)
	}
}

// The operator's own suspend / resume, and a sent invoice cancelled through
// a full credit note over the API.
func TestIntegrationOperatorSuspendResumeAndCancelSentInvoice(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "org-y", Name: "Org Y", AdminEmail: "ap@y.example", Kind: "organization", OrgSlug: "org-y", Commercial: postpaidTransferCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	rec := mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/suspend", map[string]any{"reason": "credit review"}, 200)
	if rec["ok"] != true || rec["source"] != store.SuspendSourceOperator {
		t.Fatalf("suspend = %+v", rec)
	}
	cust := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID, 200)
	if cust["status"] != "suspended" || cust["platform_suspended_at"] == nil || cust["suspension_reason"] != "credit review" {
		t.Fatalf("customer = %+v", cust)
	}
	// A payment does not lift an OPERATOR suspension; only the operator does.
	d := draftFor(t, env.st, c.ID, "2026-01-01", "10.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "10.000000", "reference": "T1"}, 200)
	if got := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID, 200); got["status"] != "suspended" {
		t.Fatalf("a payment lifted an operator suspension: %+v", got)
	}
	mustJSONDo(t, env.h, op, "POST", "/api/v1/customers/"+c.ID+"/resume", map[string]any{}, 200)
	if got := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID, 200); got["status"] != "active" || got["platform_suspended_at"] != nil {
		t.Fatalf("after resume = %+v", got)
	}
	if calls := env.plat.Recorded(); len(calls) != 2 {
		t.Fatalf("platform = %+v", calls)
	}
	actions := customerAuditActions(t, env.st, c.ID)
	if !hasAction(actions, "customer.suspend") || !hasAction(actions, "customer.resume") {
		t.Fatalf("audit = %v", actions)
	}
	// A SENT invoice is cancelled through a full credit note.
	e := draftFor(t, env.st, c.ID, "2026-02-01", "70.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+e.ID+"/issue", map[string]any{"notify": false}, 200)
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+e.ID+"/send", map[string]any{}, 200)
	voided := mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+e.ID+"/cancel", map[string]any{"reason": "duplicate"}, 200)
	notes, _ := voided["credit_notes"].([]any)
	if voided["status"] != store.StatusCancelled || len(notes) != 1 || notes[0].(map[string]any)["kind"] != store.CreditNoteFull || num(notes[0].(map[string]any)["total"]) != 70 {
		t.Fatalf("cancel sent = %+v", voided)
	}
	if list := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+e.ID+"/credit-notes", 200); len(list["credit_notes"].([]any)) != 1 {
		t.Fatalf("credit notes = %+v", list)
	}
	_ = time.Now
}

// Every write of this lane that books or moves money is billing.collect /
// billing.issue — the operator's, never the customer's (DESIGN.md §10). The
// one exception is the checkout request (POST .../payment-intents): that is
// account.topup, which a customer-owner holds on its own account, and it is
// proven in access_integration_test.go.
func TestAccountEndpointsAreOperatorOnly(t *testing.T) {
	h := newAuthzHandler()
	a := "11111111-1111-1111-1111-111111111111"
	admin := &store.Session{Email: "adm@a.example", Role: store.RoleCustomerAdmin, CustomerID: &a}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/customers/" + a + "/payments"},
		{"POST", "/api/v1/payments"},
		{"POST", "/api/v1/payments/1/allocate"},
		{"POST", "/api/v1/payments/1/refund"},
		{"POST", "/api/v1/customers/" + a + "/account/apply-credit"},
		{"POST", "/api/v1/statements/x/credit-notes"},
		{"POST", "/api/v1/collections/run"},
		{"POST", "/api/v1/customers/" + a + "/suspend"},
		{"POST", "/api/v1/customers/" + a + "/resume"},
	} {
		if rec := do(t, h, admin, c.method, c.path, `{}`); rec.Code != 403 {
			t.Errorf("%s %s as customer-admin = %d, want 403", c.method, c.path, rec.Code)
		}
		if rec := do(t, h, nil, c.method, c.path, `{}`); rec.Code != 401 {
			t.Errorf("%s %s anonymously = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}
