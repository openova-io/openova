package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Statement mail (#6867 follow-up): issuing a draft mails the customer's
// admin_email and every customer_users admin once — on the draft → issued
// transition only — unless the body says notify:false.
func TestIntegrationStatementIssueNotifiesCustomerOnce(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "Admin@Acme.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCustomerUser(ctx, c.ID, "second@acme.example", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCustomerUser(ctx, c.ID, "viewer@acme.example", "viewer"); err != nil {
		t.Fatal(err)
	}
	draft := func(y int, m time.Month) store.Statement {
		from := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		st1, err := st.WriteDraftStatement(ctx, store.StatementDraft{
			CustomerID: c.ID, PeriodStart: from, PeriodEnd: from.AddDate(0, 1, -1), Currency: "OMR",
			Subtotal: "850.000000", TaxRate: "0.0500", Tax: "42.500000", Total: "892.500000", Discount: "150.000000",
			Lines: []store.RatedLine{
				{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "744", UnitPrice: "1", Amount: "744.000000", ResourceCount: 1},
				{SKU: "eip", Unit: "hour", Quantity: "744", UnitPrice: "0.1", Amount: "106.000000", ResourceCount: 2},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return st1
	}
	aug := draft(2026, time.August)

	// Issue with no body: notify defaults to true → the two admins, not the viewer.
	before := len(mail.msgs)
	got := op.mustJSON("POST", "/api/v1/statements/"+aug.ID+"/issue", nil, 200)
	if got["status"] != "issued" || got["id"] != aug.ID {
		t.Fatalf("issue = %+v", got)
	}
	if len(mail.msgs) != before+2 {
		t.Fatalf("mails after issue = %d, want 2", len(mail.msgs)-before)
	}
	var to []string
	for _, m := range mail.msgs[before:] {
		parts := strings.SplitN(m, "|", 3)
		to = append(to, parts[0])
		if !strings.Contains(parts[1], "Statement for Acme — August 2026: 892.500 OMR") {
			t.Fatalf("subject = %q", parts[1])
		}
		flat := strings.Join(strings.Fields(parts[2]), " ")
		for _, want := range []string{"August 2026", "List subtotal 1000.000 OMR", "Discounts -150.000 OMR", "Net subtotal 850.000 OMR", "Tax (5.0%) 42.500 OMR", "TOTAL 892.500 OMR", "ecs.s6.large.2", "https://billing.t99.omani.works/statements/" + aug.ID} {
			if !strings.Contains(flat, want) {
				t.Fatalf("mail lacks %q:\n%s", want, parts[2])
			}
		}
	}
	if strings.Join(to, ",") != "admin@acme.example,second@acme.example" {
		t.Fatalf("recipients = %v", to)
	}

	// Re-issue: idempotent, no second mail, still 200.
	op.mustJSON("POST", "/api/v1/statements/"+aug.ID+"/issue", nil, 200)
	op.mustJSON("POST", "/api/v1/statements/"+aug.ID+"/issue", map[string]any{"notify": true}, 200)
	if len(mail.msgs) != before+2 {
		t.Fatalf("re-issue mailed again: %d", len(mail.msgs)-before)
	}

	// notify:false on a fresh draft: issued, nothing mailed.
	sep := draft(2026, time.September)
	op.mustJSON("POST", "/api/v1/statements/"+sep.ID+"/issue", map[string]any{"notify": false}, 200)
	if len(mail.msgs) != before+2 {
		t.Fatalf("notify:false mailed: %d", len(mail.msgs)-before)
	}
	if s2, _ := st.GetStatement(ctx, store.OperatorScope, sep.ID); s2.Status != "issued" {
		t.Fatalf("notify:false did not issue: %s", s2.Status)
	}
	// Unknown fields are refused, as everywhere.
	if rec, _ := op.json("POST", "/api/v1/statements/"+sep.ID+"/issue", map[string]any{"bogus": 1}); rec.Code != 400 {
		t.Fatalf("bad body = %d", rec.Code)
	}
	op.must("POST", "/api/v1/statements/00000000-0000-0000-0000-000000000000/issue", 404)

	// Audit: exactly one statement.notified, on the August statement, with
	// both recipients; the September issue recorded notify=false.
	notified, issues := 0, 0
	for _, e := range op.must("GET", "/api/v1/customers/"+c.ID+"/audit", 200)["entries"].([]any) {
		m := e.(map[string]any)
		d, _ := m["details"].(map[string]any)
		switch m["action"] {
		case "statement.notified":
			notified++
			if d["statement_id"] != aug.ID || len(d["recipients"].([]any)) != 2 {
				t.Fatalf("notified details = %+v", d)
			}
		case "statement.issue":
			issues++
			if d["statement_id"] == sep.ID && d["notify"] != false {
				t.Fatalf("september issue audit = %+v", d)
			}
		}
	}
	if notified != 1 || issues != 4 {
		t.Fatalf("audit: notified=%d issues=%d", notified, issues)
	}
}
