package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Budgets (#6867 §3.5) end to end: CRUD, validation, scope, status math
// against a seeded ledger, the summary block, and once-only alerting.

// budgetNow is "today" for these tests: the 8th, so 2026-09-01..07 are the
// complete days of the current month and a forecast exists.
var budgetNow = time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

func setupBudgetAPI(t *testing.T) (http.Handler, *store.Store, *recMail) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	mail := &recMail{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := New(Deps{
		Store:    st,
		Keys:     keys,
		Mail:     mail,
		Verifier: &fakeVerifier{},
		Config:   config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}},
		Metrics:  metrics.New(),
		Now:      func() time.Time { return budgetNow },
		Version:  "test",
	})
	return h, st, mail
}

type budgetSeed struct {
	a, b store.Customer
}

// seedBudgetLedger mirrors the store package's seedLedger: customer A runs
// one ECS (0.5/h) + 100 GB EVS (0.1/h) every hour of 2026-09-01..07 →
// 14.4/day, 100.8 for the seven days; B one EIP (0.02/h) → 3.36. August
// 25..31 A ran the ECS 12 h/day → 42 last month.
func seedBudgetLedger(t *testing.T, st *store.Store) budgetSeed {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02"},
	}, true); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, b.ID, "huawei-project", "me-east-1", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time) {
		lb, _ := json.Marshal(map[string]any{"name": res, "status": "ACTIVE"})
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	for d := 1; d <= 7; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at)
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at)
			rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at)
		}
	}
	for d := 25; d <= 31; d++ {
		for h := 0; h < 12; h++ {
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, day(2026, 8, d).Add(time.Duration(h)*time.Hour))
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return budgetSeed{a: a, b: b}
}

// customerClient returns a client signed in as a customer principal.
func customerClient(t *testing.T, h http.Handler, st *store.Store, email, role, customerID string) *client {
	t.Helper()
	sess, err := st.CreateSession(context.Background(), email, role, &customerID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, h: h, cookies: []*http.Cookie{{Name: sessionCookie, Value: sess.Token}}}
}

func nearF(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func ints(v any) []int {
	var out []int
	for _, x := range v.([]any) {
		out = append(out, int(x.(float64)))
	}
	return out
}

func TestIntegrationBudgetsValidationAndAuthz(t *testing.T) {
	h, st, mail := setupBudgetAPI(t)
	s := seedBudgetLedger(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	anon := &client{t: t, h: h}
	cust := customerClient(t, h, st, "admin@acme.example", store.RoleCustomerAdmin, s.a.ID)

	anon.must("GET", "/api/v1/budgets", 401)
	anon.mustJSON("POST", "/api/v1/budgets", map[string]any{"name": "x", "amount": "1"}, 401)
	cust.mustJSON("POST", "/api/v1/budgets", map[string]any{"name": "x", "amount": "1"}, 403)

	bad := []struct {
		name string
		body map[string]any
		code int
		msg  string
	}{
		{"missing name", map[string]any{"amount": "100"}, 400, "name is required"},
		{"blank name", map[string]any{"name": "   ", "amount": "100"}, 400, "name is required"},
		{"missing amount", map[string]any{"name": "x"}, 400, "amount is required"},
		{"negative amount", map[string]any{"name": "x", "amount": "-1"}, 400, "amount must be 0 or more"},
		{"non-numeric amount", map[string]any{"name": "x", "amount": "abc"}, 400, "invalid body"},
		{"exponent amount", map[string]any{"name": "x", "amount": "1e3"}, 400, "invalid body"},
		{"two-letter currency", map[string]any{"name": "x", "amount": "1", "currency": "OM"}, 400, "currency must be a 3-letter code"},
		{"digit currency", map[string]any{"name": "x", "amount": "1", "currency": "OM1"}, 400, "currency must be a 3-letter code"},
		{"weekly period", map[string]any{"name": "x", "amount": "1", "period": "weekly"}, 400, "period must be monthly"},
		{"zero threshold", map[string]any{"name": "x", "amount": "1", "thresholds": []int{0}}, 400, "thresholds must be whole percentages between 1 and 1000"},
		{"threshold over 1000", map[string]any{"name": "x", "amount": "1", "thresholds": []int{50, 1001}}, 400, "thresholds must be whole percentages between 1 and 1000"},
		{"fractional threshold", map[string]any{"name": "x", "amount": "1", "thresholds": []float64{50.5}}, 400, "invalid body"},
		{"bad email", map[string]any{"name": "x", "amount": "1", "notify_emails": []string{"fin@acme.example", "not-an-email"}}, 400, "notify_emails must be valid email addresses"},
		{"customer_id not a string", map[string]any{"name": "x", "amount": "1", "customer_id": 7}, 400, "customer_id must be a string or null"},
		{"unknown field", map[string]any{"name": "x", "amount": "1", "colour": "red"}, 400, "invalid body"},
		{"unknown customer", map[string]any{"name": "x", "amount": "1", "customer_id": "00000000-0000-0000-0000-000000000000"}, 404, "not found"},
		{"malformed customer id", map[string]any{"name": "x", "amount": "1", "customer_id": "nope"}, 404, "not found"},
	}
	for _, c := range bad {
		rec, out := op.json("POST", "/api/v1/budgets", c.body)
		if rec.Code != c.code || !strings.Contains(out["error"].(string), c.msg) {
			t.Fatalf("%s: %d %s (want %d %q)", c.name, rec.Code, rec.Body.String(), c.code, c.msg)
		}
	}
	if n, _ := st.ListBudgets(context.Background(), store.OperatorScope); len(n) != 0 {
		t.Fatalf("rejected budgets were stored: %+v", n)
	}
	// Nothing rejected is audited; nothing was mailed.
	for _, m := range mail.msgs {
		if strings.Contains(m, "|Budget ") {
			t.Fatalf("mail sent during validation: %s", m)
		}
	}
}

func TestIntegrationBudgetsCRUDScopeStatusSummaryAndAlerts(t *testing.T) {
	h, st, mail := setupBudgetAPI(t)
	s := seedBudgetLedger(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := customerClient(t, h, st, "admin@acme.example", store.RoleCustomerAdmin, s.a.ID)
	bravo := customerClient(t, h, st, "viewer@bravo.example", store.RoleCustomerViewer, s.b.ID)

	// --- create: defaults applied, arrays normalized, customer name joined.
	global := op.mustJSON("POST", "/api/v1/budgets", map[string]any{"name": "Sovereign cap", "amount": 1000}, 201)
	if global["customer_id"] != nil || global["currency"] != "OMR" || global["period"] != "monthly" || global["active"] != true || global["amount"].(float64) != 1000 {
		t.Fatalf("global = %+v", global)
	}
	if th := ints(global["thresholds"]); len(th) != 3 || th[0] != 50 || th[1] != 80 || th[2] != 100 {
		t.Fatalf("default thresholds = %v", th)
	}
	if em := global["notify_emails"].([]any); len(em) != 0 {
		t.Fatalf("default notify_emails = %v", em)
	}
	globalID := global["id"].(string)

	acme200 := op.mustJSON("POST", "/api/v1/budgets", map[string]any{
		"name": " Acme cap ", "customer_id": s.a.ID, "amount": "200", "currency": "omr",
		"thresholds": []int{80, 50, 50}, "notify_emails": []string{"Fin@acme.example", "fin@acme.example"},
	}, 201)
	if acme200["name"] != "Acme cap" || acme200["customer_id"] != s.a.ID || acme200["customer_name"] != "Acme" || acme200["currency"] != "OMR" {
		t.Fatalf("acme200 = %+v", acme200)
	}
	if th := ints(acme200["thresholds"]); len(th) != 2 || th[0] != 50 || th[1] != 80 {
		t.Fatalf("thresholds not sorted/deduped: %v", th)
	}
	if em := acme200["notify_emails"].([]any); len(em) != 1 || em[0] != "fin@acme.example" {
		t.Fatalf("emails not normalized: %v", em)
	}
	acme200ID := acme200["id"].(string)

	acme100 := op.mustJSON("POST", "/api/v1/budgets", map[string]any{
		"name": "Acme tight", "customer_id": s.a.ID, "amount": "100",
		"notify_emails": []string{"fin@acme.example", "cfo@acme.example"},
	}, 201)
	acme100ID := acme100["id"].(string)

	bravoB := op.mustJSON("POST", "/api/v1/budgets", map[string]any{"name": "Bravo cap", "customer_id": s.b.ID, "amount": "50", "active": false}, 201)
	bravoID := bravoB["id"].(string)

	// --- list / get by scope.
	if l := op.must("GET", "/api/v1/budgets", 200)["budgets"].([]any); len(l) != 4 {
		t.Fatalf("operator list = %d", len(l))
	}
	mine := acme.must("GET", "/api/v1/budgets", 200)["budgets"].([]any)
	if len(mine) != 2 {
		t.Fatalf("acme list = %+v", mine)
	}
	for _, b := range mine {
		if b.(map[string]any)["customer_id"] != s.a.ID {
			t.Fatalf("acme sees a budget that is not its own: %+v", b)
		}
	}
	if l := bravo.must("GET", "/api/v1/budgets", 200)["budgets"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != bravoID {
		t.Fatalf("bravo list = %+v", l)
	}
	acme.must("GET", "/api/v1/budgets/"+globalID, 404)
	acme.must("GET", "/api/v1/budgets/"+globalID+"/status", 404)
	bravo.must("GET", "/api/v1/budgets/"+acme200ID, 404)
	bravo.must("GET", "/api/v1/budgets/"+acme200ID+"/status", 404)
	acme.must("GET", "/api/v1/budgets/"+acme200ID, 200)
	op.must("GET", "/api/v1/budgets/"+globalID, 200)
	op.must("GET", "/api/v1/budgets/00000000-0000-0000-0000-000000000000", 404)
	// Customer-lens listing.
	if l := op.must("GET", "/api/v1/customers/"+s.a.ID+"/budgets", 200)["budgets"].([]any); len(l) != 2 {
		t.Fatalf("operator customer list = %d", len(l))
	}
	if l := acme.must("GET", "/api/v1/customers/"+s.a.ID+"/budgets", 200)["budgets"].([]any); len(l) != 2 {
		t.Fatalf("acme customer list = %d", len(l))
	}
	acme.must("GET", "/api/v1/customers/"+s.b.ID+"/budgets", 404)
	// Writes are operator-only, whatever the customer role.
	acme.mustJSON("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"amount": "1"}, 403)
	acme.must("DELETE", "/api/v1/budgets/"+acme200ID, 403)
	bravo.mustJSON("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"amount": "1"}, 403)

	// --- status math for the current month (2026-09 as of the 8th).
	// Acme: 100.8 of 200 → 50.4 %, threshold 50 crossed, 80 not; forecast =
	// 100.8 + 14.4 × 23 remaining days = 432 → 216 % → warning either way.
	st200 := acme.must("GET", "/api/v1/budgets/"+acme200ID+"/status", 200)
	for _, k := range []string{"id", "name", "customer_id", "customer_name", "amount", "currency", "period", "actual", "forecast", "pct_actual", "pct_forecast", "status", "thresholds"} {
		if _, ok := st200[k]; !ok {
			t.Fatalf("status lacks %q: %+v", k, st200)
		}
	}
	if st200["period"] != "2026-09" || st200["customer_name"] != "Acme" || !nearF(st200["actual"].(float64), 100.8) || !nearF(st200["pct_actual"].(float64), 50.4) || st200["status"] != "warning" {
		t.Fatalf("acme200 status = %+v", st200)
	}
	if !nearF(st200["forecast"].(float64), 432) || !nearF(st200["pct_forecast"].(float64), 216) {
		t.Fatalf("acme200 forecast = %v / %v", st200["forecast"], st200["pct_forecast"])
	}
	th := st200["thresholds"].([]any)
	if len(th) != 2 || th[0].(map[string]any)["pct"].(float64) != 50 || th[0].(map[string]any)["crossed"] != true || th[1].(map[string]any)["crossed"] != false || th[0].(map[string]any)["alerted_at"] != nil {
		t.Fatalf("acme200 thresholds = %+v", th)
	}
	// Acme tight: 100.8 of 100 → exceeded, every threshold crossed.
	st100 := op.must("GET", "/api/v1/budgets/"+acme100ID+"/status", 200)
	if st100["status"] != "exceeded" || !nearF(st100["pct_actual"].(float64), 100.8) {
		t.Fatalf("acme100 status = %+v", st100)
	}
	for _, x := range st100["thresholds"].([]any) {
		if x.(map[string]any)["crossed"] != true {
			t.Fatalf("acme100 thresholds = %+v", st100["thresholds"])
		}
	}
	// Global: 104.16 of 1000 → 10.416 %, forecast 104.16 + 14.88 × 23 = 446.4 → ok.
	stG := op.must("GET", "/api/v1/budgets/"+globalID+"/status", 200)
	if stG["customer_id"] != nil || stG["customer_name"] != nil || !nearF(stG["actual"].(float64), 104.16) || !nearF(stG["pct_actual"].(float64), 10.416) || stG["status"] != "ok" || !nearF(stG["forecast"].(float64), 446.4) {
		t.Fatalf("global status = %+v", stG)
	}
	// A past month: August's 42 against 200 → 21 %, no forecast, ok.
	stAug := op.must("GET", "/api/v1/budgets/"+acme200ID+"/status?period=2026-08", 200)
	if stAug["period"] != "2026-08" || !nearF(stAug["actual"].(float64), 42) || !nearF(stAug["pct_actual"].(float64), 21) || stAug["forecast"] != nil || stAug["pct_forecast"] != nil || stAug["status"] != "ok" {
		t.Fatalf("august status = %+v", stAug)
	}
	// A future month: nothing, no forecast.
	if stOct := op.must("GET", "/api/v1/budgets/"+acme200ID+"/status?period=2026-10", 200); stOct["actual"].(float64) != 0 || stOct["forecast"] != nil {
		t.Fatalf("october status = %+v", stOct)
	}
	if rec, _ := op.do("GET", "/api/v1/budgets/"+acme200ID+"/status?period=2026-9", "", nil); rec.Code != 400 {
		t.Fatalf("bad period = %d", rec.Code)
	}

	// --- the summary carries the status rows of the ACTIVE budgets in scope.
	sum := op.must("GET", "/api/v1/cost/summary", 200)["budgets"].([]any)
	if len(sum) != 3 {
		t.Fatalf("operator summary budgets = %d (%+v)", len(sum), sum)
	}
	seen := map[string]string{}
	for _, row := range sum {
		m := row.(map[string]any)
		seen[m["id"].(string)] = m["status"].(string)
	}
	if seen[globalID] != "ok" || seen[acme200ID] != "warning" || seen[acme100ID] != "exceeded" {
		t.Fatalf("summary statuses = %v", seen)
	}
	if _, inactive := seen[bravoID]; inactive {
		t.Fatal("an inactive budget must not be in the summary")
	}
	if ov := op.must("GET", "/api/v1/overview", 200)["budgets"].([]any); len(ov) != 3 {
		t.Fatalf("overview budgets = %d", len(ov))
	}
	if l := op.must("GET", "/api/v1/customers/"+s.a.ID+"/cost/summary", 200)["budgets"].([]any); len(l) != 2 {
		t.Fatalf("operator customer-lens summary budgets = %d", len(l))
	}
	custSum := acme.must("GET", "/api/v1/customers/"+s.a.ID+"/cost/summary", 200)["budgets"].([]any)
	if len(custSum) != 2 {
		t.Fatalf("acme summary budgets = %d", len(custSum))
	}
	for _, row := range custSum {
		if row.(map[string]any)["customer_id"] != s.a.ID {
			t.Fatalf("acme summary leaks %+v", row)
		}
	}
	if l := bravo.must("GET", "/api/v1/customers/"+s.b.ID+"/cost/summary", 200)["budgets"].([]any); len(l) != 0 {
		t.Fatalf("bravo (only an inactive budget) summary budgets = %+v", l)
	}

	// --- evaluator: one alert row + one mail per recipient per crossing,
	// and a second run changes nothing.
	mailsBefore := len(mail.msgs)
	ev := &budget.Evaluator{Store: st, Mail: mail, Now: func() time.Time { return budgetNow }}
	rep, err := ev.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Crossings: acme200 → 50 (1 recipient); acme100 → 50, 80, 100 (2
	// recipients each); global → none; bravo inactive → skipped.
	if rep.Budgets != 3 || rep.Crossings != 4 || rep.Mails != 7 || rep.Errors != 0 {
		t.Fatalf("first run = %+v", rep)
	}
	budgetMails := func() []string {
		var out []string
		for _, m := range mail.msgs[mailsBefore:] {
			if strings.Contains(m, "|Budget ") {
				out = append(out, m)
			}
		}
		return out
	}
	sent := budgetMails()
	if len(sent) != 7 {
		t.Fatalf("mails = %d: %v", len(sent), sent)
	}
	want := "fin@acme.example|Budget Acme cap: 50% of 200 OMR reached for 2026-09|"
	found := false
	for _, m := range sent {
		if strings.HasPrefix(m, want) {
			found = true
			if !strings.Contains(m, "Actual so far: 100.8 OMR (50.4% of the budget)") || !strings.Contains(m, "Month-end forecast: 432.00 OMR (216.0% of the budget)") {
				t.Fatalf("mail body = %s", m)
			}
		}
	}
	if !found {
		t.Fatalf("no mail starting %q in %v", want, sent)
	}
	a200, _ := st.ListBudgetAlerts(ctx, acme200ID)
	a100, _ := st.ListBudgetAlerts(ctx, acme100ID)
	aG, _ := st.ListBudgetAlerts(ctx, globalID)
	if len(a200) != 1 || a200[0].Threshold != 50 || a200[0].Period != "2026-09" || string(a200[0].Actual) != "100.800000" || len(a100) != 3 || len(aG) != 0 {
		t.Fatalf("alerts: acme200=%+v acme100=%+v global=%+v", a200, a100, aG)
	}
	audit, err := st.ListAudit(ctx, store.OperatorScope, s.a.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	crossings := 0
	for _, e := range audit {
		if e.Action == "budget.threshold" {
			crossings++
			var d struct {
				BudgetID  string `json:"budget_id"`
				Threshold int    `json:"threshold"`
				Actual    string `json:"actual"`
				Amount    string `json:"amount"`
				Period    string `json:"period"`
			}
			if err := json.Unmarshal(e.Details, &d); err != nil {
				t.Fatalf("audit details %s: %v", e.Details, err)
			}
			if e.Actor != "system" || d.Period != "2026-09" || d.BudgetID == "" || d.Threshold < 50 || d.Actual != "100.800000" || d.Amount == "" {
				t.Fatalf("audit entry = actor=%s details=%s", e.Actor, e.Details)
			}
		}
	}
	if crossings != 4 {
		t.Fatalf("audited crossings = %d", crossings)
	}

	rep, err = ev.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Crossings != 0 || rep.Mails != 0 || rep.Errors != 0 || len(budgetMails()) != 7 {
		t.Fatalf("second run must be a no-op: %+v, mails=%d", rep, len(budgetMails()))
	}
	if a200, _ = st.ListBudgetAlerts(ctx, acme200ID); len(a200) != 1 {
		t.Fatalf("second run added alert rows: %+v", a200)
	}
	// The status now shows when the threshold was alerted.
	st200 = acme.must("GET", "/api/v1/budgets/"+acme200ID+"/status", 200)
	th = st200["thresholds"].([]any)
	if th[0].(map[string]any)["alerted_at"] == nil || th[1].(map[string]any)["alerted_at"] != nil {
		t.Fatalf("alerted_at after evaluation = %+v", th)
	}

	// --- update: partial body keeps the rest; null customer_id makes it global.
	upd := op.mustJSON("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"amount": "300", "thresholds": []int{90}}, 200)
	if upd["name"] != "Acme cap" || upd["customer_id"] != s.a.ID || upd["amount"].(float64) != 300 || len(ints(upd["thresholds"])) != 1 || upd["notify_emails"].([]any)[0] != "fin@acme.example" {
		t.Fatalf("partial update = %+v", upd)
	}
	upd = op.mustJSON("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"customer_id": nil, "active": false}, 200)
	if upd["customer_id"] != nil || upd["customer_name"] != nil || upd["active"] != false {
		t.Fatalf("null customer update = %+v", upd)
	}
	// Now global and inactive: acme no longer sees it, the summary drops it.
	acme.must("GET", "/api/v1/budgets/"+acme200ID, 404)
	if l := acme.must("GET", "/api/v1/budgets", 200)["budgets"].([]any); len(l) != 1 {
		t.Fatalf("acme list after re-point = %d", len(l))
	}
	if rec, out := op.json("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"amount": "-5"}); rec.Code != 400 || out["error"] != "amount must be 0 or more" {
		t.Fatalf("invalid update = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/budgets/"+acme200ID, map[string]any{"customer_id": "00000000-0000-0000-0000-000000000000"}); rec.Code != 404 {
		t.Fatalf("update to unknown customer = %d", rec.Code)
	}
	op.mustJSON("PUT", "/api/v1/budgets/00000000-0000-0000-0000-000000000000", map[string]any{"name": "x"}, 404)

	// --- delete: gone, alerts gone, 404 after.
	if out := op.must("DELETE", "/api/v1/budgets/"+acme100ID, 200); out["deleted"] != true {
		t.Fatalf("delete = %+v", out)
	}
	op.must("GET", "/api/v1/budgets/"+acme100ID, 404)
	op.must("DELETE", "/api/v1/budgets/"+acme100ID, 404)
	if a100, _ = st.ListBudgetAlerts(ctx, acme100ID); len(a100) != 0 {
		t.Fatalf("alerts survived delete: %+v", a100)
	}

	// --- every write was audited under the budget's customer (nil → not
	// under any customer).
	audit, _ = st.ListAudit(ctx, store.OperatorScope, s.a.ID, 100)
	actions := map[string]int{}
	for _, e := range audit {
		actions[e.Action]++
	}
	if actions["budget.create"] != 2 || actions["budget.update"] < 1 || actions["budget.delete"] != 1 {
		t.Fatalf("audit actions for acme = %v", actions)
	}
	for _, e := range audit {
		if strings.HasPrefix(e.Action, "budget.") && e.Action != "budget.threshold" && e.Actor != opEmail {
			t.Fatalf("write audited to %q, want the operator", e.Actor)
		}
	}
}
