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

// TestGeneratePerOrgAppsTree_NoDuplicateAppsSyncKustomization is the #4758
// regression guard: on a Sovereign (PerOrgGitops) the org-controller's
// `catalyst-tenant-<slug>-apps` Flux Kustomization is the SINGLE authoritative
// reconciler of the per-Org apps tree. The legacy contabo-model funnel
// scaffolding emits a SECOND `tenant-<slug>-apps` Kustomization
// (generateAppsSyncKustomization → apps-sync.yaml) that, once committed to a
// Sovereign, becomes a broken DUPLICATE — historically it stuck FALSE on a
// hardcoded `sourceRef.name: flux-system` (finding #1 on hw223), and even with
// the sourceRef repointed it double-reconciles the SAME WordPress Deployment as
// the org-controller's Kustomization (WordPress double-reconcile churn, #4827).
//
// GeneratePerOrgAppsTree is the pure-function seam that re-roots ONLY the app
// payload into `vcluster/apps/` and DROPS the contabo host scaffolding. This
// test locks that contract by NAME so the duplicate can never be re-minted: the
// per-Org tree must carry NO apps-sync.yaml, NO `tenant-<slug>-apps`
// Kustomization, and NO `sourceRef` at all (the org-controller owns the
// GitRepository + Kustomization wiring).
func TestGeneratePerOrgAppsTree_NoDuplicateAppsSyncKustomization(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			files, _ := g.GeneratePerOrgAppsTree("g6wpwalk", plan, []string{"wordpress"}, "pw123")

			for path, content := range files {
				// The contabo apps-sync scaffolding must be dropped entirely.
				if strings.HasSuffix(path, "apps-sync.yaml") {
					t.Errorf("per-Org tree leaked the legacy apps-sync scaffolding %q — the org-controller's catalyst-tenant-<slug>-apps is authoritative (#4758):\n%s", path, content)
				}
				// No rendered doc may BE the broken duplicate Kustomization: a
				// `kind: Kustomization` named `tenant-<slug>-apps`.
				if strings.Contains(content, "kind: Kustomization") &&
					strings.Contains(content, "name: tenant-g6wpwalk-apps") {
					t.Errorf("per-Org tree emitted the duplicate tenant-<slug>-apps Kustomization at %q (#4758 finding #1):\n%s", path, content)
				}
				// The org-controller owns the GitRepository/sourceRef wiring — the
				// funnel payload must carry no sourceRef (and never the historically
				// broken flux-system one).
				if strings.Contains(content, "sourceRef:") {
					t.Errorf("per-Org tree payload %q carries a sourceRef — the org-controller owns the GitRepository binding, the funnel payload must not (#4758):\n%s", path, content)
				}
			}
		})
	}
}

// TestMergePerOrgAppsKustomization preserves the org-controller's NP baseline
// AND adds the funnel's app docs, deterministically + idempotently — and
// #4567 force-EXCLUDES ciliumnetworkpolicy.yaml (which lives in host-apps/, not
// apps/, so listing it wedges the kustomize build → app never deploys).
func TestMergePerOrgAppsKustomization(t *testing.T) {
	// A stale/buggy repo that already listed the CNP in the apps tree — the
	// merge must SELF-HEAL by dropping it (#4567).
	existing := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - networkpolicy.yaml
  - ciliumnetworkpolicy.yaml
`
	out := MergePerOrgAppsKustomization(existing, "s", nil, []string{"app-wordpress.yaml", "db-mysql.yaml"})

	for _, want := range []string{"networkpolicy.yaml", "app-wordpress.yaml", "db-mysql.yaml"} {
		if !strings.Contains(out, "- "+want) {
			t.Errorf("merged kustomization missing %q:\n%s", want, out)
		}
	}
	// #4567: the CNP must NOT survive into the apps tree, even when a prior bad
	// commit listed it.
	if strings.Contains(out, "- ciliumnetworkpolicy.yaml") {
		t.Errorf("ciliumnetworkpolicy.yaml leaked into vcluster/apps/kustomization.yaml — it lives in host-apps/, so this breaks the kustomize build:\n%s", out)
	}
	// Host tier (plan s): namespace.yaml does not exist in the tree — indexing
	// it would be a #4567-class "no such file" kustomize build failure.
	if strings.Contains(out, "- namespace.yaml") {
		t.Errorf("namespace.yaml must NOT be indexed for a host-tier plan (file doesn't exist there):\n%s", out)
	}

	// Idempotent: merging the same docs into the produced output is byte-stable.
	out2 := MergePerOrgAppsKustomization(out, "s", nil, []string{"app-wordpress.yaml", "db-mysql.yaml"})
	if out != out2 {
		t.Errorf("MergePerOrgAppsKustomization not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}

	// Empty existing seeds the baseline from scratch — networkpolicy.yaml only.
	fresh := MergePerOrgAppsKustomization("", "s", nil, []string{"app-wordpress.yaml"})
	if !strings.Contains(fresh, "- networkpolicy.yaml") {
		t.Errorf("empty-existing merge must seed the networkpolicy baseline:\n%s", fresh)
	}
	if strings.Contains(fresh, "- ciliumnetworkpolicy.yaml") {
		t.Errorf("empty-existing merge must NOT seed the CNP into the apps tree (#4567):\n%s", fresh)
	}
	if !strings.Contains(fresh, "- app-wordpress.yaml") {
		t.Errorf("empty-existing merge must include the new app doc:\n%s", fresh)
	}
}

// TestMergePerOrgAppsKustomization_VclusterNamespace_5104 locks the exact hw255
// regression class: for a VCLUSTER-tier plan the merged apps index MUST carry
// the #4992 vcluster target-ns `namespace.yaml`, even when the existing
// (funnel-first) index does not list it — otherwise the org-controller keeps
// committing the file while nothing ever applies it, the target namespace never
// exists inside the vcluster, and the apps Flux Kustomization wedges
// permanently on `namespaces "<slug>" not found` (purchased app never deploys).
func TestMergePerOrgAppsKustomization_VclusterNamespace_5104(t *testing.T) {
	// The exact index shape found on hw255 (acme255 / acme-255b): app docs +
	// NP baseline, namespace.yaml file committed to the tree but NOT indexed.
	existing := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-umami.yaml
  - db-postgres.yaml
  - networkpolicy.yaml
`
	out := MergePerOrgAppsKustomization(existing, "m", nil, []string{"app-umami.yaml", "db-postgres.yaml"})
	for _, want := range []string{"namespace.yaml", "networkpolicy.yaml", "app-umami.yaml", "db-postgres.yaml"} {
		if !strings.Contains(out, "- "+want) {
			t.Errorf("vcluster-tier merged apps index missing %q (the #5104 wedge):\n%s", want, out)
		}
	}

	// Uninstall shape (existing dropped, only surviving docs merged): the
	// plan-aware baseline must STILL carry namespace.yaml — a plain
	// baseline+survivors rebuild used to drop it.
	uninstall := MergePerOrgAppsKustomization("", "m", nil, []string{"app-umami.yaml"})
	if !strings.Contains(uninstall, "- namespace.yaml") {
		t.Errorf("uninstall rebuild dropped namespace.yaml for a vcluster-tier plan:\n%s", uninstall)
	}

	// Every vcluster-tier plan slug carries the target-ns; host-tier plans must not.
	for plan, want := range map[string]bool{"m": true, "l": true, "xl": true, "flexi": true, "s": false, "free": false, "": false} {
		got := contains(PerOrgAppsBaselineDocs(plan), "namespace.yaml")
		if got != want {
			t.Errorf("PerOrgAppsBaselineDocs(%q) namespace.yaml = %v, want %v", plan, got, want)
		}
	}
}

// TestMergePerOrgAppsKustomization_TreeDerivedBaseline_5104 locks the
// structural half of #5104: baseline docs are derived from the files ACTUALLY
// committed in the tree, so a future org-controller boundary doc survives the
// funnel merge without a funnel code change — while funnel-owned docs
// (app-*/db-*, committed AND pruned by this very commit), the index itself,
// and #4567-excluded docs are never derived from the tree.
func TestMergePerOrgAppsKustomization_TreeDerivedBaseline_5104(t *testing.T) {
	treeDocs := []string{
		"namespace.yaml",           // #4992 baseline — must be indexed
		"networkpolicy.yaml",       // baseline — must be indexed
		"quota-override.yaml",      // hypothetical FUTURE baseline doc — must be indexed
		"ciliumnetworkpolicy.yaml", // #4567 — lives in host-apps/, must NOT be indexed
		"kustomization.yaml",       // the index itself — never a resource
		"app-stale.yaml",           // funnel-owned — pruned by this commit, must NOT be derived
		"db-old.yaml",              // funnel-owned — pruned by this commit, must NOT be derived
		"README.md",                // not a manifest
	}
	out := MergePerOrgAppsKustomization("", "m", treeDocs, []string{"app-wordpress.yaml"})

	for _, want := range []string{"namespace.yaml", "networkpolicy.yaml", "quota-override.yaml", "app-wordpress.yaml"} {
		if !strings.Contains(out, "- "+want) {
			t.Errorf("tree-derived merge missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"- ciliumnetworkpolicy.yaml", "- kustomization.yaml", "- app-stale.yaml", "- db-old.yaml", "- README.md"} {
		if strings.Contains(out, bad) {
			t.Errorf("tree-derived merge must not index %q:\n%s", bad, out)
		}
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
