package handlers

import "testing"

// #4297 — the provisioning workflow's app-readiness poll must match the right
// pod-name shape per tier. Vcluster-tier (M+) app pods are synced up to the
// host ns as `<appSlug>-...-x-apps-x-vcluster`; host-tier (free/S, no vcluster)
// pods run natively in the host ns as `<appSlug>-...`. A host-tier wait that
// looked for the syncer suffix would never match → 10-min timeout → false
// provision failure. This locks the pure matcher.
func TestAppPodNameMatches_TierAware(t *testing.T) {
	cases := []struct {
		name       string
		podName    string
		appSlug    string
		isVcluster bool
		want       bool
	}{
		// Vcluster tier — only the syncer-suffixed pod matches.
		{"vcluster synced pod matches", "wordpress-7d9f-x-apps-x-vcluster", "wordpress", true, true},
		{"vcluster native pod does NOT match", "wordpress-7d9f", "wordpress", true, false},
		{"vcluster wrong app", "ghost-7d9f-x-apps-x-vcluster", "wordpress", true, false},

		// Host tier — only the native-named pod matches; a stray synced pod from
		// a sibling vcluster Org sharing the ns must NOT satisfy the wait.
		{"host native pod matches", "wordpress-7d9f", "wordpress", false, true},
		{"host rejects synced pod", "wordpress-7d9f-x-apps-x-vcluster", "wordpress", false, false},
		{"host wrong app", "ghost-7d9f", "wordpress", false, false},

		// Prefix discipline — a different app whose name starts similarly.
		{"prefix boundary respected", "wordpress2-7d9f", "wordpress", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appPodNameMatches(tc.podName, tc.appSlug, tc.isVcluster); got != tc.want {
				t.Errorf("appPodNameMatches(%q, %q, isVcluster=%v) = %v, want %v",
					tc.podName, tc.appSlug, tc.isVcluster, got, tc.want)
			}
		})
	}
}
