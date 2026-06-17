package gitops

import "testing"

// #3785 (Refs #3376 #3761) — proxyImage must re-tag upstream app images
// through the registry-appropriate Sovereign Harbor proxy-cache project so
// they pass the harbor-proxy-pull Kyverno ClusterPolicy (Enforce, glob
// */proxy-*/*). The vCluster syncer schedules the backing Pod on the HOST
// cluster where the policy enforces, so a raw image is admission-denied and
// the purchased app never Runs.
func TestProxyImage(t *testing.T) {
	const mirror = "harbor.openova.io"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"dockerhub library (wordpress)", "wordpress:6-apache", "harbor.openova.io/proxy-dockerhub/wordpress:6-apache"},
		{"dockerhub namespaced", "invoiceshelf/invoiceshelf:latest", "harbor.openova.io/proxy-dockerhub/invoiceshelf/invoiceshelf:latest"},
		{"ghcr", "ghcr.io/umami-software/umami:postgresql-latest", "harbor.openova.io/proxy-ghcr/umami-software/umami:postgresql-latest"},
		{"quay", "quay.io/foo/bar:1", "harbor.openova.io/proxy-quay/foo/bar:1"},
		{"registry.k8s.io", "registry.k8s.io/pause:3.9", "harbor.openova.io/proxy-k8s/pause:3.9"},
		// lscr.io has no Harbor proxy project — pass through unchanged (no
		// regression; day-2 catalog app, not the funnel terminal).
		{"unknown registry passes through", "lscr.io/linuxserver/bookstack:latest", "lscr.io/linuxserver/bookstack:latest"},
		// Already proxied — idempotent.
		{"already proxied", "harbor.openova.io/proxy-dockerhub/wordpress:6-apache", "harbor.openova.io/proxy-dockerhub/wordpress:6-apache"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proxyImage(c.in, mirror); got != c.want {
				t.Errorf("proxyImage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// Empty mirror → no rewrite (pre-Harbor bootstrap / disabled).
	if got := proxyImage("wordpress:6-apache", ""); got != "wordpress:6-apache" {
		t.Errorf("empty mirror must not rewrite; got %q", got)
	}
	// Empty image → empty.
	if got := proxyImage("", mirror); got != "" {
		t.Errorf("empty image must stay empty; got %q", got)
	}
}
