package handlers

import (
	"context"
	"net/http"
	"testing"
)

// #4473 — the marketplace funnel posts the plan SLUG ("m"/"l"/"xl"/…) as
// plan_id, but resolvePlanSlug treated EVERY input as a catalog UUID, so a
// funnel-posted slug matched no catalog row and silently fell through to the
// "s" default — every funnel Org provisioned at the S boundary regardless of
// the chosen plan (verified live on prov 91dc05917e44d1c1: plan_id:"m" → Org CR
// spec.planSlug:"s"). These tests lock the slug fast-path + the preserved
// UUID-lookup fallback + the logged-default behavior.

func TestResolvePlanSlug_SlugInReturnsThatSlug(t *testing.T) {
	// A known slug must be returned as-is WITHOUT any catalog round-trip — even
	// with no CatalogURL configured, so it can never silently downgrade.
	h := &Handler{CatalogURL: ""}
	cases := map[string]string{
		"s":     "s",
		"m":     "m",
		"l":     "l",
		"xl":    "xl",
		"flexi": "flexi",
		"free":  "free",
		// case-insensitive + whitespace-tolerant.
		"M":     "m",
		" xl ":  "xl",
		"Flexi": "flexi",
	}
	for in, want := range cases {
		if got := h.resolvePlanSlug(context.Background(), in); got != want {
			t.Errorf("resolvePlanSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePlanSlug_UUIDInResolvesViaCatalog(t *testing.T) {
	// The BSS door posts a plan UUID; it must still resolve via /catalog/plans.
	url := catalogPlansStub(t, http.StatusOK, `[{"id":"plan-m-uuid","slug":"m"},{"id":"plan-l-uuid","slug":"l"}]`)
	h := &Handler{CatalogURL: url}
	if got := h.resolvePlanSlug(context.Background(), "plan-l-uuid"); got != "l" {
		t.Errorf("resolvePlanSlug(plan-l-uuid) = %q, want l", got)
	}
}

func TestResolvePlanSlug_UnknownInDefaultsToS(t *testing.T) {
	// An input that is neither a known slug nor a resolvable catalog UUID falls
	// back to the historical "s" default (now logged, but still "s").
	url := catalogPlansStub(t, http.StatusOK, `[{"id":"plan-m-uuid","slug":"m"}]`)
	h := &Handler{CatalogURL: url}
	if got := h.resolvePlanSlug(context.Background(), "totally-unknown-uuid"); got != "s" {
		t.Errorf("resolvePlanSlug(unknown) = %q, want s (default)", got)
	}
}

func TestResolvePlanSlug_TransientCatalogStillDefaultsToS(t *testing.T) {
	// A non-slug input + a transient catalog failure keeps the historical "s"
	// default for the creation path (resolveTenantPlanSlug carries the richer
	// fail-closed signal for the day-2 path; the creation path resolves once).
	url := catalogPlansStub(t, http.StatusInternalServerError, `boom`)
	h := &Handler{CatalogURL: url}
	if got := h.resolvePlanSlug(context.Background(), "plan-m-uuid"); got != "s" {
		t.Errorf("resolvePlanSlug(uuid, transient) = %q, want s", got)
	}
}

func TestLookupPlanSlug_SlugInIsAuthoritative(t *testing.T) {
	// #4473: a known slug passed to the day-2 resolver is authoritative with no
	// catalog dependency (reachable=true), so a funnel slug is never mistaken
	// for an unreachable-catalog guess.
	h := &Handler{CatalogURL: ""}
	slug, reachable := h.lookupPlanSlug(context.Background(), "xl")
	if !reachable {
		t.Fatalf("a known slug must be reachable=true (authoritative) even with no CatalogURL")
	}
	if slug != "xl" {
		t.Errorf("slug = %q, want xl", slug)
	}
}

func TestIsKnownPlanSlug(t *testing.T) {
	known := []string{"s", "m", "l", "xl", "flexi", "free", "M", " L ", "XL"}
	for _, k := range known {
		if _, ok := isKnownPlanSlug(k); !ok {
			t.Errorf("isKnownPlanSlug(%q) = false, want true", k)
		}
	}
	unknown := []string{"", "  ", "plan-m-uuid", "xxl", "medium", "0", "tier-m"}
	for _, u := range unknown {
		if norm, ok := isKnownPlanSlug(u); ok {
			t.Errorf("isKnownPlanSlug(%q) = true (norm=%q), want false", u, norm)
		}
	}
}
