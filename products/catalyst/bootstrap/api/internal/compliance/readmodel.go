package compliance

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ModelObjectName is the name of the one ComplianceModel object of a cluster
// that the read model reads; an object of another name is ignored.
const ModelObjectName = "default"

// ReasonModelNotLoaded is the reason of every score of a cluster that has no
// valid ComplianceModel object: no valid object was ever loaded, or the
// object was deleted. Such a score is null.
const ReasonModelNotLoaded = "model not loaded"

// metricModelValid is 1 for a cluster whose ComplianceModel object loaded,
// and 0 when its last version did not load (the read model keeps scoring the
// cluster with the model it had) or when it was deleted.
var metricModelValid = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "catalyst",
	Subsystem: "compliance",
	Name:      "model_valid",
	Help:      "1 when the cluster's ComplianceModel object named default loaded; 0 when its last version did not load or it was deleted.",
}, []string{"cluster"})

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

// ReadModel holds, per cluster, the model loaded from the cluster's own
// ComplianceModel object, the findings of every source object and the
// subjects they judge, and scores them with the package's rules. A cluster
// with no loaded model has subjects and findings but no score. It is fed one
// informer event at a time and is safe for concurrent use.
type ReadModel struct {
	resolver Resolver
	adapters map[string]Adapter
	warn     WarnFunc
	store    Lister // the informer store the model of a cluster is read from (ReadModelsFrom); nil: the events alone

	mu       sync.RWMutex
	models   map[string]*Model // the valid model of each cluster
	read     map[string]string // the uid and resourceVersion of the model object last read from the store, per cluster
	clusters map[string]*clusterState
}

// WarnFunc logs one line: a message and key-value pairs, as slog.Logger.Warn.
type WarnFunc func(msg string, args ...any)

// NewReadModel returns a read model that feeds each event kind to the
// adapter that names it, and the events of kind k8scache.KindComplianceModel
// to ApplyModel. No cluster has a model until ApplyModel loads one, or the
// store of ReadModelsFrom holds one. warn, when not nil, logs the one line of
// a model object that does not load.
func NewReadModel(warn WarnFunc, r Resolver, adapters ...Adapter) *ReadModel {
	rm := &ReadModel{resolver: r, adapters: map[string]Adapter{}, warn: warn, models: map[string]*Model{}, read: map[string]string{}, clusters: map[string]*clusterState{}}
	for _, a := range adapters {
		for _, k := range a.Kinds() {
			rm.adapters[k] = a
		}
	}
	return rm
}

// ReadModelsFrom makes store the source of the model of every cluster whose
// ComplianceModel kind it holds, and returns rm. The informer keeps its store
// exact; an event of the fanout may never reach the read model: an object the
// Factory hydrated from its disk snapshot and found unchanged at its first
// LIST is a resync, which an informer of resync 0 hands to no subscriber; an
// event sent before the aggregator subscribed reaches none; a full buffer
// drops the oldest. A model fed by the events alone was never loaded on a
// Sovereign whose catalyst-api restarted on its snapshot, and every check
// weighed 0 (the same class as the policy objects, CMP-023). A cluster whose
// kind the store does not hold (its List fails) keeps the model of the events.
func (rm *ReadModel) ReadModelsFrom(store Lister) *ReadModel {
	rm.store = store
	return rm
}

// stored returns the ComplianceModel objects the store holds for the
// cluster, and false when the store does not hold that kind of the cluster.
func (rm *ReadModel) stored(cluster string) ([]*unstructured.Unstructured, bool) {
	if rm.store == nil {
		return nil, false
	}
	objs, _, err := rm.store.List(cluster, k8scache.KindComplianceModel, nil)
	return objs, err == nil
}

// modelOf returns the model of the cluster, nil when it has none: the one
// its object in the store holds when the store holds the kind of the
// cluster, else the one its events loaded.
func (rm *ReadModel) modelOf(cluster string) *Model {
	if objs, held := rm.stored(cluster); held {
		m, err := rm.fromStore(cluster, objs)
		rm.warnModel(cluster, err)
		return m
	}
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.models[cluster]
}

// fromStore makes the object named ModelObjectName of objs, the cluster's
// ComplianceModel objects in the store, the model of the cluster and returns
// it; no such object leaves the cluster with no model. Each uid and
// resourceVersion is loaded once: when it does not load, the cluster keeps
// the model it had and the error is returned that once.
func (rm *ReadModel) fromStore(cluster string, objs []*unstructured.Unstructured) (*Model, error) {
	var obj *unstructured.Unstructured
	for _, o := range objs {
		if o.GetName() == ModelObjectName {
			obj = o
		}
	}
	version := ""
	if obj != nil {
		version = string(obj.GetUID()) + "/" + obj.GetResourceVersion()
	}
	rm.mu.RLock()
	m, seen := rm.models[cluster], rm.read[cluster]
	rm.mu.RUnlock()
	if obj != nil && seen == version {
		return m, nil
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if obj != nil && rm.read[cluster] == version {
		return rm.models[cluster], nil // read by another caller meanwhile
	}
	if obj == nil {
		delete(rm.models, cluster)
		delete(rm.read, cluster)
		metricModelValid.WithLabelValues(cluster).Set(0)
		return nil, nil
	}
	rm.read[cluster] = version
	loaded, err := LoadModel(obj)
	if err != nil {
		metricModelValid.WithLabelValues(cluster).Set(0)
		return rm.models[cluster], fmt.Errorf("cluster %s keeps its previous model: %w", cluster, err)
	}
	rm.models[cluster] = loaded
	metricModelValid.WithLabelValues(cluster).Set(1)
	return loaded, nil
}

// warnModel logs the one line of a model object of the cluster that did not
// load; a nil err logs nothing.
func (rm *ReadModel) warnModel(cluster string, err error) {
	if err != nil && rm.warn != nil {
		rm.warn("compliance: the ComplianceModel object did not load", "cluster", cluster, "name", ModelObjectName, "err", err)
	}
}

// subjectsOf returns every subject of the cluster, in key order.
func (rm *ReadModel) subjectsOf(cluster string) []Subject {
	rm.mu.RLock()
	var current []Subject
	if cs := rm.clusters[cluster]; cs != nil {
		for _, s := range cs.subjects {
			current = append(current, s)
		}
	}
	rm.mu.RUnlock()
	sortSubjects(current)
	return current
}

// Handles reports whether the read model consumes events of kind: the kinds
// of its adapters and the kind of the model objects.
func (rm *ReadModel) Handles(kind string) bool {
	return kind == k8scache.KindComplianceModel || rm.adapters[kind] != nil
}

// ApplyModel loads the ComplianceModel object of an event of kind
// k8scache.KindComplianceModel as the model of the event's cluster; a
// deleted object leaves the cluster with no model. An object not named
// ModelObjectName is ignored. When the object does not load, the cluster
// keeps the model it had and ApplyModel returns the error. Otherwise it
// returns every subject of the cluster, in key order: their scores changed.
// When the store of ReadModelsFrom holds the kind of the cluster, the event
// only says that the model changed: the object is read from the store.
func (rm *ReadModel) ApplyModel(ev k8scache.Event) ([]Subject, error) {
	if ev.Object == nil || ev.Object.GetName() != ModelObjectName {
		return nil, nil
	}
	if objs, held := rm.stored(ev.Cluster); held {
		_, err := rm.fromStore(ev.Cluster, objs)
		return rm.subjectsOf(ev.Cluster), err
	}
	var m *Model
	if ev.Type != k8scache.EventDeleted {
		loaded, err := LoadModel(ev.Object)
		if err != nil {
			metricModelValid.WithLabelValues(ev.Cluster).Set(0)
			return nil, fmt.Errorf("cluster %s keeps its previous model: %w", ev.Cluster, err)
		}
		m = loaded
	}
	rm.mu.Lock()
	if m == nil {
		delete(rm.models, ev.Cluster)
	} else {
		rm.models[ev.Cluster] = m
	}
	rm.mu.Unlock()
	if m == nil {
		metricModelValid.WithLabelValues(ev.Cluster).Set(0)
	} else {
		metricModelValid.WithLabelValues(ev.Cluster).Set(1)
	}
	return rm.subjectsOf(ev.Cluster), nil
}

// owned reports whether a producer owns findings of source s in the cluster:
// the engine and the evaluators always, any other source when a check of the
// cluster's model names it.
func (rm *ReadModel) owned(cluster string, s Source) bool {
	if s == SourceKyverno || s == SourceEvaluator {
		return true
	}
	if m := rm.modelOf(cluster); m != nil {
		for _, c := range m.Checks {
			if c.Source == s {
				return true
			}
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
// whose source no producer owns is dropped and counted. An event of kind
// k8scache.KindComplianceModel goes to ApplyModel; an object that does not
// load is logged with warn and changes nothing.
func (rm *ReadModel) Apply(ev k8scache.Event) (current, gone []Subject) {
	if ev.Kind == k8scache.KindComplianceModel {
		current, err := rm.ApplyModel(ev)
		rm.warnModel(ev.Cluster, err)
		return current, nil
	}
	a := rm.adapters[ev.Kind]
	if a == nil || ev.Object == nil {
		return nil, nil
	}
	ref := sourceKey(a, ev)
	var kept []Finding
	dropped := 0
	facts := map[SubjectKey]Subject{}
	for _, f := range a.Apply(ev, rm.resolver) {
		if !rm.owned(ev.Cluster, f.Source) {
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
// no producer owns their source.
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
// each subject in a fixed order, and the model that scores them (nil when
// the cluster has none).
type view struct {
	model     *Model
	subjects  []Subject
	bySubject map[SubjectKey][]Finding
}

func (rm *ReadModel) view(cluster string) view {
	m := rm.modelOf(cluster)
	rm.mu.RLock()
	cs := rm.clusters[cluster]
	v := view{model: m, bySubject: map[SubjectKey][]Finding{}}
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
	return v
}

func (v view) findingsOf(subjects []Subject) []Finding {
	var fs []Finding
	for _, s := range subjects {
		fs = append(fs, v.bySubject[s.Key]...)
	}
	return fs
}

// posture is the posture of a scope: scored by the view's model, or with no
// value and the reason ReasonModelNotLoaded when the view has no model.
func (v view) posture(scope, id, cluster string, subjects []Subject) (Posture, string) {
	if v.model == nil {
		return Posture{Scope: scope, ID: id, Cluster: cluster}, ReasonModelNotLoaded
	}
	return ScopePosture(v.model, scope, id, cluster, subjects, v.findingsOf(subjects), nil), ""
}

// Checks returns the checks the cluster is scored with, in model order; nil
// when the cluster has no model.
func (rm *ReadModel) Checks(cluster string) []Check {
	m := rm.view(cluster).model
	if m == nil {
		return nil
	}
	return append([]Check(nil), m.Checks...)
}

// ModelVersion returns spec.version of the cluster's model, and false when
// the cluster has none.
func (rm *ReadModel) ModelVersion(cluster string) (string, bool) {
	if m := rm.modelOf(cluster); m != nil {
		return m.Version, true
	}
	return "", false
}

// Rollup is the posture of one scope, with the Organization, Environment and
// Application of the first subject of the scope in key order. Reason says why
// the posture has no value when the cluster has no model.
type Rollup struct {
	Posture
	Organization, Environment, Application string
	Reason                                 string
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
		p, reason := v.posture(g.scope, g.id, cluster, ss)
		out = append(out, Rollup{Posture: p, Reason: reason,
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
// result of every check of the model that has a finding of its own source on
// the subject. Reason says why the posture has no value when the cluster has
// no model; Results is then empty.
type SubjectView struct {
	Subject   Subject
	Posture   Posture
	Reason    string
	Results   []CheckResult // in model order
	UpdatedAt time.Time     // the newest ObservedAt of the subject's findings
}

func subjectView(v view, s Subject) SubjectView {
	fs := v.bySubject[s.Key]
	sv := SubjectView{Subject: s}
	sv.Posture, sv.Reason = v.posture(ScopeWorkload, s.Key.Name, s.Key.Cluster, []Subject{s})
	for _, f := range fs {
		if f.ObservedAt.After(sv.UpdatedAt) {
			sv.UpdatedAt = f.ObservedAt
		}
	}
	if v.model == nil {
		return sv
	}
	idx := indexFindings(fs)
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
