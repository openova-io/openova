package api

import (
	"context"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The per-source price book over HTTP (DESIGN.md §2 / §3.8, founder
// direction 2026-09-08): GET/PATCH /customers/{id}/sources/{sid} and
// /sources/{id} carry layer, price_book_id and price_book_name; a book whose
// scope is not the source's layer is 400 with the exact message; the
// customer create/patch API no longer takes a price book; and the operator
// source directory lists every source with its book.
func TestIntegrationSourcePriceBookAssignmentAndScopeMismatch(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)

	// A platform book and the Organization's platform source.
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	platSrc, _, err := st.UpsertSource(ctx, seed.acme.ID, store.SourceKindOrg, "", "acme")
	if err != nil {
		t.Fatal(err)
	}

	// The source document carries the layer and the assigned book.
	got := op.must("GET", "/api/v1/customers/"+seed.acme.ID+"/sources/"+seed.srcA.ID, 200)
	if got["layer"] != "cloud" || got["price_book_id"] != seed.bookID || got["price_book_name"] != "List 2026" || got["internal"] != false {
		t.Fatalf("cloud source document = %+v", got)
	}
	if got := op.must("GET", "/api/v1/sources/"+platSrc.ID, 200); got["layer"] != "platform" || got["price_book_id"] != nil {
		t.Fatalf("platform source document = %+v", got)
	}

	// Scope mismatch, both directions: 400 naming scope and layer, nothing
	// changed.
	_, out := op.json("PATCH", "/api/v1/sources/"+platSrc.ID, map[string]any{"price_book_id": seed.bookID})
	if msg, _ := out["error"].(string); !strings.Contains(msg, "price book scope cloud does not match source layer platform") {
		t.Fatalf("cloud book on a platform source = %+v", out)
	}
	op.mustJSON("PATCH", "/api/v1/sources/"+platSrc.ID, map[string]any{"price_book_id": seed.bookID}, 400)
	op.mustJSON("PATCH", "/api/v1/sources/"+seed.srcA.ID, map[string]any{"price_book_id": plans.ID}, 400)
	if src, _ := st.GetSource(ctx, store.OperatorScope, seed.srcA.ID); src.PriceBookID == nil || *src.PriceBookID != seed.bookID {
		t.Fatalf("a refused assignment changed the source: %+v", src)
	}
	// A book that does not exist is a 400, not a 500.
	op.mustJSON("PATCH", "/api/v1/sources/"+platSrc.ID, map[string]any{"price_book_id": "00000000-0000-0000-0000-000000000000"}, 400)

	// The matching assignment succeeds and is audited.
	assigned := op.mustJSON("PATCH", "/api/v1/customers/"+seed.acme.ID+"/sources/"+platSrc.ID, map[string]any{"price_book_id": plans.ID}, 200)
	if assigned["price_book_id"] != plans.ID || assigned["price_book_name"] != store.PlanBookName || assigned["layer"] != "platform" {
		t.Fatalf("platform source after assignment = %+v", assigned)
	}
	// Clearing it is legitimate.
	if cleared := op.mustJSON("PATCH", "/api/v1/sources/"+platSrc.ID, map[string]any{"price_book_id": ""}, 200); cleared["price_book_id"] != nil {
		t.Fatalf("book not cleared: %+v", cleared)
	}
	op.mustJSON("PATCH", "/api/v1/sources/"+platSrc.ID, map[string]any{"price_book_id": plans.ID}, 200)

	// A customer admin may not set the book — it decides the rates.
	acme.mustJSON("PATCH", "/api/v1/sources/"+seed.srcA.ID, map[string]any{"price_book_id": seed.bookID}, 403)
	// Another customer's source is not even confirmed to exist.
	bravo := &client{t: t, h: h}
	bravo.signIn(bravoAdmin, mail)
	bravo.must("GET", "/api/v1/customers/"+seed.acme.ID+"/sources/"+seed.srcA.ID, 404)
	bravo.must("GET", "/api/v1/sources/"+seed.srcA.ID, 404)
	// A source id that belongs to another customer under this customer's
	// path is a 404, never someone else's document.
	op.must("GET", "/api/v1/customers/"+seed.bravo.ID+"/sources/"+seed.srcA.ID, 404)

	// The operator-wide directory: every source with its layer and book;
	// the internal source only when asked for.
	internal, _, err := st.EnsureInternalSource(ctx, "hw307-omani-works")
	if err != nil {
		t.Fatal(err)
	}
	list := op.must("GET", "/api/v1/sources", 200)["sources"].([]any)
	for _, raw := range list {
		if raw.(map[string]any)["id"] == internal.ID {
			t.Fatalf("the internal source is in the default directory: %+v", raw)
		}
	}
	if len(list) != 3 {
		t.Fatalf("source directory = %d rows, want acme's two and bravo's one", len(list))
	}
	withInternal := op.must("GET", "/api/v1/sources?internal=true", 200)["sources"].([]any)
	found := false
	for _, raw := range withInternal {
		m := raw.(map[string]any)
		if m["id"] == internal.ID {
			found = true
			if m["internal"] != true || m["layer"] != "platform" || m["customer_id"] != "" {
				t.Fatalf("internal source row = %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("?internal=true did not list the internal source: %+v", withInternal)
	}
	acme.must("GET", "/api/v1/sources", 403)
	// The internal source is never edited through the API.
	op.mustJSON("PATCH", "/api/v1/sources/"+internal.ID, map[string]any{"price_book_id": plans.ID}, 400)

	// The customer API no longer accepts a price book: the key is ignored,
	// never an error, and nothing moves.
	patched := op.mustJSON("PATCH", "/api/v1/customers/"+seed.acme.ID, map[string]any{"price_book_id": plans.ID}, 200)
	if patched["price_book_id"] != nil {
		t.Fatalf("customer patch wrote a price book: %+v", patched["price_book_id"])
	}
	created := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "delta", "name": "Delta", "admin_email": "d@delta.example", "price_book_id": seed.bookID}, 201)
	if created["price_book_id"] != nil {
		t.Fatalf("customer create wrote a price book: %+v", created["price_book_id"])
	}
	// A cloud source may be created with its book in one call.
	src := op.mustJSON("POST", "/api/v1/customers/"+created["id"].(string)+"/sources",
		map[string]any{"kind": "huawei-project", "region": "me-east-215", "project_id": "proj-delta", "price_book_id": seed.bookID}, 201)
	if src["price_book_id"] != seed.bookID || src["layer"] != "cloud" {
		t.Fatalf("created source = %+v", src)
	}
	// Platform sources are automatic: the API creates cloud kinds only.
	op.mustJSON("POST", "/api/v1/customers/"+created["id"].(string)+"/sources", map[string]any{"kind": "openova-org", "project_id": "delta"}, 400)
	op.mustJSON("POST", "/api/v1/customers/"+created["id"].(string)+"/sources", map[string]any{"kind": "openova-platform", "project_id": "delta"}, 400)

	// The customer list counts sources per layer.
	for _, raw := range op.must("GET", "/api/v1/customers", 200)["customers"].([]any) {
		m := raw.(map[string]any)
		if m["id"] != seed.acme.ID {
			continue
		}
		if m["cloud_source_count"].(float64) != 1 || m["platform_source_count"].(float64) != 1 {
			t.Fatalf("acme source counts = %+v", m)
		}
	}
}

// TestIntegrationMixedCurrencyStatementRunIs400: the HTTP surface of the
// refusal — a single-customer run whose sources carry books of different
// currencies answers 400 with the message, not a 500.
func TestIntegrationMixedCurrencyStatementRunIs400(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	usd, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "USD list", Scope: store.LayerCloud, Currency: "USD", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := st.UpsertSource(ctx, seed.acme.ID, store.SourceKindHuaweiProject, "me-east-216", "ok-a-usd")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, second.ID, usd.ID)

	_, out := op.json("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08", "customer_id": seed.acme.ID})
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "one currency") || !strings.Contains(msg, "USD") {
		t.Fatalf("mixed-currency run = %+v", out)
	}
	op.mustJSON("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08", "customer_id": seed.acme.ID}, 400)
}
