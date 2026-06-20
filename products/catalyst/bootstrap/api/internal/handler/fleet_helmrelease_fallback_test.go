// fleet_helmrelease_fallback_test.go — #4003.
//
// On a steady-state Sovereign there are NO `apps.openova.io/Application`
// CRs on the live cluster (the Application CR is the mother-side install
// record, never projected onto the Sovereign chroot). The wizard-
// installed + bootstrap-kit apps run as Flux HelmReleases. Reading the
// Application CR list alone therefore returned `/fleet/applications` →
// total:0 despite ~65 live HelmReleases (caught live on hw173
// 7bb723da8da06047). This test pins the HelmRelease fallback:
// collectApplicationsForSovereign falls back to enumerating HelmReleases
// via h.k8sCache (the SAME source the AppDetail + placement paths use)
// when the Application CR list is empty, so the cross-Sovereign table
// enumerates the real running apps and the per-Sovereign apps count is
// non-zero.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// newHelmReleaseCR composes a minimal Flux HelmRelease unstructured for
// the fleet HR-fallback test. `ready` maps onto the Ready condition the
// fallback reads for the row's status.
func newHelmReleaseCR(name, ns, chart, version, ready string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	u.SetKind("HelmRelease")
	u.SetName(name)
	u.SetNamespace(ns)
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"chart": map[string]any{
			"spec": map[string]any{"chart": chart, "version": version},
		},
		"targetNamespace": ns,
		"releaseName":     name,
	}, "spec")
	if ready != "" {
		_ = unstructured.SetNestedSlice(u.Object, []any{
			map[string]any{"type": "Ready", "status": ready},
		}, "status", "conditions")
	}
	return u
}

// fleetHRCacheFactory wires a started k8scache.Factory with a single
// cluster registered under `clusterID`, the helmrelease kind, and the
// seeded HR objects — mirroring dashboard_test's newDashHandlerWithCache
// but scoped to the HelmRelease kind the fleet fallback reads.
func fleetHRCacheFactory(t *testing.T, clusterID string, hrs ...*unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	scheme := runtime.NewScheme()
	hrGVK := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	hrListGVK := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"}
	scheme.AddKnownTypeWithName(hrGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(hrListGVK, &unstructured.UnstructuredList{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}: "HelmReleaseList",
	}
	rtObjs := make([]runtime.Object, 0, len(hrs))
	for _, o := range hrs {
		rtObjs = append(rtObjs, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, rtObjs...)
	core := kfake.NewSimpleClientset()

	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{
		Name:       "helmrelease",
		GVR:        schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
		Namespaced: true,
	})
	cfg := k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: r,
		Clusters: []k8scache.ClusterRef{
			{ID: clusterID, DynamicClient: dyn, CoreClient: core},
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

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _, _ := f.List(clusterID, "helmrelease", labels.Everything())
		if len(got) >= len(hrs) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}

// TestHandleFleetApplications_HelmReleaseFallback — the #4003 root fix.
// A Sovereign with ZERO Application CRs but live HelmReleases must
// surface those HRs as fleet rows (total>0) and a non-zero per-Sovereign
// apps count.
func TestHandleFleetApplications_HelmReleaseFallback(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installFleetSovereign(t, h, "sov-hr", "hr.example.com", "ready")

	// Application CR list is EMPTY (no seed) — the chroot reality.
	factory, _ := fakeFleetDynamicFactory()
	h.dynamicFactory = factory

	// k8sCache registered under the Sovereign's id with two live HRs.
	f := fleetHRCacheFactory(t, dep.ID,
		newHelmReleaseCR("bp-grafana", "mgmt", "grafana", "1.2.3", "True"),
		newHelmReleaseCR("bp-sandbox", "rtz", "sandbox", "0.4.0", "False"),
	)
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetApplicationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total: got %d want 2 (HR-fallback should enumerate both HelmReleases); body=%s",
			resp.Total, rec.Body.String())
	}
	// Per-Sovereign rollup count must be non-zero (the issue's acceptance).
	foundCount := -1
	for _, s := range resp.Sovereigns {
		if s.ID == dep.ID {
			foundCount = s.Apps
		}
	}
	if foundCount != 2 {
		t.Fatalf("sovereigns[].apps: got %d want 2 for %s; rollup=%+v", foundCount, dep.ID, resp.Sovereigns)
	}
	// Row-level identity + status assertions.
	var grafanaReady, sandboxFailed bool
	for _, row := range resp.Applications {
		if row.App.Name == "bp-grafana" && row.Status == "Ready" {
			grafanaReady = true
		}
		if row.App.Name == "bp-sandbox" && row.Status == "Failed" {
			sandboxFailed = true
		}
	}
	if !grafanaReady {
		t.Fatalf("expected bp-grafana row with status=Ready; got %+v", resp.Applications)
	}
	if !sandboxFailed {
		t.Fatalf("expected bp-sandbox row with status=Failed; got %+v", resp.Applications)
	}
}

// TestHandleFleetApplications_MergesCRsAndHRs — when Application CRs DO
// exist, the table MERGES them with the platform HelmReleases that have
// no companion CR. A CR-backed app's own HelmRelease is deduped so it is
// never double-counted; an HR-only platform app still surfaces.
func TestHandleFleetApplications_MergesCRsAndHRs(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installFleetSovereign(t, h, "sov-both", "both.example.com", "ready")

	// `wp` is a wizard app with an Application CR.
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "fsn1"),
	)
	h.dynamicFactory = factory

	// Live HRs: the companion HR for `wp` (same ns+name → must dedup) PLUS
	// a platform HR with no CR (bp-grafana → must appear).
	f := fleetHRCacheFactory(t, dep.ID,
		newHelmReleaseCR("wp", "acme", "wordpress", "1.0", "True"),
		newHelmReleaseCR("bp-grafana", "mgmt", "grafana", "1.2.3", "True"),
	)
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications", nil, registerFleetRoutes)
	var resp fleetApplicationsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Fatalf("expected merge of 1 CR + 1 HR-only (companion HR deduped) = 2; got total=%d %+v",
			resp.Total, resp.Applications)
	}
	var haveWP, haveGrafana int
	for _, row := range resp.Applications {
		switch row.App.Name {
		case "wp":
			haveWP++
		case "bp-grafana":
			haveGrafana++
		}
	}
	if haveWP != 1 {
		t.Fatalf("wp must appear exactly once (CR row, companion HR deduped); got %d", haveWP)
	}
	if haveGrafana != 1 {
		t.Fatalf("bp-grafana (HR-only) must appear once; got %d", haveGrafana)
	}
}

var _ = metav1.Now
