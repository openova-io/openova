package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// A partner opens the cost explorer across ITS customers (DESIGN.md §13.5) —
// the founder's reason for resellers: "so the resellers could see the cost
// analysis of their customers".
//
// The routes under test are the Sovereign-wide ones (`/cost/explore`,
// `/cost/summary`, `/cost/export.csv`, `/resources`, `/anomalies`,
// `/recommendations`), which a partner principal now reaches — confined, by
// the store scope, to the customers assigned to its partner. The ledger is
// uneven on purpose: One's customers cost 10 and 20, Two's 40, a direct
// customer 80. 30 is reachable only by seeing exactly One's two.

const (
	oneContact   = "ap@one.example"
	explWindow   = "from=2026-09-01&to=2026-09-08"
	explOneTotal = 30.0
)

type explorerSeed struct {
	oneID, twoID           string
	oneParty               string
	oneA, oneB, twoA, dirC string
}

func seedPartnerExplorer(t *testing.T, h http.Handler, st *store.Store, mail *recMail) (explorerSeed, *client) {
	t.Helper()
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	var s explorerSeed

	book := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "list", "scope": "cloud", "annual_divisor": 8760}, 201)
	bookID := book["id"].(string)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+bookID+"/items", map[string]any{
		"items": []map[string]any{{"sku": "ecs.m7n.xlarge.8", "unit": "instance-hour", "unit_price": "1"}},
	}, 200)

	one := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "one", "name": "One", "contact_email": oneContact}, 201)
	two := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "two", "name": "Two", "contact_email": "ap@two.example"}, 201)
	s.oneID, s.twoID = one["id"].(string), two["id"].(string)
	s.oneParty = one["party_customer_id"].(string)

	// hours is the record count and, at a unit price of 1, the cost.
	mk := func(slug, name, partnerID string, hours int) string {
		t.Helper()
		c := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": slug, "name": name, "admin_email": slug + "@example.com"}, 201)
		id := c["id"].(string)
		if partnerID != "" {
			op.mustJSON("PATCH", "/api/v1/customers/"+id, map[string]any{"partner_id": partnerID}, 200)
		}
		src, _, err := st.UpsertSource(ctx, id, "huawei-project", "me-east-215", "ok-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		assignBook(t, st, src.ID, bookID)
		from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{
			{ResourceID: "vm-" + slug, Kind: "ecs", Name: "vm " + name, Attrs: map[string]any{"status": "ACTIVE"}, Created: from, SeenAt: from.Add(time.Duration(hours) * time.Hour)},
		}); err != nil {
			t.Fatal(err)
		}
		var recs []store.UsageRecord
		for i := 0; i < hours; i++ {
			at := from.Add(time.Duration(i) * time.Hour)
			recs = append(recs, store.UsageRecord{
				CustomerID: id, SourceID: src.ID, ResourceID: "vm-" + slug, ResourceKind: "ecs",
				SKU: "ecs.m7n.xlarge.8", Quantity: "1.000000", Unit: "instance-hour",
				WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215",
			})
		}
		if _, err := st.UpsertUsage(ctx, recs); err != nil {
			t.Fatal(err)
		}
		return id
	}
	s.oneA = mk("one-a", "One A", s.oneID, 10)
	s.oneB = mk("one-b", "One B", s.oneID, 20)
	s.twoA = mk("two-a", "Two A", s.twoID, 40)
	s.dirC = mk("direct", "Direct", "", 80)
	return s, op
}

// groupKeys reads an explore document's group keys and their totals.
func groupKeys(t *testing.T, doc map[string]any) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, g := range doc["groups"].([]any) {
		row := g.(map[string]any)
		out[row["key"].(string)] = row["total"].(float64)
	}
	return out
}

func TestIntegrationPartnerReadsTheCostExplorerAcrossItsCustomers(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartnerExplorer(t, h, st, mail)

	owner := &client{t: t, h: h}
	if me := owner.signIn(oneContact, mail); me["role"] != store.RolePartnerOwner {
		t.Fatalf("role = %v, want partner-owner", me["role"])
	}

	// The explorer, grouped by customer: its own two and their exact total.
	doc := owner.must("GET", "/api/v1/cost/explore?"+explWindow+"&group_by=customer", 200)
	keys := groupKeys(t, doc)
	if len(keys) != 2 || keys[s.oneA] != 10 || keys[s.oneB] != 20 {
		t.Fatalf("a partner's group-by customer = %v, want One A 10 and One B 20", keys)
	}
	if _, ok := keys[s.twoA]; ok {
		t.Fatalf("a partner saw another partner's customer: %v", keys)
	}
	if _, ok := keys[s.dirC]; ok {
		t.Fatalf("a partner saw a direct customer: %v", keys)
	}
	if got := doc["total"].(map[string]any)["current"]; got != explOneTotal {
		t.Fatalf("a partner's window total = %v, want %v", got, explOneTotal)
	}

	// Naming another partner's customer by filter reaches nothing — and is
	// NOT quietly answered with the partner's own rows.
	doc = owner.must("GET", "/api/v1/cost/explore?"+explWindow+"&group_by=customer&customer="+s.twoA, 200)
	if keys := groupKeys(t, doc); len(keys) != 0 {
		t.Fatalf("a partner filtering for another's customer saw %v", keys)
	}
	if got := doc["total"].(map[string]any)["current"]; got != 0.0 {
		t.Fatalf("a foreign customer filter totalled %v, want 0", got)
	}
	// Naming it as a path: the id is not even confirmed.
	owner.must("GET", "/api/v1/customers/"+s.twoA+"/cost/explore?"+explWindow, 404)
	owner.must("GET", "/api/v1/customers/"+s.dirC+"/cost/summary", 404)

	// One of its OWN, named: that customer alone, not its whole book.
	doc = owner.must("GET", "/api/v1/customers/"+s.oneB+"/cost/explore?"+explWindow+"&group_by=customer", 200)
	if keys := groupKeys(t, doc); len(keys) != 1 || keys[s.oneB] != 20 {
		t.Fatalf("a partner on one of its own customers = %v, want One B 20", keys)
	}

	// The filter pickers offer its two customers and no others.
	dims := owner.must("GET", "/api/v1/cost/dimensions?"+explWindow, 200)
	if got := dims["dimensions"].(map[string]any)["customer"].([]any); len(got) != 2 {
		t.Fatalf("a partner's customer dimension = %v, want two", got)
	}

	// The CSV export is scoped the same way, down to the file's rows.
	rec, _ := owner.do("GET", "/api/v1/cost/export.csv?"+explWindow+"&group_by=customer", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a partner's CSV export = %d: %s", rec.Code, rec.Body.String())
	}
	csv := rec.Body.String()
	if !strings.Contains(csv, "One A") || !strings.Contains(csv, "One B") {
		t.Fatalf("a partner's CSV lacks its own customers:\n%s", csv)
	}
	if strings.Contains(csv, "Two A") || strings.Contains(csv, "Direct") || strings.Contains(csv, s.twoA) {
		t.Fatalf("a partner's CSV carried another partner's customer:\n%s", csv)
	}

	// The summary: its customers' costs, and none of the Sovereign's counts.
	sum := owner.must("GET", "/api/v1/cost/summary", 200)
	byCustomer := sum["by_customer"].([]any)
	names := map[string]bool{}
	for _, row := range byCustomer {
		names[row.(map[string]any)["name"].(string)] = true
	}
	if len(byCustomer) != 2 || !names["One A"] || !names["One B"] {
		t.Fatalf("a partner's summary by_customer = %v", byCustomer)
	}
	if got := sum["customers"].(map[string]any); len(got) != 0 {
		t.Fatalf("a partner read the Sovereign's customer counts: %v", got)
	}
	if got := sum["sources"].(map[string]any); len(got) != 0 {
		t.Fatalf("a partner read the Sovereign's source counts: %v", got)
	}
	if got := sum["resources_live"].(float64); got != 2 {
		t.Fatalf("a partner's live resources = %v, want its two", got)
	}

	// Resources, anomalies and recommendations ride on the same scope.
	res := owner.must("GET", "/api/v1/resources?"+explWindow, 200)
	if got := res["total"].(float64); got != 2 {
		t.Fatalf("a partner's resources = %v rows, want 2", got)
	}
	for _, row := range res["rows"].([]any) {
		if name := row.(map[string]any)["customer_name"].(string); name != "One A" && name != "One B" {
			t.Fatalf("a partner's resource list carried %q", name)
		}
	}
	owner.must("GET", "/api/v1/anomalies?"+explWindow, 200)
	recs := owner.must("GET", "/api/v1/recommendations", 200)
	// Its two customers and its own party account — the whole of its scope.
	mine := map[string]bool{"": true, s.oneA: true, s.oneB: true, s.oneParty: true}
	for _, row := range recs["rows"].([]any) {
		if id, ok := row.(map[string]any)["customer_id"].(string); ok && !mine[id] {
			t.Fatalf("a partner's recommendations named customer %s", id)
		}
	}

	// Saved views belong to the signed-in email, so a partner saves its own.
	owner.mustJSON("POST", "/api/v1/views", map[string]any{"name": "My customers by SKU", "page": "explore", "params": map[string]any{"group_by": "sku"}}, 201)
	if views := owner.must("GET", "/api/v1/views?page=explore", 200)["views"].([]any); len(views) != 1 {
		t.Fatalf("a partner's saved views = %v", views)
	}

	// The Sovereign's own picture is unchanged: every customer, every cost.
	doc = op.must("GET", "/api/v1/cost/explore?"+explWindow+"&group_by=customer", 200)
	if keys := groupKeys(t, doc); len(keys) != 4 {
		t.Fatalf("the operator's group-by customer = %v, want four customers", keys)
	}
	if got := op.must("GET", "/api/v1/cost/summary", 200)["customers"].(map[string]any); len(got) == 0 {
		t.Fatal("the operator lost its customer counts")
	}

	// And a CUSTOMER principal is still refused the cross-customer routes:
	// its lens is /customers/{id}/… .
	cust := &client{t: t, h: h}
	cust.signIn("one-a@example.com", mail)
	rec, out := cust.do("GET", "/api/v1/cost/explore?"+explWindow, "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "metering.read") {
		t.Fatalf("a customer principal on /cost/explore = %d %v, want 403", rec.Code, out)
	}
	cust.must("GET", "/api/v1/resources?"+explWindow, 403)
	cust.must("GET", "/api/v1/anomalies", 403)
	if keys := groupKeys(t, cust.must("GET", "/api/v1/customers/"+s.oneA+"/cost/explore?"+explWindow+"&group_by=customer", 200)); len(keys) != 1 || keys[s.oneA] != 10 {
		t.Fatalf("a customer principal's own explorer = %v, want itself at 10", keys)
	}
}
