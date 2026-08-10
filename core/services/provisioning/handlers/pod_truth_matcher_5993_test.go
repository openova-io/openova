package handlers

import (
	"strings"
	"testing"
)

// pod_truth_matcher_5993_test.go — UAT row 86.
//
// The pod-truth reconciler is what re-observes reality for a provision record
// that was written `failed` and whose workload later came up. #5646 made a
// `failed` record re-observable; this file covers WHICH PODS it can see while
// doing so, which is a separate question and was answered wrong.
//
// reconcileOneProvision carried its OWN third definition of "is this pod
// mine", inline:
//
//	if !strings.HasSuffix(name, "-x-vcluster") { continue }   // (a)
//	...
//	parts := strings.Split(podPart, "-")
//	if len(parts) < 3 { continue }                            // (b)
//	slug := strings.Join(parts[:len(parts)-2], "-")           // (c)
//
// Three defects, each independently sufficient to strand a record:
//
//	(a) HOST TIER IS INVISIBLE. isolationForTier maps plan free/S/"" to
//	    "namespace" (allTiersVcluster is false), so those Orgs run their app
//	    pods NATIVELY in the host `<slug>` ns with no syncer suffix. The
//	    suffix gate skips every one of them, `ready` comes back empty and
//	    reconcileOneProvision returns before it can heal anything. The
//	    write-once record #5646 set out to fix stays permanently failed for
//	    exactly the tier the funnel sells first.
//
//	(b) SHORT POD NAMES ARE SKIPPED. The `< 3 segments` guard assumes every
//	    pod is Deployment-shaped (`<slug>-<rsHash>-<podHash>`). A
//	    StatefulSet pod is `<slug>-<ordinal>` — two segments — so `postgres-1`
//	    is dropped. The per-Org CNPG Postgres in UAT row 238 has precisely
//	    that shape.
//
//	(c) THE SLUG IS RECONSTRUCTED, NOT MATCHED. Dropping the last two dashed
//	    segments is a guess about the tail rather than a comparison against
//	    the slugs this provision actually asked for.
//
// handlers.go:868 already states the rule this violated — "Delegating to
// vclusterInnerPodName is the point ... There must be one definition of 'is
// this a synced pod', not two." podOwnerSlug now delegates to that helper for
// the synced shape and to matchWantedSlug (backing_services.go) for the
// slug comparison, so there is one definition of each.
//
// Refs #5993

// wantedSet is the shape reconcileOneProvision builds from the provision's
// declared apps + step names.
func wantedSet(slugs ...string) map[string]bool {
	m := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		m[s] = true
	}
	return m
}

// legacyPodOwnerSlug reproduces the matcher this change replaces, verbatim in
// behaviour. It is the CONTROL: every case below is asserted against BOTH
// implementations, so each assertion is shown to discriminate rather than to
// pass on anything. A case where both agree is marked as such and is there to
// prove the new matcher did not regress the shape that already worked.
func legacyPodOwnerSlug(podName string, _ map[string]bool) string {
	const vcSuffix = "-x-vcluster"
	if !strings.HasSuffix(podName, vcSuffix) {
		return ""
	}
	core := strings.TrimSuffix(podName, vcSuffix)
	idx := strings.LastIndex(core, "-x-")
	if idx < 0 {
		return ""
	}
	podPart := core[:idx]
	if strings.HasPrefix(podPart, "coredns") || strings.HasPrefix(podPart, "vcluster") {
		return ""
	}
	parts := strings.Split(podPart, "-")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "-")
}

func TestPodOwnerSlug(t *testing.T) {
	apps := wantedSet("wordpress", "mysql", "postgres", "uptime-kuma")

	cases := []struct {
		name       string
		pod        string
		want       string
		wantLegacy string // what the replaced matcher returned
	}{
		{
			// (a) HOST TIER. Plan free/S Org — native pod, no syncer suffix.
			// This is the case that stranded the record.
			name:       "host-tier native Deployment pod",
			pod:        "mysql-5b9d89cbc6-hh6jt",
			want:       "mysql",
			wantLegacy: "",
		},
		{
			name:       "host-tier native app pod",
			pod:        "wordpress-7d4f9c8b6-2xk9p",
			want:       "wordpress",
			wantLegacy: "",
		},
		{
			// (b) SHORT NAME. StatefulSet ordinal, host tier.
			name:       "host-tier StatefulSet pod",
			pod:        "postgres-1",
			want:       "postgres",
			wantLegacy: "",
		},
		{
			// (b) again, this time synced — the suffix gate passes but the
			// three-segment guard drops it.
			name:       "vcluster-synced StatefulSet pod",
			pod:        "postgres-1-x-uatco-x-vcluster",
			want:       "postgres",
			wantLegacy: "",
		},
		{
			// Both agree — the shape that already worked must keep working.
			name:       "vcluster-synced Deployment pod (no regression)",
			pod:        "mysql-5b9d89cbc6-hh6jt-x-uatco-x-vcluster",
			want:       "mysql",
			wantLegacy: "mysql",
		},
		{
			// (c) SLUG RECONSTRUCTION. A dashed slug survives the legacy
			// split only by luck of segment count; assert it against the
			// requested set instead.
			name:       "dashed slug, host tier",
			pod:        "uptime-kuma-6b8d7c9f4-abcde",
			want:       "uptime-kuma",
			wantLegacy: "",
		},
		{
			// (c) again. The legacy matcher RECONSTRUCTED a slug from the
			// pod name, so it claimed an app this provision never asked
			// for; the new one compares against the requested set and
			// declines.
			name:       "unrequested app is not claimed",
			pod:        "redis-5b9d89cbc6-hh6jt-x-uatco-x-vcluster",
			want:       "",
			wantLegacy: "redis",
		},
		{
			name:       "infra pod is never an app",
			pod:        "coredns-abc-def-x-uatco-x-vcluster",
			want:       "",
			wantLegacy: "",
		},
		{
			name:       "vcluster control-plane pod is never an app",
			pod:        "vcluster-0-x-uatco-x-vcluster",
			want:       "",
			wantLegacy: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := podOwnerSlug(tc.pod, apps); got != tc.want {
				t.Errorf("podOwnerSlug(%q) = %q, want %q", tc.pod, got, tc.want)
			}
			// Control: pin what the replaced matcher did, so a case that
			// "passes" cannot be one both implementations already handled
			// unless it is declared as such above.
			if got := legacyPodOwnerSlug(tc.pod, apps); got != tc.wantLegacy {
				t.Errorf("legacy matcher drifted: legacyPodOwnerSlug(%q) = %q, want %q",
					tc.pod, got, tc.wantLegacy)
			}
		})
	}
}

// TestPodOwnerSlug_HostTierIsTheRegression states the headline in one
// assertion: on the tier the funnel sells first, the replaced matcher saw
// NOTHING, so reconcileOneProvision returned early and no failed step could
// ever be superseded.
func TestPodOwnerSlug_HostTierIsTheRegression(t *testing.T) {
	apps := wantedSet("wordpress", "mysql")
	hostTierPods := []string{
		"wordpress-7d4f9c8b6-2xk9p",
		"mysql-5b9d89cbc6-hh6jt",
	}
	seen := 0
	legacySeen := 0
	for _, p := range hostTierPods {
		if podOwnerSlug(p, apps) != "" {
			seen++
		}
		if legacyPodOwnerSlug(p, apps) != "" {
			legacySeen++
		}
	}
	if seen != len(hostTierPods) {
		t.Errorf("host-tier pods matched: got %d, want %d", seen, len(hostTierPods))
	}
	if legacySeen != 0 {
		t.Errorf("control is not exercising the defect: the replaced matcher saw %d "+
			"host-tier pods, expected 0 — if this fires the test proves nothing", legacySeen)
	}
}
