// topology_override_row60_test.go — UAT row 60 (Refs #3375).
//
// Clause: "Catalog → New instance → pick `active-hot-standby` → Provision →
// that app's Topology tab shows a 2-region pair (region-a primary + region-b
// replica + armed Switchover)."
//
// # What was actually broken
//
// The `armed Switchover` half. The per-app Continuum CR — the object that ARMS
// a hot standby for promotion — is minted by buildContinuumPlan, whose first
// gate is the resolved topology CHOICE. Reconcile resolved that choice by
// handing ResolveTopology `spec.Topology`, read by parseSpec as
// `unstructured.NestedString(app.Object, "spec", "topology")`. But the
// Application CRD types `spec.topology` as an OBJECT
// (products/catalyst/chart/crds/application.yaml:221 — autoFailover / rto /
// rpo / minReplicas), so that read could only ever return "": the operator-
// override branch of ResolveTopology was unreachable and every Application
// resolved the Blueprint's region-count DEFAULT instead of the posture the
// operator picked.
//
// Meanwhile placement.Resolve — the source of the per-region primary/standby
// roles the Topology tab paints — always read the operator's real posture
// (`spec.placement(.mode)`). So the two halves of the same clause were driven
// by two different fields, and a Blueprint that supports active-hot-standby but
// DEFAULTS to singleton produced exactly the reported symptom: a 2-region pair
// with nothing armed.
//
// # Why the pre-existing Continuum tests could not catch it
//
// TestReconcile_PerAppContinuumCR_Produced (continuum_test.go:308) uses a
// Blueprint whose `defaults.multi-region` IS `active-hot-standby` on a
// multi-region Environment. The operator's choice and the Blueprint default
// AGREE there, so the test passes identically whichever field is read — it
// cannot discriminate. Every fixture below makes them DISAGREE, which is the
// only way this seam is observable.
//
// The Blueprint shape used here is not contrived: bp-openclaw and
// bp-stalwart-tenant both ship `defaults.multi-region: singleton` in the
// catalog seed while supporting more than one posture.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeBlueprintSingletonDefaultDRCapable builds a Blueprint that SUPPORTS
// active-hot-standby (with a switchover mechanism, so the Continuum producer
// can arm it) but whose region-count DEFAULTS are singleton on both shapes.
//
// That gap is the whole point: `choice` comes out singleton if the resolver
// reads the Blueprint default, and active-hot-standby only if it reads what
// the operator chose.
func makeBlueprintSingletonDefaultDRCapable(name, version string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	u.Object["spec"] = map[string]interface{}{
		"version": version,
		"card":    map[string]interface{}{"title": name},
		"placementSchema": map[string]interface{}{
			// The mode gate Reconcile applies BEFORE topology resolution. It is
			// deliberately a different list from topology.supported below —
			// mirroring the real Blueprints, and the reason the fix falls back
			// rather than failing when the two disagree.
			"modes": []interface{}{"singleton", "active-hot-standby", "active-active"},
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
				// BOTH default to singleton — so a resolver reading the default
				// can never reach the DR variant, whatever the region count.
				"multi-region":  "singleton",
				"single-region": "singleton",
			},
			"perTopology": map[string]interface{}{
				"active-hot-standby": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     "",
						"clusters": []interface{}{"rtz-A", "rtz-B"},
						"roles": map[string]interface{}{
							"rtz-A": "active",
							"rtz-B": "passive",
						},
					},
					"switchover": map[string]interface{}{
						"mechanism":  "bp-continuum",
						"rtoSeconds": int64(60),
						"rpoSeconds": int64(0),
					},
				},
				"singleton": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     "",
						"clusters": []interface{}{"rtz-A"},
						"roles":    map[string]interface{}{"rtz-A": "singleton"},
					},
				},
			},
		},
	}
	u.Object["status"] = map[string]interface{}{"ociDigest": "sha256:row60-fixture"}
	return u
}

// makeAppObjectPlacement builds the Application EXACTLY as the "Catalog → +
// New instance" door writes it: `spec.placement` in its OBJECT form carrying
// the chosen `mode` + `regions`, and NO `spec.topology`.
//
// This is the shape newApplicationCRFromSeed produces
// (products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go:2335-2360)
// and it is what makes this a call-site test rather than a helper test: the
// fixture is the wire object the create door actually commits, not a
// hand-tuned appSpec.
func makeAppObjectPlacement(namespace, name, env, bpName, bpVer, mode string, regions []string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetGeneration(1)
	regionsAny := make([]interface{}, len(regions))
	for i, r := range regions {
		regionsAny[i] = r
	}
	u.Object["spec"] = map[string]interface{}{
		"environmentRef": env,
		"blueprintRef": map[string]interface{}{
			"name":    bpName,
			"version": bpVer,
		},
		"placement": map[string]interface{}{
			"mode":    mode,
			"regions": regionsAny,
		},
		"regions":    regionsAny,
		"parameters": map[string]interface{}{},
	}
	return u
}

const (
	row60RegionA = "hetzner-fsn-rtz-prod"
	row60RegionB = "hetzner-nbg-rtz-prod"
)

func row60ContinuumExists(t *testing.T, r *Reconciler, ns, app string) bool {
	t.Helper()
	_, err := r.Dynamic.Resource(ContinuumGVR).Namespace(ns).
		Get(context.Background(), ContinuumNameFor(app), metav1.GetOptions{})
	return err == nil
}

// TestRow60_OperatorHotStandbyArmsSwitchover_DespiteSingletonBlueprintDefault
// is the row's acceptance in source: the posture the operator chose at create
// time reaches the Continuum producer, so the Switchover is armed even though
// the Blueprint's own default for this Sovereign shape is singleton.
//
// Before the fix this failed — `choice` came back `singleton`, buildContinuumPlan
// returned (zero, false) at its first gate, and no `dr-<app>` CR was ever
// written while the HelmReleases still fanned out across two regions.
func TestRow60_OperatorHotStandbyArmsSwitchover_DespiteSingletonBlueprintDefault(t *testing.T) {
	bp := makeBlueprintSingletonDefaultDRCapable("bp-row60", "1.0.0")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppObjectPlacement("acme", "shop", "acme-prod", "bp-row60", "1.0.0",
		"active-hot-standby", []string{row60RegionA, row60RegionB})

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "shop")

	got := readApp(t, r, "acme", "shop")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	cr, err := r.Dynamic.Resource(ContinuumGVR).Namespace("acme").
		Get(context.Background(), ContinuumNameFor("shop"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no Continuum CR for an app the operator placed active-hot-standby "+
			"across %s + %s: the Switchover half of row 60 is unarmed (%v)",
			row60RegionA, row60RegionB, err)
	}

	// The armed contract must name the operator's OWN regions, in their order —
	// a CR that armed the Blueprint's cluster list instead would be a different
	// (and wrong) answer that still satisfies "a CR exists".
	primary, _, _ := unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if primary != row60RegionA {
		t.Errorf("primaryRegion = %q, want %q (regions[0] is the operator's primary)",
			primary, row60RegionA)
	}
	standbys, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	if len(standbys) != 1 || standbys[0] != row60RegionB {
		t.Errorf("hotStandbyRegions = %v, want [%s]", standbys, row60RegionB)
	}
	if mech, _, _ := unstructured.NestedString(cr.Object, "spec", "switchover", "mechanism"); mech == "" {
		t.Error("switchover.mechanism is empty — nothing to arm against")
	}
}

// TestRow60_SingletonChoiceStillArmsNothing is the CONTROL that shares the
// suspect property. Same Blueprint, same Environment, same two regions on the
// CR — only the operator's chosen mode differs. If the fix had simply started
// arming everything (or begun trusting spec.regions instead of the posture),
// this would mint a CR too and the test above would prove nothing.
func TestRow60_SingletonChoiceStillArmsNothing(t *testing.T) {
	bp := makeBlueprintSingletonDefaultDRCapable("bp-row60", "1.0.0")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppObjectPlacement("acme", "blog", "acme-prod", "bp-row60", "1.0.0",
		"singleton", []string{row60RegionA, row60RegionB})

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "blog")

	if row60ContinuumExists(t, r, "acme", "blog") {
		t.Fatal("a singleton Application minted a Continuum CR — the producer is now " +
			"arming on region count rather than on the operator's chosen posture")
	}
}

// TestRow60_UnsupportedPostureFallsBackAndDoesNotFail pins the deliberate
// non-regression in topologyOverrideFromPlacement.
//
// `active-active` passes the placementSchema.modes gate but is absent from this
// Blueprint's topology.supported — a Blueprint-authoring inconsistency that
// exists in the catalog today. Handing it to ResolveTopology verbatim would
// return ErrInvalidTopology and move the Application to Failed, which would be
// a NEW failure this row never asked for. The override therefore yields "" for
// an unsupported posture and the pre-existing region-count default stands.
func TestRow60_UnsupportedPostureFallsBackAndDoesNotFail(t *testing.T) {
	bp := makeBlueprintSingletonDefaultDRCapable("bp-row60", "1.0.0")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppObjectPlacement("acme", "mesh", "acme-prod", "bp-row60", "1.0.0",
		"active-active", []string{row60RegionA, row60RegionB})

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "mesh")

	got := readApp(t, r, "acme", "mesh")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("a posture the Blueprint's topology block omits must fall back to the "+
			"Blueprint default, not fail the Application: reason=%q msg=%q", reason, msg)
	}
	// active-active is multi-master — no primary/standby flip to drive — so the
	// producer must stay silent regardless of which branch resolved the variant.
	if row60ContinuumExists(t, r, "acme", "mesh") {
		t.Error("active-active minted a Continuum CR; there is no switchover to arm")
	}
}

// TestRow60_LegacyPostureSpellingStillArms — the operator's posture is compared
// CANONICALLY on both sides, so a CR carrying the legacy `active-hotstandby`
// spelling resolves the Blueprint's canonical `active-hot-standby` variant.
// Without canonicalisation this fix would have silently re-created #6200's
// spelling defect one layer down.
func TestRow60_LegacyPostureSpellingStillArms(t *testing.T) {
	bp := makeBlueprintSingletonDefaultDRCapable("bp-row60", "1.0.0")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppObjectPlacement("acme", "legacy", "acme-prod", "bp-row60", "1.0.0",
		"active-hotstandby", []string{row60RegionA, row60RegionB})

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "legacy")

	if !row60ContinuumExists(t, r, "acme", "legacy") {
		t.Fatal("the legacy `active-hotstandby` spelling did not arm the Switchover — " +
			"the override is comparing raw strings instead of canonical postures")
	}
}

// TestRow60_SpecTopologyObjectIsNotAnOverride locks the CRD-shape half so the
// removed read cannot return as a "fix".
//
// `spec.topology` is an OBJECT (autoFailover / rto / rpo / minReplicas). An
// Application may legitimately carry it, and it must have NO influence on which
// posture is resolved. If someone re-adds a string read of this field it stays
// "" (as it always did) — but if someone "improves" it into a map read that
// treats the block as a posture selector, this fails.
func TestRow60_SpecTopologyObjectIsNotAnOverride(t *testing.T) {
	bp := makeBlueprintSingletonDefaultDRCapable("bp-row60", "1.0.0")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppObjectPlacement("acme", "knobs", "acme-prod", "bp-row60", "1.0.0",
		"active-hot-standby", []string{row60RegionA, row60RegionB})
	// The CRD-legal object form, carrying a DR knob block.
	_ = unstructured.SetNestedMap(app.Object, map[string]interface{}{
		"autoFailover": true,
		"rto":          "60s",
		"rpo":          "5s",
	}, "spec", "topology")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "knobs")

	got := readApp(t, r, "acme", "knobs")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("a CRD-legal spec.topology knob block must not fail the reconcile: "+
			"reason=%q msg=%q", reason, msg)
	}
	if !row60ContinuumExists(t, r, "acme", "knobs") {
		t.Fatal("the posture must still come from spec.placement.mode when spec.topology " +
			"carries its DR knobs")
	}
}

// TestRow60_TopologyOverrideFromPlacement_Table pins the predicate itself.
// The reconcile tests above are the load-bearing ones — this only documents the
// mapping and guards the Blueprint's-own-spelling return, which is what lets
// lookupVariant find a legacy-spelled perTopology entry.
func TestRow60_TopologyOverrideFromPlacement_Table(t *testing.T) {
	bpTopo := parseBlueprintTopology(makeBlueprintSingletonDefaultDRCapable("bp-x", "1.0.0"))
	if bpTopo == nil {
		t.Fatal("fixture Blueprint parsed to a nil topology — every case below would be vacuous")
	}

	cases := []struct {
		name string
		mode string
		want string
	}{
		{"canonical supported posture wins", "active-hot-standby", "active-hot-standby"},
		{"legacy spelling folds onto the supported one", "active-hotstandby", "active-hot-standby"},
		{"singleton is supported and wins", "singleton", "singleton"},
		{"legacy single-region folds onto singleton", "single-region", "singleton"},
		{"unsupported posture yields the default fallback", "active-active", ""},
		{"empty posture yields the default fallback", "", ""},
		{"unknown token yields the default fallback", "not-a-posture", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := topologyOverrideFromPlacement(tc.mode, bpTopo); got != tc.want {
				t.Errorf("topologyOverrideFromPlacement(%q) = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}

	// A nil Blueprint topology can never yield an override — the caller's
	// `bpTopo != nil` guard and this must agree.
	if got := topologyOverrideFromPlacement("active-hot-standby", nil); got != "" {
		t.Errorf("nil Blueprint topology returned %q, want \"\"", got)
	}
}
