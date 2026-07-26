package handler

// #5359 — secondary-region credential bridge tests.
//
// The chart's steps 05/06/08 grew region-B legs that read
// `/secondary-kubeconfigs/*.yaml` from the cutover-secondary-kubeconfigs
// Secret. These tests pin the engine-side contract that feeds that mount:
//
//   1. Single-region deployment → NO Secret (and a stale one is deleted)
//      → the chart legs no-op → pre-#5359 behavior byte-identical.
//   2. Multi-region deployment with every secondary kubeconfig on disk →
//      Secret carries one `<regionKey>.yaml` key per secondary.
//   3. Multi-region deployment whose secondary kubeconfig is MISSING →
//      hard error (runCutover aborts before step 1) — never a silent
//      single-region chain that would re-mint the #5359 false positive.
//   4. Stale keys from a prior topology are replaced, not merged.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// newSecondaryKubeconfigFixture builds a chroot-shaped Handler + fake
// cutoverDeps: SOVEREIGN_FQDN set, one deployment record served by this
// chroot, kubeconfigs dir pointed at a temp dir.
func newSecondaryKubeconfigFixture(t *testing.T, regions int) (*Handler, *cutoverDeps, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "hw288.omani.works")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{
		ID: "dep5359",
		Request: provisioner.Request{
			SovereignFQDN: "hw288.omani.works",
		},
	}
	for i := 0; i < regions; i++ {
		dep.Request.Regions = append(dep.Request.Regions, provisioner.RegionSpec{
			Provider:    "huawei",
			CloudRegion: "me-east-215",
		})
	}
	h.deployments.Store(dep.ID, dep)

	client := fakek8s.NewSimpleClientset()
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}
	return h, deps, dir
}

func writeSecondaryKubeconfig(t *testing.T, dir, depID, region string) {
	t.Helper()
	path := filepath.Join(dir, depID+"-"+region+".yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\nclusters: []\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMaterializeSecondaryKubeconfigs_SingleRegionDeletesStaleSecret(t *testing.T) {
	h, deps, _ := newSecondaryKubeconfigFixture(t, 1)

	// Seed a STALE Secret from a prior multi-region life — the
	// single-region run must remove it so the chart mount stays empty.
	_, err := deps.core.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverSecondaryKubeconfigsSecretName(), Namespace: cutoverTestNS},
		Data:       map[string][]byte{"old-region.yaml": []byte("stale")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed stale secret: %v", err)
	}

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0 for single-region", n)
	}
	_, err = deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err == nil {
		t.Fatalf("stale Secret survived a single-region run — chart legs would falsely fire a region-B pivot")
	}
}

func TestMaterializeSecondaryKubeconfigs_TwoRegionMaterializesSecret(t *testing.T) {
	h, deps, dir := newSecondaryKubeconfigFixture(t, 2)
	writeSecondaryKubeconfig(t, dir, "dep5359", "me-east-215-b")

	n, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	sec, err := deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, ok := sec.Data["me-east-215-b.yaml"]; !ok {
		t.Fatalf("secret missing me-east-215-b.yaml key; got keys %v", secretDataKeys(sec))
	}
}

func TestMaterializeSecondaryKubeconfigs_MissingKubeconfigFailsLoud(t *testing.T) {
	// 2-region spec, but NO secondary kubeconfig on disk and none in
	// memory — the run must ABORT, not proceed single-region.
	h, deps, _ := newSecondaryKubeconfigFixture(t, 2)

	if _, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps); err == nil {
		t.Fatalf("expected fail-loud error for a 2-region deployment with no secondary kubeconfig (the #5359 false-positive path); got nil")
	}
}

func TestMaterializeSecondaryKubeconfigs_ReplacesStaleKeys(t *testing.T) {
	h, deps, dir := newSecondaryKubeconfigFixture(t, 2)
	writeSecondaryKubeconfig(t, dir, "dep5359", "me-east-215-b")

	// Seed a Secret carrying a key from a PRIOR topology.
	_, err := deps.core.CoreV1().Secrets(cutoverTestNS).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cutoverSecondaryKubeconfigsSecretName(), Namespace: cutoverTestNS},
		Data:       map[string][]byte{"gone-region.yaml": []byte("stale")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	if _, err := h.materializeSecondaryKubeconfigsSecret(context.Background(), deps); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	sec, err := deps.core.CoreV1().Secrets(cutoverTestNS).Get(context.Background(), cutoverSecondaryKubeconfigsSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, stale := sec.Data["gone-region.yaml"]; stale {
		t.Fatalf("stale region key survived the data replace; got keys %v", secretDataKeys(sec))
	}
	if _, ok := sec.Data["me-east-215-b.yaml"]; !ok {
		t.Fatalf("current region key absent after update; got keys %v", secretDataKeys(sec))
	}
}

func secretDataKeys(s *corev1.Secret) []string {
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	return keys
}
