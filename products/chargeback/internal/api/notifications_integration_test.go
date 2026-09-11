package api

import (
	"context"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Notification management (DESIGN.md §21) end to end: the catalogue, the
// preference rule and its refusals, the delivery log, the scope on each
// route — and the three sends this package moved onto the catalogue still
// going out, now recorded.

func notifySetup(t *testing.T) (*client, *client, *store.Store, *recMail, store.Customer) {
	t.Helper()
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	c := op.mustJSON("POST", "/api/v1/customers", map[string]any{
		"slug": "acme", "name": "Acme Org", "admin_email": "ap@acme.example",
	}, 201)
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatalf("no customer id: %v", c)
	}
	cust, err := st.GetCustomer(context.Background(), store.OperatorScope, id)
	if err != nil {
		t.Fatal(err)
	}
	owner := &client{t: t, h: h}
	owner.signIn("ap@acme.example", mail)
	return op, owner, st, mail, cust
}

func TestIntegrationNotificationCatalogueIsServedWithItsChannelsAndTemplates(t *testing.T) {
	op, owner, _, _, _ := notifySetup(t)

	doc := op.must("GET", "/api/v1/notifications/events", 200)
	events, _ := doc["events"].([]any)
	if len(events) != len(notify.Events()) {
		t.Fatalf("events = %d, want %d", len(events), len(notify.Events()))
	}
	keys := map[string]bool{}
	mandatory := map[string]bool{}
	for _, e := range events {
		m, _ := e.(map[string]any)
		key, _ := m["key"].(string)
		keys[key] = true
		if on, _ := m["mandatory"].(bool); on {
			mandatory[key] = true
		}
	}
	for _, want := range []string{notify.EventAuthPIN, notify.EventCustomerInvite, notify.EventStatementIssued,
		notify.EventCollectionsReminder, notify.EventCollectionsEscalation, notify.EventAccountLowBalance,
		notify.EventBudgetThreshold, notify.EventReportScheduled} {
		if !keys[want] {
			t.Errorf("the catalogue does not carry %s", want)
		}
	}
	for _, want := range []string{notify.EventStatementIssued, notify.EventCollectionsReminder, notify.EventCollectionsEscalation} {
		if !mandatory[want] {
			t.Errorf("%s must be marked mandatory to the console", want)
		}
	}

	// The channels, with SMS declared and honestly unavailable — the
	// console renders that reason, and it must reach it.
	channels, _ := doc["channels"].([]any)
	var sawSMS bool
	for _, c := range channels {
		m, _ := c.(map[string]any)
		if m["name"] != notify.ChannelSMS {
			continue
		}
		sawSMS = true
		if avail, _ := m["available"].(bool); avail {
			t.Error("sms must not report itself available: this build has no transport for it")
		}
		reason, _ := m["reason"].(string)
		if !strings.Contains(reason, "Omantel") {
			t.Errorf("the sms reason must say what is missing: %q", reason)
		}
	}
	if !sawSMS {
		t.Error("sms must be listed as a declared channel")
	}

	// Every event has a template the console can show.
	templates, _ := doc["templates"].([]any)
	if len(templates) < len(notify.Events()) {
		t.Fatalf("templates = %d, want at least one per event (%d)", len(templates), len(notify.Events()))
	}

	// A CUSTOMER principal is not an operator: the Sovereign catalogue and
	// the Sovereign delivery log are both refused.
	if rec, _ := owner.do("GET", "/api/v1/notifications/events", "", nil); rec.Code != 403 {
		t.Fatalf("customer GET events = %d, want 403", rec.Code)
	}
	if rec, _ := owner.do("GET", "/api/v1/notifications/deliveries", "", nil); rec.Code != 403 {
		t.Fatalf("customer GET deliveries = %d, want 403", rec.Code)
	}
}

func TestIntegrationPreferencesResolveAndDefaultToSending(t *testing.T) {
	op, owner, _, _, cust := notifySetup(t)

	// With NO rows, every event resolves to the catalogue default — which
	// is to send, because every one of them was being sent unconditionally
	// before §21 existed.
	doc := op.must("GET", "/api/v1/notifications/preferences", 200)
	eff, _ := doc["effective"].([]any)
	if len(eff) != len(notify.Events()) {
		t.Fatalf("effective = %d rows, want one per event", len(eff))
	}
	for _, e := range eff {
		m, _ := e.(map[string]any)
		if on, _ := m["enabled"].(bool); !on {
			t.Errorf("%v is off with no preference set; an unset preference must reproduce what the product already sent", m["event"])
		}
		if src, _ := m["source"].(string); src != "catalogue default" {
			t.Errorf("%v source = %q", m["event"], src)
		}
	}

	// Switch the low-balance alert off for this customer.
	op.mustJSON("PUT", "/api/v1/notifications/preferences", map[string]any{
		"event": notify.EventAccountLowBalance, "customer_id": cust.ID, "enabled": false,
	}, 200)

	got := op.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	if !effectiveSays(t, got, notify.EventAccountLowBalance, false) {
		t.Fatalf("the customer must now resolve low_balance to off: %v", got["effective"])
	}
	// And the customer sees it on its own tab.
	mine := owner.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	if !effectiveSays(t, mine, notify.EventAccountLowBalance, false) {
		t.Fatalf("the customer's own view = %v", mine["effective"])
	}

	// A customer OWNER may set its own preference back on.
	owner.mustJSON("PUT", "/api/v1/customers/"+cust.ID+"/notifications/preferences", map[string]any{
		"event": notify.EventAccountLowBalance, "enabled": true,
	}, 200)
	mine = owner.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	if !effectiveSays(t, mine, notify.EventAccountLowBalance, true) {
		t.Fatalf("the customer's own switch did not take: %v", mine["effective"])
	}

	// Deleting the row returns the scope to the catalogue default.
	op.must("DELETE", "/api/v1/notifications/preferences/"+notify.EventAccountLowBalance+"?customer_id="+cust.ID, 204)
	got = op.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	if !effectiveSays(t, got, notify.EventAccountLowBalance, true) {
		t.Fatalf("after delete = %v", got["effective"])
	}
	// An event that is not in the catalogue is a typo, not a setting.
	op.must("DELETE", "/api/v1/notifications/preferences/not.an.event", 404)
	op.mustJSON("PUT", "/api/v1/notifications/preferences", map[string]any{"event": "not.an.event", "enabled": true}, 400)
	op.mustJSON("PUT", "/api/v1/notifications/preferences", map[string]any{"event": notify.EventAccountLowBalance, "channels": []string{"carrier-pigeon"}}, 400)
}

// THE MANDATORY GUARANTEE, over HTTP: neither an operator nor the customer
// itself can switch off an invoice or a dunning notice, and neither can move
// one onto a channel that cannot carry it.
func TestIntegrationAMandatoryNoticeCannotBeSwitchedOffOverTheAPI(t *testing.T) {
	op, owner, st, _, cust := notifySetup(t)
	ctx := context.Background()

	for _, key := range []string{notify.EventStatementIssued, notify.EventCollectionsReminder, notify.EventCollectionsEscalation} {
		body := op.mustJSON("PUT", "/api/v1/notifications/preferences", map[string]any{
			"event": key, "customer_id": cust.ID, "enabled": false,
		}, 400)
		if msg, _ := body["error"].(string); !strings.Contains(msg, "mandatory") {
			t.Errorf("%s refusal = %q, want it to say why", key, msg)
		}
		// The customer's own route refuses it too.
		owner.mustJSON("PUT", "/api/v1/customers/"+cust.ID+"/notifications/preferences", map[string]any{
			"event": key, "enabled": false,
		}, 400)
		// And replacing its channel is refused the same way.
		owner.mustJSON("PUT", "/api/v1/customers/"+cust.ID+"/notifications/preferences", map[string]any{
			"event": key, "enabled": true, "channels": []string{notify.ChannelSMS},
		}, 400)
	}

	// Nothing was stored, so nothing resolves to off.
	doc := op.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	for _, key := range []string{notify.EventStatementIssued, notify.EventCollectionsReminder, notify.EventCollectionsEscalation} {
		if !effectiveSays(t, doc, key, true) {
			t.Fatalf("%s resolved to off: %v", key, doc["effective"])
		}
	}

	// The last defence: a row written UNDER the API — an import, a restored
	// backup, a hand-written UPDATE — is ignored by the resolver, which is
	// what actually decides whether the customer is told it owes money.
	if _, err := st.PutNotificationPreference(ctx, store.NotificationPreference{
		Event: notify.EventStatementIssued, CustomerID: &cust.ID, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	doc = op.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/preferences", 200)
	if !effectiveSays(t, doc, notify.EventStatementIssued, true) {
		t.Fatalf("a disabling row that bypassed the API must still be ignored: %v", doc["effective"])
	}
	for _, e := range effectiveRows(t, doc) {
		if e["event"] != notify.EventStatementIssued {
			continue
		}
		if forced, _ := e["forced"].(bool); !forced {
			t.Fatalf("the override must be reported to the console: %v", e)
		}
		if reason, _ := e["forced_reason"].(string); !strings.Contains(reason, "cannot be switched off") {
			t.Fatalf("forced_reason = %q", reason)
		}
	}
}

// The sends this refactor MOVED still happen, and are now recorded. The PIN
// and the invite are both mandatory catalogue events; each one's mail is
// captured by the fake sender AND lands in the delivery log.
func TestIntegrationMovedSendsStillGoOutAndAreRecorded(t *testing.T) {
	op, _, _, mail, cust := notifySetup(t)

	// The sign-in code: notifySetup already signed two principals in
	// through POST /auth/pin/request, which is the auth.pin event.
	log := op.must("GET", "/api/v1/notifications/deliveries", 200)
	byEvent := deliveriesByEvent(t, log)
	if byEvent[notify.EventAuthPIN] == 0 {
		t.Fatalf("the sign-in code did not reach the delivery log: %v", log["deliveries"])
	}

	// The activation invite.
	before := len(mail.msgs)
	inv := op.mustJSON("POST", "/api/v1/customers/"+cust.ID+"/invite", map[string]any{}, 201)
	if url, _ := inv["invite_url"].(string); !strings.Contains(url, "/activate/") {
		t.Fatalf("invite = %v", inv)
	}
	if len(mail.msgs) != before+1 {
		t.Fatalf("the invite mail did not go out: %d new messages", len(mail.msgs)-before)
	}
	last := mail.last(t)
	if !strings.Contains(last, "Activate your chargeback account") || !strings.Contains(last, "Hello Acme Org,") {
		t.Fatalf("the invite mail changed: %q", last)
	}
	log = op.must("GET", "/api/v1/notifications/deliveries?event="+notify.EventCustomerInvite, 200)
	rows := deliveryRows(t, log)
	if len(rows) != 1 {
		t.Fatalf("invite deliveries = %d, want 1", len(rows))
	}
	if rows[0]["status"] != store.NotifyStatusSent || rows[0]["channel"] != notify.ChannelEmail {
		t.Fatalf("invite delivery = %v", rows[0])
	}
	if rows[0]["recipient"] != "ap@acme.example" || rows[0]["subject"] != "Activate your chargeback account" {
		t.Fatalf("invite delivery = %v", rows[0])
	}
	if id, _ := rows[0]["customer_id"].(string); id != cust.ID {
		t.Fatalf("the invite delivery must be scoped to the customer: %v", rows[0])
	}

	// The tallies the console's tiles read.
	stats, _ := log["stats"].([]any)
	if len(stats) == 0 {
		t.Fatal("no delivery stats")
	}
}

// A customer reads its OWN deliveries and nobody else's — and never a
// delivery that belongs to no customer, such as an operator's sign-in code.
func TestIntegrationACustomerReadsOnlyItsOwnDeliveries(t *testing.T) {
	op, owner, st, _, cust := notifySetup(t)
	ctx := context.Background()

	other := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "beta", "name": "Beta", "admin_email": "ap@beta.example"}, 201)
	otherID, _ := other["id"].(string)

	for _, row := range []struct {
		event string
		cust  *string
		to    string
	}{
		{notify.EventStatementIssued, &cust.ID, "ap@acme.example"},
		{notify.EventStatementIssued, &otherID, "ap@beta.example"},
		{notify.EventAuthPIN, nil, "ops@nc.example"},
	} {
		if _, err := st.RecordNotificationDelivery(ctx, store.NotificationDeliveryInput{
			Event: row.event, CustomerID: row.cust, Channel: notify.ChannelEmail, Recipient: row.to,
			Attempt: 1, Status: store.NotifyStatusSent, Subject: "S",
		}); err != nil {
			t.Fatal(err)
		}
	}

	mine := owner.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/deliveries", 200)
	for _, r := range deliveryRows(t, mine) {
		if id, _ := r["customer_id"].(string); id != cust.ID {
			t.Fatalf("the customer's log leaked %v", r)
		}
	}
	// Asking for another customer's log by id is refused as not found —
	// ids of other customers are never confirmed.
	if rec, _ := owner.do("GET", "/api/v1/customers/"+otherID+"/notifications/deliveries", "", nil); rec.Code != 404 {
		t.Fatalf("cross-customer read = %d, want 404", rec.Code)
	}
	// And the query parameter cannot redirect it: the PATH pins the
	// customer, so naming another one in the query still reads this
	// customer's own rows and never the other's.
	widened := owner.must("GET", "/api/v1/customers/"+cust.ID+"/notifications/deliveries?customer_id="+otherID, 200)
	rows := deliveryRows(t, widened)
	if len(rows) == 0 {
		t.Fatalf("the path customer's own rows should still be returned: %v", widened["deliveries"])
	}
	for _, r := range rows {
		if id, _ := r["customer_id"].(string); id != cust.ID {
			t.Fatalf("the query parameter reached another customer: %v", r)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func effectiveRows(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, _ := doc["effective"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

func effectiveSays(t *testing.T, doc map[string]any, event string, enabled bool) bool {
	t.Helper()
	for _, m := range effectiveRows(t, doc) {
		if m["event"] != event {
			continue
		}
		on, _ := m["enabled"].(bool)
		return on == enabled
	}
	t.Fatalf("no effective row for %s", event)
	return false
}

func deliveryRows(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, _ := doc["deliveries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

func deliveriesByEvent(t *testing.T, doc map[string]any) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, m := range deliveryRows(t, doc) {
		key, _ := m["event"].(string)
		out[key]++
	}
	return out
}
