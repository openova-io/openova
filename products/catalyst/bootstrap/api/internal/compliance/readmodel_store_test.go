package compliance

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// modelStore is an informer store that holds the ComplianceModel kind of the
// clusters of objs; List of any other cluster or kind fails, as the Factory's
// does for a cluster or kind it does not hold.
type modelStore struct {
	mu   sync.Mutex
	objs map[string][]*unstructured.Unstructured
}

func (s *modelStore) List(cluster, kind string, _ labels.Selector) ([]*unstructured.Unstructured, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	objs, ok := s.objs[cluster]
	if !ok || kind != k8scache.KindComplianceModel {
		return nil, 0, fmt.Errorf("k8scache: cluster %q kind %q not registered", cluster, kind)
	}
	return objs, 0, nil
}

func (s *modelStore) set(cluster string, objs ...*unstructured.Unstructured) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[cluster] = objs
}

// storedModel is testdata/model-stored.json: the object default as a
// Sovereign's API server returns it.
func storedModel(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile("testdata/model-stored.json")
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	return obj
}

// TestTheModelIsReadFromTheStore: with a store, the model of a cluster is its
// object in the store, read with no event: every check of the stored object
// weighs as its severity says. A cluster whose store holds no object has no
// model and no score; an object that does not load logs one line, once per
// version, and gives no model; a cluster the store does not hold keeps the
// model of its events.
func TestTheModelIsReadFromTheStore(t *testing.T) {
	var lines []string
	warn := func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }
	store := &modelStore{objs: map[string][]*unstructured.Unstructured{}}
	web := key("Deployment", "web")
	rm := NewReadModel(warn, observed(NewCacheResolver(nil)), fakeAdapter{by: map[string][]Finding{"r": {finding(web, "probes-present", SourceKyverno, Pass)}}}).ReadModelsFrom(store)

	obj := storedModel(t)
	store.set("c", obj)
	rm.Apply(report("r", k8scache.EventAdded))
	checks := rm.Checks("c")
	weights := map[string]int{"critical": 8, "high": 4, "medium": 2, "low": 1}
	if len(checks) != 22 {
		t.Fatalf("the stored model has 22 checks, the read model %d", len(checks))
	}
	for _, c := range checks {
		if c.Weight != weights[c.Severity] {
			t.Errorf("%s (%s): weight %d, the model %d", c.ID, c.Severity, c.Weight, weights[c.Severity])
		}
	}
	if v, ok := rm.ModelVersion("c"); !ok || v != "1.0.0" {
		t.Fatalf("model version %q %v", v, ok)
	}
	if v, _ := rm.Subject(web); value(v.Posture.Overall) != 100 || v.Reason != "" {
		t.Fatalf("the subject is scored by the stored model: %+v", v)
	}

	store.set("c")
	if v, _ := rm.Subject(web); v.Posture.Overall != nil || v.Reason != ReasonModelNotLoaded || rm.Checks("c") != nil {
		t.Fatalf("no object in the store, no model: %+v", v)
	}
	if got := gauge(t, "c"); got != 0 {
		t.Fatalf("catalyst_compliance_model_valid{cluster=c} = %v, want 0", got)
	}

	broken := obj.DeepCopy()
	_ = unstructured.SetNestedField(broken.Object, "one", "spec", "version")
	broken.SetResourceVersion("2")
	store.set("c", broken)
	for i := 0; i < 3; i++ {
		if rm.Checks("c") != nil {
			t.Fatalf("an object that does not load gives no model")
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "the ComplianceModel object did not load") || !strings.Contains(lines[0], "cluster c") {
		t.Fatalf("one line for the version that does not load: %q", lines)
	}

	ev := modelEvent(t, "d", k8scache.EventAdded, rigModel)
	if _, err := rm.ApplyModel(ev); err != nil || len(rm.Checks("d")) != 4 {
		t.Fatalf("a cluster the store does not hold takes the model of its events: %v %+v", err, rm.Checks("d"))
	}
}

// TestAModelEventReadsTheStore: with a store that holds the cluster, a model
// event only says that the model changed; the object in the store wins.
func TestAModelEventReadsTheStore(t *testing.T) {
	store := &modelStore{objs: map[string][]*unstructured.Unstructured{}}
	rm := NewReadModel(nil, observed(NewCacheResolver(nil))).ReadModelsFrom(store)
	inStore := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(rigModel), &inStore.Object); err != nil {
		t.Fatal(err)
	}
	store.set("c", inStore)
	if _, err := rm.ApplyModel(modelEvent(t, "c", k8scache.EventAdded, strings.Replace(rigModel, `version: "1.0.0"`, `version: "9.9.9"`, 1))); err != nil {
		t.Fatal(err)
	}
	if v, _ := rm.ModelVersion("c"); v != "1.0.0" {
		t.Fatalf("the store's object scores the cluster, not the event's: version %q", v)
	}
}
