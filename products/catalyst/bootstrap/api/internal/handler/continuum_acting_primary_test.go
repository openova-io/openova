package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hw307 2026-11-07: after a switch to region B the preview said "region A
// already primary" (it read status.currentPrimary, which nothing writes,
// then spec.primaryRegion). The acting primary is the observed writer.
func TestContinuumActingPrimary_PrefersObservedWriter(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{}}
	_ = unstructured.SetNestedField(cr.Object, "region-a", "spec", "primaryRegion")
	if got := continuumActingPrimary(cr); got != "region-a" {
		t.Fatalf("no observation: got %q want region-a", got)
	}
	_ = unstructured.SetNestedField(cr.Object, "region-b", "status", "primaryRegion")
	if got := continuumActingPrimary(cr); got != "region-b" {
		t.Fatalf("failed over: got %q want region-b", got)
	}
	if got := continuumItemFromCR("s", cr).CurrentPrimary; got != "region-b" {
		t.Fatalf("fleet item currentPrimary = %q want region-b", got)
	}
}
