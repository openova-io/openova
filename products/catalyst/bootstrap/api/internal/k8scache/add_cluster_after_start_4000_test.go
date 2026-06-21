// add_cluster_after_start_4000_test.go — #4000 regression guard.
//
// Root cause (the second hw174 root, no prior iteration fixed it): AddCluster
// BUILT a cluster's informers but never STARTED them. Only Factory.Start()
// started informers, and only for clusters present at boot. So a cluster
// registered at RUNTIME — by the kubeconfigs rescan loop, the
// secondary-kubeconfig POST handler, or the #4001 self-heal — registered in
// Clusters() but its informers never ran, so List() returned no objects. On
// hw174 the in-cluster catalyst-api received region-b's kubeconfig (eventually)
// yet placement still saw an empty region-b pod list → false `singleton`.
//
// This test proves AddCluster-AFTER-Start now starts the informers + the
// sync-watcher, so List() returns the new cluster's objects.
package k8scache

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

func TestAddClusterAfterStart_StartsInformers(t *testing.T) {
	// Start a factory with ONE cluster present at boot.
	podA := newPodWithLabels("default", "boot-pod", map[string]string{"app": "boot"}, "")
	dynA, coreA := fakeClientsWithRS(podA)
	cfg := Config{
		Logger:   quietLogger(),
		Registry: registryWithRSAndPod(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dynA, CoreClient: coreA}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Boot cluster works (sanity).
	waitForList(t, f, "pod", 1)

	// Now register a SECOND cluster AFTER Start — the runtime-add path the
	// secondary-kubeconfig delivery exercises on a chroot. Pre-#4000 its
	// informers never started and the assertion below would hang/fail.
	podB := newPodWithLabels("region-b", "regionb-pod", map[string]string{"app": "rb"}, "")
	dynB, coreB := fakeClientsWithRS(podB)
	if err := f.AddCluster(ClusterRef{ID: "beta", DynamicClient: dynB, CoreClient: coreB}); err != nil {
		t.Fatalf("AddCluster(beta) after Start: %v", err)
	}

	// beta must appear in Clusters() AND its informer must actually serve List.
	if !containsClusterID(f.Clusters(), "beta") {
		t.Fatalf("Clusters() = %v, want to contain beta", f.Clusters())
	}
	waitForListOnCluster(t, f, "beta", "pod", 1)
}

func containsClusterID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// waitForListOnCluster is waitForList parametrised by cluster id (the package
// helper hardcodes "alpha").
func waitForListOnCluster(t *testing.T, f *Factory, clusterID, kind string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items, _, err := f.List(clusterID, kind, labels.Everything())
		if err == nil && len(items) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("cluster %q informer %q never reached %d items (AddCluster-after-Start did not start informers)", clusterID, kind, want)
}
