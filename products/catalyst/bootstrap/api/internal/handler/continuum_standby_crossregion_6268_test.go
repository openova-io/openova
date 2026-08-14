// continuum_standby_crossregion_6268_test.go — #6268 / UAT row 60.
//
// THE DEFECT. `augmentReplicationStandbyStatus` resolved the standby half of a
// cnpg cluster-pair ONLY through the region-A-scoped `sovereignDynamicClient`.
// In the 2-region active-hot-standby shape the replica half is a cnpg `Cluster`
// on the REGION-B apiserver, so neither region-A path could see it, and a
// Continuum that ALSO carries no `status.standbyAvailable` probe (measured on
// hw296: `walkfour/dr-r60fresh`) hit no fallback at all — leaving
// `replicaPromotable` at its zero value false and the Topology tab's Switchover
// control permanently DISABLED against a healthy pair.
//
// VACUITY DISCIPLINE. The happy-path assertions are ordered SUBSTANCE FIRST
// (`replicaPromotable`, then the tri-state verdict, then the gate label, then
// the provenance string) so a short-circuit on a cheap label check can never
// hide the reading that actually matters. Every control below mutates exactly
// ONE behaviour off the SAME fixture the happy path uses, and each mutation is
// aimed at a branch this code demonstrably takes:
//
//	region-B cluster removed          → no determination  (the fix's own input)
//	replica Ready=False               → explicit absent    (#4901 region-kill)
//	replica promoted (replica off)    → available, NOT promotable
//	replica lag 120s                  → available, NOT promotable
//	replica in another namespace      → no determination  (Org isolation)
//	region-B owned by another dep     → no determination  (Sovereign isolation)
//	spec.cnpgPair names a missing pair→ no determination  (no substitution)
//	controller probe says false       → Fail, disarmed    (disagreement rule)
//	no pair in cache, probe says true → the RELAY message  (relay not swallowed)
package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// crossRegionCNPGCache wires a started k8scache.Factory with one registered
// cluster per entry of `perCluster`, each serving the `cnpgcluster` kind from
// its own fake dynamic client. This is the multi-cluster shape the real
// Sovereign runs (region A + every secondary posted to
// /api/v1/sovereign/secondary-kubeconfig).
func crossRegionCNPGCache(t *testing.T, perCluster map[string][]*unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	scheme := runtime.NewScheme()
	gvk := schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	listGVK := schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ClusterList"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	gvrToListKind := map[schema.GroupVersionResource]string{cnpgClusterGVR: "ClusterList"}

	reg := k8scache.NewRegistry()
	_ = reg.Add(k8scache.Kind{Name: "cnpgcluster", GVR: cnpgClusterGVR, Namespaced: true})

	refs := make([]k8scache.ClusterRef, 0, len(perCluster))
	ids := make([]string, 0, len(perCluster))
	for cid, objs := range perCluster {
		rtObjs := make([]runtime.Object, 0, len(objs))
		for _, o := range objs {
			rtObjs = append(rtObjs, o)
		}
		refs = append(refs, k8scache.ClusterRef{
			ID:            cid,
			DynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, rtObjs...),
			CoreClient:    kfake.NewSimpleClientset(),
		})
		ids = append(ids, cid)
	}
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: reg,
		Clusters: refs,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	// Wait for every seeded object to land in its indexer — a race here would
	// make an assertion pass or fail for reasons unrelated to the fix.
	deadline := time.Now().Add(3 * time.Second)
	for {
		synced := true
		for _, cid := range ids {
			got, _, lerr := f.List(cid, "cnpgcluster", labels.Everything())
			if lerr != nil || len(got) < len(perCluster[cid]) {
				synced = false
				break
			}
		}
		if synced || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}

// row60Continuum is the exact live hw296 shape: an Org-scoped Continuum for a
// Catalog-provisioned active-hot-standby app that carries NEITHER
// spec.cnpgPair NOR status.standbyAvailable, so both pre-existing fallbacks are
// unreachable for it.
func row60Continuum() *unstructured.Unstructured {
	return newContinuumUnstructured(
		"dr-r60fresh", "walkfour", "walkfour/r60fresh",
		"me-east-215-a", []string{"me-east-215-b"})
}

// row60Halves returns the primary (region A) + replica (region B) cnpg Cluster
// CRs of the pair `postgres` in namespace `walkfour`, matching the labels the
// bp-postgres chart stamps on both halves.
func row60Halves() (primary, replica *unstructured.Unstructured) {
	return newCNPGPairFixture("postgres", "walkfour",
		"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod")
}

// setCNPGReady flips the replica half's Ready condition — the #4901
// region-kill state.
func setCNPGReady(u *unstructured.Unstructured, ready string) {
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": ready},
	}, "status", "conditions")
}

// ── THE FIX ──────────────────────────────────────────────────────────────

// A Continuum with no cnpgPair ref and no controller probe, whose pair's two
// halves live in DIFFERENT registered clusters, must resolve the standby off
// the replica half itself and ARM the switchover.
func TestReplicationStatus_Row60_CrossRegionPairArmsSwitchover(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	// The region-A dynamic client sees ONLY the primary half — the live split.
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-crossregion")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")

	// SUBSTANCE FIRST — this is the value UAT row 60's fourth clause turns on.
	if !resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got false want true — the pair's replica half is Ready and following in region B, so the Switchover control must arm; body=%+v", resp)
	}
	if resp.StandbyAvailable == nil || !*resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want an explicit true", resp.StandbyAvailable)
	}
	g := gateByName(t, resp, "standby-available")
	if g.Status != "Pass" {
		t.Fatalf("standby-available gate: got %q (%q) want Pass", g.Status, g.Message)
	}
	// PROVENANCE — the Pass must come from reading the replica half, not from
	// relaying the controller probe (which this CR does not even carry). The
	// region-B cluster id can only appear via the cross-region path.
	if !strings.Contains(g.Message, dep.ID+"-me-east-215-b-1") {
		t.Fatalf("standby-available message %q does not name the region-B cluster the verdict was read from — a Pass with no provenance is indistinguishable from the relay", g.Message)
	}
}

// ── CONTROLS — one mutated behaviour each ────────────────────────────────

// Remove the region-B cluster and NOTHING else. The verdict must collapse to
// honest-unknown. This is the control that makes the test above non-vacuous:
// if the fix were removed, the happy path would land here.
func TestReplicationStatus_Row60_NoRegionBClusterStaysUnknown(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, _ := row60Halves()
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-no-region-b")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID: {primary},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true with no observable standby half — that is a switchover armed against nothing")
	}
	if resp.StandbyAvailable != nil {
		t.Fatalf("standbyAvailable: got %v want omitted (a lone primary is not evidence of an absent standby, and not evidence of a present one either)", *resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Warn" {
		t.Fatalf("standby-available gate: got %q (%q) want Warn", g.Status, g.Message)
	}
}

// The region-kill shape: the replica half is OBSERVABLE but not Ready. That is
// positive evidence of an ABSENT standby — explicit false, Fail, disarmed.
func TestReplicationStatus_Row60_CrossRegionAbsentStandbyIsExplicit(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	setCNPGReady(replica, "False")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-absent")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.StandbyAvailable == nil || *resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want an explicit false — a lost standby that reports unknown leaves the red banner unreachable", resp.StandbyAvailable)
	}
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true against a not-Ready replica half")
	}
	if resp.StreamingState != "interrupted" {
		t.Fatalf("streamingState: got %q want interrupted", resp.StreamingState)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Fail" {
		t.Fatalf("standby-available gate: got %q want Fail", g.Status)
	}
}

// A PROMOTED standby (Ready, but spec.replica.enabled=false) is AVAILABLE and
// must NOT be promotable — it is no longer following the primary, so promoting
// it again is meaningless. This proves the `Following` term is load-bearing:
// delete it and this test flips to promotable while every other test stays green.
func TestReplicationStatus_Row60_PromotedStandbyIsAvailableButNotPromotable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	_ = unstructured.SetNestedField(replica.Object, false, "spec", "replica", "enabled")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-promoted")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true for a half that is no longer a follower (spec.replica.enabled=false)")
	}
	if resp.StandbyAvailable == nil || !*resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want true — the half IS up; availability and followership are different axes", resp.StandbyAvailable)
	}
}

// A replica lagging beyond the 30s switchover-safety threshold is available but
// not promotable. This proves the lag term is load-bearing: delete it and this
// test flips while the promoted-standby control above stays green.
func TestReplicationStatus_Row60_LaggingStandbyIsNotPromotable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	_ = unstructured.SetNestedField(replica.Object, int64(120), "status", "lag")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-lagging")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true against a 120s-lagging replica — promoting it loses committed transactions")
	}
	if resp.StandbyAvailable == nil || !*resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want true — a lagging standby is reachable, and lag rides its own axis (never a standby-absent alarm)", resp.StandbyAvailable)
	}
}

// 🔒 Organization isolation: the replica half lives in ANOTHER namespace. The
// resolver must not reach across the Org boundary — no determination.
func TestReplicationStatus_Row60_ReplicaInAnotherNamespaceDoesNotResolve(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	replica.SetNamespace("walkfive")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-ns-isolation")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.StandbyAvailable != nil {
		t.Fatalf("standbyAvailable: got %v — another Organization's replica half was adopted as this Continuum's standby", *resp.StandbyAvailable)
	}
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true off a cross-Organization half")
	}
}

// 🔒 Sovereign isolation: the mother's cache holds every managed Sovereign's
// clusters and namespace names repeat across them. A cluster id that does not
// belong to THIS deployment must contribute nothing.
func TestReplicationStatus_Row60_ForeignSovereignClusterDoesNotResolve(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	primary, replica := row60Halves()
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-foreign")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID: {primary},
		// Same namespace, same pair label, DIFFERENT Sovereign.
		"some-other-deployment-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.StandbyAvailable != nil {
		t.Fatalf("standbyAvailable: got %v — a DIFFERENT Sovereign's replica half was read as this deployment's standby", *resp.StandbyAvailable)
	}
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true off another Sovereign's cluster")
	}
}

// When the CR NAMES its pair, a pair that is not observable must yield no
// determination — never a substituted same-namespace neighbour.
func TestReplicationStatus_Row60_NamedPairIsNeverSubstituted(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	setCnpgPairRef(cr, "some-other-pair", "walkfour")
	primary, replica := row60Halves()
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-named-pair")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.StandbyAvailable != nil {
		t.Fatalf("standbyAvailable: got %v — the CR names pair %q, and the only observable pair is a different database", *resp.StandbyAvailable, "some-other-pair")
	}
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true off a pair the Continuum does not reference")
	}
}

// DISAGREEMENT RULE: the controller's standby probe says the leg is GONE while
// the informer cache still holds a Ready replica object (the stale-cache shape
// a region outage produces). The disarming oracle must win.
func TestReplicationStatus_Row60_NegativeProbeOutranksAHealthyCacheRead(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := row60Continuum()
	setContinuumControllerStatus(cr, "Healthy", false, 0)
	primary, replica := row60Halves()
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-probe-wins")
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID:                      {primary},
		dep.ID + "-me-east-215-b-1": {replica},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-r60fresh", "walkfour")
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true while the controller's own standby probe reports the leg unreachable")
	}
	if resp.StandbyAvailable == nil || *resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want an explicit false", resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Fail" {
		t.Fatalf("standby-available gate: got %q want Fail", g.Status)
	}
}

// CONTROL — the pre-existing controller-probe RELAY must still fire when no
// pair is observable in the cache. Asserting on the relay's own wording proves
// the new branch did not swallow it (a Pass alone would not distinguish them).
func TestReplicationStatus_Row60_ControllerProbeRelaySurvives(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-data/shared-pg",
		"hw-me-east-215-a-rtz-prod", []string{"hw-me-east-215-b-rtz-prod"})
	setContinuumControllerStatus(cr, "Healthy", true, 0)
	setCnpgPairRef(cr, "shared-pg", "shared-data")
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data",
		"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row60-relay-control")
	// The cache is WIRED but holds no replica half — the pre-#6268 world.
	h.SetK8sCache(crossRegionCNPGCache(t, map[string][]*unstructured.Unstructured{
		dep.ID: {primary},
	}), k8scache.NewSARCache(), "X-Forwarded-User")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
	if resp.StandbyAvailable == nil || !*resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want true — the controller's probe relay is the fallback and must survive", resp.StandbyAvailable)
	}
	g := gateByName(t, resp, "standby-available")
	if g.Status != "Pass" {
		t.Fatalf("standby-available gate: got %q want Pass", g.Status)
	}
	if !strings.Contains(g.Message, "Continuum controller's standby probe") {
		t.Fatalf("standby-available message %q is not the relay's — the relay branch was swallowed", g.Message)
	}
}
