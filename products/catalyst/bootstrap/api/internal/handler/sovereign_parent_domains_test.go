// sovereign_parent_domains_test.go — guards the canonical four-entry
// org-pool seed surfaced by LoadOrganizationParentDomainsFromEnv. This is
// the regression boundary for DoD D30 (issue #1830 — "free-subdomain
// selection from operator-curated pool").
//
// Why the test matters:
// The marketplace UI (core/marketplace/src/components/AddonsStep.svelte)
// hard-codes the four .omani.X TLDs (homes / rest / trade / works) in
// the subdomain picker. The customer-journey Playwright spec asserts
// every option is present (test "07 subdomain picker shows omani.homes
// pool"). If the backend seed drifts, the Organization tenant create handler's
// FindParentDomain check rejects the operator-supplied parent_domain
// → the customer sees a 422 right at signup despite the UI option
// being shown. Keeping seed + UI + AllowedTLDs locked together is the
// only way the four-domain contract survives across the stack.
package handler

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestLoadOrganizationParentDomainsFromEnv_CanonicalFourEntryPool guards
// the hardcoded fallback path (CATALYST_ORG_POOL_DOMAINS unset). The
// returned slice must carry every .omani.X TLD from
// core/services/domain/store.AllowedTLDs so the marketplace /addons
// picker, the catalyst-api Organization tenant create validator, and the
// store-side TLD allowlist all agree on the pool.
//
// DoD D30 (issue #1830): all four entries (omani.homes, omani.rest,
// omani.trade, omani.works) must be present with Role=org-pool and
// NSFlipReady=true. NSFlipReady=true reflects that these zones are
// already delegated to the Sovereign's PowerDNS at gTLD level — no
// Day-2 Dynadot flip is needed (pdmFlipNS nsAlreadyMatches
// short-circuits).
func TestLoadOrganizationParentDomainsFromEnv_CanonicalFourEntryPool(t *testing.T) {
	// Defensively isolate from the caller's env. Tests may run in
	// parallel; CATALYST_ORG_POOL_DOMAINS and CATALYST_OTECH_FQDN
	// must be unset to exercise the hardcoded fallback path.
	t.Setenv("CATALYST_ORG_POOL_DOMAINS", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	got := LoadOrganizationParentDomainsFromEnv()

	want := []string{"omani.homes", "omani.rest", "omani.trade", "omani.works"}
	names := make([]string, 0, len(got))
	for _, p := range got {
		if !strings.EqualFold(p.Role, "org-pool") {
			// Skip the (none-on-this-path) primary entry if it
			// somehow leaks in — the test's contract is only on
			// the org-pool subset.
			continue
		}
		if !p.NSFlipReady {
			t.Errorf("seed entry %q must be NSFlipReady=true (zone already delegated to Sovereign PowerDNS)", p.Name)
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	if len(names) != len(want) {
		t.Fatalf("seed must contain %d org-pool entries, got %d (%v)", len(want), len(names), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("seed org-pool entries mismatch.\nwant: %v\n got: %v", want, names)
		}
	}
}

// TestLoadOrganizationParentDomainsFromEnv_OTECHFQDNPrimary verifies that
// when CATALYST_OTECH_FQDN is set on the hardcoded-fallback path, the
// otech FQDN is prepended as the role=primary entry. This is the
// post-handover catalyst-api topology where the Sovereign's own FQDN
// becomes the implicit primary and the four .omani.X TLDs are the
// org-pool offered to Organization tenants registering through the marketplace.
func TestLoadOrganizationParentDomainsFromEnv_OTECHFQDNPrimary(t *testing.T) {
	t.Setenv("CATALYST_ORG_POOL_DOMAINS", "")
	t.Setenv("CATALYST_OTECH_FQDN", "t99.example.io")
	got := LoadOrganizationParentDomainsFromEnv()
	if len(got) == 0 || got[0].Name != "t99.example.io" || got[0].Role != "primary" {
		t.Fatalf("first entry must be the OTECH primary, got %+v", got)
	}
	// And the four org-pool entries still follow.
	orgPoolCount := 0
	for _, p := range got {
		if p.Role == "org-pool" {
			orgPoolCount++
		}
	}
	if orgPoolCount != 4 {
		t.Fatalf("OTECH primary + 4 org-pool entries expected; got %d org-pool (%+v)", orgPoolCount, got)
	}
}

// TestLoadOrganizationParentDomainsFromEnv_EnvOverride checks that the
// CATALYST_ORG_POOL_DOMAINS env-var override path still works (operator
// can swap the pool wholesale on a non-omani Sovereign). The hardcoded
// fallback only kicks in when this env var is unset.
func TestLoadOrganizationParentDomainsFromEnv_EnvOverride(t *testing.T) {
	t.Setenv("CATALYST_ORG_POOL_DOMAINS", "first.example:primary,second.example,third.example")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	got := LoadOrganizationParentDomainsFromEnv()
	if len(got) != 3 {
		t.Fatalf("env override should produce 3 entries, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "first.example" || got[0].Role != "primary" {
		t.Errorf("first env entry mismatch: %+v", got[0])
	}
	for _, p := range got[1:] {
		if p.Role != "org-pool" {
			t.Errorf("entries without :role suffix should default to org-pool, got %+v", p)
		}
	}
	// Belt-and-braces: env override must not leak the hardcoded four-entry
	// fallback (regression guard for a confused refactor that double-seeds).
	for _, p := range got {
		if strings.HasSuffix(p.Name, ".omani.homes") || p.Name == "omani.homes" ||
			p.Name == "omani.rest" || p.Name == "omani.trade" || p.Name == "omani.works" {
			t.Errorf("env override path leaked hardcoded fallback entry %q", p.Name)
		}
	}
	// Also assert clean env state.
	if v := os.Getenv("CATALYST_ORG_POOL_DOMAINS"); v == "" {
		t.Fatalf("test setup: env var should be set")
	}
}
