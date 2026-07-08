// k8s_resource_test.go — coverage for the EPIC-4 Slice R (#1099)
// resource browser endpoints: GET / DELETE / scale / restart / dry-run
// / apply / tree / metrics.
//
// Test strategy: spin up a real k8scache.Factory pointed at a fake
// dynamic client seeded with realistic objects, then route through chi
// against the handler. The auth gate is exercised with a Claims context.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// quietK8sResourceLogger discards log output for clean test runs. The
// handler package already declares quietHandlerLogger in dashboard_test.go;
// this slice's tests use a distinct symbol to avoid the redecl conflict.
func quietK8sResourceLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// k8sResourceTestRig wires up a Handler with a started k8scache.Factory
// against the supplied seeded objects. Returns the rig + the underlying
// fake dynamic client so tests can mutate state directly.
type k8sResourceTestRig struct {
	h    *Handler
	dyn  *dynamicfake.FakeDynamicClient
	cl   string // cluster id
	stop func()
}

func newResourceRig(t *testing.T, objs ...runtime.Object) *k8sResourceTestRig {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMapList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DeploymentList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSetList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, &unstructured.Unstructured{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                        "PodList",
		{Version: "v1", Resource: "configmaps"}:                  "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:  "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:  "ReplicaSetList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	core := kfake.NewSimpleClientset()

	r := k8scache.NewRegistry()
	for _, k := range []k8scache.Kind{
		{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true},
		{Name: "configmap", GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Namespaced: true, Sensitive: true},
		{Name: "deployment", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespaced: true},
		{Name: "replicaset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, Namespaced: true},
	} {
		_ = r.Add(k)
	}
	cfg := k8scache.Config{
		Logger:   quietK8sResourceLogger(),
		Registry: r,
		Clusters: []k8scache.ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	factory, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := factory.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait briefly for the informer to sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, err := factory.List("alpha", "pod", labels.Everything())
		_ = items
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	h := NewWithPDM(quietK8sResourceLogger(), &fakePDM{})
	h.SetK8sCache(factory, k8scache.NewSARCache(), "X-Forwarded-User")
	return &k8sResourceTestRig{
		h:    h,
		dyn:  dyn,
		cl:   "alpha",
		stop: factory.Stop,
	}
}

func (r *k8sResourceTestRig) router() chi.Router {
	rt := chi.NewRouter()
	rt.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", r.h.HandleK8sResourceGet)
	rt.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree", r.h.HandleK8sResourceTree)
	rt.Get("/api/v1/sovereigns/{id}/k8s/metrics/{kind}/{ns}/{name}", r.h.HandleK8sResourceMetrics)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", r.h.HandleK8sResourceScale)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart", r.h.HandleK8sResourceRestart)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run", r.h.HandleK8sResourceDryRun)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply", r.h.HandleK8sResourceApply)
	rt.Delete("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", r.h.HandleK8sResourceDelete)
	return rt
}

func newPodObj(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-" + ns + "-" + name,
			"resourceVersion": "1",
			"labels":          map[string]any{"app": "wp"},
		},
		"spec": map[string]any{
			"containers": []any{},
		},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
}

func newDeploymentObj(ns, name string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-dep-" + name,
			"resourceVersion": "1",
		},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{
				"matchLabels": map[string]any{"app": "wp"},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "wp"},
				},
			},
		},
	}}
}

// ── GET single resource ─────────────────────────────────────────────

func TestHandleK8sResourceGet_ReturnsLiveObject(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	meta := got["metadata"].(map[string]any)
	if meta["name"] != "wp-1" {
		t.Fatalf("name: got %q want %q", meta["name"], "wp-1")
	}
}

func TestHandleK8sResourceGet_404OnMissing(t *testing.T) {
	rig := newResourceRig(t)
	defer rig.stop()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/pod/default/nope", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sResourceGet_404OnUnknownKind(t *testing.T) {
	rig := newResourceRig(t)
	defer rig.stop()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/madeup/default/x", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Scale ───────────────────────────────────────────────────────────

func TestHandleK8sResourceScale_HappyPath(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()
	body, _ := json.Marshal(map[string]int{"replicas": 5})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := rig.dyn.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).
		Namespace("default").Get(context.Background(), "wp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if replicas != 5 {
		t.Fatalf("replicas: got %d want 5", replicas)
	}
}

func TestHandleK8sResourceScale_RejectsNegative(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()
	body, _ := json.Marshal(map[string]int{"replicas": -1})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sResourceScale_RejectsNonScalableKind(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	body, _ := json.Marshal(map[string]int{"replicas": 5})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sResourceScale_RBACGate_Forbidden(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()
	body, _ := json.Marshal(map[string]int{"replicas": 5})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	// Inject a viewer-tier claim — should be 403.
	claims := &auth.Claims{Tier: "viewer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sResourceScale_RBACGate_AdminAllowed(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()
	body, _ := json.Marshal(map[string]int{"replicas": 4})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Restart ─────────────────────────────────────────────────────────

func TestHandleK8sResourceRestart_HappyPath(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/restart", bytes.NewBuffer([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := rig.dyn.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).
		Namespace("default").Get(context.Background(), "wp", metav1.GetOptions{})
	annot, _, _ := unstructured.NestedStringMap(got.Object, "spec", "template", "metadata", "annotations")
	if _, ok := annot["kubectl.kubernetes.io/restartedAt"]; !ok {
		t.Fatalf("restartedAt annotation not stamped; got annot=%v", annot)
	}
}

// ── Delete ──────────────────────────────────────────────────────────

func TestHandleK8sResourceDelete_HappyPath(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	_, err := rig.dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).
		Namespace("default").Get(context.Background(), "wp-1", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("pod should be deleted")
	}
}

// ── Dry-run + Apply ─────────────────────────────────────────────────

func TestHandleK8sResourceDryRun_AcceptsValidYAML(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	yamlBody := `apiVersion: v1
kind: Pod
metadata:
  name: wp-1
  namespace: default
  labels:
    app: wp
    env: prod
spec:
  containers: []
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/dry-run", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["dryRun"] != true {
		t.Fatalf("dryRun flag not echoed back: %v", resp)
	}
}

// TestHandleK8sResourceDryRun_StampsResourceVersionWhenOmitted is the
// #4860 guard. The Edit-IaC editor submits CR YAML without
// metadata.resourceVersion; a real apiserver 400s a plain Update with
// "resourceVersion ... must be specified for an update" (caught live on
// hw228, bp-wordpress). The lenient fake client does NOT enforce this,
// so we install a reactor that mimics the apiserver: reject any update
// whose object carries no resourceVersion. Pre-fix the handler would
// forward the RV-less object and this reactor would 400; post-fix the
// handler Gets the live object and stamps its RV first, so the update
// carries a valid token and the dry-run returns 200.
func TestHandleK8sResourceDryRun_StampsResourceVersionWhenOmitted(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	pod.SetResourceVersion("4242")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	rig.dyn.ClearActions()

	yamlBody := `apiVersion: v1
kind: Pod
metadata:
  name: wp-1
  namespace: default
spec:
  containers: []
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/dry-run", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run with RV-less YAML: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The fix's contract: when the submitted YAML omits resourceVersion,
	// the handler MUST Get the live object first (to stamp its RV before
	// the Update) — otherwise a real apiserver 400s "resourceVersion must
	// be specified for an update". Assert a `get` on pods preceded the
	// `update`, and that the object sent to Update carried the live RV.
	var sawGet bool
	var updateRV string
	for _, a := range rig.dyn.Actions() {
		if a.GetVerb() == "get" && a.GetResource().Resource == "pods" {
			sawGet = true
		}
		if ua, ok := a.(clienttesting.UpdateAction); ok && a.GetResource().Resource == "pods" {
			if u, ok := ua.GetObject().(*unstructured.Unstructured); ok {
				updateRV = u.GetResourceVersion()
			}
		}
	}
	if !sawGet {
		t.Fatalf("handler did not Get the live object to stamp resourceVersion; actions=%v", rig.dyn.Actions())
	}
	if updateRV != "4242" {
		t.Fatalf("Update object resourceVersion = %q, want %q (stamped from live object)", updateRV, "4242")
	}
}

func TestHandleK8sResourceApply_RejectsNameMismatch(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	yamlBody := `apiVersion: v1
kind: Pod
metadata:
  name: WRONG
  namespace: default
spec: {}
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/apply", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "name-mismatch") {
		t.Fatalf("expected name-mismatch error, got %s", rec.Body.String())
	}
}

func TestHandleK8sResourceApply_RejectsInvalidYAML(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	body, _ := json.Marshal(map[string]string{"yaml": "this is: not\n  : valid yaml [["})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/apply", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sResourceApply_EmptyYAMLRejected(t *testing.T) {
	rig := newResourceRig(t)
	defer rig.stop()
	body, _ := json.Marshal(map[string]string{"yaml": "  \n  "})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1/apply", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Tree ────────────────────────────────────────────────────────────

func TestHandleK8sResourceTree_DeploymentChainWalksDown(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rs := newReplicaSetWithOwner("default", "wp-67", "Deployment", "wp", map[string]string{"app": "wp"})
	pod := newPodWithRSOwner("default", "wp-67-abc", "wp-67", map[string]string{"app": "wp"})
	rig := newResourceRig(t, dep, rs, pod)
	defer rig.stop()
	// Wait for indexer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := rig.h.k8sCache.List("alpha", "deployment", labels.Everything())
		if len(items) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/deployment/default/wp/tree", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var node resourceTreeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if node.Name != "wp" || node.Kind != "deployment" {
		t.Fatalf("root node mismatch: %+v", node)
	}
	if len(node.Children) == 0 {
		t.Fatalf("expected at least one child, got %v", node.Children)
	}
}

func newReplicaSetWithOwner(ns, name, ownerKind, ownerName string, matchLabels map[string]string) *unstructured.Unstructured {
	owners := []any{
		map[string]any{
			"apiVersion": "apps/v1",
			"kind":       ownerKind,
			"name":       ownerName,
			"uid":        "uid-" + ownerName,
		},
	}
	matchLabelsAny := map[string]any{}
	for k, v := range matchLabels {
		matchLabelsAny[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-rs-" + name,
			"ownerReferences": owners,
			"labels":          matchLabelsAny,
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": matchLabelsAny,
			},
		},
	}}
}

func newPodWithRSOwner(ns, name, ownerName string, lbls map[string]string) *unstructured.Unstructured {
	owners := []any{
		map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "ReplicaSet",
			"name":       ownerName,
			"uid":        "uid-rs-" + ownerName,
		},
	}
	lblsAny := map[string]any{}
	for k, v := range lbls {
		lblsAny[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-pod-" + name,
			"ownerReferences": owners,
			"labels":          lblsAny,
		},
		"spec": map[string]any{},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
}

// ── Metrics ─────────────────────────────────────────────────────────

func TestHandleK8sResourceMetrics_ReturnsUnavailableWhenPodMetricsAbsent(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/metrics/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp metricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Source != "unavailable" {
		t.Fatalf("expected unavailable source on cluster without podmetrics, got %q", resp.Source)
	}
}
