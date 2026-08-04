package gitops

import (
	"sort"
	"strings"
	"testing"
)

// #5423 — a funnel Organization on a VCLUSTER-tier plan (m/l/xl/flexi) whose
// cart contains a HelmRelease-shaped app (openclaw / stalwart-mail / newapi)
// never deployed ANY app in that cart, including the plain Deployment-shaped
// ones. Live on hw290 for 2/2 such Orgs (acme-corp, walk-two):
//
//	$ kubectl get kustomization -n flux-system catalyst-tenant-acme-corp-apps
//	READY: False  ReconciliationFailed
//	MSG:   HelmRelease/acme-corp/bp-openclaw dry-run failed:
//	       no matches for kind "HelmRelease" in version "helm.toolkit.fluxcd.io/v2"
//	inventory: 0
//
// GeneratePerOrgAppsTree re-rooted the host-scoped `app-<x>.yaml` HelmRelease
// files into `vcluster/apps/` for EVERY plan. On the host tier that is correct
// (the apps Kustomization has `kubeConfig: null` → applies to the host, where
// the Flux CRDs live). On the vcluster tier the org-controller attaches
// `kubeConfig.secretRef: tenant-<slug>-kubeconfig`, so the same doc is applied
// INTO the Org vcluster — which registers no `helm.toolkit.fluxcd.io` CRDs at
// all. Flux aborts the WHOLE Kustomization on one dry-run failure, so the two
// poison docs took `app-wordpress.yaml` and `db-mysql.yaml` down with them.
//
// Downstream that was UAT rows 86 (timeline RED), 90 (wordpress → HTTP 500 via
// `ResolvedRefs=False / BackendNotFound`), 233 (HTTPRoute present but nothing
// serves — routes live in host-apps and applied fine, only the backends were
// blocked) and 234 (stalwart-mail carted, no HelmRelease ever created).
func TestPerOrgAppsTree_5423_VclusterTierKeepsFluxDocsOutOfTheKubeconfigTargetedTree(t *testing.T) {
	cart := []string{"wordpress", "stalwart-mail", "openclaw"}

	// The invariant that actually matters: nothing under vcluster/apps/ may
	// carry a kind the Org vcluster cannot serve. Asserting on the KIND rather
	// than the filename is what makes this a real guard — a future HR-shaped
	// catalog app is caught even if it is named nothing like app-openclaw.yaml.
	fluxOnlyKinds := []string{"HelmRelease", "HelmRepository", "OCIRepository", "GitRepository", "Kustomization"}

	t.Run("vcluster tier routes HR apps to host-apps", func(t *testing.T) {
		g := NewManifestGenerator("clusters/sov/org-tenants")
		files, appDocs := g.GeneratePerOrgAppsTree("acmex", "m", cart, "pw123")

		for path, content := range files {
			if !strings.HasPrefix(path, PerOrgAppsDir+"/") {
				continue
			}
			for _, k := range fluxOnlyKinds {
				if strings.Contains(content, "\nkind: "+k) || strings.HasPrefix(content, "kind: "+k) {
					t.Errorf("%s carries kind %s — the vcluster-tier apps Kustomization is kubeConfig-targeted at the Org vcluster, which has no Flux CRDs; the whole Kustomization fails dry-run and every sibling app dies with it (#5423)", path, k)
				}
			}
		}

		for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			if _, ok := files[PerOrgHostAppsDir+"/"+want]; !ok {
				t.Errorf("expected %s under %s (kubeConfig: null, targetNamespace: <slug> — the host ns these HRs always meant to install into)", want, PerOrgHostAppsDir)
			}
			if _, ok := files[PerOrgAppsDir+"/"+want]; ok {
				t.Errorf("%s must NOT remain under %s on the vcluster tier", want, PerOrgAppsDir)
			}
		}

		// The customer's actual purchase must still be in the vcluster tree —
		// this is the payload that was collateral damage.
		for _, want := range []string{"app-wordpress.yaml", "db-mysql.yaml"} {
			if _, ok := files[PerOrgAppsDir+"/"+want]; !ok {
				t.Errorf("expected %s to stay under %s", want, PerOrgAppsDir)
			}
		}

		// appDocs indexes vcluster/apps/kustomization.yaml. Listing a file that
		// is no longer in that dir breaks the kustomize build outright — the
		// #4567 failure mode.
		for _, d := range appDocs {
			if d == "app-openclaw.yaml" || d == "app-stalwart-mail.yaml" {
				t.Errorf("appDocs still lists %s; its file moved to %s, so the apps kustomization would reference a missing file", d, PerOrgHostAppsDir)
			}
		}

		hostDocs := PerOrgHostHelmReleaseAppDocs("m", cart)
		want := []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"}
		sort.Strings(hostDocs)
		if strings.Join(hostDocs, ",") != strings.Join(want, ",") {
			t.Errorf("PerOrgHostHelmReleaseAppDocs = %v, want %v — the file set and the index set must not drift", hostDocs, want)
		}
	})

	// The host tier is the path that always worked; a regression here would
	// break every free/S Org, so pin it explicitly.
	t.Run("host tier is unchanged", func(t *testing.T) {
		g := NewManifestGenerator("clusters/sov/org-tenants")
		files, appDocs := g.GeneratePerOrgAppsTree("acmex", "s", cart, "pw123")

		for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			if _, ok := files[PerOrgAppsDir+"/"+want]; !ok {
				t.Errorf("host tier: %s must stay under %s (that Kustomization applies to the host, where the Flux CRDs live)", want, PerOrgAppsDir)
			}
			if _, ok := files[PerOrgHostAppsDir+"/"+want]; ok {
				t.Errorf("host tier: %s must NOT move to %s", want, PerOrgHostAppsDir)
			}
		}
		idx := strings.Join(appDocs, ",")
		for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			if !strings.Contains(idx, want) {
				t.Errorf("host tier: appDocs must still index %s, got %v", want, appDocs)
			}
		}
		if got := PerOrgHostHelmReleaseAppDocs("s", cart); got != nil {
			t.Errorf("host tier: PerOrgHostHelmReleaseAppDocs must be nil, got %v", got)
		}
	})
}

// A per-Org repo written by a pre-#5423 build still lists app-<x>.yaml in
// vcluster/apps/kustomization.yaml while the file now lives in host-apps. The
// merge must strip that entry on the vcluster tier so the next cart install
// heals an Org that would otherwise stay wedged forever — the same self-heal
// #4567 applies to a stale ciliumnetworkpolicy.yaml entry.
func TestMergePerOrgAppsKustomization_5423_StripsStaleHRAppEntryOnVclusterTier(t *testing.T) {
	stale := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - networkpolicy.yaml
  - app-openclaw.yaml
  - app-stalwart-mail.yaml
  - app-wordpress.yaml
  - db-mysql.yaml
`

	got := MergePerOrgAppsKustomization(stale, "m", nil, []string{"app-wordpress.yaml", "db-mysql.yaml"})
	for _, gone := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		if strings.Contains(got, gone) {
			t.Errorf("vcluster tier: stale %s survived the merge — the Org stays wedged on a kustomization entry whose file is not in the dir:\n%s", gone, got)
		}
	}
	for _, keep := range []string{"app-wordpress.yaml", "db-mysql.yaml", "networkpolicy.yaml", "namespace.yaml"} {
		if !strings.Contains(got, keep) {
			t.Errorf("vcluster tier: %s must survive the merge:\n%s", keep, got)
		}
	}

	// Host tier: the very same entries are legitimate and must be preserved.
	gotHost := MergePerOrgAppsKustomization(stale, "s", nil, []string{"app-wordpress.yaml", "db-mysql.yaml"})
	for _, keep := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		if !strings.Contains(gotHost, keep) {
			t.Errorf("host tier: %s must be preserved — its file really is in vcluster/apps/:\n%s", keep, gotHost)
		}
	}
}
