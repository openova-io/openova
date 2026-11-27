package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// kyverno is a set of checks of the engine, by id.
func kyverno(ids ...string) map[string]compliance.Source {
	out := map[string]compliance.Source{}
	for _, id := range ids {
		out[id] = compliance.SourceKyverno
	}
	return out
}

// goldenChecks are the checks of the golden fixture (goldenEvents): four of the
// engine and flux-managed of the evaluator. conformance-tls is a result of
// another producer and no check.
func goldenChecks() map[string]compliance.Source {
	out := kyverno("require-probes", "disallow-latest-tag", "require-requests", "pvc-expand")
	out["flux-managed"] = compliance.SourceEvaluator
	return out
}

// testComplianceModel is a ComplianceModel object named default: one group and
// one axis "all", the given weight for every severity, no mandatory check, a
// cap of 100, no exemption, and one check of severity high per id of checks.
func testComplianceModel(checks map[string]compliance.Source, weight int) *unstructured.Unstructured {
	ids := make([]string, 0, len(checks))
	for id := range checks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]any, 0, len(ids))
	for _, id := range ids {
		c := map[string]any{"id": id, "title": id, "source": string(checks[id]), "axis": "all", "severity": "high",
			"ownerSide": "organization", "description": id, "remediation": "Fix what the check " + id + " names."}
		if checks[id] == compliance.SourceKyverno {
			c["valuesKey"] = "k"
		}
		list = append(list, c)
	}
	w := int64(weight)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": compliance.ModelAPIVersion, "kind": compliance.ModelKind,
		"metadata": map[string]any{"name": compliance.ModelObjectName},
		"spec": map[string]any{
			"version":         "0.1.0",
			"levels":          []any{map[string]any{"name": "L2", "min": int64(50)}, map[string]any{"name": "L1", "min": int64(0)}},
			"mandatoryCap":    map[string]any{"value": int64(100), "environmentTypes": []any{}},
			"severityWeights": map[string]any{"critical": w, "high": w, "medium": w, "low": w},
			"groups":          []any{map[string]any{"id": "all", "title": "All", "weight": int64(1)}},
			"axes":            []any{map[string]any{"id": "all", "title": "All", "group": "all", "type": "checks", "naText": "Nothing in view."}},
			"checks":          list,
			"exemptions":      []any{},
		},
	}}
}

// loadTestModel loads testComplianceModel(checks, 1) into the cluster of a
// handler built without a watch loop.
func loadTestModel(t *testing.T, c *ComplianceHandler, cluster string, checks map[string]compliance.Source) {
	t.Helper()
	if _, err := c.model.ApplyModel(k8scache.Event{Cluster: cluster, Kind: k8scache.KindComplianceModel, Type: k8scache.EventAdded,
		Object: testComplianceModel(checks, 1)}); err != nil {
		t.Fatalf("load the test model: %v", err)
	}
}

// publishModel sends a ComplianceModel event for cluster acme through the rig.
func publishModel(f *k8scache.Factory, typ k8scache.EventType, obj *unstructured.Unstructured) {
	f.Publish(k8scache.Event{Cluster: "acme", Kind: k8scache.KindComplianceModel, Type: typ, Object: obj, At: time.Now()})
}

// recordingLogger keeps every Warn line.
type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (*recordingLogger) Info(string, ...any)  {}
func (*recordingLogger) Error(string, ...any) {}
func (l *recordingLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg+" "+fmt.Sprint(args...))
}

func (l *recordingLogger) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

func TestComplianceConfig_SubscribesToTheModelKind(t *testing.T) {
	var cfg ComplianceConfig
	cfg.defaults()
	for _, k := range cfg.SubscribeKinds {
		if k == k8scache.KindComplianceModel {
			return
		}
	}
	t.Fatalf("the aggregator does not subscribe to %s: %v", k8scache.KindComplianceModel, cfg.SubscribeKinds)
}

// TestCompliance_ModelEvent_CMP042: before the cluster's model arrives a
// subject's score is null (the read model gives the reason "model not
// loaded"); the model event publishes the subject again with its score; an
// object that does not load is logged once and the cluster keeps its model; a
// deleted model makes the score null again.
func TestCompliance_ModelEvent_CMP042(t *testing.T) {
	f := newComplianceFactory(t)
	nats := newFakeNATS()
	log := &recordingLogger{}
	c := NewComplianceHandler(ComplianceConfig{}, log, f, nats)
	c.Start(context.Background())
	t.Cleanup(c.Stop)
	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	web := compliance.SubjectKey{Cluster: "acme", Namespace: "ns1", Kind: "Deployment", Name: "web"}
	score := func() Score {
		body, _ := nats.Get("resource.deployment/ns1/web")
		var s Score
		_ = json.Unmarshal(body, &s)
		return s
	}
	reason := func() string { v, _ := c.model.Subject(web); return v.Reason }
	publishToFactory(f, "policyreport", mkPolicyReport("ns1", "web", []map[string]any{
		goldenResult("require-probes", "r", "pass", "", "Deployment", "ns1", "web"),
		goldenResult("require-requests", "r", "fail", "", "Deployment", "ns1", "web")}))
	waitFor(t, time.Second, func() bool { _, ok := nats.Get("resource.deployment/ns1/web"); return ok })
	if s := score(); s.Total != nil || s.Score != nil || s.Denominator != 0 || reason() != compliance.ReasonModelNotLoaded {
		t.Fatalf("before the model: %+v, reason %q", s, reason())
	}
	if sov, _ := nats.Get("sovereign.acme"); !strings.Contains(string(sov), `"total":null`) {
		t.Fatalf("the sovereign rollup before the model: %s", sov)
	}

	publishModel(f, k8scache.EventAdded, testComplianceModel(kyverno("require-probes", "require-requests"), 1))
	waitFor(t, time.Second, func() bool { return score().Total != nil })
	if s := score(); *s.Total != 50 || s.Denominator != 2 || s.PolicyResults["require-requests"] != "fail" || reason() != "" {
		t.Fatalf("after the model: %+v, reason %q", s, reason())
	}

	broken := testComplianceModel(kyverno("require-probes", "require-requests"), 1)
	_ = unstructured.SetNestedField(broken.Object, "not-a-version", "spec", "version")
	publishModel(f, k8scache.EventModified, broken)
	waitFor(t, time.Second, func() bool { return len(log.lines()) > 0 })
	if l := log.lines(); len(l) != 1 || !strings.Contains(l[0], "the ComplianceModel object did not load") || !strings.Contains(l[0], "acme") {
		t.Fatalf("one line for the invalid version: %q", l)
	}
	if s := score(); s.Total == nil || *s.Total != 50 {
		t.Fatalf("the previous model still scores: %+v", s)
	}

	publishModel(f, k8scache.EventDeleted, testComplianceModel(kyverno("require-probes", "require-requests"), 1))
	waitFor(t, time.Second, func() bool { return score().Total == nil })
	if reason() != compliance.ReasonModelNotLoaded {
		t.Fatalf("after the delete: %+v, reason %q", score(), reason())
	}
}

// TestCompliance_GoldenScorecard_CMP041 — the fixture of the CMP-032 golden,
// scored with the check weights of severity of the policy chart's model
// (critical 8, high 4, medium 2, low 1), each fixture check given the severity
// of the model's check it stands for: require-probes as probes-present (high),
// disallow-latest-tag as image-tag-pinned (high), require-requests as
// resource-requests (high), pvc-expand as pvc-volume-expansion (medium),
// flux-managed (high, of the evaluator here). The result is the committed
// testdata/golden/cmp-041-after.json; UPDATE_GOLDEN=1 writes it.
func TestCompliance_GoldenScorecard_CMP041(t *testing.T) {
	h, _, nats, f := newComplianceTestRig(t)
	m := testComplianceModel(goldenChecks(), 1)
	_ = unstructured.SetNestedField(m.Object, map[string]any{"critical": int64(8), "high": int64(4), "medium": int64(2), "low": int64(1)}, "spec", "severityWeights")
	checks, _, _ := unstructured.NestedSlice(m.Object, "spec", "checks")
	for _, x := range checks {
		if c := x.(map[string]any); c["id"] == "pvc-expand" {
			c["severity"] = "medium"
		}
	}
	_ = unstructured.SetNestedSlice(m.Object, checks, "spec", "checks")
	publishModel(f, k8scache.EventAdded, m)
	got := goldenScorecard(t, h, nats, f)
	const file = "testdata/golden/cmp-041-after.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(file, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil || string(got) != string(want) {
		t.Fatalf("scorecard differs from %s (%v):\n%s", file, err, got)
	}
	before, err := os.ReadFile("testdata/golden/cmp-032-after.json")
	if err != nil || string(before) == string(want) {
		t.Fatalf("the weights of severity must change the scorecard of the CMP-032 golden (%v)", err)
	}
}
