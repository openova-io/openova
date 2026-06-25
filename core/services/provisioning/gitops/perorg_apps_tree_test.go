package gitops

import (
	"strings"
	"testing"
)

// TestGeneratePerOrgAppsTree_WordpressCart is the #4384 render-proof: a funnel
// wordpress cart Org re-roots its app payload under `vcluster/apps/` (the
// per-Org `<slug>/catalyst-tenant` tree the org-controller's apps Kustomization
// reconciles), NOT under the legacy contabo `clusters/.../<slug>/apps/` tree
// that lands in the global openova/openova repo.
func TestGeneratePerOrgAppsTree_WordpressCart(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			files, docs := g.GeneratePerOrgAppsTree("g6wpwalk", plan, []string{"wordpress"}, "pw123")

			// The wordpress workload MUST land at vcluster/apps/app-wordpress.yaml.
			wp, ok := files["vcluster/apps/app-wordpress.yaml"]
			if !ok {
				t.Fatalf("wordpress HelmRelease NOT at vcluster/apps/app-wordpress.yaml (#4384) (keys: %v)", keys(files))
			}
			if !strings.Contains(wp, "wordpress") {
				t.Errorf("app-wordpress.yaml does not mention wordpress:\n%s", wp)
			}

			// Its DB Secret must travel with it under vcluster/apps/.
			if _, ok := files["vcluster/apps/db-mysql.yaml"]; !ok {
				t.Errorf("wordpress db Secret NOT at vcluster/apps/db-mysql.yaml (keys: %v)", keys(files))
			}

			// NOTHING may land under the legacy contabo tenant-dir tree — that
			// repo (openova/openova) is the wrong target for a Sovereign's
			// per-Org apps (#4384 root cause).
			for p := range files {
				if strings.HasPrefix(p, "clusters/") {
					t.Errorf("per-Org tree leaked a contabo-model path %q — must be vcluster/apps relative", p)
				}
				if !strings.HasPrefix(p, "vcluster/apps/") {
					t.Errorf("per-Org tree path %q is not under vcluster/apps/", p)
				}
			}

			// The org-controller OWNS the boundary ns + the apps kustomization;
			// the funnel must NOT re-emit them.
			if _, ok := files["vcluster/apps/namespace.yaml"]; ok {
				t.Errorf("funnel must NOT re-emit vcluster/apps/namespace.yaml (org-controller owns the boundary ns)")
			}

			// appDocs must enumerate the app files for the kustomization merge.
			if !contains(docs, "app-wordpress.yaml") {
				t.Errorf("appDocs missing app-wordpress.yaml: %v", docs)
			}
		})
	}
}

// TestMergePerOrgAppsKustomization preserves the org-controller's NP/CNP
// baseline AND adds the funnel's app docs, deterministically + idempotently.
func TestMergePerOrgAppsKustomization(t *testing.T) {
	existing := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - networkpolicy.yaml
  - ciliumnetworkpolicy.yaml
`
	out := MergePerOrgAppsKustomization(existing, []string{"app-wordpress.yaml", "db-mysql.yaml"})

	for _, want := range []string{"networkpolicy.yaml", "ciliumnetworkpolicy.yaml", "app-wordpress.yaml", "db-mysql.yaml"} {
		if !strings.Contains(out, "- "+want) {
			t.Errorf("merged kustomization missing %q:\n%s", want, out)
		}
	}

	// Idempotent: merging the same docs into the produced output is byte-stable.
	out2 := MergePerOrgAppsKustomization(out, []string{"app-wordpress.yaml", "db-mysql.yaml"})
	if out != out2 {
		t.Errorf("MergePerOrgAppsKustomization not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}

	// Empty existing seeds the baseline from scratch.
	fresh := MergePerOrgAppsKustomization("", []string{"app-wordpress.yaml"})
	if !strings.Contains(fresh, "- networkpolicy.yaml") || !strings.Contains(fresh, "- ciliumnetworkpolicy.yaml") {
		t.Errorf("empty-existing merge must seed the NP/CNP baseline:\n%s", fresh)
	}
	if !strings.Contains(fresh, "- app-wordpress.yaml") {
		t.Errorf("empty-existing merge must include the new app doc:\n%s", fresh)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
