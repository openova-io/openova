package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeCatalogRows serves whatever plan rows the test hands it, so the three
// generations of the catalog wire shape can be exercised side by side.
func fakeCatalogRows(t *testing.T, rows []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/plans", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	})
	return httptest.NewServer(mux)
}

// TestPriceFromCatalog_DecimalPlanPriceIsReadFromBaisa (#6971): the National
// Cloud packages cost 2.490 / 4.490 / 7.990 / 13.990 OMR, so the catalog now
// serves price_omr as a decimal and price_baisa as the money. The billing
// reader must (1) not choke on the decimal — the old `int` field would have
// failed to decode 4.49 and 400'd every checkout — and (2) take the amount
// from price_baisa, never from float arithmetic on price_omr.
func TestPriceFromCatalog_DecimalPlanPriceIsReadFromBaisa(t *testing.T) {
	cases := []struct {
		name      string
		row       map[string]any
		wantBaisa int64
		wantWhole int
	}{
		{"current catalog: baisa + decimal mirror", map[string]any{"id": "m", "price_omr": 4.49, "price_baisa": 4490}, 4490, 5},
		{"baisa wins when the mirror disagrees", map[string]any{"id": "m", "price_omr": 9, "price_baisa": 4490}, 4490, 5},
		{"older catalog: decimal only, rounded once", map[string]any{"id": "m", "price_omr": 2.49}, 2490, 3},
		{"oldest catalog: integer only", map[string]any{"id": "m", "price_omr": 9}, 9000, 9},
		{"free plan", map[string]any{"id": "m", "price_omr": 0, "price_baisa": 0}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			catalog := fakeCatalogRows(t, []map[string]any{{"stripe_price_id": "", "slug": "m", "cpu": "2 vCPU"}, c.row})
			defer catalog.Close()
			h := &Handler{CatalogURL: catalog.URL}
			p, err := h.priceFromCatalog(context.Background(), pricingRequest{PlanID: "m", Topology: topologySingleRegion})
			if err != nil {
				t.Fatalf("priceFromCatalog: %v", err)
			}
			if p.PlanBaisa != c.wantBaisa || p.TotalBaisa != c.wantBaisa {
				t.Errorf("plan/total = %d/%d baisa, want %d", p.PlanBaisa, p.TotalBaisa, c.wantBaisa)
			}
			if got := p.WholeOMR(); got != c.wantWhole {
				t.Errorf("WholeOMR = %d, want %d (rounded UP so credit in whole OMR never falls short)", got, c.wantWhole)
			}
		})
	}
}

// TestCatalogPlan_PlanBaisa pins the reader's own rule on the struct, so the
// fallback for a catalog without price_baisa is visible by value.
func TestCatalogPlan_PlanBaisa(t *testing.T) {
	for _, c := range []struct {
		in   catalogPlan
		want int64
	}{
		{catalogPlan{PriceBaisa: 13990, PriceOMR: 13.99}, 13990},
		{catalogPlan{PriceOMR: 7.99}, 7990},
		{catalogPlan{PriceOMR: 2.49}, 2490}, // 2.49*1000 is 2489.999… in binary; rounding, not truncation
		{catalogPlan{PriceOMR: 16}, 16000},
		{catalogPlan{}, 0},
	} {
		if got := c.in.PlanBaisa(); got != c.want {
			t.Errorf("%+v.PlanBaisa() = %d, want %d", c.in, got, c.want)
		}
	}
}
