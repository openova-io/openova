package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Tests for UAT row 7 — the PURCHASED PLAN is absent from the Organization
// wire payload.
//
// The Organization CR carries spec.planSlug (the #4292 split: `tier` is the
// isolation class, `planSlug` is the purchased plan and the single truth
// source for the ResourceQuota/LimitRange the org-controller materialises).
// org_list_from_cr.go reads it and stamps it onto the provision record
// (PlanSlug), and the STORE record serialises it as `plan_slug`. But
// orgTenantResponse never declared the field, so orgTenantRecordToResponse
// dropped it on the floor: every consumer of GET /api/v1/organizations saw an
// Org with no plan at all.
//
// The assertions below are on the wire KEY AND ITS VALUE, deliberately:
//
//   - Asserting the key alone would pass on `plan_slug: ""`, which is exactly
//     the state the console cannot render. A guard that goes green on an empty
//     value is not a guard.
//   - Asserting via the typed struct alone cannot see the key NAME, and the
//     key name is load-bearing here: the front-end reads this row and a
//     mis-named key is indistinguishable from an absent one. The raw-JSON half
//     pins the contract the SPA actually binds.
//
// Each case carries a DIFFERENT plan so the pair discriminates: a fix that
// hardcoded any single literal would satisfy one case and fail the other.

// planCR builds an Organization CR with an explicit planSlug. orgReadyCR
// hardcodes planSlug "s", which cannot show that the response reports the
// ACTUAL plan rather than a constant.
func planCR(slug, planSlug string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name":   slug,
			"labels": map[string]any{"openova.io/tenant-id": "tid-" + slug},
		},
		"spec": map[string]any{
			"slug":         slug,
			"displayName":  slug,
			"kind":         "customer",
			"tier":         "org",
			"planSlug":     planSlug,
			"billingMode":  "real",
			"sovereignRef": "otech.example",
			"owners": []any{
				map[string]any{"email": "owner@" + slug + ".example", "role": "owner"},
			},
		},
		"status": map[string]any{"vcluster": map[string]any{"phase": "Ready"}},
	}}
}

// rowJSONFor issues the real list call and returns both the typed row and the
// raw JSON object for the single seeded Org.
func rowJSONFor(t *testing.T, planSlug string) (orgTenantResponse, map[string]json.RawMessage) {
	t.Helper()
	h, _ := newOrgHandlerWithSeededCRs(t, planCR("acme", planSlug))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}

	var typed struct {
		Items []orgTenantResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &typed); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if len(typed.Items) != 1 {
		t.Fatalf("items: want 1 got %d: %s", len(typed.Items), w.Body.String())
	}

	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	return typed.Items[0], raw.Items[0]
}

// TestOrgResponse_CarriesPurchasedPlan_Row7 — an Org on plan `m` reports
// plan_slug "m" on the wire.
func TestOrgResponse_CarriesPurchasedPlan_Row7(t *testing.T) {
	row, raw := rowJSONFor(t, "m")

	if row.PlanSlug != "m" {
		t.Errorf("typed row: plan_slug want %q got %q", "m", row.PlanSlug)
	}

	blob, present := raw["plan_slug"]
	if !present {
		t.Fatalf("wire JSON must carry the plan_slug key; got keys %v", rawKeys(raw))
	}
	var got string
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("decode plan_slug: %v", err)
	}
	if got != "m" {
		t.Errorf("wire plan_slug: want %q got %q", "m", got)
	}
}

// TestOrgResponse_PurchasedPlanIsTheActualPlan_Row7 is the discriminating
// control: the SAME assertion against a different plan. A response that
// hardcoded "m" would pass the case above and fail here.
func TestOrgResponse_PurchasedPlanIsTheActualPlan_Row7(t *testing.T) {
	row, raw := rowJSONFor(t, "xl")

	if row.PlanSlug != "xl" {
		t.Errorf("typed row: plan_slug want %q got %q", "xl", row.PlanSlug)
	}
	var got string
	if err := json.Unmarshal(raw["plan_slug"], &got); err != nil {
		t.Fatalf("decode plan_slug: %v", err)
	}
	if got != "xl" {
		t.Errorf("wire plan_slug: want %q got %q", "xl", got)
	}

	// CONTROL — the fields this row already reported correctly must survive.
	// Row 7 asserts NINE canonical fields and eight of them were already
	// right; a "fix" that reshaped the payload and broke them would be a
	// regression wearing a green test.
	if row.Kind != "customer" {
		t.Errorf("control kind: want customer got %q", row.Kind)
	}
	if row.Tier != "org" {
		t.Errorf("control tier: want org got %q", row.Tier)
	}
	if row.BillingMode != "real" {
		t.Errorf("control billing_mode: want real got %q", row.BillingMode)
	}
	// plan xl is a vcluster-tier plan (#4292 gate) — isolation is DERIVED
	// from planSlug, so this also pins that the new field and the existing
	// derivation still agree.
	if row.Isolation != "vcluster" {
		t.Errorf("control isolation: want vcluster for plan xl got %q", row.Isolation)
	}
}

func rawKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
