package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #5422 — the app-detail endpoint must serialize `spec.placement` for BOTH
// shapes the CRD accepts.
//
// The legacy shape is a bare string. The #3373 shape is an object
// `{mode, vcluster, regions, clusters}` with the posture in `mode`. Reading
// only the string form makes a raw NestedString return ok=false against the
// object form, leaving the field empty so `omitempty` drops it — and the
// console converts that absence into a concrete wrong value
// (`?? 'singleton'`, AppDetail.tsx:255).
//
// Live proof on hw290: `uat-ahs-hw290` (object form) rendered Overview
// `singleton` while the Topology tab derived `active-hot-standby` from the
// same CR, with the Overview contradicting itself by listing two REGIONS
// directly beneath. Of 16 apps probed it was the only one whose API omitted
// placement — and the only one carrying object-form placement, which is why
// this read as app-specific flakiness rather than a shape bug.
//
// A test written against the string form alone passes on the broken code —
// the dual-form IS the defect — so the object case below is the load-bearing
// one.
func TestApplicationDetail_PlacementDualForm_5422(t *testing.T) {
	// Calls the SAME helper the detail endpoint uses. An earlier draft of this
	// test reimplemented the resolution inline, which would have passed against
	// the unfixed handler — a guard that cannot fail is worse than no guard.
	placementOf := func(spec map[string]interface{}) string {
		return placementFromSpec(&unstructured.Unstructured{
			Object: map[string]interface{}{"spec": spec},
		})
	}

	t.Run("object dual-form resolves via mode", func(t *testing.T) {
		got := placementOf(map[string]interface{}{
			"placement": map[string]interface{}{
				"mode":    "active-hot-standby",
				"regions": []interface{}{"me-east-215", "me-east-215-b-1"},
			},
		})
		if got != "active-hot-standby" {
			t.Errorf("object-form placement resolved to %q, want %q — the field will be dropped by omitempty "+
				"and the console will render a fabricated 'singleton' for a two-region app (#5422)", got, "active-hot-standby")
		}
	})

	t.Run("legacy string form still resolves", func(t *testing.T) {
		if got := placementOf(map[string]interface{}{"placement": "singleton"}); got != "singleton" {
			t.Errorf("string-form placement resolved to %q, want %q — the fix must not regress the legacy shape", got, "singleton")
		}
	})

	// Absence must stay absent. Defaulting here (e.g. by calling readTopology,
	// which returns "singleton" for an unset placement) would re-manufacture
	// exactly the value this issue is about — the console has to render unknown
	// rather than be handed a confident guess.
	t.Run("absent placement is NOT defaulted to a concrete value", func(t *testing.T) {
		if got := placementOf(map[string]interface{}{}); got != "" {
			t.Errorf("absent placement resolved to %q, want empty — inventing a value is the #5422 defect, "+
				"not the fix for it", got)
		}
	})

	t.Run("object form without mode is not defaulted", func(t *testing.T) {
		got := placementOf(map[string]interface{}{
			"placement": map[string]interface{}{"regions": []interface{}{"me-east-215"}},
		})
		if got != "" {
			t.Errorf("object placement lacking mode resolved to %q, want empty", got)
		}
	})
}
