// compliance_policy_store_test.go — CMP-023: the policies route shows the
// mode the policy object carries in the informer store of a cluster the
// Factory holds, also when the handler never received the object's event.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

var (
	storeLegacyGVR     = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}
	storeValidatingGVR = schema.GroupVersionResource{Group: "policies.kyverno.io", Version: "v1", Resource: "validatingpolicies"}
	storeMutatingGVR   = schema.GroupVersionResource{Group: "policies.kyverno.io", Version: "v1", Resource: "mutatingpolicies"}
)

// blockingNATS holds its first Put until release is closed, and tells entered
// when that Put begins: the handler's watch loop then reads no event.
type blockingNATS struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingNATS) Put(context.Context, string, []byte) error {
	first := false
	b.once.Do(func() { first = true })
	if first {
		close(b.entered)
		<-b.release
	}
	return nil
}

// storeFactory builds a Factory that holds cluster acme over a fake client
// with objs, its three policy kinds registered, every subscriber buffer of
// the given size (0: the default), and a probe subscribed before the
// informers start. It starts the informers and waits until their stores
// hold as many objects as objs.
func storeFactory(t *testing.T, buffer int, objs ...*unstructured.Unstructured) (*k8scache.Factory, *dynamicfake.FakeDynamicClient, *storeProbe) {
	t.Helper()
	reg := k8scache.NewRegistry()
	for name, gvr := range map[string]schema.GroupVersionResource{
		"clusterpolicy": storeLegacyGVR, k8scache.KindValidatingPolicy: storeValidatingGVR, k8scache.KindMutatingPolicy: storeMutatingGVR,
	} {
		if err := reg.Add(k8scache.Kind{Name: name, GVR: gvr}); err != nil {
			t.Fatal(err)
		}
	}
	seed := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		seed = append(seed, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		storeLegacyGVR: "ClusterPolicyList", storeValidatingGVR: "ValidatingPolicyList", storeMutatingGVR: "MutatingPolicyList",
	}, seed...)
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: quietComplianceLogger(), Registry: reg, EventBufferSize: buffer,
		Clusters: []k8scache.ClusterRef{{ID: "acme", DynamicClient: dyn}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	p := newStoreProbe(t, f)
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		held := 0
		for _, kind := range []string{"clusterpolicy", k8scache.KindValidatingPolicy, k8scache.KindMutatingPolicy} {
			items, _, err := f.List("acme", kind, nil)
			if err != nil {
				t.Fatal(err)
			}
			held += len(items)
		}
		return held == len(objs)
	})
	return f, dyn, p
}

// storeProbe records every policy event the Factory fans out to it.
type storeProbe struct {
	mu   sync.Mutex
	seen map[string]bool // "<type>/<object kind>/<name>"
}

func newStoreProbe(t *testing.T, f *k8scache.Factory) *storeProbe {
	p := &storeProbe{seen: map[string]bool{}}
	ch, unsub := f.Subscribe("", map[string]struct{}{"clusterpolicy": {}, k8scache.KindValidatingPolicy: {}, k8scache.KindMutatingPolicy: {}})
	t.Cleanup(unsub)
	go func() {
		for ev := range ch {
			p.mu.Lock()
			p.seen[string(ev.Type)+"/"+ev.Object.GetKind()+"/"+ev.Object.GetName()] = true
			p.mu.Unlock()
		}
	}()
	return p
}

func (p *storeProbe) await(t *testing.T, typ k8scache.EventType, o *unstructured.Unstructured) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.seen[string(typ)+"/"+o.GetKind()+"/"+o.GetName()]
	})
}

// routeModes reads GET /api/v1/sovereigns/acme/compliance/policies and
// returns the mode of every listed policy by name.
func routeModes(t *testing.T, h *Handler) map[string]string {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/acme/compliance/policies", nil))
	var resp struct {
		Items []PolicyView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("policies: %d %v %s", rec.Code, err, rec.Body.String())
	}
	out := map[string]string{}
	for _, p := range resp.Items {
		out[p.Name] = p.Mode
	}
	return out
}

func startStoreHandler(t *testing.T, f *k8scache.Factory, pub PolicyRollupPublisher) *Handler {
	t.Helper()
	c := NewComplianceHandler(ComplianceConfig{}, quietComplianceLogger(), f, pub)
	h := NewWithPDM(quietComplianceLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	h.SetComplianceHandler(c)
	c.Start(context.Background())
	t.Cleanup(c.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

// The events of the first LIST went out before the handler subscribed (the
// start-up order of main.go: the Factory starts, then the handler). The
// route lists every object of the store with its mode; a name both kinds
// hold takes the legacy object, as in setPolicyObject.
func TestCompliance_PoliciesModeFromStore_EventsBeforeSubscribe(t *testing.T) {
	objs := []*unstructured.Unstructured{
		mkValidatingPolicy("flux-managed", "Deny"),
		mkValidatingPolicy("topology-spread", "Audit"),
		mkClusterPolicy("forbid-nodeport-service", "Enforce", ""),
		mkClusterPolicy("moved", "Audit", ""),
		mkValidatingPolicy("moved", "Deny"),
	}
	f, _, probe := storeFactory(t, 0, objs...)
	for _, o := range objs { // every ADDED event is out: the handler subscribes after them
		probe.await(t, k8scache.EventAdded, o)
	}
	h := startStoreHandler(t, f, nil)
	want := map[string]string{"flux-managed": "enforcing", "topology-spread": "permissive", "forbid-nodeport-service": "enforcing", "moved": "permissive"}
	if got := routeModes(t, h); !reflect.DeepEqual(got, want) {
		t.Fatalf("the route lists %v; want %v", got, want)
	}
}

// The flip of a policy to enforce reaches the handler's full buffer and is
// dropped (Factory.fanout drops the oldest event): its watch loop is held in
// a Put while three policies flip, one after the other through one informer.
// The route shows the mode the store holds for each.
func TestCompliance_PoliciesModeFromStore_DroppedFlip(t *testing.T) {
	names := []string{"flux-managed", "harbor-proxy-pull", "image-tag-pinned"}
	objs := []*unstructured.Unstructured{}
	for _, n := range names {
		objs = append(objs, mkValidatingPolicy(n, "Audit"))
	}
	pub := &blockingNATS{entered: make(chan struct{}), release: make(chan struct{})}
	f, dyn, probe := storeFactory(t, 1, objs...)
	h := startStoreHandler(t, f, pub)

	// A report whose subject is published holds the watch loop in Put.
	f.Publish(k8scache.Event{Cluster: "acme", Kind: "policyreport", Type: k8scache.EventAdded, At: time.Now(),
		Object: mkPolicyReport("ns1", "pr", []map[string]any{{"policy": "flux-managed", "rule": "r", "result": "fail",
			"resources": []any{map[string]any{"kind": "Deployment", "namespace": "ns1", "name": "web"}}}})})
	select {
	case <-pub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch loop never published the report's subject")
	}
	for _, n := range names {
		if _, err := dyn.Resource(storeValidatingGVR).Update(context.Background(), mkValidatingPolicy(n, "Deny"), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// The informer sends the three flips in order: the second one dropped
	// the first from the held loop's buffer of one before the third began.
	probe.await(t, k8scache.EventModified, objs[2])
	close(pub.release)

	// The released loop takes the last flip in, so no send to its buffer is
	// left when the test stops it (k8scache's unsubscribe closes the channel
	// a fan-out may still send on).
	c := h.ComplianceHandler()
	waitFor(t, 5*time.Second, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.policyMetaByName["acme"][names[2]].mode == "enforce"
	})
	want := map[string]string{"flux-managed": "enforcing", "harbor-proxy-pull": "enforcing", "image-tag-pinned": "enforcing"}
	if got := routeModes(t, h); !reflect.DeepEqual(got, want) {
		t.Fatalf("after three flips to enforce the route lists %v; want %v", got, want)
	}
}
