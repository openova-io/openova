// dashboard_test.go — coverage for the Sovereign Dashboard treemap
// endpoint.
//
// These tests pin both:
//
//  1. The HTTP shape the UI consumes (group_by validation, color_by,
//     size_by, Percentage encoding).
//  2. The end-to-end cache→aggregation path: a fake k8scache.Factory
//     seeded with a handful of unstructured Pods + PVCs (+ optional
//     PodMetrics) is wired into the Handler, then the handler is
//     exercised across every group_by × color_by × size_by combo.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func quietHandlerLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// dashboardFixturePod produces an unstructured Pod with the labels +
// resource limits the dashboard aggregations read.
type dashFixturePod struct {
	Namespace   string
	Name        string
	Application string
	Family      string
	CPULimit    string
	MemLimit    string
	Ready       bool
	Created     time.Time
	PVCs        []string
}

func mkDashPod(p dashFixturePod) *unstructured.Unstructured {
	containers := []any{
		map[string]any{
			"name":  "main",
			"image": "ghcr.io/openova-io/test:1",
			"resources": map[string]any{
				"limits": map[string]any{
					"cpu":    p.CPULimit,
					"memory": p.MemLimit,
				},
			},
		},
	}
	volumes := make([]any, 0, len(p.PVCs))
	for _, pvc := range p.PVCs {
		volumes = append(volumes, map[string]any{
			"name": "v-" + pvc,
			"persistentVolumeClaim": map[string]any{
				"claimName": pvc,
			},
		})
	}
	readyStatus := "False"
	if p.Ready {
		readyStatus = "True"
	}
	created := p.Created
	if created.IsZero() {
		created = time.Now().Add(-1 * time.Hour)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":         p.Namespace,
			"name":              p.Name,
			"creationTimestamp": created.UTC().Format(time.RFC3339),
			"resourceVersion":   "1",
			"labels": map[string]any{
				"app.kubernetes.io/instance": p.Application,
				"catalyst.openova.io/family": p.Family,
			},
		},
		"spec": map[string]any{
			"containers": containers,
			"volumes":    volumes,
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": readyStatus},
			},
		},
	}}
}

func mkDashPVC(ns, name, storage string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
		"spec": map[string]any{
			"resources": map[string]any{
				"requests": map[string]any{
					"storage": storage,
				},
			},
		},
	}}
}

// mkDashPodMetrics emits a metrics.k8s.io/v1beta1 PodMetrics for a
// single-container pod with the given cpu usage (millicores).
func mkDashPodMetrics(ns, name, cpuUsage string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "PodMetrics",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
		"containers": []any{
			map[string]any{
				"name": "main",
				"usage": map[string]any{
					"cpu":    cpuUsage,
					"memory": "0",
				},
			},
		},
	}}
}

// dashFixtureScheme + listKinds describes the fake dynamic client the
// k8scache.Factory talks to during tests.
func dashFixtureClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, *kfake.Clientset) {
	scheme := runtime.NewScheme()
	gvks := []struct {
		gvk schema.GroupVersionKind
	}{
		{schema.GroupVersionKind{Version: "v1", Kind: "Pod"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "PodList"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaimList"}},
		{schema.GroupVersionKind{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics"}},
		{schema.GroupVersionKind{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetricsList"}},
	}
	for _, g := range gvks {
		if strings.HasSuffix(g.gvk.Kind, "List") {
			scheme.AddKnownTypeWithName(g.gvk, &unstructured.UnstructuredList{})
		} else {
			scheme.AddKnownTypeWithName(g.gvk, &unstructured.Unstructured{})
		}
	}
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                               "PodList",
		{Version: "v1", Resource: "persistentvolumeclaims"}:             "PersistentVolumeClaimList",
		{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}: "PodMetricsList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	core := kfake.NewSimpleClientset()
	return dyn, core
}

// newDashHandlerWithCache wires a Handler with a started k8scache
// factory containing a single test cluster id. Pass `withMetrics=true`
// to register PodMetrics on the cluster's discovery surface (covered
// by the k8scache_test.go side; dashboard tests focus on the
// no-metrics path which is the correct default).
func newDashHandlerWithCache(t *testing.T, clusterID string, withMetrics bool, objs ...*unstructured.Unstructured) *Handler {
	t.Helper()
	if withMetrics {
		t.Skipf("metrics-server present-path is exercised by k8scache_test; dashboard tests focus on the absent-path null-percentage contract")
	}
	rtObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rtObjs = append(rtObjs, o)
	}
	dyn, core := dashFixtureClients(rtObjs...)
	// Minimal registry — the dashboard handler only reads pod, PVC,
	// and (optionally) podmetrics. A full DefaultKinds registry would
	// require every GVR to be wired into the fake scheme.
	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{
		Name:       "pod",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Namespaced: true,
	})
	_ = r.Add(k8scache.Kind{
		Name:       "persistentvolumeclaim",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"},
		Namespaced: true,
	})
	_ = r.Add(k8scache.Kind{
		Name:       "podmetrics",
		GVR:        schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"},
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

	// Wait for informers to populate the indexer with the seeded
	// objects. Listing returns 0 items both when the informer hasn't
	// synced AND when there are genuinely no objects, so we count
	// expected pods/pvcs upfront and poll until the indexer matches.
	wantPods := 0
	wantPVCs := 0
	for _, o := range objs {
		switch o.GetKind() {
		case "Pod":
			wantPods++
		case "PersistentVolumeClaim":
			wantPVCs++
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotPods, _, _ := f.List(clusterID, "pod", labels.Everything())
		gotPVCs, _, _ := f.List(clusterID, "persistentvolumeclaim", labels.Everything())
		if len(gotPods) >= wantPods && len(gotPVCs) >= wantPVCs {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	return h
}

func dashGet(t *testing.T, h *Handler, qs string) treemapResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/treemap?"+qs, nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out treemapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

/* ── Validation tests (no cache wired) ─────────────────────────── */

func TestDashboardTreemap_RejectsUnknownDimension(t *testing.T) {
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?group_by=widget", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid-group-by") {
		t.Fatalf("expected invalid-group-by error: %s", rec.Body.String())
	}
}

func TestDashboardTreemap_RejectsUnknownColorBy(t *testing.T) {
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?color_by=mood", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

func TestDashboardTreemap_RejectsUnknownSizeBy(t *testing.T) {
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/treemap?size_by=carbohydrates", nil)
	rec := httptest.NewRecorder()
	h.GetDashboardTreemap(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

// No cache → empty well-shaped response. Was the old fixture path's
// behaviour to return a 30-cell tree; now the contract is "empty when
// no live data" so tests reflect that.
func TestDashboardTreemap_NoCacheEmpty(t *testing.T) {
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	out := dashGet(t, h, "group_by=application")
	if len(out.Items) != 0 {
		t.Fatalf("expected empty items[] when cache absent; got %+v", out)
	}
	if out.TotalCount != 0 {
		t.Fatalf("expected total_count=0; got %d", out.TotalCount)
	}
}

// Wrong deployment_id → empty.
func TestDashboardTreemap_UnknownDeploymentEmpty(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-cilium", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=does-not-exist&group_by=application")
	if len(out.Items) != 0 {
		t.Fatalf("expected empty for unknown deployment_id; got %+v", out)
	}
}

/* ── Aggregation tests (cache wired) ───────────────────────────── */

// TestDashboardTreemap_GroupByApplication_CPULimit verifies that
// pods with the same app.kubernetes.io/instance label collapse into
// one cell whose size_value is the sum of CPU limits.
func TestDashboardTreemap_GroupByApplication_CPULimit(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-cilium", Family: "spine", CPULimit: "200m", MemLimit: "256Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p2", Application: "bp-cilium", Family: "spine", CPULimit: "300m", MemLimit: "256Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns2", Name: "p3", Application: "bp-keycloak", Family: "pilot", CPULimit: "1", MemLimit: "1Gi", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health&size_by=cpu_limit")
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 application buckets; got %d (%+v)", len(out.Items), out)
	}
	bySize := map[string]float64{}
	for _, it := range out.Items {
		bySize[it.Name] = it.SizeValue
	}
	if bySize["bp-cilium"] != 500 {
		t.Errorf("bp-cilium cpu_limit: got %v want 500m", bySize["bp-cilium"])
	}
	if bySize["bp-keycloak"] != 1000 {
		t.Errorf("bp-keycloak cpu_limit: got %v want 1000m", bySize["bp-keycloak"])
	}
}

// TestDashboardTreemap_HealthColor — color_by=health emits a real
// percentage (Σ Ready / total). Two of three pods Ready → 66.67%.
func TestDashboardTreemap_HealthColor(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-app", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p2", Application: "bp-app", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p3", Application: "bp-app", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: false}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health&size_by=cpu_limit")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 bucket; got %d", len(out.Items))
	}
	if out.Items[0].Percentage == nil {
		t.Fatalf("health percentage must not be nil for cache-only data")
	}
	got := *out.Items[0].Percentage
	want := 100.0 * 2.0 / 3.0
	if got < want-0.5 || got > want+0.5 {
		t.Errorf("health pct: got %v want ~%v", got, want)
	}
}

// TestDashboardTreemap_UtilizationNullWhenNoMetrics — no PodMetrics
// in the cache → percentage encodes JSON null.
func TestDashboardTreemap_UtilizationNullWhenNoMetrics(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-app", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=utilization&size_by=cpu_limit")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 bucket; got %d", len(out.Items))
	}
	if out.Items[0].Percentage != nil {
		t.Errorf("expected nil percentage when metrics-server absent; got %v", *out.Items[0].Percentage)
	}
}

// TestDashboardTreemap_NestedFamilyApplication — two layers nest, the
// parent's size is the sum of its children's sizes.
func TestDashboardTreemap_NestedFamilyApplication(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-cilium", Family: "spine", CPULimit: "200m", MemLimit: "64Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p2", Application: "bp-flux", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns2", Name: "p3", Application: "bp-keycloak", Family: "pilot", CPULimit: "1", MemLimit: "64Mi", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=family,application&color_by=health&size_by=cpu_limit")
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 family buckets; got %d", len(out.Items))
	}
	parents := map[string]treemapItem{}
	for _, p := range out.Items {
		parents[p.Name] = p
	}
	spine := parents["Spine"]
	if len(spine.Children) != 2 {
		t.Errorf("spine children: got %d want 2", len(spine.Children))
	}
	if spine.SizeValue != 300 {
		t.Errorf("spine size: got %v want 300m", spine.SizeValue)
	}
	pilot := parents["Pilot"]
	if pilot.SizeValue != 1000 {
		t.Errorf("pilot size: got %v want 1000m", pilot.SizeValue)
	}
}

// TestDashboardTreemap_StorageLimitFromPVCs — size_by=storage_limit
// sums PVC.spec.resources.requests.storage of every PVC referenced by
// pods in the bucket.
func TestDashboardTreemap_StorageLimitFromPVCs(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-app", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true, PVCs: []string{"data-0"}}),
		mkDashPVC("ns1", "data-0", "1Gi"),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health&size_by=storage_limit")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 bucket; got %d", len(out.Items))
	}
	want := float64(1 * 1024 * 1024 * 1024)
	if out.Items[0].SizeValue != want {
		t.Errorf("storage_limit: got %v want %v", out.Items[0].SizeValue, want)
	}
}

// TestDashboardTreemap_PercentageInRange — guard that no bucket
// produces an out-of-range percentage. Uses the health metric so we
// always get a non-nil percentage.
func TestDashboardTreemap_PercentageInRange(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-a", Family: "spine", CPULimit: "100m", MemLimit: "64Mi", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns2", Name: "p2", Application: "bp-b", Family: "pilot", CPULimit: "100m", MemLimit: "64Mi", Ready: false}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=family,application&color_by=health&size_by=cpu_limit")
	for _, p := range out.Items {
		if p.Percentage == nil {
			continue
		}
		if *p.Percentage < 0 || *p.Percentage > 100 {
			t.Fatalf("parent %s percentage out of range: %f", p.Name, *p.Percentage)
		}
		for _, c := range p.Children {
			if c.Percentage == nil {
				continue
			}
			if *c.Percentage < 0 || *c.Percentage > 100 {
				t.Fatalf("child %s percentage out of range: %f", c.Name, *c.Percentage)
			}
		}
	}
}

