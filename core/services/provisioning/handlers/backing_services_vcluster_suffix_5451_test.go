package handlers

import "testing"

// The pod-name matcher hardcoded the inner vCluster namespace as `apps`. Since
// #4290 the inner namespace is the Org slug, so the matcher matched NOTHING and
// every service reported "not_found" — a total miss that read as an answer.
//
// Every fixture below is a VERBATIM pod name observed live on hw290
// (2026-07-27, namespace theta-corp), not an invented shape.

func TestVclusterInnerPodName_RealHw290Names(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		// The form that broke it: inner namespace is the Org slug, not "apps".
		{"umami-7c4df67dc6-vdwb7-x-theta-corp-x-vcluster", "umami-7c4df67dc6-vdwb7", true},
		{"uptime-kuma-8c896959b-dqdbn-x-theta-corp-x-vcluster", "uptime-kuma-8c896959b-dqdbn", true},
		{"postgres-6686dd746c-ljztg-x-theta-corp-x-vcluster", "postgres-6686dd746c-ljztg", true},
		{"coredns-7f84df6475-lscbf-x-kube-system-x-vcluster", "coredns-7f84df6475-lscbf", true},
		// The legacy form must keep working.
		{"postgres-79dc6fc6d-4n9r5-x-apps-x-vcluster", "postgres-79dc6fc6d-4n9r5", true},
		// Host-native pods are not vCluster-synced.
		{"vcluster-0", "", false},
		{"orgpg-1", "", false},
		{"postgres-1-initdb-v67jd", "", false},
	}
	for _, c := range cases {
		got, ok := vclusterInnerPodName(c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("%s → (%q,%v), want (%q,%v)", c.host, got, ok, c.want, c.ok)
		}
	}
}

// Slugs contain dashes. Splitting on the first dash mapped "uptime-kuma-..." to
// "uptime", which matches no requested slug — so even with the suffix fixed,
// every multi-word app would still have reported not_found.
func TestMatchWantedSlug_DashedSlugsAndLongestWins(t *testing.T) {
	wanted := map[string]bool{"umami": true, "uptime-kuma": true, "postgres": true}

	if got := matchWantedSlug("uptime-kuma-8c896959b-dqdbn", wanted); got != "uptime-kuma" {
		t.Errorf("dashed slug: got %q, want uptime-kuma", got)
	}
	if got := matchWantedSlug("umami-7c4df67dc6-vdwb7", wanted); got != "umami" {
		t.Errorf("simple slug: got %q, want umami", got)
	}
	if got := matchWantedSlug("coredns-7f84df6475-lscbf", wanted); got != "" {
		t.Errorf("unrequested workload must not match, got %q", got)
	}

	// A shorter slug must never shadow a longer one that also matches.
	shadow := map[string]bool{"uptime": true, "uptime-kuma": true}
	if got := matchWantedSlug("uptime-kuma-8c896959b-dqdbn", shadow); got != "uptime-kuma" {
		t.Errorf("longest match must win, got %q", got)
	}

	// A slug must not match a different workload that merely shares a prefix.
	if got := matchWantedSlug("umamiother-abc", map[string]bool{"umami": true}); got != "" {
		t.Errorf("prefix must be dash-delimited, got %q", got)
	}
}
