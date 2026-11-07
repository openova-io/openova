package controller

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func continuumFixture(scope string, spec map[string]interface{}) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1",
		"kind":       "Continuum",
		"metadata": map[string]interface{}{
			"name":      "dr-cnpg-pair",
			"namespace": "cnpg",
			"labels":    map[string]interface{}{"openova.io/scope": scope},
		},
		"spec": spec,
	}}
	return cr
}

// hw307 2026-11-07: a placement edit (B Primary, A Standby) on the cnpg-pair
// Application REPLACED the chart-owned Continuum spec — primaryRegion moved
// to B and spec.cnpgPair vanished. The controller then believed it lived in
// region B, reported B as the writer while A served writes, moved the lease,
// and ran no switchover. A placement edit must move ONLY the desired primary.
func TestDesiredStatePatch_PlatformCR_OnlyDesiredPrimaryMoves(t *testing.T) {
	cur := continuumFixture("platform", map[string]interface{}{
		"applicationRef":    "cnpg/cnpg-pair",
		"primaryRegion":     "hw-me-east-215-a-rtz-prod",
		"hotStandbyRegions": []interface{}{"hw-me-east-215-b-rtz-prod"},
		"cnpgPair":          map[string]interface{}{"name": "cnpg-pair-bp-cnpg-pair", "namespace": "cnpg"},
	})
	cp := continuumPlan{
		PrimaryRegion:  "hw-me-east-215-b-rtz-prod",
		StandbyRegions: []string{"hw-me-east-215-a-rtz-prod"},
		Mechanism:      "cnpg-pair",
		RTOSeconds:     60,
	}
	got := continuumDesiredStatePatch(cur, cp)
	want := map[string]interface{}{"spec": map[string]interface{}{
		"desiredPrimaryRegion": "hw-me-east-215-b-rtz-prod",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch = %#v, want %#v (topology + cnpgPair must stay chart-owned)", got, want)
	}
}

func TestDesiredStatePatch_NoOpWhenAlreadyDesired(t *testing.T) {
	cur := continuumFixture("platform", map[string]interface{}{
		"primaryRegion":        "a-x-y-z",
		"desiredPrimaryRegion": "b-x-y-z",
	})
	if p := continuumDesiredStatePatch(cur, continuumPlan{PrimaryRegion: "b-x-y-z", StandbyRegions: []string{"a-x-y-z"}}); p != nil {
		t.Fatalf("expected no patch, got %#v", p)
	}
}

// An application-scoped CR keeps its HOME topology across a primary flip:
// the home stays, membership is unchanged, only the desired primary moves.
func TestDesiredStatePatch_AppCR_FlipKeepsHomeTopology(t *testing.T) {
	cur := continuumFixture("application", map[string]interface{}{
		"primaryRegion":        "a-x-y-z",
		"hotStandbyRegions":    []interface{}{"b-x-y-z"},
		"desiredPrimaryRegion": "a-x-y-z",
		"switchover":           map[string]interface{}{"mechanism": "stateless"},
	})
	cp := continuumPlan{PrimaryRegion: "b-x-y-z", StandbyRegions: []string{"a-x-y-z"}, Mechanism: "stateless"}
	got := continuumDesiredStatePatch(cur, cp)
	want := map[string]interface{}{"spec": map[string]interface{}{"desiredPrimaryRegion": "b-x-y-z"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch = %#v, want %#v", got, want)
	}
}

// When the home region leaves the placement entirely, the app-scoped CR is
// re-homed and its standby membership recomputed.
func TestDesiredStatePatch_AppCR_RehomesWhenHomeLeaves(t *testing.T) {
	cur := continuumFixture("application", map[string]interface{}{
		"primaryRegion":        "a-x-y-z",
		"hotStandbyRegions":    []interface{}{"b-x-y-z"},
		"desiredPrimaryRegion": "a-x-y-z",
	})
	cp := continuumPlan{PrimaryRegion: "b-x-y-z", StandbyRegions: []string{"c-x-y-z"}}
	got := continuumDesiredStatePatch(cur, cp)
	want := map[string]interface{}{"spec": map[string]interface{}{
		"desiredPrimaryRegion": "b-x-y-z",
		"primaryRegion":        "b-x-y-z",
		"hotStandbyRegions":    []interface{}{"c-x-y-z"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch = %#v, want %#v", got, want)
	}
}
