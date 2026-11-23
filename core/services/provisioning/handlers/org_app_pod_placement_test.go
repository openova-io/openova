package handlers

import "testing"

// orgAppPodMatches takes each Organization app pod in the shape its placement
// gives it on the host: the customer's apps synced out of the vCluster, the
// per-Organization platform stack (newapi, openclaw, agenity) native on the
// host `<slug>` namespace.
//
// Synced names below are VERBATIM from hw307-a namespace g01-w3ojsz0a
// (2026-11-22, read-only kubectl get pods). They are longer than 63
// characters inside the syncer, so the syncer cut and hashed them: no
// `-x-vcluster` tail, only the label says they are synced. No host install of
// the funnel's platform stack has run yet to copy a native name from: the
// native names are constructed from the charts' templates, and the labels are
// the ones those templates put on each pod (selectorLabels on the workloads,
// cnpg.io/cluster from the CNPG operator, job-name from the Job controller,
// component per-user-pod on openclaw's workspace pods).
func TestOrgAppPodMatches_PlacementShapes(t *testing.T) {
	synced := map[string]string{vclusterManagedByLabel: "vcluster"}
	workload := func(slug string, extra ...string) map[string]string {
		l := map[string]string{
			"app.kubernetes.io/name":     "bp-" + slug,
			"app.kubernetes.io/instance": slug,
		}
		for i := 0; i+1 < len(extra); i += 2 {
			l[extra[i]] = extra[i+1]
		}
		return l
	}
	cases := []struct {
		name    string
		podName string
		labels  map[string]string
		appSlug string
		want    bool
	}{
		// Platform stack: the app's own workload pods on the host are the app.
		{"openclaw controller on the host", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l", workload("openclaw", "catalyst.openova.io/component", "controller"), "openclaw", true},
		{"newapi Deployment pod on the host", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", workload("newapi"), "newapi", true},
		{"agenity StatefulSet pod on the host", "agenity-bp-agenity-0", workload("agenity"), "agenity", true},

		// Platform stack: other pods of the same release are not the app,
		// whatever their name starts with.
		{"newapi CNPG instance", "newapi-bp-newapi-newapi-pg-1", map[string]string{"cnpg.io/cluster": "newapi-bp-newapi-newapi-pg", "cnpg.io/instanceRole": "primary"}, "newapi", false},
		{"newapi CNPG instance carrying the chart labels", "newapi-bp-newapi-newapi-pg-1", workload("newapi", "cnpg.io/cluster", "newapi-bp-newapi-newapi-pg"), "newapi", false},
		{"newapi pre-install hook Job pod", "newapi-eswebhook-gate-7xk2p", map[string]string{"catalyst.openova.io/component": "external-secrets-webhook-gate", "job-name": "newapi-eswebhook-gate", "batch.kubernetes.io/job-name": "newapi-eswebhook-gate"}, "newapi", false},
		{"newapi admin-promote CronJob pod", "newapi-bp-newapi-admin-promote-29922434-l44wt", map[string]string{"app.kubernetes.io/name": "newapi-admin-promote", "app.kubernetes.io/instance": "newapi", "catalyst.openova.io/component": "admin-promote", "job-name": "newapi-bp-newapi-admin-promote-29922434"}, "newapi", false},
		{"Job pod carrying the workload labels", "newapi-bp-newapi-channel-seed-k2x4q", workload("newapi", "job-name", "newapi-bp-newapi-channel-seed"), "newapi", false},
		{"openclaw per-user workspace pod", "openclaw-user-1b6f0c2e-4d7a-4f3e-9a51-0c9e8d7b6a54", map[string]string{"app.kubernetes.io/name": "bp-openclaw", "app.kubernetes.io/component": "per-user-pod"}, "openclaw", false},
		{"agenity oidc-gate pod", "agenity-gate-6d9c8b7f5-q2w3e", map[string]string{"app.kubernetes.io/name": "bp-oidc-gate", "catalyst.openova.io/component": "agenity-gate"}, "agenity", false},
		{"unlabelled pod with the app's name prefix", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", nil, "newapi", false},
		{"another app's host pod", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", workload("newapi"), "openclaw", false},

		// Platform stack: a synced pod is a leftover of the old placement,
		// never the app, even when its cut name hides the syncer tail.
		{"live cut synced openclaw pod", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g-dbc85157d1", synced, "openclaw", false},
		{"live cut synced newapi CronJob pod", "newapi-bp-newapi-admin-promote-29922434-l44wt-x-g01--648fb43420", synced, "newapi", false},
		{"synced openclaw pod with the full tail", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g01-w3ojsz0a-x-vcluster", workload("openclaw"), "openclaw", false},

		// Customer apps: unchanged, the synced shape only (UAT row 86 names).
		{"live wordpress synced pod", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", synced, "wordpress", true},
		{"native wordpress pod is not the app", "wordpress-678cbb45dc-lv6hw", nil, "wordpress", false},
		{"stalwart-mail stays in the vCluster", "stalwart-mail-0-x-acme-x-vcluster", synced, "stalwart-mail", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgAppPodMatches(tc.podName, tc.labels, tc.appSlug); got != tc.want {
				t.Errorf("orgAppPodMatches(%q, %v, %q) = %v, want %v", tc.podName, tc.labels, tc.appSlug, got, tc.want)
			}
		})
	}
}

// The pod-truth reconciler gives a pod to a slug by name (podOwnerSlug) and
// then asks podTruthCounts whether it may count: a Ready CNPG, hook, CronJob
// or per-user pod of the platform stack must not re-green the app's step,
// while the app's own workload and every other app's pods still do.
func TestPodTruthCounts_PlatformStackNeedsItsOwnWorkload(t *testing.T) {
	wanted := map[string]bool{"newapi": true, "openclaw": true, "wordpress": true, "postgres": true}
	cases := []struct {
		name    string
		podName string
		labels  map[string]string
		want    string
	}{
		{"newapi Deployment pod", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", map[string]string{"app.kubernetes.io/name": "bp-newapi", "app.kubernetes.io/instance": "newapi"}, "newapi"},
		{"newapi CNPG instance", "newapi-bp-newapi-newapi-pg-1", map[string]string{"cnpg.io/cluster": "newapi-bp-newapi-newapi-pg"}, ""},
		{"newapi admin-promote CronJob pod", "newapi-bp-newapi-admin-promote-29922434-l44wt", map[string]string{"app.kubernetes.io/name": "bp-newapi", "app.kubernetes.io/instance": "newapi", "job-name": "newapi-bp-newapi-admin-promote-29922434"}, ""},
		{"openclaw per-user workspace pod", "openclaw-user-1b6f0c2e", map[string]string{"app.kubernetes.io/name": "bp-openclaw", "app.kubernetes.io/component": "per-user-pod"}, ""},
		{"synced openclaw pod of an older Organization", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g-dbc85157d1", map[string]string{vclusterManagedByLabel: "vcluster"}, "openclaw"},
		{"customer app synced pod", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", map[string]string{vclusterManagedByLabel: "vcluster"}, "wordpress"},
		{"backing service synced pod", "postgres-1-x-uatco-x-vcluster", map[string]string{vclusterManagedByLabel: "vcluster"}, "postgres"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := podOwnerSlug(tc.podName, wanted)
			if got != "" && !podTruthCounts(tc.podName, tc.labels, got) {
				got = ""
			}
			if got != tc.want {
				t.Errorf("pod %q labels %v counts for %q, want %q", tc.podName, tc.labels, got, tc.want)
			}
		})
	}
}

// The uninstall wait (orgAppPodPresent) must see the app in either placement:
// a synced platform-stack pod of an Organization created before the stack
// moved to the host holds "gone" until it leaves, and on the host only the
// app's own workload does, so the CNPG pod that resource-policy keep leaves
// behind and openclaw's per-user pods cannot hold the wait to its bound.
func TestOrgAppPodPresent_UninstallSeesBothPlacements(t *testing.T) {
	synced := map[string]string{vclusterManagedByLabel: "vcluster"}
	newapi := map[string]string{"app.kubernetes.io/name": "bp-newapi", "app.kubernetes.io/instance": "newapi"}
	cases := []struct {
		name    string
		podName string
		labels  map[string]string
		appSlug string
		want    bool
	}{
		{"newapi Deployment pod on the host", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", newapi, "newapi", true},
		{"kept newapi CNPG instance", "newapi-bp-newapi-newapi-pg-1", map[string]string{"cnpg.io/cluster": "newapi-bp-newapi-newapi-pg"}, "newapi", false},
		{"openclaw per-user workspace pod", "openclaw-user-1b6f0c2e", map[string]string{"app.kubernetes.io/name": "bp-openclaw", "app.kubernetes.io/component": "per-user-pod"}, "openclaw", false},
		{"older Organization: synced openclaw pod with the full tail", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g01-w3ojsz0a-x-vcluster", synced, "openclaw", true},
		{"older Organization: live cut synced openclaw pod", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g-dbc85157d1", synced, "openclaw", true},
		{"cut synced pod of another app", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g-dbc85157d1", synced, "newapi", false},
		{"customer app synced pod", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", synced, "wordpress", true},
		{"native wordpress pod is not the app", "wordpress-678cbb45dc-lv6hw", nil, "wordpress", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgAppPodPresent(tc.podName, tc.labels, tc.appSlug); got != tc.want {
				t.Errorf("orgAppPodPresent(%q, %v, %q) = %v, want %v", tc.podName, tc.labels, tc.appSlug, got, tc.want)
			}
			// Every pod the install wait takes, the uninstall wait sees.
			if orgAppPodMatches(tc.podName, tc.labels, tc.appSlug) && !orgAppPodPresent(tc.podName, tc.labels, tc.appSlug) {
				t.Errorf("orgAppPodMatches takes %q for %q but orgAppPodPresent does not", tc.podName, tc.appSlug)
			}
		})
	}
}
