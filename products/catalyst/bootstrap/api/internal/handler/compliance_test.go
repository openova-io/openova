// compliance_test.go — unit + integration coverage for slice S
// (#1096) score aggregator.
//
// Test matrix:
//
//   computeScore (pure):
//     - all pass        → 100
//     - all fail        → 0
//     - half pass       → 50
//     - skip drops      → not in denominator
//     - empty weights   → fallback equal-weights observed-policies
//     - stateful scope  → drops on stateless workload
//     - stateless scope → drops on stateful workload
//     - missing verdict → drops from denominator
//     - warn pulls down → counted in denominator only
//
//   normalizedScore (pure):
//     - den=0 → nil
//     - num>den (over-pass) → clamped to 100
//     - negatives → clamped to 0
//
//   end-to-end via fake k8scache.Factory:
//     - PolicyReport ingest → resource score recorded + published
//     - synthetic SyntheticReport ingest → resource score recorded
//     - rollups: app + env + org + sovereign all computed
//     - HTTP /scorecard returns the rolled shape
//     - HTTP /policies returns weight + violation tally
//     - HTTP /violations returns paginated rows
//     - NATS publisher receives one Put per scope on each event
//
// Pre-existing CI failures noted in canon §7 are unaffected — this
// file does not import from the failing test files.
package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ── Helpers ──────────────────────────────────────────────────────────────

func quietComplianceLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeNATS records every Put for assertion.
type fakeNATS struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func newFakeNATS() *fakeNATS { return &fakeNATS{entries: map[string][]byte{}} }

func (f *fakeNATS) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.entries[key] = cp
	return nil
}

func (f *fakeNATS) Get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.entries[key]
	return v, ok
}

func (f *fakeNATS) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for k := range f.entries {
		out = append(out, k)
	}
	return out
}

// fakeResolver returns a fixed EnvironmentPolicySpec for matching env
// names; everything else returns (nil, nil) to exercise the fallback
// path.
type fakeResolver struct {
	by map[string]*EnvironmentPolicySpec
}

func (r *fakeResolver) Resolve(_ context.Context, env string) (*EnvironmentPolicySpec, error) {
	if r == nil || r.by == nil {
		return nil, nil
	}
	return r.by[env], nil
}

// ── computeScore — every algorithm edge case ─────────────────────────────

func TestComputeScore_AllPass(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "pass"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50, Scope: "all"},
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 100 {
		t.Fatalf("all-pass: want 100, got %+v", got.Total)
	}
}

func TestComputeScore_AllFail(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "fail"}, "b": {result: "fail"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50, Scope: "all"},
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 0 {
		t.Fatalf("all-fail: want 0, got %+v", got.Total)
	}
	if got.Violations != 2 {
		t.Fatalf("all-fail: want 2 violations, got %d", got.Violations)
	}
}

func TestComputeScore_HalfPass(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "fail"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50, Scope: "all"},
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 50 {
		t.Fatalf("half-pass: want 50, got %+v", got.Total)
	}
}

func TestComputeScore_SkipDropsFromDenominator(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "skip"}, "c": {result: "pass"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 1}, "b": {Weight: 1}, "c": {Weight: 1},
	}}
	got := computeScore(rs, spec)
	// 2 pass / 2 effective denominator = 100
	if got.Total == nil || *got.Total != 100 {
		t.Fatalf("skip-drop: want 100 (skip excluded from denom), got %+v num=%d den=%d",
			got.Total, got.Numerator, got.Denominator)
	}
	if got.Denominator != 2 {
		t.Fatalf("skip-drop: want denom=2, got %d", got.Denominator)
	}
}

func TestComputeScore_EmptyWeightsFallback(t *testing.T) {
	// No EnvironmentPolicy → fallback to "weight=1 per observed policy".
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "fail"},
		},
	}
	got := computeScore(rs, nil)
	if got.Total == nil {
		t.Fatalf("empty-weights: want non-nil total, got nil")
	}
	if *got.Total != 50 {
		t.Fatalf("empty-weights: want 50 (1/2), got %d", *got.Total)
	}
}

func TestComputeScore_StatefulScopeOnStateless(t *testing.T) {
	rs := &resourceState{
		stateful: false,
		results:  map[string]policyVerdict{"a": {result: "fail"}, "b": {result: "pass"}},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 100, Scope: "stateful"}, // dropped — stateless workload
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 100 {
		t.Fatalf("stateful-scope: want 100 (a dropped), got %+v", got.Total)
	}
}

func TestComputeScore_StatelessScopeOnStateful(t *testing.T) {
	rs := &resourceState{
		stateful: true,
		results:  map[string]policyVerdict{"a": {result: "pass"}, "b": {result: "pass"}},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50, Scope: "stateless"}, // dropped — stateful workload
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 100 {
		t.Fatalf("stateless-scope: want 100, got %+v", got.Total)
	}
	if got.Denominator != 50 {
		t.Fatalf("stateless-scope: want denom=50, got %d", got.Denominator)
	}
}

func TestComputeScore_MissingVerdictDropsPolicy(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{"a": {result: "pass"}}, // b missing
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50}, "b": {Weight: 50},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 100 {
		t.Fatalf("missing: want 100 (b not yet evaluated), got %+v", got.Total)
	}
	if got.Denominator != 50 {
		t.Fatalf("missing: want denom=50, got %d", got.Denominator)
	}
}

func TestComputeScore_WarnPullsDown(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "warn"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50}, "b": {Weight: 50},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || *got.Total != 50 {
		t.Fatalf("warn: want 50 (warn in denom only), got %+v", got.Total)
	}
}

func TestComputeScore_NoPolicies(t *testing.T) {
	rs := &resourceState{results: map[string]policyVerdict{}}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{}}
	got := computeScore(rs, spec)
	if got.Total != nil {
		t.Fatalf("no-policies: want nil (n/a), got %v", *got.Total)
	}
}

func TestNormalizedScore_DenZero(t *testing.T) {
	if normalizedScore(0, 0) != nil {
		t.Fatalf("den=0 should be nil")
	}
}

func TestNormalizedScore_Clamps(t *testing.T) {
	if v := normalizedScore(200, 100); v == nil || *v != 100 {
		t.Fatalf("over-pass clamp: %v", v)
	}
	if v := normalizedScore(-1, 100); v == nil || *v != 0 {
		t.Fatalf("negative clamp: %v", v)
	}
}

// ── End-to-end via the watch loop + Factory ──────────────────────────────

// mkPolicyReport produces a Kyverno-shaped *unstructured.Unstructured
// for use as the SSE event payload.
func mkPolicyReport(ns, name string, results []map[string]any) *unstructured.Unstructured {
	r := make([]any, len(results))
	for i, x := range results {
		r[i] = x
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "PolicyReport",
		"metadata": map[string]any{
			"namespace": ns,
			"name":      name,
		},
		"results": r,
	}}
}

// mkSyntheticReport produces a SyntheticReport-shaped event payload
// matching the JSON tags in evaluators.SyntheticReport.
func mkSyntheticReport(policy, result, kind, ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"policy": policy,
		"rule":   policy,
		"result": result,
		"resource": map[string]any{
			"apiVersion": "apps/v1",
			"kind":       kind,
			"name":       name,
		},
		"namespace": ns,
		"message":   "",
		"time":      time.Now().Format(time.RFC3339),
	}}
}

func newComplianceTestRig(t *testing.T) (*Handler, *ComplianceHandler, *fakeNATS, *k8scache.Factory) {
	t.Helper()
	cfg := k8scache.Config{
		Logger:   quietComplianceLogger(),
		Registry: k8scache.NewRegistry(),
	}
	// Minimal registry — only the kinds the aggregator subscribes to.
	_ = cfg.Registry.Add(k8scache.Kind{
		Name:       "policyreport",
		GVR:        schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"},
		Namespaced: true,
	})
	_ = cfg.Registry.Add(k8scache.Kind{
		Name:       "clusterpolicyreport",
		GVR:        schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"},
		Namespaced: false,
	})
	_ = cfg.Registry.Add(k8scache.Kind{
		Name:       "deployment",
		GVR:        schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Namespaced: true,
	})
	f, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Stop)

	nats := newFakeNATS()
	resolver := &fakeResolver{by: map[string]*EnvironmentPolicySpec{
		"acme-prod": {
			Weights: map[string]PolicyWeight{
				"probes-present":    {Weight: 50, Scope: "all"},
				"flux-managed":      {Weight: 50, Scope: "all"},
				"hpa-effective":     {Weight: 25, Scope: "stateless"},
				"pvc-volume-expand": {Weight: 25, Scope: "stateful"},
			},
			Modes: map[string]string{
				"probes-present": "enforcing",
				"flux-managed":   "enforcing",
				"hpa-effective":  "permissive",
			},
		},
	}}
	c := NewComplianceHandler(ComplianceConfig{
		SSEHeartbeatInterval: 100 * time.Millisecond,
		ViolationsPageSize:   10,
		ViolationsMaxPage:    100,
	}, quietComplianceLogger(), f, nats, resolver)

	h := NewWithPDM(quietComplianceLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	h.SetComplianceHandler(c)
	c.Start(context.Background())
	t.Cleanup(c.Stop)
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatalf("compliance handler not ready: %v", err)
	}
	return h, c, nats, f
}

// publishToFactory injects an event onto the Factory's fanout — the
// same path the real informer uses. Lets tests drive the watch loop
// without spinning up a fake apiserver.
func publishToFactory(f *k8scache.Factory, kind string, obj *unstructured.Unstructured) {
	f.Publish(k8scache.Event{
		Cluster: "acme",
		Kind:    kind,
		Type:    k8scache.EventAdded,
		Object:  obj,
		At:      time.Now(),
	})
}

// waitFor polls until cond returns true OR the deadline passes.
func waitFor(t *testing.T, dur time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor: condition not met within %s", dur)
}

func TestCompliance_IngestPolicyReportAndPublish(t *testing.T) {
	_, c, nats, f := newComplianceTestRig(t)

	// First seed a Deployment so labels enrich the resourceState.
	dep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1",
			"name":      "billing",
			"labels": map[string]any{
				"catalyst.openova.io/application":  "billing",
				"catalyst.openova.io/environment":  "acme-prod",
				"catalyst.openova.io/organization": "acme",
			},
		},
	}}
	publishToFactory(f, "deployment", dep)

	// Now a PolicyReport for the Deployment.
	report := mkPolicyReport("ns1", "billing-report", []map[string]any{
		{
			"policy":  "probes-present",
			"rule":    "probes-present",
			"result":  "pass",
			"message": "ok",
			"resources": []any{
				map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "billing"},
			},
		},
		{
			"policy":  "flux-managed",
			"rule":    "flux-managed",
			"result":  "fail",
			"message": "no managed-by label",
			"resources": []any{
				map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "billing"},
			},
		},
	})
	publishToFactory(f, "policyreport", report)

	waitFor(t, 1*time.Second, func() bool {
		return len(nats.Keys()) >= 4 // resource + app + env + org + sovereign
	})

	keys := nats.Keys()
	want := []string{
		"resource:deployment/ns1/billing",
		"application:billing",
		"environment:acme-prod",
		"organization:acme",
		"sovereign:acme",
	}
	for _, k := range want {
		if _, ok := nats.Get(k); !ok {
			t.Errorf("missing NATS key %q (got: %v)", k, keys)
		}
	}

	// Verify resource score = 50 (1 of 2 weight-50 policies pass).
	body, _ := nats.Get("resource:deployment/ns1/billing")
	var s Score
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal score: %v", err)
	}
	if s.Total == nil || *s.Total != 50 {
		t.Fatalf("score: want 50, got %v", s.Total)
	}
	if s.Violations != 1 {
		t.Fatalf("violations: want 1, got %d", s.Violations)
	}
	_ = c // unused
}

func TestCompliance_IngestSyntheticReport(t *testing.T) {
	_, _, nats, f := newComplianceTestRig(t)

	report := mkSyntheticReport("flux-managed", "fail", "Deployment", "ns1", "billing")
	publishToFactory(f, k8scache.KindComplianceEvaluator, report)

	waitFor(t, 1*time.Second, func() bool {
		_, ok := nats.Get("resource:deployment/ns1/billing")
		return ok
	})
	body, _ := nats.Get("resource:deployment/ns1/billing")
	var s Score
	_ = json.Unmarshal(body, &s)
	if s.PolicyResults["flux-managed"] != "fail" {
		t.Fatalf("synthetic ingest: expected fail, got %v", s.PolicyResults)
	}
}

// ── HTTP endpoint coverage ───────────────────────────────────────────────

func TestCompliance_ScorecardEndpoint(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)

	// Seed a Deployment + PolicyReport.
	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1",
			"name":      "web",
			"labels": map[string]any{
				"catalyst.openova.io/application":  "web",
				"catalyst.openova.io/environment":  "acme-prod",
				"catalyst.openova.io/organization": "acme",
			},
		},
	}})
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "web-pr", []map[string]any{
		{"policy": "probes-present", "rule": "probes-present", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
		{"policy": "flux-managed", "rule": "flux-managed", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	waitFor(t, 1*time.Second, func() bool {
		c := h.ComplianceHandler()
		return len(c.rollupsFor("acme")) > 0
	})

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/scorecard", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scorecard: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ScorecardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Sovereign.Total == nil || *resp.Sovereign.Total != 100 {
		t.Fatalf("sovereign rollup: want 100, got %+v", resp.Sovereign)
	}
	if len(resp.Applications) != 1 || resp.Applications[0].ID != "web" {
		t.Fatalf("applications: %+v", resp.Applications)
	}
}

func TestCompliance_PoliciesEndpoint(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1", "name": "web",
			"labels": map[string]any{
				"catalyst.openova.io/application": "web",
				"catalyst.openova.io/environment": "acme-prod",
			},
		},
	}})
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "web-pr", []map[string]any{
		{"policy": "flux-managed", "rule": "flux-managed", "result": "fail",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	waitFor(t, 1*time.Second, func() bool {
		c := h.ComplianceHandler()
		return len(c.policiesFor(context.Background(), "acme")) > 0
	})

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("policies: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []PolicyView `json:"items"`
		Count int          `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	gotFlux := false
	for _, p := range resp.Items {
		if p.Name == "flux-managed" {
			gotFlux = true
			if p.Violations != 1 {
				t.Errorf("flux-managed violations: want 1, got %d", p.Violations)
			}
			if p.Mode != "enforcing" {
				t.Errorf("flux-managed mode: want enforcing, got %s", p.Mode)
			}
		}
	}
	if !gotFlux {
		t.Fatalf("flux-managed missing from policies: %+v", resp.Items)
	}
}

func TestCompliance_ViolationsPagination(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1", "name": "web",
			"labels": map[string]any{"catalyst.openova.io/application": "web"},
		},
	}})
	// 3 failures.
	for _, p := range []string{"a", "b", "c"} {
		publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr-"+p, []map[string]any{
			{"policy": p, "rule": p, "result": "fail",
				"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
		}))
	}
	waitFor(t, 1*time.Second, func() bool {
		return len(h.ComplianceHandler().violationsFor("acme", "web")) == 3
	})

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/violations", h.HandleComplianceViolations)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/violations?app=web&limit=2&offset=0", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("violations: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items  []Violation `json:"items"`
		Total  int         `json:"total"`
		Offset int         `json:"offset"`
		Limit  int         `json:"limit"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 3 || resp.Limit != 2 || len(resp.Items) != 2 {
		t.Fatalf("page1: total=%d limit=%d items=%d", resp.Total, resp.Limit, len(resp.Items))
	}
}

// TestCompliance_SovereignAlertCount — slice Z2 follow-up.
//
// SovereignAlertCount returns len(violationsFor(clusterID, "")). When
// no violations have been ingested it returns 0; on N failing
// (resource, policy) pairs it returns N. Nil receiver returns 0
// (catalyst-api Pods running without compliance wired keep the
// dashboard green).
func TestCompliance_SovereignAlertCount(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)

	// Pre-violation: zero alerts.
	if got := h.ComplianceHandler().SovereignAlertCount("acme"); got != 0 {
		t.Fatalf("pre-violation alerts: got %d want 0", got)
	}

	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1", "name": "web",
			"labels": map[string]any{"catalyst.openova.io/application": "web"},
		},
	}})
	for _, p := range []string{"alpha", "bravo", "charlie"} {
		publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr-"+p, []map[string]any{
			{"policy": p, "rule": p, "result": "fail",
				"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
		}))
	}
	waitFor(t, 1*time.Second, func() bool {
		return h.ComplianceHandler().SovereignAlertCount("acme") == 3
	})
	if got := h.ComplianceHandler().SovereignAlertCount("acme"); got != 3 {
		t.Fatalf("post-violation alerts: got %d want 3", got)
	}
	// Unknown cluster → 0 (no violations indexed).
	if got := h.ComplianceHandler().SovereignAlertCount("unknown"); got != 0 {
		t.Fatalf("unknown-cluster alerts: got %d want 0", got)
	}
}

// TestCompliance_SovereignAlertCount_NilReceiver — guards the
// nil-tolerant contract documented on SovereignAlertCount.
func TestCompliance_SovereignAlertCount_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *ComplianceHandler
	if got := c.SovereignAlertCount("any"); got != 0 {
		t.Fatalf("nil-receiver alerts: got %d want 0", got)
	}
}

func TestCompliance_StreamSendsScores(t *testing.T) {
	h, c, _, f := newComplianceTestRig(t)

	// Subscribe directly to the SSE channel without HTTP — the
	// HTTP path is identical wiring on top.
	ch, unsub := c.subscribe("acme")
	defer unsub()

	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "x", []map[string]any{
		{"policy": "p", "rule": "p", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "x"}}},
	}))

	select {
	case ev := <-ch:
		if ev.Type != "score" {
			t.Fatalf("event type: want score, got %s", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event received within 2s")
	}
	_ = h
}

func TestCompliance_HandlerNilReturns503(t *testing.T) {
	h := NewWithPDM(quietComplianceLogger(), &fakePDM{})
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/scorecard", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "compliance handler not wired") {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestCompliance_KyvernoResourceFor_PerResultPreference(t *testing.T) {
	r := mkPolicyReport("ns", "pr", []map[string]any{
		{
			"policy": "p", "result": "pass",
			"resources": []any{map[string]any{"kind": "Pod", "namespace": "ns", "name": "alpha"}},
		},
	})
	results, _, _ := unstructured.NestedSlice(r.Object, "results")
	rmap := results[0].(map[string]any)
	kind, ns, name := kyvernoResourceFor(r, rmap)
	if kind != "Pod" || ns != "ns" || name != "alpha" {
		t.Fatalf("per-result extraction: kind=%s ns=%s name=%s", kind, ns, name)
	}
}

func TestCompliance_KyvernoResourceFor_FallbackScope(t *testing.T) {
	r := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "PolicyReport",
		"metadata":   map[string]any{"namespace": "ns", "name": "pr"},
		"scope": map[string]any{
			"kind": "Pod", "namespace": "ns", "name": "beta",
		},
		"results": []any{
			map[string]any{"policy": "p", "result": "pass"}, // no per-result resources
		},
	}}
	results, _, _ := unstructured.NestedSlice(r.Object, "results")
	rmap := results[0].(map[string]any)
	kind, ns, name := kyvernoResourceFor(r, rmap)
	if kind != "Pod" || ns != "ns" || name != "beta" {
		t.Fatalf("scope fallback: kind=%s ns=%s name=%s", kind, ns, name)
	}
}

func TestCompliance_GuessStateful(t *testing.T) {
	cases := map[string]bool{
		"StatefulSet":           true,
		"PersistentVolumeClaim": true,
		"DaemonSet":             true,
		"Deployment":            false,
		"Service":               false,
		"":                      false,
	}
	for k, want := range cases {
		if got := guessStateful(k); got != want {
			t.Errorf("guessStateful(%q): want %v got %v", k, want, got)
		}
	}
}

func TestCompliance_ResourceKeyRoundTrip(t *testing.T) {
	k := resourceKey("Deployment", "ns1", "billing")
	kind, ns, name := splitResourceKey(k)
	if kind != "deployment" || ns != "ns1" || name != "billing" {
		t.Fatalf("roundtrip: %s/%s/%s", kind, ns, name)
	}
}

func TestCompliance_LabelOr(t *testing.T) {
	lbls := map[string]string{"a": "", "b": "ok", "c": "no"}
	if got := labelOr(lbls, "a", "b", "c"); got != "ok" {
		t.Fatalf("labelOr: want ok, got %s", got)
	}
	if got := labelOr(lbls, "z"); got != "" {
		t.Fatalf("labelOr missing: %s", got)
	}
}

// TestHandleComplianceStream_ImmediateSnapshotFrame asserts the SSE
// /compliance/stream endpoint emits a `data:` snapshot frame on
// connect — even when the cluster has no compliance state yet.
//
// Regression test for qa-loop iter-1 TC-030: the prior implementation
// only sent a `: connected ...` comment line on connect and waited up
// to SSEHeartbeatInterval (15s default) for the next event, causing
// 6s probe timeouts on quiet clusters.
func TestHandleComplianceStream_ImmediateSnapshotFrame(t *testing.T) {
	h, _, _, _ := newComplianceTestRig(t)

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/stream", h.HandleComplianceStream)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sovereigns/acme/compliance/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type: %q", ct)
	}

	// Expect at least one `data:` line within 1s.
	type result struct {
		payload string
		err     error
	}
	doneCh := make(chan result, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				doneCh <- result{err: err}
				return
			}
			if strings.HasPrefix(line, "data: ") {
				doneCh <- result{payload: strings.TrimSpace(strings.TrimPrefix(line, "data: "))}
				return
			}
		}
	}()

	select {
	case got := <-doneCh:
		if got.err != nil {
			t.Fatalf("never received `data:` frame: %v", got.err)
		}
		var ev complianceEvent
		if err := json.Unmarshal([]byte(got.payload), &ev); err != nil {
			t.Fatalf("decode payload %q: %v", got.payload, err)
		}
		if ev.Type != "snapshot" {
			t.Fatalf("first event type: want snapshot, got %q", ev.Type)
		}
		if ev.Cluster != "acme" {
			t.Fatalf("event cluster: want acme, got %q", ev.Cluster)
		}
		if ev.Score.Scope != "sovereign" {
			t.Fatalf("snapshot score scope: want sovereign, got %q", ev.Score.Scope)
		}
	case <-time.After(1 * time.Second):
		_ = resp.Body.Close()
		t.Fatalf("timed out waiting for initial `data:` snapshot frame")
	}
}
