// Tests for #5482 — the App detail Overview rendered a HOST CLUSTER LABEL
// where a region belongs, because the handler read a status key the
// Application CR does not have.
//
// Live on hw291, one Application reported three different values:
//
//	Overview PRIMARY REGION   platform-bootstrap-owned-host   <- a host label
//	Topology tab              hw-me-east-215-a-rtz-prod
//	the CR itself             me-east-215-a
//
// The read was `status.primaryRegion`; the CR carries
// `status.placement.primaryRegion`. NestedString returns ok=false rather than
// erroring on a missing path, so the miss was silent and AppDetail.tsx:256
// fell back to appRegions[0].
//
// Anti-theater: TestApplicationPrimaryRegion_ReadsNestedPlacementPath fails
// against the pre-fix code, which is the whole defect. The empty-status case
// matters just as much — returning "" is what lets the UI decide to render
// nothing, rather than being handed a value of the wrong KIND.

package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func appWithStatus(status map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openova.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "shared-pg", "namespace": "catalyst"},
		"status":     status,
	}}
}

func TestApplicationPrimaryRegion_ReadsNestedPlacementPath(t *testing.T) {
	t.Parallel()
	// The real hw291 shape: status keys are conditions, lastReconciledAt,
	// observedGeneration, phase, placement — and nothing else.
	obj := appWithStatus(map[string]interface{}{
		"phase":              "Ready",
		"observedGeneration": int64(3),
		"lastReconciledAt":   "2026-07-29T15:00:00Z",
		"placement": map[string]interface{}{
			"mode":           "active-hot-standby",
			"primaryRegion":  "me-east-215-a",
			"standbyRegions": []interface{}{"me-east-215-b"},
		},
	})
	if got := applicationPrimaryRegion(obj); got != "me-east-215-a" {
		t.Errorf("primaryRegion = %q want %q — the Overview tab renders this, and an "+
			"empty value makes the UI substitute a host-cluster label", got, "me-east-215-a")
	}
}

func TestApplicationPrimaryRegion_TopLevelPathStillHonoured(t *testing.T) {
	t.Parallel()
	obj := appWithStatus(map[string]interface{}{
		"phase":         "Ready",
		"primaryRegion": "me-east-215-a",
	})
	if got := applicationPrimaryRegion(obj); got != "me-east-215-a" {
		t.Errorf("a CR that DOES set the top-level key must still be honoured; got %q", got)
	}
}

// Nested wins when both are present and disagree — the placement block is the
// object the placement controller actually maintains.
func TestApplicationPrimaryRegion_NestedWinsOverStaleTopLevel(t *testing.T) {
	t.Parallel()
	obj := appWithStatus(map[string]interface{}{
		"primaryRegion": "me-east-215-b-STALE",
		"placement":     map[string]interface{}{"primaryRegion": "me-east-215-a"},
	})
	if got := applicationPrimaryRegion(obj); got != "me-east-215-a" {
		t.Errorf("primaryRegion = %q want the placement-block value me-east-215-a", got)
	}
}

func TestApplicationPrimaryRegion_AbsentIsEmptyNotFabricated(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		obj  *unstructured.Unstructured
	}{
		{"nil object", nil},
		{"no status at all", appWithStatus(map[string]interface{}{})},
		{"placement without a region", appWithStatus(map[string]interface{}{
			"placement": map[string]interface{}{"mode": "singleton"},
		})},
		{"empty nested value", appWithStatus(map[string]interface{}{
			"placement": map[string]interface{}{"primaryRegion": ""},
		})},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := applicationPrimaryRegion(tc.obj); got != "" {
				t.Errorf("absent region must resolve to empty, got %q — a non-empty value "+
					"here would be fabricated", got)
			}
		})
	}
}
