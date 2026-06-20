package handler

// applications_placement_runtime_test.go (#3982) — locks the runtime
// placement derivation: a component that runs in BOTH regions must surface
// 2 targets (Pattern ≠ singleton), a CNPG pair must surface Primary +
// Standby, and a genuinely single-region component must surface ONE target
// (Pattern singleton — honest). These reproduce the hw173 failure: grafana
// + keycloak ran in both regions yet the Topology tab showed singleton
// because nothing populated placement from the live runtime.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// placementFixturePod builds a Pod with the labels the derivation reads.
func placementFixturePod(ns, name, instance, region, cnpgRole string) *unstructured.Unstructured {
	lbls := map[string]any{
		"app.kubernetes.io/instance": instance,
	}
	if region != "" {
		lbls["openova.io/region"] = region
	}
	if cnpgRole != "" {
		lbls["openova.io/cnpg-role"] = cnpgRole
		lbls["cnpg.io/cluster"] = instance
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":         ns,
			"name":              name,
			"creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"resourceVersion":   "1",
			"labels":            lbls,
		},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "main", "image": "ghcr.io/x:1"}},
		},
		"status": map[string]any{
			"phase":      "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
}

// newPlacementHandler stands up a Handler whose k8scache holds TWO region
// clusters, each seeded with the supplied pods, plus a deployment record
// declaring the two cloud regions (so the cluster→region fallback resolves).
func newPlacementHandler(t *testing.T, depID, regionA, regionB string, podsA, podsB []*unstructured.Unstructured) *Handler {
	t.Helper()

	clusterA := depID
	clusterB := depID + "-" + regionB

	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(k8scache.Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Namespaced: false})
	_ = r.Add(k8scache.Kind{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Namespaced: false})

	dynA, coreA := dashFixtureClients(toRuntimeObjs(podsA)...)
	dynB, coreB := dashFixtureClients(toRuntimeObjs(podsB)...)

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

	// Wait for both clusters' pod informers to sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a, _, _ := f.List(clusterA, "pod", labels.Everything())
		b, _, _ := f.List(clusterB, "pod", labels.Everything())
		if len(a) >= len(podsA) && len(b) >= len(podsB) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	// Seed the deployment record so clusterRegionMap resolves both regions.
	h.deployments.Store(depID, &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: "t99.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: regionA},
				{CloudRegion: regionB},
			},
		},
	})
	return h
}

func toRuntimeObjs(pods []*unstructured.Unstructured) []runtime.Object {
	out := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

// callPlacement invokes HandleApplicationPlacement via a chi router so the
// {id}/{name} URL params resolve.
func callPlacement(t *testing.T, h *Handler, depID, name string) runtimePlacementResponse {
	t.Helper()
	router := chi.NewRouter()
	router.Get("/api/v1/sovereigns/{id}/applications/{name}/placement", h.HandleApplicationPlacement)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+depID+"/applications/"+name+"/placement", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("placement: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp runtimePlacementResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return resp
}

// A stateless component running in BOTH regions must surface 2 targets,
// both Primary → Pattern active-active (NOT singleton). This is the hw173
// grafana/keycloak case.
func TestPlacementRuntime_StatelessTwoRegions_NotSingleton(t *testing.T) {
	depID := "dep-grafana-2region"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{placementFixturePod("mgmt", "grafana-aaa", "grafana", regionA, "")},
		[]*unstructured.Unstructured{placementFixturePod("mgmt", "grafana-bbb", "grafana", regionB, "")},
	)

	resp := callPlacement(t, h, depID, "grafana")
	if len(resp.Targets) != 2 {
		t.Fatalf("grafana targets: got %d want 2 (%+v)", len(resp.Targets), resp.Targets)
	}
	for _, tgt := range resp.Targets {
		if tgt.Role != bpv1.DataRolePrimary {
			t.Fatalf("grafana target %+v: role %q want Primary (stateless multi-region = multi-primary)", tgt, tgt.Role)
		}
	}
	if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityMultiPrimary); got == bpv1.PatternSingleton {
		t.Fatalf("grafana pattern is singleton — the exact hw173 bug; targets=%+v", resp.Targets)
	}
	// Both regions represented.
	regions := map[string]bool{}
	for _, tgt := range resp.Targets {
		regions[tgt.Region] = true
	}
	if !regions[regionA] || !regions[regionB] {
		t.Fatalf("grafana regions: got %v want both %s + %s", regions, regionA, regionB)
	}
}

// A CNPG pair (primary in region-a, replica in region-b) must surface a
// Primary target + a Standby·Hot target → Pattern active-hot-standby.
func TestPlacementRuntime_CNPGPair_PrimaryPlusStandby(t *testing.T) {
	depID := "dep-cnpg-pair"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{placementFixturePod("shared-data", "shared-pg-1", "shared-pg", regionA, "primary")},
		[]*unstructured.Unstructured{placementFixturePod("shared-data", "shared-pg-2", "shared-pg", regionB, "replica")},
	)

	resp := callPlacement(t, h, depID, "shared-pg")
	if len(resp.Targets) != 2 {
		t.Fatalf("cnpg targets: got %d want 2 (%+v)", len(resp.Targets), resp.Targets)
	}
	var primaries, hotStandbys int
	for _, tgt := range resp.Targets {
		switch tgt.Role {
		case bpv1.DataRolePrimary:
			primaries++
		case bpv1.DataRoleStandby:
			if tgt.StandbyType == bpv1.StandbyHot {
				hotStandbys++
			}
		}
	}
	if primaries != 1 || hotStandbys != 1 {
		t.Fatalf("cnpg roles: primaries=%d hotStandbys=%d want 1+1 (%+v)", primaries, hotStandbys, resp.Targets)
	}
	if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != bpv1.PatternActiveHotStandby {
		t.Fatalf("cnpg pattern: got %q want active-hot-standby (%+v)", got, resp.Targets)
	}
}

// A genuinely single-region component surfaces ONE target → singleton.
// This is the HONEST singleton (not the uniform false one).
func TestPlacementRuntime_SingleRegion_HonestSingleton(t *testing.T) {
	depID := "dep-single"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{placementFixturePod("apps", "solo-xyz", "solo", regionA, "")},
		[]*unstructured.Unstructured{}, // nothing in region-b
	)

	resp := callPlacement(t, h, depID, "solo")
	if len(resp.Targets) != 1 {
		t.Fatalf("single-region targets: got %d want 1 (%+v)", len(resp.Targets), resp.Targets)
	}
	if resp.Targets[0].Region != regionA {
		t.Fatalf("single-region: region %q want %q", resp.Targets[0].Region, regionA)
	}
	if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != bpv1.PatternSingleton {
		t.Fatalf("single-region pattern: got %q want singleton (%+v)", got, resp.Targets)
	}
}

// A component that doesn't exist anywhere surfaces zero targets (empty, not
// an error) — the FE then keeps its legacy fallback.
func TestPlacementRuntime_Unknown_EmptyTargets(t *testing.T) {
	depID := "dep-empty"
	h := newPlacementHandler(t, depID, "me-east-215-a", "me-east-215-b",
		[]*unstructured.Unstructured{placementFixturePod("mgmt", "grafana-aaa", "grafana", "me-east-215-a", "")},
		[]*unstructured.Unstructured{},
	)
	resp := callPlacement(t, h, depID, "does-not-exist")
	if len(resp.Targets) != 0 {
		t.Fatalf("unknown component: got %d targets want 0 (%+v)", len(resp.Targets), resp.Targets)
	}
	if !resp.DerivedFromRuntime {
		t.Fatalf("derivedFromRuntime must be true even when empty")
	}
}
