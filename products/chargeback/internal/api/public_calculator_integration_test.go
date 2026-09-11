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

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The public calculator, end to end (DESIGN.md §11).
//
// The load-bearing assertion is the estimate-equals-invoice block: a
// statement is rated from a real month of usage, and an estimate of the SAME
// shape — one instance for 730 hours, 100 GB of SSD — produces the same unit
// price, the same quantity, the same amount, the same tax and the same
// total. If the calculator ever grew a pricing path of its own, that is
// where it shows. The control beside it: a 20 % campaign moves the invoice
// and does not move the public estimate by a baisa.
//
// Every money comparison here goes through exactJSON, which re-reads the
// recorded body with json.Number: a decimal is compared as the TEXT the
// server wrote, never as a float64 that happens to print the same.

// anonClient is a caller with no cookie and no session — what the public
// routes must serve and what every other route must refuse.
func anonClient(t *testing.T, h http.Handler) *client { return &client{t: t, h: h} }

// publicHandler builds a handler over the same store with the given
// public-calculator configuration (origins, per-address budget, clock).
func publicHandler(t *testing.T, st *store.Store, origins []string, perMinute int, now func() time.Time) http.Handler {
	t.Helper()
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	return New(Deps{
		Store: st, Keys: keys, Mail: &recMail{}, Verifier: &fakeVerifier{},
		Config: config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "sovereign", OperatorEmails: []string{opEmail},
			PublicCalculatorOrigins: origins, PublicCalculatorRatePerMinute: perMinute},
		Metrics: metrics.New(), Version: "test", Now: now,
	})
}

// exactJSON parses a recorded body keeping every number as its literal text.
func exactJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	d.UseNumber()
	var out map[string]any
	if err := d.Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// dec renders a wire value as the text it arrived as (json.Number marshals
// verbatim), so a decimal comparison is on the digits.
func dec(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// getExact / postExact are do/json with the status checked and the body read
// exactly.
func getExact(t *testing.T, c *client, path string, want int) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec, _ := c.do("GET", path, "", nil)
	if rec.Code != want {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec, exactJSON(t, rec)
}

func postExact(t *testing.T, c *client, path string, body any, want int) map[string]any {
	t.Helper()
	rec, _ := c.json("POST", path, body)
	if rec.Code != want {
		t.Fatalf("POST %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return exactJSON(t, rec)
}

func TestIntegrationPublicCalculator(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// The list book the Sovereign publishes, and a negotiated clone of it
	// that must never reach the public surface.
	list := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "NC list 2026", "annual_divisor": 8760, "bill_stopped": "compute"}, 201)
	listID := list["id"].(string)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+listID+"/items", map[string]any{"items": []map[string]any{
		{"sku": "ecs.s6.large.2", "unit": "instance-hour", "unit_price": "0.10000000", "description": "General computing 2 vCPU 4 GB"},
		{"sku": "evs.ssd.gb", "unit": "gb-hour", "unit_price": "0.00013699", "description": "SSD block storage per GB"},
	}}, 200)
	negotiated := op.mustJSON("POST", "/api/v1/pricebooks/"+listID+"/clone", map[string]any{"name": "Acme negotiated 2026"}, 201)
	negotiatedID := negotiated["id"].(string)
	op.mustJSON("PATCH", "/api/v1/pricebooks/"+negotiatedID+"/items/ecs.s6.large.2", map[string]any{"unit_price": "0.04000000"}, 200)
	// The two platform books the Organization sync owns are published with
	// the cloud list.
	if _, _, err := st.EnsurePlanBook(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.EnsurePAYGBook(ctx); err != nil {
		t.Fatal(err)
	}

	// A customer whose source is on the LIST book, with a month of usage:
	// 730 hours of one instance and of 100 GB of SSD.
	acme := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "acme", "name": "ACME LLC", "admin_email": "ops@acme.example"}, 201)
	acmeID := acme["id"].(string)
	src, _, err := st.UpsertSource(ctx, acmeID, store.SourceKindFile, "me-east-215-a", "acme-file")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, listID); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for hr := 0; hr < 730; hr++ {
		w0 := from.Add(time.Duration(hr) * time.Hour)
		recs = append(recs,
			store.UsageRecord{CustomerID: acmeID, SourceID: src.ID, ResourceID: "srv-1", ResourceKind: "ecs", SKU: "ecs.s6.large.2", Quantity: "1.000000", Unit: "instance-hour", WindowStart: w0, WindowEnd: w0.Add(time.Hour), Region: "me-east-215-a"},
			store.UsageRecord{CustomerID: acmeID, SourceID: src.ID, ResourceID: "vol-1", ResourceKind: "evs", SKU: "evs.ssd.gb", Quantity: "100.000000", Unit: "gb-hour", WindowStart: w0, WindowEnd: w0.Add(time.Hour), Region: "me-east-215-a"})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// ── nothing is public until a book is designated ─────────────────────
	anon := anonClient(t, h)
	if rec, out := anon.do("GET", "/api/v1/public/catalog", "", nil); rec.Code != 404 || !strings.Contains(fmt.Sprint(out["error"]), "not published yet") {
		t.Fatalf("catalog before designation = %d %v", rec.Code, out)
	}
	if rec, _ := anon.json("PUT", "/api/v1/pricebooks/"+listID+"/public", map[string]any{"public": true}); rec.Code != 401 {
		t.Fatalf("anonymous publish = %d, want 401", rec.Code)
	}
	pub := op.mustJSON("PUT", "/api/v1/pricebooks/"+listID+"/public", map[string]any{"public": true}, 200)
	if pub["public"] != true {
		t.Fatalf("published book = %+v", pub)
	}
	if rec, out := op.json("PUT", "/api/v1/pricebooks/"+negotiatedID+"/public", map[string]any{"public": true}); rec.Code != 409 || !strings.Contains(fmt.Sprint(out["error"]), "NC list 2026") {
		t.Fatalf("second public book = %d %v, want 409 naming the current one", rec.Code, out)
	}
	plansBook, err := st.GetPriceBookByName(ctx, store.PlanBookName)
	if err != nil {
		t.Fatal(err)
	}
	if rec, out := op.json("PUT", "/api/v1/pricebooks/"+plansBook.ID+"/public", map[string]any{"public": true}); rec.Code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "only a cloud price book") {
		t.Fatalf("platform book public = %d %v", rec.Code, out)
	}

	// ── the catalog: the public book, the plans, the pay-per-use rates ───
	catRec, cat := getExact(t, anon, "/api/v1/public/catalog", 200)
	if book := cat["price_book"].(map[string]any); book["id"] != listID || book["name"] != "NC list 2026" || book["updated_at"] == nil {
		t.Fatalf("catalog price_book = %+v", book)
	}
	if cat["currency"] != "OMR" || dec(t, cat["tax_rate"]) != "0.0500" || cat["list_prices"] != true || dec(t, cat["hours_per_month"]) != "730" {
		t.Fatalf("catalog header = currency %v tax_rate %s hours %s", cat["currency"], dec(t, cat["tax_rate"]), dec(t, cat["hours_per_month"]))
	}
	if !strings.Contains(cat["notice"].(string), "List prices") {
		t.Fatalf("notice = %v", cat["notice"])
	}
	skus := map[string]map[string]any{}
	for _, s := range cat["skus"].([]any) {
		m := s.(map[string]any)
		skus[m["sku"].(string)] = m
	}
	if len(skus) != 2 {
		t.Fatalf("catalog carries %d SKUs, want only the public book's two: %v", len(skus), skus)
	}
	ecs := skus["ecs.s6.large.2"]
	if dec(t, ecs["unit_price"]) != "0.10000000" || dec(t, ecs["monthly"]) != "73.000000" || ecs["service"] != "ecs" || ecs["unit"] != "instance-hour" {
		t.Fatalf("catalog ecs = %+v", ecs)
	}
	if evs := skus["evs.ssd.gb"]; dec(t, evs["unit_price"]) != "0.00013699" || dec(t, evs["monthly"]) != "0.100003" {
		t.Fatalf("catalog evs = %+v", evs)
	}
	// The negotiated book's cheaper rate is nowhere in the document.
	if body := catRec.Body.String(); strings.Contains(body, "0.04000000") || strings.Contains(body, "Acme negotiated 2026") {
		t.Fatalf("the catalog leaks the negotiated book: %s", body)
	}
	plans := cat["plans"].([]any)
	if len(plans) != 4 {
		t.Fatalf("plans = %v, want S/M/L/XL (flexi is pay per use)", plans)
	}
	var planM map[string]any
	for _, p := range plans {
		if m := p.(map[string]any); m["slug"] == "m" {
			planM = m
		}
	}
	if planM == nil || planM["name"] != "M" || dec(t, planM["vcpu"]) != "4" || dec(t, planM["memory_gib"]) != "8" || dec(t, planM["monthly"]) != "9.000002" {
		t.Fatalf("plan M = %+v", planM)
	}
	if payg := cat["payg"].([]any); len(payg) != 3 {
		t.Fatalf("payg rates = %v, want the three k8s meters", payg)
	}
	if regions := cat["regions"].([]any); len(regions) != 1 || regions[0] != "me-east-215-a" {
		t.Fatalf("regions = %v", regions)
	}

	// ── estimate math equals invoice math ────────────────────────────────
	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08"}, 200)
	var stmtID string
	for _, r := range run["results"].([]any) {
		if m := r.(map[string]any); m["customer_id"] == acmeID {
			stmtID = m["statement_id"].(string)
		}
	}
	if stmtID == "" {
		t.Fatalf("no statement for acme: %v", run["results"])
	}
	_, stmt := getExact(t, op, "/api/v1/statements/"+stmtID, 200)
	invoiceLines := map[string]map[string]any{}
	for _, l := range stmt["lines"].([]any) {
		m := l.(map[string]any)
		invoiceLines[m["sku"].(string)] = m
	}
	if len(invoiceLines) != 2 {
		t.Fatalf("invoice lines = %v", stmt["lines"])
	}
	est := postExact(t, anon, "/api/v1/public/estimates", map[string]any{"region": "me-east-215-a", "lines": []map[string]any{
		{"sku": "ecs.s6.large.2", "quantity": "1", "hours_per_month": "730"},
		{"sku": "evs.ssd.gb", "quantity": "100", "hours_per_month": "730"},
	}}, 201)
	estLines := map[string]map[string]any{}
	for _, l := range est["lines"].([]any) {
		m := l.(map[string]any)
		estLines[m["sku"].(string)] = m
	}
	if len(estLines) != 2 {
		t.Fatalf("estimate lines = %v", est["lines"])
	}
	for sku, inv := range invoiceLines {
		e, ok := estLines[sku]
		if !ok {
			t.Fatalf("the estimate priced no %s", sku)
		}
		if e["unit"] != inv["unit"] {
			t.Fatalf("%s unit: estimate %v ≠ invoice %v", sku, e["unit"], inv["unit"])
		}
		for _, key := range []string{"unit_price", "amount"} {
			if dec(t, e[key]) != dec(t, inv[key]) {
				t.Fatalf("%s %s: estimate %s ≠ invoice %s", sku, key, dec(t, e[key]), dec(t, inv[key]))
			}
		}
		if dec(t, e["rated_quantity"]) != dec(t, inv["quantity"]) {
			t.Fatalf("%s quantity: estimate %s ≠ invoice %s", sku, dec(t, e["rated_quantity"]), dec(t, inv["quantity"]))
		}
	}
	for _, key := range []string{"subtotal", "tax", "total", "tax_rate"} {
		if dec(t, est[key]) != dec(t, stmt[key]) {
			t.Fatalf("%s: estimate %s ≠ invoice %s", key, dec(t, est[key]), dec(t, stmt[key]))
		}
	}
	// The exact figures, so a change in either engine is visible here:
	// 730 × 0.1 = 73; 73000 × 0.00013699 = 10.00027; 5 % of 83.00027.
	if dec(t, est["subtotal"]) != "83.000270" || dec(t, est["tax"]) != "4.150014" || dec(t, est["total"]) != "87.150284" {
		t.Fatalf("estimate totals = %s / %s / %s", dec(t, est["subtotal"]), dec(t, est["tax"]), dec(t, est["total"]))
	}
	// One month of lines: monthly is the total, yearly is 12 × monthly.
	if dec(t, est["monthly"]) != dec(t, est["total"]) || dec(t, est["yearly"]) != "1045.803408" {
		t.Fatalf("monthly/yearly = %s / %s", dec(t, est["monthly"]), dec(t, est["yearly"]))
	}
	created, err := time.Parse(time.RFC3339, est["created_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339, est["valid_until"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d := until.Sub(created); d != 30*24*time.Hour {
		t.Fatalf("validity = %v, want 30 days", d)
	}
	estID := est["id"].(string)
	if est["share_url"] != "https://billing.t99.omani.works/estimate/"+estID || est["lead"] != false || est["list_prices"] != true {
		t.Fatalf("estimate document = %+v", est)
	}
	// The shareable link needs no session and carries the same numbers.
	_, shared := getExact(t, anonClient(t, h), "/api/v1/public/estimates/"+estID, 200)
	if dec(t, shared["total"]) != dec(t, est["total"]) || shared["id"] != estID || len(shared["lines"].([]any)) != 2 {
		t.Fatalf("shared estimate = %+v", shared)
	}
	if rec, _ := anon.do("GET", "/api/v1/public/estimates/00000000-0000-0000-0000-000000000000", "", nil); rec.Code != 404 {
		t.Fatalf("unknown estimate = %d", rec.Code)
	}
	// preview=1 prices without saving.
	prev := postExact(t, anonClient(t, h), "/api/v1/public/estimates?preview=1", map[string]any{"lines": []map[string]any{{"sku": "ecs.s6.large.2", "quantity": "1", "hours_per_month": "730"}}}, 200)
	if dec(t, prev["total"]) != "76.650000" || prev["id"] != "" {
		t.Fatalf("preview = %+v", prev)
	}

	// ── the control: a discount moves the invoice, never the estimate ────
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "Launch campaign", "kind": "percent", "value": 20}, 201)
	run = op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08"}, 200)
	for _, r := range run["results"].([]any) {
		if m := r.(map[string]any); m["customer_id"] == acmeID {
			stmtID = m["statement_id"].(string)
		}
	}
	_, discounted := getExact(t, op, "/api/v1/statements/"+stmtID, 200)
	if dec(t, discounted["discount_total"]) != "16.600054" || dec(t, discounted["subtotal"]) != "66.400216" {
		t.Fatalf("the campaign must reduce the invoice: discount %s subtotal %s", dec(t, discounted["discount_total"]), dec(t, discounted["subtotal"]))
	}
	again := postExact(t, anonClient(t, h), "/api/v1/public/estimates", map[string]any{"lines": []map[string]any{
		{"sku": "ecs.s6.large.2", "quantity": "1", "hours_per_month": "730"},
		{"sku": "evs.ssd.gb", "quantity": "100", "hours_per_month": "730"},
	}}, 201)
	if dec(t, again["subtotal"]) != "83.000270" || dec(t, again["total"]) != "87.150284" {
		t.Fatalf("a discount reached the public estimate: %s / %s", dec(t, again["subtotal"]), dec(t, again["total"]))
	}

	// ── plans, pay per use, refusals ─────────────────────────────────────
	planEst := postExact(t, anonClient(t, h), "/api/v1/public/estimates", map[string]any{"lines": []map[string]any{{"plan": "m", "months": 3}}}, 201)
	line := planEst["lines"].([]any)[0].(map[string]any)
	if line["plan"] != "m" || line["sku"] != "plan.m" || dec(t, line["rated_quantity"]) != "2190.000000" {
		t.Fatalf("plan line = %+v", line)
	}
	if dec(t, planEst["total"]) != "28.350006" || dec(t, planEst["monthly"]) != "9.450002" {
		t.Fatalf("plan estimate = %s / %s", dec(t, planEst["total"]), dec(t, planEst["monthly"]))
	}
	paygEst := postExact(t, anonClient(t, h), "/api/v1/public/estimates", map[string]any{"lines": []map[string]any{{"sku": store.SKUVCPU, "quantity": "4", "hours_per_month": "730"}}}, 201)
	if dec(t, paygEst["subtotal"]) != "8.000012" {
		t.Fatalf("pay-per-use estimate = %s", dec(t, paygEst["subtotal"]))
	}
	for _, bad := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"lines": []map[string]any{{"sku": "ecs.nope"}}}, `unknown sku "ecs.nope"`},
		{map[string]any{"lines": []map[string]any{}}, "at least one line"},
		{map[string]any{"lines": []map[string]any{{"sku": "ecs.s6.large.2", "quantity": "0"}}}, "quantity must be more"},
		{map[string]any{"lines": []map[string]any{{"plan": "flexi"}}}, "pay per use"},
		{map[string]any{"currency": "USD", "lines": []map[string]any{{"sku": "ecs.s6.large.2"}}}, "not converted"},
		{map[string]any{"contact_email": "nope", "lines": []map[string]any{{"sku": "ecs.s6.large.2"}}}, "valid email"},
	} {
		rec, out := anonClient(t, h).json("POST", "/api/v1/public/estimates", bad.body)
		if rec.Code != 400 || !strings.Contains(fmt.Sprint(out["error"]), bad.want) {
			t.Fatalf("%v = %d %v, want 400 naming %q", bad.body, rec.Code, out, bad.want)
		}
	}
	many := make([]map[string]any, estimateMaxLines+1)
	for i := range many {
		many[i] = map[string]any{"sku": "ecs.s6.large.2"}
	}
	if rec, out := anonClient(t, h).json("POST", "/api/v1/public/estimates", map[string]any{"lines": many}); rec.Code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "at most 200 lines") {
		t.Fatalf("201 lines = %d %v", rec.Code, out)
	}

	// ── leads ────────────────────────────────────────────────────────────
	lead := postExact(t, anonClient(t, h), "/api/v1/public/estimates", map[string]any{"contact_email": " Buyer@Example.com ", "lines": []map[string]any{{"sku": "ecs.s6.large.2"}}}, 201)
	if lead["lead"] != true {
		t.Fatalf("an address must make it a lead: %+v", lead)
	}
	if _, present := lead["contact_email"]; present {
		t.Fatalf("the public document must never echo the address: %+v", lead)
	}
	if _, shared := getExact(t, anonClient(t, h), "/api/v1/public/estimates/"+lead["id"].(string), 200); shared["contact_email"] != nil {
		t.Fatalf("the shared document leaks the address: %+v", shared)
	}
	if rec, _ := anonClient(t, h).do("GET", "/api/v1/leads", "", nil); rec.Code != 401 {
		t.Fatalf("anonymous leads = %d, want 401", rec.Code)
	}
	_, leadsDoc := getExact(t, op, "/api/v1/leads", 200)
	leads := leadsDoc["leads"].([]any)
	if len(leads) != 1 {
		t.Fatalf("leads = %d rows, want only the one estimate with an address", len(leads))
	}
	l := leads[0].(map[string]any)
	if l["contact_email"] != "buyer@example.com" || l["id"] != lead["id"] || l["share_url"] == "" || dec(t, l["monthly"]) != "76.650000" {
		t.Fatalf("lead row = %+v", l)
	}
	// Leads are customers.manage: a customer principal is refused.
	op.mustJSON("POST", "/api/v1/customers/"+acmeID+"/users", map[string]string{"email": "owner@acme.example", "role": "admin"}, 201)
	cust := &client{t: t, h: h}
	cust.signIn("owner@acme.example", mail)
	if rec, _ := cust.do("GET", "/api/v1/leads", "", nil); rec.Code != 403 {
		t.Fatalf("customer leads = %d, want 403", rec.Code)
	}
}

// The budget: the configured number of requests a minute per address, then
// 429 with Retry-After. Another address has its own budget, and the
// authenticated API never draws on it.
func TestIntegrationPublicCalculatorRateLimit(t *testing.T) {
	st := testdb.Open(t)
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	h := publicHandler(t, st, nil, 3, func() time.Time { return now })
	call := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/public/catalog", nil)
		req.RemoteAddr = ip + ":5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// A 404 (no public book here) is still a served request: the budget is
	// spent before the handler decides anything.
	for i := 0; i < 3; i++ {
		if rec := call("198.51.100.10"); rec.Code == 429 {
			t.Fatalf("request %d within the budget was limited", i+1)
		}
	}
	rec := call("198.51.100.10")
	if rec.Code != 429 {
		t.Fatalf("fourth request = %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "20" {
		t.Fatalf("Retry-After = %q, want 20 (one token per 20s at 3/min)", ra)
	}
	if !strings.Contains(rec.Body.String(), "too many requests") {
		t.Fatalf("429 body = %s", rec.Body.String())
	}
	if rec := call("198.51.100.11"); rec.Code == 429 {
		t.Fatal("another address has its own budget")
	}
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/v1/pricebooks", nil)
		req.RemoteAddr = "198.51.100.10:5000"
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code == 429 {
			t.Fatal("the authenticated API must not draw on the public budget")
		}
	}
}

// CORS and framing: the configured origin is answered (preflight included),
// another is not, the page is framed only by that origin, and a public route
// neither reads nor sets a session cookie.
func TestIntegrationPublicCalculatorCORSAndNoSession(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC list 2026"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, pb.ID, []store.PriceItem{{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "0.10000000"}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPriceBookPublic(ctx, pb.ID, true); err != nil {
		t.Fatal(err)
	}
	h := publicHandler(t, st, []string{"https://www.omantel.om"}, 60, nil)
	call := func(method, path, origin, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		// A stale session cookie must be ignored, never looked up.
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-session"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := call("GET", "/api/v1/public/catalog", "https://www.omantel.om", "")
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://www.omantel.om" {
		t.Fatalf("allowed origin = %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatalf("Vary = %q", rec.Header().Get("Vary"))
	}
	if rec := call("GET", "/api/v1/public/catalog", "https://evil.example", ""); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("an unlisted origin must not be allowed: %v", rec.Header())
	}
	pre := call("OPTIONS", "/api/v1/public/estimates", "https://www.omantel.om", "")
	if pre.Code != 204 || !strings.Contains(pre.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("preflight = %d %v", pre.Code, pre.Header())
	}
	post := call("POST", "/api/v1/public/estimates", "https://www.omantel.om", `{"lines":[{"sku":"ecs.s6.large.2"}]}`)
	if post.Code != 201 {
		t.Fatalf("estimate with a stale cookie = %d %s", post.Code, post.Body.String())
	}
	for _, c := range post.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Fatal("a public route must never set the session cookie")
		}
	}
	if csp := call("GET", "/estimate", "", "").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "https://www.omantel.om") {
		t.Fatalf("estimate CSP = %q", csp)
	}
	if xfo := call("GET", "/overview", "", "").Header().Get("X-Frame-Options"); xfo != "DENY" {
		t.Fatalf("console X-Frame-Options = %q", xfo)
	}
}
