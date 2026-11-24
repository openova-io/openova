package compliance

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

//go:embed testdata/model-default.yaml
var defaultModelYAML []byte

// DefaultModel returns the model a cluster is scored with while no
// ComplianceModel object is loaded: one group and one axis, both "all", no
// mandatory check, a cap of 100 and no check of its own. The read model adds
// one check of weight 1 for every check it observes. DefaultModel panics when
// the embedded file does not load; TestDefaultModelLoads proves that it does.
func DefaultModel() *Model {
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(defaultModelYAML, &obj.Object); err != nil {
		panic(fmt.Sprintf("compliance: embedded default model: %v", err))
	}
	m, err := LoadModel(obj)
	if err != nil {
		panic(fmt.Sprintf("compliance: embedded default model: %v", err))
	}
	return m
}

// SourceKeyer is implemented by an adapter whose event objects have no name
// of their own. The read model keys the findings of such an event by
// SourceKey instead of the object's apiVersion, kind, namespace and name.
type SourceKeyer interface {
	SourceKey(ev k8scache.Event) ObjectRef
}

type clusterState struct {
	bySource map[ObjectRef][]Finding // the findings of each source object
	dropped  map[ObjectRef]int       // the results of each source object that no producer of the model owns
	refs     map[SubjectKey]int      // how many findings name each subject
	subjects map[SubjectKey]Subject  // every subject with a finding
}

// ReadModel holds, per cluster, the findings of every source object and the
// subjects they judge, and scores them with the package's rules. It is fed
// one informer event at a time and is safe for concurrent use.
type ReadModel struct {
	base     *Model
	resolver Resolver
	adapters map[string]Adapter

	mu       sync.RWMutex
	clusters map[string]*clusterState
}

// NewReadModel returns a read model that scores with base and feeds each
// event kind to the adapter that names it.
func NewReadModel(base *Model, r Resolver, adapters ...Adapter) *ReadModel {
	rm := &ReadModel{base: base, resolver: r, adapters: map[string]Adapter{}, clusters: map[string]*clusterState{}}
	for _, a := range adapters {
		for _, k := range a.Kinds() {
			rm.adapters[k] = a
		}
	}
	return rm
}

// Handles reports whether an adapter of the read model consumes kind.
func (rm *ReadModel) Handles(kind string) bool { return rm.adapters[kind] != nil }

// owned reports whether a producer of the model owns findings of source s.
func (rm *ReadModel) owned(s Source) bool {
	if s == SourceKyverno || s == SourceEvaluator {
		return true
	}
	for _, c := range rm.base.Checks {
		if c.Source == s {
			return true
		}
	}
	return false
}

func sourceKey(a Adapter, ev k8scache.Event) ObjectRef {
	if k, ok := a.(SourceKeyer); ok {
		return k.SourceKey(ev)
	}
	o := ev.Object
	return ObjectRef{APIVersion: o.GetAPIVersion(), Kind: o.GetKind(), Namespace: o.GetNamespace(), Name: o.GetName()}
}

// Apply replaces every finding of the event's object with what its adapter
// now returns. It returns the subjects the change touched that still have a
// finding, and those that lost their last one, both in key order. A finding
// whose source no producer of the model owns is dropped and counted.
func (rm *ReadModel) Apply(ev k8scache.Event) (current, gone []Subject) {
	a := rm.adapters[ev.Kind]
	if a == nil || ev.Object == nil {
		return nil, nil
	}
	ref := sourceKey(a, ev)
	var kept []Finding
	dropped := 0
	facts := map[SubjectKey]Subject{}
	for _, f := range a.Apply(ev, rm.resolver) {
		if !rm.owned(f.Source) {
			dropped++
			continue
		}
		kept = append(kept, f)
		if _, ok := facts[f.Subject]; !ok {
			facts[f.Subject] = rm.resolver.Subject(f.Subject)
		}
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	cs := rm.clusters[ev.Cluster]
	if cs == nil {
		cs = &clusterState{bySource: map[ObjectRef][]Finding{}, dropped: map[ObjectRef]int{}, refs: map[SubjectKey]int{}, subjects: map[SubjectKey]Subject{}}
		rm.clusters[ev.Cluster] = cs
	}
	touched := map[SubjectKey]bool{}
	for _, f := range cs.bySource[ref] {
		touched[f.Subject] = true
		cs.refs[f.Subject]--
	}
	for _, f := range kept {
		touched[f.Subject] = true
		cs.refs[f.Subject]++
	}
	if len(kept) == 0 {
		delete(cs.bySource, ref)
	} else {
		cs.bySource[ref] = kept
	}
	if dropped == 0 {
		delete(cs.dropped, ref)
	} else {
		cs.dropped[ref] = dropped
	}
	for k := range touched {
		if cs.refs[k] <= 0 {
			s, ok := cs.subjects[k]
			if !ok {
				s = Subject{Key: k}
			}
			delete(cs.refs, k)
			delete(cs.subjects, k)
			gone = append(gone, s)
			continue
		}
		if s, ok := facts[k]; ok {
			cs.subjects[k] = s
		}
		current = append(current, cs.subjects[k])
	}
	sortSubjects(current)
	sortSubjects(gone)
	return current, gone
}

// Refresh reads the facts of subject k again from the resolver, when k has a
// finding. It returns the subject and whether it is in view.
func (rm *ReadModel) Refresh(k SubjectKey) (Subject, bool) {
	s := rm.resolver.Subject(k)
	rm.mu.Lock()
	defer rm.mu.Unlock()
	cs := rm.clusters[k.Cluster]
	if cs == nil {
		return Subject{}, false
	}
	if _, ok := cs.subjects[k]; !ok {
		return Subject{}, false
	}
	cs.subjects[k] = s
	return s, true
}

// Dropped returns how many results in view of the cluster were dropped because
// no producer of the model owns their source.
func (rm *ReadModel) Dropped(cluster string) int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	n := 0
	if cs := rm.clusters[cluster]; cs != nil {
		for _, d := range cs.dropped {
			n += d
		}
	}
	return n
}

// view is a copy of one cluster: its subjects in key order, the findings of
// each subject in a fixed order, and the model that scores them.
type view struct {
	model     *Model
	subjects  []Subject
	bySubject map[SubjectKey][]Finding
}

func (rm *ReadModel) view(cluster string) view {
	rm.mu.RLock()
	cs := rm.clusters[cluster]
	v := view{bySubject: map[SubjectKey][]Finding{}}
	var all []Finding
	if cs != nil {
		for _, s := range cs.subjects {
			v.subjects = append(v.subjects, s)
		}
		for _, fs := range cs.bySource {
			all = append(all, fs...)
		}
	}
	rm.mu.RUnlock()
	sortSubjects(v.subjects)
	sort.SliceStable(all, func(i, j int) bool { return findingLess(all[i], all[j]) })
	for _, f := range all {
		v.bySubject[f.Subject] = append(v.bySubject[f.Subject], f)
	}
	v.model = withObserved(rm.base, all)
	return v
}

func (v view) findingsOf(subjects []Subject) []Finding {
	var fs []Finding
	for _, s := range subjects {
		fs = append(fs, v.bySubject[s.Key]...)
	}
	return fs
}

// withObserved returns base with one check of weight 1 on the first axis of
// type checks for every check id of fs that base does not hold and whose
// source has a producer today. Of the two producers the engine wins.
func withObserved(base *Model, fs []Finding) *Model {
	have := map[string]bool{}
	for _, c := range base.Checks {
		have[c.ID] = true
	}
	src := map[string]Source{}
	for _, f := range fs {
		if have[f.Check] || (f.Source != SourceKyverno && f.Source != SourceEvaluator) {
			continue
		}
		if s, ok := src[f.Check]; !ok || (s == SourceEvaluator && f.Source == SourceKyverno) {
			src[f.Check] = f.Source
		}
	}
	if len(src) == 0 {
		return base
	}
	axis := ""
	for _, a := range base.Axes {
		if a.Type == AxisTypeChecks {
			axis = a.ID
			break
		}
	}
	ids := make([]string, 0, len(src))
	for id := range src {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	m := *base
	m.Checks = append([]Check(nil), base.Checks...)
	for _, id := range ids {
		m.Checks = append(m.Checks, Check{ID: id, Title: id, Source: src[id], Axis: axis, Weight: 1})
	}
	return &m
}

// Checks returns the checks the cluster is scored with, in model order.
func (rm *ReadModel) Checks(cluster string) []Check {
	return append([]Check(nil), rm.view(cluster).model.Checks...)
}

// Rollup is the posture of one scope, with the Organization, Environment and
// Application of the first subject of the scope in key order.
type Rollup struct {
	Posture
	Organization, Environment, Application string
}

// Rollups returns the posture of the cluster and of every Application,
// Environment and Organization a subject names, ordered by scope and id. It
// returns nil when the cluster has no subject.
func (rm *ReadModel) Rollups(cluster string) []Rollup {
	v := rm.view(cluster)
	if len(v.subjects) == 0 {
		return nil
	}
	type scopeID struct{ scope, id string }
	groups := map[scopeID][]Subject{}
	for _, s := range v.subjects {
		for _, g := range []scopeID{{ScopeApplication, s.Application}, {ScopeEnvironment, s.Environment}, {ScopeOrganization, s.Organization}, {ScopeSovereign, cluster}} {
			if g.id != "" {
				groups[g] = append(groups[g], s)
			}
		}
	}
	out := make([]Rollup, 0, len(groups))
	for g, ss := range groups {
		out = append(out, Rollup{Posture: ScopePosture(v.model, g.scope, g.id, cluster, ss, v.findingsOf(ss), nil),
			Organization: ss[0].Organization, Environment: ss[0].Environment, Application: ss[0].Application})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope+"\x00"+out[i].ID < out[j].Scope+"\x00"+out[j].ID })
	return out
}

// CheckResult is the effective result of one check on one subject and the
// finding that decided it: the first finding of the check's source with that
// result, or a zero Finding when a waiver or an exemption decided it.
type CheckResult struct {
	Check   string
	Source  Source
	Result  Result
	Finding Finding
}

// SubjectView is the posture of one subject (scope workload) and the effective
// result of every check that has a finding of its own source on the subject.
type SubjectView struct {
	Subject   Subject
	Posture   Posture
	Results   []CheckResult // in model order
	UpdatedAt time.Time     // the newest ObservedAt of the subject's findings
}

func subjectView(v view, s Subject) SubjectView {
	fs := v.bySubject[s.Key]
	sv := SubjectView{Subject: s, Posture: ScopePosture(v.model, ScopeWorkload, s.Key.Name, s.Key.Cluster, []Subject{s}, fs, nil)}
	idx := indexFindings(fs)
	for _, f := range fs {
		if f.ObservedAt.After(sv.UpdatedAt) {
			sv.UpdatedAt = f.ObservedAt
		}
	}
	for i := range v.model.Checks {
		c := &v.model.Checks[i]
		var own []Finding
		for _, f := range idx[findingKey{subject: s.Key, check: c.ID}] {
			if f.Source == c.Source {
				own = append(own, f)
			}
		}
		if len(own) == 0 {
			continue
		}
		cr := CheckResult{Check: c.ID, Source: c.Source, Result: effectiveResult(v.model, s.Key, c, own, nil)}
		for _, f := range own {
			if f.Result == cr.Result {
				cr.Finding = f
				break
			}
		}
		sv.Results = append(sv.Results, cr)
	}
	return sv
}

// Subject returns the view of subject k, and false when k has no finding.
// The kind is matched without regard to case.
func (rm *ReadModel) Subject(k SubjectKey) (SubjectView, bool) {
	v := rm.view(k.Cluster)
	for _, s := range v.subjects {
		if s.Key.Namespace == k.Namespace && s.Key.Name == k.Name && strings.EqualFold(s.Key.Kind, k.Kind) {
			return subjectView(v, s), true
		}
	}
	return SubjectView{}, false
}

// Subjects returns the view of every subject of the cluster, in key order.
func (rm *ReadModel) Subjects(cluster string) []SubjectView {
	v := rm.view(cluster)
	out := make([]SubjectView, 0, len(v.subjects))
	for _, s := range v.subjects {
		out = append(out, subjectView(v, s))
	}
	return out
}

// sortKey orders subjects by cluster, kind, namespace and name.
func sortKey(k SubjectKey) string {
	return strings.Join([]string{k.Cluster, k.Kind, k.Namespace, k.Name}, "\x00")
}

func sortSubjects(ss []Subject) {
	sort.Slice(ss, func(i, j int) bool { return sortKey(ss[i].Key) < sortKey(ss[j].Key) })
}

// findingLess is a total order of findings: subject, check, evidence, message.
func findingLess(a, b Finding) bool {
	ka := strings.Join([]string{sortKey(a.Subject), a.Check, fmt.Sprint(a.Evidence), a.Message}, "\x00")
	return ka < strings.Join([]string{sortKey(b.Subject), b.Check, fmt.Sprint(b.Evidence), b.Message}, "\x00")
}
