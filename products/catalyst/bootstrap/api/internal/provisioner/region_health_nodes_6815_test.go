package provisioner

import "testing"

// region_health_nodes_6815_test.go — #6815.
//
// hw307 ran for 12+ hours with 12 ECS instances in Huawei and 11 nodes joined:
// worker `…-me-east-215-a-w9b4787` was ACTIVE and never registered with k3s.
// Nothing reported it, because the census counts HelmReleases and a kit
// converges fine on fewer machines than were paid for. These tests pin the
// node-side counts and — more importantly — pin the cases where the census must
// stay SILENT rather than guess.

func regions() []RegionHealth {
	return []RegionHealth{
		{Region: "me-east-215-a", Primary: true, HRReady: 64, HRTotal: 68},
		{Region: "me-east-215-b", HRReady: 60, HRTotal: 68},
	}
}

func TestWithNodeCensus_DecoratesByRegion(t *testing.T) {
	out := WithNodeCensus(regions(), map[string]NodeCensus{
		"me-east-215-a": {Joined: 5, Requested: 6}, // the hw307 shortfall
		"me-east-215-b": {Joined: 6, Requested: 6},
	})
	if out[0].NodesJoined != 5 || out[0].NodesRequested != 6 {
		t.Fatalf("primary census not applied: %+v", out[0])
	}
	if !out[0].NodesShort() {
		t.Error("5 joined of 6 requested must report short — this is the hw307 case the row exists for")
	}
	if out[1].NodesShort() {
		t.Error("6 of 6 must not report short")
	}
}

func TestWithNodeCensus_UnmeasuredRegionUntouched(t *testing.T) {
	// Only the primary could be reached. The secondary must come back
	// unchanged — reporting 0 joined for a region we could not list would
	// read as "it lost every node", which is worse than saying nothing.
	out := WithNodeCensus(regions(), map[string]NodeCensus{"me-east-215-a": {Joined: 5, Requested: 6}})
	if out[1].NodesJoined != 0 || out[1].NodesRequested != 0 {
		t.Fatalf("unreachable region was decorated: %+v", out[1])
	}
	if out[1].NodesShort() {
		t.Error("an unmeasured region must never report short")
	}
}

func TestWithNodeCensus_EmptyCensusIsIdentity(t *testing.T) {
	in := regions()
	out := WithNodeCensus(in, nil)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(out))
	}
	for i := range out {
		if out[i] != in[i] {
			t.Errorf("region %d mutated with an empty census: %+v -> %+v", i, in[i], out[i])
		}
	}
}

func TestWithNodeCensus_DoesNotMutateInput(t *testing.T) {
	// The caller holds dep.liveRegionCensus; decorating must not write
	// through to it before the lock is taken.
	in := regions()
	_ = WithNodeCensus(in, map[string]NodeCensus{"me-east-215-a": {Joined: 5, Requested: 6}})
	if in[0].NodesJoined != 0 {
		t.Errorf("input slice was mutated: %+v", in[0])
	}
}

func TestNodesShort_SilentWhenUnmeasured(t *testing.T) {
	for _, c := range []struct {
		name string
		r    RegionHealth
		want bool
	}{
		{"both zero (never measured)", RegionHealth{}, false},
		{"joined known, requested unknown", RegionHealth{NodesJoined: 5}, false},
		{"requested known, joined unknown", RegionHealth{NodesRequested: 6}, false},
		{"short", RegionHealth{NodesJoined: 5, NodesRequested: 6}, true},
		{"exact", RegionHealth{NodesJoined: 6, NodesRequested: 6}, false},
		{"more than asked (scaled up)", RegionHealth{NodesJoined: 7, NodesRequested: 6}, false},
	} {
		if got := c.r.NodesShort(); got != c.want {
			t.Errorf("%s: NodesShort() = %v, want %v", c.name, got, c.want)
		}
	}
}
