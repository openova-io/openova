package semver

import "testing"

func TestIsValidRange(t *testing.T) {
	cases := []struct {
		in    string
		wantE bool
	}{
		{"1.0.0", false},
		{"1.2.3-beta.1", false},
		{"^1.4", false},
		{"~1.4", false},
		{"1.x", false},
		{"1.0.x", false},
		{">=1.0.0 <2", false},
		{"", true},
		{"1.0..0", true},
		{"abc", true},
	}
	for _, tc := range cases {
		err := IsValidRange(tc.in)
		if (err != nil) != tc.wantE {
			t.Errorf("IsValidRange(%q) = %v, wantErr=%v", tc.in, err, tc.wantE)
		}
	}
}

func TestIsExact(t *testing.T) {
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
