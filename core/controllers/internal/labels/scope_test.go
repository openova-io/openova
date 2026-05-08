package labels

import "testing"

func TestScopeMatch_ExactKV(t *testing.T) {
	s := Scope{Key: "openova.io/application", Value: "wordpress"}
	if !s.Match(map[string]string{"openova.io/application": "wordpress"}) {
		t.Fatalf("expected match on exact key/value")
	}
	if s.Match(map[string]string{"openova.io/application": "drupal"}) {
		t.Fatalf("must not match different value")
	}
	if s.Match(map[string]string{"openova.io/env-type": "wordpress"}) {
		t.Fatalf("must not match same value at different key")
	}
}

func TestScopeMatch_KeyWildcard(t *testing.T) {
	// `key=*` — match if the target carries the key, any value.
	s := Scope{Key: "openova.io/env-type", Value: Wildcard}
	if !s.Match(map[string]string{"openova.io/env-type": "dev"}) {
		t.Fatalf("expected match with key=*")
	}
	if !s.Match(map[string]string{"openova.io/env-type": "prod"}) {
		t.Fatalf("key=* must match any value")
	}
	if s.Match(map[string]string{"openova.io/application": "wordpress"}) {
		t.Fatalf("key=* must NOT match a missing key")
	}
}

func TestScopeMatch_ValueWildcard(t *testing.T) {
	// `*=value` — match if any label has the value.
	s := Scope{Key: Wildcard, Value: "dev"}
	if !s.Match(map[string]string{"openova.io/env-type": "dev"}) {
		t.Fatalf("expected match with *=value")
	}
	if !s.Match(map[string]string{"any-key": "dev"}) {
		t.Fatalf("*=value must match any key with that value")
	}
	if s.Match(map[string]string{"any-key": "prod"}) {
		t.Fatalf("*=value must not match any other value")
	}
}

func TestScopeMatch_FullWildcard(t *testing.T) {
	s := Scope{Key: Wildcard, Value: Wildcard}
	if !s.IsWildcard() {
		t.Fatalf("{*,*} should report IsWildcard=true")
	}
	if !s.Match(map[string]string{}) {
		t.Fatalf("{*,*} must match empty target labels")
	}
	if !s.Match(map[string]string{"foo": "bar"}) {
		t.Fatalf("{*,*} must match any labels")
	}
}

func TestAndWithin(t *testing.T) {
	target := map[string]string{
		"openova.io/application":  "wordpress",
		"openova.io/env-type":     "dev",
		"openova.io/organization": "acme",
	}
	cases := []struct {
		name   string
		scopes []Scope
		want   bool
	}{
		{
			name:   "empty scopes = match everything",
			scopes: nil,
			want:   true,
		},
		{
			name: "all match — AND",
			scopes: []Scope{
				{Key: "openova.io/application", Value: "wordpress"},
				{Key: "openova.io/env-type", Value: "dev"},
			},
			want: true,
		},
		{
			name: "one fails — AND short-circuits",
			scopes: []Scope{
				{Key: "openova.io/application", Value: "wordpress"},
				{Key: "openova.io/env-type", Value: "prod"},
			},
			want: false,
		},
		{
			name: "wildcard within AND",
			scopes: []Scope{
				{Key: "openova.io/application", Value: "wordpress"},
				{Key: "openova.io/organization", Value: Wildcard},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AndWithin(tc.scopes, target)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestOrAcross(t *testing.T) {
	target := map[string]string{
		"openova.io/application": "wordpress",
		"openova.io/env-type":    "dev",
	}
	// One UA grants prod-only (won't match), another grants dev (will).
	uaSets := [][]Scope{
		{
			{Key: "openova.io/env-type", Value: "prod"},
		},
		{
			{Key: "openova.io/env-type", Value: "dev"},
		},
	}
	if !OrAcross(uaSets, target) {
		t.Fatalf("OR-across should grant when ANY UA matches")
	}
	// Both fail.
	uaSetsAllFail := [][]Scope{
		{{Key: "openova.io/env-type", Value: "prod"}},
		{{Key: "openova.io/env-type", Value: "stg"}},
	}
	if OrAcross(uaSetsAllFail, target) {
		t.Fatalf("OR-across must not grant when no UA matches")
	}
	// Empty input = no UA = no grant.
	if OrAcross(nil, target) {
		t.Fatalf("nil UA set must not grant")
	}
}

func TestScope_TableMatrix_AllFiveCatalogTiers(t *testing.T) {
	// Exercise the matcher against the 5 catalog-tier shapes per
	// docs/EPICS-1-6-unified-design.md §6.2 — this is the
	// `go test ./internal/labels/... -run TestScope` row required by
	// the slice C5 brief's test plan.
	cases := []struct {
		tier            string
		wantEnforcedLen int
		wantLevel       int
	}{
		{"viewer", 0, 10},
		{"developer", 1, 20}, // enforced: env-type=dev
		{"operator", 0, 30},
		{"admin", 0, 40},
		{"owner", 0, 50},
	}
	for _, tc := range cases {
		t.Run(tc.tier, func(t *testing.T) {
			es := EnforcedScopes(tc.tier)
			if len(es) != tc.wantEnforcedLen {
				t.Fatalf("tier=%s len=%d want=%d", tc.tier, len(es), tc.wantEnforcedLen)
			}
			lvl := TierLevel(tc.tier)
			if lvl != tc.wantLevel {
				t.Fatalf("tier=%s level=%d want=%d", tc.tier, lvl, tc.wantLevel)
			}
		})
	}
	// Unknown tier returns nil (distinct from empty slice — caller
	// can decide to treat as legacy fallback).
	if EnforcedScopes("editor") != nil {
		t.Fatalf("unknown tier must return nil enforced-scope slice (got non-nil)")
	}
	if TierLevel("editor") != 0 {
		t.Fatalf("unknown tier must return level 0")
	}
	// Verify the canonical-tier ordering.
	tiers := CatalogTiers()
	wantOrder := []string{"viewer", "developer", "operator", "admin", "owner"}
	if len(tiers) != len(wantOrder) {
		t.Fatalf("CatalogTiers length: got %d want %d", len(tiers), len(wantOrder))
	}
	for i, want := range wantOrder {
		if tiers[i] != want {
			t.Fatalf("CatalogTiers[%d]=%s want %s", i, tiers[i], want)
		}
	}
	// Verify the level ordering is strictly ascending (the design doc
	// promise: viewer < developer < operator < admin < owner).
	last := -1
	for _, t2 := range tiers {
		l := TierLevel(t2)
		if l <= last {
			t.Fatalf("non-ascending tier levels: %s=%d after level %d", t2, l, last)
		}
		last = l
	}
}

func TestEnforcedScopes_DeveloperEnvType(t *testing.T) {
	es := EnforcedScopes("developer")
	if len(es) != 1 {
		t.Fatalf("developer must auto-inject one scope, got %d", len(es))
	}
	if es[0].Key != "openova.io/env-type" || es[0].Value != "dev" {
		t.Fatalf("developer enforced scope should be openova.io/env-type=dev, got %+v", es[0])
	}
	// Mutating the returned slice MUST NOT contaminate later calls
	// (the function is documented to return a fresh copy each call).
	es[0].Value = "PROD-tampered"
	if EnforcedScopes("developer")[0].Value != "dev" {
		t.Fatalf("EnforcedScopes returned an aliased slice — caller mutation leaked")
	}
}
