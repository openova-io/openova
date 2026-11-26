package adapters

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

var at = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

func reportObj(apiVersion string, scope map[string]any, results ...map[string]any) *unstructured.Unstructured {
	rs := make([]any, len(results))
	for i, r := range results {
		rs[i] = r
	}
	o := map[string]any{"apiVersion": apiVersion, "kind": "PolicyReport",
		"metadata": map[string]any{"namespace": "ns", "name": "rep"}, "results": rs}
	if scope != nil {
		o["scope"] = scope
	}
	return &unstructured.Unstructured{Object: o}
}

func res(policy, result, source, kind, name string) map[string]any {
	r := map[string]any{"policy": policy, "rule": policy + "-rule", "result": result, "message": "m"}
	if source != "" {
		r["source"] = source
	}
	if name != "" {
		r["resources"] = []any{map[string]any{"kind": kind, "namespace": "ns", "name": name}}
	}
	return r
}

// resolver knows one Pod owned by ReplicaSet web-1 of Deployment web.
func resolver() *compliance.CacheResolver {
	r := compliance.NewCacheResolver(nil)
	for _, o := range []map[string]any{
		{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"namespace": "ns", "name": "web-1",
			"ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "web", "controller": true}}}},
		{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "ns", "name": "web-1-a",
			"ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "web-1", "controller": true}}}},
	} {
		u := &unstructured.Unstructured{Object: o}
		r.Observe(k8scache.Event{Cluster: "c", Kind: strings.ToLower(u.GetKind()), Type: k8scache.EventAdded, Object: u})
	}
	return r
}

func TestPolicyReportFindings(t *testing.T) {
	long := strings.Repeat("x", 600)
	rep := reportObj("wgpolicyk8s.io/v1alpha2", nil,
		res("latest-tag", "fail", "kyverno", "Pod", "web-1-a"),
		res("requests", "skip", "", "Deployment", "web"),
		res("probes", "error", "", "Deployment", "web"),
		res("tls", "fail", "catalyst-conformance", "Deployment", "web"),
		map[string]any{"policy": "", "result": "pass"}, // no policy: no finding
		map[string]any{"policy": "x"},                  // no result: no finding
	)
	rep.Object["results"].([]any)[0].(map[string]any)["message"] = long
	fs := PolicyReport{}.Apply(k8scache.Event{Cluster: "c", Kind: "policyreport", Type: k8scache.EventAdded, Object: rep, At: at}, resolver())
	if len(fs) != 4 {
		t.Fatalf("want 4 findings, got %d: %+v", len(fs), fs)
	}
	web := compliance.SubjectKey{Cluster: "c", Namespace: "ns", Kind: "Deployment", Name: "web"}
	want := []struct {
		check  string
		src    compliance.Source
		result compliance.Result
	}{
		{"latest-tag", compliance.SourceKyverno, compliance.Fail},
		{"requests", compliance.SourceKyverno, compliance.NA},
		{"probes", compliance.SourceKyverno, compliance.NotMeasured},
		{"tls", compliance.Source("catalyst-conformance"), compliance.Fail},
	}
	for i, w := range want {
		f := fs[i]
		if f.Check != w.check || f.Source != w.src || f.Result != w.result || f.Subject != web || !f.ObservedAt.Equal(at) {
			t.Errorf("finding %d: %+v, want %+v on %+v", i, f, w, web)
		}
		if f.Properties["rule"] != w.check+"-rule" || f.Evidence.Name != "rep" || f.Evidence.Kind != "PolicyReport" {
			t.Errorf("finding %d rule or evidence: %+v", i, f)
		}
	}
	if len(fs[0].Message) != maxMessage {
		t.Errorf("message must be cut at %d, got %d", maxMessage, len(fs[0].Message))
	}
}

// TestPolicyReportValidatingPolicyResult — a result the engine writes for a
// ValidatingPolicy (measured by spike CMP-S1: source KyvernoValidatingPolicy,
// no rule) is a finding of the source kyverno with no rule property, so the
// check of that policy (source kyverno) keeps it after CMP-062 rewrites the
// policy in place.
func TestPolicyReportValidatingPolicyResult(t *testing.T) {
	r := res("multi-replica-drainability", "fail", "KyvernoValidatingPolicy", "Deployment", "web")
	delete(r, "rule")
	rep := reportObj("wgpolicyk8s.io/v1alpha2", nil, r)
	fs := PolicyReport{}.Apply(k8scache.Event{Cluster: "c", Kind: "policyreport", Type: k8scache.EventAdded, Object: rep, At: at}, resolver())
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(fs), fs)
	}
	if f := fs[0]; f.Check != "multi-replica-drainability" || f.Source != compliance.SourceKyverno || f.Result != compliance.Fail || f.Properties != nil {
		t.Errorf("want check multi-replica-drainability, source %q, result fail and no properties, got %+v", compliance.SourceKyverno, f)
	}
}

func TestPolicyReportReadsOnlyItsGroupAndNothingOnDelete(t *testing.T) {
	r := resolver()
	other := reportObj("example.io/v1", nil, res("a", "fail", "", "Deployment", "web"))
	if fs := (PolicyReport{}).Apply(k8scache.Event{Cluster: "c", Type: k8scache.EventAdded, Object: other}, r); fs != nil {
		t.Fatalf("a report of another group: %+v", fs)
	}
	own := reportObj("wgpolicyk8s.io/v1beta1", nil, res("a", "fail", "", "Deployment", "web"))
	if fs := (PolicyReport{}).Apply(k8scache.Event{Cluster: "c", Type: k8scache.EventDeleted, Object: own}, r); fs != nil {
		t.Fatalf("a deletion: %+v", fs)
	}
}

func TestEvaluatorFinding(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{
		"policy": "flux-managed", "rule": "flux-managed", "result": "fail", "message": "no label", "namespace": "ns",
		"resource": map[string]any{"apiVersion": "v1", "kind": "Pod", "name": "web-1-a"},
	}}
	ev := k8scache.Event{Cluster: "c", Kind: k8scache.KindComplianceEvaluator, Type: k8scache.EventAdded, Object: o, At: at}
	fs := Evaluator{}.Apply(ev, resolver())
	if len(fs) != 1 || fs[0].Source != compliance.SourceEvaluator || fs[0].Result != compliance.Fail || fs[0].Subject.Name != "web" || fs[0].Subject.Kind != "Deployment" {
		t.Fatalf("evaluator finding: %+v", fs)
	}
	if k := (Evaluator{}).SourceKey(ev); k.Name != "web-1-a" || k.Kind != "Pod" || !strings.HasSuffix(k.APIVersion, "/flux-managed") {
		t.Fatalf("source key: %+v", k)
	}
	ev.Type = k8scache.EventDeleted
	if fs := (Evaluator{}).Apply(ev, resolver()); fs != nil {
		t.Fatalf("a deletion: %+v", fs)
	}
}
