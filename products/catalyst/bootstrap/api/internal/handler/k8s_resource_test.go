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
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	return newResourceRigWithPrep(t, nil, objs...)
}

// newResourceRigWithPrep is newResourceRig plus a hook that runs against
// the fake dynamic client BEFORE the k8scache informer factory starts.
//
// This ordering is load-bearing for tests that install a PrependReactor:
// client-go's fake `PrependReactor` mutates `Fake.ReactionChain` WITHOUT
// taking the fake's mutex (testing/fake.go), whereas the background
// informer's reflector reads that same chain under the lock via
// `Fake.Invokes` on every List/Watch. Installing the reactor after
// `factory.Start` therefore data-races the reflector goroutine. Running
// the prep here — before any reflector goroutine exists — means the
// reaction chain is fully built by the time concurrent Invokes begin, and
// every subsequent access is serialized under `Fake.Lock`.
func newResourceRigWithPrep(t *testing.T, prep func(*dynamicfake.FakeDynamicClient), objs ...runtime.Object) *k8sResourceTestRig {
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
	// Blueprint is a CLUSTER-scoped catalyst CRD (products/catalyst/chart/crds
	// /blueprint.yaml: scope: Cluster). Registered here so the #4896 Edit-IaC
	// dry-run test can route through the real handler against a seeded CR.
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "catalyst.openova.io", Version: "v1", Kind: "BlueprintList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint"}, &unstructured.Unstructured{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                                     "PodList",
		{Version: "v1", Resource: "configmaps"}:                               "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:               "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:               "ReplicaSetList",
		{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}: "BlueprintList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	// Install any reactors BEFORE the informer factory starts — see the
	// doc comment on newResourceRigWithPrep for why the ordering matters.
	if prep != nil {
		prep(dyn)
	}
	core := kfake.NewSimpleClientset()

	r := k8scache.NewRegistry()
	for _, k := range []k8scache.Kind{
		{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true},
		{Name: "configmap", GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Namespaced: true, Sensitive: true},
		{Name: "deployment", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespaced: true},
		{Name: "replicaset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, Namespaced: true},
		// Mirrors products/catalyst/bootstrap/api/internal/k8scache/kinds.go
		// so the #4896 Edit-IaC dry-run routes resolve the Blueprint kind.
		// Cluster-scoped (Namespaced:false) to match the served CRD
		// (blueprint.yaml scope: Cluster) — see #4860.
		{Name: "blueprint", GVR: schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}, Namespaced: false},
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

// newBlueprintObj builds a cluster-scoped Blueprint CR named with the
// canonical `bp-` prefix (e.g. "bp-alloy"), as the chart's catalog-seed
// ships it. No metadata.namespace — the CRD is cluster-scoped.
func newBlueprintObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalyst.openova.io/v1",
		"kind":       "Blueprint",
		"metadata": map[string]any{
			"name":            name,
			"uid":             "uid-bp-" + name,
			"resourceVersion": "7",
		},
		"spec": map[string]any{
			"version": "1.0.2",
			"card":    map[string]any{"title": "Alloy"},
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

// TestReconcileBlueprintBareName is the #4896 unit contract for the
// bp-prefix name reconciliation used by the Edit-IaC dry-run/apply guard.
func TestReconcileBlueprintBareName(t *testing.T) {
	mk := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "catalyst.openova.io/v1",
			"kind":       "Blueprint",
			"metadata":   map[string]any{"name": name},
		}}
	}
	// Authored bare slug vs canonical bp-prefixed URL → SAME identity:
	// reconciled, and parsed.name stamped to the URL form so the k8s
	// Update targets the real bp-<slug> CR.
	p := mk("alloy")
	if !reconcileBlueprintBareName(p, "blueprint", "bp-alloy") {
		t.Fatalf("bare 'alloy' vs URL 'bp-alloy' should reconcile")
	}
	if p.GetName() != "bp-alloy" {
		t.Fatalf("parsed name not stamped: got %q want %q", p.GetName(), "bp-alloy")
	}
	// A genuine rename (bare) must NOT reconcile — the guard still fires.
	if reconcileBlueprintBareName(mk("loki"), "blueprint", "bp-alloy") {
		t.Fatalf("genuine rename 'loki' vs 'bp-alloy' must not reconcile")
	}
	// A genuine rename between two prefixed names must NOT reconcile.
	if reconcileBlueprintBareName(mk("bp-loki"), "blueprint", "bp-alloy") {
		t.Fatalf("prefixed rename 'bp-loki' vs 'bp-alloy' must not reconcile")
	}
	// Non-Blueprint kinds are never bp-reconciled (guard fully preserved).
	if reconcileBlueprintBareName(mk("alloy"), "configmap", "bp-alloy") {
		t.Fatalf("non-blueprint kind must not reconcile")
	}
}

// TestHandleK8sResourceDryRun_BlueprintBareNameAgainstBpPrefixedURL is the
// #4896 regression: the catalog Edit-IaC editor seeds the AUTHORED
// blueprint.yaml (metadata.name = bare "alloy", no resourceVersion) and
// dry-runs it against the in-cluster CR "bp-alloy" (URL name). Before the
// fix this 400'd with "metadata.name='alloy' does not match URL
// name='bp-alloy'"; now the bp-prefix is reconciled and the dry-run
// returns 200 against the seeded CR.
func TestHandleK8sResourceDryRun_BlueprintBareNameAgainstBpPrefixedURL(t *testing.T) {
	bp := newBlueprintObj("bp-alloy")
	rig := newResourceRig(t, bp)
	defer rig.stop()

	// Exactly what the YamlEditor submits: the bare-named authored source.
	yamlBody := `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: alloy
spec:
  version: 1.0.2
  card:
    title: Alloy
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	// URL kind segment is "Blueprint" (as the console sends it) → canonicalised
	// to the registry "blueprint"; ns segment "_" → cluster-scoped.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/Blueprint/_/bp-alloy/dry-run", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blueprint bare-name dry-run: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["dryRun"] != true {
		t.Fatalf("dryRun flag not echoed: %v", resp)
	}
	if resp["name"] != "bp-alloy" {
		t.Fatalf("response name: got %v want bp-alloy (Update must target the prefixed CR)", resp["name"])
	}
}

// TestHandleK8sResourceApply_BlueprintGenuineRenameStillRejected proves the
// #4896 fix does NOT weaken the guard: renaming the Blueprint in the editor
// (bp-alloy URL, but metadata.name = "loki") still 400s name-mismatch.
func TestHandleK8sResourceApply_BlueprintGenuineRenameStillRejected(t *testing.T) {
	bp := newBlueprintObj("bp-alloy")
	rig := newResourceRig(t, bp)
	defer rig.stop()

	yamlBody := `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: loki
spec:
  version: 1.0.2
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/Blueprint/_/bp-alloy/apply", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("genuine blueprint rename: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "name-mismatch") {
		t.Fatalf("expected name-mismatch error, got %s", rec.Body.String())
	}
}

// blueprintsGR is the GroupResource the fake apiserver reactors below
// stamp onto the synthetic Forbidden errors, mirroring what a real
// kube-apiserver returns when catalyst-api's ServiceAccount lacks the
// verb on catalyst.openova.io/blueprints.
var blueprintsGR = schema.GroupResource{Group: "catalyst.openova.io", Resource: "blueprints"}

// TestHandleK8sResourceDryRun_ForbiddenUpdateMapsTo403 is the #4860
// regression: an apiserver RBAC denial on the dry-run Update MUST surface
// as 403 (a clear client/permission error), never as 500. This is the
// exact "Validate(dry-run) → HTTP 500" symptom seen live on hw228 before
// #4862 granted catalyst-api update/patch on blueprints — the handler's
// error switch was missing the IsForbidden case, so a Forbidden fell
// through to the resource-update-failed 500 default. The scope-mismatch
// (Blueprint registered Namespaced while the CRD is cluster-scoped) is
// fixed alongside so the cluster-scoped Update routes correctly.
func TestHandleK8sResourceDryRun_ForbiddenUpdateMapsTo403(t *testing.T) {
	bp := newBlueprintObj("bp-alloy")
	// Let the RV-block Get succeed (object is seeded) but deny the Update,
	// exactly as an under-privileged SA would experience it. The reactor is
	// installed pre-start (via the prep hook) so it does not race the
	// informer's reflector — see newResourceRigWithPrep.
	rig := newResourceRigWithPrep(t, func(dyn *dynamicfake.FakeDynamicClient) {
		dyn.PrependReactor("update", "blueprints", func(action clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(blueprintsGR, "bp-alloy",
				errors.New(`blueprints.catalyst.openova.io "bp-alloy" is forbidden: `+
					`User "system:serviceaccount:catalyst:catalyst-api" cannot update resource "blueprints"`))
		})
	}, bp)
	defer rig.stop()

	yamlBody := `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-alloy
spec:
  version: 1.0.3
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/Blueprint/_/bp-alloy/dry-run", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden Update: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "apiserver-forbidden") {
		t.Fatalf("expected apiserver-forbidden envelope, got %s", rec.Body.String())
	}
}

// TestHandleK8sResourceApply_ForbiddenGetMapsTo403 covers the sibling
// error site: the resourceVersion pre-fetch Get (run when the submitted
// YAML omits metadata.resourceVersion, as the Edit-IaC editor always
// does) hitting an RBAC denial. Before #4860 this returned
// resource-get-failed 500; it must now surface 403.
func TestHandleK8sResourceApply_ForbiddenGetMapsTo403(t *testing.T) {
	bp := newBlueprintObj("bp-alloy")
	// Reactor installed pre-start so it does not race the informer's
	// reflector List/Watch — see newResourceRigWithPrep.
	rig := newResourceRigWithPrep(t, func(dyn *dynamicfake.FakeDynamicClient) {
		dyn.PrependReactor("get", "blueprints", func(action clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(blueprintsGR, "bp-alloy",
				errors.New(`blueprints.catalyst.openova.io "bp-alloy" is forbidden: `+
					`User "system:serviceaccount:catalyst:catalyst-api" cannot get resource "blueprints"`))
		})
	}, bp)
	defer rig.stop()

	// YAML deliberately omits resourceVersion → the handler runs the
	// RV-block Get, which the reactor denies.
	yamlBody := `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-alloy
spec:
  version: 1.0.3
`
	body, _ := json.Marshal(map[string]string{"yaml": yamlBody})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/Blueprint/_/bp-alloy/apply", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden Get: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "apiserver-forbidden") {
		t.Fatalf("expected apiserver-forbidden envelope, got %s", rec.Body.String())
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
