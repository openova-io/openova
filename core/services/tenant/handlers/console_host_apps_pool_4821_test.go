package handlers

// console_host_apps_pool_4821_test.go — #4821 Finding-2 contract.
//
// The tenant-service returns a SERVER-AUTHORITATIVE `console_host`
// (`console.<slug>.<parentDomain>`) that the marketplace funnel redirects to
// after Launch (CheckoutStep.svelte::setActiveOrgConsoleHost). The
// org-controller — the door that actually WRITES the per-Org DNS A-record +
// console TLS cert + HTTPRoute — stamps its pool via the identical
// apps-pool-wins rule (organization_create.go::resolveOrgParentDomain, #4421):
// the Sovereign's EFFECTIVE apps pool (env TENANT_PARENT_DOMAIN) WINS over the
// funnel-selected TLD.
//
// Finding-2 was the DISAGREEMENT: on hw228 the funnel offered `.omani.rest`
// but the Sovereign's single apps pool is `omani.homes`. The org-controller
// provisioned `console.acme.omani.homes`, while this service returned
// `console.acme.omani.rest` (the raw funnel pick) — an NXDOMAIN host — so the
// post-Launch redirect 404'd. These tests lock the two doors to the SAME pool.

import (
	"testing"

	"github.com/openova-io/openova/core/services/tenant/store"
)

func TestResolveOrgParentDomain_AppsPoolWins(t *testing.T) {
	cases := []struct {
		name       string
		appsPool   string
		funnelPick string
		want       string
	}{
		{
			// The exact #4821 Finding-2 shape: funnel offered omani.rest,
			// Sovereign apps pool is omani.homes → apps pool WINS.
			name:       "funnel_pick_differs_apps_pool_wins",
			appsPool:   "omani.homes",
			funnelPick: "omani.rest",
			want:       "omani.homes",
		},
		{
			// Funnel pick agrees with the apps pool (the #4176 separate-pool
			// happy path) → no change, both resolve to the same value.
			name:       "funnel_pick_matches_apps_pool",
			appsPool:   "omani.homes",
			funnelPick: "omani.homes",
			want:       "omani.homes",
		},
		{
			// Funnel offered nothing but the Sovereign has an apps pool →
			// apps pool supplies the zone (this is what makes console_host
			// resolvable even when the funnel omits parent_domain).
			name:       "empty_funnel_pick_uses_apps_pool",
			appsPool:   "omani.homes",
			funnelPick: "",
			want:       "omani.homes",
		},
		{
			// Degenerate / single-domain Sovereign with NO apps pool wired →
			// fall back to the funnel pick, matching the org-controller's own
			// empty-appsPool fallback (resolveOrgParentDomain returns payload).
			name:       "no_apps_pool_falls_back_to_funnel_pick",
			appsPool:   "",
			funnelPick: "omani.rest",
			want:       "omani.rest",
		},
		{
			// Neither configured → empty (deriveTenantConsoleHost then yields
			// "" and the client keeps its host-splice fallback).
			name:       "both_empty",
			appsPool:   "",
			funnelPick: "",
			want:       "",
		},
		{
			// Casing + surrounding whitespace are normalised.
			name:       "apps_pool_normalised",
			appsPool:   "  Omani.Homes  ",
			funnelPick: "omani.rest",
			want:       "omani.homes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveOrgParentDomain(tc.appsPool, tc.funnelPick); got != tc.want {
				t.Fatalf("resolveOrgParentDomain(%q, %q) = %q, want %q",
					tc.appsPool, tc.funnelPick, got, tc.want)
			}
		})
	}
}

// TestConsoleHost_MatchesOrgControllerPool is the end-to-end Finding-2 contract:
// the console_host this service returns MUST be composed from the apps-pool-
// resolved parent domain, NOT the raw funnel pick — so it equals the host the
// org-controller provisions. Reproduces the hw228 acme scenario exactly.
func TestConsoleHost_MatchesOrgControllerPool(t *testing.T) {
	const (
		slug       = "acme"
		appsPool   = "omani.homes" // what the Sovereign actually serves + org-controller writes
		funnelPick = "omani.rest"  // what the funnel offered (Finding-2)
	)

	// Mirror CreateOrg: the persisted Tenant.ParentDomain is the RESOLVED pool.
	resolved := resolveOrgParentDomain(appsPool, funnelPick)
	tenant := &store.Tenant{Subdomain: slug, ParentDomain: resolved}

	got := deriveTenantConsoleHost(tenant)
	const want = "console.acme.omani.homes"
	if got != want {
		t.Fatalf("console_host = %q, want %q (must match the org-controller's provisioned pool, not the funnel pick)", got, want)
	}

	// Guard against the regression: the returned host must NOT carry the raw
	// funnel TLD, which the org-controller never provisions (→ NXDOMAIN → 404).
	badTenant := &store.Tenant{Subdomain: slug, ParentDomain: funnelPick}
	if bad := deriveTenantConsoleHost(badTenant); bad == got {
		t.Fatalf("resolved host collided with the raw funnel-pick host %q — the apps-pool override did not take effect", bad)
	}
}

// TestConsoleHost_LegacySingleDomain_UsesFunnelPick asserts back-compat: a
// Sovereign with NO apps pool wired (AppsParentDomain empty) still honours the
// funnel pick verbatim, so #4176/#4179 separate-pool Sovereigns are unaffected.
func TestConsoleHost_LegacySingleDomain_UsesFunnelPick(t *testing.T) {
	resolved := resolveOrgParentDomain("", "omani.works")
	tenant := &store.Tenant{Subdomain: "demo", ParentDomain: resolved}
	if got, want := deriveTenantConsoleHost(tenant), "console.demo.omani.works"; got != want {
		t.Fatalf("legacy console_host = %q, want %q", got, want)
	}
}
