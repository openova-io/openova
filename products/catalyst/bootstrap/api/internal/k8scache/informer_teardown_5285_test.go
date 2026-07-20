// informer_teardown_5285_test.go — #5285 regression guard.
//
// Root cause: a per-deployment k8scache cluster (keyed `<depID>` and
// `<depID>-<region>`) watches every DefaultKind, INCLUDING the Catalyst
// CRDs cnpgpairs / continuums / organizations / useraccesses. When a
// deployment concluded terminal-FAILED, nothing tore its informer set
// down — RemoveCluster only ran on wipe. Worse, the failed env's
// kubeconfig deliberately lingers on the PVC for the eventual wipe, so a
// bare RemoveCluster would be undone within one rescan tick (≤30s), which
// re-registers any kubeconfig still on disk. The failed apiserver is up
// but its CRDs never installed, so the reflectors 404-flood ("the server
// could not find the requested resource"), spike catalyst-api CPU, and
// starve the /wipe + /deployments control endpoints needed to recover the
// env (hw279, dep 059126bb).
//
// The fix: on terminal-FAILED conclusion the handler calls
// QuarantineDeployment(depID) — it tears down every `<depID>` /
// `<depID>-<region>` reflector set AND suppresses re-registration by the
// rescan loop / AddCluster while the kubeconfig lingers, self-clearing
// once the eventual wipe removes the kubeconfig.
package k8scache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeMinimalKubeconfig writes a valid token-auth kubeconfig for cluster
// id `id` into `dir/<id>.yaml` (the shape LoadClustersFromDir + AddCluster
// both accept). The server IP is a black hole — the informers built off it
// are never Start()ed in these tests, so nothing dials it.
func writeMinimalKubeconfig(t *testing.T, dir, id string) {
	t.Helper()
	kc := "apiVersion: v1\n" +
		"kind: Config\n" +
		"clusters:\n" +
		"- cluster:\n" +
		"    server: https://10.255.0.1:6443\n" +
		"    insecure-skip-tls-verify: true\n" +
		"  name: c\n" +
		"contexts:\n" +
		"- context:\n" +
		"    cluster: c\n" +
		"    user: u\n" +
		"  name: c\n" +
		"current-context: c\n" +
		"users:\n" +
		"- name: u\n" +
		"  user:\n" +
		"    token: test-token\n"
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(kc), 0o600); err != nil {
		t.Fatalf("write kubeconfig %s: %v", id, err)
	}
}

// TestQuarantineDeployment_StopsAndSuppressesReflectors is the hermetic
// method-level guard: QuarantineDeployment removes every cluster of a
// deployment (primary + secondary), AddCluster then REFUSES to re-register
// them (any resurrection path), and UnquarantineDeployment restores the
// ability to register again. Unrelated deployments are untouched.
func TestQuarantineDeployment_StopsAndSuppressesReflectors(t *testing.T) {
	f, err := NewFactory(Config{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	// Register a failed deployment's primary + secondary + an unrelated
	// healthy deployment. No Start() → informers are built but never dial.
	addCluster := func(id string) {
		dyn, core := fakeClients()
		if err := f.AddCluster(ClusterRef{ID: id, DynamicClient: dyn, CoreClient: core}); err != nil {
			t.Fatalf("AddCluster(%s): %v", id, err)
		}
	}
	addCluster("dep1")
	addCluster("dep1-b")
	addCluster("other")

	for _, id := range []string{"dep1", "dep1-b", "other"} {
		if !containsClusterID(f.Clusters(), id) {
			t.Fatalf("precondition: Clusters()=%v missing %q", f.Clusters(), id)
		}
	}

	// Conclude dep1 as terminal-FAILED.
	f.QuarantineDeployment("dep1")

	if containsClusterID(f.Clusters(), "dep1") {
		t.Errorf("after QuarantineDeployment: primary dep1 still registered — reflectors not torn down")
	}
	if containsClusterID(f.Clusters(), "dep1-b") {
		t.Errorf("after QuarantineDeployment: secondary dep1-b still registered — secondary reflectors not torn down")
	}
	if !containsClusterID(f.Clusters(), "other") {
		t.Errorf("after QuarantineDeployment: unrelated deployment 'other' was wrongly removed")
	}

	// The crux of #5285: re-registration (the rescan/POST resurrection
	// vector while the kubeconfig lingers) must be REFUSED for both the
	// primary and the secondary. AddCluster returns nil (no-op) but the
	// cluster must NOT reappear.
	for _, id := range []string{"dep1", "dep1-b"} {
		dyn, core := fakeClients()
		if err := f.AddCluster(ClusterRef{ID: id, DynamicClient: dyn, CoreClient: core}); err != nil {
			t.Fatalf("AddCluster(%s) while quarantined returned error, want nil no-op: %v", id, err)
		}
		if containsClusterID(f.Clusters(), id) {
			t.Errorf("quarantined %q was re-registered by AddCluster — the 404-flood would resume", id)
		}
	}

	// Wipe path clears the quarantine (kubeconfig removed) → registration
	// works again.
	f.UnquarantineDeployment("dep1")
	dyn, core := fakeClients()
	if err := f.AddCluster(ClusterRef{ID: "dep1", DynamicClient: dyn, CoreClient: core}); err != nil {
		t.Fatalf("AddCluster(dep1) after Unquarantine: %v", err)
	}
	if !containsClusterID(f.Clusters(), "dep1") {
		t.Errorf("after UnquarantineDeployment, dep1 failed to re-register")
	}
}

// TestRemoveDeployment_TearsDownPrimaryAndSecondaries proves the wipe-path
// helper removes BOTH the bare `<depID>` primary and every
// `<depID>-<region>` secondary (the bare-id RemoveCluster missed the
// secondaries), without quarantining — so a subsequent AddCluster of the
// same id can proceed (wipe deletes the kubeconfig, no resurrection risk).
func TestRemoveDeployment_TearsDownPrimaryAndSecondaries(t *testing.T) {
	f, err := NewFactory(Config{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	for _, id := range []string{"dep2", "dep2-a", "dep2-b", "other"} {
		dyn, core := fakeClients()
		if err := f.AddCluster(ClusterRef{ID: id, DynamicClient: dyn, CoreClient: core}); err != nil {
			t.Fatalf("AddCluster(%s): %v", id, err)
		}
	}

	f.RemoveDeployment("dep2")

	for _, id := range []string{"dep2", "dep2-a", "dep2-b"} {
		if containsClusterID(f.Clusters(), id) {
			t.Errorf("RemoveDeployment left %q registered", id)
		}
	}
	if !containsClusterID(f.Clusters(), "other") {
		t.Errorf("RemoveDeployment wrongly removed unrelated 'other'")
	}

	// RemoveDeployment must NOT quarantine — a same-id re-register works.
	dyn, core := fakeClients()
	if err := f.AddCluster(ClusterRef{ID: "dep2", DynamicClient: dyn, CoreClient: core}); err != nil {
		t.Fatalf("AddCluster(dep2) after RemoveDeployment: %v", err)
	}
	if !containsClusterID(f.Clusters(), "dep2") {
		t.Errorf("RemoveDeployment wrongly quarantined dep2 (re-register refused)")
	}
}

// TestFailedDeployment_RescanDoesNotResurrectReflectors is the end-to-end
// guard against the real defect: the kubeconfigs rescan loop must NOT
// re-register a terminally-failed deployment whose kubeconfig still lingers
// on disk (kept for the eventual wipe), and the quarantine must self-clear
// once that kubeconfig is finally removed.
func TestFailedDeployment_RescanDoesNotResurrectReflectors(t *testing.T) {
	dir := t.TempDir()
	writeMinimalKubeconfig(t, dir, "dep1")   // primary
	writeMinimalKubeconfig(t, dir, "dep1-b") // secondary region

	f, err := NewFactory(Config{Logger: quietLogger(), KubeconfigsDir: dir})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	ctx := context.Background()

	// First rescan registers both clusters from disk (informers built, not
	// started — the factory was never Start()ed).
	f.rescanOnce(ctx)
	for _, id := range []string{"dep1", "dep1-b"} {
		if !containsClusterID(f.Clusters(), id) {
			t.Fatalf("precondition: rescan did not register %q (Clusters=%v)", id, f.Clusters())
		}
	}

	// Deployment concludes terminal-FAILED.
	f.QuarantineDeployment("dep1")
	if containsClusterID(f.Clusters(), "dep1") || containsClusterID(f.Clusters(), "dep1-b") {
		t.Fatalf("QuarantineDeployment left a cluster registered: %v", f.Clusters())
	}

	// The kubeconfig files STILL exist on disk (kept for wipe). A rescan
	// MUST NOT resurrect the 404-flooding reflectors.
	f.rescanOnce(ctx)
	for _, id := range []string{"dep1", "dep1-b"} {
		if containsClusterID(f.Clusters(), id) {
			t.Fatalf("rescan RESURRECTED quarantined %q — the reflector flood would resume (the #5285 defect)", id)
		}
	}

	// The eventual wipe removes the kubeconfig files. The next rescan
	// self-clears the quarantine (bounded set), and a genuinely new
	// kubeconfig with the same id may register again.
	if err := os.Remove(filepath.Join(dir, "dep1.yaml")); err != nil {
		t.Fatalf("remove dep1.yaml: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "dep1-b.yaml")); err != nil {
		t.Fatalf("remove dep1-b.yaml: %v", err)
	}
	f.rescanOnce(ctx) // sees the files gone → evictStaleQuarantine clears dep1

	writeMinimalKubeconfig(t, dir, "dep1")
	f.rescanOnce(ctx)
	if !containsClusterID(f.Clusters(), "dep1") {
		t.Errorf("after wipe removed the kubeconfig, quarantine did not self-clear — a fresh same-id kubeconfig failed to register")
	}
}
