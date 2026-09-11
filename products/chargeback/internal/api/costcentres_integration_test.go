package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Cost-centre labelling end to end (DESIGN.md §19), against Postgres.
//
// The claims under test, in the order a reader would want them proved:
//
//  1. ATTRIBUTION — the per-resource override beats the rules, the rules
//     beat nothing, and what nothing names lands in the unassigned bucket.
//  2. THE INVOICE DOES NOT MOVE — rating the same period before and after a
//     customer's cost centres exist produces the SAME subtotal, tax and
//     total. A showback dimension that changes a bill is a defect.
//  3. THE BREAKDOWN ADDS UP — each money column of the breakdown sums to the
//     statement's own figure exactly, on a split that does not divide.
//  4. IT IS FROZEN — deleting a rule after issue does not move a figure on
//     the invoice that was issued under it.
//  5. THE CLOSED PERIOD STAYS CLOSED — the only route that writes a
//     breakdown is the rating run, which a closed period already refuses.
//  6. THE PERMISSIONS — an owner reads its own and writes nothing; a
//     stranger gets 404; nobody unauthenticated gets anything.

// costCentreEnv is one customer with four resources whose usage divides
// AWKWARDLY: the unit price carries a sixth decimal, so a naive pro-rata
// split of the statement is a unit short of the invoice and the exactness
// assertions below have something to catch.
type costCentreEnv struct {
	h    http.Handler
	st   *store.Store
	op   *store.Session
	cust store.Customer
	src  store.CostSource
}

const costCentrePeriod = "2026-09"

func setupCostCentres(t *testing.T) costCentreEnv {
	t.Helper()
	h, st, _, _, _ := setupAPI(t)
	ctx := context.Background()

	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	// 1.000001 per instance-hour: every figure below carries a sixth
	// decimal, so nothing divides cleanly by three.
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "1.000001"}}, true); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{
		Slug: "acme", Name: "Acme Trading", AdminEmail: "owner@acme.example", StartDate: "2026-09-01",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer},
	})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-215", "ok-p1")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, book.ID)

	ws := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	add := func(res string, tags map[string]string) {
		for hr := 0; hr < 10; hr++ {
			at := ws.Add(time.Duration(hr) * time.Hour)
			labels := map[string]any{"name": res, "status": "ACTIVE"}
			if tags != nil {
				labels["tags"] = tags
			}
			lb, _ := json.Marshal(labels)
			recs = append(recs, store.UsageRecord{
				CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: "ecs", SKU: "ecs.s6.large.2",
				Quantity: "1.000000", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour),
				Region: "me-east-215", Labels: lb,
			})
		}
	}
	add("srv-1", map[string]string{"team": "platform"})
	add("srv-2", map[string]string{"team": "research"})
	add("srv-3", nil)                                   // nothing ever names this one
	add("srv-4", map[string]string{"team": "platform"}) // the rule says ENG; the override will say RES
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return costCentreEnv{h: h, st: st, op: operatorSession(), cust: c, src: src}
}

func (e costCentreEnv) call(t *testing.T, sess *store.Session, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := do(t, e.h, sess, method, path, body)
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

// mustJSON is call with the status asserted, so a test reads as the walk it
// is rather than as a chain of status checks.
func (e costCentreEnv) mustJSON(t *testing.T, sess *store.Session, method, path, body string, want int) map[string]any {
	t.Helper()
	rec, out := e.call(t, sess, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return out
}

func decEq(t *testing.T, got, want string) bool {
	t.Helper()
	a, ok := new(big.Rat).SetString(got)
	if !ok {
		t.Fatalf("not a number: %q", got)
	}
	b, ok := new(big.Rat).SetString(want)
	if !ok {
		t.Fatalf("not a number: %q", want)
	}
	return a.Cmp(b) == 0
}

func sumStrings(t *testing.T, vals []string) string {
	t.Helper()
	total := new(big.Rat)
	for _, v := range vals {
		r, ok := new(big.Rat).SetString(v)
		if !ok {
			t.Fatalf("not a number: %q", v)
		}
		total.Add(total, r)
	}
	return total.FloatString(6)
}

// column reads one money column out of a statement's cost_centre_lines.
func column(t *testing.T, lines []store.CostCentreLine, pick func(store.CostCentreLine) store.Decimal) []string {
	t.Helper()
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, string(pick(l)))
	}
	return out
}

func runStatementsFor(t *testing.T, e costCentreEnv) store.Statement {
	t.Helper()
	rec := do(t, e.h, e.op, "POST", "/api/v1/statements/run", fmt.Sprintf(`{"period":%q,"customer_id":%q}`, costCentrePeriod, e.cust.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("run = %d: %s", rec.Code, rec.Body.String())
	}
	sts, err := e.st.ListStatements(context.Background(), store.OperatorScope, e.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sts {
		if strings.HasPrefix(s.PeriodStart, costCentrePeriod) {
			return s
		}
	}
	t.Fatalf("no statement for %s", costCentrePeriod)
	return store.Statement{}
}

// assertBreakdownAddsUp is the contract: every money column of the breakdown
// sums to the statement's own figure, exactly.
func assertBreakdownAddsUp(t *testing.T, s store.Statement) {
	t.Helper()
	if len(s.CostCentreLines) == 0 {
		t.Fatal("the statement carries no cost-centre breakdown at all")
	}
	wantTotal := new(big.Rat)
	sub, _ := new(big.Rat).SetString(string(s.Subtotal))
	tx, _ := new(big.Rat).SetString(string(s.Tax))
	wantTotal.Add(sub, tx)
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"net", sumStrings(t, column(t, s.CostCentreLines, func(l store.CostCentreLine) store.Decimal { return l.Net })), string(s.Subtotal)},
		{"discount", sumStrings(t, column(t, s.CostCentreLines, func(l store.CostCentreLine) store.Decimal { return l.Discount })), string(s.DiscountTotal)},
		{"tax", sumStrings(t, column(t, s.CostCentreLines, func(l store.CostCentreLine) store.Decimal { return l.Tax })), string(s.Tax)},
		{"total", sumStrings(t, column(t, s.CostCentreLines, func(l store.CostCentreLine) store.Decimal { return l.Total })), string(s.Total)},
	} {
		if !decEq(t, c.got, c.want) {
			t.Errorf("the breakdown's %s sums to %s and the invoice carries %s — they disagree", c.name, c.got, c.want)
		}
	}
}

func byCode(lines []store.CostCentreLine) map[string]store.CostCentreLine {
	out := map[string]store.CostCentreLine{}
	for _, l := range lines {
		out[l.Code] = l
	}
	return out
}

func TestIntegrationCostCentreAttributionBreakdownAndFreeze(t *testing.T) {
	e := setupCostCentres(t)
	ctx := context.Background()

	// A percent discount, so the discount column is not trivially zero.
	active := true
	if _, err := e.st.CreateDiscount(ctx, store.DiscountInput{CustomerID: &e.cust.ID, Name: "Launch", Kind: "percent", Value: "7", Active: &active}); err != nil {
		t.Fatal(err)
	}

	// ── 1. Before any cost centre exists ──────────────────────────────────
	// Every figure is attributed to the NAMED unassigned bucket. Nothing is
	// dropped just because the customer has not labelled anything yet.
	before := runStatementsFor(t, e)
	if len(before.CostCentreLines) != 1 || before.CostCentreLines[0].Code != store.CostCentreUnassigned {
		t.Fatalf("breakdown with no centres = %+v", before.CostCentreLines)
	}
	assertBreakdownAddsUp(t, before)
	if before.Subtotal == "0.000000" || before.Tax == "0.000000" {
		t.Fatalf("nothing was rated: subtotal %s tax %s", before.Subtotal, before.Tax)
	}

	// ── 2. The centres, the rules and the override ────────────────────────
	eng := e.mustJSON(t, e.op, "POST", "/api/v1/customers/"+e.cust.ID+"/cost-centres", `{"code":"ENG","name":"Engineering"}`, http.StatusCreated)
	res := e.mustJSON(t, e.op, "POST", "/api/v1/customers/"+e.cust.ID+"/cost-centres", `{"code":"RES","name":"Research"}`, http.StatusCreated)
	engID, resID := eng["id"].(string), res["id"].(string)
	e.mustJSON(t, e.op, "PUT", "/api/v1/customers/"+e.cust.ID+"/cost-centres/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"platform"}`, engID), http.StatusOK)
	e.mustJSON(t, e.op, "PUT", "/api/v1/customers/"+e.cust.ID+"/cost-centres/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"research"}`, resID), http.StatusOK)
	// srv-4 is tagged platform and is nonetheless RESEARCH's: the override
	// beats the rule, which is the whole reason it exists.
	e.mustJSON(t, e.op, "PUT", "/api/v1/customers/"+e.cust.ID+"/cost-centres/resources/srv-4", fmt.Sprintf(`{"cost_centre_id":%q}`, resID), http.StatusOK)

	// ── 3. Rate again ─────────────────────────────────────────────────────
	after := runStatementsFor(t, e)

	// THE INVOICE DID NOT MOVE. This is the assertion the whole design
	// stands on: a cost centre attributes an amount already computed.
	if after.Subtotal != before.Subtotal || after.Tax != before.Tax || after.Total != before.Total || after.DiscountTotal != before.DiscountTotal {
		t.Fatalf("labelling changed the bill: before %s/%s/%s/%s after %s/%s/%s/%s",
			before.Subtotal, before.DiscountTotal, before.Tax, before.Total, after.Subtotal, after.DiscountTotal, after.Tax, after.Total)
	}
	assertBreakdownAddsUp(t, after)

	// THE ATTRIBUTION. srv-1 is ENG; srv-2 and srv-4 are RES (the second by
	// override); srv-3 is nobody's. The weights are therefore 1 : 2 : 1.
	rows := byCode(after.CostCentreLines)
	if len(rows) != 3 {
		t.Fatalf("breakdown rows = %+v", after.CostCentreLines)
	}
	for _, code := range []string{"ENG", "RES", store.CostCentreUnassigned} {
		if _, ok := rows[code]; !ok {
			t.Fatalf("no row for %s: %+v", after.CostCentreLines, code)
		}
	}
	if !decEq(t, string(rows["ENG"].Usage), "10.000010") || !decEq(t, string(rows["RES"].Usage), "20.000020") || !decEq(t, string(rows[store.CostCentreUnassigned].Usage), "10.000010") {
		t.Fatalf("weights = ENG %s RES %s unassigned %s", rows["ENG"].Usage, rows["RES"].Usage, rows[store.CostCentreUnassigned].Usage)
	}
	// RES carries twice what ENG does, to the unit.
	twiceENG := new(big.Rat).SetInt64(2)
	engNet, _ := new(big.Rat).SetString(string(rows["ENG"].Net))
	resNet, _ := new(big.Rat).SetString(string(rows["RES"].Net))
	diff := new(big.Rat).Sub(resNet, new(big.Rat).Mul(engNet, twiceENG))
	if diff.Abs(diff).Cmp(big.NewRat(1, 1000000)) > 0 {
		t.Fatalf("RES net %s is not twice ENG net %s", rows["RES"].Net, rows["ENG"].Net)
	}
	// The unassigned bucket is visible and carries srv-3's quarter — never
	// spread over the two named centres.
	if rows[store.CostCentreUnassigned].Net == "0.000000" {
		t.Fatal("srv-3's cost vanished instead of landing in the unassigned bucket")
	}

	// ── 4. The explorer's own dimension agrees ────────────────────────────
	window := "from=2026-09-01&to=2026-10-01&customer=" + e.cust.ID
	_, ex := e.call(t, e.op, "GET", "/api/v1/cost/explore?"+window+"&group_by=cost_centre", "")
	groups := map[string]float64{}
	for _, g := range ex["groups"].([]any) {
		m := g.(map[string]any)
		groups[m["key"].(string)] = m["total"].(float64)
	}
	if len(groups) != 3 || groups["RES"] <= groups["ENG"] || groups[store.CostCentreUnassigned] == 0 {
		t.Fatalf("explorer by cost_centre = %v", groups)
	}

	// ── 5. The report and its CSV ─────────────────────────────────────────
	_, rep := e.call(t, e.op, "GET", "/api/v1/customers/"+e.cust.ID+"/cost-centres/report?period="+costCentrePeriod, "")
	if rep["source"] != "statement" || rep["statement_id"] != after.ID {
		t.Fatalf("report source = %v / %v", rep["source"], rep["statement_id"])
	}
	inv, _ := rep["invoice"].(map[string]any)
	if inv == nil || inv["agrees"] != true {
		t.Fatalf("report does not claim to agree with the invoice: %v", rep["invoice"])
	}
	csvRec := do(t, e.h, e.op, "GET", "/api/v1/customers/"+e.cust.ID+"/cost-centres/report.csv?period="+costCentrePeriod, "")
	if csvRec.Code != http.StatusOK || !strings.HasPrefix(csvRec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv = %d %s", csvRec.Code, csvRec.Header().Get("Content-Type"))
	}
	body := csvRec.Body.String()
	for _, want := range []string{"code,name,usage,list,discount,net,tax,total,currency,source", "ENG,Engineering,", "RES,Research,", store.CostCentreUnassigned, "TOTAL,,"} {
		if !strings.Contains(body, want) {
			t.Fatalf("csv missing %q:\n%s", want, body)
		}
	}
	// The TOTAL row of the export carries the invoice's own total — that is
	// the line a finance reader checks the file by.
	if !strings.Contains(body, ","+string(after.Total)+",") {
		t.Fatalf("csv total row does not carry the invoice total %s:\n%s", after.Total, body)
	}

	// ── 6. Issue, then edit the rules ─────────────────────────────────────
	if rec := do(t, e.h, e.op, "POST", "/api/v1/statements/"+after.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	rules, err := e.st.ListCostCentreRules(ctx, store.OperatorScope, e.cust.ID)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %v %v", rules, err)
	}
	for _, r := range rules {
		if rec := do(t, e.h, e.op, "DELETE", "/api/v1/cost-centres/rules/"+r.ID, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete rule = %d: %s", rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, e.h, e.op, "DELETE", "/api/v1/cost-centres/"+engID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete centre = %d: %s", rec.Code, rec.Body.String())
	}
	// THE INVOICE IS FROZEN. Its breakdown still names ENG and still carries
	// the same figures: the column stores codes, not a foreign key.
	issued, err := e.st.GetStatement(ctx, store.OperatorScope, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != store.StatusIssued {
		t.Fatalf("status = %s", issued.Status)
	}
	frozen := byCode(issued.CostCentreLines)
	if len(frozen) != 3 || frozen["ENG"].Net != rows["ENG"].Net || frozen["RES"].Net != rows["RES"].Net {
		t.Fatalf("the issued invoice's breakdown moved when the rules were edited: %+v", issued.CostCentreLines)
	}
	assertBreakdownAddsUp(t, issued)
}

// A CLOSED period does not become editable through any cost-centre route.
// The only thing that writes a breakdown is the rating run, and the run is
// already refused once the books are closed (DESIGN.md §18.4) — so the guard
// holds by construction, and this walks it rather than asserting it.
func TestIntegrationCostCentresCannotReopenAClosedPeriod(t *testing.T) {
	e := setupCostCentres(t)
	ctx := context.Background()
	s := runStatementsFor(t, e)
	if rec := do(t, e.h, e.op, "POST", "/api/v1/statements/"+s.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, e.h, e.op, "POST", "/api/v1/finance/periods/"+costCentrePeriod+"/close", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("close = %d: %s", rec.Code, rec.Body.String())
	}
	issued, err := e.st.GetStatement(ctx, store.OperatorScope, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeLines := issued.CostCentreLines

	// Configuring cost centres is still allowed — it changes no money, and
	// refusing it would make a closed month unmanageable for the next one.
	c := e.mustJSON(t, e.op, "POST", "/api/v1/customers/"+e.cust.ID+"/cost-centres", `{"code":"ENG"}`, http.StatusCreated)
	e.mustJSON(t, e.op, "PUT", "/api/v1/customers/"+e.cust.ID+"/cost-centres/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"platform"}`, c["id"].(string)), http.StatusOK)

	// The one route that could write a new breakdown onto the closed month
	// is the run, and it is refused — naming the period.
	rec := do(t, e.h, e.op, "POST", "/api/v1/statements/run", fmt.Sprintf(`{"period":%q}`, costCentrePeriod))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "closed") {
		t.Fatalf("re-run of a closed period = %d: %s", rec.Code, rec.Body.String())
	}
	after, err := e.st.GetStatement(ctx, store.OperatorScope, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.CostCentreLines) != len(beforeLines) || after.CostCentreLines[0].Total != beforeLines[0].Total {
		t.Fatalf("the closed period's breakdown moved: %+v -> %+v", beforeLines, after.CostCentreLines)
	}
	if after.CostCentreLines[0].Code != store.CostCentreUnassigned {
		t.Fatalf("the closed invoice picked up a rule written after the close: %+v", after.CostCentreLines)
	}
}

// The access model (DESIGN.md §10.2, §19): reading is metering.read on the
// customer, so an OWNER reads its own; every write is customers.manage,
// which a customer role does not hold. A stranger is 404, never 403 — ids of
// other customers are not confirmed.
func TestIntegrationCostCentrePermissions(t *testing.T) {
	e := setupCostCentres(t)
	ctx := context.Background()
	other, err := e.st.CreateCustomer(ctx, store.CustomerInput{Slug: "beta", Name: "Beta", AdminEmail: "owner@beta.example"})
	if err != nil {
		t.Fatal(err)
	}
	centre := e.mustJSON(t, e.op, "POST", "/api/v1/customers/"+e.cust.ID+"/cost-centres", `{"code":"ENG"}`, http.StatusCreated)
	centreID := centre["id"].(string)

	owner := &store.Session{Email: "owner@acme.example", Role: store.RoleCustomerAdmin, CustomerID: &e.cust.ID, ExpiresAt: time.Now().Add(time.Hour)}
	stranger := &store.Session{Email: "owner@beta.example", Role: store.RoleCustomerAdmin, CustomerID: &other.ID, ExpiresAt: time.Now().Add(time.Hour)}

	reads := []string{
		"/api/v1/customers/" + e.cust.ID + "/cost-centres",
		"/api/v1/customers/" + e.cust.ID + "/cost-centres/rules",
		"/api/v1/customers/" + e.cust.ID + "/cost-centres/resources",
		"/api/v1/customers/" + e.cust.ID + "/cost-centres/report?period=" + costCentrePeriod,
		"/api/v1/customers/" + e.cust.ID + "/cost-centres/report.csv?period=" + costCentrePeriod,
	}
	for _, path := range reads {
		if rec := do(t, e.h, owner, "GET", path, ""); rec.Code != http.StatusOK {
			t.Errorf("owner GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if rec := do(t, e.h, stranger, "GET", path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("stranger GET %s = %d, want 404", path, rec.Code)
		}
		if rec := do(t, e.h, nil, "GET", path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401", path, rec.Code)
		}
	}

	writes := []struct {
		method, path, body string
	}{
		{"POST", "/api/v1/customers/" + e.cust.ID + "/cost-centres", `{"code":"NEW"}`},
		{"PUT", "/api/v1/cost-centres/" + centreID, `{"code":"ENG2"}`},
		{"DELETE", "/api/v1/cost-centres/" + centreID, ""},
		{"PUT", "/api/v1/customers/" + e.cust.ID + "/cost-centres/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"x"}`, centreID)},
		{"PUT", "/api/v1/customers/" + e.cust.ID + "/cost-centres/resources/srv-1", fmt.Sprintf(`{"cost_centre_id":%q}`, centreID)},
		{"DELETE", "/api/v1/customers/" + e.cust.ID + "/cost-centres/resources/srv-1", ""},
	}
	for _, w := range writes {
		if rec := do(t, e.h, owner, w.method, w.path, w.body); rec.Code != http.StatusForbidden {
			t.Errorf("owner %s %s = %d, want 403", w.method, w.path, rec.Code)
		} else if !strings.Contains(rec.Body.String(), "customers.manage") {
			t.Errorf("%s %s refusal does not name the permission: %s", w.method, w.path, rec.Body.String())
		}
		if rec := do(t, e.h, nil, w.method, w.path, w.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401", w.method, w.path, rec.Code)
		}
	}
	// A write addressed by the CENTRE's id, from a principal with no binding
	// on its customer, is 404 — the id is not confirmed either.
	if rec := do(t, e.h, stranger, "PUT", "/api/v1/cost-centres/"+centreID, `{"code":"X"}`); rec.Code != http.StatusNotFound {
		t.Errorf("stranger PUT by centre id = %d, want 404", rec.Code)
	}
}

// The validation the model rests on: a code shape, one cost centre per tag
// value, and an inactive centre that nothing new may point at.
func TestIntegrationCostCentreValidation(t *testing.T) {
	e := setupCostCentres(t)
	base := "/api/v1/customers/" + e.cust.ID + "/cost-centres"

	rec, out := e.call(t, e.op, "POST", base, `{"code":"not a code"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), store.CostCentreCodeRule) {
		t.Fatalf("bad code = %d %v", rec.Code, out)
	}
	// The unassigned bucket's own name can never be claimed as a code: the
	// parentheses put it outside the rule.
	if rec, _ := e.call(t, e.op, "POST", base, `{"code":"`+store.CostCentreUnassigned+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("the unassigned bucket was accepted as a code: %d", rec.Code)
	}
	eng := e.mustJSON(t, e.op, "POST", base, `{"code":"ENG","name":"Engineering"}`, http.StatusCreated)
	res := e.mustJSON(t, e.op, "POST", base, `{"code":"RES"}`, http.StatusCreated)
	engID, resID := eng["id"].(string), res["id"].(string)
	if rec, _ := e.call(t, e.op, "POST", base, `{"code":"ENG"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate code = %d", rec.Code)
	}

	// ONE cost centre per tag value: re-pointing a value EDITS its rule
	// rather than adding a second, so a record can never match two rules on
	// the same key.
	first := e.mustJSON(t, e.op, "PUT", base+"/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"platform"}`, engID), http.StatusOK)
	second := e.mustJSON(t, e.op, "PUT", base+"/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"platform"}`, resID), http.StatusOK)
	if first["id"] != second["id"] || second["code"] != "RES" {
		t.Fatalf("re-pointing a tag value made a second rule: %v then %v", first, second)
	}
	rules, err := e.st.ListCostCentreRules(context.Background(), store.OperatorScope, e.cust.ID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %v %v", rules, err)
	}
	// A rule naming a tag key that could not be a tag is refused with the rule.
	if rec, out := e.call(t, e.op, "PUT", base+"/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"has space","tag_value":"x"}`, engID)); rec.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), store.TagKeyRule) {
		t.Fatalf("bad tag key = %d %v", rec.Code, out)
	}
	// An INACTIVE centre may not be pointed at, and the refusal says why.
	e.mustJSON(t, e.op, "PUT", "/api/v1/cost-centres/"+engID, `{"code":"ENG","name":"Engineering","active":false}`, http.StatusOK)
	if rec, out := e.call(t, e.op, "PUT", base+"/rules", fmt.Sprintf(`{"cost_centre_id":%q,"tag_key":"team","tag_value":"eng"}`, engID)); rec.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), "inactive") {
		t.Fatalf("rule onto an inactive centre = %d %v", rec.Code, out)
	}
	if rec, _ := e.call(t, e.op, "PUT", base+"/resources/srv-1", fmt.Sprintf(`{"cost_centre_id":%q}`, engID)); rec.Code != http.StatusBadRequest {
		t.Fatalf("override onto an inactive centre = %d", rec.Code)
	}
	// A centre of ANOTHER customer is not addressable by code or by id.
	if rec, _ := e.call(t, e.op, "PUT", base+"/rules", `{"code":"NOPE","tag_key":"team","tag_value":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code = %d", rec.Code)
	}

	// A resource id may contain SLASHES — a Kubernetes object is
	// namespace/name — so the override route is a multi-segment wildcard and
	// the id must survive the round trip unchanged, percent signs included.
	for _, rid := range []string{"kube-system/coredns-abc", "srv one", "srv%2Fnot-a-slash"} {
		e.mustJSON(t, e.op, "PUT", base+"/resources/"+strings.ReplaceAll(url.PathEscape(rid), "%2F", "/"), fmt.Sprintf(`{"cost_centre_id":%q}`, resID), http.StatusOK)
	}
	rows, err := e.st.ListCostCentreResources(context.Background(), store.OperatorScope, e.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range rows {
		got[o.ResourceID] = true
	}
	for _, rid := range []string{"kube-system/coredns-abc", "srv one", "srv%2Fnot-a-slash"} {
		if !got[rid] {
			t.Fatalf("resource id %q did not survive the round trip: %v", rid, got)
		}
	}
	if rec := do(t, e.h, e.op, "DELETE", base+"/resources/kube-system/coredns-abc", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("clearing a slash-bearing override = %d: %s", rec.Code, rec.Body.String())
	}
}

// An unrated period reports the period's USAGE and says so, rather than
// inventing an invoice that does not exist.
func TestIntegrationCostCentreReportBeforeTheRun(t *testing.T) {
	e := setupCostCentres(t)
	_, rep := e.call(t, e.op, "GET", "/api/v1/customers/"+e.cust.ID+"/cost-centres/report?period="+costCentrePeriod, "")
	if rep["source"] != "usage" || rep["invoice"] != nil {
		t.Fatalf("report before the run = %v", rep)
	}
	lines, _ := rep["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	// Decimal marshals as a JSON NUMBER (store.Decimal.MarshalJSON), so the
	// assertions read numbers rather than the strings the Go side holds.
	row := lines[0].(map[string]any)
	num := func(k string) float64 {
		v, _ := row[k].(float64)
		return v
	}
	if row["code"] != store.CostCentreUnassigned || num("usage") <= 0 {
		t.Fatalf("unrated row = %v", row)
	}
	if num("net") != 0 || num("tax") != 0 || num("total") != 0 || num("discount") != 0 {
		t.Fatalf("an unrated period claimed money: %v", row)
	}
	if rec, _ := e.call(t, e.op, "GET", "/api/v1/customers/"+e.cust.ID+"/cost-centres/report?period=2026-13", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period = %d", rec.Code)
	}
}
