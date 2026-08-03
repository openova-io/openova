// post_handover_policy_enforce_test.go — unit coverage for the Wave 5.90
// phase 2b bootstrapMode flip (#2441), specifically its #5591 contract:
// the flip PATCHes the bp-kyverno-policies HelmRelease in EVERY region of
// the deployment, not just the primary. Live-proven defect: on hw291 and
// hw292 (2-region me-east-215-a/-b-1) region-a came up Enforce while
// region-b stayed Audit forever, because the flip read only
// `<depID>.yaml` and never the secondary `<depID>-<regionKey>.yaml`.
package handler

import (
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// policyFlipHRObj builds the bp-kyverno-policies HelmRelease as a fresh
// prov installs it: bootstrapMode true (every policy at Audit).
func policyFlipHRObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      bpKyvernoPoliciesHRName,
			"namespace": bpKyvernoPoliciesHRNamespace,
		},
		"spec": map[string]any{
			"values": map[string]any{
				"compliancePolicies": map[string]any{
					"bootstrapMode": true,
				},
			},
		},
	}}
}

func policyFlipFakeDynamic() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		helmReleaseGVR: "HelmReleaseList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, policyFlipHRObj())
}

// policyFlipBootstrapMode reads the HR's live bootstrapMode from a fake.
func policyFlipBootstrapMode(t *testing.T, fake *dynamicfake.FakeDynamicClient) (bool, bool) {
	t.Helper()
	obj, err := fake.Resource(helmReleaseGVR).Namespace(bpKyvernoPoliciesHRNamespace).Get(t.Context(), bpKyvernoPoliciesHRName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HR from fake: %v", err)
	}
	v, found, err := unstructured.NestedBool(obj.Object, "spec", "values", "compliancePolicies", "bootstrapMode")
	if err != nil {
		t.Fatalf("read bootstrapMode: %v", err)
	}
	return v, found
}

// stubPolicyFlipClients routes each kubeconfig file's CONTENT to its
// region's fake client, restoring the real seam on cleanup.
func stubPolicyFlipClients(t *testing.T, byContent map[string]*dynamicfake.FakeDynamicClient) {
	t.Helper()
	orig := dynamicClientForPolicyFlip
	dynamicClientForPolicyFlip = func(kcRaw []byte) (dynamic.Interface, error) {
		fake, ok := byContent[string(kcRaw)]
		if !ok {
			t.Fatalf("dynamicClientForPolicyFlip called with unexpected kubeconfig content %q", string(kcRaw))
		}
		return fake, nil
	}
	t.Cleanup(func() { dynamicClientForPolicyFlip = orig })
}

// TestPolicyEnforceFlip_FlipsEveryRegion_5591 — a 2-region deployment must
// end with bootstrapMode=false on BOTH regions' HRs. The secondary
// kubeconfig is discovered via the on-disk `<depID>-<regionKey>.yaml`
// store (the restart-immune #4000 path), NOT the in-memory map.
func TestPolicyEnforceFlip_FlipsEveryRegion_5591(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "dep-1.yaml"), []byte("kc-primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dep-1-eu-west-101.yaml"), []byte("kc-secondary"), 0o600); err != nil {
		t.Fatal(err)
	}

	primary := policyFlipFakeDynamic()
	secondary := policyFlipFakeDynamic()
	stubPolicyFlipClients(t, map[string]*dynamicfake.FakeDynamicClient{
		"kc-primary":   primary,
		"kc-secondary": secondary,
	})

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	dep := &Deployment{
		ID: "dep-1",
		Request: provisioner.Request{
			SovereignFQDN: "t99.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: "me-east-215"},
				{CloudRegion: "eu-west-101"},
			},
		},
	}

	h.runPostHandoverPolicyEnforceFlip(dep)

	for label, fake := range map[string]*dynamicfake.FakeDynamicClient{"primary": primary, "secondary": secondary} {
		v, found := policyFlipBootstrapMode(t, fake)
		if !found || v {
			t.Errorf("%s region: bootstrapMode = %v (found=%v), want false — the #5591 regression leaves a region at Audit forever", label, v, found)
		}
	}
}

// TestPolicyEnforceFlip_SingleRegionUnchanged — a 1-region deployment
// flips exactly its primary; no secondary lookup may fail the run.
func TestPolicyEnforceFlip_SingleRegionUnchanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "dep-2.yaml"), []byte("kc-primary"), 0o600); err != nil {
		t.Fatal(err)
	}

	primary := policyFlipFakeDynamic()
	stubPolicyFlipClients(t, map[string]*dynamicfake.FakeDynamicClient{"kc-primary": primary})

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	dep := &Deployment{
		ID: "dep-2",
		Request: provisioner.Request{
			SovereignFQDN: "t98.omani.works",
			Regions:       []provisioner.RegionSpec{{CloudRegion: "me-east-215"}},
		},
	}

	h.runPostHandoverPolicyEnforceFlip(dep)

	if v, found := policyFlipBootstrapMode(t, primary); !found || v {
		t.Errorf("primary bootstrapMode = %v (found=%v), want false", v, found)
	}
}

// TestPolicyEnforceFlip_SecondaryUnreadableStillFlipsPrimary — per-region
// best-effort: an in-memory secondary path whose file is gone must not
// abort the primary flip (idempotent re-trigger covers the secondary).
func TestPolicyEnforceFlip_SecondaryUnreadableStillFlipsPrimary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "dep-3.yaml"), []byte("kc-primary"), 0o600); err != nil {
		t.Fatal(err)
	}

	primary := policyFlipFakeDynamic()
	stubPolicyFlipClients(t, map[string]*dynamicfake.FakeDynamicClient{"kc-primary": primary})

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	dep := &Deployment{
		ID: "dep-3",
		Request: provisioner.Request{
			SovereignFQDN: "t97.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: "me-east-215"},
				{CloudRegion: "eu-west-101"},
			},
		},
		secondaryKubeconfigPaths: map[string]string{
			"eu-west-101": filepath.Join(dir, "does-not-exist.yaml"),
		},
	}

	h.runPostHandoverPolicyEnforceFlip(dep)

	if v, found := policyFlipBootstrapMode(t, primary); !found || v {
		t.Errorf("primary bootstrapMode = %v (found=%v), want false even when a secondary kubeconfig is unreadable", v, found)
	}
}
