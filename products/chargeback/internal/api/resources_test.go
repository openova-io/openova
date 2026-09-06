package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Resources over HTTP (#6867): routes, scope, CSV — the store's arithmetic
// is pinned in store/resources_integration_test.go.

type resSeed struct {
	a, b       store.Customer
	srcA, srcB store.CostSource
}

// seedResourcesAPI: customer A (admin a@acme.test) owns vm-1 (ECS, 36) and
// vol-1 (EVS, 7.2); customer B (b@bravo.test) owns eip-1 (1.44). Window
// 2026-09-01..03.
func seedResourcesAPI(t *testing.T, st *store.Store) resSeed {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR"})
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
	a, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.test", PriceBookID: book.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.test", PriceBookID: book.ID})
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
	seen := time.Date(2026, 9, 3, 23, 0, 0, 0, time.UTC)
	if _, err := st.UpsertInventory(ctx, srcA.ID, []store.InventoryUpsert{
		{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"status": "ACTIVE"}, SeenAt: seen},
		{ResourceID: "vol-1", Kind: "evs", Name: "data-vol", Attrs: map[string]any{"size_gb": 100}, SeenAt: seen},
		{ResourceID: "ns/pod-1", Kind: "k8s-pod", Name: "pod-1", Attrs: map[string]any{}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertInventory(ctx, srcB.ID, []store.InventoryUpsert{
		{ResourceID: "eip-1", Kind: "eip", Name: "1.2.3.4", Attrs: map[string]any{"status": "ACTIVE"}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit, qty string, at time.Time) {
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(qty), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: json.RawMessage(`{}`)})
	}
	for d := 1; d <= 3; d++ {
		for h := 0; h < 24; h++ {
			at := time.Date(2026, 9, d, h, 0, 0, 0, time.UTC)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", "1", at)
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", "100", at)
			rec(a, srcA, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", "0.5", at)
			rec(b, srcB, "eip-1", "eip", "eip", "hour", "1", at)
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return resSeed{a: a, b: b, srcA: srcA, srcB: srcB}
}

func getRaw(t *testing.T, h http.Handler, path, email string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const resWin = "from=2026-09-01&to=2026-09-04"

func TestResourcesListDetailAndCSV(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	s := seedResourcesAPI(t, st)

	code, body := getJSON(t, h, "/api/v1/resources?"+resWin, opEmail)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	for _, k := range []string{"rows", "total", "sum_cost", "limit", "offset", "currency"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("list lacks %q", k)
		}
	}
	rows := body["rows"].([]any)
	if body["total"].(float64) != 4 || len(rows) != 4 || body["sum_cost"].(float64) != 44.64 || body["limit"].(float64) != 50 || body["currency"] != "OMR" {
		t.Fatalf("list = total %v rows %d sum %v limit %v currency %v", body["total"], len(rows), body["sum_cost"], body["limit"], body["currency"])
	}
	first := rows[0].(map[string]any)
	for _, k := range []string{"source_id", "resource_id", "kind", "name", "region", "customer_id", "customer_name", "status", "first_seen", "last_seen", "deleted_at", "cost", "currency", "lines"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("row lacks %q: %+v", k, first)
		}
	}
	if first["resource_id"] != "vm-1" || first["cost"].(float64) != 36 || first["status"] != "live" {
		t.Fatalf("first row (cost desc) = %+v", first)
	}
	line := first["lines"].([]any)[0].(map[string]any)
	if line["sku"] != "ecs.m7n.xlarge.8" || line["quantity"].(float64) != 72 || line["cost"].(float64) != 36 || line["unit"] != "instance-hour" {
		t.Fatalf("line = %+v", line)
	}

	// Filters and paging through the query string; the customer filter.
	if _, body := getJSON(t, h, "/api/v1/resources?"+resWin+"&kind=evs", opEmail); body["total"].(float64) != 1 {
		t.Fatalf("kind=evs total = %v", body["total"])
	}
	if _, body := getJSON(t, h, "/api/v1/resources?"+resWin+"&q=POD", opEmail); body["total"].(float64) != 1 {
		t.Fatalf("q=POD total = %v", body["total"])
	}
	if _, body := getJSON(t, h, "/api/v1/resources?"+resWin+"&customer="+s.b.ID, opEmail); body["total"].(float64) != 1 || body["sum_cost"].(float64) != 1.44 {
		t.Fatalf("customer=B = %+v", body)
	}
	_, body = getJSON(t, h, "/api/v1/resources?"+resWin+"&limit=2&offset=2&sort=name&order=asc", opEmail)
	if body["total"].(float64) != 4 || len(body["rows"].([]any)) != 2 || body["offset"].(float64) != 2 || body["limit"].(float64) != 2 {
		t.Fatalf("page = %+v", body)
	}
	// Bad parameters are 400, never silently defaulted.
	for _, bad := range []string{"status=gone", "sort=colour", "order=sideways", "limit=0", "limit=501", "offset=-1", "from=2026-09-04&to=2026-09-01", "from=yesterday"} {
		if code, _ := getJSON(t, h, "/api/v1/resources?"+bad, opEmail); code != 400 {
			t.Fatalf("%s: got %d, want 400", bad, code)
		}
	}
	// Default window is the last 30 days: nothing of September 2026 in it
	// unless the clock says so, so total is 4 rows with zero cost.
	if _, body := getJSON(t, h, "/api/v1/resources", opEmail); body["total"].(float64) != 4 {
		t.Fatalf("default window total = %v", body["total"])
	}

	// Detail: a slash in the resource id survives the route.
	code, body = getJSON(t, h, "/api/v1/resources/"+s.srcA.ID+"/ns/pod-1?"+resWin, opEmail)
	if code != 200 || body["resource_id"] != "ns/pod-1" || body["kind"] != "k8s-pod" {
		t.Fatalf("detail with slash id: %d %+v", code, body)
	}
	code, body = getJSON(t, h, "/api/v1/resources/"+s.srcA.ID+"/"+url.PathEscape("vm-1")+"?"+resWin, opEmail)
	if code != 200 {
		t.Fatalf("detail: %d %+v", code, body)
	}
	for _, k := range []string{"daily", "attrs", "transitions", "records_recent", "lines", "cost", "from", "to"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("detail lacks %q", k)
		}
	}
	daily := body["daily"].([]any)
	if len(daily) != 3 || daily[0].(map[string]any)["day"] != "2026-09-01" || daily[0].(map[string]any)["cost"].(float64) != 12 || daily[0].(map[string]any)["has_data"] != true {
		t.Fatalf("daily = %+v", daily)
	}
	if body["attrs"].(map[string]any)["status"] != "ACTIVE" || len(body["records_recent"].([]any)) != 48 {
		t.Fatalf("detail attrs/records = %+v / %d", body["attrs"], len(body["records_recent"].([]any)))
	}
	if code, _ := getJSON(t, h, "/api/v1/resources/"+s.srcA.ID+"/nope?"+resWin, opEmail); code != 404 {
		t.Fatalf("unknown resource: %d", code)
	}

	// CSV: the documented columns, one line per row, every row (not a page).
	rec := getRaw(t, h, "/api/v1/resources.csv?"+resWin+"&limit=1", opEmail)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Header().Get("Content-Disposition"), "resources-2026-09-01-2026-09-04.csv") {
		t.Fatalf("csv = %d %q", rec.Code, rec.Header())
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if lines[0] != "customer,kind,resource_id,name,region,status,first_seen,last_seen,cost,currency" {
		t.Fatalf("csv header = %q", lines[0])
	}
	if len(lines) != 5 {
		t.Fatalf("csv rows = %d, want 4 (+ header) — the export must not stop at one page:\n%s", len(lines)-1, rec.Body.String())
	}
	if !strings.HasPrefix(lines[1], "Acme,ecs,vm-1,web-1,me-east-1,live,2026-09-03T23:00:00Z,2026-09-03T23:00:00Z,36.000000,OMR") {
		t.Fatalf("csv first row = %q", lines[1])
	}
	// Customer-scoped CSV carries only that customer.
	rec = getRaw(t, h, "/api/v1/customers/"+s.b.ID+"/resources.csv?"+resWin, opEmail)
	if rec.Code != 200 || strings.Count(rec.Body.String(), "\n") != 2 || !strings.Contains(rec.Body.String(), "Bravo,eip,eip-1") {
		t.Fatalf("customer csv = %d %q", rec.Code, rec.Body.String())
	}
}

// Customer B must never see A's resources: not in a list, not by id, not
// through the operator route, not in a CSV.
func TestResourcesScopeCannotLeak(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	s := seedResourcesAPI(t, st)
	bEmail := "b@bravo.test"

	// Own list: B's row only, and it is priced.
	code, body := getJSON(t, h, "/api/v1/customers/"+s.b.ID+"/resources?"+resWin, bEmail)
	if code != 200 || body["total"].(float64) != 1 || body["rows"].([]any)[0].(map[string]any)["resource_id"] != "eip-1" || body["sum_cost"].(float64) != 1.44 {
		t.Fatalf("B own list = %d %+v", code, body)
	}
	// A's list by id: 404, so the id is not confirmed.
	if code, _ := getJSON(t, h, "/api/v1/customers/"+s.a.ID+"/resources?"+resWin, bEmail); code != 404 {
		t.Fatalf("B listing A: %d, want 404", code)
	}
	// The operator route: 403 whatever filter is passed.
	if code, _ := getJSON(t, h, "/api/v1/resources?"+resWin+"&customer="+s.b.ID, bEmail); code != 403 {
		t.Fatalf("B on the operator list: %d, want 403", code)
	}
	if code, _ := getJSON(t, h, "/api/v1/resources.csv?"+resWin, bEmail); code != 403 {
		t.Fatalf("B on the operator csv: %d, want 403", code)
	}
	// A's resource by its real ids: 404.
	if code, _ := getJSON(t, h, "/api/v1/resources/"+s.srcA.ID+"/vm-1?"+resWin, bEmail); code != 404 {
		t.Fatalf("B reading A's resource: %d, want 404", code)
	}
	// B's own resource: 200.
	if code, body := getJSON(t, h, "/api/v1/resources/"+s.srcB.ID+"/eip-1?"+resWin, bEmail); code != 200 || body["cost"].(float64) != 1.44 {
		t.Fatalf("B reading its own resource: %d %+v", code, body)
	}
	// A's CSV: 404.
	if rec := getRaw(t, h, "/api/v1/customers/"+s.a.ID+"/resources.csv?"+resWin, bEmail); rec.Code != 404 {
		t.Fatalf("B exporting A: %d", rec.Code)
	}
	// Unauthenticated: 401 everywhere.
	for _, p := range []string{"/api/v1/resources", "/api/v1/resources.csv", "/api/v1/customers/" + s.a.ID + "/resources", "/api/v1/resources/" + s.srcA.ID + "/vm-1"} {
		if code, _ := getJSON(t, h, p+"?"+resWin, ""); code != 401 {
			t.Fatalf("%s unauthenticated: %d", p, code)
		}
	}
}
