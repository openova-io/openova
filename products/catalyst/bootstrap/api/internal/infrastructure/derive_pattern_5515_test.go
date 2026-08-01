package infrastructure

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// #5515 — derivePattern must not report a topology richer than the live one.
//
// THE DEFECT (fixed by this change): derivePattern took only LoaderInput, whose
// Regions field is []provisioner.RegionSpec — the DECLARED wizard payload. Liveness
// lives elsewhere: buildTopology appends buildAbsentRegion(rs) (Clusters:nil,
// Status:"degraded") for any declared region whose kubeconfig never arrived
// (#4811/#4814). So a 2-region prov whose region-b never converged reported
// pattern="multi-region", and the console presented a DR topology that did not exist.
//
// The root cause was a MISSING INPUT, not a mis-ordered switch — no argument carried
// liveness, so no reordering of the existing cases could have fixed it. The fix moves
// the derivation below the build loop and threads the built regions in.
//
// Same declared-vs-actual family as #5542 (HTTP 200 declaring 400) and #5545 (61
// "Deleted" that never happened, confirmed live on the mothership 2026-08-01).
//
// These tests now assert the FIXED behaviour. If one fails, the fail-open has
// regressed — do not "repair" it by relaxing the expectation.
func TestDerivePattern_5515_FailsOpenOnAbsentSecondaryRegion(t *testing.T) {
	// Two DECLARED regions. region-b carries a full spec — the wizard declared it —
	// but at runtime it never converged, so buildTopology would emit
	// buildAbsentRegion() for it: zero clusters, status "degraded".
	in := LoaderInput{
		Status: "ready",
		Regions: []provisioner.RegionSpec{
			{CloudRegion: "eu-west-101", Provider: "huawei", WorkerCount: 3},
			{CloudRegion: "eu-west-102", Provider: "huawei", WorkerCount: 3}, // never converged
		},
	}

	// The BUILT regions: region-a converged (one live Cluster), region-b did not
	// (buildAbsentRegion shape — Clusters:nil, degraded).
	built := []Region{
		{ID: "region-eu-west-101", WorkerCount: 3, Clusters: []Cluster{{ID: "c-a"}}},
		{ID: "region-eu-west-102", WorkerCount: 3, Status: "degraded", Clusters: nil},
	}

	got := derivePattern(in, built)

	if got != "ha-pair" {
		t.Fatalf("#5515 regression: one live region (3 workers) + one declared-dead "+
			"region must NOT report multi-region; want %q, got %q", "ha-pair", got)
	}

	t.Logf("#5515 FIXED: derivePattern returned %q — the declared-but-dead secondary "+
		"no longer inflates the pattern to multi-region", got)
}

// Vacuity control. A guard that only ever asserts one input can pass because the
// function is a constant. These pin the other branches so the test above is known to
// be discriminating rather than trivially true.
func TestDerivePattern_5515_ControlBranchesAreDistinct(t *testing.T) {
	cases := []struct {
		name  string
		in    LoaderInput
		built []Region
		want  string
	}{
		{
			name:  "single region, 3+ workers -> ha-pair",
			in:    LoaderInput{Regions: []provisioner.RegionSpec{{CloudRegion: "eu-west-101", WorkerCount: 3}}},
			built: []Region{{WorkerCount: 3, Clusters: []Cluster{{ID: "c"}}}},
			want:  "ha-pair",
		},
		{
			name:  "single region, <3 workers -> solo",
			in:    LoaderInput{Regions: []provisioner.RegionSpec{{CloudRegion: "eu-west-101", WorkerCount: 1}}},
			built: []Region{{WorkerCount: 1, Clusters: []Cluster{{ID: "c"}}}},
			want:  "solo",
		},
		{
			name:  "legacy singular Region field -> solo",
			in:    LoaderInput{Region: "eu-west-101"},
			built: nil, // legacy singular path, nothing built yet -> declared fallback
			want:  "solo",
		},
		{
			name:  "nothing declared -> unknown",
			in:    LoaderInput{},
			built: nil,
			want:  "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := derivePattern(tc.in, tc.built); got != tc.want {
				t.Fatalf("derivePattern = %q, want %q", got, tc.want)
			}
		})
	}
}

// The discriminating check: liveness is INVISIBLE to derivePattern. Two inputs that
// differ ONLY in whether the second region ever converged are indistinguishable to it,
// because it never receives that information. This is the precise defect — not a
// mis-ordered case, but a missing input.
func TestDerivePattern_5515_LivenessIsNotAnInput(t *testing.T) {
	bothLive := LoaderInput{
		Regions: []provisioner.RegionSpec{
			{CloudRegion: "eu-west-101", WorkerCount: 3},
			{CloudRegion: "eu-west-102", WorkerCount: 3},
		},
	}
	// Byte-identical declared shape; in reality region-b is dead. derivePattern has
	// no parameter that could carry that fact.
	secondaryDead := LoaderInput{
		Regions: []provisioner.RegionSpec{
			{CloudRegion: "eu-west-101", WorkerCount: 3},
			{CloudRegion: "eu-west-102", WorkerCount: 3},
		},
	}

	bothBuilt := []Region{
		{WorkerCount: 3, Clusters: []Cluster{{ID: "c-a"}}},
		{WorkerCount: 3, Clusters: []Cluster{{ID: "c-b"}}},
	}
	deadBuilt := []Region{
		{WorkerCount: 3, Clusters: []Cluster{{ID: "c-a"}}},
		{WorkerCount: 3, Status: "degraded", Clusters: nil},
	}

	a, b := derivePattern(bothLive, bothBuilt), derivePattern(secondaryDead, deadBuilt)
	if a == b {
		t.Fatalf("#5515 regression: derivePattern still cannot distinguish a live "+
			"secondary from a dead one — both yielded %q", a)
	}
	if a != "multi-region" || b != "ha-pair" {
		t.Fatalf("want multi-region/ha-pair, got %q/%q", a, b)
	}
	t.Logf("#5515 fixed at the root: identical DECLARED shapes now yield %q vs %q "+
		"because liveness reaches the derivation via the built regions", a, b)
}

// In-flight guard (#5515 fix, regression-protection). While a fresh prov is still
// converging, NO region has clusters yet. Deriving purely from live state would
// report "unknown" for every provision in progress — fixing the degraded path by
// breaking the normal one. Nothing built => fall back to the DECLARED shape.
func TestDerivePattern_5515_InFlightFallsBackToDeclared(t *testing.T) {
	in := LoaderInput{
		Regions: []provisioner.RegionSpec{
			{CloudRegion: "eu-west-101", WorkerCount: 3},
			{CloudRegion: "eu-west-102", WorkerCount: 3},
		},
	}
	// Mid-provision: regions declared, none converged.
	if got := derivePattern(in, nil); got != "multi-region" {
		t.Fatalf("in-flight 2-region prov must report declared %q, got %q", "multi-region", got)
	}
	// Same, but the built slice exists with no clusters yet.
	empty := []Region{{Clusters: nil}, {Clusters: nil}}
	if got := derivePattern(in, empty); got != "multi-region" {
		t.Fatalf("in-flight (empty clusters) must report declared %q, got %q", "multi-region", got)
	}
}
