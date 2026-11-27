package compliance

import (
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

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

// rigModel: one group and one axis "all", every severity weighs 1, checks a
// and latest-tag of the engine, b and z of the evaluators, no exemption.
const rigModel = `apiVersion: compliance.openova.io/v1alpha1
kind: ComplianceModel
metadata:
  name: default
spec:
  version: "1.0.0"
  levels: [{name: L2, min: 50}, {name: L1, min: 0}]
  mandatoryCap: {value: 100, environmentTypes: []}
  severityWeights: {critical: 1, high: 1, medium: 1, low: 1}
  groups: [{id: all, title: All, weight: 1}]
  axes: [{id: all, title: All, group: all, type: checks, naText: Nothing in view.}]
  checks:
    - {id: a, title: a, source: kyverno, axis: all, severity: high, ownerSide: organization, valuesKey: a, description: a, remediation: Fix the thing the check names.}
    - {id: b, title: b, source: evaluator, axis: all, severity: high, ownerSide: organization, description: b, remediation: Fix the thing the check names.}
    - {id: latest-tag, title: t, source: kyverno, axis: all, severity: high, ownerSide: organization, valuesKey: t, description: t, remediation: Fix the thing the check names.}
    - {id: z, title: z, source: evaluator, axis: all, severity: high, ownerSide: organization, description: z, remediation: Fix the thing the check names.}
  exemptions: []
`

// modelEvent is an event of the ComplianceModel kind in the cluster; edit
// changes the object before it is sent.
func modelEvent(t *testing.T, cluster string, typ k8scache.EventType, text string) k8scache.Event {
	t.Helper()
	o := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(text), &o.Object); err != nil {
		t.Fatalf("model fixture: %v", err)
	}
	return k8scache.Event{Cluster: cluster, Kind: k8scache.KindComplianceModel, Type: typ, Object: o}
}

// bare returns a read model with no model whose resolver knows the labels of
// the Deployments web and db.
func bare(by map[string][]Finding) *ReadModel {
	res := observed(NewCacheResolver(nil),
		obj("Deployment", "ns", "web", map[string]any{"catalyst.openova.io/application": "web",
			"catalyst.openova.io/environment": "acme-prod", "catalyst.openova.io/organization": "acme"}, "", ""),
		obj("Deployment", "ns", "db", map[string]any{"catalyst.openova.io/application": "db",
			"catalyst.openova.io/environment": "acme-prod", "catalyst.openova.io/organization": "acme"}, "", ""),
	)
	return NewReadModel(nil, res, fakeAdapter{by: by})
}

// rig is bare with rigModel loaded into cluster c.
func rig(t *testing.T, by map[string][]Finding) *ReadModel {
	t.Helper()
	rm := bare(by)
	if _, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, rigModel)); err != nil {
		t.Fatalf("load the test model: %v", err)
	}
	return rm
}

func value(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func TestApplyReplacesTheFindingsOfOneSourceObject(t *testing.T) {
	web := key("Deployment", "web")
	by := map[string][]Finding{"r1": {finding(web, "a", SourceKyverno, Fail)}}
	rm := rig(t, by)
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
	rm := rig(t, map[string][]Finding{"r1": {finding(web, "a", SourceKyverno, Fail)}})
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
	rm := rig(t, map[string][]Finding{
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
	rm := rig(t, by)
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
	rm := rig(t, map[string][]Finding{"r": {finding(web, "a", SourceKyverno, NA), finding(web, "latest-tag", SourceKyverno, NotMeasured)}})
	rm.Apply(report("r", k8scache.EventAdded))
	v, _ := rm.Subject(web)
	// not-measured: the finding of latest-tag, and b and z, which have none.
	if v.Posture.Overall != nil || v.Posture.Counts.NA != 1 || v.Posture.Counts.NotMeasured != 3 || v.Posture.Axes[0].Den != 0 {
		t.Fatalf("na and not-measured must not enter num or den: %+v", v.Posture)
	}
}

func TestRollupsOfEveryScope(t *testing.T) {
	web, db := key("Deployment", "web"), key("Deployment", "db")
	rm := rig(t, map[string][]Finding{"r": {
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
		if value(r.Overall) != w[0] || r.Axes[0].Num != w[1] || r.Axes[0].Den != w[2] || r.Reason != "" || r.ModelVersion != "1.0.0" {
			t.Errorf("%s: overall %d num %d den %d reason %q version %q, want %v", id, value(r.Overall), r.Axes[0].Num, r.Axes[0].Den, r.Reason, r.ModelVersion, w)
		}
	}
	if r := got["application/web"]; r.Environment != "acme-prod" || r.Organization != "acme" {
		t.Errorf("application refs: %+v", r)
	}
}

// TestOnlyTheChecksOfTheModelAreScored: a check is judged by the source the
// model names, and a finding of a check the model does not hold is no result.
func TestOnlyTheChecksOfTheModelAreScored(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(t, map[string][]Finding{"r": {
		finding(web, "a", SourceEvaluator, Pass), finding(web, "a", SourceKyverno, Fail),
		finding(web, "not-in-the-model", SourceKyverno, Fail),
	}})
	rm.Apply(report("r", k8scache.EventAdded))
	var ids []string
	for _, c := range rm.Checks("c") {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "a,b,latest-tag,z" {
		t.Fatalf("the checks of the cluster are those of its model: %v", ids)
	}
	v, _ := rm.Subject(web)
	if len(v.Results) != 1 || v.Results[0].Check != "a" || v.Results[0].Source != SourceKyverno || v.Results[0].Result != Fail {
		t.Fatalf("only check a, judged by the engine: %+v", v.Results)
	}
	if v.Posture.Axes[0].Num != 0 || v.Posture.Axes[0].Den != 1 {
		t.Fatalf("one result in the denominator: %+v", v.Posture.Axes[0])
	}
}

func TestRefreshReadsTheLabelsAgainAndTheKindMatchesInAnyCase(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(t, map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass)}})
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

// TestNoModelNoScore: a cluster with findings and no model has subjects and
// rollups, every one with no value and the reason "model not loaded".
func TestNoModelNoScore(t *testing.T) {
	web := key("Deployment", "web")
	rm := bare(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass)}})
	rm.Apply(report("r", k8scache.EventAdded))
	v, ok := rm.Subject(web)
	if !ok || v.Posture.Overall != nil || v.Reason != ReasonModelNotLoaded || len(v.Results) != 0 || len(v.Posture.Axes) != 0 {
		t.Fatalf("subject without a model: %v %+v", ok, v)
	}
	rolls := rm.Rollups("c")
	if len(rolls) != 4 {
		t.Fatalf("rollups without a model: %+v", rolls)
	}
	for _, r := range rolls {
		if r.Overall != nil || r.Reason != ReasonModelNotLoaded {
			t.Errorf("%s/%s: overall %v reason %q", r.Scope, r.ID, r.Overall, r.Reason)
		}
	}
	if rm.Checks("c") != nil {
		t.Errorf("a cluster without a model has no checks: %+v", rm.Checks("c"))
	}
	if _, ok := rm.ModelVersion("c"); ok {
		t.Errorf("a cluster without a model has no model version")
	}
}

// TestApplyModelScoresItsOwnClusterOnly: the model of cluster c scores the
// subjects of c, and ApplyModel returns them; cluster d keeps no score.
func TestApplyModelScoresItsOwnClusterOnly(t *testing.T) {
	web, db := key("Deployment", "web"), key("Deployment", "db")
	other := SubjectKey{Cluster: "d", Namespace: "ns", Kind: "Deployment", Name: "web"}
	rm := bare(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass), finding(db, "a", SourceKyverno, Fail)},
		"rd": {finding(other, "a", SourceKyverno, Pass)}})
	rm.Apply(report("r", k8scache.EventAdded))
	ev := report("rd", k8scache.EventAdded)
	ev.Cluster = "d"
	rm.Apply(ev)
	current, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, rigModel))
	if err != nil || len(current) != 2 || current[0].Key != db || current[1].Key != web {
		t.Fatalf("ApplyModel returns the subjects of c in key order: %+v %v", current, err)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 100 || v.Reason != "" {
		t.Fatalf("c is scored: %+v", v)
	}
	if v, _ := rm.Subject(other); v.Posture.Overall != nil || v.Reason != ReasonModelNotLoaded {
		t.Fatalf("d is not: %+v", v)
	}
	if got := gauge(t, "c"); got != 1 {
		t.Fatalf("catalyst_compliance_model_valid{cluster=c} = %v, want 1", got)
	}
}

// TestAnInvalidModelKeepsThePreviousOne: a version that does not load leaves
// the cluster scored by the model it had, returns the error and sets the gauge
// to 0; with no model before, the cluster keeps no score.
func TestAnInvalidModelKeepsThePreviousOne(t *testing.T) {
	web := key("Deployment", "web")
	rm := bare(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass), finding(web, "latest-tag", SourceKyverno, Fail)}})
	rm.Apply(report("r", k8scache.EventAdded))
	broken := strings.Replace(rigModel, "{name: L1, min: 0}", "{name: L1, min: 60}", 1)
	if _, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, broken)); err == nil || !strings.Contains(err.Error(), "levels") {
		t.Fatalf("a model with unsorted levels loads: %v", err)
	}
	if v, _ := rm.Subject(web); v.Posture.Overall != nil || v.Reason != ReasonModelNotLoaded {
		t.Fatalf("no valid model was ever loaded: %+v", v)
	}
	if got := gauge(t, "c"); got != 0 {
		t.Fatalf("gauge after the first, invalid version: %v, want 0", got)
	}
	if _, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, rigModel)); err != nil {
		t.Fatal(err)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 50 {
		t.Fatalf("the valid version scores: %+v", v.Posture)
	}
	current, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventModified, broken))
	if err == nil || current != nil {
		t.Fatalf("an invalid update: current %+v err %v", current, err)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 50 || v.Posture.ModelVersion != "1.0.0" {
		t.Fatalf("the previous model still scores: %+v", v.Posture)
	}
	if got := gauge(t, "c"); got != 0 {
		t.Fatalf("gauge after an invalid update: %v, want 0", got)
	}
}

// TestADeletedModelLeavesNoScore and an object of another name changes nothing.
func TestADeletedModelLeavesNoScore(t *testing.T) {
	web := key("Deployment", "web")
	rm := rig(t, map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass)}})
	rm.Apply(report("r", k8scache.EventAdded))
	renamed := strings.Replace(rigModel, "name: default", "name: other", 1)
	if current, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventDeleted, renamed)); err != nil || current != nil {
		t.Fatalf("an object of another name: %+v %v", current, err)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 100 {
		t.Fatalf("deleting an object of another name changed the score: %+v", v.Posture)
	}
	current, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventDeleted, rigModel))
	if err != nil || len(current) != 1 || current[0].Key != web {
		t.Fatalf("a deleted model returns the subjects it scored: %+v %v", current, err)
	}
	if v, _ := rm.Subject(web); v.Posture.Overall != nil || v.Reason != ReasonModelNotLoaded {
		t.Fatalf("after the delete: %+v", v)
	}
	if got := gauge(t, "c"); got != 0 {
		t.Fatalf("gauge after the delete: %v, want 0", got)
	}
}

// TestTheModelsWeightsAndExemptions: the weight of a check is its severity's
// weight in the model, and a namespace the model exempts gives exempt, which
// leaves the denominator.
func TestTheModelsWeightsAndExemptions(t *testing.T) {
	web := key("Deployment", "web")
	other := SubjectKey{Cluster: "c", Namespace: "kube-system", Kind: "Deployment", Name: "coredns"}
	m := strings.Replace(rigModel, "severityWeights: {critical: 1, high: 1, medium: 1, low: 1}", "severityWeights: {critical: 8, high: 4, medium: 2, low: 1}", 1)
	m = strings.Replace(m, "source: kyverno, axis: all, severity: high, ownerSide: organization, valuesKey: t", "source: kyverno, axis: all, severity: medium, ownerSide: organization, valuesKey: t", 1)
	m = strings.Replace(m, "  exemptions: []\n", "  exemptions:\n    - {namespace: kube-system, checks: [a], reason: Platform namespace installed before the engine., owner: sovereign-admin}\n", 1)
	rm := bare(map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass), finding(web, "latest-tag", SourceKyverno, Fail),
		finding(other, "a", SourceKyverno, Fail), finding(other, "latest-tag", SourceKyverno, Pass)}})
	rm.Apply(report("r", k8scache.EventAdded))
	if _, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, m)); err != nil {
		t.Fatal(err)
	}
	v, _ := rm.Subject(web)
	if value(v.Posture.Overall) != 66 || v.Posture.Axes[0].Num != 4 || v.Posture.Axes[0].Den != 6 {
		t.Fatalf("a (high, 4) passes and latest-tag (medium, 2) fails: 4 of 6 gives 66, got %+v", v.Posture)
	}
	e, _ := rm.Subject(other)
	if e.Results[0].Check != "a" || e.Results[0].Result != Exempt || e.Posture.Counts.Exempt != 1 || value(e.Posture.Overall) != 100 || e.Posture.Axes[0].Den != 2 {
		t.Fatalf("check a is exempt in kube-system and leaves the denominator: %+v", e)
	}
}

// gauge reads catalyst_compliance_model_valid of the cluster.
func gauge(t *testing.T, cluster string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metricModelValid.WithLabelValues(cluster).Write(&m); err != nil {
		t.Fatalf("read the gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// TestApplyRoutesTheModelKindAndWarnsOnce: the read model handles the model
// kind itself; Apply of a model event loads it and returns the subjects of the
// cluster, and an object that does not load logs one line and changes nothing.
func TestApplyRoutesTheModelKindAndWarnsOnce(t *testing.T) {
	web := key("Deployment", "web")
	var lines []string
	warn := func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }
	rm := NewReadModel(warn, observed(NewCacheResolver(nil)), fakeAdapter{by: map[string][]Finding{"r": {finding(web, "a", SourceKyverno, Pass)}}})
	if !rm.Handles(k8scache.KindComplianceModel) || !rm.Handles("report") || rm.Handles("pod") {
		t.Fatalf("Handles: model %v report %v pod %v", rm.Handles(k8scache.KindComplianceModel), rm.Handles("report"), rm.Handles("pod"))
	}
	rm.Apply(report("r", k8scache.EventAdded))
	current, gone := rm.Apply(modelEvent(t, "c", k8scache.EventAdded, rigModel))
	if len(current) != 1 || current[0].Key != web || gone != nil || len(lines) != 0 {
		t.Fatalf("a model event: current %+v gone %+v lines %q", current, gone, lines)
	}
	current, gone = rm.Apply(modelEvent(t, "c", k8scache.EventModified, strings.Replace(rigModel, `version: "1.0.0"`, `version: "one"`, 1)))
	if current != nil || gone != nil || len(lines) != 1 || !strings.Contains(lines[0], "the ComplianceModel object did not load") || !strings.Contains(lines[0], "cluster c") {
		t.Fatalf("an invalid model event: current %+v gone %+v lines %q", current, gone, lines)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 100 {
		t.Fatalf("the valid model still scores: %+v", v.Posture)
	}
}
