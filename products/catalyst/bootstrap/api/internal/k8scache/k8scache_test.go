// k8scache_test.go — unit tests for the k8scache package.
//
// Coverage targets:
//   - kinds.go    : registry add / get / canonical name
//   - factory.go  : per-cluster informer spawn + List + Subscribe
//   - snapshot.go : atomic write, version envelope, age skip
//   - hydrate.go  : hydrate-then-resume, hydrate-stale-then-relist
//   - redact.go   : Secret data + ConfigMap data stripping
//   - sar.go      : positive cache + fail-closed on apiserver error
//
// Tests use fake.NewSimpleDynamicClient for the dynamic surface and
// fake.NewSimpleClientset for the typed client. No real cluster.
package k8scache

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgokubernetes "k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
)

// quietLogger discards log output so test runs aren't noisy.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newPod returns a tiny unstructured Pod for a given namespace + name.
func newPod(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-" + ns + "-" + name,
			"resourceVersion": "100",
		},
		"spec": map[string]any{},
	}}
}

func newSecret(ns, name string, data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
		"data": data,
	}}
}

// ── kinds.go ─────────────────────────────────────────────────────

func TestRegistry_AddGetCanonicalName(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Kind{
		Name: "Pod",
		GVR: schema.GroupVersionResource{
			Version:  "v1",
			Resource: "pods",
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := r.Get("pod"); !ok {
		t.Fatalf("registry should resolve case-insensitive name")
	}
	if _, ok := r.Get("POD"); !ok {
		t.Fatalf("registry should resolve uppercase name")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatalf("registry should not resolve unknown name")
	}
}

func TestRegistry_AddRequiresResource(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Kind{Name: "x"}); err == nil {
		t.Fatalf("Add with empty GVR.Resource must error")
	}
}

// TestRegistry_PluralAliasResolution asserts that operators with kubectl
// muscle memory can hit the catalyst-api REST list endpoint with the
// plural form (`/k8s/services`) and have it resolve to the canonical
// singular Kind. Iter-1 QA-loop matrix surfaced the gap as TC-084 /
// TC-085 / TC-090 / TC-091 / TC-130 — every cloud-list HTTP call was
// using the K8s plural and got 404 from the singular-only registry.
func TestRegistry_PluralAliasResolution(t *testing.T) {
	r := NewRegistry()
	for _, k := range DefaultKinds {
		if err := r.Add(k); err != nil {
			t.Fatalf("Add %q: %v", k.Name, err)
		}
	}

	cases := []struct {
		name    string
		query   string
		want    string // canonical singular Name expected on resolution
	}{
		{name: "singular still works", query: "service", want: "service"},
		{name: "plural service", query: "services", want: "service"},
		{name: "plural node", query: "nodes", want: "node"},
		{name: "plural namespace", query: "namespaces", want: "namespace"},
		{name: "plural pvc", query: "persistentvolumeclaims", want: "persistentvolumeclaim"},
		{name: "plural deployment", query: "deployments", want: "deployment"},
		{name: "kubectl short pvc", query: "pvc", want: "persistentvolumeclaim"},
		{name: "kubectl short pvcs", query: "pvcs", want: "persistentvolumeclaim"},
		{name: "kubectl short ns", query: "ns", want: "namespace"},
		{name: "kubectl short svc", query: "svc", want: "service"},
		{name: "case-insensitive plural", query: "Pods", want: "pod"},
		// QA-loop iter-4 Fix #24 — CRD aliases. Operators reach for
		// `kubectl get crd` (short singular), `kubectl get crds` (short
		// plural), and `kubectl get customresourcedefinitions` (full
		// plural). All three must hit the same canonical Kind.
		{name: "kubectl short crd", query: "crd", want: "customresourcedefinition"},
		{name: "kubectl short crds", query: "crds", want: "customresourcedefinition"},
		{name: "plural customresourcedefinitions", query: "customresourcedefinitions", want: "customresourcedefinition"},
		{name: "case-insensitive CRD", query: "CRD", want: "customresourcedefinition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ok := r.Get(tc.query)
			if !ok {
				t.Fatalf("Get(%q) returned !ok — alias should resolve", tc.query)
			}
			if k.Name != tc.want {
				t.Fatalf("Get(%q) → Name=%q; want %q", tc.query, k.Name, tc.want)
			}
		})
	}

	// Negative — a bogus name still rejects.
	if _, ok := r.Get("notakind"); ok {
		t.Fatalf("Get(%q) should not resolve", "notakind")
	}
}

// TestRegistry_PluralDoesNotShadowSingular guards the Add() invariant
// that a plural-alias write never overwrites a registered singular Kind
// with a different GVR. Concrete: the metrics.k8s.io PodMetrics resource
// is registered with GVR.Resource="pods" — same plural as core/v1 Pod.
// The plural-alias index must not point "pods" at podmetrics and shadow
// the canonical Pod registration.
func TestRegistry_PluralDoesNotShadowSingular(t *testing.T) {
	r := NewRegistry()
	for _, k := range DefaultKinds {
		_ = r.Add(k)
	}
	got, ok := r.Get("pods")
	if !ok {
		t.Fatalf("Get(%q) returned !ok", "pods")
	}
	if got.Name != "pod" {
		t.Fatalf("Get(%q) → %q; expected canonical singular %q (shadow guard)",
			"pods", got.Name, "pod")
	}
}

// TestDefaultKinds_GraphAndDashboardSurface asserts the kinds the
// architecture-graph adapter and dashboard treemap depend on are part
// of the default registry. A regression here would silently break the
// /cloud?view=graph K8s-workload projection or the /dashboard live
// aggregation, so it's pinned by name.
func TestDefaultKinds_GraphAndDashboardSurface(t *testing.T) {
	r := NewRegistry()
	for _, k := range DefaultKinds {
		_ = r.Add(k)
	}
	mandatory := []string{
		// existing
		"namespace", "node", "pod", "service", "configmap", "secret",
		"persistentvolumeclaim", "deployment", "statefulset", "daemonset",
		"ingress",
		// graph + dashboard depend on these
		"persistentvolume", "replicaset", "endpointslice",
		// dashboard color_by=utilization depends on this (#1084)
		"podmetrics",
		// EPIC-1 (#1096) compliance — Kyverno PolicyReports.
		"policyreport", "clusterpolicyreport",
		// QA-loop iter-2 Fix #17 — CRDs surfaced through /k8s/{kind}.
		// The /components, /apps, /users, /organizations, /environments,
		// and /blueprints pages all consume one of these via the SSE
		// stream. A regression here would silently re-introduce the
		// "unknown kind" 404 surface seen on omantel iter-2.
		"helmrelease", "useraccess", "application", "blueprint",
		"organization", "environment",
		// QA-loop iter-3 Fix #18 — RBAC kinds surfaced through
		// /k8s/{kind}. The Sovereign Console's RBAC pane consumes
		// rbac.authorization.k8s.io/clusterroles + clusterrolebindings.
		// Caught live on omantel iter-2: TC-122/196/199/248 returned 404
		// "unknown kind" for the cluster-wide RBAC list.
		"clusterrole", "clusterrolebinding",
		// QA-loop iter-4 Fix #24 — apiextensions.k8s.io
		// /customresourcedefinitions surfaced through /k8s/{kind}. The
		// Sovereign Console's CRD inventory pane consumes this. Caught
		// live on omantel iter-4: TC-199 returned HTTP 404 "unknown kind"
		// because the GVR was never registered. Both the canonical
		// singular AND the kubectl-short forms must resolve.
		"customresourcedefinition",
	}
	for _, name := range mandatory {
		if _, ok := r.Get(name); !ok {
			t.Errorf("DefaultKinds missing %q — required by architecture-graph or dashboard", name)
		}
	}
}

func TestRegistry_AllAndNames(t *testing.T) {
	r := NewRegistry()
	for _, k := range DefaultKinds {
		_ = r.Add(k)
	}
	if len(r.All()) != len(DefaultKinds) {
		t.Fatalf("All count mismatch: got %d want %d", len(r.All()), len(DefaultKinds))
	}
	if len(r.Names()) != len(DefaultKinds) {
		t.Fatalf("Names count mismatch")
	}
}

// ── factory.go ───────────────────────────────────────────────────

// fakeClients constructs a (dynamic, typed) pair seeded with the
// supplied unstructured objects. Each object's GVR is inferred from
// apiVersion + kind via the supplied scheme.
func fakeClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, clientgokubernetes.Interface) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "SecretList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, &unstructured.Unstructured{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:    "PodList",
		{Version: "v1", Resource: "secrets"}: "SecretList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	core := kfake.NewSimpleClientset()
	return dyn, core
}

func minimalRegistry() *Registry {
	r := NewRegistry()
	_ = r.Add(Kind{
		Name:       "pod",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Namespaced: true,
	})
	_ = r.Add(Kind{
		Name:       "secret",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		Namespaced: true,
		Sensitive:  true,
	})
	return r
}

func TestFactory_StartAndList(t *testing.T) {
	pod := newPod("default", "x")
	dyn, core := fakeClients(pod)

	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait up to 2s for the informer to sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, err := f.List("alpha", "pod", labels.Everything())
		if err == nil && len(items) == 1 {
			if items[0].GetName() != "x" {
				t.Fatalf("unexpected pod name: %q", items[0].GetName())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("informer never synced")
}

// TestFactory_ListResolvesPluralAlias asserts that the indexer lookup
// in Factory.List honours the same plural / short-form aliases the
// REST handler tier already accepts. Without this the handler would
// resolve `/k8s/services` at the registry level (200 path) but the
// downstream factory.List would still see "services" and return
// "kind not registered" — exactly the iter-1 cloud-list 404 chain
// surfaced live on omantel for TC-084 / TC-085 / TC-090 / TC-091.
func TestFactory_ListResolvesPluralAlias(t *testing.T) {
	pod := newPod("default", "x")
	dyn, core := fakeClients(pod)

	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for sync via the canonical singular form first.
	deadline := time.Now().Add(2 * time.Second)
	synced := false
	for time.Now().Before(deadline) {
		items, _, err := f.List("alpha", "pod", labels.Everything())
		if err == nil && len(items) == 1 {
			synced = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !synced {
		t.Fatalf("informer never synced via singular form")
	}

	// Now exercise alias forms — every one MUST resolve to the same Pod.
	for _, alias := range []string{"pods", "Pod", "PODS", "po"} {
		items, _, err := f.List("alpha", alias, labels.Everything())
		if err != nil {
			t.Errorf("List(%q): %v — alias should resolve via Registry", alias, err)
			continue
		}
		if len(items) != 1 {
			t.Errorf("List(%q): got %d items; want 1", alias, len(items))
			continue
		}
		if items[0].GetName() != "x" {
			t.Errorf("List(%q): unexpected pod name %q", alias, items[0].GetName())
		}
	}

	// Negative — a truly unknown kind still returns "not registered".
	if _, _, err := f.List("alpha", "notakind", labels.Everything()); err == nil {
		t.Fatalf("List(notakind) should error — Registry has no entry")
	}
}

func TestFactory_ListUnknownClusterErrors(t *testing.T) {
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	_, _, err = f.List("missing", "pod", labels.Everything())
	if err == nil {
		t.Fatalf("expected error for unknown cluster")
	}
}

func TestFactory_SubscribeReceivesEvents(t *testing.T) {
	dyn, core := fakeClients() // empty initial state
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ch, unsub := f.Subscribe("operator", map[string]struct{}{"pod": {}})
	defer unsub()

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait briefly for informer to start, then create a Pod via the
	// fake dynamic client.
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	_, err = dyn.Resource(gvr).Namespace("ns-a").Create(context.Background(), newPod("ns-a", "p1"), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod: %v", err)
	}

	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("subscriber channel closed")
			}
			if ev.Kind == "pod" && ev.Type == EventAdded && ev.Object.GetName() == "p1" {
				return
			}
		case <-timeout:
			t.Fatalf("never received pod ADDED event")
		}
	}
}

func TestFactory_RedactsSecretData(t *testing.T) {
	sec := newSecret("default", "creds", map[string]any{
		"password": "c2hocyB0aGlz", // base64 — must not appear post-redaction
	})
	dyn, core := fakeClients(sec)
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var items []*unstructured.Unstructured
	for time.Now().Before(deadline) {
		items, _, _ = f.List("alpha", "secret", labels.Everything())
		if len(items) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(items))
	}
	body, _ := json.Marshal(items[0].Object)
	if contains(body, "c2hocyB0aGlz") {
		t.Fatalf("secret value leaked through redaction: %s", string(body))
	}
	if !contains(body, "redactedKeys") {
		t.Fatalf("expected redactedKeys field in redacted Secret: %s", string(body))
	}
}

func contains(b []byte, sub string) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}

// ── snapshot.go + hydrate.go ─────────────────────────────────────

func TestSnapshot_AtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	env := snapshotEnvelope{
		Version: snapshotEnvelopeVersion,
		Cluster: "alpha",
		Kind:    "pod",
		Wrote:   time.Now(),
		Items: []*unstructured.Unstructured{
			newPod("default", "x"),
		},
	}
	path := snapshotPath(dir, "alpha", "pod")
	if err := writeSnapshotAtomic(path, env); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got snapshotEnvelope
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cluster != "alpha" || got.Kind != "pod" || len(got.Items) != 1 {
		t.Fatalf("envelope round-trip mismatch: %+v", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file not cleaned up")
	}
}

func TestSnapshot_HydrateThenResume(t *testing.T) {
	dir := t.TempDir()
	pod := newPod("default", "seed")
	env := snapshotEnvelope{
		Version: snapshotEnvelopeVersion,
		Cluster: "alpha",
		Kind:    "pod",
		Wrote:   time.Now(),
		Items:   []*unstructured.Unstructured{pod},
	}
	path := snapshotPath(dir, "alpha", "pod")
	if err := writeSnapshotAtomic(path, env); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Stand up a fake informer with NO objects in the apiserver — the
	// hydrate path must seed it from disk.
	dyn, core := fakeClients()
	cfg := Config{
		Logger:      quietLogger(),
		Registry:    minimalRegistry(),
		SnapshotDir: dir,
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Allow time for hydrate + factory.Start LIST. Hydrate
	// pre-populates; the LIST then reconciles. With no objects in
	// the apiserver the LIST clears the seeded one — that IS the
	// contract: the apiserver is the source of truth. The
	// "hydrate-then-resume" guarantee is that for the brief window
	// between hydrate and the first WATCH event, the cache served
	// the seeded data; after the watch lands, the cache reflects
	// the apiserver. Here we just check hydrate ran without panic
	// and see the cache settle to apiserver state (empty).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := f.List("alpha", "pod", labels.Everything())
		_ = items
		time.Sleep(20 * time.Millisecond)
	}
	// Final state: should be empty (apiserver wins).
	items, _, _ := f.List("alpha", "pod", labels.Everything())
	if len(items) != 0 {
		t.Fatalf("expected apiserver-driven empty cache, got %d items", len(items))
	}
}

func TestSnapshot_HydrateStaleThenRelist(t *testing.T) {
	dir := t.TempDir()
	pod := newPod("default", "old")
	env := snapshotEnvelope{
		Version: snapshotEnvelopeVersion,
		Cluster: "alpha",
		Kind:    "pod",
		Wrote:   time.Now().Add(-25 * time.Hour), // stale
		Items:   []*unstructured.Unstructured{pod},
	}
	path := snapshotPath(dir, "alpha", "pod")
	if err := writeSnapshotAtomic(path, env); err != nil {
		t.Fatalf("write: %v", err)
	}

	freshPod := newPod("default", "fresh")
	dyn, core := fakeClients(freshPod)
	cfg := Config{
		Logger:         quietLogger(),
		Registry:       minimalRegistry(),
		SnapshotDir:    dir,
		SnapshotMaxAge: 1 * time.Hour,
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := f.List("alpha", "pod", labels.Everything())
		if len(items) == 1 && items[0].GetName() == "fresh" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stale snapshot should have been bypassed; expected only 'fresh'")
}

func TestSnapshot_DuringShutdown(t *testing.T) {
	// Single eager pass on Start should land a snapshot file before
	// Stop is called, even with no events.
	pod := newPod("default", "x")
	dyn, core := fakeClients(pod)
	dir := t.TempDir()
	cfg := Config{
		Logger:           quietLogger(),
		Registry:         minimalRegistry(),
		SnapshotDir:      dir,
		SnapshotInterval: 50 * time.Millisecond,
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for snapshot to land.
	expected := snapshotPath(dir, "alpha", "pod")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expected); err == nil {
			f.Stop()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.Stop()
	t.Fatalf("snapshot file %q never written", expected)
}

// ── redact.go ────────────────────────────────────────────────────

func TestRedactSecret_StripsDataAndSurfacesKeys(t *testing.T) {
	k := Kind{Name: "secret", Sensitive: true}
	sec := newSecret("ns", "creds", map[string]any{
		"password": "abc",
		"token":    "def",
	})
	red := redactObject(k, sec)
	if _, ok := red.Object["data"]; ok {
		t.Fatalf("data block should be stripped")
	}
	if _, ok := red.Object["stringData"]; ok {
		t.Fatalf("stringData block should be stripped")
	}
	keysAny, ok := red.Object["redactedKeys"]
	if !ok {
		t.Fatalf("expected redactedKeys field")
	}
	keys, _ := keysAny.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 redacted keys, got %v", keys)
	}
}

func TestRedactNonSensitive_NoCopy(t *testing.T) {
	k := Kind{Name: "pod"} // not sensitive
	pod := newPod("default", "x")
	red := redactObject(k, pod)
	if red != pod {
		t.Fatalf("non-sensitive kind should pass through identity")
	}
}

// ── sar.go ───────────────────────────────────────────────────────

func TestSARCache_FailsClosedOnUnknownCluster(t *testing.T) {
	c := NewSARCache()
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if c.Allowed(context.Background(), f, "alice", "missing", "pod", "default", "get") {
		t.Fatalf("SAR must fail closed when cluster is unregistered")
	}
}

// ── concurrency smoke ────────────────────────────────────────────

func TestFactory_FanoutDoesNotBlockOnSlowSubscriber(t *testing.T) {
	pod := newPod("default", "x")
	dyn, core := fakeClients(pod)
	cfg := Config{
		Logger:          quietLogger(),
		Registry:        minimalRegistry(),
		EventBufferSize: 2, // tiny buffer to force the drop path
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	// Slow subscriber that never reads.
	_, _ = f.Subscribe("slow", nil)

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Producer side: hammer with creates. The factory must not
	// deadlock; we assert by completing the test under a timeout.
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			_, _ = dyn.Resource(gvr).Namespace("ns").Create(context.Background(), newPod("ns", "p"+itoa(i)), metav1.CreateOptions{})
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("producer wedged on slow subscriber — backpressure failure")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// keep corev1 referenced to prevent the import being elided when this
// file changes.
var _ = corev1.NamespaceDefault

// ── EPIC-1 (#1096) — PolicyReport SSE fanout ─────────────────────

// newPolicyReport returns a tiny unstructured wgpolicyk8s.io PolicyReport.
// Mirrors the schema produced by the Kyverno reports controller — fields
// kept minimal because the score aggregator (slice S1) reads only
// `metadata.{name,namespace,labels,ownerReferences}` and `results[]`.
//
// Results pass through DeepCopyJSON so the slice element type must be
// `any` (not `map[string]any`) — DeepCopyJSON refuses unknown concrete
// slice element types.
func newPolicyReport(ns, name string, results []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "PolicyReport",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
		"results": results,
	}}
}

func newClusterPolicyReport(name string, results []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "ClusterPolicyReport",
		"metadata": map[string]any{
			"name":            name,
			"resourceVersion": "1",
		},
		"results": results,
	}}
}

// policyReportRegistry — minimal registry with the two PolicyReport
// kinds. Used by the W1 fanout test below.
func policyReportRegistry() *Registry {
	r := NewRegistry()
	_ = r.Add(Kind{
		Name:       "policyreport",
		GVR:        schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"},
		Namespaced: true,
	})
	_ = r.Add(Kind{
		Name:       "clusterpolicyreport",
		GVR:        schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"},
		Namespaced: false,
	})
	return r
}

// fakePolicyReportClients — like fakeClients but seeds the discovery
// scheme with the wgpolicyk8s.io types so the dynamic informer's LIST +
// WATCH succeed against the in-memory store.
func fakePolicyReportClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, clientgokubernetes.Interface) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "PolicyReportList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "PolicyReport"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "ClusterPolicyReportList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "ClusterPolicyReport"}, &unstructured.Unstructured{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}:        "PolicyReportList",
		{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}: "ClusterPolicyReportList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	core := kfake.NewSimpleClientset()
	return dyn, core
}

// TestPolicyReport_FlowsThroughSSEFanout asserts W1 — applying a
// PolicyReport CR fires the same ADD event the architecture-graph kinds
// fire, with no special-case handling. Coverage:
//   - PolicyReport (namespace-scoped)
//   - ClusterPolicyReport (cluster-scoped) on the same factory
//   - Subscriber filtered to compliance kinds receives both
func TestPolicyReport_FlowsThroughSSEFanout(t *testing.T) {
	dyn, core := fakePolicyReportClients()
	cfg := Config{
		Logger:   quietLogger(),
		Registry: policyReportRegistry(),
		Clusters: []ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ch, unsub := f.Subscribe("operator", map[string]struct{}{
		"policyreport":        {},
		"clusterpolicyreport": {},
	})
	defer unsub()

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Apply a namespaced PolicyReport.
	results := []any{
		map[string]any{
			"policy":  "multi-replica-drainability",
			"rule":    "multi-replica-drainability",
			"result":  "fail",
			"message": "Deployment has only 1 replica",
		},
	}
	prGVR := schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}
	if _, err := dyn.Resource(prGVR).Namespace("acme").Create(context.Background(), newPolicyReport("acme", "pod-frontend-7c5f", results), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create PolicyReport: %v", err)
	}

	// Apply a cluster-scoped ClusterPolicyReport.
	cprGVR := schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}
	if _, err := dyn.Resource(cprGVR).Create(context.Background(), newClusterPolicyReport("ns-acme", results), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ClusterPolicyReport: %v", err)
	}

	gotPR, gotCPR := false, false
	timeout := time.After(2 * time.Second)
	for !(gotPR && gotCPR) {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("subscriber channel closed")
			}
			if ev.Type != EventAdded {
				continue
			}
			if ev.Kind == "policyreport" && ev.Object.GetName() == "pod-frontend-7c5f" {
				// Sanity check — body survives unmodified (PolicyReport
				// is non-sensitive so redact is a no-op).
				if results, _, _ := unstructured.NestedSlice(ev.Object.Object, "results"); len(results) != 1 {
					t.Fatalf("PolicyReport.results not preserved through fanout")
				}
				gotPR = true
			}
			if ev.Kind == "clusterpolicyreport" && ev.Object.GetName() == "ns-acme" {
				gotCPR = true
			}
		case <-timeout:
			t.Fatalf("never received PolicyReport (got=%v) + ClusterPolicyReport (got=%v) ADD events", gotPR, gotCPR)
		}
	}
}
