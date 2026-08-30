package huawei

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// TestIntegrationCollectorIsolatesUndecryptableCredential is the database
// version of the isolation unit test: a leftover source whose secret was
// sealed under a different APP_ENCRYPTION_KEY must not stop the healthy
// source from producing inventory and usage, and must not be retried.
func TestIntegrationCollectorIsolatesUndecryptableCredential(t *testing.T) {
	col, _, good, st := setupCollector(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	col.Now = func() time.Time { return now }

	// A second source on the same customer with a blob no current key opens.
	badCred, err := st.CreateCredential(ctx, good.CustomerID, "AKSTALE", []byte("sealed-under-a-previous-key"))
	if err != nil {
		t.Fatal(err)
	}
	bad, _, err := st.UpsertSource(ctx, good.CustomerID, "huawei-project", "me-east-215", "ffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSourceCredential(ctx, bad.ID, badCred.ID)
	_ = st.SetSourceVerified(ctx, bad.ID, "")
	if live, _ := st.ListVerifiedSources(ctx); len(live) != 2 {
		t.Fatalf("collectable sources before the tick = %d, want 2", len(live))
	}

	res := col.CollectAll(ctx)
	if res.Result != "partial" || res.Sources != 2 || res.Failed != 1 || res.Errors[bad.ID] == nil || res.Errors[good.ID] != nil {
		t.Fatalf("tick = %+v", res)
	}
	badNow, _ := st.GetSource(ctx, store.OperatorScope, bad.ID)
	if badNow.Status != "failed" || badNow.LastError == nil || !strings.Contains(*badNow.LastError, "APP_ENCRYPTION_KEY") {
		t.Fatalf("stale source = %+v", badNow)
	}
	goodNow, _ := st.GetSource(ctx, store.OperatorScope, good.ID)
	if goodNow.Status != "verified" || goodNow.LastCollectedAt == nil || goodNow.LastError != nil {
		t.Fatalf("healthy source = %+v", goodNow)
	}
	inv, _ := st.ListInventory(ctx, good.ID)
	if len(inv) != 18 {
		t.Fatalf("healthy source inventory = %d, want 18", len(inv))
	}
	if n, _ := st.UsageCount(ctx, good.ID); n == 0 {
		t.Fatal("healthy source produced no usage")
	}
	if n, _ := st.UsageCount(ctx, bad.ID); n != 0 {
		t.Fatalf("stale source produced %d usage records", n)
	}

	// Not retried: the failed source has left the collectable set, and the
	// next tick is clean. A re-entered credential brings it back.
	res = col.CollectAll(ctx)
	if res.Result != "ok" || res.Sources != 1 || res.Failed != 0 {
		t.Fatalf("second tick = %+v", res)
	}
	freshEnc, _ := col.Keys.Seal([]byte(testSecret))
	fresh, _ := st.CreateCredential(ctx, good.CustomerID, "AKTEST", freshEnc)
	_ = st.SetSourceCredential(ctx, bad.ID, fresh.ID)
	_ = st.SetSourceVerified(ctx, bad.ID, "")
	res = col.CollectAll(ctx)
	if res.Result != "ok" || res.Sources != 2 || res.Failed != 0 {
		t.Fatalf("tick after re-entering the credential = %+v", res)
	}
	if inv, _ := st.ListInventory(ctx, bad.ID); len(inv) != 18 {
		t.Fatalf("re-enabled source inventory = %d, want 18", len(inv))
	}
}
