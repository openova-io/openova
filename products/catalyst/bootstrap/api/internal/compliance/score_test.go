package compliance

import (
	"fmt"
	"testing"
)

// testModel: group run (axes reliability and capacity) and group security
// (axis exposure and the measure axis runtime). nodeport is mandatory; hpa
// has weight 0. kube-system is exempt from every check, monitoring from probes.
func testModel() *Model {
	return &Model{
		Version:         "1.0.0",
		Levels:          []Level{{"L5", 90}, {"L4", 75}, {"L3", 60}, {"L2", 40}, {"L1", 0}},
		MandatoryCap:    MandatoryCap{Value: 74, EnvironmentTypes: []string{"prod"}},
		SeverityWeights: SeverityWeights{Critical: 8, High: 4, Medium: 2, Low: 1},
		Groups:          []Group{{ID: "run", Title: "Run", Weight: 1}, {ID: "security", Title: "Security", Weight: 1}},
		Axes: []Axis{
			{ID: "reliability", Group: "run", Type: AxisTypeChecks, NAText: "No reliability result."},
			{ID: "capacity", Group: "run", Type: AxisTypeChecks, NAText: "No capacity result."},
			{ID: "exposure", Group: "security", Type: AxisTypeChecks, NAText: "No exposure result."},
			{ID: "runtime", Group: "security", Type: AxisTypeMeasure, Measure: "runtime-clean", NAText: "No runtime data."},
		},
		Checks: []Check{
			{ID: "replicas", Source: SourceKyverno, Axis: "reliability", Severity: "medium", Weight: 2},
			{ID: "probes", Source: SourceKyverno, Axis: "reliability", Severity: "high", Weight: 4},
			{ID: "requests", Source: SourceKyverno, Axis: "capacity", Severity: "high", Weight: 4},
			{ID: "hpa", Source: SourceEvaluator, Axis: "capacity", Severity: "low", Weight: 0},
			{ID: "nodeport", Source: SourceKyverno, Axis: "exposure", Severity: "critical", Weight: 8, Mandatory: true},
		},
		Exemptions: []Exemption{
			{Namespace: "kube-system", Checks: []string{"*"}},
			{Namespace: "monitoring", Checks: []string{"probes"}},
		},
	}
}

func subj(ns, name, envType string) Subject {
	return Subject{Key: SubjectKey{Cluster: "c1", Namespace: ns, Kind: "Deployment", Name: name}, EnvironmentType: envType}
}

func find(s Subject, check string, src Source, r Result) Finding {
	return Finding{Check: check, Source: src, Subject: s.Key, Result: r}
}

func kyv(s Subject, check string, r Result) Finding { return find(s, check, SourceKyverno, r) }

func waiveAll(SubjectKey, string) Result { return Waived }

func intp(v int) *int { return &v }

func fmtp(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func checkOf(m *Model, id string) *Check {
	for i := range m.Checks {
		if m.Checks[i].ID == id {
			return &m.Checks[i]
		}
	}
	panic("no check " + id)
}

// Rule 1, one case per sentence.
func TestRule1EffectiveResult(t *testing.T) {
	m := testModel()
	app := subj("shop", "web", "prod")
	cases := []struct {
		name   string
		s      Subject
		check  string
		fs     []Finding
		covers Covers
		want   Result
	}{
		{"only the check's source counts", app, "replicas", []Finding{find(app, "replicas", SourceEvaluator, Fail)}, nil, NotMeasured},
		{"a foreign finding beside the right one is ignored", app, "replicas", []Finding{find(app, "replicas", SourceEvaluator, Fail), kyv(app, "replicas", Pass)}, nil, Pass},
		{"worst wins: fail over warn", app, "replicas", []Finding{kyv(app, "replicas", Warn), kyv(app, "replicas", Fail)}, nil, Fail},
		{"worst wins: warn over not-measured", app, "replicas", []Finding{kyv(app, "replicas", NotMeasured), kyv(app, "replicas", Warn)}, nil, Warn},
		{"worst wins: not-measured over pass", app, "replicas", []Finding{kyv(app, "replicas", Pass), kyv(app, "replicas", NotMeasured)}, nil, NotMeasured},
		{"worst wins: pass over na", app, "replicas", []Finding{kyv(app, "replicas", NA), kyv(app, "replicas", Pass)}, nil, Pass},
		{"an unknown producer result counts as not-measured", app, "replicas", []Finding{kyv(app, "replicas", Result("error")), kyv(app, "replicas", Pass)}, nil, NotMeasured},
		{"a waiver gives waived, even over a fail", app, "replicas", []Finding{kyv(app, "replicas", Fail)}, waiveAll, Waived},
		{"a cover that does not return waived is no waiver", app, "replicas", []Finding{kyv(app, "replicas", Fail)}, func(SubjectKey, string) Result { return Exempt }, Fail},
		{"an exemption for every check gives exempt", subj("kube-system", "dns", "prod"), "replicas", []Finding{kyv(subj("kube-system", "dns", "prod"), "replicas", Fail)}, nil, Exempt},
		{"an exemption for one check gives exempt", subj("monitoring", "agent", ""), "probes", []Finding{kyv(subj("monitoring", "agent", ""), "probes", Fail)}, nil, Exempt},
		{"an exemption does not cover another check", subj("monitoring", "agent", ""), "replicas", []Finding{kyv(subj("monitoring", "agent", ""), "replicas", Fail)}, nil, Fail},
		{"a waiver comes before an exemption", subj("kube-system", "dns", "prod"), "replicas", nil, waiveAll, Waived},
		{"no finding gives not-measured", app, "replicas", nil, nil, NotMeasured},
		{"a finding of another subject does not count", app, "replicas", []Finding{kyv(subj("shop", "db", "prod"), "replicas", Fail)}, nil, NotMeasured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := indexFindings(tc.fs)
			got := effectiveResult(m, tc.s.Key, checkOf(m, tc.check), idx[findingKey{subject: tc.s.Key, check: tc.check}], tc.covers)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// Rule 2, one case per sentence.
func TestRule2SubjectScore(t *testing.T) {
	app := subj("shop", "web", "prod")
	dev := subj("shop", "web", "dev")
	lightCap := testModel()
	checkOf(lightCap, "nodeport").Weight = 1
	zeroCap := testModel()
	checkOf(zeroCap, "nodeport").Weight = 0
	cases := []struct {
		name       string
		m          *Model
		s          Subject
		fs         []Finding
		want       *int
		wantCapped bool
		wantCounts Counts
	}{
		{"num over den, floored", testModel(), app, []Finding{kyv(app, "replicas", Pass), kyv(app, "probes", Fail)}, intp(33), false,
			Counts{Pass: 1, Fail: 1, NotMeasured: 3}},
		{"warn is in the denominator only", testModel(), app, []Finding{kyv(app, "replicas", Pass), kyv(app, "probes", Warn)}, intp(33), false,
			Counts{Pass: 1, Warn: 1, NotMeasured: 3}},
		{"an empty denominator gives nil", testModel(), app, nil, nil, false, Counts{NotMeasured: 5}},
		{"a weight-0 check is counted, not scored", testModel(), app, []Finding{kyv(app, "replicas", Pass), find(app, "hpa", SourceEvaluator, Fail)}, intp(100), false,
			Counts{Pass: 1, Fail: 1, NotMeasured: 3}},
		{"a failing mandatory check in prod caps the score", lightCap, app,
			[]Finding{kyv(app, "replicas", Pass), kyv(app, "probes", Pass), kyv(app, "requests", Pass), kyv(app, "nodeport", Fail)}, intp(74), true,
			Counts{Pass: 3, Fail: 1, NotMeasured: 1}},
		{"the cap is set even when the score is already lower", testModel(), app,
			[]Finding{kyv(app, "replicas", Pass), kyv(app, "probes", Pass), kyv(app, "requests", Pass), kyv(app, "nodeport", Fail)}, intp(55), true,
			Counts{Pass: 3, Fail: 1, NotMeasured: 1}},
		{"the cap does not apply outside the listed environment types", lightCap, dev,
			[]Finding{kyv(dev, "replicas", Pass), kyv(dev, "probes", Pass), kyv(dev, "requests", Pass), kyv(dev, "nodeport", Fail)}, intp(90), false,
			Counts{Pass: 3, Fail: 1, NotMeasured: 1}},
		{"a nil score is never capped", zeroCap, app, []Finding{kyv(app, "nodeport", Fail)}, nil, false, Counts{Fail: 1, NotMeasured: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, capped, c := SubjectScore(tc.m, tc.s, tc.fs, nil)
			if fmtp(got) != fmtp(tc.want) || capped != tc.wantCapped || c != tc.wantCounts {
				t.Fatalf("got %s capped %v counts %+v, want %s capped %v counts %+v", fmtp(got), capped, c, fmtp(tc.want), tc.wantCapped, tc.wantCounts)
			}
		})
	}
}

func axisOf(p Posture, id string) AxisValue {
	for _, a := range p.Axes {
		if a.ID == id {
			return a
		}
	}
	panic("no axis " + id)
}

// Rule 3, one case per sentence.
func TestRule3AxisValue(t *testing.T) {
	m := testModel()
	s1, s2 := subj("shop", "web", "prod"), subj("shop", "api", "prod")
	fs := []Finding{
		kyv(s1, "replicas", Pass), kyv(s1, "probes", Fail),
		kyv(s2, "replicas", Pass), kyv(s2, "probes", Pass),
		find(s1, "hpa", SourceEvaluator, Fail),
	}
	p := ScopePosture(m, ScopeNamespace, "shop", "c1", []Subject{s1, s2}, fs, nil)

	rel := axisOf(p, "reliability")
	if fmtp(rel.Value) != "66" || rel.Num != 8 || rel.Den != 12 || rel.Level != "L3" || rel.Subjects != 2 || rel.NotMeasured != 0 {
		t.Errorf("reliability: sums of rule 2 over the subjects, floored: got %+v value %s", rel, fmtp(rel.Value))
	}
	if rel.Gap != "probes" || rel.GapLost != 4 {
		t.Errorf("reliability: gap is the check with the largest lost weight: got %q %d", rel.Gap, rel.GapLost)
	}
	capa := axisOf(p, "capacity")
	if capa.Value != nil || capa.Reason != "No capacity result." || capa.Level != "" || capa.NotMeasured != 2 || capa.Gap != "" {
		t.Errorf("capacity: an empty denominator gives nil with naText: got %+v", capa)
	}
	rt := axisOf(p, "runtime")
	if rt.Value != nil || rt.Reason != "No runtime data." || rt.Den != 0 {
		t.Errorf("runtime: a measure axis has no value here: got %+v", rt)
	}

	// Equal lost weight: the check that comes first in the model is the gap.
	tie := ScopePosture(m, ScopeNamespace, "shop", "c1", []Subject{s1, s2},
		[]Finding{kyv(s1, "replicas", Fail), kyv(s2, "replicas", Warn), kyv(s1, "probes", Fail)}, nil)
	if g := axisOf(tie, "reliability"); g.Gap != "replicas" || g.GapLost != 4 {
		t.Errorf("tie: got gap %q %d, want replicas 4", g.Gap, g.GapLost)
	}
}

// Rule 4, one case per sentence.
func TestRule4Overall(t *testing.T) {
	s1 := subj("shop", "web", "prod")
	base := []Finding{kyv(s1, "replicas", Pass), kyv(s1, "probes", Fail), kyv(s1, "nodeport", Pass)}
	// reliability 2/6 = 33, capacity nil, exposure 100.

	p := ScopePosture(testModel(), ScopeNamespace, "shop", "", []Subject{s1}, base, nil)
	if fmtp(p.Overall) != "67" || p.Level != "L3" || p.Partial {
		t.Errorf("weighted mean of group means, rounded: got %s %q partial %v", fmtp(p.Overall), p.Level, p.Partial)
	}

	weighted := testModel()
	weighted.Groups[0].Weight = 3
	p = ScopePosture(weighted, ScopeNamespace, "shop", "", []Subject{s1},
		[]Finding{kyv(s1, "replicas", Pass), kyv(s1, "probes", Pass), kyv(s1, "requests", Fail), kyv(s1, "nodeport", Pass)}, nil)
	// run: (100 + 0) / 2 = 50; security: 100; (3*50 + 100) / 4 = 62.5, rounded 63.
	if fmtp(p.Overall) != "63" {
		t.Errorf("group weights and rounding: got %s, want 63", fmtp(p.Overall))
	}

	p = ScopePosture(testModel(), ScopeNamespace, "shop", "", []Subject{s1}, base[:2], nil)
	if fmtp(p.Overall) != "33" || !p.Partial || p.Level != "L1" {
		t.Errorf("a group with no measured axis is left out and sets Partial: got %s partial %v level %q", fmtp(p.Overall), p.Partial, p.Level)
	}

	p = ScopePosture(testModel(), ScopeNamespace, "shop", "", []Subject{s1}, nil, nil)
	if p.Overall != nil || !p.Partial || p.Level != "" {
		t.Errorf("every group left out gives nil: got %s partial %v level %q", fmtp(p.Overall), p.Partial, p.Level)
	}

	m := testModel()
	for _, tc := range []struct {
		v    int
		want string
	}{{100, "L5"}, {90, "L5"}, {89, "L4"}, {75, "L4"}, {74, "L3"}, {40, "L2"}, {39, "L1"}, {0, "L1"}} {
		if got := m.level(tc.v); got != tc.want {
			t.Errorf("level(%d) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// The mandatory cap bounds Overall in the workload scope only.
func TestWorkloadCap(t *testing.T) {
	m := testModel()
	m.Groups[0].Weight = 3
	prod, dev := subj("shop", "web", "prod"), subj("shop", "web", "dev")
	fs := func(s Subject) []Finding {
		return []Finding{kyv(s, "replicas", Pass), kyv(s, "probes", Pass), kyv(s, "requests", Pass), kyv(s, "nodeport", Fail)}
	}
	// run 100, security 0: (3*100 + 0) / 4 = 75.
	p := ScopePosture(m, ScopeWorkload, "web", "c1", []Subject{prod}, fs(prod), nil)
	if fmtp(p.Overall) != "74" || !p.Capped || p.Level != "L3" {
		t.Errorf("prod workload: got %s capped %v level %q, want 74 true L3", fmtp(p.Overall), p.Capped, p.Level)
	}
	p = ScopePosture(m, ScopeWorkload, "web", "c1", []Subject{dev}, fs(dev), nil)
	if fmtp(p.Overall) != "75" || p.Capped {
		t.Errorf("dev workload: got %s capped %v, want 75 false", fmtp(p.Overall), p.Capped)
	}
	p = ScopePosture(m, ScopeNamespace, "shop", "c1", []Subject{prod}, fs(prod), nil)
	if fmtp(p.Overall) != "75" || p.Capped {
		t.Errorf("namespace scope: got %s capped %v, want 75 false", fmtp(p.Overall), p.Capped)
	}
	if p.ModelVersion != "1.0.0" || p.Scope != ScopeNamespace || p.ID != "shop" || p.Cluster != "c1" {
		t.Errorf("identity fields: %+v", p)
	}
	if p.Counts != (Counts{Pass: 3, Fail: 1, NotMeasured: 1}) {
		t.Errorf("counts: %+v", p.Counts)
	}
}

// Rule 5: not-measured, na, waived and exempt never enter a numerator or a
// denominator. Each variant scores like the check being absent.
func TestRule5NeverAFalsePass(t *testing.T) {
	m := testModel()
	app := subj("shop", "web", "prod")
	mon := subj("monitoring", "agent", "prod")
	cases := []struct {
		name   string
		s      Subject
		fs     []Finding
		covers Covers
	}{
		{"not-measured", app, []Finding{kyv(app, "replicas", Pass), kyv(app, "probes", NotMeasured)}, nil},
		{"na", app, []Finding{kyv(app, "replicas", Pass), kyv(app, "probes", NA)}, nil},
		{"waived", app, []Finding{kyv(app, "replicas", Pass), kyv(app, "probes", Fail)},
			func(_ SubjectKey, check string) Result {
				if check == "probes" {
					return Waived
				}
				return ""
			}},
		{"exempt", mon, []Finding{kyv(mon, "replicas", Pass), kyv(mon, "probes", Fail)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := SubjectScore(m, tc.s, tc.fs, tc.covers)
			if fmtp(got) != "100" {
				t.Fatalf("score %s, want 100: the %s check must not enter num or den", fmtp(got), tc.name)
			}
			p := ScopePosture(m, ScopeNamespace, tc.s.Key.Namespace, "", []Subject{tc.s}, tc.fs, tc.covers)
			if a := axisOf(p, "reliability"); a.Num != 2 || a.Den != 2 {
				t.Fatalf("axis num %d den %d, want 2 2", a.Num, a.Den)
			}
			only, _, _ := SubjectScore(m, tc.s, tc.fs[1:], tc.covers)
			if only != nil {
				t.Fatalf("with only the %s check the score is %s, want nil", tc.name, fmtp(only))
			}
		})
	}
}

// next is a small deterministic generator for the property test: it
// advances the state x and returns a number in 0..n-1.
func next(x *int, n int) int {
	*x = (*x*1103515245 + 12345) & 0x7fffffff
	return (*x >> 16) % n
}

// raised reports whether after is a higher score than before.
func raised(before, after *int) bool {
	if after == nil {
		return false
	}
	return before == nil || *after > *before
}

// Property: replacing any pass by not-measured or na never raises a score.
func TestPropertyNoPassRemovalRaisesAScore(t *testing.T) {
	m := testModel()
	m.Groups[0].Weight = 2
	results := []Result{Pass, Fail, Warn, NA, NotMeasured, Waived, Exempt, Result("error")}
	sources := []Source{SourceKyverno, SourceEvaluator}
	namespaces := []string{"shop", "monitoring", "kube-system"}
	envTypes := []string{"prod", "dev", ""}
	seed := 20261113
	for round := 0; round < 3000; round++ {
		var subjects []Subject
		var fs []Finding
		for i, n := 0, 1+next(&seed, 3); i < n; i++ {
			s := subj(namespaces[next(&seed, len(namespaces))], fmt.Sprintf("w%d", i), envTypes[next(&seed, len(envTypes))])
			subjects = append(subjects, s)
			for _, c := range m.Checks {
				for j, k := 0, next(&seed, 3); j < k; j++ {
					fs = append(fs, find(s, c.ID, sources[next(&seed, len(sources))], results[next(&seed, len(results))]))
				}
			}
		}
		waivedCheck := m.Checks[next(&seed, len(m.Checks))].ID
		covers := func(_ SubjectKey, check string) Result {
			if check == waivedCheck {
				return Waived
			}
			return ""
		}
		scope := ScopeNamespace
		if len(subjects) == 1 {
			scope = ScopeWorkload
		}
		before := ScopePosture(m, scope, "x", "", subjects, fs, covers)
		for i := range fs {
			if fs[i].Result != Pass {
				continue
			}
			for _, repl := range []Result{NotMeasured, NA} {
				changed := append([]Finding(nil), fs...)
				changed[i].Result = repl
				for _, s := range subjects {
					b, _, _ := SubjectScore(m, s, fs, covers)
					a, _, _ := SubjectScore(m, s, changed, covers)
					if raised(b, a) {
						t.Fatalf("case %d: subject %v score rose from %s to %s when finding %d became %s", round, s.Key, fmtp(b), fmtp(a), i, repl)
					}
				}
				after := ScopePosture(m, scope, "x", "", subjects, changed, covers)
				if raised(before.Overall, after.Overall) {
					t.Fatalf("case %d: overall rose from %s to %s when finding %d became %s", round, fmtp(before.Overall), fmtp(after.Overall), i, repl)
				}
				for k := range before.Axes {
					if raised(before.Axes[k].Value, after.Axes[k].Value) {
						t.Fatalf("case %d: axis %s rose from %s to %s when finding %d became %s", round, before.Axes[k].ID, fmtp(before.Axes[k].Value), fmtp(after.Axes[k].Value), i, repl)
					}
				}
			}
		}
	}
}
