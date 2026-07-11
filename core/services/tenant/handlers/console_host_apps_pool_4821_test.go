package handlers

// console_host_apps_pool_4821_test.go — #4821 Finding-2 → #4999 contract.
//
// The tenant-service returns a SERVER-AUTHORITATIVE `console_host`
// (`console.<slug>.<parentDomain>`) that the marketplace funnel redirects to
// after Launch (CheckoutStep.svelte::setActiveOrgConsoleHost). The
// org-controller — the door that actually WRITES the per-Org DNS A-record +
// console TLS cert + HTTPRoute — stamps its pool via the identical resolver
// (organization_create.go::resolveOrgParentDomain), so both doors AND the apps
// generator agree on ONE zone per Org.
//
// #4821 Finding-2 (the original bug this file guarded) was the DISAGREEMENT
// where this service returned the raw funnel pick while the org-controller
// provisioned the apps pool, yielding an NXDOMAIN console_host. The old fix
// forced the apps pool to WIN (#4421) — which silently DROPPED every non-primary
// funnel pick, so "two Orgs on two different TLDs" (Pillar-1) could never hold.
//
// #4999 supersedes that: the pick is HONORED when the Sovereign serves that pool
// zone, and BOTH doors + the apps generator follow it — so the two doors stay in
// lockstep (no NXDOMAIN) AND the customer's chosen TLD is actually provisioned.

import (
	"testing"

	"github.com/openova-io/openova/core/services/tenant/store"
)

func TestResolveOrgParentDomain_HonorsServedPick(t *testing.T) {
	cases := []struct {
		name        string
		appsPool    string
		poolDomains []string
		funnelPick  string
		want        string
	}{
		{
			// The #4999 core: funnel offered omani.rest (a served pool zone),
			// apps pool is omani.homes → HONOR the pick. (Pre-#4999 this returned
			// omani.homes and dropped the customer's choice.)
			name:       "served_pick_is_honored",
			appsPool:   "omani.homes",
			funnelPick: "omani.rest",
			want:       "omani.rest",
		},
		{
			// Funnel pick agrees with the apps pool → no change.
			name:       "pick_matches_apps_pool",
			appsPool:   "omani.homes",
			funnelPick: "omani.homes",
			want:       "omani.homes",
		},
		{
			// Funnel offered nothing → apps pool supplies the zone (keeps
			// console_host resolvable when the funnel omits parent_domain).
			name:       "empty_pick_uses_apps_pool",
			appsPool:   "omani.homes",
			funnelPick: "",
			want:       "omani.homes",
		},
		{
			// Pick is NOT a served pool zone (the #4421 dead-IP guard) → fall
			// back to the apps pool so we never provision under an unserved zone.
			name:       "unserved_pick_falls_back_to_apps_pool",
			appsPool:   "omani.homes",
			funnelPick: "evil.example.com",
			want:       "omani.homes",
		},
		{
			// An explicit served-set that EXCLUDES the pick → fall back. Proves
			// the offer is gated on TENANT_POOL_DOMAINS, not blindly trusted.
			name:        "pick_outside_configured_set_falls_back",
			appsPool:    "omani.homes",
			poolDomains: []string{"omani.homes"}, // only homes served here
			funnelPick:  "omani.rest",
			want:        "omani.homes",
		},
		{
			// Degenerate / single-domain Sovereign with NO apps pool wired →
			// fall back to the pick, matching the org-controller's empty-appsPool
			// fallback.
			name:       "no_apps_pool_falls_back_to_pick",
			appsPool:   "",
			funnelPick: "omani.rest",
			want:       "omani.rest",
		},
		{
			name:       "both_empty",
			appsPool:   "",
			funnelPick: "",
			want:       "",
		},
		{
			// Casing + surrounding whitespace + a leading dot are normalised, and
			// the (served) pick still wins.
			name:       "pick_normalised_and_honored",
			appsPool:   "omani.homes",
			funnelPick: "  .Omani.Rest  ",
			want:       "omani.rest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveOrgParentDomain(tc.appsPool, tc.poolDomains, tc.funnelPick); got != tc.want {
				t.Fatalf("resolveOrgParentDomain(%q, %v, %q) = %q, want %q",
					tc.appsPool, tc.poolDomains, tc.funnelPick, got, tc.want)
			}
		})
	}
}

// TestConsoleHost_HonorsServedFunnelPick is the end-to-end #4999 contract: the
// console_host this service returns is composed from the HONORED funnel pick
// (when served) — so a 2nd Org on `.omani.rest` lands on console.<slug>.omani.rest,
// which the org-controller (same resolver) provisions. Reproduces the hw240
// walk-stranger-two scenario that regressed to omani.homes pre-#4999.
func TestConsoleHost_HonorsServedFunnelPick(t *testing.T) {
	const (
		slug       = "walk-stranger-two"
		appsPool   = "omani.homes" // Sovereign primary apps pool
		funnelPick = "omani.rest"  // the customer's chosen (served) pool TLD
	)

	// Mirror CreateOrg: the persisted Tenant.ParentDomain is the RESOLVED pool.
	resolved := resolveOrgParentDomain(appsPool, nil, funnelPick)
	tenant := &store.Tenant{Subdomain: slug, ParentDomain: resolved}

	got := deriveTenantConsoleHost(tenant)
	const want = "console.walk-stranger-two.omani.rest"
	if got != want {
		t.Fatalf("console_host = %q, want %q (the honored funnel pick, #4999)", got, want)
	}

	// Regression guard: it must NOT collapse back to the primary apps pool.
	if got == "console.walk-stranger-two.omani.homes" {
		t.Fatalf("console_host regressed to the primary apps pool — the funnel TLD choice was dropped (pre-#4999 #4421 behavior)")
	}
}

// TestConsoleHost_LegacySingleDomain_UsesFunnelPick asserts back-compat: a
// Sovereign with NO apps pool wired (AppsParentDomain empty) still honours the
// funnel pick verbatim, so #4176/#4179 separate-pool Sovereigns are unaffected.
func TestConsoleHost_LegacySingleDomain_UsesFunnelPick(t *testing.T) {
	resolved := resolveOrgParentDomain("", nil, "omani.works")
	tenant := &store.Tenant{Subdomain: "demo", ParentDomain: resolved}
	if got, want := deriveTenantConsoleHost(tenant), "console.demo.omani.works"; got != want {
		t.Fatalf("legacy console_host = %q, want %q", got, want)
	}
}
