package infrastructure

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// #5515 — derivePattern fails OPEN on a declared-but-dead secondary region.
//
// derivePattern keys off len(in.Regions), and in.Regions is []provisioner.RegionSpec
// — the DECLARED region specs from the wizard payload, not the live build-out. The
// live build-out is the local `regions` slice in buildTopology, which appends
// buildAbsentRegion(rs) (Clusters: nil, Status: "degraded") for any declared region
// whose kubeconfig never arrived (#4811 / #4814).
//
// Consequence: a 2-region prov where region-b NEVER CONVERGED still reports
// pattern="multi-region". The console then presents a multi-region topology for a
// deployment that has exactly one live region — the same declared-vs-actual shape as
// #5542 (HTTP 200 declaring 400), #5545 (61 "Deleted" that never happened) and the
// live janitor summary observed on the mothership 2026-08-01.
//
// This test PINS THE CURRENT (defective) BEHAVIOUR so the fail-open is executable
// evidence rather than a reading of the source. When #5515 is fixed, this test SHOULD
// fail — that is the signal to flip the expectation to "solo" (or whatever degraded
// value the fix elects) and delete this comment.
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

	got := derivePattern(in)

	if got != "multi-region" {
		t.Fatalf("guard drifted: expected the CURRENT defective behaviour %q, got %q — "+
			"if #5515 has been fixed, update this expectation deliberately", "multi-region", got)
	}

	t.Logf("#5515 CONFIRMED: derivePattern returned %q for a topology whose secondary "+
		"region has no live cluster — the pattern is derived from DECLARED specs, never "+
		"from liveness", got)
}

// Vacuity control. A guard that only ever asserts one input can pass because the
// function is a constant. These pin the other branches so the test above is known to
// be discriminating rather than trivially true.
func TestDerivePattern_5515_ControlBranchesAreDistinct(t *testing.T) {
	cases := []struct {
		name string
		in   LoaderInput
		want string
	}{
		{
			name: "single region, 3+ workers -> ha-pair",
			in:   LoaderInput{Regions: []provisioner.RegionSpec{{CloudRegion: "eu-west-101", WorkerCount: 3}}},
			want: "ha-pair",
		},
		{
			name: "single region, <3 workers -> solo",
			in:   LoaderInput{Regions: []provisioner.RegionSpec{{CloudRegion: "eu-west-101", WorkerCount: 1}}},
			want: "solo",
		},
		{
			name: "legacy singular Region field -> solo",
			in:   LoaderInput{Region: "eu-west-101"},
			want: "solo",
		},
		{
			name: "nothing declared -> unknown",
			in:   LoaderInput{},
			want: "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := derivePattern(tc.in); got != tc.want {
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

	a, b := derivePattern(bothLive), derivePattern(secondaryDead)
	if a != b {
		t.Fatalf("unexpected: derivePattern distinguished the two shapes (%q vs %q)", a, b)
	}
	t.Logf("#5515 root cause: both shapes yield %q — derivePattern cannot express "+
		"liveness because no live-state argument reaches it. A fix must thread the "+
		"built regions (or a live-region count) into the derivation, not reorder the "+
		"existing switch.", a)
}
