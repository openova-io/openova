package clusterregistry

import "testing"

// TestParse — an identifier is read as its region alone; the retired tier
// prefix is accepted and dropped, and the bare region parses the same.
func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantR   Region
		wantErr bool
	}{
		{"mgmt-A", RegionA, false},
		{"mgmt-B", RegionB, false},
		{"dmz-A", RegionA, false},
		{"dmz-B", RegionB, false},
		{"rtz-A", RegionA, false},
		{"rtz-B", RegionB, false},
		// the bare region, the form the data takes once the prefix is stripped
		{"A", RegionA, false},
		{"b", RegionB, false},
		// lower-case region tolerated
		{"mgmt-b", RegionB, false},
		// trimmed
		{"  rtz-A ", RegionA, false},
		// errors
		{"", "", true},
		{"mgmt", "", true},
		{"mgmt-", "", true},
		{"-A", "", true},
		{"foo-A", "", true},   // unknown prefix
		{"mgmt-C", "", true},  // unknown region (locked A/B)
		{"mgmt-A1", "", true}, // unknown region A1
		{"C", "", true},
		{"9a1f230f320d7ff9-me-east-215-b-1", "", true},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) = %+v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got.Region != c.wantR {
			t.Errorf("Parse(%q).Region = %q, want %q", c.in, got.Region, c.wantR)
		}
	}
}

// TestClusterIDString — the wire form keeps the prefix the identifier pattern
// admits, so derived release names do not change.
func TestClusterIDString(t *testing.T) {
	for _, c := range []struct {
		r    Region
		want string
	}{{RegionA, "rtz-A"}, {RegionB, "rtz-B"}} {
		s := ClusterID{Region: c.r}.String()
		if s != c.want {
			t.Errorf("ClusterID{%s}.String() = %q, want %q", c.r, s, c.want)
		}
		back, err := Parse(s)
		if err != nil || back.Region != c.r {
			t.Errorf("Parse(%q) = (%+v, %v), want region %s", s, back, err, c.r)
		}
	}
}

func TestIsCanonical(t *testing.T) {
	for _, s := range []string{"mgmt-A", "dmz-B", "rtz-A", "A", "B"} {
		if !IsCanonical(s) {
			t.Errorf("IsCanonical(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "mgmt", "hz-fsn-rtz-prod", "mgmt-C", "vc-mgmt", "9a1f230f320d7ff9"} {
		if IsCanonical(s) {
			t.Errorf("IsCanonical(%q) = true, want false", s)
		}
	}
}

// TestPosition — the region letter as a position, beyond the locked A/B set.
func TestPosition(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"rtz-A", 0, true},
		{"mgmt-B", 1, true},
		{"dmz-c", 2, true},
		{"A", 0, true},
		{"rtz-Z", 25, true},
		{"rtz-AB", 0, false},
		{"rtz-1", 0, false},
		{"foo-A", 0, false},
		{"hz-fsn-rtz-prod", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := Position(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Position(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}
