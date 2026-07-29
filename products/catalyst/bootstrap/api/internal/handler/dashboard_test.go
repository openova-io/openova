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
	CPURequest  string
	MemRequest  string
	CPULimit    string
	MemLimit    string
	Ready       bool
	Created     time.Time
	PVCs        []string
}

func mkDashPod(p dashFixturePod) *unstructured.Unstructured {
	resources := map[string]any{}
	if p.CPURequest != "" || p.MemRequest != "" {
		req := map[string]any{}
		if p.CPURequest != "" {
			req["cpu"] = p.CPURequest
		}
		if p.MemRequest != "" {
			req["memory"] = p.MemRequest
		}
		resources["requests"] = req
	}
	if p.CPULimit != "" || p.MemLimit != "" {
		lim := map[string]any{}
		if p.CPULimit != "" {
			lim["cpu"] = p.CPULimit
		}
		if p.MemLimit != "" {
			lim["memory"] = p.MemLimit
		}
		resources["limits"] = lim
	}
	containers := []any{
		map[string]any{
			"name":      "main",
			"image":     "ghcr.io/openova-io/test:1",
			"resources": resources,
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
		// Wave 2 Family D: Namespaces + Nodes are joined onto Pods for
		// family/vcluster/region enrichment. Register both so tests that
		// seed them can exercise the join.
		{schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "NamespaceList"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "Node"}},
		{schema.GroupVersionKind{Version: "v1", Kind: "NodeList"}},
		// #5485: ReplicaSets are the Deployment→Pod ownerRef hop the
		// treemap walks to name an application after its Deployment
		// rather than the hash-suffixed ReplicaSet.
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSetList"}},
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
		{Version: "v1", Resource: "namespaces"}:                         "NamespaceList",
		{Version: "v1", Resource: "nodes"}:                              "NodeList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:         "ReplicaSetList",
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
	// Wave 2 Family D: namespace + node are joined onto pods for
	// family/vcluster-role/region enrichment. Register both so the
	// informer surface has them and the dashboard handler's per-cluster
	// h.k8sCache.List("namespace"|"node") returns the seeded fixtures.
	_ = r.Add(k8scache.Kind{
		Name:       "namespace",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
		Namespaced: false,
	})
	_ = r.Add(k8scache.Kind{
		Name:       "node",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "nodes"},
		Namespaced: false,
	})
	// #5485 defect B: the Deployment→Pod ownerRef hop. Registered so the
	// handler's h.k8sCache.List(cid, "replicaset", …) returns the seeded
	// fixtures and applicationKey can resolve past the ReplicaSet.
	_ = r.Add(k8scache.Kind{
		Name:       "replicaset",
		GVR:        schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"},
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
	wantNS := 0
	wantNodes := 0
	wantRS := 0
	for _, o := range objs {
		switch o.GetKind() {
		case "Pod":
			wantPods++
		case "PersistentVolumeClaim":
			wantPVCs++
		case "Namespace":
			wantNS++
		case "Node":
			wantNodes++
		case "ReplicaSet":
			wantRS++
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotPods, _, _ := f.List(clusterID, "pod", labels.Everything())
		gotPVCs, _, _ := f.List(clusterID, "persistentvolumeclaim", labels.Everything())
		gotNS, _, _ := f.List(clusterID, "namespace", labels.Everything())
		gotNodes, _, _ := f.List(clusterID, "node", labels.Everything())
		gotRS, _, _ := f.List(clusterID, "replicaset", labels.Everything())
		if len(gotPods) >= wantPods && len(gotPVCs) >= wantPVCs &&
			len(gotNS) >= wantNS && len(gotNodes) >= wantNodes &&
			len(gotRS) >= wantRS {
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

// TestDashboardTreemap_GroupByApplication_CPURequest — size_by=cpu_request
// sums spec.containers[*].resources.requests.cpu (millicores).
func TestDashboardTreemap_GroupByApplication_CPURequest(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-cilium", Family: "spine", CPURequest: "100m", CPULimit: "500m", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p2", Application: "bp-cilium", Family: "spine", CPURequest: "150m", CPULimit: "500m", Ready: true}),
		mkDashPod(dashFixturePod{Namespace: "ns2", Name: "p3", Application: "bp-keycloak", Family: "pilot", CPURequest: "500m", CPULimit: "2", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health&size_by=cpu_request")
	bySize := map[string]float64{}
	for _, it := range out.Items {
		bySize[it.Name] = it.SizeValue
	}
	if bySize["bp-cilium"] != 250 {
		t.Errorf("bp-cilium cpu_request: got %v want 250m", bySize["bp-cilium"])
	}
	if bySize["bp-keycloak"] != 500 {
		t.Errorf("bp-keycloak cpu_request: got %v want 500m", bySize["bp-keycloak"])
	}
}

// TestSumSize_NewSizeByOptions — direct unit tests of sumSize for the
// 4 new size_by options. The cache-driven integration paths
// (newDashHandlerWithCache(..., true, ...)) skip in this package by
// design (covered in k8scache_test); this exercises the math directly.
func TestSumSize_NewSizeByOptions(t *testing.T) {
	rows := []podRow{
		{cpuReq: 100, memReq: 256 * 1024 * 1024, cpuLim: 500, memLim: 1024 * 1024 * 1024, cpuUsage: 75, memUsage: 200 * 1024 * 1024, hasMetrics: true},
		{cpuReq: 200, memReq: 512 * 1024 * 1024, cpuLim: 800, memLim: 2 * 1024 * 1024 * 1024, cpuUsage: 150, memUsage: 400 * 1024 * 1024, hasMetrics: true},
	}
	cases := []struct {
		sizeBy string
		want   float64
	}{
		{"cpu_request", 300},
		{"memory_request", 768 * 1024 * 1024},
		{"cpu_usage", 225},
		{"memory_usage", 600 * 1024 * 1024},
		{"cpu_limit", 1300},
	}
	for _, c := range cases {
		got := sumSize(rows, c.sizeBy)
		if got != c.want {
			t.Errorf("sumSize(%s)=%v want %v", c.sizeBy, got, c.want)
		}
	}
}

// TestComputePercentage_UtilizationVsRequest — the new utilization
// formula: denominator is cpu_request, not cpu_limit. Limit unset is
// fine (and reflects the bp-* chart reality).
func TestComputePercentage_UtilizationVsRequest(t *testing.T) {
	rows := []podRow{
		{cpuReq: 200, cpuUsage: 100, hasMetrics: true},
	}
	pct := computePercentage(rows, "utilization")
	if pct == nil {
		t.Fatalf("expected non-nil percentage with metrics + request")
	}
	if *pct < 49.5 || *pct > 50.5 {
		t.Errorf("utilization vs request: got %v want ~50", *pct)
	}
}

// TestComputePercentage_UtilizationOver100 — over-request utilization
// is a real signal; do NOT clamp to 100.
func TestComputePercentage_UtilizationOver100(t *testing.T) {
	rows := []podRow{
		{cpuReq: 100, cpuUsage: 250, hasMetrics: true},
	}
	pct := computePercentage(rows, "utilization")
	if pct == nil {
		t.Fatalf("expected non-nil percentage")
	}
	if *pct < 249 || *pct > 251 {
		t.Errorf("over-request utilization should be ~250%%; got %v", *pct)
	}
}

// TestComputePercentage_UtilizationFallsBackToLimit — when request is
// 0, fall back to limit so legacy charts (limit-only) still surface
// a percentage instead of returning grey nil.
func TestComputePercentage_UtilizationFallsBackToLimit(t *testing.T) {
	rows := []podRow{
		{cpuReq: 0, cpuLim: 200, cpuUsage: 100, hasMetrics: true},
	}
	pct := computePercentage(rows, "utilization")
	if pct == nil {
		t.Fatalf("expected fallback to limit when request=0")
	}
	if *pct < 49.5 || *pct > 50.5 {
		t.Errorf("fallback util pct: got %v want ~50", *pct)
	}
}

// TestComputePercentage_UtilizationNullWhenNeitherSet — request=0 AND
// limit=0 (truly unbounded workload) returns nil so the cell renders
// grey rather than divide-by-zero.
func TestComputePercentage_UtilizationNullWhenNeitherSet(t *testing.T) {
	rows := []podRow{
		{cpuReq: 0, cpuLim: 0, cpuUsage: 50, hasMetrics: true},
	}
	pct := computePercentage(rows, "utilization")
	if pct != nil {
		t.Errorf("expected nil when request and limit both 0; got %v", *pct)
	}
}

// TestDashboardTreemap_DefaultSizeByIsCPURequest — no size_by query
// param defaults to cpu_request (was cpu_limit before #1084).
func TestDashboardTreemap_DefaultSizeByIsCPURequest(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPod(dashFixturePod{Namespace: "ns1", Name: "p1", Application: "bp-app", Family: "spine", CPURequest: "100m", CPULimit: "500m", Ready: true}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 bucket; got %d", len(out.Items))
	}
	if out.Items[0].SizeValue != 100 {
		t.Errorf("default size_by must be cpu_request (100m); got %v", out.Items[0].SizeValue)
	}
}

/* ── Wave 2 Family D — Namespace + Node enrichment ───────────────── */

// mkDashNamespace produces an unstructured Namespace carrying the
// canonical OpenOva labels the dashboard's family + vcluster grouping
// reads. Pass empty strings to omit a particular label.
func mkDashNamespace(name, vclusterRole, family string) *unstructured.Unstructured {
	labels := map[string]any{}
	if vclusterRole != "" {
		labels["catalyst.openova.io/vcluster-role"] = vclusterRole
	}
	if family != "" {
		labels["catalyst.openova.io/family"] = family
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":            name,
			"resourceVersion": "1",
			"labels":          labels,
		},
	}}
}

// mkDashNode produces an unstructured Node carrying region/zone labels.
// `topology.kubernetes.io/region` is the K8s-standard label hcloud-ccm
// stamps; `openova.io/region` is the OpenOva-canonical label set by
// per-region cloud-init.
func mkDashNode(name, openovaRegion, topologyRegion string) *unstructured.Unstructured {
	labels := map[string]any{}
	if openovaRegion != "" {
		labels["openova.io/region"] = openovaRegion
	}
	if topologyRegion != "" {
		labels["topology.kubernetes.io/region"] = topologyRegion
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata": map[string]any{
			"name":            name,
			"resourceVersion": "1",
			"labels":          labels,
		},
	}}
}

// mkDashPodOnNode is like mkDashPod but ALSO stamps spec.nodeName so
// node-label enrichment can resolve. The pod's own labels are left
// EMPTY for vcluster-role + family so we exercise the namespace-join
// path — this matches reality where charts don't stamp these labels
// on pods, only on the host Namespace.
func mkDashPodOnNode(ns, name, app, nodeName string) *unstructured.Unstructured {
	p := mkDashPod(dashFixturePod{
		Namespace: ns, Name: name, Application: app, Family: "",
		CPURequest: "100m", Ready: true,
	})
	_ = unstructured.SetNestedField(p.Object, nodeName, "spec", "nodeName")
	// Drop the family label that mkDashPod always sets so the empty-
	// string path exercises the namespace-join correctly.
	delete(p.Object["metadata"].(map[string]any)["labels"].(map[string]any),
		"catalyst.openova.io/family")
	return p
}

// TestDashboardTreemap_FamilyFromNamespaceLabel — when the Pod has no
// `catalyst.openova.io/family` label but its host Namespace does, the
// family grouping bucketises by the Namespace label. Pre-fix every
// pod collapsed into the single "Other" bucket.
func TestDashboardTreemap_FamilyFromNamespaceLabel(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNamespace("ns-cilium", "", "spine"),
		mkDashNamespace("ns-kc", "", "pilot"),
		mkDashPodOnNode("ns-cilium", "p1", "bp-cilium", ""),
		mkDashPodOnNode("ns-cilium", "p2", "bp-cilium", ""),
		mkDashPodOnNode("ns-kc", "p3", "bp-keycloak", ""),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=family&color_by=health&size_by=cpu_request")
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 family buckets (spine,pilot); got %d (%+v)",
			len(out.Items), out)
	}
	names := map[string]bool{}
	for _, it := range out.Items {
		names[it.Name] = true
	}
	if !names["Spine"] || !names["Pilot"] {
		t.Errorf("expected family buckets Spine+Pilot from namespace labels; got %v", names)
	}
}

// TestDashboardTreemap_VClusterFromNamespaceLabel — when the Pod has no
// vcluster-role label but its host Namespace does (the canonical
// bp-{mgmt,dmz,rtz}-vcluster shape), grouping by vcluster bucketises
// by the Namespace label. Pre-fix every pod collapsed into the single
// "host" bucket.
func TestDashboardTreemap_VClusterFromNamespaceLabel(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNamespace("mgmt", "mgmt", ""),
		mkDashNamespace("dmz", "dmz", ""),
		mkDashNamespace("rtz", "rtz", ""),
		mkDashPodOnNode("mgmt", "p1", "bp-vcluster", ""),
		mkDashPodOnNode("dmz", "p2", "bp-vcluster", ""),
		mkDashPodOnNode("rtz", "p3", "bp-vcluster", ""),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=vcluster&color_by=health&size_by=cpu_request")
	if len(out.Items) != 3 {
		t.Fatalf("expected 3 vcluster buckets (mgmt,dmz,rtz); got %d (%+v)",
			len(out.Items), out)
	}
	names := map[string]bool{}
	for _, it := range out.Items {
		names[it.Name] = true
	}
	for _, want := range []string{"mgmt", "dmz", "rtz"} {
		if !names[want] {
			t.Errorf("expected vcluster bucket %q; got %v", want, names)
		}
	}
}

// TestDashboardTreemap_ClusterLabelPostfixesRegion — TBD-E4b (#1756).
// Grouping by `cluster` should not surface the bare deployment-id hex
// (e.g. `alpha` in tests / `30dbef8b238c2d84` in prod) on the cell
// label; when the row carries a region, postfix `(<region>)` so the
// label reads `alpha (hz-hel-rtz-prod)`. Bucket id stays the cluster
// id so all rows still merge into one cell.
func TestDashboardTreemap_ClusterLabelPostfixesRegion(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNode("node-hel-1", "hz-hel-rtz-prod", ""),
		mkDashPodOnNode("ns1", "p1", "bp-cilium", "node-hel-1"),
		mkDashPodOnNode("ns1", "p2", "bp-cilium", "node-hel-1"),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=cluster&color_by=health&size_by=cpu_request")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 cluster bucket; got %d (%+v)", len(out.Items), out)
	}
	got := out.Items[0].Name
	want := "alpha (hz-hel-rtz-prod)"
	if got != want {
		t.Errorf("cluster label = %q; want %q", got, want)
	}
}

// TestDashboardTreemap_ClusterLabelNoRegion — when no row carries a
// region label (single-region Sovereign with no node-label
// enrichment), the cluster cell falls back to the bare cluster id.
func TestDashboardTreemap_ClusterLabelNoRegion(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashPodOnNode("ns1", "p1", "bp-cilium", ""),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=cluster&color_by=health&size_by=cpu_request")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 cluster bucket; got %d (%+v)", len(out.Items), out)
	}
	if got, want := out.Items[0].Name, "alpha"; got != want {
		t.Errorf("cluster label = %q; want %q", got, want)
	}
}

// TestDashboardTreemap_RegionFromNodeLabel — when the Pod has no
// openova.io/region label but its host Node does, region grouping
// reads from the Node's label set. Both `openova.io/region` and the
// K8s-standard `topology.kubernetes.io/region` are consulted.
func TestDashboardTreemap_RegionFromNodeLabel(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		// One node per region; openova.io label canonical, topology
		// fallback for the second region.
		mkDashNode("node-fsn-1", "fsn1", ""),
		mkDashNode("node-hel-1", "", "hel1"),
		// Pods bound to each node via spec.nodeName.
		mkDashPodOnNode("ns1", "p1", "bp-cilium", "node-fsn-1"),
		mkDashPodOnNode("ns1", "p2", "bp-cilium", "node-hel-1"),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=region&color_by=health&size_by=cpu_request")
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 region buckets (fsn1,hel1); got %d (%+v)",
			len(out.Items), out)
	}
	names := map[string]bool{}
	for _, it := range out.Items {
		names[it.Name] = true
	}
	for _, want := range []string{"fsn1", "hel1"} {
		if !names[want] {
			t.Errorf("expected region bucket %q; got %v", want, names)
		}
	}
}

// TestDashboardTreemap_FamilyPodLabelOverridesNamespace — when BOTH
// pod-level and namespace-level family labels are set, the pod-level
// label wins. Mirrors mimir's _helpers.tpl which stamps family on the
// pod template; the Namespace might also have a different (or absent)
// label and we want pod-level granularity to take precedence.
func TestDashboardTreemap_FamilyPodLabelOverridesNamespace(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNamespace("ns1", "", "observability"),
		// Pod-level family=cortex overrides the namespace's observability.
		mkDashPod(dashFixturePod{
			Namespace: "ns1", Name: "p1", Application: "mimir",
			Family: "cortex", CPURequest: "100m", Ready: true,
		}),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=family&color_by=health&size_by=cpu_request")
	if len(out.Items) != 1 {
		t.Fatalf("expected 1 bucket; got %d (%+v)", len(out.Items), out)
	}
	if out.Items[0].Name != "Cortex" {
		t.Errorf("expected pod-level Cortex to win over namespace observability; got %q",
			out.Items[0].Name)
	}
}

// TestDropEphemeralRows_FiltersJobOwnedPods — #3687 (fold #3692): one-shot
// Job pods (cutover-*, scan-vulnerabilityreport-*, *-snapshot-save-*) must
// NEVER survive into the treemap. Durable workloads (Deployment /
// StatefulSet / DaemonSet / ReplicaSet) are retained.
func TestDropEphemeralRows_FiltersJobOwnedPods(t *testing.T) {
	rows := []podRow{
		{namespace: "harbor", application: "harbor", ownerKind: "StatefulSet", cpuReq: 200},
		{namespace: "flux-system", application: "cutover-harbor-prewarm", ownerKind: "Job", cpuReq: 50},
		{namespace: "trivy-system", application: "scan-vulnerabilityreport-abc", ownerKind: "Job", cpuReq: 30},
		{namespace: "openbao", application: "openbao-snapshot-save", ownerKind: "Job", cpuReq: 20},
		{namespace: "kube-system", application: "cilium", ownerKind: "DaemonSet", cpuReq: 100},
	}
	got := dropEphemeralRows(rows)
	if len(got) != 2 {
		t.Fatalf("expected 2 durable rows after dropping Job pods, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if strings.EqualFold(r.ownerKind, "Job") {
			t.Errorf("a Job-owned pod survived: %+v", r)
		}
	}

	// Integration: a Job pod produces NO treemap cell under group_by=application.
	out := aggregateRows(dropEphemeralRows([]podRow{
		{namespace: "trivy-system", application: "scan-vulnerabilityreport-xyz", ownerKind: "Job", cpuReq: 30},
	}), []string{"application"}, "health", "cpu_request")
	if len(out.Items) != 0 {
		t.Errorf("a Job-only estate must yield zero application cells, got %d: %+v", len(out.Items), out.Items)
	}
}

/* ── #3687 fold #3692 — Organization dimension ───────────────────────── */

// mkDashNamespaceOrg produces an unstructured Namespace carrying the
// canonical `openova.io/organization` label the treemap's organization
// grouping (and the per-Org showback) read. Empty `org` omits the label
// so the host/control-plane (no-org) fallback can be exercised.
func mkDashNamespaceOrg(name, org string) *unstructured.Unstructured {
	labels := map[string]any{}
	if org != "" {
		labels["openova.io/organization"] = org
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":            name,
			"resourceVersion": "1",
			"labels":          labels,
		},
	}}
}

// TestDashboardTreemap_OrganizationFromNamespaceLabel — grouping by
// `organization` bucketises pods by the owning Organization, resolved
// from the host Namespace's `openova.io/organization` label (the single
// join key the per-Org showback uses). Pods in namespaces WITHOUT an
// org label (host/control-plane) roll into the "Platform overhead"
// bucket — visible + labelled, never mis-attributed to a tenant.
func TestDashboardTreemap_OrganizationFromNamespaceLabel(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNamespaceOrg("acme", "acme"),
		mkDashNamespaceOrg("beta", "beta"),
		mkDashNamespaceOrg("kube-system", ""), // no org label → platform overhead
		mkDashPodOnNode("acme", "p1", "blog", ""),
		mkDashPodOnNode("beta", "p2", "shop", ""),
		mkDashPodOnNode("kube-system", "p3", "cilium", ""),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=organization&color_by=health&size_by=cpu_request")
	if len(out.Items) != 3 {
		t.Fatalf("expected 3 org buckets (acme,beta,Platform overhead); got %d (%+v)",
			len(out.Items), out)
	}
	names := map[string]bool{}
	for _, it := range out.Items {
		names[it.Name] = true
	}
	for _, want := range []string{"acme", "beta", "Platform overhead"} {
		if !names[want] {
			t.Errorf("expected org bucket %q; got %v", want, names)
		}
	}
}

// TestDashboardTreemap_OrganizationDrillToApplication — the canonical
// Lane-E view: Layer-1 Organization → Layer-2 Application. A tenant's
// org cell nests its own apps; the unattributed estate nests under
// "Platform overhead".
func TestDashboardTreemap_OrganizationDrillToApplication(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashNamespaceOrg("acme", "acme"),
		mkDashPodOnNode("acme", "p1", "blog", ""),
		mkDashPodOnNode("acme", "p2", "blog", ""),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=organization,application&color_by=health&size_by=cpu_request")
	if len(out.Items) != 1 || out.Items[0].Name != "acme" {
		t.Fatalf("expected single org bucket 'acme'; got %+v", out.Items)
	}
	kids := out.Items[0].Children
	if len(kids) != 1 || kids[0].Name != "blog" {
		t.Fatalf("expected acme to nest the 'blog' application; got %+v", kids)
	}
}

// TestDimensionKey_OrganizationFallsBackToPlatformOverhead — the unit
// contract: an empty-org row keys on the synthetic platformOrg id (the
// SAME sentinel the showback uses) so the two surfaces agree on the
// unattributed estate, and renders the human-readable "Platform
// overhead" label.
func TestDimensionKey_OrganizationFallsBackToPlatformOverhead(t *testing.T) {
	id, name := dimensionKey(podRow{org: "acme"}, "organization")
	if id != "acme" || name != "acme" {
		t.Errorf("labelled org: got id=%q name=%q, want acme/acme", id, name)
	}
	id, name = dimensionKey(podRow{org: ""}, "organization")
	if id != platformOrg {
		t.Errorf("empty org: got id=%q, want platformOrg sentinel %q", id, platformOrg)
	}
	if name != "Platform overhead" {
		t.Errorf("empty org: got name=%q, want \"Platform overhead\"", name)
	}
}

/* ── #5485 defect B — Application names, not ReplicaSet names ────────── */

// mkDashOwnedPod produces an unstructured Pod carrying NO
// app.kubernetes.io/{instance,name} labels — the shape of the 31 live
// pods (org-services/, flux-system/, kube-system/coredns, metrics-server,
// nats-jetstream, openbao-unseal-reconciler, provider-opentofu) that made
// #5485 defect B operator-visible. Its application identity therefore has
// to come from the ownerRef chain. `ownerKind`/`ownerName` empty ⇒ a bare
// unowned pod.
func mkDashOwnedPod(ns, name, ownerKind, ownerName, cpuRequest string) *unstructured.Unstructured {
	meta := map[string]any{
		"namespace":         ns,
		"name":              name,
		"creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"resourceVersion":   "1",
	}
	if ownerKind != "" && ownerName != "" {
		apiVersion := "apps/v1"
		if ownerKind == "Job" {
			apiVersion = "batch/v1"
		}
		meta["ownerReferences"] = []any{map[string]any{
			"apiVersion": apiVersion,
			"kind":       ownerKind,
			"name":       ownerName,
			"uid":        "uid-" + ownerName,
			"controller": true,
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   meta,
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":      "main",
				"image":     "ghcr.io/openova-io/test:1",
				"resources": map[string]any{"requests": map[string]any{"cpu": cpuRequest}},
			}},
			"volumes": []any{},
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
}

// mkDashReplicaSet produces the intermediate Deployment→Pod hop. Empty
// `deployment` ⇒ a bare ReplicaSet with no controller of its own (the
// no-further-owner case).
func mkDashReplicaSet(ns, name, deployment string) *unstructured.Unstructured {
	meta := map[string]any{
		"namespace":       ns,
		"name":            name,
		"resourceVersion": "1",
	}
	if deployment != "" {
		meta["ownerReferences"] = []any{map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"name":       deployment,
			"uid":        "uid-" + deployment,
			"controller": true,
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata":   meta,
		"spec":       map[string]any{"replicas": int64(1)},
	}}
}

// TestApplicationKey_ResolvesDeploymentThroughReplicaSet — #5485 defect B.
// A Deployment's pods are owned by a hash-suffixed ReplicaSet; stopping at
// the pod's first ownerRef rendered `admin-865b6dd6c7` /
// `coredns-57fc96f748` / `source-controller-cc7bd674d` as "applications"
// (24 of 101 live treemap leaves). The resolver takes the second hop
// RS → Deployment, and leaves every other owner shape untouched.
func TestApplicationKey_ResolvesDeploymentThroughReplicaSet(t *testing.T) {
	rsIndex := indexReplicaSets([]*unstructured.Unstructured{
		mkDashReplicaSet("org-services", "admin-865b6dd6c7", "admin"),
		mkDashReplicaSet("kube-system", "coredns-57fc96f748", "coredns"),
		// A ReplicaSet with no controller of its own — hand-applied,
		// no Deployment above it.
		mkDashReplicaSet("edge", "bare-rs-7d9f", ""),
	})

	cases := []struct {
		name string
		pod  *unstructured.Unstructured
		want string
	}{
		{
			// THE defect: Deployment pod resolves to the Deployment.
			name: "deployment pod via replicaset hop",
			pod:  mkDashOwnedPod("org-services", "admin-865b6dd6c7-p4xkv", "ReplicaSet", "admin-865b6dd6c7", "50m"),
			want: "admin",
		},
		{
			name: "kube-system coredns via replicaset hop",
			pod:  mkDashOwnedPod("kube-system", "coredns-57fc96f748-abcde", "ReplicaSet", "coredns-57fc96f748", "100m"),
			want: "coredns",
		},
		// ── no-further-owner cases: keep a real name, invent nothing ──
		{
			name: "replicaset absent from the index keeps the replicaset name",
			pod:  mkDashOwnedPod("flux-system", "source-controller-cc7bd674d-zzz", "ReplicaSet", "source-controller-cc7bd674d", "10m"),
			want: "source-controller-cc7bd674d",
		},
		{
			name: "bare replicaset with no controller keeps the replicaset name",
			pod:  mkDashOwnedPod("edge", "bare-rs-7d9f-qqq", "ReplicaSet", "bare-rs-7d9f", "10m"),
			want: "bare-rs-7d9f",
		},
		{
			name: "daemonset pod keeps its workload name (no hop)",
			pod:  mkDashOwnedPod("kube-system", "cilium-9xk2p", "DaemonSet", "cilium", "100m"),
			want: "cilium",
		},
		{
			name: "statefulset pod keeps its workload name (no hop)",
			pod:  mkDashOwnedPod("shared-data", "shared-pg-1", "StatefulSet", "shared-pg", "200m"),
			want: "shared-pg",
		},
		{
			name: "job pod keeps its job name (no hop)",
			pod:  mkDashOwnedPod("catalyst-system", "cutover-harbor-prewarm-1785340840-tt7", "Job", "cutover-harbor-prewarm-1785340840", "50m"),
			want: "cutover-harbor-prewarm-1785340840",
		},
		{
			name: "unowned pod falls back to its own name",
			pod:  mkDashOwnedPod("edge", "static-probe", "", "", "10m"),
			want: "static-probe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applicationKey(tc.pod, rsIndex); got != tc.want {
				t.Errorf("applicationKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestApplicationKey_LabelsWinAndNeverEmpty — the vacuity control for
// defect B. A "fix" that returned "" (or dropped the ownerRef fallback)
// would satisfy "no hash-suffixed names" while destroying the grouping:
// every pod would collapse into one nameless cell. Locks that the
// chart-authoring precedence still holds and that NO input yields "".
func TestApplicationKey_LabelsWinAndNeverEmpty(t *testing.T) {
	rsIndex := indexReplicaSets([]*unstructured.Unstructured{
		mkDashReplicaSet("apps", "blog-6c7d", "blog-deploy"),
	})

	// 1. app.kubernetes.io/instance wins over the whole ownerRef chain.
	labelled := mkDashOwnedPod("apps", "blog-6c7d-aaa", "ReplicaSet", "blog-6c7d", "50m")
	labelled.SetLabels(map[string]string{"app.kubernetes.io/instance": "blog"})
	if got := applicationKey(labelled, rsIndex); got != "blog" {
		t.Errorf("instance label must win: got %q, want %q", got, "blog")
	}
	// 2. app.kubernetes.io/name is the second precedence step.
	named := mkDashOwnedPod("apps", "blog-6c7d-bbb", "ReplicaSet", "blog-6c7d", "50m")
	named.SetLabels(map[string]string{"app.kubernetes.io/name": "blog-chart"})
	if got := applicationKey(named, rsIndex); got != "blog-chart" {
		t.Errorf("name label must be second: got %q, want %q", got, "blog-chart")
	}
	// 3. NOTHING resolves to the empty string — not a nil index, not a
	//    missing ReplicaSet, not a pod with no owner at all.
	probes := map[string]*unstructured.Unstructured{
		"rs-owned, nil index":    mkDashOwnedPod("apps", "blog-6c7d-ccc", "ReplicaSet", "blog-6c7d", "50m"),
		"rs-owned, missing rs":   mkDashOwnedPod("apps", "ghost-1111-ddd", "ReplicaSet", "ghost-1111", "50m"),
		"unowned":                mkDashOwnedPod("apps", "loner", "", "", "50m"),
		"owner ref with no name": mkDashOwnedPod("apps", "nameless-owner", "", "", "50m"),
		"labelled":               labelled,
	}
	for what, p := range probes {
		if got := applicationKey(p, nil); got == "" {
			t.Errorf("%s: applicationKey returned the empty string — every pod must land in a named bucket", what)
		}
	}
	// A nil index must NOT change the pre-#5485 answer for an RS-owned
	// pod: the ReplicaSet name, never "" and never a fabricated one.
	if got := applicationKey(probes["rs-owned, nil index"], nil); got != "blog-6c7d" {
		t.Errorf("nil rs index: got %q, want the replicaset name %q", got, "blog-6c7d")
	}
}

// TestDashboardTreemap_ApplicationNamedAfterDeploymentNotReplicaSet —
// the wired proof for defect B: the handler lists ReplicaSets from the
// same cluster cache and the rendered treemap leaves carry Deployment
// names. Exercises the full HTTP path, so a fix that resolves correctly
// in applicationKey but is never threaded through buildPodRows fails here.
func TestDashboardTreemap_ApplicationNamedAfterDeploymentNotReplicaSet(t *testing.T) {
	h := newDashHandlerWithCache(t, "alpha", false,
		mkDashReplicaSet("org-services", "admin-865b6dd6c7", "admin"),
		mkDashReplicaSet("kube-system", "coredns-57fc96f748", "coredns"),
		mkDashOwnedPod("org-services", "admin-865b6dd6c7-p4xkv", "ReplicaSet", "admin-865b6dd6c7", "100m"),
		mkDashOwnedPod("org-services", "admin-865b6dd6c7-w2mnb", "ReplicaSet", "admin-865b6dd6c7", "100m"),
		mkDashOwnedPod("kube-system", "coredns-57fc96f748-abcde", "ReplicaSet", "coredns-57fc96f748", "50m"),
	)
	out := dashGet(t, h, "deployment_id=alpha&group_by=application&color_by=health&size_by=cpu_request")

	bySize := map[string]float64{}
	for _, it := range out.Items {
		bySize[it.Name] = it.SizeValue
		if strings.Contains(it.Name, "865b6dd6c7") || strings.Contains(it.Name, "57fc96f748") {
			t.Errorf("a raw ReplicaSet name rendered as an Application leaf: %q", it.Name)
		}
	}
	// Vacuity control: the leaves are still THERE, still named, and still
	// carry the summed request of their pods.
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 application leaves (admin + coredns), got %d: %+v", len(out.Items), out.Items)
	}
	if bySize["admin"] != 200 {
		t.Errorf("admin cpu_request: got %v want 200m (two pods folded under the Deployment)", bySize["admin"])
	}
	if bySize["coredns"] != 50 {
		t.Errorf("coredns cpu_request: got %v want 50m", bySize["coredns"])
	}
}
