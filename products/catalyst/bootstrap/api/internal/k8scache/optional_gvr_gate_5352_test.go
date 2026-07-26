// optional_gvr_gate_5352_test.go — #5352 regression guard.
//
// Root cause: the AddCluster informer loop registered an informer for EVERY
// registered Kind unconditionally. On a Huawei-hosted Sovereign (hw288) the
// hcloud.crossplane.io/v1alpha1 managed-resource GVRs (servers /
// loadbalancers / networks / volumes) and cilium.io/v2alpha1
// CiliumEndpointSlice are NOT served — the provider is Hetzner-only and that
// cilium version is not built. A client-go reflector watching an absent GVR
// hot-loops "Failed to watch … → retry" forever, leaking memory until
// catalyst-api OOMKills (62 restarts over 2.5 days).
//
// The fix restores a BOUNDED existence gate that applies ONLY to Optional
// kinds: before registering an Optional kind's informer, AddCluster probes
// discovery (ServerResourcesForGroupVersion) under a per-GroupVersion
// context timeout. Served → register; absent / error / timeout → skip.
// Non-optional (universally present) kinds register with no probe, so a dead
// kubeconfig can never block startup — the original hang the pre-#5352 probe
// caused (it had no timeout) stays closed.
package k8scache

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// hasInformer reports whether cluster `clusterID` built an informer for the
// Kind registered under `kindName`. Reads the package-internal per-cluster
// map under the factory lock.
func hasInformer(f *Factory, clusterID, kindName string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cs, ok := f.clusters[clusterID]
	if !ok || cs == nil {
		return false
	}
	_, ok = cs.informers[kindName]
	return ok
}

// TestAddCluster_OptionalKindGate is the end-to-end guard: with a registry of
// one non-optional kind + one SERVED optional kind + one ABSENT optional kind,
// AddCluster must build informers for the non-optional and served-optional
// kinds and SKIP the absent optional kind (the churn source). Informers are
// built during AddCluster (no Start needed), so nothing dials the fake.
func TestAddCluster_OptionalKindGate(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(Kind{Name: "server.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "servers"}, Namespaced: false, Optional: true})
	_ = r.Add(Kind{Name: "ciliumendpointslice", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2alpha1", Resource: "ciliumendpointslices"}, Namespaced: false, Optional: true})

	// The apiserver serves ONLY hcloud.crossplane.io/v1alpha1 (with the
	// `servers` resource). cilium.io/v2alpha1 is absent → discovery 404s it.
	core := kfake.NewSimpleClientset()
	core.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "hcloud.crossplane.io/v1alpha1",
			APIResources: []metav1.APIResource{{Name: "servers", Namespaced: false, Kind: "Server"}},
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

	f, err := NewFactory(Config{
		Logger:   quietLogger(),
		Registry: r,
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	// Non-optional kind: registered unconditionally (fast path).
	if !hasInformer(f, "alpha", "pod") {
		t.Errorf("non-optional kind 'pod' not registered — the no-probe fast path is broken")
	}
	// Served optional kind: probe confirms it is served → registered.
	if !hasInformer(f, "alpha", "server.hcloud") {
		t.Errorf("served optional kind 'server.hcloud' not registered — the gate wrongly skipped a served GVR")
	}
	// Absent optional kind: probe reports absent → SKIPPED. This is the
	// #5352 churn fix: no informer, no reflector, no OOM.
	if hasInformer(f, "alpha", "ciliumendpointslice") {
		t.Errorf("absent optional kind 'ciliumendpointslice' WAS registered — its reflector would hot-loop 'Failed to watch' and leak memory (#5352)")
	}
}

// TestAddCluster_OptionalGate_NilCoreSkipsOptional proves that when no
// discovery client is available the gate fails safe (skips Optional kinds)
// rather than registering a possibly-absent GVR — and still registers every
// non-optional kind. The gate must never panic on a nil core.
func TestAddCluster_OptionalGate_NilCoreSkipsOptional(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(Kind{Name: "server.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "servers"}, Namespaced: false, Optional: true})

	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	// CoreClient omitted → cs.core is nil.
	f, err := NewFactory(Config{
		Logger:   quietLogger(),
		Registry: r,
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	if !hasInformer(f, "alpha", "pod") {
		t.Errorf("non-optional 'pod' not registered with nil core — fast path must not depend on discovery")
	}
	if hasInformer(f, "alpha", "server.hcloud") {
		t.Errorf("optional 'server.hcloud' registered despite nil discovery — gate must fail safe (skip)")
	}
}

// TestProbeGroupVersion_ServedListsResources — a served GroupVersion reports
// each of its resources as served, and a resource NOT in the returned list
// as not-served (resource-level precision, so one hcloud GV that omits a
// resource still skips only that resource).
func TestProbeGroupVersion_ServedListsResources(t *testing.T) {
	core := kfake.NewSimpleClientset()
	core.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "hcloud.crossplane.io/v1alpha1",
			APIResources: []metav1.APIResource{{Name: "servers"}, {Name: "loadbalancers"}},
		},
	}
	p := probeGroupVersion(core, "hcloud.crossplane.io/v1alpha1", optionalGVProbeTimeout)
	if !p.ok {
		t.Fatalf("served GroupVersion probe returned ok=false")
	}
	if !p.served("servers") || !p.served("loadbalancers") {
		t.Errorf("served resources reported not-served: %+v", p.resources)
	}
	if p.served("networks") {
		t.Errorf("resource absent from the served list wrongly reported served")
	}
}

// TestProbeGroupVersion_AbsentGVSkips — an unserved GroupVersion (the fake
// returns an IsNotFound StatusError) yields not-served.
func TestProbeGroupVersion_AbsentGVSkips(t *testing.T) {
	core := kfake.NewSimpleClientset() // no Resources set → NotFound
	p := probeGroupVersion(core, "cilium.io/v2alpha1", optionalGVProbeTimeout)
	if p.served("ciliumendpointslices") {
		t.Errorf("absent GroupVersion must report not-served (IsNotFound path), got %+v", p)
	}
}

// TestProbeGroupVersion_ErrorSkips — a discovery transport error yields
// not-served (the informer would only churn against an unreachable apiserver).
func TestProbeGroupVersion_ErrorSkips(t *testing.T) {
	core := kfake.NewSimpleClientset()
	core.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("boom: apiserver unreachable")
	})
	p := probeGroupVersion(core, "hcloud.crossplane.io/v1alpha1", optionalGVProbeTimeout)
	if p.ok || p.served("servers") {
		t.Errorf("discovery error must yield ok=false / not-served, got %+v", p)
	}
}

// TestProbeGroupVersion_TimeoutSkipsWithoutHanging — a discovery call slower
// than the timeout must NOT hang the probe: it returns not-served within the
// bound. This is the guard against reintroducing the #5352 startup hang (the
// removed probe had no timeout and blocked boot for minutes on a dead cluster).
func TestProbeGroupVersion_TimeoutSkipsWithoutHanging(t *testing.T) {
	core := kfake.NewSimpleClientset()
	core.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		time.Sleep(500 * time.Millisecond) // far longer than the probe timeout below
		return true, nil, nil
	})
	start := time.Now()
	p := probeGroupVersion(core, "hcloud.crossplane.io/v1alpha1", 30*time.Millisecond)
	elapsed := time.Since(start)
	if p.ok || p.served("servers") {
		t.Errorf("timed-out probe must yield ok=false / not-served, got %+v", p)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("probe hung for %v — timeout did not bound the wait (would reintroduce the #5352 startup hang)", elapsed)
	}
}

// TestProbeGroupVersion_NilCoreSkips — a nil discovery client never panics and
// reports not-served.
func TestProbeGroupVersion_NilCoreSkips(t *testing.T) {
	p := probeGroupVersion(nil, "hcloud.crossplane.io/v1alpha1", optionalGVProbeTimeout)
	if p.served("servers") {
		t.Errorf("nil core must report not-served, got %+v", p)
	}
}

// TestDefaultKinds_OptionalFlags pins the config-level invariant: the GVRs
// not served on every cluster carry Optional:true (so the gate probes them),
// while universally-present kinds stay non-optional (so they always register
// with no probe — "non-optional kinds always register").
func TestDefaultKinds_OptionalFlags(t *testing.T) {
	byName := map[string]Kind{}
	for _, k := range DefaultKinds {
		byName[k.Name] = k
	}
	wantOptional := []string{
		"server.hcloud", "loadbalancer.hcloud", "network.hcloud", "volume.hcloud",
		"ciliumendpointslice",
	}
	for _, name := range wantOptional {
		k, ok := byName[name]
		if !ok {
			t.Errorf("DefaultKinds missing expected optional kind %q", name)
			continue
		}
		if !k.Optional {
			t.Errorf("#5352: kind %q must be Optional:true — its GVR is not served on every cluster", name)
		}
	}
	wantNonOptional := []string{"namespace", "node", "pod", "service", "secret", "deployment"}
	for _, name := range wantNonOptional {
		k, ok := byName[name]
		if !ok {
			t.Errorf("DefaultKinds missing expected non-optional kind %q", name)
			continue
		}
		if k.Optional {
			t.Errorf("#5352: universally-present kind %q must stay non-optional (registers unconditionally, no probe)", name)
		}
	}
}

// TestDefaultKinds_NoSandboxKind guards the #5352 removal of the Sandbox CRD
// from the registry. The Sandbox concept was removed platform-wide (founder
// 2026-06-30); no cluster serves sandbox.openova.io/v1, so an informer for it
// would only hot-loop "Failed to watch" and leak memory. It must not be
// re-added to DefaultKinds.
func TestDefaultKinds_NoSandboxKind(t *testing.T) {
	for _, k := range DefaultKinds {
		if k.Name == "sandbox" || k.GVR.Group == "sandbox.openova.io" {
			t.Fatalf("#5352: DefaultKinds must not register the removed Sandbox CRD "+
				"(kind %q, gvr %q) — the Sandbox concept was removed 2026-06-30 and no "+
				"cluster serves the GVR; an informer would churn and leak.",
				k.Name, k.GVR.String())
		}
	}
}
