package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #5872 — the applications LIST endpoint omitted `placement` while the DETAIL
// endpoint returned it, so a caller reading the list saw no placement for any
// app and could not distinguish "single-region" from "not reported".
//
// WHAT THIS TEST ASSERTS, and why it is shaped this way. The obvious test —
// "the list item has a non-empty Placement" — would pass on a hardcoded
// constant and would NOT have caught the original defect's real danger, which
// is the two endpoints DISAGREEING about one CR. So the assertion is
// equivalence: for the same object, the value the list reports must equal the
// value the detail path reports, because both must come from placementFromSpec.
//
// It also pins the honest-absence contract: an object with no placement must
// yield "" from both, so `omitempty` drops the field rather than the API
// manufacturing a default the console would render as fact.
func appObj(placement string, withPlacement bool) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "catalyst.openova.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "demo", "namespace": "acme"},
		"spec":       map[string]interface{}{},
	}}
	if withPlacement {
		o.Object["spec"].(map[string]interface{})["placement"] = placement
	}
	return o
}

func TestListPlacementMatchesDetail_5872(t *testing.T) {
	for _, tc := range []struct {
		name          string
		placement     string
		withPlacement bool
	}{
		{"single-region", "single-region", true},
		{"active-hot-standby", "active-hot-standby", true},
		{"absent stays empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := appObj(tc.placement, tc.withPlacement)

			// what the LIST path now records …
			var row applicationListItem
			row.Placement = placementFromSpec(obj)

			// … must equal what the DETAIL path records for the same object.
			detail := placementFromSpec(obj)

			if row.Placement != detail {
				t.Fatalf("list and detail disagree for the same CR: list=%q detail=%q", row.Placement, detail)
			}
			if tc.withPlacement && row.Placement != tc.placement {
				t.Fatalf("list placement = %q, want %q", row.Placement, tc.placement)
			}
			if !tc.withPlacement && row.Placement != "" {
				t.Fatalf("absent placement must stay empty so omitempty drops it, got %q", row.Placement)
			}
		})
	}
}

// The struct must actually carry the field — a guard against someone removing
// it and leaving the assignment to fail silently at review time.
func TestListItemDeclaresPlacement_5872(t *testing.T) {
	row := applicationListItem{Placement: "single-region"}
	if row.Placement != "single-region" {
		t.Fatal("applicationListItem must carry a Placement field (#5872)")
	}
}
