package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/collections"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Customer self-service (DESIGN.md §16) against a real database: a saved
// payment method through the gateway seam, and a dispute that takes an
// invoice out of collections chasing and puts it back.

func gatewayCommercial() store.Commercial {
	return store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodGateway, GatewayName: "omantel"}
}

// customerOwner is a customer-owner session on one customer — the principal
// every self-service surface is written for.
func customerOwner(email, customerID string) *store.Session {
	id := customerID
	return &store.Session{Email: email, Role: store.RoleCustomerAdmin, CustomerID: &id, ExpiresAt: time.Now().Add(time.Hour)}
}

// customerViewer holds metering.read alone: it may read invoices and must
// not be able to save a card or freeze an invoice's collections.
func customerViewer(email, customerID string) *store.Session {
	id := customerID
	return &store.Session{Email: email, Role: store.RoleCustomerViewer, CustomerID: &id, ExpiresAt: time.Now().Add(time.Hour)}
}

// A customer saves a payment method through the gateway seam: the setup is
// started, the completion is confirmed, and what this product keeps is the
// DISPLAY record — brand, last four, expiry — with the gateway's token
// nowhere on the wire and nowhere in the audit trail.
func TestIntegrationPaymentMethodThroughTheGatewaySeam(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "gulf-air", Name: "Gulf Air", AdminEmail: "ap@gulfair.example", Commercial: gatewayCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerOwner("ap@gulfair.example", c.ID)

	// ── start the setup ───────────────────────────────────────────────────
	started := mustJSONDo(t, env.h, owner, "POST", "/api/v1/customers/"+c.ID+"/payment-methods", map[string]any{"label": "Company card"}, 201)
	if started["status"] != store.MethodPending {
		t.Fatalf("a setup that has not been completed is %v, want pending", started["status"])
	}
	if url, _ := started["setup_url"].(string); !strings.HasPrefix(url, "https://pay.omantel.example/setup/") {
		t.Fatalf("the customer was not sent to the gateway's own page: %q", url)
	}
	if started["saved"] != false {
		t.Fatalf("a pending method reports saved = %v", started["saved"])
	}
	methodID, _ := started["id"].(string)
	if methodID == "" {
		t.Fatal("the setup was not recorded")
	}

	// ── the customer returns from the gateway's page ──────────────────────
	confirmed := mustJSONDo(t, env.h, owner, "POST", "/api/v1/customers/"+c.ID+"/payment-methods/"+methodID+"/confirm", map[string]any{}, 200)
	if confirmed["status"] != store.MethodActive || confirmed["saved"] != true {
		t.Fatalf("the confirmed method is %v / saved %v", confirmed["status"], confirmed["saved"])
	}
	for key, want := range map[string]any{"brand": "visa", "last4": "4242", "exp_month": float64(11), "exp_year": float64(2030)} {
		if confirmed[key] != want {
			t.Errorf("%s = %v, want %v — the display triple is what a payer recognises the card by", key, confirmed[key], want)
		}
	}

	// ── the token never leaves the process ────────────────────────────────
	// It IS stored (the gateway seam reads it back); it is on no wire.
	rec := do(t, env.h, owner, "GET", "/api/v1/customers/"+c.ID+"/payment-methods", "")
	if rec.Code != 200 {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "pm_seti_") || strings.Contains(strings.ToLower(body), `"token"`) {
		t.Fatalf("the gateway's token reached the wire: %s", body)
	}
	// The operator reads the same document and is told no more.
	opBody := do(t, env.h, op, "GET", "/api/v1/customers/"+c.ID+"/payment-methods", "").Body.String()
	if strings.Contains(opBody, "pm_seti_") {
		t.Fatalf("an operator was shown the gateway token: %s", opBody)
	}
	if !strings.Contains(opBody, "4242") {
		t.Fatalf("an operator cannot see that a method exists: %s", opBody)
	}

	// ── nor the audit trail ───────────────────────────────────────────────
	entries, err := env.st.ListAudit(ctx, store.OperatorScope, c.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, e := range entries {
		actions = append(actions, e.Action)
		if strings.Contains(string(e.Details), "pm_seti_") {
			t.Fatalf("the audit entry %s carries the gateway token: %s", e.Action, e.Details)
		}
	}
	for _, want := range []string{"payment_method.setup", "payment_method.confirm"} {
		if !hasAction(actions, want) {
			t.Errorf("%s was not audited; entries: %v", want, actions)
		}
	}

	// ── the store holds the token, so a later charge can name it ──────────
	held, err := env.st.GetPaymentMethod(ctx, store.OperatorScope, methodID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Token != "pm_seti_gulf-air" {
		t.Fatalf("the gateway's token was not kept: %q", held.Token)
	}

	// ── the customer removes it ───────────────────────────────────────────
	removed := mustJSONDo(t, env.h, owner, "DELETE", "/api/v1/customers/"+c.ID+"/payment-methods/"+methodID, map[string]any{}, 200)
	if removed["status"] != store.MethodRemoved {
		t.Fatalf("remove left the method %v", removed["status"])
	}
	gone, err := env.st.GetPaymentMethod(ctx, store.OperatorScope, methodID)
	if err != nil {
		t.Fatal(err)
	}
	if gone.Token != "" {
		t.Fatalf("a removed method still carries a chargeable token: %q", gone.Token)
	}
	after := do(t, env.h, owner, "GET", "/api/v1/customers/"+c.ID+"/payment-methods", "").Body.String()
	if strings.Contains(after, methodID) {
		t.Fatalf("a removed method is still on file: %s", after)
	}
}

// A customer whose method is a TRANSFER has no instrument to keep, and is
// told so rather than left with a method that cannot exist.
func TestIntegrationPaymentMethodRefusedForTransfer(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "corp-transfer", Name: "Corp", AdminEmail: "ap@corp.example", Commercial: postpaidTransferCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerOwner("ap@corp.example", c.ID)
	rec := do(t, env.h, owner, "POST", "/api/v1/customers/"+c.ID+"/payment-methods", `{}`)
	if rec.Code != 409 {
		t.Fatalf("a transfer customer saving a card = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// Every self-service write refuses the principals that must not hold it, and
// a FOREIGN customer's id answers 404 — never a filtered list, never a 403
// that would confirm the id exists.
func TestIntegrationSelfServiceScopeAndPermissions(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	mine, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "mine", Name: "Mine", AdminEmail: "a@mine.example", Commercial: gatewayCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "theirs", Name: "Theirs", AdminEmail: "a@theirs.example", Commercial: gatewayCommercial()})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerOwner("a@mine.example", mine.ID)
	viewer := customerViewer("v@mine.example", mine.ID)

	// A method on my own account, then the foreign reads of it.
	started := mustJSONDo(t, env.h, owner, "POST", "/api/v1/customers/"+mine.ID+"/payment-methods", map[string]any{}, 201)
	methodID := started["id"].(string)
	theirOwner := customerOwner("a@theirs.example", theirs.ID)

	// Two issued invoices, one per customer.
	myInvoice := draftFor(t, env.st, mine.ID, "2026-05-01", "100.000000")
	theirInvoice := draftFor(t, env.st, theirs.ID, "2026-05-01", "200.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+myInvoice.ID+"/issue", map[string]any{"notify": false}, 200)
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+theirInvoice.ID+"/issue", map[string]any{"notify": false}, 200)

	cases := []struct {
		name   string
		sess   *store.Session
		method string
		path   string
		body   string
		want   int
	}{
		{"a viewer cannot save a card", viewer, "POST", "/api/v1/customers/" + mine.ID + "/payment-methods", `{}`, 403},
		{"a viewer cannot remove one", viewer, "DELETE", "/api/v1/customers/" + mine.ID + "/payment-methods/" + methodID, ``, 403},
		{"a viewer reads what is on file", viewer, "GET", "/api/v1/customers/" + mine.ID + "/payment-methods", ``, 200},
		{"another customer's methods are not found", theirOwner, "GET", "/api/v1/customers/" + mine.ID + "/payment-methods", ``, 404},
		{"another customer's method by id is not found", theirOwner, "GET", "/api/v1/payment-methods/" + methodID, ``, 404},
		{"another customer cannot remove my method", theirOwner, "DELETE", "/api/v1/payment-methods/" + methodID, ``, 404},
		{"anonymous saves nothing", nil, "POST", "/api/v1/customers/" + mine.ID + "/payment-methods", `{}`, 401},

		{"a viewer cannot dispute", viewer, "POST", "/api/v1/statements/" + myInvoice.ID + "/disputes", `{"reason":"wrong"}`, 403},
		{"a dispute needs a reason", owner, "POST", "/api/v1/statements/" + myInvoice.ID + "/disputes", `{"reason":"  "}`, 400},
		{"another customer's invoice is not found", owner, "POST", "/api/v1/statements/" + theirInvoice.ID + "/disputes", `{"reason":"not mine"}`, 404},
		{"another customer's disputes are not listed", owner, "GET", "/api/v1/statements/" + theirInvoice.ID + "/disputes", ``, 404},
		{"anonymous disputes nothing", nil, "POST", "/api/v1/statements/" + myInvoice.ID + "/disputes", `{"reason":"x"}`, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, env.h, tc.sess, tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// A customer may never resolve its own dispute: that is the operator's.
	opened := mustJSONDo(t, env.h, owner, "POST", "/api/v1/statements/"+myInvoice.ID+"/disputes", map[string]any{"reason": "duplicate line"}, 201)
	disputeID := opened["id"].(string)
	if rec := do(t, env.h, owner, "POST", "/api/v1/disputes/"+disputeID+"/resolve", `{"outcome":"upheld"}`); rec.Code != 403 {
		t.Fatalf("a customer resolving its own dispute = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, env.h, theirOwner, "GET", "/api/v1/disputes/"+disputeID, ""); rec.Code != 404 {
		t.Fatalf("another customer reading my dispute = %d, want 404", rec.Code)
	}
	// A second dispute on the same invoice is refused while one is open.
	if rec := do(t, env.h, owner, "POST", "/api/v1/statements/"+myInvoice.ID+"/disputes", `{"reason":"again"}`); rec.Code != 409 {
		t.Fatalf("a second open dispute = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// An outcome that is neither upheld nor rejected is refused.
	if rec := do(t, env.h, op, "POST", "/api/v1/disputes/"+disputeID+"/resolve", `{"outcome":"maybe"}`); rec.Code != 400 {
		t.Fatalf("an invented outcome = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// A dispute takes the invoice out of collections chasing, and REJECTING it
// puts it straight back — the aging report and the evaluator both read the
// one flag, and both say so in their output.
func TestIntegrationDisputeStopsAndResumesCollections(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "muscat-co", Name: "Muscat Co", AdminEmail: "ap@muscat.example",
		Commercial: postpaidTransferCommercial(), PaymentTermsDays: intp(0)})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerOwner("ap@muscat.example", c.ID)
	inv := draftFor(t, env.st, c.ID, "2026-03-01", "400.000000")
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+inv.ID+"/issue", map[string]any{"notify": false}, 200)

	// 60 days past a due date of the issue day: comfortably chaseable.
	asOf := time.Now().UTC().AddDate(0, 0, 60).Format("2006-01-02")

	before := agingOf(t, env.h, op, asOf)
	if before.Overdue == "0.000000" || before.Overdue == "" {
		t.Fatalf("the invoice was not overdue before the dispute: %+v", before)
	}
	chasedBefore := before.Overdue

	// ── the customer disputes the whole invoice ───────────────────────────
	opened := mustJSONDo(t, env.h, owner, "POST", "/api/v1/statements/"+inv.ID+"/disputes", map[string]any{"reason": "the March lines were not ours"}, 201)
	if opened["status"] != store.DisputeOpen {
		t.Fatalf("the dispute opened as %v", opened["status"])
	}
	if opened["amount"] != 400.0 {
		t.Fatalf("the disputed amount = %v, want the whole outstanding 400", opened["amount"])
	}
	// The statement itself carries the flag and the reason.
	st := mustDo(t, env.h, owner, "GET", "/api/v1/statements/"+inv.ID, 200)
	if st["disputed_at"] == nil || st["dispute_reason"] != "the March lines were not ours" {
		t.Fatalf("the statement does not carry the dispute: disputed_at=%v reason=%v", st["disputed_at"], st["dispute_reason"])
	}

	// ── the aging report: listed, named, and OUT of the overdue figure ────
	during := agingOf(t, env.h, op, asOf)
	if during.Overdue != "0.000000" {
		t.Fatalf("a disputed invoice is still being chased: overdue = %s (was %s)", during.Overdue, chasedBefore)
	}
	if during.Disputed != chasedBefore {
		t.Fatalf("the disputed total = %s, want the %s that left the overdue figure", during.Disputed, chasedBefore)
	}
	if during.Total != before.Total {
		t.Fatalf("a dispute moved the balance: total %s → %s; it must stay owed", before.Total, during.Total)
	}
	if during.DisputedInvoices != 1 {
		t.Fatalf("the report counted %d disputed invoices", during.DisputedInvoices)
	}
	found := false
	for _, row := range during.Invoices {
		if row.StatementID == inv.ID {
			found = true
			if !row.Disputed || row.DisputeReason == "" {
				t.Fatalf("the invoice is listed without saying it is disputed: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("a disputed invoice vanished from the aging report; the operator must still see it")
	}

	// ── the evaluator passes over it, and says so ─────────────────────────
	rep, err := (&collections.Evaluator{Store: env.st, Owns: func(context.Context) (bool, error) { return true, nil }}).
		RunAt(ctx, time.Now().UTC().AddDate(0, 0, 60))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Disputed != 1 {
		t.Fatalf("the evaluator reported %d disputed invoices, want 1: %+v", rep.Disputed, rep)
	}
	if rep.Reminders != 0 || rep.Escalations != 0 {
		t.Fatalf("a disputed invoice was chased: %d reminders, %d escalations", rep.Reminders, rep.Escalations)
	}

	// ── rejected: collections resume ──────────────────────────────────────
	resolved := mustJSONDo(t, env.h, op, "POST", "/api/v1/disputes/"+opened["id"].(string)+"/resolve",
		map[string]any{"outcome": store.DisputeRejected, "note": "the lines are the customer's own"}, 200)
	if resolved["status"] != store.DisputeRejected || resolved["credit_note_id"] != nil {
		t.Fatalf("a rejected dispute credited something: %v", resolved)
	}
	after := agingOf(t, env.h, op, asOf)
	if after.Overdue != chasedBefore {
		t.Fatalf("collections did not resume: overdue = %s, want %s", after.Overdue, chasedBefore)
	}
	if after.Disputed != "0.000000" {
		t.Fatalf("the dispute is still counted after rejection: %s", after.Disputed)
	}
	rep2, err := (&collections.Evaluator{Store: env.st, Owns: func(context.Context) (bool, error) { return true, nil }}).
		RunAt(ctx, time.Now().UTC().AddDate(0, 0, 60))
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Disputed != 0 || rep2.Reminders == 0 {
		t.Fatalf("the evaluator did not resume chasing: %+v", rep2)
	}

	actions := customerAuditActions(t, env.st, c.ID)
	for _, want := range []string{"dispute.open", "dispute.resolve"} {
		if !hasAction(actions, want) {
			t.Errorf("%s was not audited; entries: %v", want, actions)
		}
	}
}

// An UPHELD dispute issues a credit note for the disputed amount through the
// existing credit-note machinery, and the invoice's balance moves by exactly
// that — not a penny more, not a second mechanism.
func TestIntegrationDisputeUpheldCreditsExactlyTheDisputedAmount(t *testing.T) {
	env := setupAccountAPI(t)
	ctx := context.Background()
	op := operatorSession()
	c, err := env.st.CreateCustomer(ctx, store.CustomerInput{Slug: "salalah-llc", Name: "Salalah LLC", AdminEmail: "ap@salalah.example",
		Commercial: postpaidTransferCommercial(), PaymentTermsDays: intp(30)})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerOwner("ap@salalah.example", c.ID)

	// Two rated lines of 300 and 100: disputing the 100 line disputes a
	// quarter of the invoice.
	from, _ := time.Parse("2006-01-02", "2026-04-01")
	draft, err := env.st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID: c.ID, PeriodStart: from, PeriodEnd: from.AddDate(0, 1, -1), Currency: "OMR",
		Subtotal: "400.000000", TaxRate: "0", Tax: "0", Total: "400.000000",
		Lines: []store.RatedLine{
			{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "1", UnitPrice: "300.000000", Amount: "300.000000", ResourceCount: 1},
			{SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: "1", UnitPrice: "100.000000", Amount: "100.000000", ResourceCount: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustJSONDo(t, env.h, op, "POST", "/api/v1/statements/"+draft.ID+"/issue", map[string]any{"notify": false}, 200)

	issued, err := env.st.GetStatement(ctx, store.OperatorScope, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	var disputedLine string
	for _, l := range issued.Lines {
		if l.SKU == "evs.ssd.gb" {
			disputedLine = strconv.FormatInt(l.ID, 10)
		}
	}
	if disputedLine == "" {
		t.Fatal("the invoice has no storage line to dispute")
	}
	balanceBefore := issued.Balance

	opened := mustJSONDo(t, env.h, owner, "POST", "/api/v1/statements/"+draft.ID+"/disputes",
		map[string]any{"reason": "the storage line is not ours", "lines": []string{disputedLine}}, 201)
	if opened["amount"] != 100.0 {
		t.Fatalf("the named line's share of the invoice = %v, want 100", opened["amount"])
	}

	resolved := mustJSONDo(t, env.h, op, "POST", "/api/v1/disputes/"+opened["id"].(string)+"/resolve",
		map[string]any{"outcome": store.DisputeUpheld, "note": "agreed, the volume belongs to another account"}, 200)
	noteID, _ := resolved["credit_note_id"].(string)
	if noteID == "" {
		t.Fatal("an upheld dispute issued no credit note")
	}
	note, err := env.st.GetCreditNote(ctx, store.OperatorScope, noteID)
	if err != nil {
		t.Fatal(err)
	}
	if string(note.Total) != "100.000000" {
		t.Fatalf("the credit note is for %s, the dispute was for 100.000000", note.Total)
	}
	if note.Number == "" {
		t.Fatal("the dispute's credit note took no number; it is not the product's credit note")
	}

	settled, err := env.st.GetStatement(ctx, store.OperatorScope, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved := decimalOf(t, balanceBefore) - decimalOf(t, settled.Balance)
	if moved != 100 {
		t.Fatalf("the balance moved by %v (%s → %s); an upheld dispute moves it by exactly the disputed amount", moved, balanceBefore, settled.Balance)
	}
	if settled.DisputedAt != nil {
		t.Fatal("the flag survived the resolution; collections would never resume")
	}
	if string(settled.Credited) != "100.000000" {
		t.Fatalf("the invoice records %s credited", settled.Credited)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// agingOf reads GET /collections/aging at a date, as the operator's
// Collections page does.
func agingOf(t *testing.T, h http.Handler, sess *store.Session, asOf string) collections.AgingReport {
	t.Helper()
	rec := do(t, h, sess, "GET", "/api/v1/collections/aging?as_of="+asOf, "")
	if rec.Code != 200 {
		t.Fatalf("aging = %d: %s", rec.Code, rec.Body.String())
	}
	var rep collections.AgingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode aging: %v: %s", err, rec.Body.String())
	}
	return rep
}

func decimalOf(t *testing.T, d store.Decimal) float64 {
	t.Helper()
	var f float64
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(d))), &f); err != nil {
		t.Fatalf("%q is not a decimal: %v", d, err)
	}
	return f
}
