// k8scache_quarantine_5285_test.go — #5285 durable-restart guard.
//
// The terminal-conclusion QuarantineDeployment call (phase1_watch.go) is
// in-memory only. On a catalyst-api Pod restart, restoreFromStore() reloads
// every deployment record and the k8scache rescan loop re-registers every
// lingering kubeconfig on the PVC — including a failed env's kubeconfig,
// which is deliberately kept for the eventual wipe. Without re-deriving the
// quarantine from the persisted Status=="failed" records, the restarted Pod
// would resurrect the 404-flood against the failed env's missing CRDs.
//
// This test proves SetK8sCache re-quarantines exactly the failed
// deployments (primary + secondary) and leaves healthy ones registered.
package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
)

func fakeDynForQuarantineTest() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"})
}

func clustersContain(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestSetK8sCache_QuarantinesFailedDeploymentsAcrossRestart(t *testing.T) {
	// A restarted Pod's k8scache has re-registered every lingering
	// kubeconfig: a fully-failed env (primary + secondary) and a healthy
	// (ready) env. No Start() → informers built but never dial.
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	for _, id := range []string{"faileddep", "faileddep-b", "readydep"} {
		if err := f.AddCluster(k8scache.ClusterRef{
			ID:            id,
			DynamicClient: fakeDynForQuarantineTest(),
			CoreClient:    kfake.NewSimpleClientset(),
		}); err != nil {
			t.Fatalf("AddCluster(%s): %v", id, err)
		}
	}

	// A Handler restored from the store: one failed record, one ready.
	h := &Handler{}
	h.deployments.Store("faileddep", &Deployment{ID: "faileddep", Status: "failed"})
	h.deployments.Store("readydep", &Deployment{ID: "readydep", Status: "ready"})

	// main.go calls this at boot, after restoreFromStore populated
	// h.deployments.
	h.SetK8sCache(f, nil, "X-Forwarded-User")

	if clustersContain(f.Clusters(), "faileddep") {
		t.Errorf("failed deployment primary was not re-quarantined across restart (still registered) — the flood would resume")
	}
	if clustersContain(f.Clusters(), "faileddep-b") {
		t.Errorf("failed deployment SECONDARY was not re-quarantined across restart (still registered)")
	}
	if !clustersContain(f.Clusters(), "readydep") {
		t.Errorf("healthy 'ready' deployment was wrongly quarantined on restart — console live view would go dark")
	}

	// The quarantine must also block a subsequent rescan re-register of the
	// failed env while its kubeconfig lingers.
	if err := f.AddCluster(k8scache.ClusterRef{
		ID:            "faileddep",
		DynamicClient: fakeDynForQuarantineTest(),
		CoreClient:    kfake.NewSimpleClientset(),
	}); err != nil {
		t.Fatalf("AddCluster(faileddep) while quarantined: %v", err)
	}
	if clustersContain(f.Clusters(), "faileddep") {
		t.Errorf("quarantined failed deployment was re-registered by a later rescan/AddCluster")
	}
}
