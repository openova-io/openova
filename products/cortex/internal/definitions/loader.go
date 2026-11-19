package definitions

// The loader: an informer on the one ConfigMap of the definitions, and a rate-limited queue of
// one key in front of ApplyDefinitions. Each event puts the key in the queue; the worker reads
// the newest version from the informer's cache, so versions that arrive while one is applied
// are folded into one. A version that is refused is not tried again. A version that the store
// fails to apply is tried again with growing delays until it is applied or a newer one
// replaces it. Nothing here polls: the informer's watch and the queue's delays drive it.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// Applier receives every valid version of the sources. The store opened with the index role is one.
type Applier interface {
	ApplyDefinitions(ctx context.Context, defs []store.SourceDefinition) error
}

// Loader keeps the store's sources equal to the definitions of one ConfigMap.
type Loader struct {
	factory   informers.SharedInformerFactory
	namespace string
	name      string
	apply     Applier
	logf      func(format string, a ...any)
	queue     workqueue.TypedRateLimitingInterface[string]

	applied chan struct{}
	once    sync.Once
}

// NewLoader returns a loader of the ConfigMap name in namespace. logf receives one line for
// every version applied, refused or retried, and never the document; it may be nil.
func NewLoader(client kubernetes.Interface, namespace, name string, apply Applier, logf func(format string, a ...any)) *Loader {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	selector := fields.OneTermEqualSelector("metadata.name", name).String()
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.FieldSelector = selector }))
	return &Loader{factory: factory, namespace: namespace, name: name, apply: apply, logf: logf,
		queue:   workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		applied: make(chan struct{})}
}

// Applied is closed when the first version has been applied.
func (l *Loader) Applied() <-chan struct{} { return l.applied }

// Run watches the ConfigMap and applies its versions until ctx ends.
func (l *Loader) Run(ctx context.Context) error {
	cms := l.factory.Core().V1().ConfigMaps()
	enqueue := func(obj any) {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == l.name {
			l.queue.Add(l.name)
		}
	}
	if _, err := cms.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		UpdateFunc: func(_, obj any) { enqueue(obj) },
		DeleteFunc: func(any) {
			l.logf("definitions: ConfigMap %s/%s was deleted; the sources in use stay", l.namespace, l.name)
		},
	}); err != nil {
		return fmt.Errorf("definitions: %w", err)
	}
	l.factory.Start(ctx.Done())
	defer l.factory.Shutdown()
	if !cache.WaitForCacheSync(ctx.Done(), cms.Informer().HasSynced) {
		return errors.New("definitions: the informer did not sync before the end")
	}
	go func() {
		<-ctx.Done()
		l.queue.ShutDown()
	}()
	get := cms.Lister().ConfigMaps(l.namespace).Get
	for l.next(ctx, get) {
		continue
	}
	return nil
}

// next applies the newest version once. It returns false when the queue was shut down.
func (l *Loader) next(ctx context.Context, get func(string) (*corev1.ConfigMap, error)) bool {
	key, quit := l.queue.Get()
	if quit {
		return false
	}
	defer l.queue.Done(key)
	cm, err := get(key)
	if err != nil {
		l.queue.Forget(key)
		return true
	}
	at := fmt.Sprintf("ConfigMap %s/%s version %s", cm.Namespace, cm.Name, cm.ResourceVersion)
	raw, ok := cm.Data[DataKey]
	if !ok {
		err = fmt.Errorf("it has no key %s", DataKey)
	}
	var defs []store.SourceDefinition
	if err == nil {
		defs, err = Parse([]byte(raw))
	}
	if err != nil {
		l.queue.Forget(key)
		l.logf("definitions: refused %s: %v; the sources in use stay", at, err)
		return true
	}
	if err := l.apply.ApplyDefinitions(ctx, defs); err != nil {
		l.logf("definitions: %s was not applied: %v; it is tried again", at, err)
		l.queue.AddRateLimited(key)
		return true
	}
	l.queue.Forget(key)
	l.once.Do(func() { close(l.applied) })
	l.logf("definitions: applied %s: %d sources", at, len(defs))
	return true
}
