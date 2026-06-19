package gitops

import (
	"strings"
	"testing"
)

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

// TestDBBackingImages_Proxied (#3785, Refs #3376 #3761) — the single-Pod
// DB-backing Deployments (postgres / mysql / redis) co-installed with a
// customer's purchased app must ALSO route their images through the Sovereign
// Harbor proxy-cache. Their backing Pods land on the HOST cluster where the
// harbor-proxy-pull Kyverno ClusterPolicy (Enforce) DENIES any image not
// matching */proxy-*/* — a bare postgres:16-alpine / mariadb:11 /
// valkey/valkey:8-alpine would block the DB, so the app it backs never Runs
// and the #3376 funnel terminal stays connection-refused.
func TestDBBackingImages_Proxied(t *testing.T) {
	g := &ManifestGenerator{BasePath: "clusters/contabo-mkt/tenants"}
	// chatwoot needs postgres + redis; ghost needs mysql — one tenant that
	// exercises all three DB-backing generators at once.
	files := g.GenerateAllWithAppConfigs("acme", "flexi",
		[]string{"chatwoot", "ghost"}, "deadbeef", nil)

	want := map[string]string{
		"db-postgres.yaml": "image: harbor.openova.io/proxy-dockerhub/postgres:16-alpine",
		"db-mysql.yaml":    "image: harbor.openova.io/proxy-dockerhub/mariadb:11",
		"db-redis.yaml":    "image: harbor.openova.io/proxy-dockerhub/valkey/valkey:8-alpine",
	}
	// The pre-fix bare references that must NEVER survive into the rendered
	// manifest (they'd be Kyverno-denied on the host).
	banned := map[string]string{
		"db-postgres.yaml": "image: postgres:16-alpine",
		"db-mysql.yaml":    "image: mariadb:11",
		"db-redis.yaml":    "image: valkey/valkey:8-alpine",
	}

	seen := map[string]bool{}
	for path, content := range files {
		for suffix, proxied := range want {
			if !strings.HasSuffix(path, suffix) {
				continue
			}
			seen[suffix] = true
			if !strings.Contains(content, proxied) {
				t.Errorf("%s missing proxied image %q — full manifest:\n%s", suffix, proxied, content)
			}
			if strings.Contains(content, banned[suffix]) {
				t.Errorf("%s still carries bare image %q — Kyverno harbor-proxy-pull would DENY it", suffix, banned[suffix])
			}
		}
	}
	for suffix := range want {
		if !seen[suffix] {
			t.Errorf("expected %s to be generated for the chatwoot+ghost tenant", suffix)
		}
	}
}
