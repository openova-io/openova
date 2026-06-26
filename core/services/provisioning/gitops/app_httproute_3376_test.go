package gitops

import (
	"strings"
	"testing"
)

// #3376 (funnel cart WordPress 404-without-route gap) — the funnel cart
// WordPress is a Deployment-shaped app emitted inline by generateAppDeployment (NOT the
// bp-wordpress-tenant chart, which already carries its own HTTPRoute from
// #3785). The generator historically emitted the Deployment + Service but NO
// HTTPRoute, so a healthy WordPress Pod returned public 404: the host
// generateHostIngress() traefik Ingress is INERT on a Cilium-Gateway Sovereign
// and is dropped entirely by the de-vcluster'd per-Org apps tree (#4384). These
// tests are the render-proof that the co-located HTTPRoute now ships with the
// app, with the right host + console-Gateway parentRef + plain-Service backendRef.

// httpRouteDoc finds the app HTTPRoute inside the rendered app-<slug>.yaml doc.
func wordpressAppDoc(t *testing.T, g *ManifestGenerator, slug, plan string) string {
	t.Helper()
	out := g.GenerateAllWithAppConfigs(slug, plan, []string{"wordpress"}, "pw", nil)
	for path, body := range out {
		if strings.HasSuffix(path, "/app-wordpress.yaml") {
			return body
		}
	}
	t.Fatalf("app-wordpress.yaml not rendered for slug=%q plan=%q (keys: %v)", slug, plan, keys(out))
	return ""
}

// TestAppHTTPRoute_WordPress_Rendered — the funnel WordPress app doc carries a
// Gateway-API HTTPRoute alongside the Deployment + Service.
func TestAppHTTPRoute_WordPress_Rendered(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/tenants")
	g.ParentDomain = "omani.homes"

	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			doc := wordpressAppDoc(t, g, "g6wp", plan)

			// The HTTPRoute object renders (Gateway API, NOT traefik Ingress).
			if !strings.Contains(doc, "kind: HTTPRoute") {
				t.Fatalf("app-wordpress.yaml MISSING the Gateway-API HTTPRoute (would 404 on a Cilium-Gateway Sovereign):\n%s", doc)
			}
			if !strings.Contains(doc, "apiVersion: gateway.networking.k8s.io/v1") {
				t.Errorf("HTTPRoute not on the gateway.networking.k8s.io/v1 apiVersion:\n%s", doc)
			}

			// Host = <app>.<slug>.<parentDomain> (same convention as openclaw.<slug>.<parent>).
			if !strings.Contains(doc, "- wordpress.g6wp.omani.homes") {
				t.Errorf("HTTPRoute host is not wordpress.<slug>.<parentDomain>:\n%s", doc)
			}

			// parentRef = the dedicated console Gateway (wildcard *.<pool> TLS → no per-host cert).
			if !strings.Contains(doc, "name: cilium-gateway-console") {
				t.Errorf("HTTPRoute parentRef is not the console Cilium Gateway:\n%s", doc)
			}
			if !strings.Contains(doc, "namespace: kube-system") {
				t.Errorf("HTTPRoute parentRef namespace is not kube-system:\n%s", doc)
			}

			// backendRef = the plain WordPress Service on :80 (co-located in the same boundary).
			if !strings.Contains(doc, "backendRefs:") {
				t.Errorf("HTTPRoute MISSING backendRefs:\n%s", doc)
			}
			if !strings.Contains(doc, "- name: wordpress\n          port: 80") {
				t.Errorf("HTTPRoute backendRef is not the wordpress Service:80:\n%s", doc)
			}
		})
	}
}

// TestAppHTTPRoute_ParentDomainDefault — with no ParentDomain wiring the host
// falls back to the catalog-canon default pool (never a bare `<app>.<slug>.`).
func TestAppHTTPRoute_ParentDomainDefault(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/tenants") // ParentDomain unset
	doc := wordpressAppDoc(t, g, "g6wp", "s")
	want := "- wordpress.g6wp." + parentDomainDefault
	if !strings.Contains(doc, want) {
		t.Errorf("HTTPRoute host did not fall back to %q:\n%s", want, doc)
	}
}

// TestAppHTTPRoute_SurvivesPerOrgReRoot — the #4384 de-vcluster'd per-Org apps
// tree drops the contabo host scaffolding (incl. the inert traefik ingress.yaml)
// but the co-located HTTPRoute travels WITH the app under vcluster/apps/, so a
// fresh funnel Org's WordPress is routed zero-touch (no hand-applied route).
func TestAppHTTPRoute_SurvivesPerOrgReRoot(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	files, _ := g.GeneratePerOrgAppsTree("g6wp", "m", []string{"wordpress"}, "pw")
	wp, ok := files["vcluster/apps/app-wordpress.yaml"]
	if !ok {
		t.Fatalf("app-wordpress.yaml missing from per-Org tree (keys: %v)", keys(files))
	}
	if !strings.Contains(wp, "kind: HTTPRoute") {
		t.Fatalf("re-rooted app-wordpress.yaml lost its HTTPRoute — funnel Org would 404:\n%s", wp)
	}
	if !strings.Contains(wp, "- wordpress.g6wp.omani.homes") {
		t.Errorf("re-rooted HTTPRoute host wrong:\n%s", wp)
	}
	if !strings.Contains(wp, "name: cilium-gateway-console") {
		t.Errorf("re-rooted HTTPRoute parentRef wrong:\n%s", wp)
	}

	// The contabo host scaffolding (the inert traefik ingress.yaml) MUST NOT
	// have leaked into the per-Org tree.
	for p := range files {
		if strings.HasSuffix(p, "/ingress.yaml") {
			t.Errorf("inert traefik ingress.yaml leaked into per-Org tree at %q", p)
		}
	}
}
