package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/adapter/openova"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The gateway's own confirmation (DESIGN.md §9.2): POST
// /api/v1/gateways/{name}/callback books the payment without an operator
// click — verified by the gateway's signature, through the provider,
// idempotent on the reference — and the account balance is on the customer
// document.
func postCallback(t *testing.T, h http.Handler, gateway string, body any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/gateways/"+gateway+"/callback", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(openova.CallbackSignatureHeader, openova.SignCallback(secret, raw))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationGatewayCallbackBooksThePaymentWithoutAnOperator(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "sme-gw", Name: "SME on the gateway", AdminEmail: "ap@smegw.example",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: "omantel"}})
	if err != nil {
		t.Fatal(err)
	}
	d := draftFor(t, env.st, c.ID, "2026-01-01", "120.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	body := map[string]any{"statement_id": d.ID, "amount": "120.000000", "paid_at": "2026-02-01", "reference": "OMT-TXN-1", "status": "settled"}

	// Unsigned, mis-signed, unknown gateway, and a gateway with no callback.
	if rec := postCallback(t, env.h, "omantel", body, ""); rec.Code != 401 {
		t.Fatalf("unsigned = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postCallback(t, env.h, "omantel", body, "wrong"); rec.Code != 401 {
		t.Fatalf("mis-signed = %d", rec.Code)
	}
	if rec := postCallback(t, env.h, "nobody", body, callbackSecret); rec.Code != 404 {
		t.Fatalf("unknown gateway = %d", rec.Code)
	}
	if rec := postCallback(t, env.h, "manual", body, callbackSecret); rec.Code != 501 {
		t.Fatalf("manual gateway = %d %s", rec.Code, rec.Body.String())
	}
	// Nothing was booked by any of those.
	if st := mustDo(t, env.h, op, "GET", "/api/v1/statements/"+d.ID, 200); st["status"] != store.StatusIssued {
		t.Fatalf("a refused callback must book nothing: %+v", st)
	}

	// The signed one books the payment against the invoice — no operator.
	rec := postCallback(t, env.h, "omantel", body, callbackSecret)
	if rec.Code != 200 {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	stDoc, _ := out["statement"].(map[string]any)
	payDoc, _ := out["payment"].(map[string]any)
	if stDoc["status"] != store.StatusPaid || num(stDoc["paid_total"]) != 120 || payDoc["reference"] != "OMT-TXN-1" || payDoc["gateway"] != "omantel" || payDoc["recorded_by"] != "gateway:omantel" {
		t.Fatalf("callback outcome = %+v", out)
	}
	// Redelivered: the same payment, nothing booked twice.
	rec = postCallback(t, env.h, "omantel", body, callbackSecret)
	if rec.Code != 200 {
		t.Fatalf("redelivery = %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["duplicate"] != true {
		t.Fatalf("a redelivered confirmation is a duplicate: %+v", out)
	}
	if pays := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/payments", 200); len(pays["payments"].([]any)) != 1 {
		t.Fatalf("payments = %+v", pays)
	}

	// A checkout confirmation (no statement) is a top-up: credit on the account.
	rec = postCallback(t, env.h, "omantel", map[string]any{"customer_slug": "sme-gw", "amount": "40.000000", "reference": "OMT-TOPUP-1"}, callbackSecret)
	if rec.Code != 200 {
		t.Fatalf("checkout callback = %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if p, _ := out["payment"].(map[string]any); p["purpose"] != store.PurposeCheckout || num(p["unallocated"]) != 40 {
		t.Fatalf("checkout callback payment = %+v", out)
	}

	// The balance is on the customer document and in the directory — read-only, from the ledger.
	cust := mustDo(t, env.h, op, "GET", "/api/v1/customers/"+c.ID, 200)
	if num(cust["balance"]) != -40 || num(cust["available_credit"]) != 40 {
		t.Fatalf("customer balance = %v / %v, want -40 / 40", cust["balance"], cust["available_credit"])
	}
	list := mustDo(t, env.h, op, "GET", "/api/v1/customers", 200)
	found := false
	for _, row := range list["customers"].([]any) {
		if m := row.(map[string]any); m["id"] == c.ID {
			found = num(m["balance"]) == -40
		}
	}
	if !found {
		t.Fatalf("the directory carries the balance: %+v", list)
	}
	// The trail says the gateway did it.
	if actions := customerAuditActions(t, env.st, c.ID); !hasAction(actions, "gateway.callback") {
		t.Fatalf("audit = %v", actions)
	}

	// In external mode a confirmation FOR AN INVOICE is the billing system's
	// to book (409); a checkout still lands.
	setProvider(t, env.st, store.ProviderExternal)
	d2 := draftFor(t, env.st, c.ID, "2026-02-01", "10.000000")
	if _, err := env.st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{ExternalAccountID: strPtr("BA-GW")}); err != nil {
		t.Fatal(err)
	}
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", map[string]any{"notify": false}, 200)
	if rec := postCallback(t, env.h, "omantel", map[string]any{"statement_id": d2.ID, "amount": "10", "reference": "OMT-TXN-2"}, callbackSecret); rec.Code != 409 {
		t.Fatalf("invoice callback in external mode = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postCallback(t, env.h, "omantel", map[string]any{"customer_slug": "sme-gw", "amount": "5", "reference": "OMT-TOPUP-2"}, callbackSecret); rec.Code != 200 {
		t.Fatalf("checkout callback in external mode = %d %s", rec.Code, rec.Body.String())
	}
}

func strPtr(s string) *string { return &s }
