package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleGetOrgBssOverview_Returns200WithFullShape pins the wire
// shape — the FE getBssOverview() in ui/src/lib/bss.api.ts expects a
// fully-shaped BssOverview object. A missing key would parse as 0 /
// null on the FE side (the JS Number()/?? guards tolerate it), but the
// contract test asserts the BE emits the full shape so the operator
// sees real zeros rather than the "API pending" fallback (Refs #1949).
func TestHandleGetOrgBssOverview_Returns200WithFullShape(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/org/bss/overview", nil)
	w := httptest.NewRecorder()
	h.HandleGetOrgBssOverview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var resp orgBssOverviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\nbody=%s", err, w.Body.String())
	}

	// Decode into a map too so the test pins the JSON key set the FE
	// reads — any rename here breaks the FE bss.api.ts parse path.
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response is not a JSON object: %v", err)
	}
	for _, k := range []string{"billing", "orders", "vouchers", "tenants", "revenue"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q in response: %s", k, w.Body.String())
		}
	}

	// Zero-payload semantics — every numeric is the explicit zero and
	// every nullable is JSON null (Go *float64/*int nil -> null). The
	// FE Number()/?? guards collapse null to null and 0 to 0; the
	// `pendingApi=false` flag flips because the request itself was 2xx.
	if resp.Billing.MrrCents != 0 {
		t.Errorf("billing.mrrCents = %d, want 0 (zero-payload baseline)", resp.Billing.MrrCents)
	}
	if resp.Billing.DeltaPct != nil {
		t.Errorf("billing.deltaPct = %v, want nil (no prior period yet)", *resp.Billing.DeltaPct)
	}
	if resp.Orders.Pending != 0 {
		t.Errorf("orders.pending = %d, want 0", resp.Orders.Pending)
	}
	if resp.Orders.OldestDays != nil {
		t.Errorf("orders.oldestDays = %v, want nil (empty queue)", *resp.Orders.OldestDays)
	}
	if resp.Vouchers.Active != 0 {
		t.Errorf("vouchers.active = %d, want 0", resp.Vouchers.Active)
	}
	if resp.Vouchers.RedeemRate != nil {
		t.Errorf("vouchers.redeemRate = %v, want nil (no issuance yet)", *resp.Vouchers.RedeemRate)
	}
	if resp.Tenants.Active != 0 {
		t.Errorf("tenants.active = %d, want 0", resp.Tenants.Active)
	}
	if resp.Tenants.NewThisWeek != 0 {
		t.Errorf("tenants.newThisWeek = %d, want 0", resp.Tenants.NewThisWeek)
	}
	if resp.Revenue.Last30dCents != 0 {
		t.Errorf("revenue.last30dCents = %d, want 0", resp.Revenue.Last30dCents)
	}
	if resp.Revenue.DeltaPct != nil {
		t.Errorf("revenue.deltaPct = %v, want nil (no prior 30d window)", *resp.Revenue.DeltaPct)
	}
	// Sparkline MUST be a non-nil slice — the FE Array.isArray() guard
	// flips to the empty fallback when it isn't an array. Empty is
	// fine, nil/null is not.
	if resp.Revenue.Sparkline == nil {
		t.Errorf("revenue.sparkline must be a non-nil slice (got nil); FE Array.isArray() guard relies on this")
	}
	if len(resp.Revenue.Sparkline) != 0 {
		t.Errorf("revenue.sparkline len = %d, want 0 (zero-payload baseline)", len(resp.Revenue.Sparkline))
	}

	// Raw JSON-level pin — sparkline MUST serialise as `[]` not `null`,
	// otherwise the FE's `Array.isArray(body.revenue?.sparkline)` guard
	// fails and the page collapses to the empty fallback rather than
	// rendering the (empty) sparkline.
	revRaw, ok := raw["revenue"].(map[string]any)
	if !ok {
		t.Fatalf("revenue is not a JSON object: %v", raw["revenue"])
	}
	if revRaw["sparkline"] == nil {
		t.Errorf("revenue.sparkline serialised as JSON null; expected `[]` so FE Array.isArray() passes")
	}
}
