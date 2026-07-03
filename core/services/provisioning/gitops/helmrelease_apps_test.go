package gitops

import (
	"strings"
	"testing"
)

// #4272 (openclaw) + #4307 (stalwart-mail) — the funnel-deployable catalog gap.
//
// Both apps were flagged Deployable=false because the generic generator only
// rendered a single Deployment (Image + Port) and they need HelmRelease-shaped
// overlays. The fix (helmrelease_apps.go) emits the upstream bp-openclaw /
// bp-stalwart-tenant HelmReleases as HOST files from the generic generator, so
// a funnel cart Org (which #4364 dispatches through GenerateAllWithPassword)
// now renders their HelmReleases into the per-Org gitops tree. These tests are
// the render-proof: a cart Org with openclaw+stalwart produces their HRs.

// cartOrgFor renders a funnel cart Org with the given apps + plan tier and
// returns the full file map (mirrors the day-2 GenerateAllWithPassword call in
// consumer.go: GenerateAllWithAppConfigs with an empty appConfigs map).
func cartOrgFor(t *testing.T, slug, planSlug string, apps []string) map[string]string {
	t.Helper()
	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = "omani.homes"
	return g.GenerateAllWithAppConfigs(slug, planSlug, apps, "pw", nil)
}

// TestFunnelCart_OpenClawStalwart_RenderHelmReleases is THE render-proof for
// #4272/#4307: a cart Org carrying both apps emits a bp-openclaw +
// bp-stalwart-tenant HelmRelease into the per-Org gitops tree. Run across both
// boundary tiers so neither the host nor the vcluster path regresses.
func TestFunnelCart_OpenClawStalwart_RenderHelmReleases(t *testing.T) {
	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			out := cartOrgFor(t, "acme", plan, []string{"wordpress", "openclaw", "stalwart-mail"})

			openclaw, ok := out[testBasePath+"/acme/app-openclaw.yaml"]
			if !ok {
				t.Fatalf("bp-openclaw HelmRelease NOT rendered for a cart Org (#4272) (keys: %v)", keys(out))
			}
			stalwart, ok := out[testBasePath+"/acme/app-stalwart-mail.yaml"]
			if !ok {
				t.Fatalf("bp-stalwart-tenant HelmRelease NOT rendered for a cart Org (#4307) (keys: %v)", keys(out))
			}

			// Each must be a real HelmRelease pointed at the right chart, NOT a
			// Deployment (the old broken `image: Required value` shape).
			for name, body := range map[string]string{"openclaw": openclaw, "stalwart": stalwart} {
				if !strings.Contains(body, "kind: HelmRelease") {
					t.Errorf("%s overlay is not a HelmRelease:\n%s", name, body)
				}
				if strings.Contains(body, "kind: Deployment") {
					t.Errorf("%s overlay must NOT render a raw Deployment (the #941 broken path):\n%s", name, body)
				}
			}
			if !strings.Contains(openclaw, "chart: bp-openclaw") {
				t.Errorf("openclaw overlay must source the bp-openclaw chart:\n%s", openclaw)
			}
			if !strings.Contains(stalwart, "chart: bp-stalwart-tenant") {
				t.Errorf("stalwart overlay must source the bp-stalwart-tenant chart:\n%s", stalwart)
			}

			// Each ships its own HelmRepository so a fresh funnel Org resolves
			// the sourceRef (the org-controller seeds only the vcluster repo).
			if !strings.Contains(openclaw, "kind: HelmRepository") || !strings.Contains(openclaw, "name: bp-openclaw") {
				t.Errorf("openclaw overlay must ship its bp-openclaw HelmRepository:\n%s", openclaw)
			}
			if !strings.Contains(stalwart, "kind: HelmRepository") || !strings.Contains(stalwart, "name: bp-stalwart-tenant") {
				t.Errorf("stalwart overlay must ship its bp-stalwart-tenant HelmRepository:\n%s", stalwart)
			}

			// They are HOST files (helm-controller runs on the host, #3055) —
			// NOT in the vcluster-redirected apps/ tree (which runs no
			// helm-controller, so an HR there never reconciles).
			if _, inApps := out[testBasePath+"/acme/apps/app-openclaw.yaml"]; inApps {
				t.Errorf("openclaw HR must be a HOST file, not in the vcluster apps/ tree")
			}
			if _, inApps := out[testBasePath+"/acme/apps/app-stalwart-mail.yaml"]; inApps {
				t.Errorf("stalwart HR must be a HOST file, not in the vcluster apps/ tree")
			}

			// And the host kustomization must enumerate them (else Flux ignores
			// the files).
			hostKust := out[testBasePath+"/acme/kustomization.yaml"]
			if !strings.Contains(hostKust, "app-openclaw.yaml") {
				t.Errorf("host kustomization must list app-openclaw.yaml:\n%s", hostKust)
			}
			if !strings.Contains(hostKust, "app-stalwart-mail.yaml") {
				t.Errorf("host kustomization must list app-stalwart-mail.yaml:\n%s", hostKust)
			}
		})
	}
}

// TestFunnelCart_OpenClaw_PublicHttpsEgress — the TITULAR #4272 gap: the funnel
// openclaw HR MUST stamp networkPolicy.egress.allowPublicHttps so the chart's
// CiliumNetworkPolicy emits the :443 public-HTTPS egress carve-out the /readyz
// JWKS hairpin to the public auth.<fqdn> host needs. Once the companion CNP
// attaches the apiserver-entity egress block, Cilium makes the controller
// egress CNP-enforced, so the K8s NP's ipBlock 0.0.0.0/0 :443 rule is inert for
// the reserved-identity hairpin — the CNP must carry the egress itself.
func TestFunnelCart_OpenClaw_PublicHttpsEgress(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"openclaw"})
	openclaw, ok := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !ok {
		t.Fatalf("bp-openclaw HelmRelease NOT rendered (keys: %v)", keys(out))
	}
	if !strings.Contains(openclaw, "allowPublicHttps: true") {
		t.Errorf("funnel openclaw HR must stamp networkPolicy.egress.allowPublicHttps: true (#4272 titular egress gap):\n%s", openclaw)
	}
	if !strings.Contains(openclaw, "allowApiserverEntity: true") {
		t.Errorf("funnel openclaw HR must keep networkPolicy.egress.allowApiserverEntity: true (#4319 reaper hop):\n%s", openclaw)
	}
	if !strings.Contains(openclaw, "allowGatewayEntity: true") {
		t.Errorf("funnel openclaw HR must keep networkPolicy.ingress.allowGatewayEntity: true (#4300 gateway hop):\n%s", openclaw)
	}
}

// TestFunnelCart_HRApps_NoDeploymentNoHostIngress — the HR apps must NOT also
// produce a raw app-*.yaml Deployment in the vcluster apps/ tree, and must NOT
// be wired into the traefik host ingress (they carry their own chart HTTPRoute
// parented to cilium-gateway-console). A Deployment-shaped co-installed app
// (wordpress) must still appear so the generic path keeps working.
func TestFunnelCart_HRApps_NoDeploymentNoHostIngress(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"wordpress", "openclaw", "stalwart-mail"})

	// No vcluster-tree Deployment for the HR apps.
	if _, ok := out[testBasePath+"/acme/apps/app-openclaw.yaml"]; ok {
		t.Errorf("openclaw must NOT render a raw Deployment under apps/")
	}
	if _, ok := out[testBasePath+"/acme/apps/app-stalwart-mail.yaml"]; ok {
		t.Errorf("stalwart-mail must NOT render a raw Deployment under apps/")
	}
	// The Deployment-shaped co-installed app still renders under apps/.
	if _, ok := out[testBasePath+"/acme/apps/app-wordpress.yaml"]; !ok {
		t.Errorf("wordpress Deployment must still render under apps/ (generic path intact) (keys: %v)", keys(out))
	}

	// Host ingress must not route the HR apps (they have their own HTTPRoute).
	ingress := out[testBasePath+"/acme/ingress.yaml"]
	if strings.Contains(ingress, "openclaw") {
		t.Errorf("host ingress must NOT route openclaw (chart owns its HTTPRoute):\n%s", ingress)
	}
	if strings.Contains(ingress, "stalwart") {
		t.Errorf("host ingress must NOT route stalwart-mail (chart owns its HTTPRoute):\n%s", ingress)
	}
	// wordpress still routed.
	if !strings.Contains(ingress, "wordpress-x-acme-x-vcluster") {
		t.Errorf("host ingress must still route the Deployment-shaped wordpress:\n%s", ingress)
	}
}

// TestFunnelCart_HRApps_TierAwareKubeConfig — the HR-level kubeConfig follows
// the same tier gate as the apps-sync + bp-cnpg-pair: vcluster tier (M+)
// installs the chart INTO the Org vcluster via the tenant-<slug>-kubeconfig
// mirror; host tier (S/free) installs straight into the host <slug> ns with NO
// kubeConfig.
func TestFunnelCart_HRApps_TierAwareKubeConfig(t *testing.T) {
	t.Run("vcluster-tier", func(t *testing.T) {
		out := cartOrgFor(t, "acme", "m", []string{"openclaw", "stalwart-mail"})
		for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
			body := out[testBasePath+"/acme/"+f]
			if !strings.Contains(body, "kubeConfig:") {
				t.Errorf("vcluster tier %s MUST carry an HR-level kubeConfig so the host helm-controller installs INTO the vcluster:\n%s", f, body)
			}
			if !strings.Contains(body, "name: tenant-acme-kubeconfig") {
				t.Errorf("vcluster tier %s kubeConfig must reference the tenant-<slug>-kubeconfig mirror:\n%s", f, body)
			}
		}
	})
	t.Run("host-tier", func(t *testing.T) {
		for _, plan := range []string{"s", "free", ""} {
			out := cartOrgFor(t, "acme", plan, []string{"openclaw", "stalwart-mail"})
			for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
				body := out[testBasePath+"/acme/"+f]
				if strings.Contains(body, "kubeConfig:") {
					t.Errorf("host tier (plan=%q) %s MUST NOT carry kubeConfig (no vcluster mirror exists):\n%s", plan, f, body)
				}
				if strings.Contains(body, "tenant-acme-kubeconfig") {
					t.Errorf("host tier (plan=%q) %s MUST NOT reference the never-created vcluster mirror:\n%s", plan, f, body)
				}
			}
		}
	})
}

// TestFunnelCart_HRApps_HostnamesUseParentDomain — the public hostnames are
// stamped from the generator's ParentDomain (TENANT_PARENT_DOMAIN), parented to
// the dedicated console Gateway (console isolation #4054).
func TestFunnelCart_HRApps_HostnamesUseParentDomain(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = "omani.trade"
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail"}, "pw", nil)

	openclaw := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !strings.Contains(openclaw, "openclaw.acme.omani.trade") {
		t.Errorf("openclaw HTTPRoute host must be openclaw.<slug>.<parent>:\n%s", openclaw)
	}
	if !strings.Contains(openclaw, "name: cilium-gateway-console") {
		t.Errorf("openclaw HTTPRoute must parent the dedicated console Gateway (#4054):\n%s", openclaw)
	}

	stalwart := out[testBasePath+"/acme/app-stalwart-mail.yaml"]
	if !strings.Contains(stalwart, "mail.acme.omani.trade") {
		t.Errorf("stalwart webmail host must be mail.<slug>.<parent>:\n%s", stalwart)
	}
	if !strings.Contains(stalwart, "name: cilium-gateway-console") {
		t.Errorf("stalwart webmail ingress must parent the dedicated console Gateway (#4307):\n%s", stalwart)
	}
}

// TestFunnelCart_Stalwart_LiveConvergedSettings — the #4307/#4246 settings the
// live demo Org proved are required must be present: disableWait (so the OIDC
// setup Job runs before the 15m budget burns) + admin.externalSecret:false (so
// the chart auto-provisions ADMIN_PASSWORD; nothing seeds OpenBao on a fresh
// funnel Org).
func TestFunnelCart_Stalwart_LiveConvergedSettings(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"stalwart-mail"})
	stalwart := out[testBasePath+"/acme/app-stalwart-mail.yaml"]
	if !strings.Contains(stalwart, "disableWait: true") {
		t.Errorf("stalwart HR must set disableWait (#4307) so the OIDC setup Job runs:\n%s", stalwart)
	}
	if !strings.Contains(stalwart, "externalSecret:\n        enabled: false") {
		t.Errorf("stalwart HR must disable admin.externalSecret (#4246) so the chart auto-provisions ADMIN_PASSWORD:\n%s", stalwart)
	}
}

// TestFunnelCart_HRApps_NoKeycloakDependsOn — the generic funnel path emits NO
// per-tenant bp-keycloak, so the openclaw/stalwart HRs must carry NO
// dependsOn (a dependsOn on a never-rendered HR wedges the release in
// DependencyNotReady forever).
func TestFunnelCart_HRApps_NoKeycloakDependsOn(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"openclaw", "stalwart-mail"})
	for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		body := out[testBasePath+"/acme/"+f]
		if strings.Contains(body, "dependsOn:") {
			t.Errorf("%s must NOT carry dependsOn — the generic funnel path emits no bp-keycloak so the HR would wedge in DependencyNotReady:\n%s", f, body)
		}
	}
}

// TestFunnelCart_NoHRApps_NoHostHRFiles — a cart with only Deployment-shaped
// apps produces NO HR app files (no regression / no stray empty host files).
func TestFunnelCart_NoHRApps_NoHostHRFiles(t *testing.T) {
	out := cartOrgFor(t, "acme", "m", []string{"wordpress", "ghost"})
	for _, f := range []string{"app-openclaw.yaml", "app-stalwart-mail.yaml"} {
		if _, ok := out[testBasePath+"/acme/"+f]; ok {
			t.Errorf("no HR app in cart, but %s was emitted", f)
		}
	}
}

// TestFunnelCart_HRApps_SharedRealmIssuer — THE #4272 render-proof. On a
// Sovereign whose per-Org Keycloak realm is DISABLED (the default), the
// per-Org `keycloak.<slug>.<parent>` host is NXDOMAIN, so openclaw's controller
// /readyz (which fetches the issuer's JWKS) hangs at 503 forever. When the
// generator carries a SovereignFQDN it MUST stamp the resolvable SHARED-realm
// issuer (auth.<fqdn>/realms/sovereign — the SAME live issuer the console uses)
// onto BOTH the canonical oidc.issuerURL and the legacy keycloak.realmURL, and
// must NOT emit the NXDOMAIN per-Org host.
func TestFunnelCart_HRApps_SharedRealmIssuer(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = "omani.homes"
	g.SovereignFQDN = "omantel.biz"
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail"}, "pw", nil)

	const wantIssuer = "https://auth.omantel.biz/realms/sovereign"
	const nxPerOrg = "keycloak.acme.omani.homes"

	openclaw := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !strings.Contains(openclaw, "issuerURL: "+wantIssuer) {
		t.Errorf("openclaw oidc.issuerURL must be the resolvable shared realm %q:\n%s", wantIssuer, openclaw)
	}
	if !strings.Contains(openclaw, "realmURL: "+wantIssuer) {
		t.Errorf("openclaw keycloak.realmURL must be the resolvable shared realm %q:\n%s", wantIssuer, openclaw)
	}
	if strings.Contains(openclaw, nxPerOrg) {
		t.Errorf("openclaw must NOT emit the NXDOMAIN per-Org realm host %q when the per-Org realm is disabled:\n%s", nxPerOrg, openclaw)
	}

	stalwart := out[testBasePath+"/acme/app-stalwart-mail.yaml"]
	if !strings.Contains(stalwart, "realmURL: "+wantIssuer) {
		t.Errorf("stalwart keycloak.realmURL must be the resolvable shared realm %q:\n%s", wantIssuer, stalwart)
	}
	if strings.Contains(stalwart, nxPerOrg) {
		t.Errorf("stalwart must NOT emit the NXDOMAIN per-Org realm host %q when the per-Org realm is disabled:\n%s", nxPerOrg, stalwart)
	}
}

// TestFunnelCart_HRApps_PerOrgRealmFallback — when SovereignFQDN is unset
// (Catalyst-Zero, or a cluster that genuinely runs per-Org realms), the issuer
// stays on the conventional per-Org realm host (NO-REGRESS for the legacy path).
func TestFunnelCart_HRApps_PerOrgRealmFallback(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = "omani.homes"
	// SovereignFQDN intentionally unset.
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail"}, "pw", nil)

	const perOrg = "https://keycloak.acme.omani.homes/realms/org-acme"
	openclaw := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !strings.Contains(openclaw, "issuerURL: "+perOrg) {
		t.Errorf("openclaw must fall back to the per-Org realm host when SovereignFQDN is unset:\n%s", openclaw)
	}
	stalwart := out[testBasePath+"/acme/app-stalwart-mail.yaml"]
	if !strings.Contains(stalwart, "realmURL: "+perOrg) {
		t.Errorf("stalwart must fall back to the per-Org realm host when SovereignFQDN is unset:\n%s", stalwart)
	}
}

// TestSharedRealmIssuer_RealmOverride — CATALYST_KC_REALM override flows into
// the shared issuer realm segment (so a Sovereign with a non-default realm name
// still gets a resolvable issuer).
func TestSharedRealmIssuer_RealmOverride(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	g.SovereignFQDN = "omantel.biz"
	g.SharedRealmName = "catalyst"
	if got, want := g.sharedRealmIssuer(), "https://auth.omantel.biz/realms/catalyst"; got != want {
		t.Errorf("sharedRealmIssuer() = %q, want %q", got, want)
	}
	g.SovereignFQDN = ""
	if got := g.sharedRealmIssuer(); got != "" {
		t.Errorf("sharedRealmIssuer() with empty SovereignFQDN = %q, want empty", got)
	}
}

// TestParentDomain_DefaultFallback — an unset ParentDomain falls back to the
// catalog-canon default pool so a render never produces a bare `<slug>.` host.
func TestParentDomain_DefaultFallback(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	if got := g.parentDomain(); got != parentDomainDefault {
		t.Errorf("parentDomain() default = %q, want %q", got, parentDomainDefault)
	}
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw"}, "pw", nil)
	openclaw := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !strings.Contains(openclaw, "openclaw.acme."+parentDomainDefault) {
		t.Errorf("default parent-domain host wrong:\n%s", openclaw)
	}
}

// TestGenerateHelmReleaseApp_PinsChartVersion (#4706): a configured pin
// replaces the floating `version: "*"` in the rendered HR; an absent pin
// keeps "*" (degrade-to-historical, never break the render).
func TestGenerateHelmReleaseApp_PinsChartVersion(t *testing.T) {
	pinned := generateHelmReleaseApp("openclaw", helmReleaseAppOpts{
		slug: "acme", parentDomain: "omani.homes", chartVersion: "0.2.13",
	})
	if !strings.Contains(pinned, `version: "0.2.13"`) {
		t.Fatalf("pinned render must carry version: \"0.2.13\"; got:\n%s", pinned)
	}
	if strings.Contains(pinned, `version: "*"`) {
		t.Fatalf("pinned render must not retain the floating version: \"*\"")
	}

	floating := generateHelmReleaseApp("stalwart-mail", helmReleaseAppOpts{
		slug: "acme", parentDomain: "omani.homes",
	})
	if !strings.Contains(floating, `version: "*"`) {
		t.Fatalf("absent pin must fall back to the historical floating \"*\"; got:\n%s", floating)
	}
}

// TestParseHRAppVersions (#4706): wire-format parse + malformed-entry skip.
func TestParseHRAppVersions(t *testing.T) {
	m := ParseHRAppVersions(" openclaw=0.2.13, stalwart-mail = 0.1.12 ,broken,=x,y= ")
	if m["openclaw"] != "0.2.13" || m["stalwart-mail"] != "0.1.12" {
		t.Fatalf("parse failed: %#v", m)
	}
	if len(m) != 2 {
		t.Fatalf("malformed entries must be skipped, got %#v", m)
	}
}
