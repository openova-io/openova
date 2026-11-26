package compliance

import (
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// The labels a subject's Organization, Environment and Application are read
// from, first match wins. They are the labels the aggregator read before this
// package existed.
var (
	applicationLabels  = []string{"catalyst.openova.io/application", "openova.io/application", "app.kubernetes.io/instance", "app.kubernetes.io/name"}
	environmentLabels  = []string{"catalyst.openova.io/environment", "openova.io/environment"}
	organizationLabels = []string{"catalyst.openova.io/organization", "openova.io/organization"}
)

// ownerKinds are the controller kinds TopOwner walks through: a Pod to its
// ReplicaSet, Job, StatefulSet or DaemonSet; a ReplicaSet to its Deployment;
// a Job to its CronJob.
var ownerKinds = map[string]bool{
	"ReplicaSet": true, "Deployment": true, "StatefulSet": true,
	"DaemonSet": true, "Job": true, "CronJob": true,
}

// maxOwnerDepth bounds the walk; the longest real chain is Pod, Job, CronJob.
const maxOwnerDepth = 4

// Lister reads the objects of one kind from the informer cache of a cluster.
// *k8scache.Factory satisfies it.
type Lister interface {
	List(clusterID, kindName string, sel labels.Selector) ([]*unstructured.Unstructured, time.Duration, error)
}

type objectFacts struct {
	labels map[string]string
	owner  *ownerRef // the controller owner of a kind TopOwner walks through, nil when none
}

type ownerRef struct{ kind, name string }

// CacheResolver answers the questions of Resolver from the objects it was
// shown with Observe and, for an object it was not shown, from a Lister. It
// never calls an API server.
type CacheResolver struct {
	lister Lister
	now    func() time.Time

	mu      sync.RWMutex
	objects map[SubjectKey]objectFacts // Kind lower-cased
}

// NewCacheResolver returns a resolver over lister; lister may be nil.
func NewCacheResolver(lister Lister) *CacheResolver {
	return &CacheResolver{lister: lister, now: time.Now, objects: map[SubjectKey]objectFacts{}}
}

func lookupKey(cluster, namespace, kind, name string) SubjectKey {
	return SubjectKey{Cluster: cluster, Namespace: namespace, Kind: strings.ToLower(kind), Name: name}
}

func factsOf(obj *unstructured.Unstructured) objectFacts {
	f := objectFacts{labels: obj.GetLabels()}
	for _, o := range obj.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller && ownerKinds[o.Kind] && o.Name != "" {
			f.owner = &ownerRef{kind: o.Kind, name: o.Name}
			break
		}
	}
	return f
}

// Observe records the labels and the controller owner of the event's object,
// or forgets them when the event is a deletion.
func (r *CacheResolver) Observe(ev k8scache.Event) {
	if ev.Object == nil || ev.Object.GetName() == "" {
		return
	}
	k := lookupKey(ev.Cluster, ev.Object.GetNamespace(), ev.Object.GetKind(), ev.Object.GetName())
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.Type == k8scache.EventDeleted {
		delete(r.objects, k)
		return
	}
	r.objects[k] = factsOf(ev.Object)
}

func (r *CacheResolver) facts(cluster, namespace, kind, name string) (objectFacts, bool) {
	r.mu.RLock()
	f, ok := r.objects[lookupKey(cluster, namespace, kind, name)]
	r.mu.RUnlock()
	if ok || r.lister == nil || kind == "" {
		return f, ok
	}
	items, _, err := r.lister.List(cluster, strings.ToLower(kind), labels.Everything())
	if err != nil {
		return objectFacts{}, false
	}
	for _, it := range items {
		if it.GetName() == name && it.GetNamespace() == namespace {
			return factsOf(it), true
		}
	}
	return objectFacts{}, false
}

// TopOwner follows controller ownerReferences through the cache and returns
// the top controller, or the object itself when it has no controller owner or
// is not in the cache. The walk stops at an owner the cache does not hold.
func (r *CacheResolver) TopOwner(cluster, namespace, kind, name string) SubjectKey {
	k := SubjectKey{Cluster: cluster, Namespace: namespace, Kind: kind, Name: name}
	for i := 0; i < maxOwnerDepth; i++ {
		f, ok := r.facts(cluster, namespace, k.Kind, k.Name)
		if !ok || f.owner == nil {
			break
		}
		k.Kind, k.Name = f.owner.kind, f.owner.name
	}
	return k
}

// Subject returns k with the Organization, Environment and Application read
// from the labels of the object k names. Exposure is "other" for every
// subject.
func (r *CacheResolver) Subject(k SubjectKey) Subject {
	f, _ := r.facts(k.Cluster, k.Namespace, k.Kind, k.Name)
	s := Subject{
		Key:          k,
		Organization: labelOr(f.labels, organizationLabels),
		Environment:  labelOr(f.labels, environmentLabels),
		Application:  labelOr(f.labels, applicationLabels),
		Exposure:     "other",
	}
	if i := strings.LastIndex(s.Environment, "-"); i >= 0 {
		s.EnvironmentType = s.Environment[i+1:]
	}
	return s
}

// Now returns the time of the resolver's clock.
func (r *CacheResolver) Now() time.Time { return r.now() }

func labelOr(lbls map[string]string, keys []string) string {
	for _, k := range keys {
		if v := lbls[k]; v != "" {
			return v
		}
	}
	return ""
}
