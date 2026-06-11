package provisioner

import "testing"

// TestDeriveSecondaryClusterMeshName_MatchesTofuDigitStripping locks in
// the #3241 layer-5 contract: the Go derivation must strip EVERY digit
// run (tofu `replace(r.code, "/[0-9]+/", "")`), not just trailing runs.
// Live on hw128 the divergence left region-a polling a cluster-config
// key region-b never registered (`hw128-me-east-215-b` vs the real
// `hw128-me-east--b`) — etcd connected, retrieved=false forever.
func TestDeriveSecondaryClusterMeshName_MatchesTofuDigitStripping(t *testing.T) {
	req := Request{
		SovereignFQDN: "hw128.omani.works",
		Regions: []RegionSpec{
			{Provider: "huawei", CloudRegion: "me-east-215-a"},
			{Provider: "huawei", CloudRegion: "me-east-215-b"},
		},
	}
	cases := []struct {
		cloudRegion string
		want        string
	}{
		// kom4dc shape — interior digit run (the live hw128 bug).
		{"me-east-215-b", "hw128-me-east--b"},
		// Hetzner shapes — trailing run only; behaviour unchanged.
		{"hel1", "hw128-hel"},
		{"fsn1", "hw128-fsn"},
		{"nbg1", "hw128-nbg"},
		// No digits at all.
		{"sin", "hw128-sin"},
	}
	for _, c := range cases {
		got := DeriveSecondaryClusterMeshName(req, RegionSpec{Provider: "huawei", CloudRegion: c.cloudRegion})
		if got != c.want {
			t.Errorf("CloudRegion %q: got %q, want %q (must match tofu digit-run stripping)", c.cloudRegion, got, c.want)
		}
	}

	// Explicit per-region override always wins.
	if got := DeriveSecondaryClusterMeshName(req, RegionSpec{CloudRegion: "me-east-215-b", ClusterMeshName: "explicit-name"}); got != "explicit-name" {
		t.Errorf("explicit ClusterMeshName override ignored: got %q", got)
	}
}
