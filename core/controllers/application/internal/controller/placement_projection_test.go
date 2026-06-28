// placement_projection_test.go — #4601 (Refs #3986 #3375): coverage for the
// status.placement DR projection.
//
// Two layers:
//
//  1. Pure-unit: buildPlacementProjection + continuumDRStatusFromCR — the
//     primary/standby derivation, the live-Continuum override (leaseHolder
//     wins over the static plan primary), and the lag/leaseHeld surfacing.
//     Zero K8s.
//
//  2. Integration: drive a full reconcile of a bootstrap-owned (adopted)
//     active-hot-standby spine Application that already has a per-app
//     Continuum CR (leaseHolder=region-a, lag 0). Assert status.placement is
//     populated with primaryRegion=region-a + standbyRegions=[region-b] + the
//     mode. This is the regression gate for the live gap: before the
//     projection, the bootstrap-owned path wrote NO status.placement, so this
//     test would find an empty object and fail.

package controller

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// ── Pure units ───────────────────────────────────────────────────────

func TestBuildPlacementProjection(t *testing.T) {
	// active-hot-standby across region-a (primary) + region-b (standby).
	plan, err := placement.Resolve("active-hot-standby", []string{"region-a", "region-b"})
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}

	t.Run("static plan, no Continuum", func(t *testing.T) {
		got := buildPlacementProjection(plan, nil)
		if got["mode"] != placement.ModeActiveHotStandby {
			t.Errorf("mode = %v, want %s", got["mode"], placement.ModeActiveHotStandby)
		}
		if got["primaryRegion"] != "region-a" {
			t.Errorf("primaryRegion = %v, want region-a", got["primaryRegion"])
		}
		standbys, _ := got["standbyRegions"].([]interface{})
		if !reflect.DeepEqual(standbys, []interface{}{"region-b"}) {
			t.Errorf("standbyRegions = %v, want [region-b]", standbys)
		}
		// No Continuum ⇒ no lag / leaseHeld keys.
		if _, ok := got["replicationLagSeconds"]; ok {
			t.Errorf("replicationLagSeconds present with no Continuum: %v", got["replicationLagSeconds"])
		}
		if _, ok := got["leaseHeld"]; ok {
			t.Errorf("leaseHeld present with no Continuum: %v", got["leaseHeld"])
		}
	})

	t.Run("live Continuum: leaseHolder=region-a, lag 0, leaseHeld", func(t *testing.T) {
		cs := &continuumDRStatus{
			LeaseHolder:           "region-a",
			ReplicationLagSeconds: 0,
			HasLag:                true,
			LeaseHeld:             true,
			HasLeaseHeld:          true,
		}
		got := buildPlacementProjection(plan, cs)
		if got["primaryRegion"] != "region-a" {
			t.Errorf("primaryRegion = %v, want region-a", got["primaryRegion"])
		}
		standbys, _ := got["standbyRegions"].([]interface{})
		if !reflect.DeepEqual(standbys, []interface{}{"region-b"}) {
			t.Errorf("standbyRegions = %v, want [region-b]", standbys)
		}
		if got["replicationLagSeconds"] != int64(0) {
			t.Errorf("replicationLagSeconds = %v, want 0", got["replicationLagSeconds"])
		}
		if got["leaseHeld"] != true {
			t.Errorf("leaseHeld = %v, want true", got["leaseHeld"])
		}
	})

	t.Run("post-failover: leaseHolder=region-b overrides static primary", func(t *testing.T) {
		// The configured plan primary is region-a, but the lease has flipped
		// to region-b. The projection must follow the LIVE lease: primary
		// region-b, and the OLD primary region-a now listed as a standby.
		cs := &continuumDRStatus{
			LeaseHolder:           "region-b",
			ReplicationLagSeconds: 12,
			HasLag:                true,
			LeaseHeld:             true,
			HasLeaseHeld:          true,
		}
		got := buildPlacementProjection(plan, cs)
		if got["primaryRegion"] != "region-b" {
			t.Errorf("primaryRegion = %v, want region-b (failed-over lease)", got["primaryRegion"])
		}
		standbys, _ := got["standbyRegions"].([]interface{})
		if !reflect.DeepEqual(standbys, []interface{}{"region-a"}) {
			t.Errorf("standbyRegions = %v, want [region-a] (old primary demoted)", standbys)
		}
		if got["replicationLagSeconds"] != int64(12) {
			t.Errorf("replicationLagSeconds = %v, want 12", got["replicationLagSeconds"])
		}
	})

	t.Run("singleton: primary + empty standbys, no Continuum", func(t *testing.T) {
		sp, err := placement.Resolve("singleton", []string{"region-a"})
		if err != nil {
			t.Fatalf("resolve singleton: %v", err)
		}
		got := buildPlacementProjection(sp, nil)
		if got["mode"] != placement.ModeSingleton {
			t.Errorf("mode = %v, want singleton", got["mode"])
		}
		if got["primaryRegion"] != "region-a" {
			t.Errorf("primaryRegion = %v, want region-a", got["primaryRegion"])
		}
		standbys, _ := got["standbyRegions"].([]interface{})
		if len(standbys) != 0 {
			t.Errorf("standbyRegions = %v, want empty", standbys)
		}
	})

	t.Run("empty plan ⇒ nil projection", func(t *testing.T) {
		if got := buildPlacementProjection(placement.Plan{}, nil); got != nil {
			t.Errorf("expected nil projection for empty plan, got %v", got)
		}
	})
}

func TestContinuumDRStatusFromCR(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"leaseHolder":           "me-east-215-a",
			"replicationLagSeconds": int64(0),
			"conditions": []interface{}{
				map[string]interface{}{"type": "LeaseHeld", "status": "True"},
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}}
	cs := continuumDRStatusFromCR(cr)
	if cs == nil {
		t.Fatal("nil status from a populated Continuum CR")
	}
	if cs.LeaseHolder != "me-east-215-a" {
		t.Errorf("LeaseHolder = %q, want me-east-215-a", cs.LeaseHolder)
	}
	if !cs.HasLag || cs.ReplicationLagSeconds != 0 {
		t.Errorf("lag = %d (has=%v), want 0 (has=true)", cs.ReplicationLagSeconds, cs.HasLag)
	}
	if !cs.HasLeaseHeld || !cs.LeaseHeld {
		t.Errorf("leaseHeld = %v (has=%v), want true", cs.LeaseHeld, cs.HasLeaseHeld)
	}
}

func TestMergePlacementProjection(t *testing.T) {
	base := map[string]interface{}{"vcluster": "host", "source": "host-default"}
	proj := map[string]interface{}{"mode": "active-hot-standby", "primaryRegion": "region-a"}
	got := mergePlacementProjection(base, proj)
	// DR keys folded in additively; #3373 keys preserved.
	for k, want := range map[string]interface{}{
		"vcluster": "host", "source": "host-default",
		"mode": "active-hot-standby", "primaryRegion": "region-a",
	} {
		if got[k] != want {
			t.Errorf("merged[%q] = %v, want %v", k, got[k], want)
		}
	}
	// nil projection leaves the base untouched.
	if g := mergePlacementProjection(base, nil); !reflect.DeepEqual(g, base) {
		t.Errorf("nil projection mutated base: %v", g)
	}
	// nil base returns the projection alone.
	if g := mergePlacementProjection(nil, proj); !reflect.DeepEqual(g, proj) {
		t.Errorf("nil base lost projection: %v", g)
	}
}

func TestSplitNamespacedName(t *testing.T) {
	cases := []struct {
		in       string
		ns, name string
		ok       bool
	}{
		{"catalyst/dr-spine-gitea", "catalyst", "dr-spine-gitea", true},
		{"", "", "", false},
		{"noslash", "", "", false},
		{"/name", "", "", false},
		{"ns/", "", "", false},
		{"a/b/c", "", "", false},
	}
	for _, c := range cases {
		ns, name, ok := splitNamespacedName(c.in)
		if ns != c.ns || name != c.name || ok != c.ok {
			t.Errorf("split(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, ns, name, ok, c.ns, c.name, c.ok)
		}
	}
}

// ── Integration ──────────────────────────────────────────────────────

// makeContinuumCR builds a per-app Continuum CR with a populated status the
// projection reads (leaseHolder / lag / LeaseHeld), mirroring the live shape
// the continuum-controller stamps.
func makeContinuumCR(namespace, name, appName, leaseHolder string, lag int64) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion(ContinuumGroup + "/" + ContinuumVersion)
	cr.SetKind(ContinuumKind)
	cr.SetNamespace(namespace)
	cr.SetName(name)
	cr.SetLabels(map[string]string{"catalyst.openova.io/app": appName})
	cr.Object["spec"] = map[string]interface{}{
		"applicationRef":    namespace + "/" + appName,
		"primaryRegion":     leaseHolder,
		"hotStandbyRegions": []interface{}{"me-east-215-b"},
		"leaseClient":       map[string]interface{}{"kind": "k8s-lease"},
		"switchover":        map[string]interface{}{"mechanism": "cnpg-pair"},
	}
	cr.Object["status"] = map[string]interface{}{
		"leaseHolder":           leaseHolder,
		"primaryRegion":         leaseHolder,
		"replicationLagSeconds": lag,
		"phase":                 "Healthy",
		"conditions": []interface{}{
			map[string]interface{}{"type": "LeaseHeld", "status": "True"},
			map[string]interface{}{"type": "Ready", "status": "True"},
		},
	}
	return cr
}

// makeBootstrapDRSpineApp mirrors a bootstrap-owned spine Application
// (spec.bootstrap=true) with an active-hot-standby DR posture across two real
// regions — the shape of spine-gitea/harbor/keycloak on a 2-region Sovereign.
func makeBootstrapDRSpineApp(namespace, name, hrName, hrNamespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetGeneration(1)
	u.SetLabels(map[string]string{
		"apps.openova.io/bootstrap-owned":  "true",
		"catalyst.openova.io/organization": "omantel-biz",
	})
	u.Object["spec"] = map[string]interface{}{
		"bootstrap":      true,
		"environmentRef": "omantel-biz-cp",
		"blueprintRef": map[string]interface{}{
			"name":    "bp-gitea",
			"version": "1.2.24",
		},
		"placement":       "active-hot-standby",
		"regions":         []interface{}{"me-east-215-a", "me-east-215-b"},
		"targetNamespace": namespace,
		"releaseName":     name,
		"helmRelease": map[string]interface{}{
			"name":      hrName,
			"namespace": hrNamespace,
		},
	}
	return u
}

// TestReconcile_BootstrapOwned_PlacementProjectionFromContinuum is the
// regression gate for the #4601 live gap: a bootstrap-owned DR spine app with
// a live Continuum (leaseHolder=me-east-215-a) must project status.placement
// with primaryRegion=me-east-215-a + standbyRegions=[me-east-215-b] + the
// mode. Before the projection the bootstrap-owned path wrote NO
// status.placement, so the assertions below would find an empty object.
func TestReconcile_BootstrapOwned_PlacementProjectionFromContinuum(t *testing.T) {
	app := makeBootstrapDRSpineApp("catalyst", "spine-gitea", "bp-gitea-spine", "flux-system")
	hr := makeSlotHR("flux-system", "bp-gitea-spine", "True", "Helm install succeeded")
	// The producer mints/back-refs `dr-<app>`; seed it with a live lease.
	cont := makeContinuumCR("catalyst", ContinuumNameFor("spine-gitea"), "spine-gitea", "me-east-215-a", 0)

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr, cont)

	reconcileFromCluster(t, r, "catalyst", "spine-gitea")

	got := readApp(t, r, "catalyst", "spine-gitea")
	pl, found, _ := unstructured.NestedMap(got.Object, "status", "placement")
	if !found || pl == nil {
		t.Fatal("status.placement is empty — projection did not run (the #4601 gap)")
	}
	if pl["mode"] != placement.ModeActiveHotStandby {
		t.Errorf("status.placement.mode = %v, want %s", pl["mode"], placement.ModeActiveHotStandby)
	}
	if pl["primaryRegion"] != "me-east-215-a" {
		t.Errorf("status.placement.primaryRegion = %v, want me-east-215-a (live leaseHolder)", pl["primaryRegion"])
	}
	standbys, _, _ := unstructured.NestedSlice(got.Object, "status", "placement", "standbyRegions")
	if len(standbys) != 1 || standbys[0] != "me-east-215-b" {
		t.Errorf("status.placement.standbyRegions = %v, want [me-east-215-b]", standbys)
	}
	if pl["leaseHeld"] != true {
		t.Errorf("status.placement.leaseHeld = %v, want true", pl["leaseHeld"])
	}
	lag, lagFound, _ := unstructured.NestedInt64(got.Object, "status", "placement", "replicationLagSeconds")
	if !lagFound || lag != 0 {
		t.Errorf("status.placement.replicationLagSeconds = %d (found=%v), want 0", lag, lagFound)
	}
}

// TestReconcile_BootstrapOwned_SingletonNoContinuum_StaticProjection — a
// bootstrap-owned single-region app (no Continuum) still projects a static
// placement (primary + empty standbys) so the UI has a primary to show, but
// no lag/leaseHeld.
func TestReconcile_BootstrapOwned_SingletonNoContinuum_StaticProjection(t *testing.T) {
	app := makeBootstrapDRSpineApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	// Override to a singleton single-region posture (the shared-pg shape).
	spec, _, _ := unstructured.NestedMap(app.Object, "spec")
	spec["placement"] = "singleton"
	spec["regions"] = []interface{}{"me-east-215-a"}
	app.Object["spec"] = spec
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "Helm install succeeded")

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	pl, found, _ := unstructured.NestedMap(got.Object, "status", "placement")
	if !found || pl == nil {
		t.Fatal("status.placement empty for a singleton bootstrap app")
	}
	if pl["mode"] != placement.ModeSingleton {
		t.Errorf("mode = %v, want singleton", pl["mode"])
	}
	if pl["primaryRegion"] != "me-east-215-a" {
		t.Errorf("primaryRegion = %v, want me-east-215-a", pl["primaryRegion"])
	}
	if _, ok := pl["leaseHeld"]; ok {
		t.Errorf("leaseHeld present for a no-Continuum singleton: %v", pl["leaseHeld"])
	}
}
