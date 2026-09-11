package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The partners API end to end (DESIGN.md §Partners, EPIC #6867): what a
// Sovereign principal may do, what a partner owner may do on its OWN partner
// and nowhere else, and what a customer never sees.

const (
	resellContact = "ap@resell.example"
	otherContact  = "ap@other.example"
	alphaAdmin    = "a@alpha.example"
)

type partnerSeed struct {
	bookID          string
	tierID          string
	resellID        string
	resellPartyID   string
	otherID         string
	alphaID         string
	theirsID        string
	statementID     string
	partnerStmtID   string
	wholesaleTotals map[string]any
}

// seedPartners builds the fixture through the API where the API owns it and
// through the store where the store does (usage records have no endpoint).
func seedPartners(t *testing.T, h http.Handler, st *store.Store, mail *recMail) (partnerSeed, *client) {
	t.Helper()
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	var s partnerSeed

	// A list book at 100 per unit.
	book := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "NC list 2026", "scope": "cloud", "annual_divisor": 8760}, 201)
	s.bookID = book["id"].(string)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+s.bookID+"/items", map[string]any{
		"items": []map[string]any{{"sku": "ecs.m7n.xlarge.8", "unit": "instance-hour", "unit_price": "100"}},
	}, 200)

	// A tier, 30 % off list — a discount of the one engine.
	tier := op.mustJSON("POST", "/api/v1/partners/tiers", map[string]any{"name": "Gold", "description": "30 % off list"}, 201)
	s.tierID = tier["id"].(string)
	op.mustJSON("PUT", "/api/v1/partners/tiers/"+s.tierID+"/discounts", map[string]any{
		"discounts": []map[string]any{{"name": "Gold 30 %", "kind": "percent", "value": "30"}},
	}, 200)

	// Two partners: the one under test, and one it must never reach.
	resell := op.mustJSON("POST", "/api/v1/partners", map[string]any{
		"slug": "resell-co", "name": "Resell Co", "tier_id": s.tierID, "bill_to": "partner", "contact_email": resellContact,
	}, 201)
	s.resellID = resell["id"].(string)
	s.resellPartyID = resell["party_customer_id"].(string)
	other := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "other-co", "name": "Other Co", "contact_email": otherContact}, 201)
	s.otherID = other["id"].(string)

	// One customer each, assigned to their partner through the customer API.
	mk := func(slug, name, email, partnerID string) string {
		t.Helper()
		c := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": slug, "name": name, "admin_email": email}, 201)
		id := c["id"].(string)
		got := op.mustJSON("PATCH", "/api/v1/customers/"+id, map[string]any{"partner_id": partnerID}, 200)
		if got["partner_id"] != partnerID {
			t.Fatalf("%s partner_id = %v, want %s", slug, got["partner_id"], partnerID)
		}
		src, _, err := st.UpsertSource(ctx, id, "huawei-project", "me-east-215", "ok-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		assignBook(t, st, src.ID, s.bookID)
		aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{
			CustomerID: id, SourceID: src.ID, ResourceID: "vm-" + slug, ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8",
			Quantity: "1.000000", Unit: "instance-hour", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215",
		}}); err != nil {
			t.Fatal(err)
		}
		op.mustJSON("POST", "/api/v1/customers/"+id+"/discounts", map[string]any{"name": name + " 10 %", "kind": "percent", "value": "10"}, 201)
		return id
	}
	s.alphaID = mk("alpha", "Alpha", alphaAdmin, s.resellID)
	s.theirsID = mk("theirs", "Theirs", "t@theirs.example", s.otherID)

	// One run writes the customer statements AND the partner statements.
	op.mustJSON("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08"}, 200)
	list := op.must("GET", "/api/v1/customers/"+s.alphaID+"/statements", 200)
	s.statementID = list["statements"].([]any)[0].(map[string]any)["id"].(string)
	ps := op.must("GET", "/api/v1/partners/"+s.resellID+"/statements", 200)
	rows := ps["statements"].([]any)
	if len(rows) != 1 {
		t.Fatalf("partner statements = %v", ps["statements"])
	}
	s.wholesaleTotals = rows[0].(map[string]any)
	s.partnerStmtID = s.wholesaleTotals["id"].(string)
	// The operator client is returned so a test never signs the same address
	// in twice: the PIN endpoint throttles a repeat request, by design.
	return s, op
}

// The Sovereign side: the documents the console reads, with the waterfall on
// them, and the derived retail book with its below-buy warning.
func TestIntegrationPartnersSovereignShapesAndRetailRule(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)

	// The directory.
	dir := op.must("GET", "/api/v1/partners", 200)
	partners := dir["partners"].([]any)
	if len(partners) != 2 {
		t.Fatalf("partners = %v", dir["partners"])
	}
	p := op.must("GET", "/api/v1/partners/"+s.resellID, 200)
	for _, k := range []string{"id", "slug", "name", "tier_id", "bill_to", "status", "contact_email", "party_customer_id", "customer_count", "balance", "available_credit", "has_retail_rule"} {
		if _, ok := p[k]; !ok {
			t.Fatalf("the partner document lacks %q: %v", k, p)
		}
	}
	if p["bill_to"] != "partner" || p["tier_name"] != "Gold" || p["customer_count"] != 1.0 {
		t.Fatalf("partner document = %v", p)
	}

	// The customer statement carries the waterfall.
	stmt := op.must("GET", "/api/v1/statements/"+s.statementID, 200)
	if stmt["subtotal"] != 90.0 || stmt["buy_total"] != 70.0 || stmt["margin_total"] != 20.0 {
		t.Fatalf("customer statement: net %v buy %v margin %v, want 90 / 70 / 20", stmt["subtotal"], stmt["buy_total"], stmt["margin_total"])
	}
	if stmt["partner_id"] != s.resellID || stmt["statement_kind"] != "customer" {
		t.Fatalf("customer statement partner keys = %v %v", stmt["partner_id"], stmt["statement_kind"])
	}
	line := stmt["lines"].([]any)[0].(map[string]any)
	if line["list_amount"] != 100.0 || line["net_amount"] != 90.0 || line["buy_amount"] != 70.0 {
		t.Fatalf("line waterfall = %v", line)
	}

	// The partner's own wholesale statement.
	if s.wholesaleTotals["statement_kind"] != "wholesale" || s.wholesaleTotals["subtotal"] != 70.0 || s.wholesaleTotals["buy_total"] != 70.0 || s.wholesaleTotals["margin_total"] != 20.0 {
		t.Fatalf("wholesale statement = %v", s.wholesaleTotals)
	}

	// The margin report.
	margin := op.must("GET", "/api/v1/partners/"+s.resellID+"/margin?period=2026-08", 200)
	rows := margin["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("margin rows = %v", margin["rows"])
	}
	row := rows[0].(map[string]any)
	if row["service"] != "ecs" || row["customer_net"] != 90.0 || row["partner_buy"] != 70.0 || row["margin"] != 20.0 {
		t.Fatalf("margin row = %v", row)
	}
	if margin["totals"].(map[string]any)["margin"] != 20.0 {
		t.Fatalf("margin totals = %v", margin["totals"])
	}
	if _, err := time.Parse("2006-01", margin["period"].(string)); err != nil {
		t.Fatalf("margin period = %v", margin["period"])
	}

	// The retail rule: base = buy (70) + 5 % → 73.5, above the buy price.
	doc := op.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "buy", "markup_pct": "5"}, 200)
	books := doc["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("derived books = %v", doc["books"])
	}
	items := books[0].(map[string]any)["book"].(map[string]any)["items"].([]any)
	if items[0].(map[string]any)["unit_price"] != 73.5 {
		t.Fatalf("retail unit price = %v, want 73.5", items[0])
	}
	if len(doc["below_buy"].([]any)) != 0 {
		t.Fatalf("73.5 is above buy 70, but the response warns: %v", doc["below_buy"])
	}
	// A markup that prices BELOW the buy price is derived and WARNED about.
	doc = op.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "buy", "markup_pct": "-10"}, 200)
	below := doc["below_buy"].([]any)
	if len(below) != 1 {
		t.Fatalf("below_buy = %v, want one line", doc["below_buy"])
	}
	if b := below[0].(map[string]any); b["retail_unit_price"] != 63.0 || b["buy_unit_price"] != 70.0 {
		t.Fatalf("below-buy line = %v", b)
	}
	// The derived book is READ-ONLY: an edit is refused, with what to change.
	derivedID := doc["books"].([]any)[0].(map[string]any)["book"].(map[string]any)["id"].(string)
	rec, out := op.json("POST", "/api/v1/pricebooks/"+derivedID+"/items", map[string]any{"sku": "x", "unit": "hour", "unit_price": "1"})
	if rec.Code != http.StatusConflict || !strings.Contains(out["error"].(string), "retail rule") {
		t.Fatalf("editing a derived book = %d %v", rec.Code, out)
	}
	// And an invalid rule is refused by name.
	rec, out = op.json("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "cost", "markup_pct": "5"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "base") {
		t.Fatalf("an unknown base = %d %v", rec.Code, out)
	}

	// The partner's account is its party's ledger, through the same handler.
	acct := op.must("GET", "/api/v1/partners/"+s.resellID+"/account", 200)
	if acct["customer_id"] != s.resellPartyID {
		t.Fatalf("partner account = %v", acct)
	}
	for _, k := range []string{"balance", "available_credit", "outstanding", "entries"} {
		if _, ok := acct[k]; !ok {
			t.Fatalf("the partner account document lacks %q", k)
		}
	}

	// The partner's customers.
	cust := op.must("GET", "/api/v1/partners/"+s.resellID+"/customers", 200)
	if rows := cust["customers"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"] != s.alphaID {
		t.Fatalf("partner customers = %v", cust["customers"])
	}
}

// A partner owner: its own partner and nothing else.
func TestIntegrationPartnerOwnerScope(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, _ := seedPartners(t, h, st, mail)

	owner := &client{t: t, h: h}
	me := owner.signIn(resellContact, mail)
	if me["role"] != store.RolePartnerOwner {
		t.Fatalf("role = %v, want partner-owner", me["role"])
	}
	roles := me["roles"].([]any)
	if len(roles) != 1 {
		t.Fatalf("roles = %v", me["roles"])
	}
	b := roles[0].(map[string]any)
	if b["scope_kind"] != store.ScopeKindPartner || b["partner_id"] != s.resellID || b["partner_name"] != "Resell Co" {
		t.Fatalf("partner binding = %v", b)
	}
	// The scope key names the partner, and the permissions under it are the
	// partner-owner bundle.
	perms := permsOf(me, "partner:"+s.resellID)
	if !has(perms, "metering.read") || !has(perms, "partner.self.manage") || !has(perms, "account.topup") {
		t.Fatalf("partner-owner permissions = %v", perms)
	}
	if has(perms, "partners.manage") || has(perms, "billing.issue") || has(perms, "rating.manage") {
		t.Fatalf("a partner owner holds a Sovereign permission: %v", perms)
	}
	if got := strs(me["scopes"]); len(got) != 1 || got[0] != "partner:"+s.resellID {
		t.Fatalf("scopes = %v", me["scopes"])
	}

	// Its own customer: 200. Another partner's: 404 — the id is not confirmed.
	if c := owner.must("GET", "/api/v1/customers/"+s.alphaID, 200); c["id"] != s.alphaID {
		t.Fatalf("own customer = %v", c)
	}
	owner.must("GET", "/api/v1/customers/"+s.theirsID, 404)
	owner.must("GET", "/api/v1/partners/"+s.resellID, 200)
	owner.must("GET", "/api/v1/partners/"+s.otherID, 404)
	owner.must("GET", "/api/v1/partners/"+s.otherID+"/margin", 404)
	owner.mustJSON("PUT", "/api/v1/partners/"+s.otherID+"/retail-rule", map[string]any{"base": "list", "markup_pct": "5"}, 404)

	// The directory it reads is its own partner only.
	if dir := owner.must("GET", "/api/v1/partners", 200); len(dir["partners"].([]any)) != 1 {
		t.Fatalf("a partner's directory = %v", dir["partners"])
	}
	// And its customer directory is its own customers.
	if dir := owner.must("GET", "/api/v1/customers", 200); len(dir["customers"].([]any)) != 1 {
		t.Fatalf("a partner's customer directory = %v", dir["customers"])
	}

	// Provider-level surfaces are not a partner's: the list books that set
	// its buy price, and the tier catalogue.
	rec, out := owner.do("GET", "/api/v1/pricebooks", "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "metering.read") {
		t.Fatalf("provider books = %d %v, want 403", rec.Code, out)
	}
	if rec, _ := owner.do("GET", "/api/v1/pricebooks/"+s.bookID, "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("one provider book = %d, want 403", rec.Code)
	}
	if rec, _ := owner.do("GET", "/api/v1/partners/tiers", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("the tier catalogue = %d, want 403", rec.Code)
	}
	// It cannot create partners, tiers or price books either.
	owner.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "mine", "name": "Mine"}, 403)
	owner.mustJSON("POST", "/api/v1/partners/tiers", map[string]any{"name": "Mine"}, 403)
	owner.mustJSON("PATCH", "/api/v1/partners/"+s.resellID, map[string]any{"name": "Renamed"}, 403)
	// Nor assign a customer to itself.
	owner.mustJSON("PATCH", "/api/v1/customers/"+s.theirsID, map[string]any{"partner_id": s.resellID}, 404)

	// It DOES edit its own retail rule, read its statements, margin and
	// account, and manage its own users.
	owner.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "list", "markup_pct": "10"}, 200)
	owner.must("GET", "/api/v1/partners/"+s.resellID+"/retail-book", 200)
	owner.must("GET", "/api/v1/partners/"+s.resellID+"/margin?period=2026-08", 200)
	owner.must("GET", "/api/v1/partners/"+s.resellID+"/account", 200)
	// Its statement list carries its customers' statements AND its own
	// wholesale one, with the buy figures it is entitled to.
	all := owner.must("GET", "/api/v1/statements", 200)
	ids := map[string]bool{}
	for _, x := range all["statements"].([]any) {
		st := x.(map[string]any)
		ids[st["id"].(string)] = true
		if st["id"] == s.statementID && st["buy_total"] != 70.0 {
			t.Fatalf("a partner sees the buy price on its own customer's statement; got %v", st["buy_total"])
		}
	}
	if !ids[s.statementID] || !ids[s.partnerStmtID] {
		t.Fatalf("a partner's statement list = %v", all["statements"])
	}

	// A partner-viewer reads and changes nothing.
	owner.mustJSON("POST", "/api/v1/partners/"+s.resellID+"/users", map[string]any{"email": "v@resell.example", "role": store.RolePartnerViewer}, 201)
	viewer := &client{t: t, h: h}
	vme := viewer.signIn("v@resell.example", mail)
	if vme["role"] != store.RolePartnerViewer {
		t.Fatalf("viewer role = %v", vme["role"])
	}
	viewer.must("GET", "/api/v1/partners/"+s.resellID, 200)
	viewer.must("GET", "/api/v1/partners/"+s.resellID+"/margin?period=2026-08", 200)
	rec, out = viewer.json("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "list", "markup_pct": "50"})
	if rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "partner.self.manage") {
		t.Fatalf("a partner viewer edited the retail rule: %d %v", rec.Code, out)
	}
	viewer.mustJSON("POST", "/api/v1/partners/"+s.resellID+"/users", map[string]any{"email": "x@resell.example"}, 403)

	// Revoking a partner user ends its access at the next request.
	owner.must("DELETE", "/api/v1/partners/"+s.resellID+"/users/v@resell.example", 200)
	if rec, _ := viewer.do("GET", "/api/v1/partners/"+s.resellID, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked partner user is still in: %d", rec.Code)
	}
}

// A customer principal never sees partner data — not the directory, not
// another party's rows, and not the buy price or margin on its own bill.
func TestIntegrationCustomerNeverSeesPartnerFigures(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, _ := seedPartners(t, h, st, mail)

	cust := &client{t: t, h: h}
	cust.signIn(alphaAdmin, mail)

	rec, out := cust.do("GET", "/api/v1/partners", "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "metering.read") {
		t.Fatalf("a customer read the partner directory: %d %v", rec.Code, out)
	}
	cust.must("GET", "/api/v1/partners/"+s.resellID, 404)
	cust.must("GET", "/api/v1/partners/"+s.resellID+"/margin", 404)
	cust.must("GET", "/api/v1/partners/"+s.resellID+"/account", 404)

	// Its own statement: the bill it pays, with no buy price and no margin.
	stmt := cust.must("GET", "/api/v1/statements/"+s.statementID, 200)
	if stmt["subtotal"] != 90.0 {
		t.Fatalf("the customer's net = %v, want 90", stmt["subtotal"])
	}
	if _, ok := stmt["buy_total"]; ok {
		t.Fatalf("a customer was shown the partner buy price: %v", stmt["buy_total"])
	}
	if _, ok := stmt["margin_total"]; ok {
		t.Fatalf("a customer was shown the partner margin: %v", stmt["margin_total"])
	}
	line := stmt["lines"].([]any)[0].(map[string]any)
	for _, k := range []string{"buy_amount", "list_amount", "list_unit_price"} {
		if _, ok := line[k]; ok {
			t.Fatalf("a customer's line carries %q: %v", k, line)
		}
	}
	// The partner it buys through is still named on its own customer record —
	// that is its own commercial relationship, not a secret.
	if c := cust.must("GET", "/api/v1/customers/"+s.alphaID, 200); c["partner_id"] != s.resellID {
		t.Fatalf("the customer cannot see who it buys through: %v", c["partner_id"])
	}
	// And it cannot re-assign itself.
	cust.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": s.otherID}, 403)
}

// Assigning a customer to a partner is a partners.manage decision, and the
// ids it names must exist.
func TestIntegrationPartnerAssignmentValidation(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)

	rec, out := op.json("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": "00000000-0000-0000-0000-000000000000"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "partner_id") {
		t.Fatalf("an unknown partner_id = %d %v", rec.Code, out)
	}
	// Clearing it makes the customer direct again.
	got := op.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": ""}, 200)
	if got["partner_id"] != nil {
		t.Fatalf("partner_id was not cleared: %v", got["partner_id"])
	}
	// A slug and a name are required, and bill_to is one of the two models.
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "", "name": "x"}, 400)
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "ok-slug", "name": ""}, 400)
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "ok-slug", "name": "x", "bill_to": "somebody"}, 400)
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "ok-slug", "name": "x", "tier_id": "00000000-0000-0000-0000-000000000000"}, 400)
	// A slug is unique.
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "resell-co", "name": "Twin"}, 409)
	// An agent partner's commission is a percentage.
	op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "agent-co", "name": "Agent", "bill_to": "customer", "commission_pct": "150"}, 400)
	agent := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "agent-co", "name": "Agent", "bill_to": "customer", "commission_pct": "20"}, 201)
	if agent["bill_to"] != "customer" || agent["commission_pct"] != 20.0 {
		t.Fatalf("agent partner = %v", agent)
	}
	// An agent has no retail book, and the document says why.
	doc := op.must("GET", "/api/v1/partners/"+agent["id"].(string)+"/retail-book", 200)
	if len(doc["books"].([]any)) != 0 || !strings.Contains(doc["note"].(string), "agent") {
		t.Fatalf("an agent's retail book = %v", doc)
	}
	_ = st
}
