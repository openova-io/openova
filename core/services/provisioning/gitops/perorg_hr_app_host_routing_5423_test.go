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
// files into `vcluster/apps/` for EVERY plan. The org-controller attaches
// `kubeConfig.secretRef: tenant-<slug>-kubeconfig` to the apps Kustomization,
// so the doc is applied INTO the Org vcluster — which registers no
// `helm.toolkit.fluxcd.io` CRDs at all. Flux aborts the WHOLE Kustomization on
// one dry-run failure, so the two poison docs took `app-wordpress.yaml` and
// `db-mysql.yaml` down with them. (At the time only paid plans were
// vCluster-backed; since #4292 every Organization is, so the host-apps
// re-root applies to every plan — the second subtest pins s/free/"".)
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

	t.Run("plan m routes HR apps to host-apps", func(t *testing.T) {
		g := NewManifestGenerator("clusters/sov/org-tenants")
		files, appDocs := g.GeneratePerOrgAppsTree("acmex", "m", cart, "pw123")

		for path, content := range files {
			if !strings.HasPrefix(path, PerOrgAppsDir+"/") {
				continue
			}
			for _, k := range fluxOnlyKinds {
				if strings.Contains(content, "\nkind: "+k) || strings.HasPrefix(content, "kind: "+k) {
					t.Errorf("%s carries kind %s — the apps Kustomization is kubeConfig-targeted at the Org vcluster, which has no Flux CRDs; the whole Kustomization fails dry-run and every sibling app dies with it (#5423)", path, k)
				}
			}
		}

		for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			if _, ok := files[PerOrgHostAppsDir+"/"+want]; !ok {
				t.Errorf("expected %s under %s (kubeConfig: null, targetNamespace: <slug> — the host ns these HRs always meant to install into)", want, PerOrgHostAppsDir)
			}
			if _, ok := files[PerOrgAppsDir+"/"+want]; ok {
				t.Errorf("%s must NOT remain under %s", want, PerOrgAppsDir)
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

		// The invariant is that the index equals the file set — so DERIVE the
		// expectation from the files actually rendered into host-apps instead
		// of restating a hand-written list. A hardcoded `want` only ever tested
		// two lists against each other: it went red when the openclaw⇒newapi
		// dependency closure (UAT row 225) legitimately added a third doc to
		// BOTH sides, which is the drift this guard exists to permit.
		hostDocs := PerOrgHostHelmReleaseAppDocs(cart)
		want := []string{}
		for path := range files {
			if name, ok := strings.CutPrefix(path, PerOrgHostAppsDir+"/"); ok {
				want = append(want, name)
			}
		}
		sort.Strings(hostDocs)
		sort.Strings(want)
		if len(want) == 0 {
			t.Fatalf("control failed: no files rendered under %s, so the index comparison below would pass on nothing", PerOrgHostAppsDir)
		}
		if strings.Join(hostDocs, ",") != strings.Join(want, ",") {
			t.Errorf("PerOrgHostHelmReleaseAppDocs = %v, but host-apps holds %v — the file set and the index set must not drift", hostDocs, want)
		}
	})

	// s/free/"" used to keep the HR docs in vcluster/apps/ because those Orgs
	// had no vcluster. Every Organization is vCluster-backed now (#4292), so
	// they re-root exactly like m — pin it so the old arm cannot creep back.
	t.Run("plans s/free/empty re-root HR apps exactly like m", func(t *testing.T) {
		for _, plan := range []string{"s", "free", ""} {
			g := NewManifestGenerator("clusters/sov/org-tenants")
			files, appDocs := g.GeneratePerOrgAppsTree("acmex", plan, cart, "pw123")

			for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
				if _, ok := files[PerOrgHostAppsDir+"/"+want]; !ok {
					t.Errorf("plan=%q: %s must be re-rooted under %s (the apps Kustomization is kubeConfig-targeted at the CRD-less vcluster for every Organization)", plan, want, PerOrgHostAppsDir)
				}
				if _, ok := files[PerOrgAppsDir+"/"+want]; ok {
					t.Errorf("plan=%q: %s must NOT remain under %s", plan, want, PerOrgAppsDir)
				}
				if contains(appDocs, want) {
					t.Errorf("plan=%q: appDocs still lists %s; its file lives in %s, so the apps kustomization would reference a missing file", plan, want, PerOrgHostAppsDir)
				}
			}
			for _, want := range []string{"app-wordpress.yaml", "db-mysql.yaml"} {
				if _, ok := files[PerOrgAppsDir+"/"+want]; !ok {
					t.Errorf("plan=%q: expected %s to stay under %s", plan, want, PerOrgAppsDir)
				}
			}
		}
		hostDocs := PerOrgHostHelmReleaseAppDocs(cart)
		for _, want := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			if !contains(hostDocs, want) {
				t.Errorf("PerOrgHostHelmReleaseAppDocs must index %s for every Organization, got %v", want, hostDocs)
			}
		}
	})
}

// A per-Org repo written by a pre-#5423 build — or by the former s/free arm,
// which kept HR docs in vcluster/apps/ — still lists app-<x>.yaml in
// vcluster/apps/kustomization.yaml while the file now lives in host-apps. The
// merge must strip that entry for every Organization so the next cart install
// heals an Org that would otherwise stay wedged forever — the same self-heal
// #4567 applies to a stale ciliumnetworkpolicy.yaml entry.
func TestMergePerOrgAppsKustomization_5423_StripsStaleHRAppEntry(t *testing.T) {
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

	got := MergePerOrgAppsKustomization(stale, nil, []string{"app-wordpress.yaml", "db-mysql.yaml"})
	for _, gone := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		if strings.Contains(got, gone) {
			t.Errorf("stale %s survived the merge — the Org stays wedged on a kustomization entry whose file is not in the dir:\n%s", gone, got)
		}
	}
	for _, keep := range []string{"app-wordpress.yaml", "db-mysql.yaml", "networkpolicy.yaml", "namespace.yaml"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s must survive the merge:\n%s", keep, got)
		}
	}

	// The index shape the former s/free arm wrote — HR docs indexed in
	// vcluster/apps/ with no namespace.yaml — is healed by the same call: the
	// stale HR entries go, the vcluster target-ns baseline comes in.
	oldSmallPlan := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - networkpolicy.yaml
  - app-openclaw.yaml
  - app-stalwart-mail.yaml
  - app-wordpress.yaml
`
	healed := MergePerOrgAppsKustomization(oldSmallPlan, nil, []string{"app-wordpress.yaml"})
	for _, gone := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		if strings.Contains(healed, gone) {
			t.Errorf("former small-plan index: stale %s survived — every Organization is vCluster-backed, so its file is in host-apps:\n%s", gone, healed)
		}
	}
	if !strings.Contains(healed, "- namespace.yaml") {
		t.Errorf("former small-plan index: the merge must add the vcluster target-ns namespace.yaml:\n%s", healed)
	}
}
