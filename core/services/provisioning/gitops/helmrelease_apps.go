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
	"newapi":        true, // #4739 — bp-newapi HelmRelease (openclaw's LLM gateway + row225 funnel parity with the BSS-door orgTenantBPNewAPI)
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
	// chartVersion, when non-empty, pins the HR's chart version instead of
	// the floating `version: "*"` (#4706 — see pinHRChartVersion). Populated
	// per-app from ManifestGenerator.HelmReleaseAppVersions.
	chartVersion string
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
	var out string
	switch appSlug {
	case "openclaw":
		out = generateOpenClawHR(opt)
	case "stalwart-mail":
		out = generateStalwartHR(opt)
	case "newapi":
		out = generateNewAPIHR(opt)
	default:
		return ""
	}
	return pinHRChartVersion(out, opt.chartVersion)
}

// pinHRChartVersion replaces the HR template's floating `version: "*"` with
// the pinned chart version when one is configured (#4706). A floating "*"
// resolves to the HIGHEST semver tag on the OCI repo — which is how ONE
// mis-tagged artifact (the bp-agenity container image pushed to the CHART
// repo as :0.9.7) broke every Org install at once, and how an org app could
// silently jump a major. Empty version keeps "*" (the historical behaviour)
// so a missing pin degrades to today's shape, never to a broken render —
// the pin is a hardening, not a new hard dependency. Single choke-point on
// the rendered YAML so every current and future HR-shaped app template is
// covered without per-template printf surgery.
func pinHRChartVersion(yaml, version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return yaml
	}
	return strings.Replace(yaml, `version: "*"`, fmt.Sprintf("version: %q", v), 1)
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
//
// The URL is cutover-aware (#5527, see cutover_aware_5527.go): pre-cutover it
// declares the canonical public catalog (oci://ghcr.io/openova-io); once the
// step-07 registry-pivot fact is stamped on this Deployment it declares the
// Sovereign-local Harbor — the SAME value step-06 patches the live objects
// to, so the generated source and the pivoted objects agree and Flux has
// nothing to drift-correct back to ghcr (the hw291 step-08 OFFENDER wedge).
// secretRef stays ghcr-pull in both phases (the cutover rewrites that
// Secret's contents, not its name).
func helmRepoBlock(name string) string {
	return fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: %s
  namespace: flux-system
spec:
  type: oci
  interval: 15m
  url: %s
  secretRef:
    name: ghcr-pull`, name, catalogOCIBase())
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
	// #4739/#4803/#4804: the IN-CLUSTER issuer base openclaw fetches the JWKS
	// from — http://keycloak.keycloak.svc.cluster.local + the realm path — so a
	// vcluster-hosted controller resolves the JWKS INTERNALLY (the host keycloak
	// Service is mirrored into the vcluster by #4804's sync.fromHost.services)
	// instead of the public issuer's EXTERNAL jwks_uri (the NAT-EIP hairpin that
	// 503s /readyz on kom4dc). The iss claim is still validated against the
	// PUBLIC keycloakRealm above. Empty (an issuer with no /realms/ path) leaves
	// it "" → openclaw falls back to public discovery (the chart's {{- with }}
	// omits the env for an empty value), so this is a safe no-op off-kom4dc.
	internalRealm := ""
	if idx := strings.Index(keycloakRealm, "/realms/"); idx != -1 {
		internalRealm = "http://keycloak.keycloak.svc.cluster.local" + keycloakRealm[idx:]
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
      internalIssuerURL: %s
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
      # #4272 (the TITULAR egress gap): the controller's /readyz fetches the
      # OIDC issuer's JWKS at the PUBLIC host (auth.<fqdn>, #4399), which
      # hairpins out to the console-ELB EIP and back through the Cilium Gateway
      # on :443. Once the companion CNP attaches an egress block (kube-apiserver
      # entity), Cilium makes the controller egress CNP-enforced and an ipBlock
      # 0.0.0.0/0 rule no longer matches the reserved identities the hairpin
      # carries → JWKS egress-denied → /readyz 503 → controller 0/1 forever.
      # allowPublicHttps emits the CNP toEntities :443 carve-out (world+cluster+
      # host+remote-node). The chart default is already true; stamp it
      # explicitly so a funnel openclaw is correct independent of any future
      # chart-default flip.
      egress:
        allowApiserverEntity: true
        allowPublicHttps: true
`, helmRepoBlock("bp-openclaw"), opt.slug, opt.slug, opt.kubeConfigBlock(),
		keycloakRealm, internalRealm, newapiBase, keycloakRealm, newapiBase, opt.slug, host)
}

// generateNewAPIHR renders the per-Org bp-newapi HelmRelease for the generic
// funnel path (#4739) — the LLM gateway openclaw's llm.baseURL points at
// (api.<slug>.<parent>/v1) AND a standalone per-Org NewAPI (UAT row225). Mirrors
// the BSS-door orgTenantBPNewAPI (#945) but with the SAME funnel divergence as
// generateOpenClawHR: NO dependsOn: bp-keycloak (the generic funnel emits no
// per-tenant bp-keycloak — a dependsOn on a never-rendered HR wedges the release
// in DependencyNotReady forever). The chart OWNS its Postgres (cnpg.enabled
// default) + PATCHes the DSN via its own post-install hook, so disableWait:true
// lets the release reach hook execution (the #4246 deadlock fix). Tier-aware
// kubeConfig like the other HR apps (vcluster tier installs INTO the vcluster).
func generateNewAPIHR(opt helmReleaseAppOpts) string {
	host := fmt.Sprintf("api.%s.%s", opt.slug, opt.parentDomain)
	primaryDomain := fmt.Sprintf("%s.%s", opt.slug, opt.parentDomain)
	keycloakRealm := strings.TrimSpace(opt.sharedRealmIssuer)
	if keycloakRealm == "" {
		keycloakRealm = fmt.Sprintf("https://keycloak.%s.%s/realms/org-%s", opt.slug, opt.parentDomain, opt.slug)
	}
	return fmt.Sprintf(`# bp-newapi (#4739) — per-Org NewAPI LLM gateway rendered by the generic funnel
# generator. openclaw's llm.baseURL (api.<slug>.<parent>/v1) routes here. Mirrors
# the BSS-door orgTenantBPNewAPI (#945) but DROPS the dependsOn: bp-keycloak the
# generic funnel never renders (a dependsOn on a never-rendered HR wedges the
# release forever — the same divergence as bp-openclaw). Chart owns its CNPG.
%s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-newapi
  namespace: %s
  labels:
    catalyst.openova.io/app: newapi
    openova.io/category: customer-facing-capability
spec:
  interval: 10m
  releaseName: newapi
  targetNamespace: %s%s
  chart:
    spec:
      chart: bp-newapi
      version: "*"
      sourceRef:
        kind: HelmRepository
        name: bp-newapi
        namespace: flux-system
  # #4246 — disableWait: the Deployment gates on a non-empty SQL_DSN via a
  # wait-for-sql-dsn initContainer PATCHed by the chart's post-install db-dsn-
  # sync hook. With wait enabled Helm blocks on Ready BEFORE hooks run → the
  # init never unblocks → hard deadlock. disableWait lets the DSN sync + the
  # Pod converge.
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
    # Per-tenant identity zone — tenant-unique OpenBao ExternalSecret path.
    sovereignFQDN: %s
    # Chart-owned CNPG Postgres (cnpg.enabled default): the chart renders its
    # own per-Org CNPG Cluster + syncs the canonical DSN via its post-install
    # hook. Do NOT set database.existingSecret (deployment.yaml auto-resolves
    # bp-newapi-newapi-db-dsn).
    oidc:
      issuerURL: %s
    ingress:
      enabled: false
    httpRoute:
      enabled: true
      hostnames:
        - %s
      parentRef:
        name: cilium-gateway-console
        namespace: kube-system
`, helmRepoBlock("bp-newapi"), opt.slug, opt.slug, opt.kubeConfigBlock(),
		primaryDomain, keycloakRealm, host)
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


// ParseHRAppVersions parses the CATALYST_HR_APP_CHART_VERSIONS wire format
// ("slug=version,slug=version") into the HelmReleaseAppVersions map (#4706).
// Malformed entries are skipped rather than fatal — a typo in one pin must
// not take the provisioning service down; the affected app just falls back
// to the floating "*" it always had.
func ParseHRAppVersions(raw string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}
