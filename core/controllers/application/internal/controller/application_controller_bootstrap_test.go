// #3370 — bootstrap-owned ADOPTION tests.
//
// A bootstrap-owned Application (spec.bootstrap=true) is the canonical
// CR a bootstrap-kit chart self-registers for its slot-installed
// HelmRelease (shared-pg ← flux-system/bp-postgres-shared). The
// controller must ADOPT the existing install: status-only reconcile
// mirroring the owning HR's Ready condition, with ZERO HelmRelease
// writes and ZERO Gitea writes — rendering anything would duplicate the
// install the slot HR already owns. These tests are the DoD-2
// "adoption test cited" for the kubectl-get-hr before/after
// zero-duplicate-installs proof.

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// makeBootstrapOwnedApp mirrors what bp-postgres 0.1.6's
// templates/application-cr.yaml renders for slot 16a: a CR named after
// the instance, spec.bootstrap=true, the owning HR ref, NO
// environmentRef and NO regions (the CRD CEL rule waives them), and the
// Context declarations riding spec.parameters.
func makeBootstrapOwnedApp(namespace, name, hrName, hrNamespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetGeneration(1)
	u.SetLabels(map[string]string{
		"apps.openova.io/bootstrap-owned":  "true",
		"catalyst.openova.io/organization": "platform",
	})
	spec := map[string]interface{}{
		"bootstrap": true,
		"blueprintRef": map[string]interface{}{
			"name":    "bp-postgres",
			"version": "0.1.6",
		},
		"placement":       "single-region",
		"targetNamespace": namespace,
		"releaseName":     name,
		"parameters": map[string]interface{}{
			"databases": []interface{}{
				map[string]interface{}{
					"name":  "gitea",
					"owner": "gitea",
					"consumer": map[string]interface{}{
						"blueprint": "bp-gitea", "mode": "shared",
					},
					"reflect": map[string]interface{}{
						"secretName": "gitea-database-secret",
						"namespaces": []interface{}{"gitea"},
					},
				},
			},
		},
	}
	if hrName != "" {
		spec["helmRelease"] = map[string]interface{}{
			"name":      hrName,
			"namespace": hrNamespace,
		}
	}
	u.Object["spec"] = spec
	return u
}

// makeSlotHR mirrors a bootstrap-kit slot HelmRelease with a Ready
// condition (what Flux writes after install).
func makeSlotHR(namespace, name, readyStatus, message string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace(namespace)
	hr.SetName(name)
	hr.Object["spec"] = map[string]interface{}{
		"releaseName":     "shared-pg",
		"targetNamespace": "shared-data",
		"chart": map[string]interface{}{
			"spec": map[string]interface{}{
				"chart":   "bp-postgres",
				"version": "0.1.6",
			},
		},
	}
	if readyStatus != "" {
		hr.Object["status"] = map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":    "Ready",
					"status":  readyStatus,
					"reason":  "InstallSucceeded",
					"message": message,
				},
			},
		}
	}
	return hr
}

// countHelmReleases lists every HelmRelease across all namespaces in
// the fake cluster.
func countHelmReleases(t *testing.T, r *Reconciler) int {
	t.Helper()
	list, err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HelmReleases: %v", err)
	}
	return len(list.Items)
}

// TestReconcile_BootstrapOwnedAdoption_NoDuplicateInstall is the
// adoption safety gate: even with EVERY parent the normal render path
// would need fully present (Environment + Organization + Blueprint +
// regions + placement), spec.bootstrap=true must short-circuit to the
// status-only path — zero new HelmReleases, zero Gitea writes, no
// controller finalizer, phase mirroring the owning HR's Ready=True.
func TestReconcile_BootstrapOwnedAdoption_NoDuplicateInstall(t *testing.T) {
	bp := makeBlueprint("bp-postgres", "0.1.6", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True",
		"Helm install succeeded for release shared-data/shared-pg.v1 with chart bp-postgres@0.1.6")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	// Belt-and-braces: give the CR a resolvable environment + regions
	// so a guard regression would NOT bail on EnvironmentMissing but
	// actually render — making this test fail loudly on duplication.
	_ = unstructured.SetNestedField(app.Object, "acme-prod", "spec", "environmentRef")
	_ = unstructured.SetNestedStringSlice(app.Object, []string{"hetzner-fsn-rtz-prod"}, "spec", "regions")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	before := countHelmReleases(t, r)
	reconcileFromCluster(t, r, "shared-data", "shared-pg")
	after := countHelmReleases(t, r)

	if after != before {
		t.Fatalf("ADOPTION VIOLATION: HelmRelease count changed %d -> %d — the controller rendered a duplicate install for a bootstrap-owned instance", before, after)
	}
	if fg.puts != 0 {
		t.Errorf("expected 0 Gitea writes for a bootstrap-owned instance, got %d", fg.puts)
	}

	got := readApp(t, r, "shared-data", "shared-pg")
	if hasFinalizer(got, FinalizerName) {
		t.Errorf("bootstrap-owned CR must NOT carry the controller finalizer (its lifecycle is Helm's)")
	}
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Errorf("phase = %q, want %q (msg=%q)", phase, PhaseReady, message)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	lr, _, _ := unstructured.NestedString(got.Object, "status", "lastReconciledAt")
	if lr == "" {
		t.Errorf("status.lastReconciledAt empty — the adoption path must keep the freshness chip alive")
	}
}

// TestReconcile_BootstrapOwnedAdoption_DegradedOnHRFailure — the owning
// HR Ready=False mirrors as phase=Degraded with the HR message lifted
// into the condition.
func TestReconcile_BootstrapOwnedAdoption_DegradedOnHRFailure(t *testing.T) {
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "False",
		"Helm upgrade failed: timed out waiting for the condition")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want %q", phase, PhaseDegraded)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	if message == "" {
		t.Errorf("expected the HR failure message lifted into the condition")
	}
	if countHelmReleases(t, r) != 1 {
		t.Errorf("adoption path must not create/delete HelmReleases")
	}
}

// TestReconcile_BootstrapOwnedAdoption_PendingWhenHRMissing — a CR
// whose owning HR is gone (or not yet applied) surfaces Pending, no
// error, no writes.
func TestReconcile_BootstrapOwnedAdoption_PendingWhenHRMissing(t *testing.T) {
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	if fg.puts != 0 || countHelmReleases(t, r) != 0 {
		t.Errorf("missing-HR adoption path must not write anything (puts=%d, hrs=%d)", fg.puts, countHelmReleases(t, r))
	}
}

// TestReconcile_BootstrapOwnedAdoption_PendingWithoutHRRef — a CR with
// no spec.helmRelease at all stays Pending (honest unobserved state).
func TestReconcile_BootstrapOwnedAdoption_PendingWithoutHRRef(t *testing.T) {
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "", "")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
}

// TestReconcile_BootstrapOwnedDeletion_NoTeardown — deleting a
// bootstrap-owned CR drives NO Gitea cleanup and releases any stray
// finalizer; the install belongs to the slot HR, not to us.
func TestReconcile_BootstrapOwnedDeletion_NoTeardown(t *testing.T) {
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "ok")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	// Simulate a stray finalizer from a CR that pre-dates the marker.
	app.SetFinalizers([]string{FinalizerName})
	now := metav1.Now()
	app.SetDeletionTimestamp(&now)

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	if err := r.Reconcile(context.Background(), app); err != nil {
		t.Fatalf("reconcile deletion: %v", err)
	}

	if fg.deletes != 0 {
		t.Errorf("bootstrap-owned deletion drove %d Gitea deletes — must be 0", fg.deletes)
	}
	if countHelmReleases(t, r) != 1 {
		t.Errorf("bootstrap-owned deletion must not touch the slot HR")
	}
	got, err := r.Dynamic.Resource(ApplicationGVR).Namespace("shared-data").
		Get(context.Background(), "shared-pg", metav1.GetOptions{})
	if err == nil && hasFinalizer(got, FinalizerName) {
		t.Errorf("finalizer should be released on bootstrap-owned deletion")
	}
}

// makeBootstrapOwnedSpineApp mirrors the #4416 spine producer output: a
// bootstrap-owned (adopt) Application that is ALSO a DR-capable multi-region
// spine — spec.bootstrap=true + spec.helmRelease (adopt the healthy bootstrap
// HR, no duplicate render) + an explicit DR posture (active-hot-standby) +
// ≥2 regions (so the adoption path mints the per-app Continuum DR contract +
// writes the status.continuumRef back-ref).
func makeBootstrapOwnedSpineApp(namespace, name, bpName, bpVer, hrName string, regions []string) *unstructured.Unstructured {
	rgs := make([]interface{}, len(regions))
	for i, r := range regions {
		rgs[i] = r
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetGeneration(1)
	u.SetLabels(map[string]string{
		"catalyst.openova.io/spine":        "true",
		"catalyst.openova.io/organization": "platform",
	})
	u.Object["spec"] = map[string]interface{}{
		"bootstrap":       true,
		"environmentRef":  "omantel-biz-cp",
		"organizationRef": "omantel-biz",
		"placement":       "active-hot-standby",
		"regions":         rgs,
		"blueprintRef": map[string]interface{}{
			"name":    bpName,
			"version": bpVer,
		},
		"helmRelease": map[string]interface{}{
			"name":      hrName,
			"namespace": "flux-system",
		},
	}
	return u
}

// TestReconcile_BootstrapOwnedSpine_NoTopologyCarrier_NoContinuum — ADR-0015
// D8: a DR-posture bootstrap-owned app whose slot chart does NOT consume the
// topology contract (the spine bp-gitea slot declares no valuesFrom carrier)
// gets NO Continuum — one could never move it. The pre-D8 API-created
// `dr-<app>` is retired, and the adoption path still renders no duplicate
// install and writes nothing to Git.
func TestReconcile_BootstrapOwnedSpine_NoTopologyCarrier_NoContinuum(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-gitea", "1.2.24", "mgmt", []string{"mgmt-A", "mgmt-B"})
	env := makeMultiRegionEnv("omantel-biz-cp", "omantel-biz", "prod")
	org := makeOrg("omantel-biz")
	hr := makeSlotHR("flux-system", "bp-gitea", "True",
		"Helm upgrade succeeded for release gitea/gitea.v24 with chart bp-gitea@1.2.46")
	app := makeBootstrapOwnedSpineApp("catalyst", "spine-gitea", "bp-gitea", "1.2.24",
		"bp-gitea", []string{testRegionA, testRegionB})
	// The pre-D8 controller's API-created Continuum.
	legacy := makeContinuumCR("catalyst", ContinuumNameFor("spine-gitea"), "spine-gitea", testRegionA, 0)

	fg := newFakeGitea()
	fg.orgsExist["omantel-biz"] = true
	fg.orgsExist["openova"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr, legacy)
	r.Cfg.SovereignFQDN = "hw307.omani.works"

	before := countHelmReleases(t, r)
	reconcileFromCluster(t, r, "catalyst", "spine-gitea")
	if after := countHelmReleases(t, r); after != before {
		t.Fatalf("ADOPTION VIOLATION: HelmRelease count changed %d -> %d", before, after)
	}
	if fg.puts != 0 {
		t.Errorf("a slot without a topology carrier must not be Git-delivered, got %d Gitea writes", fg.puts)
	}
	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("catalyst").
		Get(context.Background(), "dr-spine-gitea", metav1.GetOptions{}); err == nil {
		t.Fatal("the API-created Continuum must be retired (and never re-created through the API)")
	}
	got := readApp(t, r, "catalyst", "spine-gitea")
	if ref, _, _ := unstructured.NestedString(got.Object, "status", "continuumRef"); ref != "" {
		t.Errorf("status.continuumRef = %q, want empty", ref)
	}
	if phase, _, _ := readPhaseAndReason(t, got); phase != PhaseReady {
		t.Errorf("phase = %q, want Ready (adopted HR is Ready=True)", phase)
	}
}

// makeCNPGPairPlatformApp mirrors what bp-cnpg-pair's application-cr.yaml
// renders for slot 16b on hw307 (spec.placement object with targets).
func makeCNPGPairPlatformApp(primary string) *unstructured.Unstructured {
	u := makeBootstrapOwnedSpineApp("cnpg", "cnpg-pair", "bp-cnpg-pair", "0.2.32", "bp-cnpg-pair",
		[]string{testRegionA, testRegionB})
	u.SetLabels(map[string]string{"apps.openova.io/bootstrap-owned": "true", "catalyst.openova.io/organization": "platform"})
	targets := []interface{}{}
	for _, rg := range []string{testRegionA, testRegionB} {
		t := map[string]interface{}{"region": rg, "cluster": rg, "role": "Standby", "standbyType": "Hot"}
		if rg == primary {
			t = map[string]interface{}{"region": rg, "cluster": rg, "role": "Primary"}
		}
		targets = append(targets, t)
	}
	_ = unstructured.SetNestedField(u.Object, map[string]interface{}{
		"mode": "active-hot-standby", "regions": []interface{}{testRegionA, testRegionB}, "targets": targets,
	}, "spec", "placement")
	_ = unstructured.SetNestedField(u.Object, "platform-bootstrap", "spec", "environmentRef")
	return u
}

// makeCNPGPairSlotHR is slot 16b: releaseName cnpg-pair into ns cnpg, reading
// the region file's carrier ConfigMap through valuesFrom (the opt-in).
func makeCNPGPairSlotHR() *unstructured.Unstructured {
	hr := makeSlotHR("flux-system", "bp-cnpg-pair", "True", "Helm upgrade succeeded")
	_ = unstructured.SetNestedField(hr.Object, "cnpg-pair", "spec", "releaseName")
	_ = unstructured.SetNestedField(hr.Object, "cnpg", "spec", "targetNamespace")
	_ = unstructured.SetNestedSlice(hr.Object, []interface{}{map[string]interface{}{
		"kind": "ConfigMap", "name": "bp-cnpg-pair-topology", "valuesKey": "values.yaml", "optional": true,
	}}, "spec", "valuesFrom")
	return hr
}

// makeLocalCNPGMember is this region's (A's) CNPG Cluster of the slot release.
func makeLocalCNPGMember(name string, replica map[string]interface{}) *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetAPIVersion("postgresql.cnpg.io/v1")
	c.SetKind("Cluster")
	c.SetNamespace("cnpg")
	c.SetName(name)
	c.SetLabels(map[string]string{"app.kubernetes.io/instance": "cnpg-pair"})
	spec := map[string]interface{}{"instances": int64(3)}
	if replica != nil {
		spec["replica"] = replica
	}
	c.Object["spec"] = spec
	return c
}

func platformFileObjs(t *testing.T, fg *fakeGitea, region, app string) []*unstructured.Unstructured {
	t.Helper()
	data, ok := fg.get("openova", "sovereign-regions", "main", regionfile.PlatformLayout("hw307.omani.works").AppPath(region, app))
	if !ok {
		return nil
	}
	objs, err := regionfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

// TestReconcile_PlatformPair_DeliveredFromGit_AdoptsLivePrimary — ADR-0015 D8
// for the platform's own database pair. On hw307 cnpg-pair's writer is region
// B (the founder moved it A→B). The adoption path must:
//   - write one file per region into the Sovereign region repo's platform
//     layout: the carrier ConfigMap the slot reads via valuesFrom (self = that
//     region, primary = the LIVE writer B, peers = the other region) + the
//     Continuum (repo/path/folder, the slot HelmRelease, stateful);
//   - create NO Continuum through the API and render no HelmRelease;
//   - on a later placement edit (Primary → A) move only the Continuum's
//     desired primary — the files' primary is Continuum's to commit.
func TestReconcile_PlatformPair_DeliveredFromGit_AdoptsLivePrimary(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-cnpg-pair", "0.2.32", "host", []string{"host-A", "host-B"})
	_ = unstructured.SetNestedField(bp.Object, map[string]interface{}{"backend": "cnpg-pair"},
		"spec", "topology", "perTopology", "active-hot-standby", "replication")
	// The live placement still says Primary=B; A's local member is a replica
	// of B in CNPG's distributed topology (the hw307 shape).
	app := makeCNPGPairPlatformApp(testRegionB)
	hr := makeCNPGPairSlotHR()
	localA := makeLocalCNPGMember("cnpg-pair-bp-cnpg-pair-primary", map[string]interface{}{
		"self": "cnpg-pair-bp-cnpg-pair-primary", "primary": "cnpg-pair-bp-cnpg-pair-replica", "source": "cnpg-pair-bp-cnpg-pair-replica",
	})
	fg := newFakeGitea()
	fg.orgsExist["openova"] = true
	r := newReconciler(t, fg, app, bp, hr, localA)
	r.Cfg.SovereignFQDN = "hw307.omani.works"

	before := countHelmReleases(t, r)
	reconcileFromCluster(t, r, "cnpg", "cnpg-pair")
	if after := countHelmReleases(t, r); after != before {
		t.Fatalf("ADOPTION VIOLATION: HelmRelease count changed %d -> %d", before, after)
	}
	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("cnpg").
		Get(context.Background(), "dr-cnpg-pair", metav1.GetOptions{}); err == nil {
		t.Fatal("the platform Continuum must be delivered from Git, never created through the API")
	}

	for _, region := range []string{testRegionA, testRegionB} {
		objs := platformFileObjs(t, fg, region, "cnpg-pair")
		if len(objs) != 2 {
			t.Fatalf("%s: want carrier ConfigMap + Continuum, got %d objects", region, len(objs))
		}
		cm, cr := objs[0], objs[1]
		if cm.GetKind() != "ConfigMap" || cm.GetName() != "bp-cnpg-pair-topology" || cm.GetNamespace() != "flux-system" {
			t.Fatalf("%s: carrier = %s %s/%s", region, cm.GetKind(), cm.GetNamespace(), cm.GetName())
		}
		if cm.GetLabels()[regionfile.TopologyValuesLabel] != "true" || cm.GetLabels()[fluxWatchLabel] != "Enabled" {
			t.Fatalf("%s: carrier labels %v", region, cm.GetLabels())
		}
		v, _, _ := regionfile.CarrierValues(cm)
		self, _, _ := unstructured.NestedString(v, "openova", "topology", "self")
		prim, _, _ := unstructured.NestedString(v, "openova", "topology", "primary")
		mode, _, _ := unstructured.NestedString(v, "openova", "topology", "mode")
		peers, _, _ := unstructured.NestedStringSlice(v, "openova", "topology", "peers")
		if self != region || prim != testRegionB || mode != "active-hot-standby" || len(peers) != 1 || peers[0] == region {
			t.Fatalf("%s: contract self=%s primary=%s mode=%s peers=%v (adoption must keep the LIVE writer B)", region, self, prim, mode, peers)
		}
		if cr.GetKind() != "Continuum" || cr.GetName() != "dr-cnpg-pair" || cr.GetNamespace() != "cnpg" {
			t.Fatalf("%s: second document = %s %s/%s", region, cr.GetKind(), cr.GetNamespace(), cr.GetName())
		}
		tr, _, _ := unstructured.NestedMap(cr.Object, "spec", "transition")
		repo, _ := tr["repo"].(map[string]interface{})
		hrRef, _ := tr["helmRelease"].(map[string]interface{})
		if repo["owner"] != "openova" || repo["name"] != "sovereign-regions" || repo["path"] != "clusters/hw307.omani.works" ||
			repo["folder"] != "platform" || tr["stateful"] != true || tr["app"] != "cnpg-pair" ||
			hrRef["name"] != "bp-cnpg-pair" || hrRef["namespace"] != "flux-system" {
			t.Fatalf("%s: Continuum transition = %v", region, tr)
		}
		if d, _, _ := unstructured.NestedString(cr.Object, "spec", "desiredPrimaryRegion"); d != testRegionB {
			t.Fatalf("%s: desiredPrimaryRegion = %q, want %s", region, d, testRegionB)
		}
	}
	got := readApp(t, r, "cnpg", "cnpg-pair")
	if ref, _, _ := unstructured.NestedString(got.Object, "status", "continuumRef"); ref != "cnpg/dr-cnpg-pair" {
		t.Fatalf("status.continuumRef = %q", ref)
	}

	// The operator sets Primary=A in the Topology tab. The files keep B
	// (Continuum commits the move, phase by phase); only the desired moves.
	cur := readApp(t, r, "cnpg", "cnpg-pair")
	moved := makeCNPGPairPlatformApp(testRegionA)
	cur.Object["spec"] = moved.Object["spec"]
	cur.SetGeneration(2)
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("cnpg").Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileFromCluster(t, r, "cnpg", "cnpg-pair")
	for _, region := range []string{testRegionA, testRegionB} {
		objs := platformFileObjs(t, fg, region, "cnpg-pair")
		f, _, _ := regionfile.ReadTransitionFields(objs)
		if f.Primary != testRegionB {
			t.Fatalf("%s: a placement edit must not rewrite the primary Continuum owns, got %q", region, f.Primary)
		}
		if d, _, _ := unstructured.NestedString(objs[1].Object, "spec", "desiredPrimaryRegion"); d != testRegionA {
			t.Fatalf("%s: desiredPrimaryRegion = %q, want %s", region, d, testRegionA)
		}
	}
}

// TestReconcile_PlatformPair_FirstFileTakesLocalPrimary — the adoption seed
// for a pair whose writer is THIS region (shared-pg on hw307: region A, legacy
// shape with no replica stanza) is this region even when the placement names
// no Primary target.
func TestReconcile_PlatformPair_FirstFileTakesLocalPrimary(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-cnpg-pair", "0.2.32", "host", []string{"host-A", "host-B"})
	_ = unstructured.SetNestedField(bp.Object, map[string]interface{}{"backend": "cnpg-pair"},
		"spec", "topology", "perTopology", "active-hot-standby", "replication")
	app := makeCNPGPairPlatformApp("")
	_ = unstructured.SetNestedField(app.Object, map[string]interface{}{"mode": "active-hot-standby"}, "spec", "placement")
	_ = unstructured.SetNestedStringSlice(app.Object, []string{"platform-bootstrap-owned-host"}, "spec", "regions")
	hr := makeCNPGPairSlotHR()
	fg := newFakeGitea()
	fg.orgsExist["openova"] = true
	r := newReconciler(t, fg, app, bp, hr, makeLocalCNPGMember("cnpg-pair-bp-cnpg-pair-primary", nil))
	r.Cfg.SovereignFQDN = "hw307.omani.works"
	reconcileFromCluster(t, r, "cnpg", "cnpg-pair")
	for _, region := range []string{testRegionA, testRegionB} {
		f, ok, _ := regionfile.ReadTransitionFields(platformFileObjs(t, fg, region, "cnpg-pair"))
		if !ok || f.Primary != testRegionA {
			t.Fatalf("%s: primary %q (ok=%v), want the live local writer %s", region, f.Primary, ok, testRegionA)
		}
	}

	// Live writer DISAGREES with the placement: placement says A, but A's
	// member is a replica (the writer is B). The first file seeds B — the live
	// writer — and desires A, so the move is Continuum's 3-commit transition,
	// never an unfenced flip at adoption.
	app2 := makeCNPGPairPlatformApp(testRegionA)
	fg2 := newFakeGitea()
	fg2.orgsExist["openova"] = true
	r2 := newReconciler(t, fg2, app2, bp, makeCNPGPairSlotHR(), makeLocalCNPGMember("cnpg-pair-bp-cnpg-pair-primary",
		map[string]interface{}{"enabled": true, "source": "cnpg-pair-bp-cnpg-pair-replica"}))
	r2.Cfg.SovereignFQDN = "hw307.omani.works"
	reconcileFromCluster(t, r2, "cnpg", "cnpg-pair")
	for _, region := range []string{testRegionA, testRegionB} {
		objs := platformFileObjs(t, fg2, region, "cnpg-pair")
		f, _, _ := regionfile.ReadTransitionFields(objs)
		d, _, _ := unstructured.NestedString(objs[1].Object, "spec", "desiredPrimaryRegion")
		if f.Primary != testRegionB || d != testRegionA {
			t.Fatalf("%s: seeded primary=%q desired=%q, want primary=%s (live) desired=%s (placement)", region, f.Primary, d, testRegionB, testRegionA)
		}
	}
}

// TestReconcile_PlatformPair_HostingGiteaDB_NeverDelivered — shared-pg holds
// Gitea's own database. A transition is a series of Gitea commits: once
// traffic-off demoted that pair, the data-primary commit could never be
// written. Even with a slot carrier declared, such a pair is never
// Git-delivered (no file, no Continuum).
func TestReconcile_PlatformPair_HostingGiteaDB_NeverDelivered(t *testing.T) {
	bp := makeBlueprintDRTopology("bp-postgres", "0.2.33", "host", []string{"host-A", "host-B"})
	_ = unstructured.SetNestedField(bp.Object, map[string]interface{}{"backend": "cnpg-pair"},
		"spec", "topology", "perTopology", "active-hot-standby", "replication")
	app := makeCNPGPairPlatformApp(testRegionA)
	app.SetNamespace("shared-data")
	app.SetName("shared-pg")
	_ = unstructured.SetNestedField(app.Object, map[string]interface{}{"name": "bp-postgres", "version": "0.2.33"}, "spec", "blueprintRef")
	_ = unstructured.SetNestedField(app.Object, map[string]interface{}{"name": "bp-postgres-shared", "namespace": "flux-system"}, "spec", "helmRelease")
	_ = unstructured.SetNestedSlice(app.Object, []interface{}{
		map[string]interface{}{"name": "gitea", "owner": "gitea", "consumer": map[string]interface{}{"blueprint": "bp-gitea", "mode": "shared"}},
	}, "spec", "parameters", "databases")
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "ok")
	_ = unstructured.SetNestedSlice(hr.Object, []interface{}{map[string]interface{}{
		"kind": "ConfigMap", "name": "bp-postgres-shared-topology", "valuesKey": "values.yaml", "optional": true,
	}}, "spec", "valuesFrom")
	fg := newFakeGitea()
	fg.orgsExist["openova"] = true
	r := newReconciler(t, fg, app, bp, hr)
	r.Cfg.SovereignFQDN = "hw307.omani.works"
	reconcileFromCluster(t, r, "shared-data", "shared-pg")
	if fg.puts != 0 {
		t.Fatalf("a pair hosting the Gitea database must never be Git-delivered, got %d Gitea writes", fg.puts)
	}
	if ref, _, _ := unstructured.NestedString(readApp(t, r, "shared-data", "shared-pg").Object, "status", "continuumRef"); ref != "" {
		t.Fatalf("status.continuumRef = %q, want empty", ref)
	}
}

// TestReconcile_BootstrapOwnedNonDRSpine_NoContinuum proves a SINGLETON
// bootstrap-owned app (shared-pg shape, placement=single-region) still mints
// NO Continuum + writes NO back-ref after the #4416 change — the shared-pg /
// substrate adoption path is unchanged (zero-extra-GET status-mirror).
func TestReconcile_BootstrapOwnedNonDRSpine_NoContinuum(t *testing.T) {
	bp := makeBlueprint("bp-postgres", "0.1.6", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "ok")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	_ = unstructured.SetNestedField(app.Object, "acme-prod", "spec", "environmentRef")
	_ = unstructured.SetNestedStringSlice(app.Object, []string{"hetzner-fsn-rtz-prod"}, "spec", "regions")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	if _, err := r.Dynamic.Resource(ContinuumGVR).Namespace("shared-data").
		Get(context.Background(), "dr-shared-pg", metav1.GetOptions{}); err == nil {
		t.Fatalf("a singleton bootstrap-owned app must NOT mint a Continuum CR")
	}
	got := readApp(t, r, "shared-data", "shared-pg")
	if ref, _, _ := unstructured.NestedString(got.Object, "status", "continuumRef"); ref != "" {
		t.Errorf("status.continuumRef = %q, want empty for a non-DR bootstrap app", ref)
	}
}
