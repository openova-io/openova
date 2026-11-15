package config

import "testing"

// The projected-token path env was renamed PLATFORM_API_TOKEN_FILE →
// PLATFORM_API_BEARER_FILE (the Sovereign's Kyverno `secret-not-in-env`
// policy flags a TOKEN-named env carrying a literal value). Both names are
// read for one release; the new one wins when both are set.
func TestPlatformAPIBearerFileEnvAndDeprecatedAlias(t *testing.T) {
	cases := []struct {
		name, bearer, legacy, want string
	}{
		{"new name only", "/run/new", "", "/run/new"},
		{"deprecated alias only", "", "/run/old", "/run/old"},
		{"both set — new name wins", "/run/new", "/run/old", "/run/new"},
		{"neither", "", "", ""},
		{"whitespace-only new name falls back to the alias", "  ", "/run/old", "/run/old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PLATFORM_API_BEARER_FILE", tc.bearer)
			t.Setenv("PLATFORM_API_TOKEN_FILE", tc.legacy)
			c, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if c.PlatformAPITokenFile != tc.want {
				t.Fatalf("PlatformAPITokenFile = %q, want %q", c.PlatformAPITokenFile, tc.want)
			}
		})
	}
}

// DEFAULT_OFFER (DESIGN.md §22.12) is on by default for the sovereign profile
// — a fresh Sovereign must publish the offer the engine ships with — and off
// for operator-central, which sells no packages. An explicit value wins.
func TestDefaultOfferOnByDefaultForTheSovereignProfile(t *testing.T) {
	cases := []struct {
		profile, env string
		want         bool
	}{
		{"", "", true},
		{"sovereign", "", true},
		{"operator-central", "", false},
		{"sovereign", "false", false},
		{"operator-central", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.profile+"/"+tc.env, func(t *testing.T) {
			t.Setenv("PROFILE", tc.profile)
			t.Setenv("DEFAULT_OFFER", tc.env)
			c, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if c.DefaultOffer != tc.want {
				t.Fatalf("PROFILE=%q DEFAULT_OFFER=%q: DefaultOffer = %v, want %v", tc.profile, tc.env, c.DefaultOffer, tc.want)
			}
		})
	}
}
