package handler

// applications_placement_runtime_livedata_test.go (#3986) — the LIVE-DATA
// proof. These tests load the ACTUAL grafana / keycloak Pod objects (plus
// the real host Nodes + the `mgmt` Namespace) captured from BOTH regions of
// hw173 (a 2-region prov) on 2026-06-20, seed them into a 2-cluster
// k8scache, and run them through the REAL derivePlacementTargets matcher.
//
// They reproduce the exact #3986 live symptom — `…/applications/bp-grafana/
// placement?namespace=mgmt` returned `{"targets":[]}` (false singleton) on
// image 3a1bb63 — and lock the fix: the bp-prefixed AppDetail route id must
// resolve the bare-labelled, loft-synced pods so grafana/keycloak surface 2
// targets. Unlike the synthetic fixtures, these are byte-for-byte the live
// host Pods: mangled name `grafana-…-x-grafana-x-mgmt-vcluster`, host ns
// `mgmt`, labels app.kubernetes.io/{instance,name}=grafana, the full loft
// annotation set, and region resolved off the REAL host Node label
// (the pods carry no openova.io/region label — the Node fallback is what
// makes region-a vs region-b distinct).
//
// testdata/hw173_3986/ holds the captured JSON (grafana-{a,b}.json,
// keycloak-{a,b}.json, nodes-{a,b}.json, ns-mgmt-{a,b}.json).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// loadLiveList reads a captured `kubectl get -o json` file and returns its
// items as runtime.Objects (or the single object when the file is not a
// List). Fatal on any read/parse error — these are checked-in fixtures.
func loadLiveList(t *testing.T, rel string) []runtime.Object {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hw173_3986", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	if items, ok := top["items"].([]any); ok {
		out := make([]runtime.Object, 0, len(items))
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, &unstructured.Unstructured{Object: m})
		}
		return out
	}
	return []runtime.Object{&unstructured.Unstructured{Object: top}}
}

// newLivePlacementHandler seeds a 2-cluster k8scache with the captured
// pods/nodes/namespace for each region and a deployment record declaring the
// two real cloud regions, then returns a Handler wired to it.
func newLivePlacementHandler(t *testing.T, depID, regionA, regionB string, objsA, objsB []runtime.Object) *Handler {
	t.Helper()

	clusterA := depID
	clusterB := depID + "-" + regionB

	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(k8scache.Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Namespaced: false})
	_ = r.Add(k8scache.Kind{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Namespaced: false})

	dynA, coreA := dashFixtureClients(objsA...)
	dynB, coreB := dashFixtureClients(objsB...)

	cfg := k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: r,
		Clusters: []k8scache.ClusterRef{
			{ID: clusterA, DynamicClient: dynA, CoreClient: coreA},
			{ID: clusterB, DynamicClient: dynB, CoreClient: coreB},
		},
	}
	f, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, _, _ := f.List(clusterA, "pod", labels.Everything())
		b, _, _ := f.List(clusterB, "pod", labels.Everything())
		if len(a) >= 1 && len(b) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	h.deployments.Store(depID, &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: "hw173.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: regionA},
				{CloudRegion: regionB},
			},
		},
	})
	return h
}

// componentLiveTargets builds the per-region object sets (pods + node + ns)
// for `component` and resolves placement targets through the REAL handler.
func componentLiveTargets(t *testing.T, component, queryName string) runtimePlacementResponse {
	t.Helper()
	depID := "7bb723da8da06047"
	// The REAL hw173 node labels: region-a nodes carry
	// topology.kubernetes.io/region=me-east-215-a, region-b =…-b. The
	// deployment record's CloudRegion only feeds the cluster fallback; the
	// authoritative per-pod region join is the host Node label below.
	regionA, regionB := "me-east-215-a", "me-east-215-b"

	objsA := loadLiveList(t, component+"-a.json")
	objsA = append(objsA, loadLiveList(t, "nodes-a.json")...)
	objsA = append(objsA, loadLiveList(t, "ns-mgmt-a.json")...)

	objsB := loadLiveList(t, component+"-b.json")
	objsB = append(objsB, loadLiveList(t, "nodes-b.json")...)
	objsB = append(objsB, loadLiveList(t, "ns-mgmt-b.json")...)

	h := newLivePlacementHandler(t, depID, regionA, regionB, objsA, objsB)
	return callPlacement(t, h, depID, queryName)
}

// TestPlacementRuntime_LiveHw173Grafana_BpPrefixed_2Targets is the canonical
// #3986 live-data proof: the REAL grafana pods from both hw173 regions,
// queried with the REAL bp-prefixed route id, must surface 2 targets.
func TestPlacementRuntime_LiveHw173Grafana_BpPrefixed_2Targets(t *testing.T) {
	resp := componentLiveTargets(t, "grafana", "bp-grafana")
	if len(resp.Targets) != 2 {
		t.Fatalf("LIVE bp-grafana targets: got %d want 2 — the #3986 false-singleton bug; targets=%+v",
			len(resp.Targets), resp.Targets)
	}
	regions := map[string]bool{}
	for _, tgt := range resp.Targets {
		if tgt.Role != bpv1.DataRolePrimary {
			t.Fatalf("LIVE bp-grafana target %+v: role %q want Primary", tgt, tgt.Role)
		}
		regions[tgt.Region] = true
	}
	if len(regions) != 2 {
		t.Fatalf("LIVE bp-grafana: got regions %v want 2 distinct (region-a + region-b); targets=%+v", regions, resp.Targets)
	}
	if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityMultiPrimary); got == bpv1.PatternSingleton {
		t.Fatalf("LIVE bp-grafana pattern is singleton — the #3986 bug; targets=%+v", resp.Targets)
	}
	t.Logf("LIVE bp-grafana -> %d targets, regions=%v (PASS)", len(resp.Targets), regions)
}

// Bare `grafana` (the wizard-install form / what the pre-fix endpoint already
// half-resolved) must ALSO surface both regions — proving the fix is additive
// and the bare path still works.
func TestPlacementRuntime_LiveHw173Grafana_Bare_2Targets(t *testing.T) {
	resp := componentLiveTargets(t, "grafana", "grafana")
	if len(resp.Targets) != 2 {
		t.Fatalf("LIVE grafana (bare) targets: got %d want 2; targets=%+v", len(resp.Targets), resp.Targets)
	}
}

// The REAL keycloak StatefulSet pods (keycloak-0-x-keycloak-x-mgmt-vcluster)
// from both regions, queried bp-prefixed, must surface 2 targets.
func TestPlacementRuntime_LiveHw173Keycloak_BpPrefixed_2Targets(t *testing.T) {
	resp := componentLiveTargets(t, "keycloak", "bp-keycloak")
	if len(resp.Targets) != 2 {
		t.Fatalf("LIVE bp-keycloak targets: got %d want 2 — #3986; targets=%+v", len(resp.Targets), resp.Targets)
	}
	regions := map[string]bool{}
	for _, tgt := range resp.Targets {
		regions[tgt.Region] = true
	}
	if len(regions) != 2 {
		t.Fatalf("LIVE bp-keycloak: got regions %v want 2 distinct; targets=%+v", regions, resp.Targets)
	}
}

// NO-REGRESSION cross-check: a genuine control-plane single-region component
// must STAY a singleton. catalyst-api runs in region-a ONLY (oracle: "A-only
// (control plane)", 1 target, singleton). The #3986 bp- strip must NOT
// fabricate a second target — the bp-stripped form `catalyst-api` matches the
// SAME single A-only pod, never an extra region. Uses the REAL hw173
// catalyst-api pod (region-a) + an empty region-b set.
func TestPlacementRuntime_LiveHw173CatalystApi_StaysSingleton(t *testing.T) {
	depID := "7bb723da8da06047"
	regionA, regionB := "me-east-215-a", "me-east-215-b"

	objsA := loadLiveList(t, "capi-a.json")
	objsA = append(objsA, loadLiveList(t, "nodes-a.json")...)
	objsA = append(objsA, loadLiveList(t, "ns-catalyst-system-a.json")...)

	// region-b: catalyst-api absent (capi-b.json is an empty List) — only the
	// nodes + ns are present, mirroring the live "0 pods in B" ground truth.
	objsB := loadLiveList(t, "capi-b.json")
	objsB = append(objsB, loadLiveList(t, "nodes-b.json")...)
	objsB = append(objsB, loadLiveList(t, "ns-catalyst-system-b.json")...)

	h := newLivePlacementHandler(t, depID, regionA, regionB, objsA, objsB)

	for _, q := range []string{"bp-catalyst-api", "catalyst-api"} {
		resp := callPlacement(t, h, depID, q)
		if len(resp.Targets) != 1 {
			t.Fatalf("LIVE %s targets: got %d want 1 (must STAY singleton, A-only control plane); targets=%+v",
				q, len(resp.Targets), resp.Targets)
		}
		if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != bpv1.PatternSingleton {
			t.Fatalf("LIVE %s pattern: got %q want singleton (honest single-region); targets=%+v", q, got, resp.Targets)
		}
	}
}
