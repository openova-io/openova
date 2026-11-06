package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hw307 2026-11-07: after the pair failed over to region B, a switchover to
// region A ran "A → B" again (from = spec.primaryRegion, to = first hot
// standby), so there was no way back. From is the OBSERVED writer; to is
// the region the operator asked for.
func TestSwitchback_FromIsObservedWriter_ToIsRequested(t *testing.T) {
	spec := ContinuumSpec{PrimaryRegion: "fsn", HotStandbyRegions: []string{"hel"}}
	cr := newTestContinuumCR("ns", "c", "fsn", []string{"hel"}, "in-memory")

	if got := actingPrimaryRegion(cr, spec); got != "fsn" {
		t.Fatalf("no observation: acting primary = %q want fsn", got)
	}
	_ = unstructured.SetNestedField(cr.Object, "hel", "status", "primaryRegion")
	from := actingPrimaryRegion(cr, spec)
	if from != "hel" {
		t.Fatalf("failed over to hel: acting primary = %q want hel", from)
	}
	if to := requestedTargetRegion(cr, spec, from); to != "fsn" {
		t.Fatalf("no explicit target: to = %q want the other member fsn", to)
	}
	_ = unstructured.SetNestedField(cr.Object, "fsn", "spec", "switchover", "targetRegion")
	if to := requestedTargetRegion(cr, spec, from); to != "fsn" {
		t.Fatalf("explicit target: to = %q want fsn", to)
	}
	_ = unstructured.SetNestedField(cr.Object, "hel", "spec", "switchover", "targetRegion")
	if to := requestedTargetRegion(cr, spec, from); to != from {
		t.Fatalf("target == acting primary must be reported as such (no-op), got %q", to)
	}
}
