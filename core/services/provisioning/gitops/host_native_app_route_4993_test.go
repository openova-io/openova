package gitops

import (
	"strings"
	"testing"
)

// #4993 — the vcluster-tier durable fix. The app's own HTTPRoute is co-located
// INSIDE the Org vcluster (apps tree, kubeConfig-targeted), but loft vcluster
// 0.33.4 registers NO httproute reflecting controller, so it never reaches the
// host Cilium Gateway → the app 404s with pods Running. GeneratePerOrgHostAppRoutes
// emits a HOST-NATIVE HTTPRoute into vcluster/host-apps/ that binds the SYNCED
// Service (<app>-x-<slug>-x-vcluster) on the host <slug> ns — the same
// host-native model the per-Org console route already uses.

// TestGeneratePerOrgHostAppRoutes_VclusterTier_SyncedBackend proves the
// host-native route lands with the synced Service backend, the <app>.<slug>.<pool>
// host, and the console-Gateway parentRef.
func TestGeneratePerOrgHostAppRoutes_VclusterTier_SyncedBackend(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	files, docs := g.GeneratePerOrgHostAppRoutes("walk-stranger-two", "m", []string{"wordpress"})

	const routePath = "vcluster/host-apps/app-wordpress-hostroute.yaml"
	doc, ok := files[routePath]
	if !ok {
		t.Fatalf("host-native route not emitted at %q (keys: %v)", routePath, keys(files))
	}
	if len(docs) != 1 || docs[0] != "app-wordpress-hostroute.yaml" {
		t.Fatalf("hostAppDocs = %v, want [app-wordpress-hostroute.yaml]", docs)
	}

	checks := map[string]string{
		"gateway apiVersion":       "apiVersion: gateway.networking.k8s.io/v1",
		"kind":                     "kind: HTTPRoute",
		"host <app>.<slug>.<pool>": "- wordpress.walk-stranger-two.omani.homes",
		"lands in host <slug> ns":  "namespace: walk-stranger-two",
		"console Gateway parent":   "name: cilium-gateway-console",
		"kube-system parent ns":    "namespace: kube-system",
		// The CRUX: backend is the SYNCED service name, NOT the plain <app>. A
		// plain `wordpress` backend does not exist on the host → ResolvedRefs=False
		// → 404 (that is exactly what the in-vcluster route would produce if it
		// ever synced). The synced name is what the syncer actually reflects.
		"synced backend service": "- name: wordpress-x-walk-stranger-two-x-vcluster",
	}
	for label, want := range checks {
		if !strings.Contains(doc, want) {
			t.Errorf("host-native route missing %s (%q):\n%s", label, want, doc)
		}
	}
	// Must NOT bind the plain Service name (that is the in-vcluster route's bug).
	if strings.Contains(doc, "- name: wordpress\n") {
		t.Errorf("host-native route bound the PLAIN Service name (404 on host) instead of the synced name:\n%s", doc)
	}
}

// TestGeneratePerOrgHostAppRoutes_HonorsPerOrgPoolZone is the #4999 apps-side
// proof: when a 2nd Org's honored pool zone is `omani.rest`, the per-Org app host
// MUST render under that SAME zone — `<app>.<slug>.omani.rest` — not the
// Sovereign's primary apps pool. consumer.go achieves this by rendering on a
// scoped generator clone whose ParentDomain is the honored zone; this test
// exercises that mechanism directly (a generator pinned to omani.rest), so the
// app host stays in lockstep with the per-Org console DNS/TLS/route zone (no
// stale-apex-wildcard dead IP — the #4421 invariant, upheld under the pick).
func TestGeneratePerOrgHostAppRoutes_HonorsPerOrgPoolZone(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	// The Sovereign's PRIMARY apps pool is omani.homes, but THIS Org chose
	// omani.rest — consumer.go clones the generator with ParentDomain=omani.rest.
	g.ParentDomain = "omani.rest"

	files, _ := g.GeneratePerOrgHostAppRoutes("walk-stranger-two", "m", []string{"wordpress"})
	doc, ok := files["vcluster/host-apps/app-wordpress-hostroute.yaml"]
	if !ok {
		t.Fatalf("host-native route not emitted (keys: %v)", keys(files))
	}
	if want := "- wordpress.walk-stranger-two.omani.rest"; !strings.Contains(doc, want) {
		t.Errorf("app host must render under the honored pool zone %q:\n%s", want, doc)
	}
	// Regression guard: it must NOT collapse back to the primary apps pool.
	if strings.Contains(doc, "wordpress.walk-stranger-two.omani.homes") {
		t.Errorf("app host regressed to the primary apps pool (omani.homes) instead of the honored omani.rest:\n%s", doc)
	}
}

// TestGeneratePerOrgHostAppRoutes_HostTier_NoRoute — free/S host-tier Orgs run the
// app + its plain Service directly in the host <slug> ns, where the co-located
// generateAppHTTPRoute already routes and no synced-service name exists. So NO
// host-native route is emitted (it would reference a non-existent synced Service).
func TestGeneratePerOrgHostAppRoutes_HostTier_NoRoute(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	for _, plan := range []string{"s", "free", ""} {
		files, docs := g.GeneratePerOrgHostAppRoutes("hostorg", plan, []string{"wordpress"})
		if len(files) != 0 || len(docs) != 0 {
			t.Errorf("plan=%q (host tier) must emit NO host-native route, got files=%v docs=%v", plan, keys(files), docs)
		}
	}
}

// TestGeneratePerOrgHostAppRoutes_SkipsHelmReleaseAndDB — HelmRelease-shaped apps
// (openclaw / stalwart-mail) carry their own chart HTTPRoute and sync no
// <app>-x-…-x-vcluster Service; shareable DB slugs are never externally routed.
// Both must be skipped so no route points at a non-existent Service.
func TestGeneratePerOrgHostAppRoutes_SkipsHelmReleaseAndDB(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	// wordpress (Deployment) + mysql (DB) — mysql must be skipped.
	files, docs := g.GeneratePerOrgHostAppRoutes("dbmix", "m", []string{"wordpress", "mysql"})
	if _, bad := files["vcluster/host-apps/app-mysql-hostroute.yaml"]; bad {
		t.Errorf("DB slug mysql must NOT get a host-native route:\n%v", keys(files))
	}
	if len(docs) != 1 || docs[0] != "app-wordpress-hostroute.yaml" {
		t.Errorf("only wordpress should be routed, got docs=%v", docs)
	}

	// A cart of ONLY HelmRelease apps (openclaw) yields no host-native routes.
	if isHelmReleaseApp("openclaw") {
		hrFiles, hrDocs := g.GeneratePerOrgHostAppRoutes("hrorg", "m", []string{"openclaw"})
		if len(hrFiles) != 0 || len(hrDocs) != 0 {
			t.Errorf("HelmRelease-only cart must emit no host-native route, got files=%v docs=%v", keys(hrFiles), hrDocs)
		}
	}
}

// TestMergePerOrgHostAppsKustomization preserves the org-controller host-apps
// baseline (CNP + provisioning RBAC), is additive across cart installs, drops the
// removed app on rebuild, and is idempotent + byte-stable.
func TestMergePerOrgHostAppsKustomization(t *testing.T) {
	// Fresh seed from the org-controller baseline + one app route.
	fresh := MergePerOrgHostAppsKustomization("", nil, []string{"app-wordpress-hostroute.yaml"})
	for _, want := range []string{
		"ciliumnetworkpolicy.yaml",
		"provisioning-rbac.yaml",
		"app-wordpress-hostroute.yaml",
	} {
		if !strings.Contains(fresh, want) {
			t.Errorf("fresh merge missing %q:\n%s", want, fresh)
		}
	}

	// A second cart install (nextcloud) is additive; the baseline + prior app survive.
	second := MergePerOrgHostAppsKustomization(fresh, nil, []string{"app-wordpress-hostroute.yaml", "app-nextcloud-hostroute.yaml"})
	for _, want := range []string{
		"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml",
		"app-wordpress-hostroute.yaml", "app-nextcloud-hostroute.yaml",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("additive merge lost %q:\n%s", want, second)
		}
	}

	// Idempotent + byte-stable.
	again := MergePerOrgHostAppsKustomization(second, nil, []string{"app-wordpress-hostroute.yaml", "app-nextcloud-hostroute.yaml"})
	if again != second {
		t.Errorf("merge not idempotent:\n--- first ---\n%s\n--- second ---\n%s", second, again)
	}

	// Uninstall rebuild (existing="" + surviving docs only) drops the removed app
	// but ALWAYS keeps the baseline.
	rebuilt := MergePerOrgHostAppsKustomization("", nil, []string{"app-nextcloud-hostroute.yaml"})
	if strings.Contains(rebuilt, "app-wordpress-hostroute.yaml") {
		t.Errorf("uninstall rebuild kept the removed app route:\n%s", rebuilt)
	}
	for _, want := range []string{"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml", "app-nextcloud-hostroute.yaml"} {
		if !strings.Contains(rebuilt, want) {
			t.Errorf("uninstall rebuild dropped %q:\n%s", want, rebuilt)
		}
	}

	// #5104 structural completeness: a FUTURE org-controller baseline doc
	// actually committed under vcluster/host-apps/ survives the merge via the
	// tree listing, while funnel-owned route docs (pruned by this very commit)
	// and the index itself are never derived from the tree.
	treeDocs := []string{"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml", "gateway-policy.yaml", "app-stale-hostroute.yaml", "kustomization.yaml"}
	derived := MergePerOrgHostAppsKustomization("", treeDocs, []string{"app-wordpress-hostroute.yaml"})
	if !strings.Contains(derived, "- gateway-policy.yaml") {
		t.Errorf("tree-derived merge dropped a future baseline doc (the #5104 orphan class):\n%s", derived)
	}
	for _, bad := range []string{"- app-stale-hostroute.yaml", "- kustomization.yaml"} {
		if strings.Contains(derived, bad) {
			t.Errorf("tree-derived merge must not index %q:\n%s", bad, derived)
		}
	}
}
