package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPlatformScope_WriteRenderForAdmissionPreflight writes the exact objects
// this renderer authors for one Organization (the PriorityClass, the Kyverno
// Policy, both quotas, the read RBAC) into $ORG_PLATFORM_SCOPE_RENDER_DIR,
// for the real-apiserver preflight
// (.github/workflows/preflight-org-platform-scope.yaml,
// tests/e2e/org-platform-scope/run.sh) to apply to a kind cluster running
// Kyverno. Skipped when the variable is unset, so `go test ./...` stays
// hermetic. The preflight applies these bytes — never a hand-copied policy.
func TestPlatformScope_WriteRenderForAdmissionPreflight(t *testing.T) {
	dir := os.Getenv("ORG_PLATFORM_SCOPE_RENDER_DIR")
	if dir == "" {
		t.Skip("ORG_PLATFORM_SCOPE_RENDER_DIR unset — only the admission preflight writes the render")
	}
	for _, c := range []struct {
		sub     string
		pending bool
	}{{"scoped", false}, {"pending", true}} {
		out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: "m",
			SovereignFQDN: "x.example", HostCluster: "kind", VClusterChartVersion: "0.33.*",
			PlatformScopePending: c.pending})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, c.sub), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"namespace.yaml", priorityClassDoc, platformScopePolicyDoc, platformQuotaDoc, "resourcequota.yaml", platformScopeRBACDoc} {
			b, ok := out["vcluster/"+f]
			if !ok {
				t.Fatalf("render has no vcluster/%s", f)
			}
			if err := os.WriteFile(filepath.Join(dir, c.sub, f), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
