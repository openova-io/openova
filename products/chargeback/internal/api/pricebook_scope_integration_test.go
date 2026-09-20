package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Who may READ a price book (#6937, DESIGN.md §10.2 and §13.7).
//
// The defect this pins was found live: a customer-scoped principal — one
// binding, `customer:<id>`, correctly refused from partners, capacity, tax
// and finance — read every price book, the list and the items, and among
// them the retail book derived for the reseller it buys through, with that
// reseller's unit prices byte for byte as the operator sees them. The three
// read routes asked `requireAuth`, which proves only that the caller is
// signed in.
//
// The assertions below are on the RESPONSE — status and body — for each
// principal in turn, never on a helper's return value: the guard is only
// worth what the wire says.

// theirsAdmin is the second partner's customer, so the refusal is proven for
// a customer of ANOTHER partner as well as for the reseller's own.
const theirsAdmin = "t@theirs.example"

func TestIntegrationPriceBookReadsAreSovereignOnly(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)

	// The reseller's derived retail book: list 100 → buy 70 (Gold, 30 %) →
	// retail 73.5 at +5 %. A real price_books row, and the exact document
	// the live walk read as a customer.
	doc := op.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "buy", "markup_pct": "5"}, 200)
	retailID := doc["books"].([]any)[0].(map[string]any)["book"].(map[string]any)["id"].(string)
	// And a negotiated per-account book, the other thing a customer must
	// never read: what somebody else pays.
	negotiated := op.mustJSON("POST", "/api/v1/pricebooks/"+s.bookID+"/clone", map[string]string{"name": "Theirs negotiated"}, 201)
	negotiatedID := negotiated["id"].(string)

	// ── the operator IS served ───────────────────────────────────────────
	list := op.must("GET", "/api/v1/pricebooks", 200)
	if len(list["pricebooks"].([]any)) != 3 {
		t.Fatalf("operator price books = %v", list["pricebooks"])
	}
	book := op.must("GET", "/api/v1/pricebooks/"+retailID, 200)
	items := book["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["unit_price"] != 73.5 {
		t.Fatalf("operator retail book items = %v", book["items"])
	}
	rec, _ := op.do("GET", "/api/v1/pricebooks/"+retailID+"/export.csv", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "73.5") {
		t.Fatalf("operator export = %d %s", rec.Code, rec.Body.String())
	}

	// ── a CUSTOMER principal is refused, on all three routes ─────────────
	cust := &client{t: t, h: h}
	me := cust.signIn(alphaAdmin, mail)
	// The principal under test really is customer-scoped and nothing else —
	// without this the refusals below could be a refusal of somebody weaker
	// than the one the defect was reported for.
	if got := strs(me["scopes"]); len(got) != 1 || got[0] != "customer:"+s.alphaID {
		t.Fatalf("the principal under test has scopes %v, want customer:%s only", me["scopes"], s.alphaID)
	}
	if me["role"] != store.RoleCustomerAdmin {
		t.Fatalf("role = %v", me["role"])
	}
	refused := func(c *client, path string) {
		t.Helper()
		rec, out := c.do("GET", path, "", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
		// Nothing priced may ride along on the refusal.
		if strings.Contains(rec.Body.String(), "73.5") || strings.Contains(rec.Body.String(), "100") {
			t.Fatalf("GET %s refused but answered with prices: %s", path, rec.Body.String())
		}
		if out != nil && !strings.Contains(out["error"].(string), "metering.read") {
			t.Fatalf("GET %s refusal = %v, want the permission named", path, out)
		}
	}
	refused(cust, "/api/v1/pricebooks")
	refused(cust, "/api/v1/pricebooks/"+retailID)
	refused(cust, "/api/v1/pricebooks/"+retailID+"/export.csv")
	refused(cust, "/api/v1/pricebooks/"+s.bookID)
	refused(cust, "/api/v1/pricebooks/"+s.bookID+"/export.csv")
	refused(cust, "/api/v1/pricebooks/"+negotiatedID)
	// An id it does not hold is refused the same way, so the 403 never
	// confirms a book exists: the unknown one answers exactly as the real.
	refused(cust, "/api/v1/pricebooks/00000000-0000-0000-0000-000000000000")

	// A customer of the OTHER partner is refused the reseller's book too.
	theirs := &client{t: t, h: h}
	theirs.signIn(theirsAdmin, mail)
	refused(theirs, "/api/v1/pricebooks")
	refused(theirs, "/api/v1/pricebooks/"+retailID)

	// The control: the customer lens is otherwise untouched — its own
	// statements and its own account still answer 200.
	cust.must("GET", "/api/v1/customers/"+s.alphaID+"/statements", 200)
	cust.must("GET", "/api/v1/customers/"+s.alphaID, 200)

	// ── a PARTNER keeps its own derived book, and only that one ──────────
	owner := &client{t: t, h: h}
	owner.signIn(resellContact, mail)
	own := owner.must("GET", "/api/v1/pricebooks/"+retailID, 200)
	if own["items"].([]any)[0].(map[string]any)["unit_price"] != 73.5 {
		t.Fatalf("a partner lost its own retail book: %v", own)
	}
	if rec, _ := owner.do("GET", "/api/v1/pricebooks/"+retailID+"/export.csv", "", nil); rec.Code != 200 {
		t.Fatalf("a partner's export of its own book = %d", rec.Code)
	}
	refused(owner, "/api/v1/pricebooks")
	refused(owner, "/api/v1/pricebooks/"+s.bookID)
	refused(owner, "/api/v1/pricebooks/"+negotiatedID)

	// ── and no session at all is 401, not 403 ────────────────────────────
	anon := &client{t: t, h: h}
	if rec, _ := anon.do("GET", "/api/v1/pricebooks", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", rec.Code)
	}
	if rec, _ := anon.do("GET", "/api/v1/pricebooks/"+retailID, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous book = %d, want 401", rec.Code)
	}
}

// The funnel is the other half of the fix: a PUBLIC book is genuinely
// public, and the unauthenticated calculator reads it through its own route
// (GET /public/catalog, POST /public/estimates) — which never passes through
// the guard above. This test fails if the guard is ever widened to the
// public path, which would take the storefront down with it.
func TestIntegrationPriceBookGuardLeavesThePublicCalculatorOpen(t *testing.T) {
	h, _, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	book := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "NC list 2026", "scope": "cloud", "annual_divisor": 8760}, 201)
	bookID := book["id"].(string)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+bookID+"/items", map[string]any{
		"items": []map[string]any{{"sku": "ecs.s6.large.2", "unit": "instance-hour", "unit_price": "0.10000000"}},
	}, 200)
	if pub := op.mustJSON("PUT", "/api/v1/pricebooks/"+bookID+"/public", map[string]any{"public": true}, 200); pub["public"] != true {
		t.Fatalf("publish = %v", pub)
	}

	anon := &client{t: t, h: h}
	cat := anon.must("GET", "/api/v1/public/catalog", 200)
	if cat["price_book"].(map[string]any)["id"] != bookID || cat["list_prices"] != true {
		t.Fatalf("public catalog = %v", cat)
	}
	skus := cat["skus"].([]any)
	if len(skus) != 1 || skus[0].(map[string]any)["sku"] != "ecs.s6.large.2" {
		t.Fatalf("public catalog skus = %v", cat["skus"])
	}
	est := anon.mustJSON("POST", "/api/v1/public/estimates", map[string]any{"lines": []map[string]any{
		{"sku": "ecs.s6.large.2", "quantity": "1", "hours_per_month": "730"},
	}}, 201)
	if est["subtotal"] != 73.0 || est["id"] == "" {
		t.Fatalf("public estimate = %v", est)
	}
	if _, ok := anon.must("GET", "/api/v1/public/estimates/"+est["id"].(string), 200)["total"]; !ok {
		t.Fatal("the shared estimate lost its total")
	}
	// The same caller still has no way into the authenticated collection.
	if rec, _ := anon.do("GET", "/api/v1/pricebooks", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous price books = %d, want 401", rec.Code)
	}
}
