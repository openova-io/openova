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
