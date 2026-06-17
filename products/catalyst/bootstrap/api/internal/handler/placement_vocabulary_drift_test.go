// placement_vocabulary_drift_test.go — #3375 DoD-1 drift guard for the
// catalyst-api surface.
//
// Pins the ONE canonical placement/topology vocabulary the catalyst-api
// emits/accepts (canonicalizeTopology + placementForTopology) to the
// SAME four canonical tokens the Go placement package
// (core/controllers/internal/placement.CanonicalModes), the catalog
// placementSchema, and both FE editors agree on:
//
//	singleton | active-active | active-hot-standby | active-passive
//
// A future edit that re-introduces the legacy editor dialect
// (single-region / active-hotstandby) as a PRIMARY emitted value, or
// drops a canonical class, fails CI here.
package handler

import "testing"

// canonicalPlacementVocabulary is the single literal this surface pins
// to. It must stay byte-equal to placement.CanonicalModes() (Go),
// CANONICAL_MODES (bootstrap-ui drift test), and BcpTopology (console
// types.ts). The bootstrap-api module cannot import the controllers'
// internal/placement package (Go forbids cross-module internal imports),
// so the contract is asserted by literal here and pinned to the same set
// on every other surface.
var canonicalPlacementVocabulary = []string{
	"singleton",
	"active-active",
	"active-hot-standby",
	"active-passive",
}

func TestCanonicalizeTopology_FoldsToCanonicalVocabulary(t *testing.T) {
	// Every canonical token canonicalises to itself.
	for _, m := range canonicalPlacementVocabulary {
		if got := canonicalizeTopology(m); got != m {
			t.Errorf("canonicalizeTopology(%q) = %q, want itself (canonical)", m, got)
		}
	}
	// Legacy spellings fold onto the canonical token — never the reverse.
	legacy := map[string]string{
		"single-region":      "singleton",
		"single_region":      "singleton",
		"active-hotstandby":  "active-hot-standby",
		"active_hot_standby": "active-hot-standby",
		"active_active":      "active-active",
		"active_passive":     "active-passive",
		"  Active-Passive ":  "active-passive",
	}
	for in, want := range legacy {
		if got := canonicalizeTopology(in); got != want {
			t.Errorf("canonicalizeTopology(%q) = %q, want %q", in, got, want)
		}
	}
	// An unknown value is returned trimmed (so the caller rejects it with
	// a clean error) — NOT silently coerced to a canonical token.
	if got := canonicalizeTopology("bogus-mode"); got != "bogus-mode" {
		t.Errorf("canonicalizeTopology(bogus) = %q, want pass-through", got)
	}
}

func TestPlacementForTopology_EmitsOnlyCanonicalTokens(t *testing.T) {
	// The CR-stamp mapper must only ever emit a canonical token — for
	// canonical input, legacy input, or unknown/empty (→ singleton).
	canonSet := map[string]struct{}{}
	for _, m := range canonicalPlacementVocabulary {
		canonSet[m] = struct{}{}
	}
	inputs := []string{
		"singleton", "active-active", "active-hot-standby", "active-passive", // canonical
		"single-region", "active-hotstandby", // legacy
		"", "garbage", "multi-region", // unknown → singleton
	}
	for _, in := range inputs {
		got := placementForTopology(in)
		if _, ok := canonSet[got]; !ok {
			t.Errorf("placementForTopology(%q) = %q, which is NOT a canonical token %v",
				in, got, canonicalPlacementVocabulary)
		}
	}
	// Specific foldings that pin the one-vocabulary contract.
	cases := map[string]string{
		"single-region":     "singleton",
		"active-hotstandby":  "active-hot-standby",
		"active-passive":     "active-passive",
		"":                   "singleton",
		"multi-region":       "singleton", // a Sovereign posture, never an instance mode
	}
	for in, want := range cases {
		if got := placementForTopology(in); got != want {
			t.Errorf("placementForTopology(%q) = %q, want %q", in, got, want)
		}
	}
}

// The install + update + preview wire-validation MUST accept every
// canonical class and the legacy aliases, and reject a genuine unknown.
func TestApplicationWireValidation_AcceptsCanonicalVocabulary(t *testing.T) {
	base := func(mode string) applicationInstallRequest {
		return applicationInstallRequest{
			BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
			Name:            "site",
			OrganizationRef: "acme",
			EnvironmentRef:  "acme-prod",
			Placement:       applicationPlacement{Mode: mode, Regions: []string{"hz-fsn-rtz-prod"}},
		}
	}
	accept := append([]string{}, canonicalPlacementVocabulary...)
	accept = append(accept, "single-region", "active-hotstandby") // legacy still admissible
	for _, m := range accept {
		if msg, ok := validateApplicationInstallRequest(base(m)); !ok {
			t.Errorf("install validation rejected accepted mode %q: %s", m, msg)
		}
	}
	if _, ok := validateApplicationInstallRequest(base("not-a-mode")); ok {
		t.Errorf("install validation accepted an unknown mode")
	}
}
