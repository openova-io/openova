package api

import (
	"context"
	"testing"
)

// The operator's customer page must show THAT customer (#6867, hw307 walk).
//
// GET /customers/{id}/cost/summary as an operator used to take the operator
// branch of gatherSummary — every source's status, every customer's
// statements — so a customer with one source and no statements rendered
// "Statements 3 · 3 draft", "3 verified sources" and another customer's
// 2,701.606 OMR statement. The global counts belong to GET /cost/summary
// alone; a selected customer counts and lists its own.
//
// The same walk found GET /statements?customer_id=<id> silently ignored for
// operators: the list handler read only `period`.

func statementsBlock(t *testing.T, sum map[string]any) (draft, issued int, latest []map[string]any) {
	t.Helper()
	blk, ok := sum["statements"].(map[string]any)
	if !ok {
		t.Fatalf("no statements block in %v", sum)
	}
	for _, raw := range blk["latest"].([]any) {
		latest = append(latest, raw.(map[string]any))
	}
	return int(blk["draft"].(float64)), int(blk["issued"].(float64)), latest
}

func sourcesBlock(t *testing.T, sum map[string]any) map[string]int {
	t.Helper()
	blk, ok := sum["sources"].(map[string]any)
	if !ok {
		t.Fatalf("no sources block in %v", sum)
	}
	out := map[string]int{}
	for k, v := range blk {
		out[k] = int(v.(float64))
	}
	return out
}

func statementsOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, ok := doc["statements"].([]any)
	if !ok {
		t.Fatalf("no statements array in %v", doc)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

// mustRun rates 2026-08 for one customer and fails unless a statement came out.
func mustRun(t *testing.T, op *client, customerID string) {
	t.Helper()
	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08", "customer_id": customerID}, 200)
	res := run["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("run %s = %v", customerID, run)
	}
	if id, _ := res[0].(map[string]any)["statement_id"].(string); id == "" {
		t.Fatalf("run %s produced no statement: %v", customerID, res[0])
	}
}

func customerIDs(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["customer_id"].(string))
	}
	return out
}

func TestIntegrationOperatorCustomerSummaryCountsOnlyThatCustomer(t *testing.T) {
	h, st, mail := setupAPIAt(t, walkNow)
	s := seedWalk(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// Statements for A only. A has two verified sources, B one, C one.
	mustRun(t, op, s.a.ID)

	// The operator overview keeps the global counts.
	global := op.must("GET", "/api/v1/cost/summary", 200)
	if draft, issued, latest := statementsBlock(t, global); draft != 1 || issued != 0 || len(latest) != 1 || latest[0]["customer_id"] != s.a.ID {
		t.Fatalf("global statements = %d/%d %v", draft, issued, latest)
	}
	if src := sourcesBlock(t, global); src["verified"] != 4 {
		t.Fatalf("global sources = %v", src)
	}
	if cust := global["customers"].(map[string]any); cust["active"] != 2.0 || cust["pending"] != 1.0 {
		t.Fatalf("global customers = %v", cust)
	}

	// B's page as the operator: B's one source, no statements — not A's.
	bsum := op.must("GET", "/api/v1/customers/"+s.b.ID+"/cost/summary", 200)
	if draft, issued, latest := statementsBlock(t, bsum); draft != 0 || issued != 0 || len(latest) != 0 {
		t.Fatalf("B statements = %d/%d %v", draft, issued, latest)
	}
	if src := sourcesBlock(t, bsum); src["verified"] != 1 || src["pending"] != 0 || src["failed"] != 0 {
		t.Fatalf("B sources = %v", src)
	}
	// A's page: its own draft and its two sources.
	asum := op.must("GET", "/api/v1/customers/"+s.a.ID+"/cost/summary", 200)
	if draft, _, latest := statementsBlock(t, asum); draft != 1 || len(latest) != 1 || latest[0]["customer_id"] != s.a.ID {
		t.Fatalf("A statements = %d %v", draft, latest)
	}
	if src := sourcesBlock(t, asum); src["verified"] != 2 {
		t.Fatalf("A sources = %v", src)
	}
	// The customer principal's own page reads the same as the operator's view of it.
	cb := customerClient(t, h, st, "b@bravo.example", "customer-admin", s.b.ID)
	own := cb.must("GET", "/api/v1/customers/"+s.b.ID+"/cost/summary", 200)
	if draft, _, latest := statementsBlock(t, own); draft != 0 || len(latest) != 0 || sourcesBlock(t, own)["verified"] != 1 {
		t.Fatalf("B own summary = %d %v %v", draft, latest, own["sources"])
	}
}

func TestIntegrationOperatorStatementsListFiltersByCustomer(t *testing.T) {
	h, st, mail := setupAPIAt(t, walkNow)
	s := seedWalk(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// One August draft each for A and B (B's project is given A's price book
	// so its EIP meter rates).
	if err := st.SetSourcePriceBook(ctx, s.srcB.ID, s.bookID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{s.a.ID, s.b.ID} {
		mustRun(t, op, id)
	}
	if all := statementsOf(t, op.must("GET", "/api/v1/statements", 200)); len(all) != 2 {
		t.Fatalf("all statements = %v", all)
	}
	// customer_id selects one customer; `customer` is its alias.
	if rows := statementsOf(t, op.must("GET", "/api/v1/statements?customer_id="+s.b.ID, 200)); len(rows) != 1 || rows[0]["customer_id"] != s.b.ID {
		t.Fatalf("?customer_id=B = %v", customerIDs(rows))
	}
	if rows := statementsOf(t, op.must("GET", "/api/v1/statements?customer="+s.a.ID, 200)); len(rows) != 1 || rows[0]["customer_id"] != s.a.ID {
		t.Fatalf("?customer=A = %v", customerIDs(rows))
	}
	// Both filters narrow together.
	if rows := statementsOf(t, op.must("GET", "/api/v1/statements?customer_id="+s.a.ID+"&period=2026-08", 200)); len(rows) != 1 {
		t.Fatalf("A 2026-08 = %v", customerIDs(rows))
	}
	if rows := statementsOf(t, op.must("GET", "/api/v1/statements?customer_id="+s.a.ID+"&period=2026-07", 200)); len(rows) != 0 {
		t.Fatalf("A 2026-07 = %v", customerIDs(rows))
	}
	if rec, _ := op.do("GET", "/api/v1/statements?customer_id="+s.a.ID+"&period=August", "", nil); rec.Code != 400 {
		t.Fatalf("bad period = %d", rec.Code)
	}
	// An id no customer has — a fresh UUID, or not a UUID at all — selects
	// nothing: an empty list, not a missing document.
	for _, unknown := range []string{"7f1c1a2e-0000-4000-8000-000000000000", "no-such-customer"} {
		if rows := statementsOf(t, op.must("GET", "/api/v1/statements?customer_id="+unknown, 200)); len(rows) != 0 {
			t.Fatalf("?customer_id=%s = %v", unknown, customerIDs(rows))
		}
	}
	// A customer principal is forced to its own list whatever it asks for.
	cb := customerClient(t, h, st, "b@bravo.example", "customer-admin", s.b.ID)
	if rows := statementsOf(t, cb.must("GET", "/api/v1/statements?customer_id="+s.a.ID, 200)); len(rows) != 1 || rows[0]["customer_id"] != s.b.ID {
		t.Fatalf("B asking for A = %v", customerIDs(rows))
	}
}
