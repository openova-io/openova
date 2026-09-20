package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Deleting a tier and deleting a partner (DESIGN.md §13.9, #6936). Every
// assertion is on the HTTP answer — the status and the sentence the console
// shows verbatim — and then on what is left in the database.

// refused asserts a 409 whose sentence carries every `want` and none of the
// `not`: the second list is what isolates ONE blocking condition from the
// others, so a test cannot pass on a neighbour's clause.
func refused(t *testing.T, c *client, path string, want, not []string) string {
	t.Helper()
	rec, out := c.do("DELETE", path, "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE %s = %d %s, want 409", path, rec.Code, rec.Body.String())
	}
	msg, _ := out["error"].(string)
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Fatalf("DELETE %s: the refusal does not say %q: %s", path, w, msg)
		}
	}
	for _, n := range not {
		if strings.Contains(msg, n) {
			t.Fatalf("DELETE %s: the refusal names %q, which is not what blocks it: %s", path, n, msg)
		}
	}
	// A refusal is read by a person: no ids, no sentinel prefix.
	if strings.HasPrefix(msg, "conflict") || strings.Contains(msg, "-0000-") || strings.Contains(msg, "_id") {
		t.Fatalf("DELETE %s: the refusal reads like a machine: %s", path, msg)
	}
	return msg
}

func countRows(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegrationPartnerTierDelete(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	tier := "/api/v1/partners/tiers/" + s.tierID

	// One partner on the tier: refused, and the partner is NAMED.
	refused(t, op, tier, []string{"Gold", "1 partner", "Resell Co", "another tier"}, []string{"Other Co"})
	// Two: both named.
	op.mustJSON("PATCH", "/api/v1/partners/"+s.otherID, map[string]any{"tier_id": s.tierID}, 200)
	refused(t, op, tier, []string{"2 partners", "Other Co, Resell Co"}, nil)
	// A refusal changed nothing: the tier, its discount and both assignments stand.
	tiers := op.must("GET", "/api/v1/partners/tiers", 200)["tiers"].([]any)
	if len(tiers) != 1 || tiers[0].(map[string]any)["partners"] != 2.0 || len(tiers[0].(map[string]any)["discounts"].([]any)) != 1 {
		t.Fatalf("after a refused delete the tier reads %v", tiers)
	}
	if n := auditActions(t, st, "partner.tier"); n != 2 { // create + discounts, from the seed
		t.Fatalf("a refused delete was audited: partner.tier entries = %d", n)
	}

	// An id nothing has, and one that is not an id: 404, never 409 or 500.
	op.must("DELETE", "/api/v1/partners/tiers/00000000-0000-0000-0000-000000000000", 404)
	op.must("DELETE", "/api/v1/partners/tiers/gold", 404)

	// Unused: it goes, and the discounts that made it go with it.
	for _, pid := range []string{s.resellID, s.otherID} {
		op.mustJSON("PATCH", "/api/v1/partners/"+pid, map[string]any{"tier_id": ""}, 200)
	}
	if n := countRows(t, st, `SELECT count(*) FROM discounts WHERE tier_id = $1`, s.tierID); n != 1 {
		t.Fatalf("the tier holds %d discounts before the delete, want 1 — otherwise 'gone after' proves nothing", n)
	}
	if out := op.must("DELETE", tier, 200); out["deleted"] != true || out["name"] != "Gold" {
		t.Fatalf("delete = %v", out)
	}
	if tiers := op.must("GET", "/api/v1/partners/tiers", 200)["tiers"].([]any); len(tiers) != 0 {
		t.Fatalf("the tier is still listed: %v", tiers)
	}
	if n := countRows(t, st, `SELECT count(*) FROM discounts WHERE tier_id = $1`, s.tierID); n != 0 {
		t.Fatalf("%d tier discounts outlived their tier", n)
	}
	// A customer's own discounts are not a tier's and are untouched.
	if n := countRows(t, st, `SELECT count(*) FROM discounts WHERE customer_id IS NOT NULL`); n != 2 {
		t.Fatalf("customer discounts after a tier delete = %d, want the seed's 2", n)
	}
	op.must("DELETE", tier, 404)

	// Audited, once, with what went.
	var actor, name, discounts string
	if err := st.DB().QueryRowContext(context.Background(), `SELECT actor, details->>'name', details->>'discounts' FROM audit_log
		WHERE action = 'partner.tier' AND details->>'op' = 'delete'`).Scan(&actor, &name, &discounts); err != nil {
		t.Fatalf("no partner.tier delete audit row: %v", err)
	}
	if actor != opEmail || name != "Gold" || discounts != "1" {
		t.Fatalf("audit = actor %q name %q discounts %q", actor, name, discounts)
	}
}

// Each blocking condition on its own, named in the answer — and the delete
// going through once it is cleared.
func TestIntegrationPartnerDeleteRefusedWhileCustomersBuyThroughIt(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	p := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "resell-co", "name": "Resell Co", "contact_email": resellContact}, 201)
	id, party := p["id"].(string), p["party_customer_id"].(string)
	for _, c := range [][2]string{{"alpha", "Alpha"}, {"beta", "Beta"}} {
		got := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": c[0], "name": c[1], "admin_email": "a@" + c[0] + ".example"}, 201)
		op.mustJSON("PATCH", "/api/v1/customers/"+got["id"].(string), map[string]any{"partner_id": id}, 200)
	}

	refused(t, op, "/api/v1/partners/"+id,
		[]string{"Resell Co", "2 customers still buy through it (Alpha, Beta)", "make them direct"},
		[]string{"issued statement", "ledger", "retail book", "suspend"})
	op.must("GET", "/api/v1/partners/"+id, 200)
	if n := auditActions(t, st, "partner.delete"); n != 0 {
		t.Fatalf("a refused delete was audited %d time(s)", n)
	}

	// Its account is the partner's, not a customer to delete from under it.
	rec, out := op.do("DELETE", "/api/v1/customers/"+party, "", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(out["error"].(string), "belongs to a partner") {
		t.Fatalf("deleting a partner's party = %d %s, want 409 saying whose account it is", rec.Code, rec.Body.String())
	}

	// Made direct, one by one: refused until the last one has gone.
	list := op.must("GET", "/api/v1/partners/"+id+"/customers", 200)["customers"].([]any)
	op.mustJSON("PATCH", "/api/v1/customers/"+list[0].(map[string]any)["id"].(string), map[string]any{"partner_id": ""}, 200)
	refused(t, op, "/api/v1/partners/"+id, []string{"1 customer still buys through it (Beta)"}, []string{"Alpha"})
	op.mustJSON("PATCH", "/api/v1/customers/"+list[1].(map[string]any)["id"].(string), map[string]any{"partner_id": ""}, 200)

	if out := op.must("DELETE", "/api/v1/partners/"+id, 200); out["deleted"] != true || out["slug"] != "resell-co" {
		t.Fatalf("delete = %v", out)
	}
	op.must("GET", "/api/v1/partners/"+id, 404)
	op.must("DELETE", "/api/v1/partners/"+id, 404)
	if dir := op.must("GET", "/api/v1/partners", 200)["partners"].([]any); len(dir) != 0 {
		t.Fatalf("the directory still lists %v", dir)
	}
	// The party, and the contact's partner-owner binding, went with it; the
	// two customers did not.
	if n := countRows(t, st, `SELECT count(*) FROM customers WHERE id = $1`, party); n != 0 {
		t.Fatal("the partner's party outlived it")
	}
	if n := countRows(t, st, `SELECT count(*) FROM role_bindings WHERE subject_email = $1`, resellContact); n != 0 {
		t.Fatalf("the partner's contact still holds %d binding(s)", n)
	}
	if dir := op.must("GET", "/api/v1/customers", 200)["customers"].([]any); len(dir) != 2 {
		t.Fatalf("customers after the partner went = %v, want Alpha and Beta, direct", dir)
	}

	var actor, slug, customer string
	if err := st.DB().QueryRowContext(context.Background(), `SELECT actor, details->>'slug', customer_id::text FROM audit_log WHERE action = 'partner.delete'`).Scan(&actor, &slug, &customer); err != nil {
		t.Fatalf("no partner.delete audit row: %v", err)
	}
	if actor != opEmail || slug != "resell-co" || customer != party {
		t.Fatalf("audit = actor %q slug %q customer %q", actor, slug, customer)
	}
}

// The partner's OWN issued statement: a wholesale invoice it received.
func TestIntegrationPartnerDeleteRefusedWhileItsOwnStatementIsIssued(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	issued := op.mustJSON("POST", "/api/v1/statements/"+s.partnerStmtID+"/issue", map[string]any{"notify": false}, 200)
	number, _ := issued["invoice_number"].(string)
	if number == "" {
		t.Fatalf("the wholesale statement was issued without a number: %v", issued)
	}
	op.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": ""}, 200)

	refused(t, op, "/api/v1/partners/"+s.resellID,
		[]string{"1 issued statement of its own (" + number + ")", "permanent records", "suspend the partner"},
		[]string{"still buy", "of its customers", "retail book"})
	op.must("GET", "/api/v1/statements/"+s.partnerStmtID, 200)
}

// A CUSTOMER statement rated through the partner carries its buy price and
// margin; once issued it is the record of that, whoever the customer buys
// through today.
func TestIntegrationPartnerDeleteRefusedWhileACustomerStatementCarriesItsMargin(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	op.mustJSON("POST", "/api/v1/statements/"+s.statementID+"/issue", map[string]any{"notify": false}, 200)
	op.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": ""}, 200)

	refused(t, op, "/api/v1/partners/"+s.resellID,
		[]string{"1 issued statement of its customers carries its buy price and margin (Alpha)", "suspend the partner"},
		[]string{"still buy", "of its own", "ledger entr", "retail book"})
	// And the statement still says who it was rated through.
	if stmt := op.must("GET", "/api/v1/statements/"+s.statementID, 200); stmt["partner_id"] != s.resellID || stmt["margin_total"] != 20.0 {
		t.Fatalf("the customer statement after the refusal = partner %v margin %v", stmt["partner_id"], stmt["margin_total"])
	}
}

// Money on the partner's account with no statement at all: a transfer it
// made ahead. The party row IS that ledger.
func TestIntegrationPartnerDeleteRefusedWhileItsAccountHoldsEntries(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	p := op.mustJSON("POST", "/api/v1/partners", map[string]any{"slug": "agent-co", "name": "Agent Co", "bill_to": "customer", "commission_pct": "20"}, 201)
	id, party := p["id"].(string), p["party_customer_id"].(string)
	op.mustJSON("POST", "/api/v1/customers/"+party+"/payments", map[string]any{"amount": "25.000000", "reference": "TRF-25"}, 201)

	refused(t, op, "/api/v1/partners/"+id,
		[]string{"Agent Co", "its account holds 1 ledger entry", "permanent records", "suspend the partner"},
		[]string{"still buy", "issued statement of", "retail book"})
	if acct := op.must("GET", "/api/v1/partners/"+id+"/account", 200); len(acct["entries"].([]any)) != 1 {
		t.Fatalf("the ledger after the refusal = %v", acct["entries"])
	}
	_ = st
}

// A derived retail book a source is priced from. The source here belongs to
// a DIRECT customer, so detaching the partner's own customers cannot clear it.
func TestIntegrationPartnerDeleteRefusedWhileItsRetailBookIsAssigned(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	ctx := context.Background()
	doc := op.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "buy", "markup_pct": "5"}, 200)
	book := doc["books"].([]any)[0].(map[string]any)["book"].(map[string]any)
	derivedID, derivedName := book["id"].(string), book["name"].(string)
	direct := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "direct", "name": "Direct", "admin_email": "d@direct.example"}, 201)
	src, _, err := st.UpsertSource(ctx, direct["id"].(string), "huawei-project", "me-east-215", "ok-direct")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, derivedID)

	refused(t, op, "/api/v1/partners/"+s.resellID,
		[]string{"its retail book " + derivedName + " is assigned to 1 source (Direct)", "another price book"}, nil)
	if got := op.must("GET", "/api/v1/pricebooks/"+derivedID, 200); got["id"] != derivedID {
		t.Fatalf("the retail book after the refusal = %v", got)
	}

	// Priced from the list book again, and its customer made direct: nothing
	// depends on the partner, and the draft statements of the seed's run are
	// not records — the partner's own goes, the customer's stays, direct.
	assignBook(t, st, src.ID, s.bookID)
	op.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": ""}, 200)
	op.must("DELETE", "/api/v1/partners/"+s.resellID, 200)
	op.must("GET", "/api/v1/statements/"+s.partnerStmtID, 404)
	if stmt := op.must("GET", "/api/v1/statements/"+s.statementID, 200); stmt["partner_id"] != nil {
		t.Fatalf("the customer's draft still names the deleted partner: %v", stmt["partner_id"])
	}
	for table, q := range map[string]string{
		"partner_retail_rules": `SELECT count(*) FROM partner_retail_rules WHERE partner_id = $1`,
		"price_books":          `SELECT count(*) FROM price_books WHERE partner_id = $1`,
		"role_bindings":        `SELECT count(*) FROM role_bindings WHERE partner_id = $1`,
	} {
		if n := countRows(t, st, q, s.resellID); n != 0 {
			t.Fatalf("%d %s row(s) outlived the partner", n, table)
		}
	}
	// The other partner, its customer and the list book are untouched.
	op.must("GET", "/api/v1/partners/"+s.otherID, 200)
	op.must("GET", "/api/v1/pricebooks/"+s.bookID, 200)
}

// Who may: partners.manage at the Sovereign, and nobody else — not a role
// that only reads, not the partner's own owner, not a customer.
func TestIntegrationPartnerDeleteIsSovereignManageOnly(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "fin@nc.example", "role": store.RoleFinanceViewer}, 201)

	for _, who := range []struct{ name, email string }{
		{"finance-viewer", "fin@nc.example"},
		{"partner-owner", resellContact},
		{"customer-owner", alphaAdmin},
	} {
		c := &client{t: t, h: h}
		c.signIn(who.email, mail)
		for _, path := range []string{"/api/v1/partners/" + s.otherID, "/api/v1/partners/" + s.resellID, "/api/v1/partners/tiers/" + s.tierID} {
			rec, out := c.do("DELETE", path, "", nil)
			if rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "partners.manage") {
				t.Fatalf("%s DELETE %s = %d %s, want 403 naming partners.manage", who.name, path, rec.Code, rec.Body.String())
			}
		}
	}
	// Not signed in at all.
	anon := &client{t: t, h: h}
	if rec, _ := anon.do("DELETE", "/api/v1/partners/"+s.otherID, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous delete = %d, want 401", rec.Code)
	}
	// Nothing went.
	if dir := op.must("GET", "/api/v1/partners", 200)["partners"].([]any); len(dir) != 2 {
		t.Fatalf("partners after the refused deletes = %v", dir)
	}
	op.must("DELETE", "/api/v1/partners/00000000-0000-0000-0000-000000000000", 404)
	op.must("DELETE", "/api/v1/partners/not-an-id", 404)
}

// Renaming a tier (#6936, found while building its delete): the console's
// Rename POSTed the CREATE, so it made a second tier under the new name — or
// answered 409 when only the description had changed — and left the old one
// where it was. PATCH /partners/tiers/{id} is the route it had no way to call.
func TestIntegrationPartnerTierRenameChangesTheTierItWasAskedTo(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	path := "/api/v1/partners/tiers/" + s.tierID

	got := op.mustJSON("PATCH", path, map[string]any{"name": "Platinum", "description": "top of the range"}, 200)
	if got["id"] != s.tierID || got["name"] != "Platinum" || got["description"] != "top of the range" {
		t.Fatalf("renamed tier = %v", got)
	}
	// ONE tier, the same one, with its discount and its partner still on it.
	tiers := op.must("GET", "/api/v1/partners/tiers", 200)["tiers"].([]any)
	if len(tiers) != 1 {
		t.Fatalf("a rename must never make a second tier: %v", tiers)
	}
	if tier := tiers[0].(map[string]any); tier["id"] != s.tierID || tier["name"] != "Platinum" || tier["partners"] != 1.0 || len(tier["discounts"].([]any)) != 1 {
		t.Fatalf("after the rename the tier reads %v", tier)
	}
	// A blank field is left as it was.
	kept := op.mustJSON("PATCH", path, map[string]any{"description": "still the top"}, 200)
	if kept["name"] != "Platinum" || kept["description"] != "still the top" {
		t.Fatalf("a PATCH with no name = %v", kept)
	}
	// Renaming onto another tier's name is a conflict a person can read.
	op.mustJSON("POST", "/api/v1/partners/tiers", map[string]any{"name": "Silver"}, 201)
	rec, out := op.json("PATCH", path, map[string]any{"name": "Silver"})
	if rec.Code != http.StatusConflict || strings.Contains(out["error"].(string), "partner_tiers") {
		t.Fatalf("renaming onto a taken name = %d %s, want 409 without the schema in it", rec.Code, rec.Body.String())
	}
	op.mustJSON("PATCH", "/api/v1/partners/tiers/00000000-0000-0000-0000-000000000000", map[string]any{"name": "Nobody"}, 404)

	// partners.manage at the Sovereign, like every other partner write.
	op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "fin@nc.example", "role": store.RoleFinanceViewer}, 201)
	for _, email := range []string{"fin@nc.example", resellContact, alphaAdmin} {
		c := &client{t: t, h: h}
		c.signIn(email, mail)
		if rec, out := c.json("PATCH", path, map[string]any{"name": "Mine"}); rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "partners.manage") {
			t.Fatalf("%s PATCH tier = %d %s, want 403 naming partners.manage", email, rec.Code, rec.Body.String())
		}
	}
	var audited int
	if err := st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'partner.tier' AND details->>'op' = 'rename' AND details->>'from' = 'Gold'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("the rename from Gold is audited once: %d, %v", audited, err)
	}
}

// Re-deriving a partner's retail books must never fail AFTER the change that
// caused it has been saved. A derived book may be assigned to a source; once
// the partner's last customer on that list book is made direct, the book is
// no longer derivable and the re-derivation tried to delete it, hit the
// source's foreign key, and answered 404 "not found" for a PATCH that had
// already committed. A book a source is priced from is kept instead: the
// source stays priced, and the caller is told the truth.
func TestIntegrationDetachingTheLastCustomerKeepsARetailBookASourceIsPricedFrom(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	s, op := seedPartners(t, h, st, mail)
	ctx := context.Background()
	doc := op.mustJSON("PUT", "/api/v1/partners/"+s.resellID+"/retail-rule", map[string]any{"base": "buy", "markup_pct": "5"}, 200)
	derivedID := doc["books"].([]any)[0].(map[string]any)["book"].(map[string]any)["id"].(string)
	direct := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "direct", "name": "Direct", "admin_email": "d@direct.example"}, 201)
	src, _, err := st.UpsertSource(ctx, direct["id"].(string), "huawei-project", "me-east-215", "ok-direct")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, derivedID)

	// The partner's only customer goes direct: the PATCH is saved AND says so.
	got := op.mustJSON("PATCH", "/api/v1/customers/"+s.alphaID, map[string]any{"partner_id": ""}, 200)
	if got["partner_id"] != nil && got["partner_id"] != "" {
		t.Fatalf("the customer is still on the partner: %v", got["partner_id"])
	}
	// The book the Direct source is priced from is still there, items and all.
	if book := op.must("GET", "/api/v1/pricebooks/"+derivedID, 200); book["id"] != derivedID {
		t.Fatalf("the assigned retail book = %v", book)
	}
	// Once nothing is priced from it, the next re-derivation takes it away.
	assignBook(t, st, src.ID, s.bookID)
	if err := st.DeleteDerivedBooks(ctx, s.resellID, nil); err != nil {
		t.Fatal(err)
	}
	op.must("GET", "/api/v1/pricebooks/"+derivedID, 404)
}
