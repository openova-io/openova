package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/report"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Scheduled cost reports (#6867 follow-up) end to end: CRUD + validation,
// scope + write rights, preview and send-now against the seeded ledger, the
// deliveries log, and the background scheduler's once-only delivery.
//
// budgetNow (2026-09-08 10:00 UTC, a Tuesday) is "now": the seeded ledger
// has 2026-09-01..07 (14.4/day for Acme, 0.48/day for Bravo) and August
// 25..31 (6/day for Acme).

func strsOf(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestIntegrationReportSchedulesCRUDAndAuthz(t *testing.T) {
	h, st, mail := setupBudgetAPI(t)
	s := seedBudgetLedger(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	anon := &client{t: t, h: h}

	if rec, _ := anon.do("GET", "/api/v1/reports/schedules", "", nil); rec.Code != 401 {
		t.Fatalf("anon list = %d", rec.Code)
	}

	// Validation.
	bad := []map[string]any{
		{"name": "", "cadence": "weekly", "recipients": []string{"a@x.example"}},
		{"name": "x", "cadence": "hourly", "recipients": []string{"a@x.example"}},
		{"name": "x", "cadence": "weekly"},
		{"name": "x", "cadence": "weekly", "recipients": []string{"not-an-email"}},
		{"name": "x", "cadence": "weekly", "recipients": []string{"a@x.example"}, "sections": []string{"summary", "bogus"}},
		{"name": "x", "cadence": "weekly", "day_of_week": 7, "recipients": []string{"a@x.example"}},
		{"name": "x", "cadence": "monthly", "day_of_month": 29, "recipients": []string{"a@x.example"}},
		{"name": "x", "cadence": "daily", "hour_utc": 24, "recipients": []string{"a@x.example"}},
		{"name": "x", "cadence": "daily", "day_of_week": "monday", "recipients": []string{"a@x.example"}},
	}
	many := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		many = append(many, fmt.Sprintf("r%d@x.example", i))
	}
	bad = append(bad, map[string]any{"name": "x", "cadence": "weekly", "recipients": many})
	for i, body := range bad {
		if rec, _ := op.json("POST", "/api/v1/reports/schedules", body); rec.Code != 400 {
			t.Fatalf("bad body %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec, _ := op.json("POST", "/api/v1/reports/schedules", map[string]any{"name": "x", "cadence": "weekly", "customer_id": "00000000-0000-0000-0000-000000000000", "recipients": []string{"a@x.example"}}); rec.Code != 404 {
		t.Fatalf("unknown customer: %d", rec.Code)
	}

	// Global weekly, Monday 06:00 — from Tuesday 8 Sep the next Monday is 14 Sep.
	weekly := op.mustJSON("POST", "/api/v1/reports/schedules", map[string]any{
		"name": "Weekly ops", "cadence": "weekly", "day_of_week": 1, "hour_utc": 6,
		"recipients": []string{"Ops@NC.example", " fin@nc.example ", "ops@nc.example"},
	}, 201)
	weeklyID := weekly["id"].(string)
	if weekly["customer_id"] != nil || weekly["next_at"] != "2026-09-14T06:00:00Z" {
		t.Fatalf("weekly = %+v", weekly)
	}
	if got := strsOf(weekly["recipients"]); len(got) != 2 || got[0] != "ops@nc.example" || got[1] != "fin@nc.example" {
		t.Fatalf("recipients not normalized: %v", got)
	}
	if got := strsOf(weekly["sections"]); len(got) != len(report.Sections()) {
		t.Fatalf("default sections = %v", got)
	}
	if weekly["day_of_month"] != nil || weekly["day_of_week"].(float64) != 1 || weekly["active"] != true {
		t.Fatalf("weekly fields = %+v", weekly)
	}

	// Customer A daily at 06:00 with two sections, given out of order.
	daily := op.mustJSON("POST", "/api/v1/reports/schedules", map[string]any{
		"name": "Acme daily", "cadence": "daily", "customer_id": s.a.ID, "hour_utc": 6,
		"recipients": []string{"a@acme.example"}, "sections": []string{"services", "summary"},
	}, 201)
	dailyID := daily["id"].(string)
	if daily["customer_id"] != s.a.ID || daily["customer_name"] != "Acme" || daily["next_at"] != "2026-09-09T06:00:00Z" {
		t.Fatalf("daily = %+v", daily)
	}
	if got := strsOf(daily["sections"]); len(got) != 2 || got[0] != "summary" || got[1] != "services" {
		t.Fatalf("sections not normalized: %v", got)
	}
	if daily["day_of_week"] != nil || daily["day_of_month"] != nil {
		t.Fatalf("daily must clear the day fields: %+v", daily)
	}

	if n := len(op.must("GET", "/api/v1/reports/schedules", 200)["schedules"].([]any)); n != 2 {
		t.Fatalf("operator list = %d", n)
	}
	if n := len(op.must("GET", "/api/v1/customers/"+s.a.ID+"/reports/schedules", 200)["schedules"].([]any)); n != 1 {
		t.Fatalf("customer A list = %d", n)
	}

	// Customer A admin: sees only its own, cannot reach the global one, and
	// every write is forced onto its own customer.
	adminA := customerClient(t, h, st, "a@acme.example", store.RoleCustomerAdmin, s.a.ID)
	list := adminA.must("GET", "/api/v1/reports/schedules", 200)["schedules"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != dailyID {
		t.Fatalf("customer list = %v", list)
	}
	adminA.must("GET", "/api/v1/reports/schedules/"+weeklyID, 404)
	adminA.must("GET", "/api/v1/customers/"+s.b.ID+"/reports/schedules", 404)
	forced := adminA.mustJSON("POST", "/api/v1/reports/schedules", map[string]any{
		"name": "Sneaky", "cadence": "monthly", "day_of_month": 3, "customer_id": s.b.ID, "recipients": []string{"a@acme.example"},
	}, 201)
	if forced["customer_id"] != s.a.ID {
		t.Fatalf("customer_id not forced: %v", forced["customer_id"])
	}
	forcedID := forced["id"].(string)
	if rec, _ := adminA.json("PUT", "/api/v1/reports/schedules/"+weeklyID, map[string]any{"name": "mine now"}); rec.Code != 404 {
		t.Fatalf("customer PUT on global = %d", rec.Code)
	}
	if rec, _ := adminA.json("PUT", "/api/v1/reports/schedules/"+forcedID, map[string]any{"customer_id": nil, "name": "Renamed"}); rec.Code != 200 {
		t.Fatalf("customer PUT own = %d %s", rec.Code, rec.Body.String())
	}
	if got := adminA.must("GET", "/api/v1/reports/schedules/"+forcedID, 200); got["customer_id"] != s.a.ID || got["name"] != "Renamed" {
		t.Fatalf("customer PUT let customer_id go: %+v", got)
	}
	adminA.must("DELETE", "/api/v1/reports/schedules/"+weeklyID, 404)
	adminA.must("DELETE", "/api/v1/reports/schedules/"+forcedID, 200)

	// Customer B viewer: reads its (empty) list, writes nothing.
	viewerB := customerClient(t, h, st, "v@bravo.example", store.RoleCustomerViewer, s.b.ID)
	if n := len(viewerB.must("GET", "/api/v1/reports/schedules", 200)["schedules"].([]any)); n != 0 {
		t.Fatalf("viewer list = %d", n)
	}
	if rec, _ := viewerB.json("POST", "/api/v1/reports/schedules", map[string]any{"name": "x", "cadence": "daily", "recipients": []string{"v@bravo.example"}}); rec.Code != 403 {
		t.Fatalf("viewer create = %d", rec.Code)
	}
	viewerB.must("GET", "/api/v1/reports/schedules/"+dailyID, 404)

	// Operator PUT: partial body, cadence change recomputes next_at and
	// clears the other day field.
	upd := op.mustJSON("PUT", "/api/v1/reports/schedules/"+weeklyID, map[string]any{"cadence": "monthly", "day_of_month": 15}, 200)
	if upd["next_at"] != "2026-09-15T06:00:00Z" || upd["day_of_week"] != nil || upd["day_of_month"].(float64) != 15 || upd["name"] != "Weekly ops" {
		t.Fatalf("PUT = %+v", upd)
	}

	// Preview: the monthly global report covers August; both customers had
	// usage in August? Only Acme (6/day × 7 days = 42). Operator scope lists
	// customers.
	pv := op.must("GET", "/api/v1/reports/schedules/"+weeklyID+"/preview", 200)
	if pv["window_from"] != "2026-08-01" || pv["window_to"] != "2026-09-01" {
		t.Fatalf("preview window = %v..%v", pv["window_from"], pv["window_to"])
	}
	body := pv["body"].(string)
	subject := pv["subject"].(string)
	if !strings.Contains(subject, "1–31 Aug 2026") || !strings.Contains(subject, "42.000 OMR") {
		t.Fatalf("preview subject = %q", subject)
	}
	for _, want := range []string{"TOP CUSTOMERS", "Acme", "TOP SERVICES", "Elastic Cloud Server", "BUDGETS", "ANOMALIES", "RECOMMENDATIONS", "Month to date (Sep 2026)", "104.160 OMR"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview body lacks %q:\n%s", want, body)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if len([]rune(line)) > report.MaxLineWidth && !strings.HasPrefix(line, "http") {
			t.Fatalf("preview line too wide: %q", line)
		}
	}
	// Customer daily preview: yesterday (7 Sep) = 14.4 for Acme, no
	// customers section, only the two selected sections.
	pv = adminA.must("GET", "/api/v1/reports/schedules/"+dailyID+"/preview", 200)
	if pv["window_from"] != "2026-09-07" || pv["window_to"] != "2026-09-08" {
		t.Fatalf("daily window = %v..%v", pv["window_from"], pv["window_to"])
	}
	body = pv["body"].(string)
	if !strings.Contains(pv["subject"].(string), "14.400 OMR") || !strings.Contains(body, "Scope: Acme") {
		t.Fatalf("daily preview = %q\n%s", pv["subject"], body)
	}
	for _, absent := range []string{"TOP CUSTOMERS", "BUDGETS", "ANOMALIES", "RECOMMENDATIONS", "Bravo"} {
		if strings.Contains(body, absent) {
			t.Fatalf("daily preview leaks %q:\n%s", absent, body)
		}
	}
	if !strings.Contains(body, "TOP SERVICES") || !strings.Contains(body, "Block storage (EVS)") {
		t.Fatalf("daily preview lacks services:\n%s", body)
	}

	// Send now: mails every recipient, records the delivery, leaves next_at.
	before := len(mail.msgs)
	sent := op.mustJSON("POST", "/api/v1/reports/schedules/"+dailyID+"/send", nil, 200)
	if got := strsOf(sent["sent_to"]); len(got) != 1 || got[0] != "a@acme.example" {
		t.Fatalf("sent_to = %v", got)
	}
	if len(mail.msgs) != before+1 || !strings.HasPrefix(mail.last(t), "a@acme.example|"+sent["subject"].(string)+"|") {
		t.Fatalf("mail = %q", mail.last(t))
	}
	if !strings.Contains(mail.last(t), "14.400 OMR") {
		t.Fatalf("mail body lacks total: %q", mail.last(t))
	}
	after := op.must("GET", "/api/v1/reports/schedules/"+dailyID, 200)
	if after["next_at"] != "2026-09-09T06:00:00Z" || after["last_sent_at"] != budgetNow.Format(time.RFC3339) || after["sent_30d"].(float64) != 1 || after["failed_30d"].(float64) != 0 {
		t.Fatalf("after send = %+v", after)
	}
	dl := op.must("GET", "/api/v1/reports/schedules/"+dailyID+"/deliveries", 200)["deliveries"].([]any)
	if len(dl) != 1 {
		t.Fatalf("deliveries = %v", dl)
	}
	d := dl[0].(map[string]any)
	if d["ok"] != true || d["window_from"] != "2026-09-07" || d["window_to"] != "2026-09-08" || d["subject"] != sent["subject"] || d["error"] != nil {
		t.Fatalf("delivery = %+v", d)
	}
	// Audited on the customer as the operator.
	found := false
	for _, e := range op.must("GET", "/api/v1/customers/"+s.a.ID+"/audit", 200)["entries"].([]any) {
		m := e.(map[string]any)
		if m["action"] == "report.sent" && m["actor"] == opEmail {
			found = true
		}
	}
	if !found {
		t.Fatal("report.sent audit entry missing")
	}
	// A viewer may preview but not send.
	viewerA := customerClient(t, h, st, "v@acme.example", store.RoleCustomerViewer, s.a.ID)
	viewerA.must("GET", "/api/v1/reports/schedules/"+dailyID+"/preview", 200)
	if rec, _ := viewerA.json("POST", "/api/v1/reports/schedules/"+dailyID+"/send", nil); rec.Code != 403 {
		t.Fatalf("viewer send = %d", rec.Code)
	}
	if rec, _ := op.do("GET", "/api/v1/reports/schedules/"+dailyID+"/deliveries?limit=0", "", nil); rec.Code != 400 {
		t.Fatalf("bad limit = %d", rec.Code)
	}

	op.must("DELETE", "/api/v1/reports/schedules/"+dailyID, 200)
	op.must("GET", "/api/v1/reports/schedules/"+dailyID, 404)
	op.must("GET", "/api/v1/reports/schedules/"+dailyID+"/deliveries", 404)
}

// failMail refuses one address and accepts the rest.
type failMail struct {
	inner *recMail
	fail  string
}

func (f *failMail) Send(ctx context.Context, to, subject, body string) error {
	if to == f.fail {
		return errors.New("550 mailbox unavailable")
	}
	return f.inner.Send(ctx, to, subject, body)
}

func TestIntegrationReportSchedulerOnceOnly(t *testing.T) {
	_, st, mail := setupBudgetAPI(t)
	s := seedBudgetLedger(t, st)
	ctx := context.Background()
	now := budgetNow
	dow := 1
	past := now.Add(-time.Hour)
	sched, err := st.CreateReportSchedule(ctx, store.ReportScheduleInput{
		Name: "Weekly ops", Cadence: "weekly", DayOfWeek: &dow, HourUTC: 6, Active: true,
		Recipients: []string{"ops@nc.example", "fin@nc.example"}, Sections: report.Sections(), NextAt: past,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An inactive due schedule is never sent.
	if _, err := st.CreateReportSchedule(ctx, store.ReportScheduleInput{
		Name: "Off", Cadence: "daily", HourUTC: 6, Active: false, Recipients: []string{"x@nc.example"}, Sections: report.Sections(), NextAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	sc := &report.Scheduler{Store: st, Mail: mail, Now: func() time.Time { return now }, PublicURL: "https://billing.t99.omani.works"}

	before := len(mail.msgs)
	rep, err := sc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Due != 1 || rep.Sent != 1 || rep.Failed != 0 || rep.Skipped != 0 {
		t.Fatalf("first run = %+v", rep)
	}
	if len(mail.msgs) != before+2 {
		t.Fatalf("mails = %d, want +2", len(mail.msgs)-before)
	}
	if !strings.Contains(mail.last(t), "1–7 Sep 2026") || !strings.Contains(mail.last(t), "Open the console:\nhttps://billing.t99.omani.works/reports") {
		t.Fatalf("mail = %q", mail.last(t))
	}
	got, err := st.GetReportSchedule(ctx, store.OperatorScope, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextAt.Equal(time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)) || got.LastSentAt == nil || !got.LastSentAt.Equal(now) {
		t.Fatalf("after run: next_at=%v last_sent_at=%v", got.NextAt, got.LastSentAt)
	}
	dl, err := st.ListReportDeliveries(ctx, sched.ID, 0)
	if err != nil || len(dl) != 1 || !dl[0].OK || len(dl[0].Recipients) != 2 || dl[0].WindowFrom != "2026-09-01" || dl[0].WindowTo != "2026-09-08" {
		t.Fatalf("deliveries = %+v (%v)", dl, err)
	}

	// Not due any more: a second run sends nothing.
	rep, _ = sc.RunOnce(ctx)
	if rep.Due != 0 || len(mail.msgs) != before+2 {
		t.Fatalf("second run = %+v mails=%d", rep, len(mail.msgs)-before)
	}

	// Two concurrent polls over one due instant deliver exactly once.
	if _, err := st.ClaimReportRun(ctx, sched.ID, got.NextAt, past); err != nil {
		t.Fatal(err)
	}
	before = len(mail.msgs)
	var wg sync.WaitGroup
	reps := make([]report.Report, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reps[i], _ = sc.RunOnce(ctx)
		}(i)
	}
	wg.Wait()
	if sent := reps[0].Sent + reps[1].Sent; sent != 1 || len(mail.msgs) != before+2 {
		t.Fatalf("concurrent: sent=%d mails=%d reps=%+v", sent, len(mail.msgs)-before, reps)
	}

	// A failing recipient: the run is recorded ok=false with the error, the
	// schedule still advances (no retry storm), and the failure is audited.
	if _, err := st.ClaimReportRun(ctx, sched.ID, time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC), past); err != nil {
		t.Fatal(err)
	}
	only := &recMail{}
	sc.Mail = &failMail{inner: only, fail: "ops@nc.example"}
	sc.Store = st
	// Make every recipient fail: swap to a sender that fails both.
	sc.Mail = &failMail{inner: &recMail{}, fail: "ops@nc.example"}
	partial, _ := sc.RunOnce(ctx)
	if partial.Sent != 1 {
		t.Fatalf("partial delivery should count as sent: %+v", partial)
	}
	dl, _ = st.ListReportDeliveries(ctx, sched.ID, 0)
	if len(dl) != 3 || !dl[0].OK || dl[0].Error == nil || !strings.Contains(*dl[0].Error, "partial") || len(dl[0].Recipients) != 1 {
		t.Fatalf("partial delivery = %+v", dl[0])
	}
	// Total failure.
	if _, err := st.ClaimReportRun(ctx, sched.ID, time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC), past); err != nil {
		t.Fatal(err)
	}
	sc.Mail = &failAll{}
	failed, _ := sc.RunOnce(ctx)
	if failed.Due != 1 || failed.Failed != 1 {
		t.Fatalf("failed run = %+v", failed)
	}
	got, _ = st.GetReportSchedule(ctx, store.OperatorScope, sched.ID)
	if !got.NextAt.After(now) || got.Failed30d != 1 || got.LastError == nil || !strings.Contains(*got.LastError, "550") {
		t.Fatalf("after failure: %+v", got)
	}
	dl, _ = st.ListReportDeliveries(ctx, sched.ID, 0)
	if len(dl) != 4 || dl[0].OK || dl[0].Error == nil {
		t.Fatalf("failed delivery = %+v", dl[0])
	}
	// last_sent_at is the last SUCCESS, not the last attempt.
	if got.LastSentAt == nil || !got.LastSentAt.Equal(now) {
		t.Fatalf("last_sent_at = %v", got.LastSentAt)
	}
	// Audited as system on the (global) schedule.
	var actions []string
	rows, err := st.DB().QueryContext(ctx, `SELECT action, actor FROM audit_log WHERE action LIKE 'report.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, actor string
		if err := rows.Scan(&a, &actor); err != nil {
			t.Fatal(err)
		}
		if actor != "system" {
			t.Fatalf("actor = %s", actor)
		}
		actions = append(actions, a)
	}
	if strings.Join(actions, ",") != "report.sent,report.sent,report.sent,report.failed" {
		t.Fatalf("audit actions = %v", actions)
	}
	_ = s
}

type failAll struct{}

func (failAll) Send(context.Context, string, string, string) error {
	return errors.New("550 relay refused")
}
