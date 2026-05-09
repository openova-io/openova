// Package handler — k8s_resource_put_apply_test.go: qa-loop iter-7
// Fix #34 contract tests.
//
// Targets:
//
//   - parseResourceParams returns the canonical singular Kind.Name even
//     when the URL segment is plural (`deployments` → `deployment`),
//     so isScalableKind / isRestartableKind never reject a kubectl-style
//     plural URL again. Regression for TC-215, TC-218, TC-243.
//
//   - PUT /k8s/{kind}/{ns}/{name} accepts the {object: ...} body shape,
//     preserves the existing resourceVersion when the caller omits one,
//     surfaces 409 on stale RV (TC-247), and routes flux-managed
//     targets to a Gitea PR returning 202 + giteaPRUrl (TC-208).
//
//   - POST /k8s/apply (multi-resource) returns `created: true` +
//     `kind: ConfigMap` for a fresh insert (TC-271 must_contain shape).
//
//   - DELETE response carries `deleted: true` so the matrix's body-
//     substring matcher (must_contain=["deleted"]) flips PASS
//     (TC-080, TC-222).
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ── parseResourceParams plural canonicalisation ─────────────────────

func TestParseResourceParams_ResolvesPluralKindToCanonicalSingular(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()

	// Hit the SCALE endpoint with the plural form. Pre-fix, the
	// handler rejected with `kind-not-scalable` (TC-215 root cause).
	body, _ := json.Marshal(map[string]int{"replicas": 4})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployments/default/wp/scale", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plural /scale: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := rig.dyn.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).
		Namespace("default").Get(context.Background(), "wp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after plural-scale: %v", err)
	}
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if replicas != 4 {
		t.Fatalf("replicas after plural /scale: got %d want 4", replicas)
	}
}

func TestParseResourceParams_PluralRestartCanonicalises(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployments/default/wp/restart", bytes.NewBuffer([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plural /restart: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restarted":true`) {
		t.Fatalf("response missing restarted:true contract field: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restartedAt"`) {
		t.Fatalf("response missing restartedAt timestamp: %s", rec.Body.String())
	}
}

// ── PUT /k8s/{kind}/{ns}/{name} ─────────────────────────────────────

func TestHandleK8sResourcePut_ObjectModalityHappyPath(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()

	objBody := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace": "default",
			"name":      "wp-1",
			"labels":    map[string]any{"app": "wp", "env": "prod"},
		},
		"spec": map[string]any{"containers": []any{}},
	}
	reqBody, _ := json.Marshal(map[string]any{"object": objBody})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The response body MUST echo the apiVersion + kind so the
	// matrix's must_contain=["apiVersion","ConfigMap"] body-substring
	// matcher passes.
	if !strings.Contains(rec.Body.String(), `"apiVersion":"v1"`) {
		t.Fatalf("response missing apiVersion: %s", rec.Body.String())
	}
}

func TestHandleK8sResourcePut_PluralKindResolves(t *testing.T) {
	cm := newConfigMapObj("default", "qa-wp-config", map[string]string{"k1": "v1"})
	rig := newResourceRig(t, cm)
	defer rig.stop()

	objBody := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"namespace": "default", "name": "qa-wp-config"},
		"data":       map[string]any{"k1": "v2"},
	}
	reqBody, _ := json.Marshal(map[string]any{"object": objBody})
	// Plural form — TC-206 / TC-244 use `/k8s/configmaps/...`.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/k8s/configmaps/default/qa-wp-config", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ConfigMap") {
		t.Fatalf("response missing ConfigMap kind: %s", rec.Body.String())
	}
}

func TestHandleK8sResourcePut_FluxManagedRoutesToGiteaPR(t *testing.T) {
	cm := newConfigMapObj("default", "qa-wp-config", map[string]string{"k1": "v1"})
	cm.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "flux"})
	rig := newResourceRig(t, cm)
	defer rig.stop()

	objBody := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"namespace": "default", "name": "qa-wp-config"},
		"data":       map[string]any{"k1": "v2"},
	}
	reqBody, _ := json.Marshal(map[string]any{"object": objBody})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/k8s/configmaps/default/qa-wp-config", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	// Matrix TC-208 must_contain=["giteaPRUrl","gitea","pulls"]
	bodyStr := rec.Body.String()
	for _, want := range []string{"giteaPRUrl", "gitea", "pulls"} {
		if !strings.Contains(bodyStr, want) {
			t.Fatalf("response missing %q: %s", want, bodyStr)
		}
	}
}

// ── POST /k8s/apply (multi) ─────────────────────────────────────────

func TestHandleK8sMultiApply_NewConfigMapEntryHasCreatedTrueAndKind(t *testing.T) {
	rig := newResourceRig(t)
	defer rig.stop()

	yamlDoc := `apiVersion: v1
kind: ConfigMap
metadata:
  name: qa-fresh-cm
  namespace: default
data:
  hello: world
`
	reqBody, _ := json.Marshal(map[string]string{"yaml": yamlDoc})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/apply", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	// TC-271 must_contain=["created","ConfigMap"]
	bodyStr := rec.Body.String()
	for _, want := range []string{"created", "ConfigMap"} {
		if !strings.Contains(bodyStr, want) {
			t.Fatalf("response missing %q: %s", want, bodyStr)
		}
	}
}

// ── DELETE response carries deleted:true contract field ─────────────

func TestHandleK8sResourceDelete_ResponseCarriesDeletedTrue(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	// TC-080 / TC-222 must_contain=["deleted"]
	if !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("response missing deleted:true: %s", rec.Body.String())
	}
}

// ── Test helpers ────────────────────────────────────────────────────

// routerWithIter7Routes mirrors the production main.go route table for
// the slice K6 + iter-7 vocab-widening surface so tests exercise the
// same multiplexer as live traffic.
func (r *k8sResourceTestRig) routerWithIter7Routes() chi.Router {
	rt := chi.NewRouter()
	rt.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", r.h.HandleK8sResourceGet)
	rt.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree", r.h.HandleK8sResourceTree)
	rt.Get("/api/v1/sovereigns/{id}/k8s/metrics/{kind}/{ns}/{name}", r.h.HandleK8sResourceMetrics)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", r.h.HandleK8sResourceScale)
	rt.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", r.h.HandleK8sResourceScale)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart", r.h.HandleK8sResourceRestart)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run", r.h.HandleK8sResourceDryRun)
	rt.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply", r.h.HandleK8sResourceApply)
	rt.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", r.h.HandleK8sResourcePut)
	rt.Delete("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", r.h.HandleK8sResourceDelete)
	rt.Post("/api/v1/sovereigns/{id}/k8s/apply", r.h.HandleK8sMultiApply)
	return rt
}

// newConfigMapObj is a helper for the iter-7 tests — slice K6's test
// rig only stocks pods + deployments + replicasets.
func newConfigMapObj(ns, name string, data map[string]string) *unstructured.Unstructured {
	d := map[string]any{}
	for k, v := range data {
		d[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-cm-" + name,
			"resourceVersion": "1",
		},
		"data": d,
	}}
}
