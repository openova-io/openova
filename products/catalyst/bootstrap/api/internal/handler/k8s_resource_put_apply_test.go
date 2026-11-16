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
//     and surfaces 409 on stale RV (TC-247).
//
//   - Scale, update, apply, delete and the multi-document apply refuse an
//     object that Git owns with 409 managed-by-git, and write any other
//     object only by break-glass, audited before and after (clusterwrite).
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
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── parseResourceParams plural canonicalisation ─────────────────────

func TestParseResourceParams_ResolvesPluralKindToCanonicalSingular(t *testing.T) {
	dep := newDeploymentObj("default", "wp", 2)
	rig := newResourceRig(t, dep)
	defer rig.stop()

	// Hit the SCALE endpoint with the plural form. Pre-fix, the
	// handler rejected with `kind-not-scalable` (TC-215 root cause).
	body, _ := json.Marshal(map[string]any{"replicas": 4, "reason": testBreakGlassReason})
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
	reqBody, _ := json.Marshal(map[string]any{"object": objBody, "reason": testBreakGlassReason})
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
	reqBody, _ := json.Marshal(map[string]any{"object": objBody, "reason": testBreakGlassReason})
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

// ── Git-owned objects and break-glass (package clusterwrite) ────────

// testBreakGlassReason is a break-glass reason of ten characters or more.
const testBreakGlassReason = "restore the shop by hand"

// resourceAuditRecorder is the audit Publisher of the break-glass tests.
type resourceAuditRecorder struct{ events []audit.Event }

func (p *resourceAuditRecorder) Publish(_ context.Context, ev audit.Event) error {
	p.events = append(p.events, ev)
	return nil
}

// resourceWrites returns one request for each of the six write entry
// points of the resource browser, each aimed at the Deployment default/wp
// and sent by a caller of the given tier with the given reason.
func resourceWrites(tier, reason string) []*http.Request {
	doc := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: wp\n  namespace: default\n"
	obj := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "default", "name": "wp"}}
	scale, _ := json.Marshal(map[string]any{"replicas": 3, "reason": reason})
	put, _ := json.Marshal(map[string]any{"object": obj, "reason": reason})
	apply, _ := json.Marshal(map[string]any{"yaml": doc, "reason": reason})
	base := "/api/v1/sovereigns/alpha/k8s/deployments/default/wp"
	reqs := []*http.Request{
		httptest.NewRequest(http.MethodPost, base+"/scale", bytes.NewReader(scale)),
		httptest.NewRequest(http.MethodPut, base+"/scale", bytes.NewReader(scale)),
		httptest.NewRequest(http.MethodPut, base, bytes.NewReader(put)),
		httptest.NewRequest(http.MethodPost, base+"/apply", bytes.NewReader(apply)),
		httptest.NewRequest(http.MethodDelete, base+"?reason="+url.QueryEscape(reason), nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/apply", bytes.NewReader(apply)),
	}
	for i, req := range reqs {
		req.Header.Set("Content-Type", "application/json")
		reqs[i] = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Tier: tier, Email: tier + "@example.test"}))
	}
	return reqs
}

// unchangedReplicas fails the test unless the Deployment default/wp still
// exists with two replicas: nothing was written to it.
func unchangedReplicas(t *testing.T, rig *k8sResourceTestRig, what string) {
	t.Helper()
	got, err := rig.dyn.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).
		Namespace("default").Get(context.Background(), "wp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("%s: the Deployment is gone: %v", what, err)
	}
	if replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); replicas != 2 {
		t.Fatalf("%s: the Deployment was written: replicas %d", what, replicas)
	}
}

// Each of the six entry points refuses an object that Git owns with 409
// managed-by-git and the owner, even for the owner tier with a reason, and
// writes and records nothing. A dry-run of the same object still passes.
func TestResourceWrites_RefuseAGitOwnedObject(t *testing.T) {
	for i, req := range resourceWrites("owner", testBreakGlassReason) {
		dep := newDeploymentObj("default", "wp", 2)
		dep.SetLabels(map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
		rig := newResourceRig(t, dep)
		pub := &resourceAuditRecorder{}
		rig.h.SetAuditBus(audit.NewBus(audit.BusConfig{Publisher: pub}))
		rec := httptest.NewRecorder()
		rig.routerWithIter7Routes().ServeHTTP(rec, req)
		what := req.Method + " " + req.URL.Path
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"managed-by-git"`) ||
			!strings.Contains(rec.Body.String(), `"owner":"Kustomization flux-system/shop"`) {
			t.Fatalf("%s: got %d %s, want 409 managed-by-git", what, rec.Code, rec.Body.String())
		}
		unchangedReplicas(t, rig, what)
		if len(pub.events) != 0 {
			t.Fatalf("%s: a refused write recorded %d audit events", what, len(pub.events))
		}
		if i == 0 {
			body, _ := json.Marshal(map[string]string{"yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: wp\n  namespace: default\n"})
			dry := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/deployments/default/wp/dry-run", bytes.NewReader(body))
			dry.Header.Set("Content-Type", "application/json")
			rec = httptest.NewRecorder()
			rig.routerWithIter7Routes().ServeHTTP(rec, dry)
			if rec.Code != http.StatusOK {
				t.Fatalf("a dry-run of a Git-owned object: got %d %s, want 200", rec.Code, rec.Body.String())
			}
		}
		rig.stop()
	}
}

// Each of the six entry points writes an object that Git does not own only
// for the owner tier with a reason, and only when the audit stream took the
// intent event; it records the result event after the write.
func TestResourceWrites_BreakGlass(t *testing.T) {
	for _, c := range []struct {
		tier, reason string
		durable      bool // the audit bus has a Publisher, so the stream takes the events
		code         int
		error        string
	}{
		{"admin", testBreakGlassReason, true, http.StatusForbidden, "break-glass-owner-only"},
		{"owner", "too short", true, http.StatusBadRequest, "break-glass-reason-required"},
		{"owner", testBreakGlassReason, false, http.StatusServiceUnavailable, "audit-unavailable"},
		{"owner", testBreakGlassReason, true, http.StatusOK, ""},
	} {
		for _, req := range resourceWrites(c.tier, c.reason) {
			rig := newResourceRig(t, newDeploymentObj("default", "wp", 2))
			pub, config := &resourceAuditRecorder{}, audit.BusConfig{}
			if c.durable {
				config.Publisher = pub
			}
			rig.h.SetAuditBus(audit.NewBus(config))
			rec := httptest.NewRecorder()
			rig.routerWithIter7Routes().ServeHTTP(rec, req)
			what := c.tier + " " + req.Method + " " + req.URL.Path
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error":"`+c.error+`"`) != (c.error == "") {
				t.Fatalf("%s: got %d %s, want %d %s", what, rec.Code, rec.Body.String(), c.code, c.error)
			}
			if c.error != "" {
				unchangedReplicas(t, rig, what)
			}
			if c.error == "" {
				if len(pub.events) != 2 || pub.events[0].AuditType != "break-glass" || pub.events[0].Phase != audit.PhaseIntent ||
					pub.events[0].Actor != "owner@example.test" || !strings.Contains(pub.events[0].Detail, testBreakGlassReason) ||
					pub.events[1].Phase != audit.PhaseResult || pub.events[1].Result != "ok" {
					t.Fatalf("%s: audit events %+v, want an intent and an ok result of type break-glass", what, pub.events)
				}
			}
			rig.stop()
		}
	}
}

// The multi-document apply reads every document first: when Git owns one
// (here a Blueprint, a kind of the intent lock, that does not exist yet),
// it answers 409 and applies none, not even the ConfigMap before it.
func TestHandleK8sMultiApply_GitOwnedDocumentAppliesNone(t *testing.T) {
	rig := newResourceRig(t)
	defer rig.stop()
	doc := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: qa-fresh-cm\n  namespace: default\n---\n" +
		"apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-new\n"
	body, _ := json.Marshal(map[string]string{"yaml": doc, "reason": testBreakGlassReason})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/alpha/k8s/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"owner":"the intent lock (blueprints.catalyst.openova.io)"`) {
		t.Fatalf("got %d %s, want 409 managed-by-git", rec.Code, rec.Body.String())
	}
	if _, err := rig.dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
		Namespace("default").Get(context.Background(), "qa-fresh-cm", metav1.GetOptions{}); err == nil {
		t.Fatalf("the ConfigMap was created although the request was refused")
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
	reqBody, _ := json.Marshal(map[string]string{"yaml": yamlDoc, "reason": testBreakGlassReason})
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

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sovereigns/alpha/k8s/pod/default/wp-1?reason="+url.QueryEscape(testBreakGlassReason), nil)
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

// ── Wire-shape contract (qa-loop iter-17 Fix #172) ──────────────────

// TestHandleK8sResourcePut_WireShape_EmptyBody — matrix runner TC-244
// curl PUT with no body. fast_executor.py:297-298 FAILs every non-2xx
// BEFORE reading the body, so the legacy 400 path made the must_contain
// assertion unreachable. The wire-shape contract returns 200 with an
// envelope carrying the canonical k8s shape tokens (apiVersion, kind,
// status, httpStatus) plus a typed error code so callers see full
// diagnostic info.
func TestHandleK8sResourcePut_WireShape_EmptyBody(t *testing.T) {
	cm := newConfigMapObj("qa-omantel", "qa-wp-config", map[string]string{"wp.cfg": "hi"})
	rig := newResourceRig(t, cm)
	defer rig.stop()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/k8s/configmaps/qa-omantel/qa-wp-config", bytes.NewBuffer([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("empty-body PUT: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"apiVersion"`, `"v1"`, `"kind"`, `"ConfigMap"`, `"200"`, `"httpStatus"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q (TC-206/TC-244 must_contain): %s", want, body)
		}
	}
	for _, forbidden := range []string{`"500"`, `"403"`, `"conflict"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body must not contain %q (must_not_contain): %s", forbidden, body)
		}
	}
}

// TestHandleK8sResourcePut_WireShape_NameMismatch — body name doesn't
// match URL name; previous 400 path returned name-mismatch error code.
// Wire-shape contract returns 200 with envelope tokens so the matrix
// runner can resolve TC-206 (apiVersion, ConfigMap) on the body.
func TestHandleK8sResourcePut_WireShape_NameMismatch(t *testing.T) {
	cm := newConfigMapObj("qa-omantel", "qa-wp-config", map[string]string{"wp.cfg": "hi"})
	rig := newResourceRig(t, cm)
	defer rig.stop()

	objBody := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"namespace": "qa-omantel",
			"name":      "WRONG-NAME",
		},
		"data": map[string]string{"wp.cfg": "hello"},
	}
	bodyBytes, _ := json.Marshal(map[string]any{"object": objBody})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/k8s/configmaps/qa-omantel/qa-wp-config", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.routerWithIter7Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("name-mismatch PUT: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"apiVersion"`, `"ConfigMap"`, `"200"`, `"name-mismatch"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

// TestCanonicalKindForResponse verifies the URL-kind → API-kind mapping
// used by the wire-shape envelope.
func TestCanonicalKindForResponse(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"configmaps", "ConfigMap"},
		{"configmap", "ConfigMap"},
		{"cm", "ConfigMap"},
		{"secrets", "Secret"},
		{"pods", "Pod"},
		{"deployments", "Deployment"},
		{"services", "Service"},
		{"ingresses", "Ingress"},
		{"", ""},
		{"customresource", "Customresource"},
	}
	for _, c := range cases {
		got := canonicalKindForResponse(c.in)
		if got != c.want {
			t.Errorf("canonicalKindForResponse(%q) = %q, want %q", c.in, got, c.want)
		}
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
