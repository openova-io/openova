// application_controller_placement_fanout_test.go — #3969 §8c/§8d.
//
// Acceptance for the desired-state write-path: when an Application declares
// the canonical `spec.placement.targets[]` (the PlacementTarget model), the
// per-cluster HelmRelease fan-out RENDERS from those targets — one HR per
// declared target cluster, with the per-target Primary|Standby role mapped
// onto the active|passive|singleton HR role label — NOT from the legacy
// `Blueprint.spec.topology` / ResolveTopology posture string.
//
// What we assert:
//
//  1. A 2-target {mgmt-A Primary, mgmt-B Standby/Hot} placement produces
//     EXACTLY 2 HelmReleases, one per declared cluster, even though the
//     Blueprint declares NO spec.topology (the legacy resolver would have
//     produced zero fan-out HRs — the §8c gap).
//
//  2. The HR cluster labels are the targets' cluster IDs (mgmt-A / mgmt-B),
//     and the roles are active (Primary) + passive (Standby).
//
//  3. A single-target {mgmt-A Primary} placement produces 1 HR with the
//     singleton role (the lone-Primary fallback) — singleton→multi is a
//     pure edit of the target list.
//
//  4. Legacy string-mode (no targets, Blueprint.spec.topology present) is
//     unchanged — covered by application_controller_fanout_persist_test.go;
//     here we add the explicit negative: an Application with NO targets and
//     NO Blueprint topology produces no fan-out HRs (the per-region path
//     owns it), i.e. the targets path is purely additive.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeAppWithTargets builds an Application whose spec.placement is the
// #3969 OBJECT form carrying targets[]. `mode` seeds the legacy posture
// string (placement.Resolve input); it does not gate the fan-out.
func makeAppWithTargets(namespace, name, env, bpName, bpVer, mode string, regions []string, targets []map[string]interface{}) *unstructured.Unstructured {
	u := makeApp(namespace, name, env, bpName, bpVer, "single-region", regions, nil)
	spec := u.Object["spec"].(map[string]interface{})
	targetsAny := make([]interface{}, len(targets))
	for i, t := range targets {
		targetsAny[i] = t
	}
	spec["placement"] = map[string]interface{}{
		"mode":    mode,
		"targets": targetsAny,
	}
	return u
}

// makeBlueprintNoTopology builds a Blueprint with NO spec.topology and NO
// allowedPlacements/modes restriction — so the targets path is the SOLE
// driver of the fan-out (the legacy resolver is a no-op for this Blueprint).
func makeBlueprintNoTopology(name, version, capability string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	spec := map[string]interface{}{
		"version": version,
		"card":    map[string]interface{}{"title": name},
		"manifests": map[string]interface{}{
			"chart": name,
			"source": map[string]interface{}{
				"kind": "HelmRepository",
				"ref":  "openova-catalog",
			},
		},
	}
	if capability != "" {
		spec["placementCapability"] = capability
	}
	u.Object["spec"] = spec
	u.Object["status"] = map[string]interface{}{
		"ociDigest": "sha256:no-topology-fixture",
	}
	return u
}

// TestReconcile_TargetsDriveFanout_TwoClustersTwoHRs — the central §8c
// acceptance: a desired-state placement of 2 targets produces 2 per-cluster
// HRs WITHOUT any Blueprint.spec.topology declaration.
func TestReconcile_TargetsDriveFanout_TwoClustersTwoHRs(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithTargets("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		[]map[string]interface{}{
			{"region": "fsn", "cluster": "mgmt-A", "vcluster": "mgmt", "role": "Primary"},
			{"region": "nbg", "cluster": "mgmt-B", "vcluster": "mgmt", "role": "Standby", "standbyType": "Hot"},
		},
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	// The targets still carry vcluster=mgmt; the HRs land in the Application namespace (NET-223).
	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in acme: %v", err)
	}
	// The per-app Continuum companion (dr-obs) shares the namespace; keep
	// the per-cluster legs only.
	legs := hrList.Items[:0]
	for _, hr := range hrList.Items {
		if hr.GetLabels()["catalyst.openova.io/cluster"] != "" {
			legs = append(legs, hr)
		}
	}
	hrList.Items = legs
	if got := len(hrList.Items); got != 2 {
		names := make([]string, 0, got)
		for _, hr := range hrList.Items {
			names = append(names, hr.GetName())
		}
		t.Fatalf("want 2 per-cluster HRs driven by targets[], got %d: %v", got, names)
	}

	seenClusters := map[string]bool{}
	roleByCluster := map[string]string{}
	for _, hr := range hrList.Items {
		labels := hr.GetLabels()
		cluster := labels["catalyst.openova.io/cluster"]
		seenClusters[cluster] = true
		roleByCluster[cluster] = labels["catalyst.openova.io/role"]
	}
	if !seenClusters["mgmt-A"] || !seenClusters["mgmt-B"] {
		t.Fatalf("HR cluster labels must be the targets' cluster IDs mgmt-A + mgmt-B, got %v", seenClusters)
	}
	if roleByCluster["mgmt-A"] != "active" {
		t.Errorf("Primary target mgmt-A → role=active, got %q", roleByCluster["mgmt-A"])
	}
	if roleByCluster["mgmt-B"] != "passive" {
		t.Errorf("Standby target mgmt-B → role=passive, got %q", roleByCluster["mgmt-B"])
	}

	// ADR-0015 D1 — each region applies its own leg onto its host cluster:
	// no leg pivots, although both targets name the tier mgmt (NET-223).
	for _, hr := range hrList.Items {
		if kc, found, _ := unstructured.NestedMap(hr.Object, "spec", "kubeConfig"); found {
			t.Errorf("HR %s must carry no kubeConfig pivot; got %v", hr.GetName(), kc)
		}
	}
}

// TestReconcile_TargetsDriveFanout_SingletonSingleTarget — a lone Primary
// target renders a single HR with the singleton role. singleton→multi is a
// pure edit of the target list (add a Standby target).
func TestReconcile_TargetsDriveFanout_SingletonSingleTarget(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithTargets("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"singleton",
		[]string{"hetzner-fsn-rtz-prod"},
		[]map[string]interface{}{
			{"region": "fsn", "cluster": "mgmt-A", "vcluster": "mgmt", "role": "Primary"},
		},
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in acme: %v", err)
	}
	if got := len(hrList.Items); got != 1 {
		t.Fatalf("want 1 singleton HR, got %d", got)
	}
	if role := hrList.Items[0].GetLabels()["catalyst.openova.io/role"]; role != "singleton" {
		t.Errorf("lone Primary target → role=singleton, got %q", role)
	}
}

// TestReconcile_SingletonFanout_CarriesParameters_4589 — regression for #4589.
// The singleton/active topology fan-out path must pass the Application's
// per-Org spec.parameters THROUGH into the rendered HelmRelease spec.values
// (merged over the Blueprint manifests.values), exactly as the non-fanout
// per-region path does (mergeMaps(bpManifestsValues, spec.Parameters)). Before
// the fix the FanoutInputs literal omitted Values:, so the fan-out HR rendered
// with chart DEFAULTS only — per-Org httpRoute.hostnames + sovereignFqdn
// dropped and the bp-oidc-gate SSO companion failed closed on a fresh Org (the
// deeper cause under #4556 item-1; agnstar only worked off its stale HR).
func TestReconcile_SingletonFanout_CarriesParameters_4589(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-agenity", "0.5.16", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "agenity", "acme-prod", "bp-agenity", "0.5.16",
		"single-region", []string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{
			"sovereignFqdn": "t99.omani.works",
			"marker4589":    "present",
		})
	// Overlay the #3969 targets placement (singleton) — the fan-out driver.
	spec := app.Object["spec"].(map[string]interface{})
	spec["placement"] = map[string]interface{}{
		"mode": "singleton",
		"targets": []interface{}{
			map[string]interface{}{"region": "fsn", "cluster": "mgmt-A", "vcluster": "mgmt", "role": "Primary"},
		},
	}
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "agenity")

	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in acme: %v", err)
	}
	if got := len(hrList.Items); got != 1 {
		t.Fatalf("want 1 singleton fan-out HR, got %d", got)
	}
	vals, found, _ := unstructured.NestedMap(hrList.Items[0].Object, "spec", "values")
	if !found {
		t.Fatalf("#4589: fan-out HR has NO spec.values — per-Org parameters dropped entirely")
	}
	if vals["marker4589"] != "present" {
		t.Errorf("#4589: fan-out HR spec.values missing per-Org parameter marker4589; got %v", vals)
	}
	if vals["sovereignFqdn"] != "t99.omani.works" {
		t.Errorf("#4589: fan-out HR spec.values missing sovereignFqdn; got %v", vals)
	}
}

// TestReconcile_SingletonFanout_OneHR_NoPerRegionGitRepository — #4556 Item 1.
// A singleton target produces EXACTLY ONE HelmRelease total and NO per-region
// GitRepository/Kustomization: the fan-out `<app>-<cluster>` HR is the SOLE
// install, and the legacy per-region path (which would deliver a duplicate
// bare `<app>` HR via a GitRepository + Kustomization, double-consuming the
// Org plan quota — the live `agenity` + `agenity-rtz-a` fault) is suppressed.
func TestReconcile_SingletonFanout_OneHR_NoPerRegionGitRepository(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "primary+standby")
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithTargets("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"singleton",
		[]string{"hetzner-fsn-rtz-prod"},
		[]map[string]interface{}{
			{"region": "fsn", "cluster": "mgmt-A", "vcluster": "mgmt", "role": "Primary"},
		},
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	// EXACTLY ONE HR across every host namespace — no duplicate bare `<app>`
	// HR rendered by the (now-suppressed) per-region path.
	total := 0
	for _, ns := range []string{"mgmt", "dmz", "rtz", "acme"} {
		hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
			Namespace(ns).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list HRs in %s: %v", ns, err)
		}
		total += len(hrList.Items)
		for _, hr := range hrList.Items {
			// The surviving HR is the fan-out `<app>-<cluster>` name, NOT the
			// bare `<app>` the per-region path would have committed.
			if hr.GetName() == "obs" {
				t.Errorf("found the duplicate bare `<app>` HR %q in %s — the per-region path must be suppressed when the fan-out owns the install", hr.GetName(), ns)
			}
		}
	}
	if total != 1 {
		t.Fatalf("want EXACTLY 1 HR for a singleton fan-out (one render path), got %d", total)
	}

	// The per-region delivery infra (GitRepository catalyst-app-{org}-{app})
	// must NOT exist — bootstrapping it is what re-creates the duplicate HR
	// every reconcile.
	_, err := r.Dynamic.Resource(FluxGitRepositoryGVR).
		Namespace("flux-system").
		Get(context.Background(), "catalyst-app-acme-obs", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("per-region GitRepository catalyst-app-acme-obs must NOT be bootstrapped for a fan-out-owned install (it delivers the duplicate HR)")
	}
}

// TestReconcile_NoTargets_NoBlueprintTopology_NoFanout — the additive
// invariant: with NO desired-state targets AND NO Blueprint topology, the
// targets path is dormant and produces zero fan-out HRs (the legacy
// per-region render path owns the install). Proves §8c is purely additive.
func TestReconcile_NoTargets_NoBlueprintTopology_NoFanout(t *testing.T) {
	bp := makeBlueprintNoTopology("bp-grafana", "1.0.6", "")
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"single-region", []string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	// No fan-out `<app>-<cluster>` HR anywhere: the only HR is the bare
	// `<app>` the per-region path renders.
	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs: %v", err)
	}
	if len(hrList.Items) != 1 || hrList.Items[0].GetName() != "obs" {
		names := make([]string, 0, len(hrList.Items))
		for _, hr := range hrList.Items {
			names = append(names, hr.GetNamespace()+"/"+hr.GetName())
		}
		t.Errorf("targets path must be dormant: want only the bare HR obs, got %v", names)
	}
}
