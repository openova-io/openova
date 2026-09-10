package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// recHook records which statements reached the Stripe-backed billing hook.
// It is wired through Deps.StatementHook — the PRE-seam field — so these
// tests also prove that existing wiring still lands on the prepaid gateway.
type recHook struct {
	mu   sync.Mutex
	seen []string
}

func (h *recHook) StatementIssued(_ context.Context, st store.Statement, c store.Customer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, c.Slug+"/"+st.PeriodStart[:7])
	return nil
}

func (h *recHook) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.seen...)
}

func setupInvoicingAPI(t *testing.T) (http.Handler, *store.Store, *recMail, *recHook) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{5}, 32))
	mail := &recMail{}
	hook := &recHook{}
	h := New(Deps{
		Store:         st,
		Keys:          keys,
		Mail:          mail,
		Config:        config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}},
		Metrics:       metrics.New(),
		Version:       "test",
		StatementHook: hook,
	})
	return h, st, mail, hook
}

func operatorSession() *store.Session {
	return &store.Session{Email: opEmail, Role: store.RoleOperator, ExpiresAt: time.Now().Add(time.Hour)}
}

// draftFor writes one draft statement for a customer and period.
func draftFor(t *testing.T, st *store.Store, customerID, periodStart, total string) store.Statement {
	t.Helper()
	from, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.WriteDraftStatement(context.Background(), store.StatementDraft{
		CustomerID: customerID, PeriodStart: from, PeriodEnd: from.AddDate(0, 1, -1), Currency: "OMR",
		Subtotal: store.Decimal(total), TaxRate: "0", Tax: "0", Total: store.Decimal(total),
		Lines: []store.RatedLine{{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "1", UnitPrice: store.Decimal(total), Amount: store.Decimal(total), ResourceCount: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func customerAuditActions(t *testing.T, st *store.Store, customerID string) []string {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), store.OperatorScope, customerID, 200)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

func hasAction(list []string, want string) bool {
	for _, a := range list {
		if a == want {
			return true
		}
	}
	return false
}

// The no-regression proof, and the one condition the Stripe-backed hook
// fires on: charging=billed AND payment_method=gateway AND
// gateway_name=stripe — which is exactly what every billing_mode=real row
// maps to. Nothing else reaches it.
func TestIntegrationStripeGatewayFiresForBilledGatewayStripeAndNothingElse(t *testing.T) {
	h, st, _, hook := setupInvoicingAPI(t)
	ctx := context.Background()
	op := operatorSession()

	// (a) The migrated row: an Organization that was billing_mode=real,
	// created here through the same legacy input the CSV importer and the
	// Organization sync use. It must still be debited, or an upgrade would
	// silently stop billing real customers.
	legacy, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "legacy-org", Name: "Legacy", AdminEmail: "ap@legacy.example",
		Kind: "organization", OrgSlug: "legacy-org", BillingMode: store.BillingModeReal})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Commercial() != store.CommercialFromBillingMode(store.BillingModeReal) {
		t.Fatalf("a legacy real customer maps to %+v", legacy.Commercial())
	}
	if legacy.BillingMode != store.BillingModeReal {
		t.Fatalf("the derived mode = %s, want real", legacy.BillingMode)
	}
	d := draftFor(t, st, legacy.ID, "2026-01-01", "100.000000")
	if rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`); rec.Code != 200 {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	if got := hook.calls(); len(got) != 1 || got[0] != "legacy-org/2026-01" {
		t.Fatalf("billing hook calls = %v, want the legacy Organization debited", got)
	}

	// (b) The same position stated in the new fields debits identically.
	explicit, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "explicit-org", Name: "Explicit", AdminEmail: "ap@explicit.example",
		Kind: "organization", OrgSlug: "explicit-org",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: store.GatewayStripe}})
	if err != nil {
		t.Fatal(err)
	}
	d2 := draftFor(t, st, explicit.ID, "2026-01-01", "100.000000")
	do(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", `{"notify":false}`)
	if got := hook.calls(); len(got) != 2 || got[1] != "explicit-org/2026-01" {
		t.Fatalf("billing hook calls = %v, want the explicit stripe customer debited too", got)
	}

	// (c) A POST-PAID TRANSFER customer never reaches it — even though its
	// derived billing_mode is still 'real'. It pays against a purchase
	// order, and posting a Stripe debit would take money it never
	// authorised. This is the case the three modes could not express.
	corp, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "corp", Name: "Corporate", AdminEmail: "ap@corp.example",
		Kind: "organization", OrgSlug: "corp", PORef: "PO-1201",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}})
	if err != nil {
		t.Fatal(err)
	}
	if corp.BillingMode != store.BillingModeReal {
		t.Fatalf("a post-paid transfer customer derives %s, want real — and must still not be debited", corp.BillingMode)
	}
	d3 := draftFor(t, st, corp.ID, "2026-01-01", "900.000000")
	out := map[string]any{}
	rec := do(t, h, op, "POST", "/api/v1/statements/"+d3.ID+"/issue", `{"notify":false}`)
	if rec.Code != 200 {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["po_reference"] != "PO-1201" || out["invoice_number"] == nil {
		t.Fatalf("issued invoice = %+v", out)
	}
	if got := hook.calls(); len(got) != 2 {
		t.Fatalf("billing hook calls = %v, want the transfer customer NOT debited", got)
	}
	// The settlement request still happened — through the manual gateway,
	// which is what the audit trail says.
	var detail string
	entries, err := st.ListAudit(ctx, store.OperatorScope, corp.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "statement.settlement.requested" {
			var m map[string]any
			_ = json.Unmarshal(e.Details, &m)
			if m["payment_method"] != store.PaymentMethodTransfer || m["gateway"] != "manual" || m["outcome"] != string(settle.AwaitingTransfer) {
				t.Fatalf("settlement audit = %v", m)
			}
			detail, _ = m["detail"].(string)
		}
	}
	if !strings.Contains(detail, "PO-1201") {
		t.Fatalf("the settlement detail must name the purchase order: %q", detail)
	}

	// (d) An INTERNAL recharge (what chargeback meant) and (e) an
	// INFORMATIONAL customer (what showback meant) both settle nowhere.
	internal, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "internal-org", Name: "Internal", AdminEmail: "i@internal.example",
		Kind: "organization", OrgSlug: "internal-org", BillingMode: store.BillingModeChargeback})
	if err != nil {
		t.Fatal(err)
	}
	do(t, h, op, "POST", "/api/v1/statements/"+draftFor(t, st, internal.ID, "2026-01-01", "10.000000").ID+"/issue", `{"notify":false}`)
	show, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "show", Name: "Informational", AdminEmail: "s@show.example"})
	if err != nil {
		t.Fatal(err)
	}
	do(t, h, op, "POST", "/api/v1/statements/"+draftFor(t, st, show.ID, "2026-01-01", "10.000000").ID+"/issue", `{"notify":false}`)
	if got := hook.calls(); len(got) != 2 {
		t.Fatalf("billing hook calls = %v, want the internal and informational customers untouched", got)
	}

	// (f) A gateway this deployment has no implementation for is an error
	// the operator can see, not a silent debit anywhere.
	other, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "omantel-sme", Name: "SME", AdminEmail: "sme@omantel.example",
		Kind: "organization", OrgSlug: "omantel-sme",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: "omantel"}})
	if err != nil {
		t.Fatal(err)
	}
	d6 := draftFor(t, st, other.ID, "2026-01-01", "10.000000")
	do(t, h, op, "POST", "/api/v1/statements/"+d6.ID+"/issue", `{"notify":false}`)
	if got := hook.calls(); len(got) != 2 {
		t.Fatalf("billing hook calls = %v, want an unregistered gateway to reach nothing", got)
	}
	if !hasAction(customerAuditActions(t, st, other.ID), "statement.hook.error") {
		t.Error("an unregistered gateway must be audited as a settlement error")
	}
}

// The whole post-paid walk over HTTP: edit the purchase order on the draft,
// issue it into a numbered invoice, send it, take a part payment, refuse an
// overpayment, and settle it — with an audit entry for every transition.
func TestIntegrationInvoiceLifecycleOverTheAPI(t *testing.T) {
	h, st, mail, hook := setupInvoicingAPI(t)
	ctx := context.Background()
	op := operatorSession()

	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "omantel-corp", Name: "Corporate customer", AdminEmail: "ap@corp.example", PORef: "PO-8000",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}})
	if err != nil {
		t.Fatal(err)
	}
	d := draftFor(t, st, c.ID, "2026-06-01", "1200.000000")

	// The operator quotes the PO this period was raised under and agrees 60
	// days rather than the customer's standing terms.
	body := map[string]any{"po_reference": "PO-8123", "payment_terms_days": 60}
	got := mustJSONDo(t, h, op, "PATCH", "/api/v1/statements/"+d.ID, body, 200)
	if got["po_reference"] != "PO-8123" {
		t.Fatalf("patched draft = %+v", got)
	}
	// Terms beyond a year are refused, and so is an empty patch.
	mustJSONDo(t, h, op, "PATCH", "/api/v1/statements/"+d.ID, map[string]any{"payment_terms_days": 400}, 400)
	mustJSONDo(t, h, op, "PATCH", "/api/v1/statements/"+d.ID, map[string]any{}, 400)

	issued := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	number, _ := issued["invoice_number"].(string)
	if number == "" || issued["po_reference"] != "PO-8123" || issued["due_at"] == nil {
		t.Fatalf("issued invoice = %+v", issued)
	}
	if issued["payment_terms_days"] != float64(60) {
		t.Fatalf("terms = %v, want the 60 agreed on the statement", issued["payment_terms_days"])
	}
	if issued["balance"] != float64(1200) {
		t.Fatalf("balance = %v", issued["balance"])
	}
	if len(hook.calls()) != 0 {
		t.Fatalf("an invoiced customer must never reach the billing hook: %v", hook.calls())
	}

	// Sending it. The mail is opt-in here — the operator is recording what
	// they did, not asking for a second copy to go out.
	before := len(mail.msgs)
	sent := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/send", map[string]any{}, 200)
	if sent["status"] != store.StatusSent || sent["sent_at"] == nil {
		t.Fatalf("sent = %+v", sent)
	}
	if len(mail.msgs) != before {
		t.Fatalf("send must not mail unless asked: %d new messages", len(mail.msgs)-before)
	}
	// A sent invoice cannot be cancelled — that is a credit note, not a flip.
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/cancel", map[string]any{"reason": "changed our mind"}, 409)

	// A part payment carries the balance and leaves the invoice open.
	part := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "500.000000", "paid_at": "2026-07-02", "reference": "TRF-77"}, 200)
	stDoc, _ := part["statement"].(map[string]any)
	if stDoc["status"] != store.StatusSent || stDoc["balance"] != float64(700) || stDoc["paid_total"] != float64(500) {
		t.Fatalf("after part payment = %+v", stDoc)
	}
	// More than the balance is refused — judged at the minor unit, so a
	// full baisa over is an overpayment (a millionth over would settle).
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "700.001", "reference": "TRF-OVER"}, 409)
	// So is a nonsense amount or date.
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "0", "reference": "TRF-0"}, 400)
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "1.000000", "paid_at": "last tuesday"}, 400)

	// The rest settles it.
	rest := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "700.000000", "paid_at": "2026-07-20", "reference": "TRF-78"}, 200)
	stDoc, _ = rest["statement"].(map[string]any)
	if stDoc["status"] != store.StatusPaid || stDoc["balance"] != float64(0) {
		t.Fatalf("after settlement = %+v", stDoc)
	}
	// Paid is terminal.
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "1.000000", "reference": "TRF-79"}, 409)
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/cancel", map[string]any{"reason": "too late"}, 409)

	// The payment history is on the document and on its own endpoint.
	doc := mustDo(t, h, op, "GET", "/api/v1/statements/"+d.ID, 200)
	payments, _ := doc["payments"].([]any)
	if len(payments) != 2 {
		t.Fatalf("payments on the statement document = %v", doc["payments"])
	}
	list := mustDo(t, h, op, "GET", "/api/v1/statements/"+d.ID+"/payments", 200)
	if l, _ := list["payments"].([]any); len(l) != 2 {
		t.Fatalf("payments endpoint = %v", list["payments"])
	}

	// Every transition is audited.
	actions := customerAuditActions(t, st, c.ID)
	for _, want := range []string{"statement.update", "statement.issue", "statement.send", "statement.payment", "statement.settlement.requested"} {
		if !hasAction(actions, want) {
			t.Errorf("audit is missing %s; got %v", want, actions)
		}
	}

	// Sending with notify:true does mail the customer, on another invoice.
	d2 := draftFor(t, st, c.ID, "2026-07-01", "10.000000")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", map[string]any{"notify": false}, 200)
	before = len(mail.msgs)
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/send", map[string]any{"notify": true}, 200)
	if len(mail.msgs) != before+1 {
		t.Fatalf("send with notify = %d new messages, want 1", len(mail.msgs)-before)
	}
	// Cancelling an ISSUED invoice is legal; a draft's is too.
	d3 := draftFor(t, st, c.ID, "2026-08-01", "10.000000")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d3.ID+"/issue", map[string]any{"notify": false}, 200)
	cancelled := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d3.ID+"/cancel", map[string]any{"reason": "raised twice"}, 200)
	if cancelled["status"] != store.StatusCancelled || cancelled["cancel_reason"] != "raised twice" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
}

// The four commercial fields and the terms are settable at create and at
// patch; billing_mode is decoded, ignored and still derived correctly.
func TestIntegrationCustomerCommercialOverTheAPI(t *testing.T) {
	h, st, _, _ := setupInvoicingAPI(t)
	op := operatorSession()

	created := mustJSONDo(t, h, op, "POST", "/api/v1/customers", map[string]any{
		"slug": "corp-api", "name": "Corp", "admin_email": "ap@corp-api.example",
		"charging": store.ChargingBilled, "payment_model": store.PaymentModelPostpaid, "payment_method": store.PaymentMethodTransfer,
		"po_reference": "PO-55", "payment_terms_days": 45,
	}, 201)
	if created["charging"] != store.ChargingBilled || created["payment_model"] != store.PaymentModelPostpaid || created["payment_method"] != store.PaymentMethodTransfer {
		t.Fatalf("created customer = %+v", created)
	}
	if created["po_reference"] != "PO-55" || created["payment_terms_days"] != float64(45) {
		t.Fatalf("created terms = %+v", created)
	}
	if created["billing_mode"] != store.BillingModeReal {
		t.Fatalf("derived billing_mode = %v, want real", created["billing_mode"])
	}
	id, _ := created["id"].(string)

	// Moving the same customer onto a gateway.
	patched := mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{
		"payment_model": store.PaymentModelPrepaid, "payment_method": store.PaymentMethodGateway, "gateway_name": store.GatewayStripe, "payment_terms_days": 0}, 200)
	if patched["payment_method"] != store.PaymentMethodGateway || patched["gateway_name"] != store.GatewayStripe || patched["payment_terms_days"] != float64(0) {
		t.Fatalf("patched customer = %+v", patched)
	}
	// A legacy client that still sends billing_mode is IGNORED, not refused,
	// and the derived value keeps reflecting the four fields.
	legacy := mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"billing_mode": "showback"}, 200)
	if legacy["billing_mode"] != store.BillingModeReal || legacy["charging"] != store.ChargingBilled {
		t.Fatalf("a legacy billing_mode patch must change nothing: %+v", legacy)
	}
	// Turning charging off clears what then has no meaning.
	off := mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"charging": store.ChargingInformational}, 200)
	if off["payment_model"] != nil || off["payment_method"] != nil || off["gateway_name"] != nil || off["billing_mode"] != store.BillingModeShowback {
		t.Fatalf("informational customer = %+v", off)
	}
	// Inconsistent combinations are refused, and the message names the field.
	mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"payment_method": "cheque"}, 400)
	mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"payment_model": store.PaymentModelPrepaid}, 400)
	mustJSONDo(t, h, op, "PATCH", "/api/v1/customers/"+id, map[string]any{"payment_terms_days": 400}, 400)
	mustJSONDo(t, h, op, "POST", "/api/v1/customers", map[string]any{"slug": "bad", "name": "Bad", "admin_email": "b@bad.example",
		"charging": store.ChargingBilled, "payment_model": store.PaymentModelPrepaid, "payment_method": store.PaymentMethodGateway}, 400)

	// The invoice prefix is a billing setting; changing it leaves the rule
	// alone and vice versa.
	settings := mustJSONDo(t, h, op, "PUT", "/api/v1/billing-settings", map[string]any{"discount_rule": store.DiscountRuleHighest, "invoice_prefix": "omt"}, 200)
	if settings["invoice_prefix"] != "OMT" || settings["discount_rule"] != store.DiscountRuleHighest {
		t.Fatalf("billing settings = %+v", settings)
	}
	settings = mustJSONDo(t, h, op, "PUT", "/api/v1/billing-settings", map[string]any{"discount_rule": store.DiscountRuleStack}, 200)
	if settings["invoice_prefix"] != "OMT" {
		t.Fatalf("a body without invoice_prefix must not reset it: %+v", settings)
	}
	mustJSONDo(t, h, op, "PUT", "/api/v1/billing-settings", map[string]any{"discount_rule": store.DiscountRuleStack, "invoice_prefix": "way too long"}, 400)
	_ = st
}

// The four new endpoints are operator-only: a customer admin may read what it
// has paid, and change nothing.
func TestInvoicingEndpointsAreOperatorOnly(t *testing.T) {
	h := newAuthzHandler()
	a := "11111111-1111-1111-1111-111111111111"
	admin := &store.Session{Email: "adm@a.example", Role: store.RoleCustomerAdmin, CustomerID: &a}
	for _, c := range []struct {
		method, path string
	}{
		{"PATCH", "/api/v1/statements/x"},
		{"POST", "/api/v1/statements/x/send"},
		{"POST", "/api/v1/statements/x/payments"},
		{"POST", "/api/v1/statements/x/cancel"},
	} {
		if rec := do(t, h, admin, c.method, c.path, `{}`); rec.Code != 403 {
			t.Errorf("%s %s as a customer admin = %d, want 403", c.method, c.path, rec.Code)
		}
		if rec := do(t, h, nil, c.method, c.path, `{}`); rec.Code != 401 {
			t.Errorf("%s %s anonymously = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

// mustJSONDo posts a JSON body with a session and asserts the status.
func mustJSONDo(t *testing.T, h http.Handler, sess *store.Session, method, path string, body any, want int) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, sess, method, path, string(b))
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func mustDo(t *testing.T, h http.Handler, sess *store.Session, method, path string, want int) map[string]any {
	t.Helper()
	rec := do(t, h, sess, method, path, "")
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// The hw307 case (DESIGN.md §8.6): an invoice of 14.856782 OMR part-paid by
// 10.000 leaves 4.856782 owed, which no transfer can carry. The dialog
// prefills 4.857 — the outstanding at the minor unit — and the store must
// take that as the settlement, flip the invoice to paid and report a zero
// balance, never a negative one. Half a baisa or more over is still refused.
func TestIntegrationPaymentSettlesAtTheMinorUnit(t *testing.T) {
	h, st, _, _ := setupInvoicingAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "nizwa-fintech", Name: "Nizwa Fintech", AdminEmail: "ap@nizwa.example",
		Kind: "organization", OrgSlug: "nizwa-fintech",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}})
	if err != nil {
		t.Fatal(err)
	}

	// Settled by the amount the dialog shows.
	d := draftFor(t, st, c.ID, "2026-08-01", "14.856782")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", map[string]any{"notify": false}, 200)
	part := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "10", "paid_at": "2026-09-02", "reference": "TRF-1"}, 200)
	stDoc, _ := part["statement"].(map[string]any)
	if stDoc["status"] != store.StatusIssued || stDoc["balance"] != float64(4.856782) || stDoc["paid_total"] != float64(10) {
		t.Fatalf("after the part payment = %+v", stDoc)
	}
	rest := mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/payments", map[string]any{"amount": "4.857", "paid_at": "2026-09-09", "reference": "TRF-2"}, 200)
	stDoc, _ = rest["statement"].(map[string]any)
	if stDoc["status"] != store.StatusPaid || stDoc["effective_status"] != store.StatusPaid {
		t.Fatalf("4.857 against 4.856782 must settle the invoice: %+v", stDoc)
	}
	if stDoc["balance"] != float64(0) || stDoc["paid_total"] != float64(14.857) {
		t.Fatalf("balance/paid_total after settlement = %v / %v, want 0 / 14.857", stDoc["balance"], stDoc["paid_total"])
	}
	if paidAt, _ := stDoc["paid_at"].(string); !strings.HasPrefix(paidAt, "2026-09-09") {
		t.Fatalf("paid_at = %v, want the day the money arrived", stDoc["paid_at"])
	}
	got, err := st.GetStatement(ctx, store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Balance != "0.000000" || got.Paid != "14.857000" || got.Status != store.StatusPaid {
		t.Fatalf("store reads balance %s paid %s status %s", got.Balance, got.Paid, got.Status)
	}

	// Half a baisa or more over is still an overpayment.
	d2 := draftFor(t, st, c.ID, "2026-07-01", "14.856782")
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/issue", map[string]any{"notify": false}, 200)
	mustJSONDo(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/payments", map[string]any{"amount": "10", "paid_at": "2026-09-02", "reference": "TRF-3"}, 200)
	rec := do(t, h, op, "POST", "/api/v1/statements/"+d2.ID+"/payments", `{"amount":"4.858","paid_at":"2026-09-09","reference":"TRF-4"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "exceeds the outstanding balance of 4.856782") {
		t.Fatalf("4.858 against 4.856782 = %d %s, want 409 naming the outstanding", rec.Code, rec.Body.String())
	}
	open, err := st.GetStatement(ctx, store.OperatorScope, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if open.Status != store.StatusIssued || open.Balance != "4.856782" {
		t.Fatalf("a refused payment must leave the invoice as it was: %s %s", open.Status, open.Balance)
	}
}
