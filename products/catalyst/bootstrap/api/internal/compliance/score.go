package compliance

import "math"

// The score rules. Every number is read from the model.
//
// Rule 1, effective result. For a subject and a check, only findings whose
// Source equals the check's source count; findings of any other source are
// ignored. Of several findings of the right source the worst wins, in the
// order fail, warn, not-measured, pass, na. Then: a waiver that covers the
// subject and the check gives waived; else an exemption of the model for the
// subject's namespace gives exempt; else no finding gives not-measured.
//
// Rule 2, subject score. den is the sum of the weights of the checks whose
// effective result is pass, fail or warn; num is the sum of those that pass.
// Score = floor(100 * num / den), or nil when den is 0. A check of weight 0
// is counted in Counts and never in num or den. When a mandatory check fails
// and the subject's environment type is listed in the mandatory cap, the
// score is at most the cap value and capped is true.
//
// Rule 3, axis value. For an axis of type checks, num and den are the sums
// of rule 2 over every subject of the scope, restricted to the checks of the
// axis. Value = floor(100 * num / den), or nil with the axis naText when den
// is 0. Gap is the check of the axis with the largest sum of weights of fail
// and warn results. An axis of type measure has no value in this package:
// nil with its naText.
//
// Rule 4, overall. Each group's mean is the mean of its non-nil axis values.
// Overall = round(sum of group weight * group mean / sum of those group
// weights), over the groups with at least one non-nil axis. Partial is true
// when a group was left out; Overall is nil when every group was left out.
// The level is the first level whose min is at or below the value.
//
// Rule 5, never a false pass. not-measured, na, waived and exempt never enter
// a numerator or a denominator.

// rank orders results for rule 1: the lower the rank, the worse the result.
var rank = map[Result]int{Fail: 0, Warn: 1, NotMeasured: 2, Pass: 3, NA: 4}

type findingKey struct {
	subject SubjectKey
	check   string
}

func indexFindings(fs []Finding) map[findingKey][]Finding {
	idx := make(map[findingKey][]Finding, len(fs))
	for _, f := range fs {
		k := findingKey{subject: f.Subject, check: f.Check}
		idx[k] = append(idx[k], f)
	}
	return idx
}

// effectiveResult applies rule 1. A finding whose Result is not one of
// fail, warn, not-measured, pass or na counts as not-measured.
func effectiveResult(m *Model, k SubjectKey, c *Check, found []Finding, covers Covers) Result {
	worst := Result("")
	for _, f := range found {
		if f.Source != c.Source {
			continue
		}
		r := f.Result
		if _, known := rank[r]; !known {
			r = NotMeasured
		}
		if worst == "" || rank[r] < rank[worst] {
			worst = r
		}
	}
	switch {
	case covers != nil && covers(k, c.ID) == Waived:
		return Waived
	case m.exempts(k.Namespace, c.ID):
		return Exempt
	case worst == "":
		return NotMeasured
	}
	return worst
}

func (c *Counts) add(r Result) {
	switch r {
	case Pass:
		c.Pass++
	case Fail:
		c.Fail++
	case Warn:
		c.Warn++
	case NA:
		c.NA++
	case Waived:
		c.Waived++
	case Exempt:
		c.Exempt++
	default:
		c.NotMeasured++
	}
}

func (c *Counts) plus(o Counts) {
	c.Pass += o.Pass
	c.Fail += o.Fail
	c.Warn += o.Warn
	c.NA += o.NA
	c.Waived += o.Waived
	c.Exempt += o.Exempt
	c.NotMeasured += o.NotMeasured
}

// subjectResults returns the effective result of every check of the model,
// in the order of m.Checks, and their counts.
func subjectResults(m *Model, s Subject, idx map[findingKey][]Finding, covers Covers) ([]Result, Counts) {
	rs := make([]Result, len(m.Checks))
	var c Counts
	for i := range m.Checks {
		chk := &m.Checks[i]
		rs[i] = effectiveResult(m, s.Key, chk, idx[findingKey{subject: s.Key, check: chk.ID}], covers)
		c.add(rs[i])
	}
	return rs, c
}

// mandatoryCapApplies reports whether rule 2's cap applies to the subject.
func mandatoryCapApplies(m *Model, s Subject, rs []Result) bool {
	listed := false
	for _, t := range m.MandatoryCap.EnvironmentTypes {
		if t == s.EnvironmentType {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	for i := range m.Checks {
		if m.Checks[i].Mandatory && rs[i] == Fail {
			return true
		}
	}
	return false
}

// weighted adds the weight of a result to num and den (rules 2 and 5).
func weighted(r Result, w int, num, den *int) {
	if w == 0 {
		return
	}
	switch r {
	case Pass:
		*num += w
		*den += w
	case Fail, Warn:
		*den += w
	}
}

// SubjectScore applies rules 1, 2 and 5 to one subject. covers may be nil.
func SubjectScore(m *Model, s Subject, fs []Finding, covers Covers) (score *int, capped bool, c Counts) {
	rs, counts := subjectResults(m, s, indexFindings(fs), covers)
	num, den := 0, 0
	for i := range m.Checks {
		weighted(rs[i], m.Checks[i].Weight, &num, &den)
	}
	if den == 0 {
		return nil, false, counts
	}
	v := 100 * num / den
	if !mandatoryCapApplies(m, s, rs) {
		return &v, false, counts
	}
	if v > m.MandatoryCap.Value {
		v = m.MandatoryCap.Value
	}
	return &v, true, counts
}

// ScopePosture applies rules 1 to 5 to the subjects of one scope. The caller
// passes the subjects that belong to the scope. For the workload scope with
// one subject, the mandatory cap of rule 2 also bounds Overall and sets
// Capped. covers may be nil. OldestInput and StaleProducers are left empty:
// the read model fills them.
func ScopePosture(m *Model, scope, id, cluster string, subjects []Subject, fs []Finding, covers Covers) Posture {
	p := Posture{Scope: scope, ID: id, Cluster: cluster, ModelVersion: m.Version}
	idx := indexFindings(fs)
	results := make([][]Result, len(subjects))
	for si, s := range subjects {
		var c Counts
		results[si], c = subjectResults(m, s, idx, covers)
		p.Counts.plus(c)
	}

	groupValues := map[string][]int{}
	for _, ax := range m.Axes {
		av := AxisValue{ID: ax.ID}
		if ax.Type != AxisTypeChecks {
			av.Reason = ax.NAText
			p.Axes = append(p.Axes, av)
			continue
		}
		lost := make([]int, len(m.Checks))
		for si := range subjects {
			num, den := 0, 0
			for ci := range m.Checks {
				chk := &m.Checks[ci]
				if chk.Axis != ax.ID {
					continue
				}
				r := results[si][ci]
				weighted(r, chk.Weight, &num, &den)
				if r == Fail || r == Warn {
					lost[ci] += chk.Weight
				}
			}
			av.Num += num
			av.Den += den
			if den > 0 {
				av.Subjects++
			} else {
				av.NotMeasured++
			}
		}
		for ci := range m.Checks {
			if lost[ci] > av.GapLost {
				av.Gap, av.GapLost = m.Checks[ci].ID, lost[ci]
			}
		}
		if av.Den == 0 {
			av.Reason = ax.NAText
		} else {
			v := 100 * av.Num / av.Den
			av.Value = &v
			av.Level = m.level(v)
			groupValues[ax.Group] = append(groupValues[ax.Group], v)
		}
		p.Axes = append(p.Axes, av)
	}

	sum, weights := 0.0, 0
	for _, g := range m.Groups {
		vals := groupValues[g.ID]
		if len(vals) == 0 {
			p.Partial = true
			continue
		}
		total := 0
		for _, v := range vals {
			total += v
		}
		sum += float64(g.Weight) * float64(total) / float64(len(vals))
		weights += g.Weight
	}
	if weights == 0 {
		return p
	}
	overall := int(math.Round(sum / float64(weights)))
	if scope == ScopeWorkload && len(subjects) == 1 && mandatoryCapApplies(m, subjects[0], results[0]) {
		if overall > m.MandatoryCap.Value {
			overall = m.MandatoryCap.Value
		}
		p.Capped = true
	}
	p.Overall = &overall
	p.Level = m.level(overall)
	return p
}
