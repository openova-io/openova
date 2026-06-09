package handlers

import (
	"testing"

	"github.com/openova-io/openova/core/services/catalog/store"
)

// TestFilterPlansByProduct guards the ?product= scoping that keeps
// product-scoped tiers (Sandbox Free/Pro/Ent) out of the generic
// Org-provisioning plan picker while preserving the unfiltered default
// the tenant service's GetPlan slug-walk depends on (#3156).
func TestFilterPlansByProduct(t *testing.T) {
	plans := []store.Plan{
		{Slug: "s", ProductSlug: ""},
		{Slug: "m", ProductSlug: ""},
		{Slug: "l", ProductSlug: ""},
		{Slug: "xl", ProductSlug: ""},
		{Slug: "flexi", ProductSlug: ""},
		{Slug: "sandbox-free", ProductSlug: "sandbox"},
		{Slug: "sandbox-pro", ProductSlug: "sandbox"},
		{Slug: "sandbox-ent", ProductSlug: "sandbox"},
	}

	// Empty product → unchanged. The tenant service's GetPlan walks this
	// full list to resolve a sandbox-* slug; filtering the default would
	// break sandbox checkout with "plan_id not found".
	if got := filterPlansByProduct(plans, ""); len(got) != len(plans) {
		t.Fatalf(`product="" len = %d, want %d (back-compat)`, len(got), len(plans))
	}

	// generic → only unscoped compute tiers (what the marketplace picker
	// requests). No sandbox row may leak.
	generic := filterPlansByProduct(plans, "generic")
	if len(generic) != 5 {
		t.Fatalf("product=generic len = %d, want 5", len(generic))
	}
	for _, p := range generic {
		if p.ProductSlug != "" {
			t.Errorf("generic filter leaked product-scoped plan %q (ProductSlug=%q)", p.Slug, p.ProductSlug)
		}
	}

	// sandbox → only the Sandbox product tiers.
	sandbox := filterPlansByProduct(plans, "sandbox")
	if len(sandbox) != 3 {
		t.Fatalf("product=sandbox len = %d, want 3", len(sandbox))
	}
	for _, p := range sandbox {
		if p.ProductSlug != "sandbox" {
			t.Errorf("sandbox filter leaked %q (ProductSlug=%q)", p.Slug, p.ProductSlug)
		}
	}

	// Unknown product → empty, never a panic.
	if got := filterPlansByProduct(plans, "nope"); len(got) != 0 {
		t.Errorf("product=nope len = %d, want 0", len(got))
	}
}
