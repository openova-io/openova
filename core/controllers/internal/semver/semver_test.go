package semver

import "testing"

func TestIsValidRange(t *testing.T) {
	t.Parallel()

	// Cases gathered from a sweep of every existing
	// platform/*/blueprint.yaml `upgrades.from[]` entry. Slice CC1
	// merged the C3 (blueprint) and C4 (application) test corpora.
	valid := []string{
		"0.x",
		"1.x",
		"1.0.x",
		"1.1.x",
		"1.2.x",
		"^1.0",
		"^1.4",
		"~1.4",
		"1.0.0",
		"1.2.3",
		"1.2.3-beta.1",
		"1.0.0-rc.1",
		"1.0.0-beta-2",
		"1.0.0+build.5",
		">=1.0.0",
		"<2",
		"<2.0.0",
		">=1.0.0 <2",
		">=1.0.0 <2.0.0",
		"=1.0.0",
		"x.x.x",
		"1",
		"*",
	}
	for _, s := range valid {
		if err := IsValidRange(s); err != nil {
			t.Errorf("IsValidRange(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"abc",
		"1.2.3.4",
		"1..2",
		"1.0..0",
		"^",
		"~",
		">=",
		"v1.0.0", // node-semver allows the v-prefix, but our existing
		// corpus does not use it; reject to keep the surface tight.
		"1.0.0-",
		"1.0.0+",
		"1..",
		">=foo",
		"1.0.0-rc.",
	}
	for _, s := range invalid {
		if err := IsValidRange(s); err == nil {
			t.Errorf("IsValidRange(%q) = nil, want error", s)
		}
	}
}

func TestIsExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want bool
	}{
		{"1.0.0", true},
		{"1.2.3-beta.1", true},
		{"1.0", false},
		{"^1.0.0", false},
		{"1.0.x", false},
		{"1.x", false},
		{"", false},
		{"abc", false},
	}
	for _, tc := range cases {
		got := IsExact(tc.in)
		if got != tc.want {
			t.Errorf("IsExact(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
