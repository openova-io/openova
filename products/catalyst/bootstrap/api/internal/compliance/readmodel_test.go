package compliance

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// fakeAdapter returns, for an event of kind "report", the findings listed
// under the event object's name; nil for a deletion.
type fakeAdapter struct{ by map[string][]Finding }

func (fakeAdapter) Kinds() []string { return []string{"report"} }

func (a fakeAdapter) Apply(ev k8scache.Event, _ Resolver) []Finding {
	if ev.Type == k8scache.EventDeleted {
		return nil
	}
	return a.by[ev.Object.GetName()]
}

func key(kind, name string) SubjectKey {
	return SubjectKey{Cluster: "c", Namespace: "ns", Kind: kind, Name: name}
}

func finding(k SubjectKey, check string, src Source, r Result) Finding {
	return Finding{Check: check, Source: src, Subject: k, Result: r, Message: check + " " + string(r)}
}

func report(name string, typ k8scache.EventType) k8scache.Event {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "wgpolicyk8s.io/v1alpha2", "kind": "PolicyReport",
		"metadata": map[string]any{"namespace": "ns", "name": name}}}
	return k8scache.Event{Cluster: "c", Kind: "report", Type: typ, Object: o}
}

// rig returns a read model over the default model whose resolver knows the
// labels of the Deployments web and db.
func rig(by map[string][]Finding) *ReadModel {
	res := observed(NewCacheResolver(nil),
		obj("Deployment", "ns", "web", map[string]any{"catalyst.openova.io/application": "web",
			"catalyst.openova.io/environment": "acme-prod", "catalyst.openova.io/organization": "acme"}, "", ""),
		obj("Deployment", "ns", "db", map[string]any{"catalyst.openova.io/application": "db",
			"catalyst.openova.io/environment": "acme-prod", "catalyst.openova.io/organization": "acme"}, "", ""),
	)
	return NewReadModel(DefaultModel(), res, fakeAdapter{by: by})
}

func value(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func TestDefaultModelLoads(t *testing.T) {
	m := DefaultModel()
	if m.Version != "0.0.0" || len(m.Groups) != 1 || len(m.Axes) != 1 || m.Axes[0].ID != "all" || len(m.Checks) != 0 || m.MandatoryCap.Value != 100 {
		t.Fatalf("default model: %+v", m)
	}
}

func TestApplyReplacesTheFindingsOfOneSourceObject(t *testing.T) {
	web := key("Deployment", "web")
	by := map[string][]Finding{"r1": {finding(web, "a", SourceKyverno, Fail)}}
	rm := rig(by)
	current, gone := rm.Apply(report("r1", k8scache.EventAdded))
	if len(current) != 1 || current[0].Application != "web" || current[0].Environment != "acme-prod" || len(gone) != 0 {
		t.Fatalf("first apply: current %+v gone %+v", current, gone)
	}
	by["r1"] = []Finding{finding(web, "a", SourceKyverno, Pass)}
	rm.Apply(report("r1", k8scache.EventModified))
	v, ok := rm.Subject(web)
	if !ok || value(v.Posture.Overall) != 100 || len(v.Results) != 1 || v.Results[0].Result != Pass {
		t.Fatalf("the second report of r1 must replace the first: %+v", v)
	}
}

func TestDeleteRemovesTheSubjectAndReportsItGone(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(map[string][]Finding{"r1": {finding(web, "a", SourceKyverno, Fail)}})
	rm.Apply(report("r1", k8scache.EventAdded))
	current, gone := rm.Apply(report("r1", k8scache.EventDeleted))
	if len(current) != 0 || len(gone) != 1 || gone[0].Key != web || gone[0].Environment != "acme-prod" {
		t.Fatalf("delete: current %+v gone %+v", current, gone)
	}
	if _, ok := rm.Subject(web); ok {
		t.Fatalf("the subject stays after its last report was deleted")
	}
	if r := rm.Rollups("c"); r != nil {
		t.Fatalf("rollups of an empty cluster: %+v", r)
	}
}

func TestOneJudgeTheWorstFindingWins(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(map[string][]Finding{
		"on-deployment": {finding(web, "latest-tag", SourceKyverno, Pass)},
		"on-pod":        {finding(web, "latest-tag", SourceKyverno, Fail)},
	})
	rm.Apply(report("on-deployment", k8scache.EventAdded))
	rm.Apply(report("on-pod", k8scache.EventAdded))
	v, _ := rm.Subject(web)
	if len(v.Results) != 1 || v.Results[0].Result != Fail || v.Results[0].Finding.Message != "latest-tag fail" {
		t.Fatalf("one check, one result, the worst: %+v", v.Results)
	}
	if n, d := v.Posture.Axes[0].Num, v.Posture.Axes[0].Den; n != 0 || d != 1 || v.Posture.Counts.Fail != 1 {
		t.Fatalf("judged once: num %d den %d counts %+v", n, d, v.Posture.Counts)
	}
}

func TestAForeignSourceIsDroppedAndCounted(t *testing.T) {
	web := key("Deployment", "web")
	by := map[string][]Finding{"conf": {finding(web, "tls", Source("catalyst-conformance"), Fail)}}
	rm := rig(by)
	current, _ := rm.Apply(report("conf", k8scache.EventAdded))
	if len(current) != 0 || rm.Dropped("c") != 1 {
		t.Fatalf("a foreign result is no finding and is counted: current %+v dropped %d", current, rm.Dropped("c"))
	}
	if _, ok := rm.Subject(web); ok {
		t.Fatalf("a dropped result made a subject")
	}
	by["conf"] = nil
	rm.Apply(report("conf", k8scache.EventModified))
	if rm.Dropped("c") != 0 {
		t.Fatalf("the count follows the object: %d", rm.Dropped("c"))
	}
}

func TestNotApplicableAndNotMeasuredLeaveTheDenominator(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, NA), finding(web, "b", SourceKyverno, NotMeasured)}})
	rm.Apply(report("r", k8scache.EventAdded))
	v, _ := rm.Subject(web)
	if v.Posture.Overall != nil || v.Posture.Counts.NA != 1 || v.Posture.Counts.NotMeasured != 1 || v.Posture.Axes[0].Den != 0 {
		t.Fatalf("na and not-measured must not enter num or den: %+v", v.Posture)
	}
}

func TestRollupsOfEveryScope(t *testing.T) {
	web, db := key("Deployment", "web"), key("Deployment", "db")
	rm := rig(map[string][]Finding{"r": {
		finding(web, "a", SourceKyverno, Pass), finding(web, "b", SourceEvaluator, Fail),
		finding(db, "a", SourceKyverno, Pass),
	}})
	rm.Apply(report("r", k8scache.EventAdded))
	got := map[string]Rollup{}
	for _, r := range rm.Rollups("c") {
		got[r.Scope+"/"+r.ID] = r
	}
	want := map[string][3]int{ // overall, num, den
		"application/db": {100, 1, 1}, "application/web": {50, 1, 2},
		"environment/acme-prod": {66, 2, 3}, "organization/acme": {66, 2, 3}, "sovereign/c": {66, 2, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("scopes: %v", got)
	}
	for id, w := range want {
		r := got[id]
		if value(r.Overall) != w[0] || r.Axes[0].Num != w[1] || r.Axes[0].Den != w[2] {
			t.Errorf("%s: overall %d num %d den %d, want %v", id, value(r.Overall), r.Axes[0].Num, r.Axes[0].Den, w)
		}
	}
	if r := got["application/web"]; r.Environment != "acme-prod" || r.Organization != "acme" {
		t.Errorf("application refs: %+v", r)
	}
}

func TestObservedChecksWeighOneAndTheEngineWins(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(map[string][]Finding{"r": {
		finding(web, "z", SourceEvaluator, Pass), finding(web, "a", SourceEvaluator, Pass), finding(web, "a", SourceKyverno, Fail),
	}})
	rm.Apply(report("r", k8scache.EventAdded))
	cs := rm.Checks("c")
	if len(cs) != 2 || cs[0].ID != "a" || cs[0].Source != SourceKyverno || cs[1].ID != "z" || cs[1].Source != SourceEvaluator {
		t.Fatalf("observed checks: %+v", cs)
	}
	for _, c := range cs {
		if c.Weight != 1 || c.Axis != "all" {
			t.Fatalf("an observed check weighs 1 on axis all: %+v", c)
		}
	}
	v, _ := rm.Subject(web)
	if v.Results[0].Check != "a" || v.Results[0].Result != Fail {
		t.Fatalf("check a is judged by its own source only: %+v", v.Results)
	}
}

func TestRefreshReadsTheLabelsAgainAndTheKindMatchesInAnyCase(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass)}})
	rm.Apply(report("r", k8scache.EventAdded))
	rm.resolver.(*CacheResolver).Observe(k8scache.Event{Cluster: "c", Kind: "deployment", Type: k8scache.EventModified,
		Object: obj("Deployment", "ns", "web", map[string]any{"catalyst.openova.io/application": "web-2"}, "", "")})
	if s, ok := rm.Refresh(web); !ok || s.Application != "web-2" {
		t.Fatalf("refresh: %+v %v", s, ok)
	}
	if _, ok := rm.Refresh(key("Deployment", "absent")); ok {
		t.Fatalf("refresh of a subject with no finding")
	}
	if v, ok := rm.Subject(key("deployment", "web")); !ok || v.Subject.Application != "web-2" {
		t.Fatalf("lower-case kind: %+v %v", v, ok)
	}
}
