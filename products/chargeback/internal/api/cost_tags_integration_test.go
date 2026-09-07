package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The tag dimension over HTTP (EPIC #6867 follow-up): /cost/dimensions lists
// tag_keys and the grouped tag's values, /cost/explore groups and filters by
// `tag:<key>` (colon plain or URL-encoded), the customer lens sees only its
// own keys, and an invalid key is a 400 that names the rule.
func TestIntegrationCostTagDimensionsOverHTTP(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "1"}, {SKU: "eip", Unit: "hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	acme, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "admin@acme.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "beta", Name: "Beta", AdminEmail: "admin@beta.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, acme.ID, "huawei-project", "me-east-215", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, beta.ID, "huawei-project", "me-east-215", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	ws := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	rec := func(cust store.Customer, src store.CostSource, res, kind, sku, unit string, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku, Quantity: "1.000000", Unit: unit,
			WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215", Labels: lb})
	}
	for hr := 0; hr < 10; hr++ {
		at := ws.Add(time.Duration(hr) * time.Hour)
		rec(acme, srcA, "srv-1", "ecs", "ecs.s6.large.2", "instance-hour", at, map[string]any{"name": "web", "status": "ACTIVE", "tags": map[string]string{"team": "platform", "env": "prod"}, "enterprise_project": "ep-1"})
		rec(acme, srcA, "srv-2", "ecs", "ecs.s6.large.2", "instance-hour", at, map[string]any{"name": "batch", "status": "ACTIVE"})
		rec(beta, srcB, "eip-1", "eip", "eip", "hour", at, map[string]any{"name": "1.2.3.4", "tags": map[string]string{"owner": "beta-ops"}})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	window := "from=2026-09-01&to=2026-09-02"

	// Operator: every key across both customers; the grouped tag's values.
	dims := op.must("GET", "/api/v1/cost/dimensions?"+window+"&group_by=tag:team", 200)
	if keys, _ := dims["tag_keys"].([]any); len(keys) != 3 || keys[0] != "env" || keys[1] != "owner" || keys[2] != "team" {
		t.Fatalf("tag_keys = %v", dims["tag_keys"])
	}
	teams, _ := dims["dimensions"].(map[string]any)["tag:team"].([]any)
	if len(teams) != 2 || teams[0].(map[string]any)["key"] != store.TagUntagged || teams[1].(map[string]any)["key"] != "platform" {
		t.Fatalf("tag:team values = %v", teams)
	}
	if _, ok := dims["dimensions"].(map[string]any)["enterprise_project"]; !ok {
		t.Fatalf("enterprise_project dimension missing: %v", dims["dimensions"])
	}

	// Explore grouped by the tag: platform = 10 h × 1 = 10; untagged = srv-2 10
	// + eip 5 = 15. The group_by echoes the tag dimension name.
	ex := op.must("GET", "/api/v1/cost/explore?"+window+"&group_by=tag:team", 200)
	if ex["group_by"] != "tag:team" {
		t.Fatalf("group_by = %v", ex["group_by"])
	}
	totals := map[string]float64{}
	for _, g := range ex["groups"].([]any) {
		m := g.(map[string]any)
		totals[m["key"].(string)] = m["total"].(float64)
	}
	if totals["platform"] != 10 || totals[store.TagUntagged] != 15 || len(totals) != 2 {
		t.Fatalf("groups = %v", totals)
	}
	// URL-encoded colon in a filter; enterprise_project as group_by.
	ex = op.must("GET", "/api/v1/cost/explore?"+window+"&group_by=kind&tag%3Ateam=platform", 200)
	if ex["total"].(map[string]any)["current"].(float64) != 10 {
		t.Fatalf("tag filter total = %v", ex["total"])
	}
	ex = op.must("GET", "/api/v1/cost/explore?"+window+"&group_by=enterprise_project", 200)
	totals = map[string]float64{}
	for _, g := range ex["groups"].([]any) {
		m := g.(map[string]any)
		totals[m["key"].(string)] = m["total"].(float64)
	}
	if totals["ep-1"] != 10 || totals["(none)"] != 15 {
		t.Fatalf("enterprise_project groups = %v", totals)
	}
	// The CSV export names the tag dimension in its group_by column.
	csvRec, _ := op.do("GET", "/api/v1/cost/export.csv?"+window+"&group_by=tag:team", "", nil)
	if csvRec.Code != 200 || !strings.Contains(csvRec.Body.String(), "tag:team,platform,platform,") {
		t.Fatalf("csv = %d %s", csvRec.Code, csvRec.Body.String())
	}

	// Invalid keys: 400, message names the rule, in every position.
	for _, bad := range []string{
		"group_by=" + url.QueryEscape("tag:team' OR 1=1--"),
		"group_by=tag:",
		url.QueryEscape("tag:x'y") + "=1",
		url.QueryEscape("exclude_tag:x y") + "=1",
	} {
		rec, out := op.do("GET", "/api/v1/cost/explore?"+window+"&"+bad, "", nil)
		if rec.Code != 400 {
			t.Fatalf("%s → %d %s", bad, rec.Code, rec.Body.String())
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, store.TagKeyRule) {
			t.Fatalf("%s → message %q must name the rule", bad, msg)
		}
		if rec, _ := op.do("GET", "/api/v1/cost/dimensions?"+window+"&"+bad, "", nil); rec.Code != 400 {
			t.Fatalf("dimensions %s → %d", bad, rec.Code)
		}
	}

	// Customer lens: Beta sees only its own key and value, whatever it asks.
	cust := &client{t: t, h: h}
	cust.signIn("admin@beta.example", mail)
	bd := cust.must("GET", "/api/v1/customers/"+beta.ID+"/cost/dimensions?"+window+"&group_by=tag:team", 200)
	if keys, _ := bd["tag_keys"].([]any); len(keys) != 1 || keys[0] != "owner" {
		t.Fatalf("beta tag_keys = %v (acme's env/team must not leak)", bd["tag_keys"])
	}
	if teams, _ := bd["dimensions"].(map[string]any)["tag:team"].([]any); len(teams) != 1 || teams[0].(map[string]any)["key"] != store.TagUntagged {
		t.Fatalf("beta tag:team values = %v", teams)
	}
	be := cust.must("GET", "/api/v1/customers/"+beta.ID+"/cost/explore?"+window+"&group_by=tag:owner&tag:team=platform", 200)
	if len(be["groups"].([]any)) != 0 || be["total"].(map[string]any)["current"].(float64) != 0 {
		t.Fatalf("beta naming acme's tag value must see nothing: %v", be["groups"])
	}
	be = cust.must("GET", "/api/v1/customers/"+beta.ID+"/cost/explore?"+window+"&group_by=tag:owner", 200)
	if gs := be["groups"].([]any); len(gs) != 1 || gs[0].(map[string]any)["key"] != "beta-ops" || gs[0].(map[string]any)["total"].(float64) != 5 {
		t.Fatalf("beta by tag:owner = %v", be["groups"])
	}
	cust.must("GET", "/api/v1/cost/dimensions?"+window, 403)
}
