package definitions

// The informer that loads the definitions from the cluster.
//
// The definitions of an installation are the YAML document under the key DataKey of one
// ConfigMap in the program's own namespace; the chart writes it, and its default name is
// ConfigMapName. An Informer watches that one object through the Kubernetes API. Each version it
// sees is parsed and checked whole; a valid document replaces the definitions in the Holder, so a
// change of a partner's state takes effect on the next call. A version that is invalid, that has
// no key DataKey, or that the accept function refuses is reported and changes nothing: the
// definitions in use stay. A deleted ConfigMap changes nothing either.

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// ConfigMapName is the default name of the ConfigMap that holds the definitions.
const ConfigMapName = "fingate-definitions"

// DataKey is the key of the ConfigMap's data that holds the definitions document.
const DataKey = "definitions.yaml"

// Informer keeps a Holder set to the definitions of one ConfigMap.
type Informer struct {
	factory informers.SharedInformerFactory
	name    string
	holder  *Holder
	accept  func(*Definitions) error
	logf    func(format string, a ...any)

	loaded chan struct{}
	once   sync.Once
}

// NewInformer returns an informer on the ConfigMap name in namespace that sets h. accept may be
// nil; when it returns an error for a valid document, that document is refused. logf receives
// one line for every version applied or refused; it may be nil.
func NewInformer(client kubernetes.Interface, namespace, name string, h *Holder, accept func(*Definitions) error, logf func(format string, a ...any)) *Informer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	selector := fields.OneTermEqualSelector("metadata.name", name).String()
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.FieldSelector = selector }))
	return &Informer{factory: factory, name: name, holder: h, accept: accept, logf: logf, loaded: make(chan struct{})}
}

// Loaded is closed when the Holder holds the first valid definitions.
func (i *Informer) Loaded() <-chan struct{} { return i.loaded }

// Run watches the ConfigMap until ctx ends.
func (i *Informer) Run(ctx context.Context) error {
	inf := i.factory.Core().V1().ConfigMaps().Informer()
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    i.apply,
		UpdateFunc: func(_, obj any) { i.apply(obj) },
		DeleteFunc: func(obj any) {
			if cm := configMap(obj); cm != nil && cm.Name == i.name {
				i.logf("definitions: ConfigMap %s/%s was deleted; the definitions in use stay", cm.Namespace, cm.Name)
			}
		},
	}); err != nil {
		return fmt.Errorf("definitions: %w", err)
	}
	i.factory.Start(ctx.Done())
	<-ctx.Done()
	i.factory.Shutdown()
	return nil
}

func configMap(obj any) *corev1.ConfigMap {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	cm, _ := obj.(*corev1.ConfigMap)
	return cm
}

// apply parses one version of the ConfigMap and sets the Holder when it is valid and accepted.
func (i *Informer) apply(obj any) {
	cm := configMap(obj)
	if cm == nil || cm.Name != i.name {
		return
	}
	at := fmt.Sprintf("ConfigMap %s/%s version %s", cm.Namespace, cm.Name, cm.ResourceVersion)
	raw, ok := cm.Data[DataKey]
	if !ok {
		i.logf("definitions: refused %s: it has no key %s; the definitions in use stay", at, DataKey)
		return
	}
	d, err := Parse([]byte(raw))
	if err == nil && i.accept != nil {
		err = i.accept(d)
	}
	if err != nil {
		i.logf("definitions: refused %s: %v; the definitions in use stay", at, err)
		return
	}
	i.holder.Set(d)
	i.once.Do(func() { close(i.loaded) }) // before the line is reported: its reader finds Loaded closed
	i.logf("definitions: loaded %s: profile %s, host %s, %d partners", at, d.Profile, d.Host, len(d.Partners))
}
