package handler

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// stubInClusterConfig temporarily points the materialize seam at a synthetic
// rest.Config (or a forced error) so the test never needs a real
// ServiceAccount mount. Restores the production hook on cleanup.
func stubInClusterConfig(t *testing.T, cfg *rest.Config, err error) {
	t.Helper()
	prev := inClusterConfigForMaterialize
	inClusterConfigForMaterialize = func() (*rest.Config, error) { return cfg, err }
	t.Cleanup(func() { inClusterConfigForMaterialize = prev })
}

// TestResolvePrimaryKubeconfig_ChrootMaterializesInClusterPrimary is the #5131
// reproduction: a healthy fresh 2-region Sovereign (hw259 shape) where the
// SECONDARY (region-b) kubeconfig resolves fine but the PRIMARY (local
// region-a) kubeconfig FILE is absent, because the mothership only ever posts
// SECONDARY kubeconfigs to the chroot. Before the fix, resolvePrimaryKubeconfig-
// Path's os.Stat missed forever, shouldStartupClusterMeshReconcile returned
// false on every attempt, and the retry exhausted its budget with "primary
// kubeconfig unresolved — next trigger is a catalyst-api restart", so the mesh
// establish (and the multi-region convergence that arms self-sovereign-cutover)
// never ran. The fix materializes the primary from the in-cluster
// ServiceAccount (no network) so the startup reconcile PROCEEDS.
func TestResolvePrimaryKubeconfig_ChrootMaterializesInClusterPrimary(t *testing.T) {
	const (
		depID      = "97917f0298eb14eb"
		sovFQDN    = "tcm.example.io"
		inClusHost = "https://10.0.0.1:6443"
		saToken    = "sa-bearer-token-xyz"
	)
	twoRegions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}

	newDep := func() *Deployment {
		return &Deployment{
			ID:     depID,
			Status: "ready",
			Request: provisioner.Request{
				Regions:       twoRegions,
				SovereignFQDN: sovFQDN,
			},
			// Result nil mimics the omitempty KubeconfigPath lost on a PVC restore.
		}
	}

	t.Run("chroot-materializes-primary-and-reconcile-proceeds", func(t *testing.T) {
		dir := t.TempDir()
		h := &Handler{log: silentLogger(), kubeconfigsDir: dir}

		// The SECONDARY (region-b) kubeconfig is present on the chroot's PVC —
		// it was POSTed at handover — proving the asymmetry the bug hinges on.
		secondaryPath := filepath.Join(dir, depID+"-me-east-215-b-1.yaml")
		if err := os.WriteFile(secondaryPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
			t.Fatalf("seed secondary kubeconfig: %v", err)
		}
		primaryPath := filepath.Join(dir, depID+".yaml")
		if _, err := os.Stat(primaryPath); err == nil {
			t.Fatalf("precondition: primary kubeconfig must be ABSENT to reproduce #5131")
		}

		// Chroot mode: SOVEREIGN_FQDN matches the deployment's FQDN, and the
		// in-cluster config resolves (no network — synthetic here).
		t.Setenv("SOVEREIGN_FQDN", sovFQDN)
		stubInClusterConfig(t, &rest.Config{
			Host:        inClusHost,
			BearerToken: saToken,
			TLSClientConfig: rest.TLSClientConfig{
				CAData: []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"),
			},
		}, nil)

		dep := newDep()

		// The startup reconcile must PROCEED (not skip) on the ready 2-region
		// Sovereign: the primary is materialized from the in-cluster SA.
		if !h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("startup reconcile skipped — #5131 regression: chroot primary kubeconfig was not materialized")
		}

		// READ-ONLY(dep.Result) contract: materialization writes a FILE, never
		// stamps the record (the State() -race guard, #3241).
		if dep.Result != nil {
			t.Errorf("gate mutated dep.Result: %+v — must stay nil", dep.Result)
		}

		// The primary file now exists at the canonical path…
		raw, err := os.ReadFile(primaryPath)
		if err != nil {
			t.Fatalf("primary kubeconfig not materialized at %s: %v", primaryPath, err)
		}
		// …and it is a valid, buildable kubeconfig pointing at the local
		// apiserver with the SA bearer token (this is what buildRegionSlots'
		// NewKubernetesClientFromKubeconfig consumes for the primary slot).
		restCfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
		if err != nil {
			t.Fatalf("materialized primary kubeconfig does not parse: %v", err)
		}
		if restCfg.Host != inClusHost {
			t.Errorf("materialized primary host = %q, want %q", restCfg.Host, inClusHost)
		}
		if restCfg.BearerToken != saToken {
			t.Errorf("materialized primary token = %q, want %q", restCfg.BearerToken, saToken)
		}

		// resolvePrimaryKubeconfigPath resolves to the canonical path now.
		got, ok := h.resolvePrimaryKubeconfigPath(dep)
		if !ok || got != primaryPath {
			t.Errorf("resolvePrimaryKubeconfigPath = (%q, %v), want (%q, true)", got, ok, primaryPath)
		}
	})

	t.Run("mothership-does-not-materialize", func(t *testing.T) {
		dir := t.TempDir()
		h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
		// Mothership: SOVEREIGN_FQDN unset. Even if in-cluster config were
		// resolvable, the chroot discriminator must gate materialization off so
		// a genuinely-missing mothership primary still warn-and-skips.
		t.Setenv("SOVEREIGN_FQDN", "")
		stubInClusterConfig(t, &rest.Config{Host: inClusHost, BearerToken: saToken}, nil)

		dep := newDep()
		if h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("mothership must NOT materialize a self-kubeconfig — reconcile should skip when the primary file is genuinely absent")
		}
		if _, err := os.Stat(filepath.Join(dir, depID+".yaml")); err == nil {
			t.Errorf("mothership wrote a primary kubeconfig it must never synthesize")
		}
	})

	t.Run("chroot-inclusterconfig-unavailable-skips", func(t *testing.T) {
		dir := t.TempDir()
		h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
		t.Setenv("SOVEREIGN_FQDN", sovFQDN)
		// In-cluster config unavailable (off-cluster dev / CI) → best-effort
		// no-op, honest skip, no file written.
		stubInClusterConfig(t, nil, os.ErrNotExist)

		dep := newDep()
		if h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("reconcile must skip when in-cluster config is unavailable")
		}
		if _, err := os.Stat(filepath.Join(dir, depID+".yaml")); err == nil {
			t.Errorf("primary kubeconfig written despite in-cluster config being unavailable")
		}
	})
}
