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

// TestSecretFor is the core DoD-4 contract: canonical IDs resolve to the
// right kubeConfig Secret under the split-side model.
func TestSecretFor(t *testing.T) {
	// Resolver running on the primary (region A) mgmt cluster, with the
	// loft-sh vCluster convention for the three tiers (mgmt has a
	// vCluster; rtz has a vCluster; dmz on host in this fixture).
	r := Resolver{
		LocalRegion: RegionA,
		TierVClusters: map[Tier]TierVCluster{
			TierMgmt: {HostNamespace: "mgmt", KubeconfigSecret: "vc-mgmt"},
			TierRtz:  {HostNamespace: "rtz", KubeconfigSecret: "vc-rtz"},
			// dmz intentionally absent ⇒ host placement, no pivot.
		},
	}

	cases := []struct {
		name    string
		cluster string
		wantSec string
		wantNs  string
	}{
		// same region, tier has a vCluster → local pivot
		{"local mgmt → vc-mgmt", "mgmt-A", "vc-mgmt", "mgmt"},
		{"local rtz → vc-rtz", "rtz-A", "vc-rtz", "rtz"},
		// same region, tier on host → no pivot
		{"local dmz on host → none", "dmz-A", "", ""},
		// other region, no remote secret → split-side default (no pivot)
		{"remote mgmt-B → split-side none", "mgmt-B", "", ""},
		{"remote rtz-B → split-side none", "rtz-B", "", ""},
		{"remote dmz-B → split-side none", "dmz-B", "", ""},
		// non-canonical → no pivot
		{"legacy bare name → none", "hz-fsn-rtz-prod", "", ""},
		{"empty → none", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sec, ns := r.SecretFor(c.cluster)
			if sec != c.wantSec || ns != c.wantNs {
				t.Fatalf("SecretFor(%q) = (%q,%q), want (%q,%q)", c.cluster, sec, ns, c.wantSec, c.wantNs)
			}
		})
	}
}

// TestSecretFor_RemoteWired proves the opt-in cross-region path: when an
// operator wires a remote-region kubeconfig Secret, a -B cluster ID
// resolves to it (the rare row that genuinely needs one control plane to
// write into the other; none in the §6 reference set, but the seam must
// support it).
func TestSecretFor_RemoteWired(t *testing.T) {
	r := Resolver{
		LocalRegion: RegionA,
		TierVClusters: map[Tier]TierVCluster{
			TierMgmt: {HostNamespace: "mgmt", KubeconfigSecret: "vc-mgmt"},
		},
		RemoteRegionSecrets: map[Region]RemoteRegionSecret{
			RegionB: {Name: "region-b-kubeconfig", Namespace: "flux-system"},
		},
	}
	sec, ns := r.SecretFor("mgmt-B")
	if sec != "region-b-kubeconfig" || ns != "flux-system" {
		t.Fatalf("SecretFor(mgmt-B) with remote wired = (%q,%q), want (region-b-kubeconfig,flux-system)", sec, ns)
	}
	// local still uses the vCluster pivot, not the remote secret
	sec, ns = r.SecretFor("mgmt-A")
	if sec != "vc-mgmt" || ns != "mgmt" {
		t.Fatalf("SecretFor(mgmt-A) = (%q,%q), want (vc-mgmt,mgmt)", sec, ns)
	}
}

// TestSecretFor_LocalRegionB proves symmetry: a controller running in
// region B resolves -B IDs locally and -A IDs split-side.
func TestSecretFor_LocalRegionB(t *testing.T) {
	r := Resolver{
		LocalRegion: RegionB,
		TierVClusters: map[Tier]TierVCluster{
			TierRtz: {HostNamespace: "rtz", KubeconfigSecret: "vc-rtz"},
		},
	}
	if sec, ns := r.SecretFor("rtz-B"); sec != "vc-rtz" || ns != "rtz" {
		t.Fatalf("region-B controller SecretFor(rtz-B) = (%q,%q), want (vc-rtz,rtz)", sec, ns)
	}
	if sec, ns := r.SecretFor("rtz-A"); sec != "" || ns != "" {
		t.Fatalf("region-B controller SecretFor(rtz-A) = (%q,%q), want split-side none", sec, ns)
	}
}

// TestSecretFor_DefaultLocalRegion proves the LocalRegion="" default is
// RegionA (so an unconfigured controller behaves as the primary).
func TestSecretFor_DefaultLocalRegion(t *testing.T) {
	r := Resolver{
		TierVClusters: map[Tier]TierVCluster{
			TierMgmt: {HostNamespace: "mgmt", KubeconfigSecret: "vc-mgmt"},
		},
	}
	if sec, _ := r.SecretFor("mgmt-A"); sec != "vc-mgmt" {
		t.Fatalf("default-LocalRegion SecretFor(mgmt-A) = %q, want vc-mgmt (RegionA default)", sec)
	}
	if sec, _ := r.SecretFor("mgmt-B"); sec != "" {
		t.Fatalf("default-LocalRegion SecretFor(mgmt-B) = %q, want split-side none", sec)
	}
}
