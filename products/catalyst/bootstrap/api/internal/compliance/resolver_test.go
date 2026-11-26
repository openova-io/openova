package compliance

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// obj builds a namespaced object with labels and, when ownerKind is set, a
// controller owner.
func obj(kind, ns, name string, lbls map[string]any, ownerKind, ownerName string) *unstructured.Unstructured {
	meta := map[string]any{"namespace": ns, "name": name}
	if lbls != nil {
		meta["labels"] = lbls
	}
	if ownerKind != "" {
		meta["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": ownerKind, "name": ownerName, "uid": "u", "controller": true}}
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": kind, "metadata": meta}}
}

func observed(r *CacheResolver, objs ...*unstructured.Unstructured) *CacheResolver {
	for _, o := range objs {
		r.Observe(k8scache.Event{Cluster: "c", Kind: o.GetKind(), Type: k8scache.EventAdded, Object: o})
	}
	return r
}

// listOnly serves List from a fixed set of objects of one kind.
type listOnly struct {
	kind  string
	items []*unstructured.Unstructured
	calls int
}

func (l *listOnly) List(_, kind string, _ labels.Selector) ([]*unstructured.Unstructured, time.Duration, error) {
	l.calls++
	if kind != l.kind {
		return nil, 0, errors.New("kind not watched")
	}
	return l.items, 0, nil
}

func TestTopOwner(t *testing.T) {
	r := observed(NewCacheResolver(nil),
		obj("Deployment", "ns", "web", nil, "", ""),
		obj("ReplicaSet", "ns", "web-1", nil, "Deployment", "web"),
		obj("Pod", "ns", "web-1-a", nil, "ReplicaSet", "web-1"),
		obj("CronJob", "ns", "nightly", nil, "", ""),
		obj("Job", "ns", "nightly-1", nil, "CronJob", "nightly"),
		obj("Pod", "ns", "nightly-1-a", nil, "Job", "nightly-1"),
		obj("Pod", "ns", "bare", nil, "", ""),
		obj("Pod", "ns", "orphan", nil, "ReplicaSet", "unknown-rs"),
		obj("Pod", "ns", "node-owned", nil, "Node", "n1"),
	)
	cases := []struct{ kind, name, wantKind, wantName string }{
		{"Pod", "web-1-a", "Deployment", "web"},       // Pod, ReplicaSet, Deployment
		{"pod", "web-1-a", "Deployment", "web"},       // the kind is matched without regard to case
		{"Pod", "nightly-1-a", "CronJob", "nightly"},  // Pod, Job, CronJob
		{"Pod", "bare", "Pod", "bare"},                // no controller: the Pod itself
		{"Pod", "orphan", "ReplicaSet", "unknown-rs"}, // the walk stops at an owner the cache does not hold
		{"Pod", "node-owned", "Pod", "node-owned"},    // an owner kind the walk does not follow
		{"Deployment", "absent", "Deployment", "absent"},
	}
	for _, c := range cases {
		got := r.TopOwner("c", "ns", c.kind, c.name)
		if got.Kind != c.wantKind || got.Name != c.wantName || got.Cluster != "c" || got.Namespace != "ns" {
			t.Errorf("TopOwner(%s/%s) = %+v, want %s/%s", c.kind, c.name, got, c.wantKind, c.wantName)
		}
	}
}

func TestTopOwnerReadsTheListerForAnObjectNoEventShowed(t *testing.T) {
	l := &listOnly{kind: "replicaset", items: []*unstructured.Unstructured{obj("ReplicaSet", "ns", "web-1", nil, "Deployment", "web")}}
	r := observed(NewCacheResolver(l), obj("Pod", "ns", "web-1-a", nil, "ReplicaSet", "web-1"))
	if got := r.TopOwner("c", "ns", "Pod", "web-1-a"); got.Kind != "Deployment" || got.Name != "web" {
		t.Fatalf("TopOwner through the lister = %+v, want Deployment/web", got)
	}
	if l.calls == 0 {
		t.Fatalf("the lister was not asked for the ReplicaSet")
	}
}

func TestSubjectFacts(t *testing.T) {
	r := observed(NewCacheResolver(nil),
		obj("Deployment", "ns", "web", map[string]any{
			"catalyst.openova.io/organization": "acme",
			"catalyst.openova.io/environment":  "acme-prod",
			"app.kubernetes.io/instance":       "web",
		}, "", ""),
		obj("Deployment", "ns", "legacy", map[string]any{
			"openova.io/environment": "acme",
			"openova.io/application": "",
			"app.kubernetes.io/name": "legacy-app",
		}, "", ""),
	)
	s := r.Subject(SubjectKey{Cluster: "c", Namespace: "ns", Kind: "Deployment", Name: "web"})
	if s.Organization != "acme" || s.Environment != "acme-prod" || s.EnvironmentType != "prod" || s.Application != "web" || s.Exposure != "other" {
		t.Fatalf("Subject(web) = %+v", s)
	}
	s = r.Subject(SubjectKey{Cluster: "c", Namespace: "ns", Kind: "Deployment", Name: "legacy"})
	if s.Organization != "" || s.Environment != "acme" || s.EnvironmentType != "" || s.Application != "legacy-app" {
		t.Fatalf("Subject(legacy): an empty label is skipped, an environment with no dash has no type; got %+v", s)
	}
	s = r.Subject(SubjectKey{Cluster: "c", Namespace: "ns", Kind: "Deployment", Name: "absent"})
	if s.Application != "" || s.Exposure != "other" || s.Key.Name != "absent" {
		t.Fatalf("Subject(absent) = %+v", s)
	}
}

func TestObserveForgetsADeletedObject(t *testing.T) {
	rs := obj("ReplicaSet", "ns", "web-1", nil, "Deployment", "web")
	r := observed(NewCacheResolver(nil), rs, obj("Pod", "ns", "web-1-a", nil, "ReplicaSet", "web-1"))
	r.Observe(k8scache.Event{Cluster: "c", Kind: "replicaset", Type: k8scache.EventDeleted, Object: rs})
	if got := r.TopOwner("c", "ns", "Pod", "web-1-a"); got.Kind != "ReplicaSet" {
		t.Fatalf("after the ReplicaSet was deleted the walk must stop at it, got %+v", got)
	}
}
