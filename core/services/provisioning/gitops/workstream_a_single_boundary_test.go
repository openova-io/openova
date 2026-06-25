package gitops

import (
	"strings"
	"testing"
)

// Workstream A (#4290 / EPIC #4293) — single-boundary regression guard.
//
// The funnel (Door 3) no longer builds its own `tenant-<slug>` boundary; the
// org-controller (Door 2) is the SINGLE producer of the `<slug>` namespace +
// `vcluster` HelmRelease. The funnel emits ONLY the app-install tree (apps/),
// the apps-sync Flux Kustomization, the provisioning RBAC, and the host
// ingress — all targeting the org-controller-owned `<slug>` namespace.
//
// TestWorkstreamA_FunnelRendersNoBoundary proves the funnel emits ZERO host
// namespace / vcluster HelmRelease and targets the org-controller `<slug>`.
func TestWorkstreamA_FunnelRendersNoBoundary(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/tenants")
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"wordpress"}, "", nil)

	for path, body := range out {
		t.Logf("FILE %s", path)
		// No funnel host namespace.
		if strings.Contains(path, "namespace.yaml") && !strings.Contains(path, "/apps/") {
			t.Errorf("STRAY: funnel emitted a HOST namespace.yaml: %s", path)
		}
		// No funnel vcluster HelmRelease.
		if strings.Contains(path, "vcluster.yaml") {
			t.Errorf("STRAY: funnel emitted vcluster.yaml: %s", path)
		}
		// No `tenant-acme` host namespace anywhere in the host-scoped files.
		if strings.HasSuffix(path, "/apps-sync.yaml") {
			if !strings.Contains(body, "targetNamespace: acme") {
				t.Errorf("apps-sync targetNamespace is NOT <slug> acme:\n%s", body)
			}
			if strings.Contains(body, "targetNamespace: tenant-acme") {
				t.Errorf("STRAY: apps-sync still targets tenant-acme:\n%s", body)
			}
		}
		if strings.HasSuffix(path, "/ingress.yaml") && strings.Contains(body, "namespace: tenant-acme") {
			t.Errorf("STRAY: ingress in tenant-acme not <slug>:\n%s", body)
		}
		if strings.HasSuffix(path, "/provisioning-rbac.yaml") && strings.Contains(body, "namespace: tenant-acme") {
			t.Errorf("STRAY: rbac in tenant-acme not <slug>:\n%s", body)
		}
	}
	// Affirmative: the apps tree + apps-sync + rbac + ingress are present.
	want := []string{
		"clusters/sov/tenants/acme/apps-sync.yaml",
		"clusters/sov/tenants/acme/provisioning-rbac.yaml",
		"clusters/sov/tenants/acme/apps/namespace.yaml",
		"clusters/sov/tenants/acme/apps/app-wordpress.yaml",
		"clusters/sov/tenants/acme/kustomization.yaml",
	}
	for _, w := range want {
		if _, ok := out[w]; !ok {
			t.Errorf("MISSING expected funnel file: %s", w)
		}
	}
	// Negative: no host namespace.yaml / vcluster.yaml keys at all.
	for _, bad := range []string{
		"clusters/sov/tenants/acme/namespace.yaml",
		"clusters/sov/tenants/acme/vcluster.yaml",
	} {
		if _, ok := out[bad]; ok {
			t.Errorf("STRAY present: %s", bad)
		}
	}
}
