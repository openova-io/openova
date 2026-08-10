package handlers

import "testing"

// #4297 — the provisioning workflow's app-readiness poll must match the right
// pod-name shape per tier. Vcluster-tier (M+) app pods are synced up to the
// host ns with a `-x-<inner-namespace>-x-vcluster` tail; host-tier (free/S, no
// vcluster) pods run natively in the host ns as `<appSlug>-...`.
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
// The same hardcoded literal broke the HOST tier in the opposite direction:
// the host branch is documented as rejecting a stray synced pod from a sibling
// vcluster Org sharing the ns, but it only rejected pods whose inner namespace
// was literally `apps` — so a real synced pod sailed through the check that
// existed to stop it. One wrong constant, a false negative on one tier and a
// false positive on the other.
//
// The names below are therefore VERBATIM live pod names, not constructed ones.
// The identical trap was already caught and fixed in the sibling file — see
// backing_services.go's `vclusterInnerPodName` and
// backing_services_vcluster_suffix_5451_test.go, whose fixture discipline this
// follows. `appPodNameMatches` was never migrated onto that helper.
func TestAppPodNameMatches_TierAware(t *testing.T) {
	cases := []struct {
		name       string
		podName    string
		appSlug    string
		isVcluster bool
		want       bool
	}{
		// ---- Vcluster tier, VERBATIM live names from hw292-a ns uatco ----
		// These are the exact pods the row-86 provision was waiting for.
		{"live mysql synced pod matches", "mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster", "mysql", true, true},
		{"live wordpress synced pod matches", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", "wordpress", true, true},
		// Cross-checks against the same live pair.
		{"vcluster wrong app", "mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster", "wordpress", true, false},
		{"vcluster native pod does NOT match", "wordpress-678cbb45dc", "wordpress", true, false},

		// The inner namespace is the Org slug and varies per Org — the matcher
		// must not care which one it is.
		{"different org slug still matches", "ghost-abc123-x-walk-stranger-two-x-vcluster", "ghost", true, true},
		{"legacy apps inner ns still matches", "wordpress-7d9f-x-apps-x-vcluster", "wordpress", true, true},

		// ---- Host tier ----
		{"host native pod matches", "wordpress-7d9f", "wordpress", false, true},
		{"host wrong app", "ghost-7d9f", "wordpress", false, false},
		// The rejection this branch was written for, with a REAL inner ns. The
		// old literal let this through.
		{"host rejects synced pod from a sibling org", "wordpress-678cbb45dc-lv6hw-x-uatco-x-vcluster", "wordpress", false, false},
		{"host rejects legacy-shaped synced pod", "wordpress-7d9f-x-apps-x-vcluster", "wordpress", false, false},

		// ---- Prefix discipline ----
		{"prefix boundary respected", "wordpress2-7d9f", "wordpress", false, false},
		{"prefix boundary respected on synced pod", "wordpress2-7d9f-x-uatco-x-vcluster", "wordpress", true, false},
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

		if !appPodNameMatches(podName, "mysql", true) {
			t.Errorf("vcluster tier: %q did not match slug %q — the matcher is tied to a specific inner namespace",
				podName, "mysql")
		}
		if appPodNameMatches(podName, "mysql", false) {
			t.Errorf("host tier: %q matched, but a synced pod must never satisfy a host-tier wait", podName)
		}
	}
}
