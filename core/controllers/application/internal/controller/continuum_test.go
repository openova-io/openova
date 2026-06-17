// continuum_test.go — #3375 DoD-3 coverage for the per-app Continuum CR
// producer (continuum.go).
//
// Two layers:
//
//  1. Pure-unit: resolveSwitchoverMechanism + buildContinuumPlan +
//     renderContinuumCR — the gate (DR-capable vs not) and the
//     mechanism-resolution table (bp-continuum alias → stateless /
//     cnpg-pair / raft-transition), zero K8s.
//
//  2. Integration: drive a full Reconcile against a Blueprint whose
//     active-hot-standby variant declares a switchover mechanism and
//     assert the controller upserted a `dr-<app>` Continuum CR with the
//     correct spec — AND that a singleton Blueprint mints NO CR.

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func intPtr(i int) *int { return &i }

// ── Pure units ───────────────────────────────────────────────────────

func TestResolveSwitchoverMechanism(t *testing.T) {
	cases := []struct {
		name    string
		variant *bpv1alpha1.TopologyVariant
		want    string
	}{
		{
			name:    "nil variant → stateless",
			variant: nil,
			want:    "stateless",
		},
		{
			name:    "no switchover block → stateless",
			variant: &bpv1alpha1.TopologyVariant{},
			want:    "stateless",
		},
		{
			name: "explicit cnpg-pair passes through",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover: &bpv1alpha1.SwitchoverSpec{Mechanism: "cnpg-pair"},
			},
			want: "cnpg-pair",
		},
		{
			name: "explicit raft-transition passes through",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover: &bpv1alpha1.SwitchoverSpec{Mechanism: "raft-transition"},
			},
			want: "raft-transition",
		},
		{
			name: "explicit stateless passes through",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover: &bpv1alpha1.SwitchoverSpec{Mechanism: "stateless"},
			},
			want: "stateless",
		},
		{
			name: "bp-continuum alias + cnpg-pair backend → cnpg-pair",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover:  &bpv1alpha1.SwitchoverSpec{Mechanism: "bp-continuum"},
				Replication: &bpv1alpha1.ReplicationSpec{Backend: "cnpg-pair"},
			},
			want: "cnpg-pair",
		},
		{
			name: "bp-continuum alias + raft backend → raft-transition",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover:  &bpv1alpha1.SwitchoverSpec{Mechanism: "bp-continuum"},
				Replication: &bpv1alpha1.ReplicationSpec{Backend: "raft"},
			},
			want: "raft-transition",
		},
		{
			name: "bp-continuum alias + NO replication → stateless (the DNS-flip-only app)",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover: &bpv1alpha1.SwitchoverSpec{Mechanism: "bp-continuum"},
			},
			want: "stateless",
		},
		{
			name: "bp-continuum alias + unrecognised backend → stateless",
			variant: &bpv1alpha1.TopologyVariant{
				Switchover:  &bpv1alpha1.SwitchoverSpec{Mechanism: "bp-continuum"},
				Replication: &bpv1alpha1.ReplicationSpec{Backend: "mirrormaker2"},
			},
			want: "stateless",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSwitchoverMechanism(tc.variant); got != tc.want {
				t.Fatalf("resolveSwitchoverMechanism = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildContinuumPlan_Gate(t *testing.T) {
	twoRegionPlan := placement.Plan{
		Mode:          "active-hotstandby",
		PrimaryRegion: "hetzner-fsn-rtz-prod",
		Regions: []placement.RegionPlan{
			{Name: "hetzner-fsn-rtz-prod", Role: placement.RolePrimary},
			{Name: "hetzner-nbg-rtz-prod", Role: placement.RoleStandby, Standby: true},
		},
	}
	hotStandbyVariant := &bpv1alpha1.TopologyVariant{
		Switchover: &bpv1alpha1.SwitchoverSpec{Mechanism: "bp-continuum", RtoSeconds: intPtr(30), RpoSeconds: intPtr(0)},
	}

	t.Run("active-hot-standby with standby → produces", func(t *testing.T) {
		cp, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpActiveHotStandby, hotStandbyVariant, twoRegionPlan, nil)
		if !ok {
			t.Fatal("expected a DR contract, got skip")
		}
		if cp.PrimaryRegion != "hetzner-fsn-rtz-prod" {
			t.Errorf("primaryRegion = %q", cp.PrimaryRegion)
		}
		if len(cp.StandbyRegions) != 1 || cp.StandbyRegions[0] != "hetzner-nbg-rtz-prod" {
			t.Errorf("standbyRegions = %v, want [hetzner-nbg-rtz-prod]", cp.StandbyRegions)
		}
		if cp.Mechanism != "stateless" {
			t.Errorf("mechanism = %q, want stateless (bp-continuum alias, no replication)", cp.Mechanism)
		}
		if cp.RTOSeconds != 30 {
			t.Errorf("rtoSeconds = %d, want 30", cp.RTOSeconds)
		}
	})

	t.Run("active-passive with standby → produces", func(t *testing.T) {
		_, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpActivePassive, hotStandbyVariant, twoRegionPlan, nil)
		if !ok {
			t.Fatal("active-passive should produce a DR contract")
		}
	})

	t.Run("singleton → skip", func(t *testing.T) {
		if _, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpSingleton, hotStandbyVariant, twoRegionPlan, nil); ok {
			t.Error("singleton must NOT produce a DR contract")
		}
	})

	t.Run("active-active → skip", func(t *testing.T) {
		if _, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpActiveActive, hotStandbyVariant, twoRegionPlan, nil); ok {
			t.Error("active-active must NOT produce a DR contract (no primary/standby flip)")
		}
	})

	t.Run("no switchover mechanism → skip", func(t *testing.T) {
		v := &bpv1alpha1.TopologyVariant{Placement: &bpv1alpha1.PlacementSpec{}}
		if _, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpActiveHotStandby, v, twoRegionPlan, nil); ok {
			t.Error("a variant with no switchover mechanism must NOT produce a DR contract")
		}
	})

	t.Run("single-region (no standby) → skip", func(t *testing.T) {
		singleRegionPlan := placement.Plan{
			Mode:          "active-hotstandby",
			PrimaryRegion: "hetzner-fsn-rtz-prod",
			Regions: []placement.RegionPlan{
				{Name: "hetzner-fsn-rtz-prod", Role: placement.RolePrimary},
			},
		}
		if _, ok := buildContinuumPlan("acme/obs", bpv1alpha1.BcpActiveHotStandby, hotStandbyVariant, singleRegionPlan, nil); ok {
			t.Error("a hot-standby topology that collapsed to one region must NOT mint a CR with empty hotStandbyRegions[]")
		}
	})
}

func TestRenderContinuumCR_Shape(t *testing.T) {
	cp := continuumPlan{
		AppRef:         "acme/obs",
		PrimaryRegion:  "hetzner-fsn-rtz-prod",
		StandbyRegions: []string{"hetzner-nbg-rtz-prod"},
		Mechanism:      "stateless",
		RTOSeconds:     30,
		OwnerLabels:    map[string]string{"catalyst.openova.io/organization": "acme"},
	}
	cr := renderContinuumCR("obs", "acme", cp)

	if cr.GetName() != "dr-obs" {
		t.Errorf("name = %q, want dr-obs", cr.GetName())
	}
	if cr.GetAPIVersion() != "dr.openova.io/v1" || cr.GetKind() != "Continuum" {
		t.Errorf("GVK = %s/%s", cr.GetAPIVersion(), cr.GetKind())
	}
	appRef, _, _ := unstructured.NestedString(cr.Object, "spec", "applicationRef")
	if appRef != "acme/obs" {
		t.Errorf("applicationRef = %q", appRef)
	}
	primary, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if primary != "hetzner-fsn-rtz-prod" {
		t.Errorf("primaryRegion = %q", primary)
	}
	standby, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	if len(standby) != 1 || standby[0] != "hetzner-nbg-rtz-prod" {
		t.Errorf("hotStandbyRegions = %v", standby)
	}
	leaseKind, _, _ := unstructured.NestedString(cr.Object, "spec", "leaseClient", "kind")
	if leaseKind != DefaultContinuumLeaseKind {
		t.Errorf("leaseClient.kind = %q, want %q", leaseKind, DefaultContinuumLeaseKind)
	}
	mech, _, _ := unstructured.NestedString(cr.Object, "spec", "switchover", "mechanism")
	if mech != "stateless" {
		t.Errorf("switchover.mechanism = %q", mech)
	}
	rto, _, _ := unstructured.NestedString(cr.Object, "spec", "rto")
	if rto != "30s" {
		t.Errorf("rto = %q, want 30s", rto)
	}
	if cr.GetLabels()[topoLabelApp()] != "obs" {
		t.Errorf("app label = %q, want obs", cr.GetLabels()[topoLabelApp()])
	}
}

// topoLabelApp returns the canonical app-label key so the test reads the
// same constant the producer stamps without importing the render package
// just for the literal.
func topoLabelApp() string { return "catalyst.openova.io/app" }

// ── Integration ──────────────────────────────────────────────────────

// makeBlueprintDRTopology returns a Blueprint whose active-hot-standby
// variant declares BOTH a placement (2 clusters) AND a switchover
// mechanism — the shape that opts an app into the per-app Continuum DR
// contract (#3375 DoD-3). The `bp-continuum` alias + no replication block
// resolves to the generic `stateless` mechanism.
func makeBlueprintDRTopology(name, version, tier string, clusters []string) *unstructured.Unstructured {
	clustersAny := make([]interface{}, len(clusters))
	for i, c := range clusters {
		clustersAny[i] = c
	}
	roles := map[string]interface{}{}
	for i, c := range clusters {
		if i == 0 {
			roles[c] = "active"
		} else {
			roles[c] = "passive"
		}
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	u.Object["spec"] = map[string]interface{}{
		"version": version,
		"card":    map[string]interface{}{"title": name},
		"placementSchema": map[string]interface{}{
			"modes": []interface{}{"active-hotstandby", "single-region"},
		},
		"manifests": map[string]interface{}{
			"chart": name,
			"source": map[string]interface{}{
				"kind": "HelmRepository",
				"ref":  "openova-catalog",
			},
		},
		"topology": map[string]interface{}{
			"supported": []interface{}{"active-hot-standby", "singleton"},
			"defaults": map[string]interface{}{
				"multi-region":  "active-hot-standby",
				"single-region": "singleton",
			},
			"perTopology": map[string]interface{}{
				"active-hot-standby": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     tier,
						"clusters": clustersAny,
						"roles":    roles,
					},
					"switchover": map[string]interface{}{
						"mechanism":  "bp-continuum",
						"rtoSeconds": int64(30),
						"rpoSeconds": int64(0),
					},
				},
				"singleton": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     tier,
						"clusters": []interface{}{clusters[0]},
						"roles":    map[string]interface{}{clusters[0]: "singleton"},
					},
				},
			},
		},
	}
	u.Object["status"] = map[string]interface{}{"ociDigest": "sha256:dr-fixture"}
	return u
}

// TestReconcile_PerAppContinuumCR_Produced — the central #3375 DoD-3
// acceptance: a DR-capable Application produces a `dr-<app>` Continuum CR
// with the resolved primary/standby regions + the stateless mechanism.
func TestReconcile_PerAppContinuumCR_Produced(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-sso-bridge", "1.0.0", "mgmt", []string{"mgmt-A", "mgmt-B"})
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "sso-bridge", "acme-prod", "bp-sso-bridge", "1.0.0",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "sso-bridge")

	got := readApp(t, r, "acme", "sso-bridge")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	cr, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected per-app Continuum CR dr-sso-bridge in ns acme: %v", err)
	}

	appRef, _, _ := unstructured.NestedString(cr.Object, "spec", "applicationRef")
	if appRef != "acme/sso-bridge" {
		t.Errorf("applicationRef = %q, want acme/sso-bridge", appRef)
	}
	primary, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if primary != "hetzner-fsn-rtz-prod" {
		t.Errorf("primaryRegion = %q, want hetzner-fsn-rtz-prod", primary)
	}
	standby, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	if len(standby) != 1 || standby[0] != "hetzner-nbg-rtz-prod" {
		t.Errorf("hotStandbyRegions = %v, want [hetzner-nbg-rtz-prod]", standby)
	}
	mech, _, _ := unstructured.NestedString(cr.Object, "spec", "switchover", "mechanism")
	if mech != "stateless" {
		t.Errorf("switchover.mechanism = %q, want stateless", mech)
	}
	if cr.GetLabels()["catalyst.openova.io/app"] != "sso-bridge" {
		t.Errorf("app label = %q", cr.GetLabels()["catalyst.openova.io/app"])
	}
}

// TestReconcile_PerAppContinuumCR_Idempotent — re-reconcile must not
// mutate the produced CR (upsertHostResource byte-equal short-circuit).
func TestReconcile_PerAppContinuumCR_Idempotent(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-sso-bridge", "1.0.0", "mgmt", []string{"mgmt-A", "mgmt-B"})
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "sso-bridge", "acme-prod", "bp-sso-bridge", "1.0.0",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "sso-bridge")
	first, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	firstSpec, _, _ := unstructured.NestedMap(first.Object, "spec")

	reconcileFromCluster(t, r, "acme", "sso-bridge")
	second, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	secondSpec, _, _ := unstructured.NestedMap(second.Object, "spec")
	if !specsEqual(firstSpec, secondSpec) {
		t.Error("Continuum CR spec diverged on re-reconcile (should be byte-equal no-op)")
	}
}

// TestReconcile_SingletonApp_NoContinuumCR — a single-region / singleton
// app must NOT mint a Continuum CR (the buildContinuumPlan gate).
func TestReconcile_SingletonApp_NoContinuumCR(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-sso-bridge", "1.0.0", "mgmt", []string{"mgmt-A", "mgmt-B"})
	// Single-region environment → resolves to the singleton default.
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "sso-bridge", "acme-prod", "bp-sso-bridge", "1.0.0",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "sso-bridge")

	_, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{})
	if err == nil {
		t.Fatal("singleton app must NOT produce a Continuum CR, but dr-sso-bridge exists")
	}
}

// TestReconcile_TopologyDowngrade_RemovesContinuumCR — when an app that
// HAD a DR contract is downgraded to a non-DR topology, the stale CR is
// removed so the roster reflects reality.
func TestReconcile_TopologyDowngrade_RemovesContinuumCR(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-sso-bridge", "1.0.0", "mgmt", []string{"mgmt-A", "mgmt-B"})
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "sso-bridge", "acme-prod", "bp-sso-bridge", "1.0.0",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	// First reconcile mints the CR.
	reconcileFromCluster(t, r, "acme", "sso-bridge")
	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected CR after first reconcile: %v", err)
	}

	// Downgrade the Application's topology to single-region and re-reconcile.
	// A real single-region downgrade collapses BOTH the placement mode AND
	// the regions[] to one entry (placement.Resolve rejects single-region
	// with >1 region), so the fixture mirrors that.
	cur := readApp(t, r, "acme", "sso-bridge")
	_ = unstructured.SetNestedField(cur.Object, "single-region", "spec", "placement")
	_ = unstructured.SetNestedField(cur.Object, "singleton", "spec", "topology")
	_ = unstructured.SetNestedStringSlice(cur.Object, []string{"hetzner-fsn-rtz-prod"}, "spec", "regions")
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").
		Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update app to downgrade: %v", err)
	}
	reconcileFromCluster(t, r, "acme", "sso-bridge")

	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), "dr-sso-bridge", metav1.GetOptions{}); err == nil {
		t.Fatal("stale Continuum CR must be removed after topology downgrade to singleton")
	}
}
