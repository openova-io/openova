package handlers

import "testing"

// #4297 — the provisioning workflow's app-readiness poll must match the ONE
// pod-name shape an Organization app pod has on the host: every Org's apps run
// inside its vCluster (#4292) and are synced up to the host ns with a
// `-x-<inner-namespace>-x-vcluster` tail. A native, un-synced pod is never an
// Organization app pod.
//
// WHY THESE FIXTURES ARE COPIED OFF A LIVE CLUSTER (UAT row 86).
//
// The previous version of this test invented every pod name it asserted on,
// and every invented name used the inner namespace `apps`:
//
//	{"vcluster synced pod matches", "wordpress-7d9f-x-apps-x-vcluster", ...}
//
// `apps` has not been the inner namespace since #4290 made it the Org slug.
// So the fixtures agreed with the matcher's hardcoded `-x-apps-x-vcluster`
// literal, the suite went green, and the production matcher could not match a
// single real pod. Measured read-only on hw292-a: pods cluster-wide carrying
// `-x-apps-x-vcluster` = ZERO, while namespace `uatco` held
// `mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster` 1/1 Running and
// `wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster` 1/1 Running.
//
// That is UAT row 86: `waitForVclusterApp` polled for ten minutes, matched
// nothing, and failed the provision with "app mysql not ready in uatco after
// 10m0s" — while mysql had been Ready within seconds and stayed up for days.
// The customer's timeline showed a permanent red step for a healthy Org.
//
// (The matcher used to carry a second, native-name arm for plans that had no
// vcluster; the same literal broke that arm's rejection of synced pods in the
// opposite direction. That arm is gone with #4292 — there is one shape now.)
//
// The names below are therefore VERBATIM live pod names, not constructed ones.
// The identical trap was already caught and fixed in the sibling file — see
// backing_services.go's `vclusterInnerPodName` and
// backing_services_vcluster_suffix_5451_test.go, whose fixture discipline this
// follows. `appPodNameMatches` was never migrated onto that helper.
func TestAppPodNameMatches_SyncedShapeOnly(t *testing.T) {
	cases := []struct {
		name    string
		podName string
		appSlug string
		want    bool
	}{
		// ---- VERBATIM live names from hw292-a ns uatco ----
		// These are the exact pods the row-86 provision was waiting for.
		{"live mysql synced pod matches", "mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster", "mysql", true},
		{"live wordpress synced pod matches", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", "wordpress", true},
		// Cross-checks against the same live pair.
		{"synced pod, wrong app", "mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster", "wordpress", false},

		// The inner namespace is the Org slug and varies per Org — the matcher
		// must not care which one it is.
		{"different org slug still matches", "ghost-abc123-x-walk-stranger-two-x-vcluster", "ghost", true},
		{"legacy apps inner ns still matches", "wordpress-7d9f-x-apps-x-vcluster", "wordpress", true},

		// ---- Native, un-synced pods are never an Organization app pod ----
		// Every Org's apps run inside its vCluster (#4292); a pod in the host
		// ns without the syncer tail is infra or a stray, never the app.
		{"native pod does NOT match", "wordpress-678cbb45dc", "wordpress", false},
		{"native Deployment-shaped pod does NOT match", "wordpress-7d9f", "wordpress", false},
		{"native pod, wrong app", "ghost-7d9f", "wordpress", false},

		// ---- Prefix discipline ----
		{"prefix boundary respected on synced pod", "wordpress2-7d9f-x-uatco-x-vcluster", "wordpress", false},
		{"prefix boundary respected on native pod", "wordpress2-7d9f", "wordpress", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appPodNameMatches(tc.podName, tc.appSlug); got != tc.want {
				t.Errorf("appPodNameMatches(%q, %q) = %v, want %v",
					tc.podName, tc.appSlug, got, tc.want)
			}
		})
	}
}

// TestAppPodNameMatches_NoHardcodedInnerNamespace is the guard that would have
// caught the original defect on the day it was written.
//
// It asserts the property that actually matters — the matcher works for an
// arbitrary Org slug — rather than enumerating slugs, so it cannot be satisfied
// by adding one more fixture to the table above.
func TestAppPodNameMatches_NoHardcodedInnerNamespace(t *testing.T) {
	// Every one of these is a plausible Org slug; `apps` is deliberately NOT
	// among them, because that is the value the broken matcher was pinned to.
	for _, innerNS := range []string{"uatco", "walk-stranger-two", "acme", "a", "org-with-many-dashes"} {
		podName := "mysql-5b9d89cbc6-hh6jt-x-" + innerNS + "-x-vcluster"

		if !appPodNameMatches(podName, "mysql") {
			t.Errorf("%q did not match slug %q — the matcher is tied to a specific inner namespace",
				podName, "mysql")
		}
	}
	// The discriminating half: the SAME pod without the syncer tail is a
	// native, un-synced pod, which is never an Organization app pod (#4292).
	if appPodNameMatches("mysql-5b9d89cbc6-hh6jt", "mysql") {
		t.Errorf("native un-synced pod %q matched — only synced pods are Organization app pods", "mysql-5b9d89cbc6-hh6jt")
	}
}
