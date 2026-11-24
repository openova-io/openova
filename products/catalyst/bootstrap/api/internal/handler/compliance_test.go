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
//     - HTTP /policies returns weight + violation tally + the engine's mode
//     - HTTP /violations returns paginated rows, filtered by policy
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
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
		"resource.deployment/ns1/billing",
		"application.billing",
		"environment.acme-prod",
		"organization.acme",
		"sovereign.acme",
	}
	for _, k := range want {
		if _, ok := nats.Get(k); !ok {
			t.Errorf("missing NATS key %q (got: %v)", k, keys)
		}
	}

	// Verify resource score = 50 (1 of 2 weight-50 policies pass).
	body, _ := nats.Get("resource.deployment/ns1/billing")
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
		_, ok := nats.Get("resource.deployment/ns1/billing")
		return ok
	})
	body, _ := nats.Get("resource.deployment/ns1/billing")
	var s Score
	_ = json.Unmarshal(body, &s)
	if s.PolicyResults["flux-managed"] != "fail" {
		t.Fatalf("synthetic ingest: expected fail, got %v", s.PolicyResults)
	}
}

// complianceScoreSeriesExists reports whether metricComplianceScore currently
// holds a series with the given `id` label — WITHOUT instantiating it (unlike
// WithLabelValues, which would resurrect a just-deleted series at 0 and make
// the assertion meaningless). Collects the vec directly and scans labels.
func complianceScoreSeriesExists(id string) bool {
	ch := make(chan prometheus.Metric, 4096)
	metricComplianceScore.Collect(ch)
	close(ch)
	for m := range ch {
		var dm dto.Metric
		if m.Write(&dm) != nil {
			continue
		}
		for _, lp := range dm.Label {
			if lp.GetName() == "id" && lp.GetValue() == id {
				return true
			}
		}
	}
	return false
}

// TestCompliance_PruneOnPolicyReportDelete_5352 proves that a DELETED
// per-resource PolicyReport prunes BOTH the in-memory resourceState and the
// per-resource `catalyst_compliance_score` gauge series. Before #5352 the
// handler ignored ev.Type, so every churning pod/job left a permanent gauge
// series → unbounded registry growth → catalyst-api OOM (28× on hw288).
func TestCompliance_PruneOnPolicyReportDelete_5352(t *testing.T) {
	_, c, nats, f := newComplianceTestRig(t)

	const key = "pod/ns1/pod-xyz" // resourceKey(Pod, ns1, pod-xyz), kind lower-cased

	report := mkPolicyReport("ns1", "pod-xyz-report", []map[string]any{
		{
			"policy":  "probes-present",
			"rule":    "probes-present",
			"result":  "pass",
			"message": "ok",
			"resources": []any{
				map[string]any{"kind": "Pod", "namespace": "ns1", "name": "pod-xyz"},
			},
		},
	})
	publishToFactory(f, "policyreport", report)

	// Scored → resourceState + gauge series both exist.
	waitFor(t, 1*time.Second, func() bool {
		_, ok := nats.Get("resource." + key)
		return ok
	})
	if !complianceScoreSeriesExists(key) {
		t.Fatalf("precondition: gauge series for %q missing after ingest", key)
	}
	c.mu.RLock()
	_, present := c.state["acme"][key]
	c.mu.RUnlock()
	if !present {
		t.Fatalf("precondition: resourceState for %q missing after ingest", key)
	}

	// Pod torn down → Kyverno deletes its per-resource PolicyReport. The
	// informer delivers the final object body on the DELETED event.
	f.Publish(k8scache.Event{
		Cluster: "acme",
		Kind:    "policyreport",
		Type:    k8scache.EventDeleted,
		Object:  report,
		At:      time.Now(),
	})

	// #5352 — the resourceState AND the gauge series must both be pruned.
	waitFor(t, 1*time.Second, func() bool {
		c.mu.RLock()
		_, present := c.state["acme"][key]
		c.mu.RUnlock()
		return !present
	})
	if complianceScoreSeriesExists(key) {
		t.Fatalf("#5352 regression: gauge series for %q still present after PolicyReport delete — the leak is back", key)
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
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("flux-managed", "Enforce", ""))
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
		// Weight 50 is the EnvironmentPolicy of acme-prod, kept at ingest.
		for _, p := range h.ComplianceHandler().policiesFor(context.Background(), "acme") {
			if p.Name == "flux-managed" && p.Violations == 1 && p.Mode == "enforcing" && p.Weight == 50 {
				return true
			}
		}
		return false
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
		return len(h.ComplianceHandler().violationsFor("acme", "web", "")) == 3
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

// TestCompliance_ResourceFindings — #4085 per-resource Compliance tab.
//
// GET /compliance/resource?kind&ns&name returns the resource's OWN
// per-policy findings (pass/fail/skip) joined with ClusterPolicy
// metadata (severity/category) so the resource detail view can render
// its own table instead of bouncing to the sovereign-wide dashboard.
func TestCompliance_ResourceFindings(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)

	// ClusterPolicy metadata so findings carry severity/category.
	publishToFactory(f, "clusterpolicy", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy",
		"metadata": map[string]any{
			"name": "disallow-privileged-containers",
			"annotations": map[string]any{
				"policies.kyverno.io/severity": "high",
				"policies.kyverno.io/category": "Pod Security Standards",
			},
		},
		"spec": map[string]any{"rules": []any{map[string]any{"name": "privileged"}}},
	}})
	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1", "name": "web",
			"labels": map[string]any{"catalyst.openova.io/application": "web"},
		},
	}})
	// One fail + one pass for this resource.
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr-fail", []map[string]any{
		{"policy": "disallow-privileged-containers", "rule": "privileged", "result": "fail",
			"message": "container runs privileged",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr-pass", []map[string]any{
		{"policy": "require-run-as-nonroot", "rule": "nonroot", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	// Wait for the state the assertions below read: BOTH reports
	// ingested. The watch goroutine handles events one at a time in
	// publish order, so pr-pass being in means the ClusterPolicy, the
	// Deployment and pr-fail are in too. Found alone turns true as soon
	// as pr-fail is stored, while pr-pass may still be queued: the test
	// then read one finding now and then (CI 2026-11-20, "expected 2
	// findings, got 1").
	waitFor(t, 1*time.Second, func() bool {
		return len(h.ComplianceHandler().resourceComplianceFor("acme", "Deployment", "ns1", "web").Findings) == 2
	})

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/resource", h.HandleComplianceResource)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/acme/compliance/resource?kind=Deployment&ns=ns1&name=web", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resource: %d %s", rec.Code, rec.Body.String())
	}
	var resp ResourceComplianceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Found {
		t.Fatalf("expected found=true, body=%s", rec.Body.String())
	}
	if len(resp.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(resp.Findings), resp.Findings)
	}
	// Fail row sorts first (most actionable) and carries the joined
	// ClusterPolicy severity/category metadata.
	first := resp.Findings[0]
	if first.Result != "fail" {
		t.Fatalf("expected fail row first, got %q", first.Result)
	}
	if first.Policy != "disallow-privileged-containers" || first.Severity != "high" || first.Category != "Pod Security Standards" {
		t.Fatalf("fail row missing metadata: %+v", first)
	}
	if first.Message != "container runs privileged" {
		t.Fatalf("fail row missing message: %+v", first)
	}
	if resp.Violations != 1 {
		t.Fatalf("expected 1 violation, got %d", resp.Violations)
	}

	// Unknown resource → found=false + empty (non-nil) findings.
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/acme/compliance/resource?kind=Deployment&ns=ns1&name=nope", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("unknown resource: %d %s", rec2.Code, rec2.Body.String())
	}
	var resp2 ResourceComplianceResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2.Found {
		t.Fatalf("expected found=false for unknown resource")
	}
	if resp2.Findings == nil {
		t.Fatalf("findings must be non-nil ([]), got nil")
	}

	// Missing required params → 400.
	req3 := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/acme/compliance/resource?kind=Deployment", nil)
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("missing name: expected 400, got %d", rec3.Code)
	}
}

// TestCompliance_SovereignAlertCount — slice Z2 follow-up.
//
// SovereignAlertCount returns len(violationsFor(clusterID, "", "")). When
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

// ── qa-loop iter-1 prov #8 Fix #97 — handler shape regression tests ──

// TestCompliance_ScorecardEchoesRegion verifies the scorecard endpoint
// echoes the `?region=` query param back into the response body so a
// multi-region consumer (TC-050) can confirm the requested scope without
// re-parsing the URL.
func TestCompliance_ScorecardEchoesRegion(t *testing.T) {
	h, _, _, _ := newComplianceTestRig(t)
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/acme/compliance/scorecard?region=hz-hel-rtz-prod", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scorecard: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hz-hel-rtz-prod") {
		t.Fatalf("scorecard body must echo region; got %s", body)
	}
}

// mkClusterPolicy builds a legacy Kyverno ClusterPolicy event payload.
// action is spec.validationFailureAction ("" leaves it out); ruleAction
// is validate.failureAction of its one rule ("" leaves it out).
func mkClusterPolicy(name, action, ruleAction string) *unstructured.Unstructured {
	rule := map[string]any{"name": name + "-rule"}
	if ruleAction != "" {
		rule["validate"] = map[string]any{"failureAction": ruleAction}
	}
	spec := map[string]any{"rules": []any{rule}}
	if action != "" {
		spec["validationFailureAction"] = action
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy",
		"metadata": map[string]any{
			"name":        name,
			"annotations": map[string]any{"policies.kyverno.io/severity": "medium"},
		},
		"spec": spec,
	}}
}

// TestCompliance_EnginePolicyMode — the mode is read from the engine's
// policy object, never from a configuration of catalyst-api.
func TestCompliance_EnginePolicyMode(t *testing.T) {
	validating := func(actions ...any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "policies.kyverno.io/v1", "kind": "ValidatingPolicy",
			"metadata": map[string]any{"name": "v"},
			"spec":     map[string]any{"validationActions": actions},
		}}
	}
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
	}{
		{"ValidatingPolicy with Deny", validating("Audit", "Deny"), "enforce"},
		{"ValidatingPolicy with Audit only", validating("Audit", "Warn"), "audit"},
		{"ClusterPolicy Enforce", mkClusterPolicy("a", "Enforce", ""), "enforce"},
		{"ClusterPolicy enforce in lower case", mkClusterPolicy("a", "enforce", ""), "enforce"},
		{"ClusterPolicy Audit", mkClusterPolicy("a", "Audit", ""), "audit"},
		{"ClusterPolicy Audit with one rule Enforce", mkClusterPolicy("a", "Audit", "Enforce"), "enforce"},
		{"ClusterPolicy with no action", mkClusterPolicy("a", "", ""), "audit"},
	}
	for _, c := range cases {
		if got := enginePolicyMode(c.obj); got != c.want {
			t.Errorf("%s: want %s, got %s", c.name, c.want, got)
		}
	}
}

// TestCompliance_PoliciesModeFollowsEngineObject — each listed policy
// shows the mode of its engine object, and a changed object changes it.
func TestCompliance_PoliciesModeFollowsEngineObject(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("enforced", "Enforce", ""))
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("rule-enforced", "Audit", "Enforce"))
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("audited", "Audit", ""))
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr", []map[string]any{
		{"policy": "audited", "rule": "audited-rule", "result": "fail",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	modes := func() map[string]string {
		out := map[string]string{}
		for _, p := range h.ComplianceHandler().policiesFor(context.Background(), "acme") {
			out[p.Name] = p.Mode
		}
		return out
	}
	waitFor(t, 1*time.Second, func() bool { return len(modes()) == 3 })
	want := map[string]string{"enforced": "enforcing", "rule-enforced": "enforcing", "audited": "permissive"}
	for n, m := range want {
		if got := modes()[n]; got != m {
			t.Errorf("%s: want mode %s, got %s", n, m, got)
		}
	}
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("audited", "Enforce", ""))
	waitFor(t, 1*time.Second, func() bool { return modes()["audited"] == "enforcing" })
}

// TestCompliance_PoliciesIgnoreBaselineParameter — the policies route has
// no baseline filter: `?baseline=true` lists every policy and the answer
// carries no baseline keys.
func TestCompliance_PoliciesIgnoreBaselineParameter(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("disallow-privileged-containers", "Enforce", ""))
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("some-custom-policy", "Audit", ""))
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr", []map[string]any{
		{"policy": "some-custom-policy", "rule": "r", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	waitFor(t, 1*time.Second, func() bool {
		return len(h.ComplianceHandler().policiesFor(context.Background(), "acme")) == 2
	})
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies?baseline=true", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("policies: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["baseline"]; ok {
		t.Errorf("the answer must carry no baseline key: %s", rec.Body.String())
	}
	if _, ok := resp["baselineCount"]; ok {
		t.Errorf("the answer must carry no baselineCount key: %s", rec.Body.String())
	}
	if resp["count"] != float64(2) {
		t.Errorf("count: want 2, got %v", resp["count"])
	}
}

// TestCompliance_ScorecardCarriesOnlyMeasuredData — the scorecard names
// only applications the aggregator has seen, whatever the environment of
// the process holds, and has no category block.
func TestCompliance_ScorecardCarriesOnlyMeasuredData(t *testing.T) {
	t.Setenv("CATALYST_QA_APPLICATIONS", "qa-wordpress,qa-wp")
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "deployment", &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"namespace": "ns1", "name": "web",
			"labels": map[string]any{"catalyst.openova.io/application": "web"},
		},
	}})
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "web-pr", []map[string]any{
		{"policy": "probes-present", "rule": "probes-present", "result": "pass",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	waitFor(t, 1*time.Second, func() bool {
		for _, s := range h.ComplianceHandler().rollupsFor("acme") {
			if s.Scope == "application" && s.ID == "web" {
				return true
			}
		}
		return false
	})
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/scorecard", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scorecard: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refs, _ := json.Marshal(resp["appRefs"])
	if string(refs) != `["web"]` {
		t.Errorf("appRefs: want [\"web\"], got %s", refs)
	}
	for _, key := range []string{"categoryScores", "security", "sre", "baseline", "reliability"} {
		if _, ok := resp[key]; ok {
			t.Errorf("the scorecard must carry no %q key: %s", key, rec.Body.String())
		}
	}
	if _, ok := resp["items"]; !ok {
		t.Errorf("the scorecard must carry items: %s", rec.Body.String())
	}
}

// TestCompliance_ViolationsPolicyFilter — `?policy=<name>` keeps the
// violations of that policy, filtered on the server before the page.
func TestCompliance_ViolationsPolicyFilter(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	for _, p := range []string{"a", "b", "c"} {
		publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr-"+p, []map[string]any{
			{"policy": p, "rule": p, "result": "fail",
				"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
		}))
	}
	waitFor(t, 1*time.Second, func() bool {
		return len(h.ComplianceHandler().violationsFor("acme", "", "")) == 3
	})
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/violations", h.HandleComplianceViolations)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/violations?policy=b&limit=1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("violations: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []Violation `json:"items"`
		Total int         `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].Policy != "b" {
		t.Fatalf("policy=b: want one violation of policy b, got total=%d items=%+v", resp.Total, resp.Items)
	}
}

// TestCompliance_PolicyByNameFromCache — the by-name route answers from
// the aggregator: 200 with the engine's mode for a policy the cache
// delivered, 404 for any other name. No cluster client is wired here.
func TestCompliance_PolicyByNameFromCache(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("require-probes", "Enforce", ""))
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "pr", []map[string]any{
		{"policy": "require-probes", "rule": "require-probes-rule", "result": "fail",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}},
	}))
	waitFor(t, 1*time.Second, func() bool {
		return len(h.ComplianceHandler().policiesFor(context.Background(), "acme")) == 1
	})
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", h.HandleCompliancePolicyByName)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies/require-probes", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("by name: %d %s", rec.Code, rec.Body.String())
	}
	var view PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Name != "require-probes" || view.Mode != "enforcing" || view.Violations != 1 || view.Severity != "medium" ||
		len(view.Rules) != 1 || view.Rules[0] != "require-probes-rule" {
		t.Fatalf("by name: unexpected view %+v", view)
	}
	get := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies/"+name, nil))
		return rec
	}
	var notFound struct {
		Error string `json:"error"`
	}
	if rec2 := get("unknown"); rec2.Code != http.StatusNotFound ||
		json.Unmarshal(rec2.Body.Bytes(), &notFound) != nil || notFound.Error != "policy not found" {
		t.Fatalf("unknown name: want 404 policy not found, got %d %s", rec2.Code, rec2.Body.String())
	}

	// A policy the engine removes leaves the route and the list.
	publishToFactory(f, "clusterpolicy", mkClusterPolicy("removed-later", "Enforce", ""))
	waitFor(t, 1*time.Second, func() bool { return get("removed-later").Code == http.StatusOK })
	f.Publish(k8scache.Event{Cluster: "acme", Kind: "clusterpolicy", Type: k8scache.EventDeleted,
		Object: mkClusterPolicy("removed-later", "Enforce", ""), At: time.Now()})
	waitFor(t, 1*time.Second, func() bool { return get("removed-later").Code == http.StatusNotFound })
	for _, p := range h.ComplianceHandler().policiesFor(context.Background(), "acme") {
		if p.Name == "removed-later" {
			t.Fatalf("a deleted policy is still listed: %+v", p)
		}
	}

	// Without the aggregator the route answers 503.
	bare := chi.NewRouter()
	bare.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", NewWithPDM(quietComplianceLogger(), &fakePDM{}).HandleCompliancePolicyByName)
	rec3 := httptest.NewRecorder()
	bare.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies/require-probes", nil))
	if rec3.Code != http.StatusServiceUnavailable {
		t.Fatalf("no aggregator: want 503, got %d %s", rec3.Code, rec3.Body.String())
	}
}

// countingResolver counts the EnvironmentPolicy reads of the aggregator.
type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(context.Context, string) (*EnvironmentPolicySpec, error) {
	r.calls++
	return nil, nil
}

// TestCompliance_PolicyRoutesReadNoClusterPerRequest — the list and the
// by-name route take the weights the last ingest kept; neither resolves an
// EnvironmentPolicy (a cluster read in production) per request, and a policy
// object lists before the cluster has any report.
func TestCompliance_PolicyRoutesReadNoClusterPerRequest(t *testing.T) {
	res := &countingResolver{}
	c := NewComplianceHandler(ComplianceConfig{}, quietComplianceLogger(), nil, nil, res)
	c.policyMetaByName["acme"] = map[string]policyMeta{"only-object": {mode: "enforce"}}
	h := NewWithPDM(quietComplianceLogger(), &fakePDM{})
	h.SetComplianceHandler(c)
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
	r.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", h.HandleCompliancePolicyByName)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies"+path, nil))
		return rec
	}
	if rec := get("/only-object"); rec.Code != http.StatusOK {
		t.Fatalf("a policy object with no report yet: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	c.state["acme"] = map[string]*resourceState{"ns1/Deployment/web": {environment: "acme-prod",
		results: map[string]policyVerdict{"weighted": {result: "fail"}}}}
	c.envSpecs["acme-prod"] = &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{"weighted": {Weight: 40, Scope: "all"}}}
	rec := get("/weighted")
	var view PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("by name: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	if view.Weight != 40 || view.Scope != "all" || view.Violations != 1 {
		t.Fatalf("by name: want the kept weight 40/all and 1 violation, got %+v", view)
	}
	if rec := get(""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":2`) {
		t.Fatalf("list: want 2 rows, got %d %s", rec.Code, rec.Body.String())
	}
	if res.calls != 0 {
		t.Fatalf("the routes resolved an EnvironmentPolicy %d times per request; want 0", res.calls)
	}
}

// TestCompliance_PolicyObjectsPerCluster — one process watches several
// clusters: each cluster's routes show the mode of its own engine object,
// a delete in one cluster leaves the other untouched, and a cluster with
// no object and no report lists nothing.
func TestCompliance_PolicyObjectsPerCluster(t *testing.T) {
	h, _, _, f := newComplianceTestRig(t)
	publish := func(cluster string, typ k8scache.EventType, obj *unstructured.Unstructured) {
		f.Publish(k8scache.Event{Cluster: cluster, Kind: "clusterpolicy", Type: typ, Object: obj, At: time.Now()})
	}
	modes := func(cluster string) map[string]string {
		out := map[string]string{}
		for _, p := range h.ComplianceHandler().policiesFor(context.Background(), cluster) {
			out[p.Name] = p.Mode
		}
		return out
	}
	publish("acme", k8scache.EventAdded, mkClusterPolicy("x", "Enforce", ""))
	publish("beta", k8scache.EventAdded, mkClusterPolicy("x", "Audit", ""))
	publish("beta", k8scache.EventAdded, mkClusterPolicy("beta-only", "Audit", ""))
	waitFor(t, 1*time.Second, func() bool { return len(modes("beta")) == 2 && len(modes("acme")) == 1 })
	if got := modes("acme")["x"]; got != "enforcing" {
		t.Fatalf("acme x: want enforcing (its own object), got %q", got)
	}
	if got := modes("beta")["x"]; got != "permissive" {
		t.Fatalf("beta x: want permissive (its own object), got %q", got)
	}
	if _, ok := modes("acme")["beta-only"]; ok {
		t.Fatalf("acme lists the policy of cluster beta: %v", modes("acme"))
	}
	if got := modes("unknown-id"); len(got) != 0 {
		t.Fatalf("a cluster with no object and no report: want no policy, got %v", got)
	}
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", h.HandleCompliancePolicyByName)
	get := func(cluster string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/"+cluster+"/compliance/policies/x", nil))
		return rec
	}
	publish("beta", k8scache.EventDeleted, mkClusterPolicy("x", "Audit", ""))
	waitFor(t, 1*time.Second, func() bool { return get("beta").Code == http.StatusNotFound })
	rec := get("acme")
	var view PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK || view.Mode != "enforcing" {
		t.Fatalf("acme x after beta deleted it: want 200 enforcing, got %d %s", rec.Code, rec.Body.String())
	}
}
