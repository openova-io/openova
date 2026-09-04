package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func getJSON(t *testing.T, h http.Handler, path string, jar string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if jar != "" {
		r.Header.Set("X-Forwarded-Email", jar)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// THE property that makes the split trustworthy: every row's share must add
// up to the whole, or cost silently disappears between the cloud total and
// the rows a customer is shown (#6850, ADR-0014 D3).
func TestAllocationSharesSumToOne(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	code, body := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if code != 200 {
		t.Fatalf("allocation: got %d, want 200", code)
	}
	rows, _ := body["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("no allocation rows — the split would show an empty screen on a busy Sovereign")
	}
	total, _ := body["share_total"].(float64)
	if total < 0.999999 || total > 1.000001 {
		t.Fatalf("shares sum to %v, want 1 — cost is being lost or double-counted between the cloud total and the rows", total)
	}
}

// The overhead line must be present and distinguishable, or the Sovereign's
// own footprint gets billed to a tenant.
func TestAllocationSeparatesOverheadFromTenants(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	_, body := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if n, _ := body["platform_overhead"].(float64); n < 1 {
		t.Fatal("no platform-overhead row — the Sovereign's own footprint would be billed as tenant consumption")
	}
	if n, _ := body["organization_rows"].(float64); n < 1 {
		t.Fatal("no organization row — tenant consumption is not being separated")
	}
}

// Cross-customer data must never reach a customer-scoped caller.
func TestAllocationIsOperatorOnly(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Forwarded-Email")

	code, _ := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", "")
	if code != 401 {
		t.Fatalf("unauthenticated allocation: got %d, want 401", code)
	}
	code, _ = getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", "stranger@example.com")
	if code == 200 {
		t.Fatal("a non-operator read the cross-customer allocation view")
	}
}

// An empty window must report zero shares, not an invented even split.
func TestAllocationEmptyWindowInventsNothing(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Forwarded-Email")

	code, body := getJSON(t, h, "/api/v1/allocation?from=2019-01-01&to=2019-01-02", opEmail)
	if code != 200 {
		t.Fatalf("empty window: got %d, want 200", code)
	}
	if total, _ := body["share_total"].(float64); total != 0 {
		t.Fatalf("empty window reported share_total=%v — numbers with no measurement behind them", total)
	}
}

// seedAllocationUsage writes platform usage for one tenant Organization and
// one platform-overhead row, so the split has both tiers to separate.
func seedAllocationUsage(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant, err := st.CreateCustomer(ctx, store.CustomerInput{
		Slug: "acme", Name: "Acme", AdminEmail: "a@acme.test", Kind: "organization", BillingMode: "showback",
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	sov, err := st.CreateCustomer(ctx, store.CustomerInput{
		Slug: "sov", Name: "Sovereign", AdminEmail: "o@sov.test", Kind: "organization", BillingMode: "showback",
	})
	if err != nil {
		t.Fatalf("create sovereign org: %v", err)
	}
	mk := func(cust string, overhead bool, qty string) store.UsageRecord {
		src, _, err := st.UpsertSource(ctx, cust, "openova-org", "", cust)
		if err != nil {
			t.Fatalf("source: %v", err)
		}
		lb := `{"namespace":"n"}`
		if overhead {
			lb = `{"namespace":"gitea","tier":"platform-overhead"}`
		}
		return store.UsageRecord{
			CustomerID: cust, SourceID: src.ID,
			ResourceID: "r-" + cust + qty, ResourceKind: "k8s-pod",
			SKU: "k8s.vcpu", Quantity: store.Decimal(qty), Unit: "vcpu-hour",
			WindowStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			WindowEnd:   time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
			Labels:      json.RawMessage(lb),
		}
	}
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{
		mk(tenant.ID, false, "3.000000"),
		mk(sov.ID, true, "1.000000"),
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}
