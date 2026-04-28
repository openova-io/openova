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

	// Must bind the sme/provisioning SA, not some ambient default.
	if !strings.Contains(got, "name: provisioning") || !strings.Contains(got, "namespace: sme") {
		t.Fatalf("expected SA binding to sme/provisioning, got: %s", got)
	}

	// Must NOT grant write on secrets in-tenant — only read. Writing tenant
	// secrets via the provisioning SA was never needed and only expanded
	// blast radius.
	if strings.Contains(got, `resources: ["secrets"]`) {
		// Allowed as long as verbs are read-only.
		lines := strings.Split(got, "\n")
		for i, line := range lines {
			if strings.Contains(line, `resources: ["secrets"]`) && i+1 < len(lines) {
				verbs := lines[i+1]
				for _, v := range []string{"create", "update", "patch", "delete"} {
					if strings.Contains(verbs, `"`+v+`"`) {
						t.Fatalf("secrets rule must be read-only, got verb %q in: %s", v, verbs)
					}
				}
			}
		}
	}
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
