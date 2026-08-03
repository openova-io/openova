// informer_rebuild_idempotence_5642_test.go — #5642 bounded-growth guard.
//
// Defect: AddCluster rebuilt the ENTIRE informer set every time it was
// called for an already-registered cluster id, even when the kubeconfig
// bytes were byte-identical to the running registration.
//
// That path is level-triggered and therefore fires forever:
// runClusterMeshSteadyStateHeal re-forwards every on-disk secondary
// kubeconfig on each 5-minute pass, and POST
// /api/v1/sovereign/secondary-kubeconfig calls AddCluster unconditionally.
// Measured live on hw292 (dep 1c56518035a83e03) 2026-08-03: two
// "k8scache: cluster registered + informers started (runtime add)" lines
// per 5-minute cycle for cluster 1c56518035a83e03-me-east-215-b-1, and
// every per-kind ADDED counter in catalyst_k8scache_informer_events_total
// for that cluster DOUBLED inside one 6.5-minute window (secret
// 1212→2424, clusterrole 788→1576, policyreport 2118→4202, …) — i.e. a
// full re-ADD sweep of the whole region-b object graph per rebuild.
// Teardown of the superseded set did not reclaim: live heap climbed
// ~1.2 MB/s and RSS ~1.7 MB/s until the 4Gi limit OOMKilled the Pod on a
// ~60-minute metronome (restartCount 16 over 15h).
//
// The guard below is a BOUNDED-GROWTH assertion, not an exercise: it
// drives AddCluster N times and asserts the number of informer sets
// constructed for that cluster id stays at 1 rather than growing with N.
// Against the unfixed AddCluster it reports N distinct sets.
//
// The second test is the opposite direction, so neither can pass
// vacuously: a kubeconfig whose bytes CHANGED (token rotation #5210, the
// #3991/#4000 EIP→private-SAN heal) must still rebuild.
package k8scache

import (
	"fmt"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// writeKubeconfig drops a syntactically-valid kubeconfig carrying `token`
// into dir and returns its path. AddCluster parses it and builds clients
// but never dials — these tests deliberately do not call Start().
func writeKubeconfig(t *testing.T, path, token string) {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://10.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: %s
`, token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig %q: %v", path, err)
	}
}

// registryTwoPlainKinds — two universally-present, non-Optional kinds, so
// AddCluster takes the no-discovery-probe fast path (#5352) and the test
// never touches the network.
func registryTwoPlainKinds(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true}); err != nil {
		t.Fatalf("registry add pod: %v", err)
	}
	if err := r.Add(Kind{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}}); err != nil {
		t.Fatalf("registry add namespace: %v", err)
	}
	return r
}

// clusterStateFor returns the live *clusterState pointer for id, or nil.
// Pointer identity is the observable for "was a new informer set built".
func clusterStateFor(f *Factory, id string) *clusterState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.clusters[id]
}

// TestAddCluster_UnchangedKubeconfigDoesNotRebuild_BoundedGrowth is the
// bounded-growth guard. Retained structure = the set of informer sets
// constructed for one cluster id. It must NOT grow with the number of
// re-registrations.
func TestAddCluster_UnchangedKubeconfigDoesNotRebuild_BoundedGrowth(t *testing.T) {
	const n = 25

	dir := t.TempDir()
	path := dir + "/dep-me-east-215-b-1.yaml"
	writeKubeconfig(t, path, "static-token")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: registryTwoPlainKinds(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ref := ClusterRef{ID: "dep-me-east-215-b-1", KubeconfigPath: path}

	// Every distinct *clusterState observed is one informer set that was
	// constructed. A set that stays at size 1 across n registrations is
	// the property under test.
	built := map[*clusterState]struct{}{}
	for i := 0; i < n; i++ {
		if err := f.AddCluster(ref); err != nil {
			t.Fatalf("AddCluster call %d: %v", i+1, err)
		}
		cs := clusterStateFor(f, ref.ID)
		if cs == nil {
			t.Fatalf("AddCluster call %d: cluster not registered", i+1)
		}
		built[cs] = struct{}{}
	}

	if len(built) != 1 {
		t.Fatalf("re-registering an UNCHANGED kubeconfig %d times built %d informer sets; want 1 — every extra set is a full re-ADD sweep whose predecessor is not reclaimed (#5642)",
			n, len(built))
	}

	// The surviving set must still be a working registration, not a
	// short-circuit that left the cluster half-registered.
	cs := clusterStateFor(f, ref.ID)
	if got := len(cs.informers); got != 2 {
		t.Fatalf("surviving clusterState has %d informers; want 2", got)
	}
	if cs.kubeconfigSHA == "" {
		t.Fatal("surviving clusterState carries no kubeconfig fingerprint; the gate can never match again")
	}
	if !containsClusterID(f.Clusters(), ref.ID) {
		t.Fatalf("Clusters() = %v, want to contain %q", f.Clusters(), ref.ID)
	}
}

// TestAddCluster_ChangedKubeconfigStillRebuilds is the opposite
// direction. Without it the guard above would also pass on an AddCluster
// that never rebuilds anything — which would strand a rotated token
// (#5210) or a healed server host (#3991/#4000) on dead credentials.
func TestAddCluster_ChangedKubeconfigStillRebuilds(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dep-me-east-215-b-1.yaml"
	writeKubeconfig(t, path, "first-token")

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: registryTwoPlainKinds(t)})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ref := ClusterRef{ID: "dep-me-east-215-b-1", KubeconfigPath: path}
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster (first): %v", err)
	}
	first := clusterStateFor(f, ref.ID)
	if first == nil {
		t.Fatal("first AddCluster did not register the cluster")
	}
	firstSHA := first.kubeconfigSHA

	// Rotate the credential on disk — the #5210 shape.
	writeKubeconfig(t, path, "rotated-token")
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster (after rotation): %v", err)
	}
	second := clusterStateFor(f, ref.ID)
	if second == nil {
		t.Fatal("post-rotation AddCluster did not register the cluster")
	}
	if second == first {
		t.Fatal("a CHANGED kubeconfig reused the old informer set; the rotated credential would never reach the reflectors (#5210)")
	}
	if second.kubeconfigSHA == firstSHA {
		t.Fatalf("fingerprint did not change across a credential rotation (%q); the gate is keyed on something that is not the bytes", firstSHA)
	}
}

// TestAddCluster_PrebuiltClientRefStillRebuilds — refs carrying a
// pre-built DynamicClient (chroot self-register, test injection) have no
// backing file and therefore no fingerprint. They must keep the old
// unconditional behaviour rather than being silently short-circuited.
func TestAddCluster_PrebuiltClientRefStillRebuilds(t *testing.T) {
	podA := newPodWithLabels("default", "p", map[string]string{"app": "a"}, "")
	dynA, coreA := fakeClientsWithRS(podA)

	f, err := NewFactory(Config{Logger: quietLogger(), Registry: registryWithRSAndPod()})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ref := ClusterRef{ID: "chroot", DynamicClient: dynA, CoreClient: coreA}
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster (first): %v", err)
	}
	first := clusterStateFor(f, "chroot")
	if first == nil {
		t.Fatal("first AddCluster did not register the cluster")
	}
	if first.kubeconfigSHA != "" {
		t.Fatalf("pre-built-client registration carries fingerprint %q; it has no backing file to fingerprint", first.kubeconfigSHA)
	}
	if err := f.AddCluster(ref); err != nil {
		t.Fatalf("AddCluster (second): %v", err)
	}
	if clusterStateFor(f, "chroot") == first {
		t.Fatal("pre-built-client re-registration was short-circuited; that path must keep rebuilding")
	}
}
