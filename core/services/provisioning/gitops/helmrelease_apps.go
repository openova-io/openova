package gitops

import (
	"fmt"
	"sort"
	"strings"
)

// HelmRelease-shaped per-Org catalog apps (#4272 openclaw, #4307 stalwart).
//
// WHY THIS FILE EXISTS — the catalog-gap fix.
//
// The generic funnel/provisioning generator (ManifestGenerator.GenerateAll*)
// renders most marketplace apps as a single raw Deployment (KnownApps +
// generateAppDeployment). Two catalog apps cannot be expressed that way:
//
//   - openclaw (catalog slug `openclaw`)        → ships a controller + a
//     per-user-pod template + an HTTPRoute + a CiliumNetworkPolicy; a one-
//     Deployment template can't carry that shape (the rendered Deployment was
//     `containers[0].image: Required value` — the live #941 failure on tenant
//     test11).
//   - stalwart-mail (catalog slug `stalwart-mail`) → ships a StatefulSet with
//     IMAP/SMTP/web Services, a DNS-01 mail cert, an OIDC setup Job, and an
//     admin-secret auto-provisioner.
//
// Because they had no render path here, DeployableAppSlugs() flagged both
// Deployable=false, so the marketplace drew a "COMING SOON" overlay AND the
// funnel cart-placement (#4364) skipped them (it only dispatches deployable
// apps). A funnel Org therefore never got their HelmReleases — the exact
// #4272/#4307 gap.
//
// THE FIX: emit the upstream bp-openclaw / bp-stalwart-tenant HelmReleases —
// mirroring the BSS-door overlay emitters in
// products/catalyst/bootstrap/api/internal/handler/organization_gitops.go
// (orgTenantBPOpenClaw / orgTenantBPStalwart) — directly from the generic
// generator. The HRs are HOST files (helm-controller runs on the host, NOT
// inside a per-Org vCluster — #3055), tier-aware via the same HR-level
// kubeConfig the bp-cnpg-pair path uses (generateCNPGPair): vcluster tier →
// install INTO the Org vCluster via the `tenant-<slug>-kubeconfig` mirror;
// host tier → install straight into the host `<slug>` ns. Each HR ships its
// own bp-* HelmRepository (flux-system) so a fresh funnel Org resolves the
// sourceRef without the org-controller having to seed it.
//
// DIVERGENCE FROM THE BSS DOOR (intentional): the generic funnel path does NOT
// emit a per-tenant bp-keycloak (the BSS door's Step-6 does). So these HRs
// carry NO `dependsOn: bp-keycloak` — a dependsOn on a never-rendered HR would
// wedge the release in `DependencyNotReady` forever. The OIDC values still
// point at the conventional per-tenant Keycloak host (keycloak.<slug>.<parent>)
// so that when the live walk wires SSO the issuer matches; until then the
// charts render with their built-in placeholder OIDC values and start. This is
// the same "plain version in the generic path" stance as wordpress (the
// generic path installs `wordpress:6-apache`, not the SSO-pre-wired
// bp-wordpress-tenant the BSS door installs).

// helmReleaseAppSlugs is the set of catalog app slugs the generic generator
// renders as a HelmRelease (via generateHelmReleaseApp) instead of a raw
// Deployment. Keyed by the catalog seedApp slug (NOT the bp-* chart name).
var helmReleaseAppSlugs = map[string]bool{
	"openclaw":      true, // #4272 — bp-openclaw HelmRelease overlay
	"stalwart-mail": true, // #4307 — bp-stalwart-tenant HelmRelease overlay
}

// isHelmReleaseApp reports whether the catalog slug is rendered as a
// HelmRelease (host file) rather than a synced in-vcluster Deployment. The
// generic generator uses it to route the slug to generateHelmReleaseApp and to
// keep it out of generateHostIngress (these apps carry their OWN chart-emitted
// HTTPRoute, parented to cilium-gateway-console, so the traefik host ingress
// must not also try to route them).
func isHelmReleaseApp(slug string) bool {
	return helmReleaseAppSlugs[slug]
}

// hasHelmReleaseApp reports whether any slug in the cart renders as a
// HelmRelease — used to decide whether the per-Org gitops tree needs the
// shared ghcr-pull / source plumbing.
func hasHelmReleaseApp(appSlugs []string) bool {
	for _, a := range appSlugs {
		if isHelmReleaseApp(a) {
			return true
		}
	}
	return false
}

// helmReleaseAppOpts carries the per-Org context the openclaw/stalwart HR
// templates interpolate. slug is the Organization subdomain (== host ns).
type helmReleaseAppOpts struct {
	slug         string // Org subdomain == host namespace
	parentDomain string // org-pool parent zone (e.g. omani.homes)
	adminEmail   string // operator/admin email for stalwart
	// kubeSecret, when non-empty, is the flux-system Secret holding the Org
	// vCluster kubeconfig (`tenant-<slug>-kubeconfig`). Set on the vcluster
	// tier so the host helm-controller installs the chart INTO the vcluster;
	// empty on the host tier (install straight into the host `<slug>` ns).
	kubeSecret string
	// sharedRealmIssuer, when non-empty, is the resolvable SHARED-realm OIDC
	// issuer (`https://auth.<sovereign-fqdn>/realms/sovereign`) the HR templates
	// stamp instead of the per-Org `keycloak.<slug>.<parent>` realm host. On a
	// Sovereign whose per-Org Keycloak realm is DISABLED (the default —
	// CATALYST_PER_ORG_REALM_ENABLED=false) the per-Org host is NXDOMAIN, so
	// openclaw's controller /readyz (which fetches the issuer's JWKS) hangs at
	// 503 forever (#4272). The shared realm at auth.<fqdn> is the SAME issuer
	// the console resolves, so its discovery + JWKS endpoints are live. Empty
	// falls back to the per-Org realm host (legacy / Catalyst-Zero / a cluster
	// that DOES run per-Org realms).
	sharedRealmIssuer string
}

// generateHelmReleaseApp renders the HelmRepository + HelmRelease pair for a
// HelmRelease-shaped catalog app. Returns "" for a slug that is not a
// HelmRelease app (defence in depth — callers gate on isHelmReleaseApp first).
func generateHelmReleaseApp(appSlug string, opt helmReleaseAppOpts) string {
	switch appSlug {
	case "openclaw":
		return generateOpenClawHR(opt)
	case "stalwart-mail":
		return generateStalwartHR(opt)
	default:
		return ""
	}
}

// kubeConfigBlock renders the HR-level spec.kubeConfig the host helm-controller
// uses to install INTO the Org vCluster (vcluster tier). Empty kubeSecret →
// empty block (host tier installs into the host ns). Mirrors generateCNPGPair.
func (opt helmReleaseAppOpts) kubeConfigBlock() string {
	if strings.TrimSpace(opt.kubeSecret) == "" {
		return ""
	}
	return fmt.Sprintf(`
  kubeConfig:
    secretRef:
      name: %s
      key: config`, opt.kubeSecret)
}

// helmRepoBlock renders the shared bp-* OCI HelmRepository the HR sourceRefs.
// Every HR-shaped app ships its own so a fresh funnel Org resolves the
// sourceRef without the org-controller seeding it (the BSS door seeds these in
// orgTenantSharedHelmRepositories; the funnel path has no such shared block).
func helmRepoBlock(name string) string {
	return fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: %s
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull`, name)
}

// generateOpenClawHR mirrors orgTenantBPOpenClaw (#4272) for the generic
// funnel path: the per-Org OpenClaw controller, SSO-wired to the per-tenant
// Keycloak realm + NewAPI gateway, exposed via the dedicated console Gateway.
func generateOpenClawHR(opt helmReleaseAppOpts) string {
	host := fmt.Sprintf("openclaw.%s.%s", opt.slug, opt.parentDomain)
	// #4272: the OIDC issuer openclaw's controller /readyz validates the JWKS
	// against. On a Sovereign whose per-Org realm is DISABLED the per-Org
	// `keycloak.<slug>.<parent>` host is NXDOMAIN → the JWKS fetch fails →
	// /readyz 503 forever → openclaw.<slug>/ serves public 503. When the
	// generator resolves a shared-realm issuer (auth.<fqdn>/realms/sovereign —
	// the SAME live issuer the console uses), stamp THAT so JWKS resolves and
	// /readyz returns 200. Empty falls back to the per-Org realm host (legacy /
	// Catalyst-Zero / a cluster that genuinely runs per-Org realms).
	keycloakRealm := strings.TrimSpace(opt.sharedRealmIssuer)
	if keycloakRealm == "" {
		keycloakRealm = fmt.Sprintf("https://keycloak.%s.%s/realms/org-%s", opt.slug, opt.parentDomain, opt.slug)
	}
	newapiBase := fmt.Sprintf("https://api.%s.%s/v1", opt.slug, opt.parentDomain)
	return fmt.Sprintf(`# bp-openclaw (#4272) — per-Org workspace controller rendered by the generic
# funnel generator. Mirrors the BSS-door orgTenantBPOpenClaw overlay so a cart
# Org gets the SAME HelmRelease the BSS door emits. The generic funnel path
# emits NO per-tenant bp-keycloak, so there is NO dependsOn here (a dependsOn on
# a never-rendered HR would wedge the release forever); the OIDC values still
# point at the conventional per-tenant Keycloak realm so SSO matches once the
# realm exists. Public exposure rides the dedicated console Gateway (#4054).
%s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-openclaw
  namespace: %s
  labels:
    catalyst.openova.io/app: openclaw
    openova.io/category: customer-facing-capability
spec:
  interval: 10m
  releaseName: openclaw
  targetNamespace: %s%s
  chart:
    spec:
      chart: bp-openclaw
      version: "*"
      sourceRef:
        kind: HelmRepository
        name: bp-openclaw
        namespace: flux-system
  install:
    timeout: 15m
    remediation:
      retries: 3
  upgrade:
    timeout: 15m
    remediation:
      retries: 3
  values:
    oidc:
      issuerURL: %s
      clientId: openclaw
      clientSecret:
        name: openclaw-oidc-client-secret
        key: OIDC_CLIENT_SECRET
    llm:
      baseURL: %s
      apiKey:
        name: openclaw-newapi-controller-token
        key: NEWAPI_KEY
      defaultModel: qwen3.6
    keycloak:
      realmURL: %s
      clientID: openclaw
      clientSecretName: openclaw-oidc-client-secret
    newapi:
      baseURL: %s
    tenant:
      namespace: %s
    # Cilium Gateway API exposure (#4272) — a Sovereign runs the Cilium
    # Gateway, NOT traefik; the chart's networking.k8s.io/v1 Ingress is inert
    # here. Attach the controller host to the DEDICATED console Gateway whose
    # *.<pool> wildcard TLS listener terminates TLS (no per-host cert),
    # mirroring bp-agenity (#4180).
    ingress:
      enabled: false
    httpRoute:
      enabled: true
      hostnames:
        - %s
      parentRef:
        name: cilium-gateway-console
        namespace: kube-system
    networkPolicy:
      enabled: true
      ingress:
        allowGatewayEntity: true
`, helmRepoBlock("bp-openclaw"), opt.slug, opt.slug, opt.kubeConfigBlock(),
		keycloakRealm, newapiBase, keycloakRealm, newapiBase, opt.slug, host)
}

// generateStalwartHR mirrors orgTenantBPStalwart (#4307) for the generic
// funnel path: the per-Org Stalwart mail server, SSO-wired to the per-tenant
// Keycloak realm, exposed via the dedicated console Gateway. disableWait +
// admin.externalSecret:false carry the #4307/#4246 live-converged settings.
func generateStalwartHR(opt helmReleaseAppOpts) string {
	mailHost := fmt.Sprintf("mail.%s.%s", opt.slug, opt.parentDomain)
	primaryDomain := fmt.Sprintf("%s.%s", opt.slug, opt.parentDomain)
	// #4272/#4307: same resolvable-issuer rule as openclaw — on a per-Org-realm-
	// disabled Sovereign the per-Org keycloak.<slug>.<parent> host is NXDOMAIN,
	// so point SSO at the shared realm (auth.<fqdn>/realms/sovereign) when the
	// generator resolved one; else fall back to the per-Org realm host.
	keycloakRealm := strings.TrimSpace(opt.sharedRealmIssuer)
	if keycloakRealm == "" {
		keycloakRealm = fmt.Sprintf("https://keycloak.%s.%s/realms/org-%s", opt.slug, opt.parentDomain, opt.slug)
	}
	adminEmail := strings.TrimSpace(opt.adminEmail)
	if adminEmail == "" {
		adminEmail = fmt.Sprintf("admin@%s", primaryDomain)
	}
	return fmt.Sprintf(`# bp-stalwart-tenant (#4307) — per-Org mail server rendered by the generic
# funnel generator. Mirrors the BSS-door orgTenantBPStalwart overlay. NO
# dependsOn on bp-keycloak (the generic funnel path emits none — a dependsOn on
# a never-rendered HR would wedge the release); the OIDC values point at the
# conventional per-tenant Keycloak realm so SSO matches once it exists.
# disableWait (#4307): the StatefulSet mounts stalwart-tls NON-optionally and
# blocks at ContainerCreating until cert-manager issues the mail.<host> leaf;
# without disableWait the install burns its 15m budget before the OIDC setup
# Job (a Helm hook) ever runs. admin.externalSecret:false (#4246): nothing
# seeds OpenBao at the admin path on a fresh per-Org install, so let the chart
# auto-provision a persistent random ADMIN_PASSWORD.
%s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-stalwart-tenant
  namespace: %s
  labels:
    catalyst.openova.io/app: stalwart-mail
    openova.io/category: customer-facing-capability
spec:
  interval: 10m
  releaseName: stalwart-tenant
  targetNamespace: %s%s
  chart:
    spec:
      chart: bp-stalwart-tenant
      version: "*"
      sourceRef:
        kind: HelmRepository
        name: bp-stalwart-tenant
        namespace: flux-system
  install:
    timeout: 15m
    disableWait: true
    remediation:
      retries: 3
  upgrade:
    timeout: 15m
    cleanupOnFail: true
    disableWait: true
    remediation:
      retries: 3
  values:
    domain:
      primary: %s
      mode: subdomain
    ingress:
      webmail:
        host: %s
        parentRef:
          name: cilium-gateway-console
          namespace: kube-system
        tls:
          enabled: true
          issuer: letsencrypt-prod
    adminEmail: %s
    keycloak:
      realmURL: %s
      clientID: stalwart
      clientSecretName: stalwart-oidc-client-secret
    admin:
      externalSecret:
        enabled: false
    mailboxProvisioner:
      setupJob:
        enabled: true
`, helmRepoBlock("bp-stalwart-tenant"), opt.slug, opt.slug, opt.kubeConfigBlock(),
		primaryDomain, mailHost, adminEmail, keycloakRealm)
}

// sortedHelmReleaseApps returns the HelmRelease-shaped slugs from appSlugs in a
// stable order so the generated file set is deterministic across regenerate
// (Go map/slice iteration order would otherwise churn the gitops diff).
func sortedHelmReleaseApps(appSlugs []string) []string {
	out := make([]string, 0, len(appSlugs))
	for _, a := range appSlugs {
		if isHelmReleaseApp(a) {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
