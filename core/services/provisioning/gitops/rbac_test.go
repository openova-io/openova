package gitops

import (
	"strings"
	"testing"
)

func TestGenerateProvisioningTenantRBAC(t *testing.T) {
	got := generateProvisioningTenantRBAC("tenant-abc123")

	// Must be a Role, not a ClusterRole — the whole point of #75 is to
	// stop granting cluster-wide delete on Flux CRs.
	if !strings.Contains(got, "kind: Role\n") {
		t.Fatalf("expected namespaced Role, got: %s", got)
	}
	if strings.Contains(got, "kind: ClusterRole") {
		t.Fatalf("must NOT be ClusterRole, got: %s", got)
	}

	// Must be scoped to this specific tenant's namespace.
	if !strings.Contains(got, "namespace: tenant-abc123") {
		t.Fatalf("expected namespace: tenant-abc123, got: %s", got)
	}

	// Must grant patch+delete on HelmReleases AND Kustomizations so the
	// teardown finalizer strip works.
	if !strings.Contains(got, "helmreleases") {
		t.Fatalf("expected helmreleases rule, got: %s", got)
	}
	if !strings.Contains(got, "kustomizations") {
		t.Fatalf("expected kustomizations rule, got: %s", got)
	}

	// Must bind the org-services/provisioning SA, not some ambient default
	// (#3383: the mesh namespace was renamed `sme` → `org-services`).
	if !strings.Contains(got, "name: provisioning") || !strings.Contains(got, "namespace: org-services") {
		t.Fatalf("expected SA binding to org-services/provisioning, got: %s", got)
	}

	// The secrets rule is read only. The service writes no Secret through the
	// API: tenant-<slug>-kubeconfig has one producer, the Organization's
	// boundary tree (the vCluster writes it, bp-reflector copies it into
	// flux-system), and the service only waits until it is held.
	secretsVerbs := verbsForResource(got, `resources: ["secrets"]`)
	if want := `verbs: ["get", "list", "watch"]`; strings.TrimSpace(secretsVerbs) != want {
		t.Fatalf("secrets rule must be exactly %s (read only), got: %q", want, secretsVerbs)
	}

	// The vcluster-dns-kick (waitForVclusterDNSOrKick) deletes vcluster-0, so
	// the pods rule MUST grant delete. Without it `delete pods -n <slug>`
	// 403s and the DNS kick can't recover a stuck syncer.
	podsVerbs := verbsForResource(got, `resources: ["pods"]`)
	if podsVerbs == "" {
		t.Fatalf("expected a pods rule, got: %s", got)
	}
	if !strings.Contains(podsVerbs, `"delete"`) {
		t.Fatalf("pods rule must grant delete for the vcluster-dns-kick, got: %s", podsVerbs)
	}
}

// verbsForResource returns the `verbs:` line that immediately follows the
// first line matching `resourceLine` in the rendered RBAC, or "" if not found.
func verbsForResource(rendered, resourceLine string) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if strings.Contains(line, resourceLine) {
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], "verbs:") {
					return lines[j]
				}
			}
		}
	}
	return ""
}

func TestGenerateAllIncludesTenantRBAC(t *testing.T) {
	g := NewManifestGenerator("clusters/contabo-mkt/tenants")
	files := g.GenerateAll("abc123", "flexi", []string{})

	key := "clusters/contabo-mkt/tenants/abc123/provisioning-rbac.yaml"
	if _, ok := files[key]; !ok {
		t.Fatalf("expected %q in generated manifests, got keys: %v", key, keysOf(files))
	}

	// Parent kustomization must include it.
	parent, ok := files["clusters/contabo-mkt/tenants/abc123/kustomization.yaml"]
	if !ok {
		t.Fatalf("missing parent kustomization.yaml")
	}
	if !strings.Contains(parent, "provisioning-rbac.yaml") {
		t.Fatalf("parent kustomization does not list provisioning-rbac.yaml, got: %s", parent)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
