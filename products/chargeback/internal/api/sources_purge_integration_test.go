package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Setting a scope token must let the operator take the previously collected
// out-of-scope hours off the ledger — and only those (#6867).
func TestIntegrationPurgeExcludedUsage(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "sov", Name: "Sovereign", AdminEmail: "a@sov.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-1", "proj")
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{
		{ResourceID: "vm-in", Kind: "ecs", Name: "catalyst-hw307-9a1f230f-a-w1", SeenAt: seen},
		{ResourceID: "vm-out", Kind: "ecs", Name: "bastion-openova", SeenAt: seen},
		{ResourceID: "vol-in", Kind: "evs", Name: "pvc-1", Attrs: map[string]any{"attached_to": "vm-in"}, SeenAt: seen},
		{ResourceID: "eip-out", Kind: "eip", Name: "212.72.24.20", Attrs: map[string]any{"bandwidth_name": "bastion-openova-bw"}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	for _, id := range []string{"vm-in", "vm-out", "vol-in", "eip-out"} {
		for hr := 0; hr < 3; hr++ {
			at := time.Date(2026, 9, 1, hr, 0, 0, 0, time.UTC)
			recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: id, ResourceKind: "x", SKU: "sku." + id, Quantity: "1", Unit: "hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// No scope yet → 400, nothing touched.
	if rec, _ := op.do("POST", "/api/v1/sources/"+src.ID+"/purge-excluded", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped purge = %d %s", rec.Code, rec.Body.String())
	}
	if err := st.SetSourceScopeToken(ctx, src.ID, "9a1f230f"); err != nil {
		t.Fatal(err)
	}
	out := op.mustJSON("POST", "/api/v1/sources/"+src.ID+"/purge-excluded", nil, 200)
	if out["usage_records_deleted"].(float64) != 6 {
		t.Fatalf("deleted = %v", out["usage_records_deleted"])
	}
	ids, _ := json.Marshal(out["excluded_resources"])
	if string(ids) != `["vm-out","eip-out"]` {
		t.Fatalf("excluded = %s", ids)
	}
	if n, _ := st.UsageCount(ctx, src.ID); n != 6 {
		t.Fatalf("remaining records = %d, want 6 (vm-in + attached vol-in)", n)
	}
	inv, _ := st.ListInventory(ctx, src.ID)
	for _, it := range inv {
		gone := it.DeletedAt != nil
		if (it.ResourceID == "vm-out" || it.ResourceID == "eip-out") != gone {
			t.Fatalf("%s deleted_at=%v", it.ResourceID, it.DeletedAt)
		}
	}
	// Idempotent: a second purge finds nothing left to delete.
	again := op.mustJSON("POST", "/api/v1/sources/"+src.ID+"/purge-excluded", nil, 200)
	if again["usage_records_deleted"].(float64) != 0 {
		t.Fatalf("second purge deleted %v", again["usage_records_deleted"])
	}
	// A customer principal cannot rewrite the ledger.
	cust := &client{t: t, h: h}
	cust.signIn("a@sov.example", mail)
	cust.must("POST", "/api/v1/sources/"+src.ID+"/purge-excluded", 403)
}
