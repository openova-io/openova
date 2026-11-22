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
// native openclaw name is the name that synced pod has INSIDE the vCluster,
// which is what a host install of the same Deployment produces, and the
// newapi and stalwart-mail names are constructed in the Deployment and
// StatefulSet shapes.
func TestOrgAppPodMatches_PlacementShapes(t *testing.T) {
	synced := map[string]string{vclusterManagedByLabel: "vcluster"}
	cases := []struct {
		name    string
		podName string
		labels  map[string]string
		appSlug string
		want    bool
	}{
		// Platform stack: native host pods are the app.
		{"openclaw controller on the host", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l", nil, "openclaw", true},
		{"newapi Deployment pod on the host", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", nil, "newapi", true},
		// Platform stack: a synced pod is a leftover of the old placement,
		// never the app, even when its cut name hides the syncer tail.
		{"live cut synced openclaw pod", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g-dbc85157d1", synced, "openclaw", false},
		{"live cut synced newapi CronJob pod", "newapi-bp-newapi-admin-promote-29922434-l44wt-x-g01--648fb43420", synced, "newapi", false},
		{"synced openclaw pod with the full tail", "openclaw-bp-openclaw-controller-6cb87f5f8c-r577l-x-g01-w3ojsz0a-x-vcluster", nil, "openclaw", false},
		{"prefix boundary on the host", "openclaw2-6cb87f5f8c-r577l", nil, "openclaw", false},
		{"another app's host pod", "newapi-bp-newapi-5d8f7c9b6d-k2x4q", nil, "openclaw", false},

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
