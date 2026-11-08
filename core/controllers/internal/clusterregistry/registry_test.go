package clusterregistry

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantT   Tier
		wantR   Region
		wantErr bool
	}{
		{"mgmt-A", TierMgmt, RegionA, false},
		{"mgmt-B", TierMgmt, RegionB, false},
		{"dmz-A", TierDmz, RegionA, false},
		{"dmz-B", TierDmz, RegionB, false},
		{"rtz-A", TierRtz, RegionA, false},
		{"rtz-B", TierRtz, RegionB, false},
		// lower-case region tolerated
		{"mgmt-b", TierMgmt, RegionB, false},
		// trimmed
		{"  rtz-A ", TierRtz, RegionA, false},
		// errors
		{"", "", "", true},
		{"mgmt", "", "", true},
		{"mgmt-", "", "", true},
		{"-A", "", "", true},
		{"foo-A", "", "", true},   // unknown tier
		{"mgmt-C", "", "", true},  // unknown region (locked A/B)
		{"mgmt-A1", "", "", true}, // unknown region A1
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
		if got.Tier != c.wantT || got.Region != c.wantR {
			t.Errorf("Parse(%q) = {%s,%s}, want {%s,%s}", c.in, got.Tier, got.Region, c.wantT, c.wantR)
		}
		// round-trip
		if c.in == got.String() || (got.Tier == c.wantT && got.Region == c.wantR) {
			// fine; String() uses upper-case region
		}
	}
}

func TestClusterIDString_RoundTrips(t *testing.T) {
	for _, id := range CanonicalIDs() {
		s := id.String()
		back, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", s, err)
		}
		if back != id {
			t.Fatalf("round-trip %q = %+v, want %+v", s, back, id)
		}
	}
}

func TestCanonicalIDStrings(t *testing.T) {
	got := CanonicalIDStrings()
	want := []string{"dmz-A", "dmz-B", "mgmt-A", "mgmt-B", "rtz-A", "rtz-B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CanonicalIDStrings() = %v, want %v", got, want)
	}
}

func TestIsCanonical(t *testing.T) {
	for _, s := range CanonicalIDStrings() {
		if !IsCanonical(s) {
			t.Errorf("IsCanonical(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "mgmt", "hz-fsn-rtz-prod", "mgmt-C", "vc-mgmt"} {
		if IsCanonical(s) {
			t.Errorf("IsCanonical(%q) = true, want false", s)
		}
	}
}

// TestSecretFor — ADR-0015 D1: the tier alone decides; the region suffix
// never does, because each region's own Flux applies its own legs.
func TestSecretFor(t *testing.T) {
	r := Resolver{
		TierVClusters: map[Tier]TierVCluster{
			TierMgmt: {HostNamespace: "mgmt", KubeconfigSecret: "vc-mgmt"},
			TierRtz:  {HostNamespace: "rtz", KubeconfigSecret: "vc-rtz"},
			// dmz intentionally absent => host placement, no pivot.
		},
	}
	cases := []struct {
		cluster, wantSec, wantNs string
	}{
		{"mgmt-A", "vc-mgmt", "mgmt"},
		{"mgmt-B", "vc-mgmt", "mgmt"},
		{"rtz-A", "vc-rtz", "rtz"},
		{"rtz-B", "vc-rtz", "rtz"},
		{"dmz-A", "", ""},
		{"dmz-B", "", ""},
		{"hz-fsn-rtz-prod", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		sec, ns := r.SecretFor(c.cluster)
		if sec != c.wantSec || ns != c.wantNs {
			t.Errorf("SecretFor(%q) = (%q,%q), want (%q,%q)", c.cluster, sec, ns, c.wantSec, c.wantNs)
		}
	}
}
