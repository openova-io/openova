package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// A month paid in advance is not billed again when the package's price is
// raised after it was paid (DESIGN.md §22.11), and the statement that paid
// it does not move. End to end through the statement run:
//
//	October (issued). Ordered on 10 October at M 4.490 a month.
//	  plan.m           10 to 31 October, 528 h                    =  3.186454
//	  plan.m           November, in advance                       =  4.490000
//	  checkout.credit  paid at checkout                           = −4.490000
//
//	The sovereign-admin raises M to 4.990 a month (annual 59.88).
//
//	November. The Source stayed on M all month; November was paid.
//	  plan.m           December, in advance, at the new price     =  4.990000
//
// On main November also carried "Upgrade M package → M package — 1 to 30
// November 2026, 720 of 720 hours at the 0.500 monthly difference": the
// frozen 4.490 compared with today's 4.990.
func TestIntegrationRaisedPriceDoesNotBillAPrepaidMonthAgain(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := "/api/v1/pricebooks/" + plans.ID
	op.mustJSON("PATCH", bookPath+"/items/plan.m", map[string]any{"annual_price": "53.88"}, 200)

	cust := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "sohar", "name": "Sohar Steel", "admin_email": "ops@sohar.example", "kind": "organization", "org_slug": "sohar", "plan_slug": "m", "billing_mode": "chargeback"}, 201)
	cid := cust["id"].(string)
	op.mustJSON("PATCH", "/api/v1/customers/"+cid, map[string]any{"status": "active"}, 200)
	src, _, err := st.UpsertSource(ctx, cid, store.SourceKindOrg, "", "sohar")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceVerified(ctx, src.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, plans.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePrepaid(ctx, src.ID, "4.490"); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	for at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC); at.Before(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); at = at.Add(time.Hour) {
		recs = append(recs, store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour)})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// lines renders a statement's non-zero lines and subtotal as one string,
	// the figure that must not move once the statement is issued.
	lines := func(id string) string {
		t.Helper()
		_, stmt := getExact(t, op, "/api/v1/statements/"+id, 200)
		var b strings.Builder
		for _, l := range stmt["lines"].([]any) {
			lm := l.(map[string]any)
			if dec(t, lm["amount"]) == "0.000000" {
				continue
			}
			desc, _ := lm["description"].(string)
			b.WriteString(lm["sku"].(string) + " " + dec(t, lm["quantity"]) + " " + dec(t, lm["amount"]) + " " + desc + "\n")
		}
		b.WriteString("subtotal " + dec(t, stmt["subtotal"]) + " total " + dec(t, stmt["total"]) + " status " + stmt["status"].(string))
		return b.String()
	}
	run := func(period string) string {
		t.Helper()
		out := postExact(t, op, "/api/v1/statements/run", map[string]any{"period": period, "customer_id": cid}, 200)
		res := out["results"].([]any)[0].(map[string]any)
		if res["error"] != nil {
			t.Fatalf("%s run = %v", period, out)
		}
		return res["statement_id"].(string)
	}

	oct := run("2026-10")
	op.mustJSON("POST", "/api/v1/statements/"+oct+"/issue", map[string]any{"notify": false}, 200)
	octIssued := lines(oct)
	if !strings.Contains(octIssued, "plan.m 1.000000 4.490000 M package — November 2026, billed in advance") {
		t.Fatalf("October did not bill November in advance at 4.490:\n%s", octIssued)
	}

	op.mustJSON("PATCH", bookPath+"/items/plan.m", map[string]any{"annual_price": "59.88"}, 200)

	nov := lines(run("2026-11"))
	if strings.Contains(nov, "Upgrade") {
		t.Fatalf("November billed the prepaid month again after the price change:\n%s", nov)
	}
	want := "plan.m 1.000000 4.990000 M package — December 2026, billed in advance\nsubtotal 4.990000"
	if !strings.HasPrefix(nov, want) {
		t.Fatalf("November =\n%s\nwant\n%s", nov, want)
	}
	if again := lines(oct); again != octIssued {
		t.Fatalf("the issued October statement moved after the price change:\nbefore\n%s\nafter\n%s", octIssued, again)
	}
}
