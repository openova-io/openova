// factory_optional_test.go — CMP-016: an optional kind starts when its
// definition arrives.
//
// AddCluster probes an optional kind once (#5352). Before CMP-016 a kind
// whose CustomResourceDefinition was installed after that probe was never
// watched until the process restarted. Now the skipped kind is remembered,
// and the ADDED or MODIFIED event of its definition, delivered by the
// customresourcedefinition informer, starts its informer. No timer, no
// retry probe.
package k8scache

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
)

var (
	optCRDGVR    = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	optWidgetGVR = schema.GroupVersionResource{Group: "example.openova.io", Version: "v1", Resource: "widgets"}
)

const optStartedLine = "k8scache: optional kind started after its definition arrived"

// optLogBuffer is a goroutine-safe sink for the factory's log lines.
type optLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *optLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *optLogBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// optWidgetCRD is the definition of example.openova.io widgets; version v1
// is served when served is true.
func optWidgetCRD(served bool, rv string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name":            "widgets.example.openova.io",
			"resourceVersion": rv,
		},
		"spec": map[string]any{
			"group": "example.openova.io",
			"names": map[string]any{"plural": "widgets", "kind": "Widget"},
			"scope": "Namespaced",
			"versions": []any{
				map[string]any{"name": "v1", "served": served, "storage": true},
			},
		},
	}}
}

func optWidget(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.openova.io/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"namespace": "ns-a", "name": name},
	}}
}

// optFactory starts a factory with one cluster "alpha" whose discovery
// serves nothing, so the optional kind "widget" is skipped at AddCluster.
// The registry holds the customresourcedefinition kind and "widget".
func optFactory(t *testing.T, objs ...runtime.Object) (*Factory, *dynamicfake.FakeDynamicClient, *optLogBuffer, <-chan Event) {
	t.Helper()
	r := NewRegistry()
	_ = r.Add(Kind{Name: "customresourcedefinition", GVR: optCRDGVR, Namespaced: false})
	_ = r.Add(Kind{Name: "widget", GVR: optWidgetGVR, Namespaced: true, Optional: true})

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		optCRDGVR:    "CustomResourceDefinitionList",
		optWidgetGVR: "WidgetList",
	}, objs...)
	core := kfake.NewSimpleClientset() // no Resources: every discovery probe is NotFound

	logs := &optLogBuffer{}
	f, err := NewFactory(Config{
		Logger:   slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		Registry: r,
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Stop)
	ch, unsub := f.Subscribe("operator", map[string]struct{}{"customresourcedefinition": {}, "widget": {}})
	t.Cleanup(unsub)
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f, dyn, logs, ch
}

// optWaitEvent reads ch until an event of kind, type and object name
// arrives, or fails after 5 seconds.
func optWaitEvent(t *testing.T, ch <-chan Event, kind string, typ EventType, name string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("subscriber channel closed while waiting for %s %s %s", kind, typ, name)
			}
			if ev.Kind == kind && ev.Type == typ && ev.Object != nil && ev.Object.GetName() == name {
				return
			}
		case <-timeout:
			t.Fatalf("no %s event for %s %q within 5s", typ, kind, name)
		}
	}
}

// optWaitList polls List until it succeeds with want items, or fails after
// 5 seconds.
func optWaitList(t *testing.T, f *Factory, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	var got int
	for time.Now().Before(deadline) {
		items, _, err := f.List("alpha", "widget", labels.Everything())
		lastErr, got = err, len(items)
		if err == nil && got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("List(alpha, widget): want %d items and no error, got %d items, err %v", want, got, lastErr)
}

// optWaitSynced polls Synced until the widget informer of alpha has synced.
func optWaitSynced(t *testing.T, f *Factory) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.Synced()["alpha"]["widget"] {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the widget informer of alpha never synced: its sync-watcher was not started")
}

// TestOptionalKindStartsWhenDefinitionArrives: a kind skipped at
// AddCluster has no informer; after its definition's ADDED event, List
// serves it, existing objects are listed, and an object created afterwards
// reaches a subscriber as an ADDED event.
func TestOptionalKindStartsWhenDefinitionArrives(t *testing.T) {
	f, dyn, logs, ch := optFactory(t, optWidget("w0"))
	ctx := context.Background()

	if optHas(f, "widget") {
		t.Fatalf("widget has an informer before its definition arrived; the #5352 skip is broken")
	}
	if _, _, err := f.List("alpha", "widget", labels.Everything()); err == nil {
		t.Fatalf("List(alpha, widget) succeeded before the definition arrived; want an error")
	}

	if _, err := dyn.Resource(optCRDGVR).Create(ctx, optWidgetCRD(true, "1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create definition: %v", err)
	}
	optWaitEvent(t, ch, "customresourcedefinition", EventAdded, "widgets.example.openova.io")
	if !optHas(f, "widget") {
		t.Fatalf("widget has no informer after its definition's ADDED event")
	}
	optWaitList(t, f, 1)
	optWaitEvent(t, ch, "widget", EventAdded, "w0")
	optWaitSynced(t, f)

	if _, err := dyn.Resource(optWidgetGVR).Namespace("ns-a").Create(ctx, optWidget("w1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create widget: %v", err)
	}
	optWaitEvent(t, ch, "widget", EventAdded, "w1")
	optWaitList(t, f, 2)

	if n := logs.count(optStartedLine); n != 1 {
		t.Fatalf("log line %q printed %d times, want 1", optStartedLine, n)
	}
}

// TestOptionalKindSecondDefinitionEventStartsNothing: a MODIFIED event of a
// definition whose kind already runs builds no second informer.
func TestOptionalKindSecondDefinitionEventStartsNothing(t *testing.T) {
	f, dyn, logs, ch := optFactory(t)
	ctx := context.Background()

	if _, err := dyn.Resource(optCRDGVR).Create(ctx, optWidgetCRD(true, "1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create definition: %v", err)
	}
	optWaitEvent(t, ch, "customresourcedefinition", EventAdded, "widgets.example.openova.io")
	optWaitList(t, f, 0)
	first := optInformer(f, "widget")

	if _, err := dyn.Resource(optCRDGVR).Update(ctx, optWidgetCRD(true, "2"), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update definition: %v", err)
	}
	optWaitEvent(t, ch, "customresourcedefinition", EventModified, "widgets.example.openova.io")

	if got := optInformer(f, "widget"); got != first {
		t.Fatalf("the second definition event replaced the widget informer")
	}
	if n := logs.count(optStartedLine); n != 1 {
		t.Fatalf("log line %q printed %d times, want 1", optStartedLine, n)
	}
}

// TestOptionalKindWaitsForServedVersion: a definition that does not serve
// the kind's version starts nothing; the MODIFIED event that serves it
// starts the informer.
func TestOptionalKindWaitsForServedVersion(t *testing.T) {
	f, dyn, logs, ch := optFactory(t)
	ctx := context.Background()

	if _, err := dyn.Resource(optCRDGVR).Create(ctx, optWidgetCRD(false, "1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create definition: %v", err)
	}
	optWaitEvent(t, ch, "customresourcedefinition", EventAdded, "widgets.example.openova.io")
	if optHas(f, "widget") {
		t.Fatalf("widget got an informer from a definition that does not serve v1")
	}
	if n := logs.count(optStartedLine); n != 0 {
		t.Fatalf("log line %q printed %d times before v1 was served, want 0", optStartedLine, n)
	}

	if _, err := dyn.Resource(optCRDGVR).Update(ctx, optWidgetCRD(true, "2"), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update definition: %v", err)
	}
	optWaitEvent(t, ch, "customresourcedefinition", EventModified, "widgets.example.openova.io")
	if !optHas(f, "widget") {
		t.Fatalf("widget has no informer after the definition began to serve v1")
	}
	optWaitList(t, f, 0)
	if n := logs.count(optStartedLine); n != 1 {
		t.Fatalf("log line %q printed %d times, want 1", optStartedLine, n)
	}
}

// optInformer returns the informer of kindName on cluster alpha, or nil.
func optInformer(f *Factory, kindName string) any {
	f.mu.RLock()
	cs := f.clusters["alpha"]
	f.mu.RUnlock()
	if cs == nil {
		return nil
	}
	inf, ok := cs.informersSnapshot()[kindName]
	if !ok {
		return nil
	}
	return inf
}

// optHas reports whether cluster alpha has an informer for kindName.
func optHas(f *Factory, kindName string) bool {
	return optInformer(f, kindName) != nil
}
