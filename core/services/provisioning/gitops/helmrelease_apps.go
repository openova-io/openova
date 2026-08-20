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
	"agenity":       true, // #6352 — bp-agenity HelmRelease (DoD Pillar 4 per-Org Agenity workspace; funnel parity with the BSS-door orgTenantBPAgenity)
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

// impliedHelmReleaseApps maps a cart slug → the HR-shaped slugs whose ABSENCE
// leaves the bought app pointing at a host nothing serves (UAT row 225).
//
// openclaw → newapi is the case that motivated this, and it is not a taste
// call: generateOpenClawHR STAMPS `llm.baseURL` and `newapi.baseURL` as
// `https://api.<slug>.<parent>/v1`, and that host is EXACTLY the HTTPRoute
// hostname generateNewAPIHR gives bp-newapi (`api.<slug>.<parent>`). So a cart
// holding openclaw but not newapi renders an openclaw whose only LLM backend
// is a hostname this Org never provisions. That is the shape observed on
// hw292: the one customer Org bought wordpress + openclaw + stalwart-mail +
// agenity, a cluster-wide sweep found exactly ONE bp-newapi (the host-level
// flux-system/bp-newapi in ns `newapi`, which openclaw does NOT point at), and
// row 225 had no per-Org bp-newapi to walk at all.
//
// #4739 added `newapi` to helmReleaseAppSlugs FOR "row225 funnel parity with
// the BSS-door orgTenantBPNewAPI". It did not achieve that parity: the BSS
// door emits bp-newapi.yaml for EVERY tenant Org unconditionally (it is a
// fixed entry in orgTenantTemplates and in orgTenantKustomization's resources
// list — products/catalyst/bootstrap/api/internal/handler/organization_gitops.go),
// while the funnel renders it only when the customer separately puts `newapi`
// in the cart. This map closes that divergence at the one place both the file
// set and the index set read from, so the two paths agree on what an openclaw
// Org contains.
//
// Deliberately NOT a blanket "install everything": only a slug whose own
// rendered values name another slug's host belongs here. Adding an entry means
// asserting that dangling-host claim about the templates.
var impliedHelmReleaseApps = map[string][]string{
	"openclaw": {"newapi"},
}

// helmReleaseAppsFor returns the HelmRelease-shaped slugs a cart renders, in a
// stable order (Go map/slice iteration order would otherwise churn the gitops
// diff), with the impliedHelmReleaseApps closure applied.
//
// This is the SINGLE enumeration both the file set (GenerateAllWithAppConfigs)
// and the index set (PerOrgHostHelmReleaseAppDocs) call. Keeping them on one
// function is the point: an implied app rendered into the tree but missing
// from the kustomization index is an unapplied file, and an index entry with
// no file breaks the whole kustomize build (the #4567 failure mode).
func helmReleaseAppsFor(appSlugs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(appSlugs)+1)
	add := func(slug string) {
		if !isHelmReleaseApp(slug) {
			return
		}
		if _, dup := seen[slug]; dup {
			return
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	// Two passes read as "what was bought, then what that implies"; the dedupe
	// in add() plus the sort below are what actually make the result
	// independent of cart order and of pass order, so an Org that buys BOTH
	// openclaw and newapi yields one entry either way.
	for _, a := range appSlugs {
		add(a)
	}
	for _, a := range appSlugs {
		for _, implied := range impliedHelmReleaseApps[a] {
			add(implied)
		}
	}
	sort.Strings(out)
	return out
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
	case "agenity":
		out = generateAgenityHR(opt)
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
    # #6114: no apiKey here. It pinned openclaw-newapi-controller-token, a
    # Secret nothing in the tree creates, for a secretKeyRef the chart no
    # longer emits and the controller binary never read. The end-user token
    # path (per-user newapi-key-{uuid} Secret, ADR-0003 3.3) is unchanged.
    llm:
      baseURL: %s
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
//
// #5987 — THE VALUES BLOCK WAS NOT SPEAKING THE CHART'S LANGUAGE. Three of the
// four keys below are corrections; each was proven by `helm template` against
// platform/newapi/chart (identical chart, command and --api-versions across
// every run, only the values file differing):
//
//   - No `catalystIntegration` at all → the chart defaults it ON with an empty
//     `externalSecret.remoteRef.key`, and templates/external-secret.yaml makes
//     that combination an explicit `{{- fail }}`. Render exited 1. This became
//     urgent when #5969 added `openclaw ⇒ newapi` to impliedHelmReleaseApps:
//     the broken HR stopped being opt-in and now reaches EVERY openclaw Org.
//   - A top-level `oidc.issuerURL` → not a bp-newapi value (the chart reads
//     `auth.adminUI.keycloak.*`). Helm drops unknown values silently, so
//     deployment.yaml's $kcConfigured gate never held: fixing only the `fail`
//     produced a release with NO Deployment.
//   - A top-level `httpRoute:` → not a bp-newapi value either (the chart reads
//     `ingress.httpRoute.*`, and `host:` is a scalar, not `hostnames:`), so no
//     HTTPRoute rendered and `api.<slug>.<parent>` — the exact host
//     generateOpenClawHR stamps into openclaw's llm.baseURL — was served by
//     nothing. That is UAT row 225's dangling-host shape.
//
// bp-openclaw's values.yaml DOES define top-level `oidc`/`llm`/`keycloak`/
// `newapi`/`tenant`/`httpRoute`, so generateOpenClawHR does not share this
// gap — verified by rendering its funnel values against platform/openclaw/chart
// (exit 0, Deployment + HTTPRoute on openclaw.<slug>.<parent>).
func generateNewAPIHR(opt helmReleaseAppOpts) string {
	host := fmt.Sprintf("api.%s.%s", opt.slug, opt.parentDomain)
	primaryDomain := fmt.Sprintf("%s.%s", opt.slug, opt.parentDomain)
	keycloakRealm := strings.TrimSpace(opt.sharedRealmIssuer)
	if keycloakRealm == "" {
		keycloakRealm = fmt.Sprintf("https://keycloak.%s.%s/realms/org-%s", opt.slug, opt.parentDomain, opt.slug)
	}
	return fmt.Sprintf(`# bp-newapi (#4739, #5987) — per-Org NewAPI LLM gateway rendered by the generic
# funnel generator. openclaw's llm.baseURL (api.<slug>.<parent>/v1) routes here.
# Mirrors the BSS-door orgTenantBPNewAPI (#945) but DROPS the dependsOn:
# bp-keycloak the generic funnel never renders (a dependsOn on a never-rendered
# HR wedges the release forever — the same divergence as bp-openclaw). Chart
# owns its CNPG.
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
    #
    # ── Ops-staff admin UI OIDC (#5987) ──────────────────────────────────
    # The chart key is auth.adminUI.keycloak.* — NOT a top-level oidc.issuerURL
    # (that key does not exist in bp-newapi's values.yaml, so Helm dropped it
    # and deployment.yaml's $kcConfigured gate — mode==keycloak AND issuer AND
    # existingSecret — left the release with no Deployment at all).
    # existingSecret is materialised BY THE CHART
    # (templates/keycloak-client-secret.yaml: lookup-or-randAlphaNum, policy
    # keep), so naming it here is self-provisioning, not a dangling reference.
    # ssoBridgeSync stays OFF, exactly as the BSS door does it (#4169): a
    # per-Org AppRegistration for clientId newapi-admin PUT-overwrites the
    # Sovereign-level newapi-admin client's redirectUris, breaking SSO on
    # newapi.<sovereign-fqdn>.
    auth:
      adminUI:
        mode: keycloak
        keycloak:
          issuer: %s
          clientId: newapi-admin
          callbackPath: /oauth/callback
          existingSecret: newapi-oidc-client-secret
          ssoBridgeSync:
            enabled: false
      customerAPI:
        keyIssuer: catalyst
    # Valkey OFF (#3858 root cause #3, same as the BSS door): the chart default
    # valkey.url is the host-placed valkey synced from the rtz vCluster, which a
    # per-Org install (vcluster tier especially) cannot reach. NewAPI treats
    # REDIS_CONN_STRING as required once set and CrashLoops on the Redis ping;
    # with valkey off it falls back to an in-process cache and Postgres still
    # holds every piece of durable state.
    valkey:
      enabled: false
    # ── CPU sized to the Org boundary, not to a Sovereign (#6114, UAT row 232) ──
    # bp-newapi's chart default is requests.cpu 100m / limits.cpu **2**
    # (platform/newapi/chart/values.yaml:150-153). That is defensible for the
    # bootstrap-kit slot-80 SOVEREIGN install, which runs in no ResourceQuota.
    # Inside an Org it is fatal: plan "s" — the default for an empty or unknown
    # plan slug — grants limits.cpu "2" TOTAL
    # (core/controllers/organization/internal/gitops/manifests.go:121, rendered
    # as hard["limits.cpu"] at :499), and a ResourceQuota counts LIMITS. So this
    # one container reserved the whole Org cap and every pod rendered beside it
    # was refused at admission — the openclaw controller (250m) and this
    # release's own CNPG (500m) included. A User saw only an opaque Helm
    # "context deadline exceeded".
    #
    # requests == limits deliberately: the boundary sizes on limits, so a
    # request far below the limit would let this pod in and then starve its
    # neighbours out of the quota it is not accounted against.
    #
    # ── WHAT THIS BLOCK DOES *NOT* SIZE (#6324) ─────────────────────────────
    # It pins ONE of the THREE quota-counted containers in this release's Pod,
    # and a ResourceQuota admits the POD:
    #
    #   sandbox-bridge    200m  chart default; a NATIVE sidecar (initContainers
    #                           entry with restartPolicy: Always, #3374), so its
    #                           limits count toward the Pod on k8s 1.29+
    #   newapi            500m  pinned below
    #   metering-sidecar  500m  chart default
    #   ------------------------
    #   POD limits.cpu   1200m
    #
    # So the Org bundle is 1200m + openclaw 250m + CNPG 500m = 1950m of the
    # 2000m smallest-plan cap: 50m of real headroom, not the 750m the
    # single-container reading suggests. Raising EITHER sidecar by 100m puts the
    # Org over the cap. The two guards now compute this Pod-level total from the
    # chart rather than from the one term pinned here — see
    # newapi_pod_quota_arithmetic_6324_test.go. Sizing the Pod itself is a
    # plan-capacity decision and belongs to #5393; nothing here changes a value.
    #
    # Overridden HERE rather than in the chart so the Sovereign-level default is
    # untouched — no chart bump, no five-site lockstep.
    newapi:
      resources:
        requests:
          cpu: 500m
          memory: 256Mi
        limits:
          cpu: 500m
          memory: 1Gi
    # ── catalystIntegration OFF — deliberate, do NOT copy the BSS door here ──
    # #5987/#4477/#5375. This block exists to hand catalyst-api a bearer for the
    # SOVEREIGN's NewAPI (unified-rbac POSTs to newapi.newapi.svc), and it is
    # singular by construction: catalyst-api's seedNewapiAdminToken seam writes
    # ONE cluster-shared OpenBao path, the external-secrets-push policy grants
    # create/update on exactly that one key, and the rendered Secret carries
    # reflector annotations that auto-mirror it into catalyst-system, where the
    # Sovereign's own copy already lives.
    #
    # Enabling it on a per-Org install would therefore be actively destructive,
    # not merely redundant: the chart's companion PushSecret (default enabled,
    # updatePolicy Replace) would overwrite that shared path with THIS Org's own
    # random token-signing-key ADMIN_SECRET, 401-ing per-user key issuance for
    # the whole Sovereign, while two ExternalSecrets fought over one mirrored
    # Secret name in catalyst-system. Nothing inside the per-Org release
    # consumes the Secret, so off is both safe and complete.
    catalystIntegration:
      enabled: false
    ingress:
      # The traefik Ingress the BSS door renders is inert on a Sovereign; public
      # exposure rides the dedicated console Gateway, same as bp-openclaw.
      enabled: false
      # #5987 — the chart key is ingress.httpRoute.* with a SCALAR host; a
      # top-level httpRoute: block with hostnames: [] is not a bp-newapi value
      # and rendered no route, leaving openclaw's llm.baseURL pointing at a
      # hostname this Org served from nowhere (UAT row 225).
      httpRoute:
        enabled: true
        host: %s
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


// DefaultHRAppChartVersions is the built-in CATALYST_HR_APP_CHART_VERSIONS
// value — the chart version every HelmRelease-shaped funnel app installs at
// when the operator sets no env override, which on every Sovereign we run is
// ALWAYS (nothing in this repo sets that env).
//
// # The invariant (UAT row 234, Refs #4307)
//
// It MUST equal the catalog-seed delivery pin for the same chart. That was
// already the stated contract at the call site in main.go ("Defaults = the
// current catalog-seed pins") and nothing enforced it, so it silently rotted:
// the funnel installed bp-stalwart-tenant 0.1.13 while the seed served 0.1.15,
// and the intervening 0.1.14 is the §854 nodePort fix WITHOUT WHICH KYVERNO
// DENIES THE HELMRELEASE AT ADMISSION — no pod, no HTTPRoute, and
// `mail.<slug>.<pool-tld>` answers nothing. bp-openclaw was five versions
// behind (0.2.13 vs 0.2.18) for the same reason.
//
// A stale pin here is invisible in every other gate: the chart renders, the
// seed renders, the bootstrap-kit renders, and the two numbers simply differ.
// hrAppPinSeedDrift in helmrelease_apps_pin_seed_drift_test.go is the guard
// that makes the drift fail a PR instead of a walk.
//
// Living here rather than as a literal in main.go is what makes the guard test
// the value main.go actually ships — a copy in the test would pass while the
// binary shipped something else.
// 2026-08-14: openclaw 0.2.18 -> 0.2.19. f7961510f bumped platform/openclaw/
// chart/Chart.yaml and the catalog seed to 0.2.19 and did not bump this pin, so
// main went red on hrAppPinSeedDrift. The guard was already correct; what let
// the drift LAND is that its workflow was path-scoped to
// core/services/provisioning/** while the value it asserts on lives in
// products/catalyst/chart/templates/catalog-seed/ — the guard never ran on the
// commit that broke it. Those paths are now in the trigger (#6324).
// 2026-08-19: stalwart-mail 0.1.15 -> 0.1.16. #6489 bumped platform/stalwart-tenant
// chart/Chart.yaml + the catalog seed to 0.1.16 (self-signed TLS fallback in
// CRD-less vclusters) and did not bump this funnel pin, so main went red on
// hrAppPinSeedDrift — a purchase would install 0.1.15 while the seed served 0.1.16.
// 2026-08-20: agenity 0.5.28 -> 0.5.31. #6317 bumped products/agenity/chart +
// the catalog seed to 0.5.31 (server-side Anthropic OAuth credential renewal +
// resync sidecar); this funnel pin moves in lockstep so a purchase installs the
// renewing chart, not the one whose credential dies every ~5h.
const DefaultHRAppChartVersions = "openclaw=0.2.19,stalwart-mail=0.1.16,newapi=1.4.153,agenity=0.5.31"

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

// sovereignHostFromRealmIssuer extracts `<sovereign-fqdn>` from a shared-realm
// issuer of the form `https://auth.<sovereign-fqdn>/realms/<realm>`. Returns ""
// when the issuer is empty or not in that shape, so callers fall back rather
// than stamp a malformed host.
//
// This exists because the Sovereign FQDN is NOT a field on helmReleaseAppOpts:
// the funnel generator is per-Org and carries the Org zone (slug+parentDomain).
// sharedRealmIssuer is the one opt that already names the Sovereign, and the
// browser-facing OIDC issuer must be the SOVEREIGN host, never the Org zone.
func sovereignHostFromRealmIssuer(issuer string) string {
	iss := strings.TrimSpace(issuer)
	if iss == "" {
		return ""
	}
	iss = strings.TrimPrefix(strings.TrimPrefix(iss, "https://"), "http://")
	host, _, found := strings.Cut(iss, "/")
	if !found || !strings.HasPrefix(host, "auth.") {
		return ""
	}
	return strings.TrimPrefix(host, "auth.")
}

// generateAgenityHR mirrors the BSS-door orgTenantBPAgenity
// (products/catalyst/bootstrap/api/internal/handler/organization_gitops.go)
// for the generic funnel path — DoD Pillar 4, the per-Organization Agenity
// workspace served at https://agenity.<slug>.<parent>/app/.
//
// WHY THIS EXISTS (#6352): agenity had NO render path in this generator, so
// helmReleaseAppSlugs never claimed it, DeployableAppSlugs() flagged it
// Deployable=false, the marketplace drew "COMING SOON" and funnel
// cart-placement skipped it. Measured live on hw298: a real funnel Organization
// (`chepherd`, tenantPublic omani.rest) converged Ready=True with its per-Org
// Kustomizations green — and agenity inventory entries across ALL SEVEN org
// Kustomizations were ZERO. The legacy BSS overlay is the only emitter of
// bp-agenity in the repo (organization_gitops.go's own #5425 header says so),
// and it is gated off for per-Org-GitOps Sovereigns. Exactly the #4272/#4307
// gap shape, for the Pillar-4 app.
//
// NO dependsOn — deliberate, and the same reasoning the BSS-door template
// records: the dashboard SPA renders independent of keycloak and cnpg, and a
// dependsOn on a never-rendered bp-keycloak wedges the release in
// DependencyNotReady forever (the divergence this file's header describes).
//
// oidcGate.issuerHost is the SOVEREIGN host, not the Org zone (#6314). The
// browser-facing issuer is auth.<sovereign-fqdn>; the Org zone would render
// auth.<slug>.<parent>, a host with no HTTPRoute — envoy 404 at sign-in. That
// bug was fixed in the chart (bp-agenity 0.5.28, oidcGate.issuerHost) and this
// generator carries the fix forward rather than reintroducing it. Falls back to
// omitting the key when the Sovereign host is not derivable, which restores the
// chart default (issuerHost | default .Values.sovereignFqdn).
func generateAgenityHR(opt helmReleaseAppOpts) string {
	host := fmt.Sprintf("agenity.%s.%s", opt.slug, opt.parentDomain)
	orgZone := fmt.Sprintf("%s.%s", opt.slug, opt.parentDomain)
	issuerHostLine := ""
	if sov := sovereignHostFromRealmIssuer(opt.sharedRealmIssuer); sov != "" {
		issuerHostLine = fmt.Sprintf("\n      issuerHost: %s", sov)
	}
	return fmt.Sprintf(`# bp-agenity (#4180, #6352) — the per-Organization agentic dashboard, rendered
# by the generic funnel generator. Mirrors the BSS-door orgTenantBPAgenity; see
# generateAgenityHR for why there is no dependsOn and why issuerHost is the
# Sovereign host.
%s
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-agenity
  namespace: %s
  labels:
    catalyst.openova.io/app: agenity
    openova.io/category: customer-facing-capability
spec:
  interval: 10m
  releaseName: agenity
  targetNamespace: %s%s
  chart:
    spec:
      chart: bp-agenity
      version: "*"
      sourceRef:
        kind: HelmRepository
        name: bp-agenity
  install:
    timeout: 15m
    remediation:
      retries: 3
  upgrade:
    timeout: 15m
    cleanupOnFail: true
    remediation:
      retries: 3
  values:
    # Per-Org identity zone — openova-MCP derives https://console.<fqdn> from it.
    sovereignFqdn: %s
    oidcGate:
      enabled: true
      clientId: agenity-%s%s
    httpRoute:
      enabled: true
      hostnames:
        - %s
      parentRefs:
        - name: cilium-gateway-console
          namespace: kube-system
    networkPolicy:
      ingress:
        allowGatewayEntity: true
    # ── #6372 — the credential the solo agent cannot start without ──────────
    # MEASURED on hw299: bp-agenity-0 sat Pending indefinitely on its
    # seed-claude-creds init container, which waits up to
    # credentialWait.timeoutSeconds for /creds/<credentialsKey> to be
    # non-empty and then fails by design (#6163). The Secret behind that
    # mount is declared optional:true, so the pod schedules, the mount stays
    # empty, and the workspace never comes up.
    #
    # This generator emitted NO anthropic block at all, so nothing ever
    # materialised the Secret on the FUNNEL path. The BSS door has always
    # shipped it (organization_gitops.go orgTenantBPAgenity) — the two doors
    # simply disagreed, which is why a funnel-born Org could never run the
    # agent even though a BSS-born Org was wired for it.
    #
    # Mirrored verbatim from the BSS door so the doors cannot drift again.
    # The chart's externalsecret-anthropic.yaml reads
    # secret/catalyst/anthropic/token — the path the catalyst-api producer
    # (seedAnthropicToken, #4277) writes at Org-create. It MUST stay under
    # the catalyst/ prefix: that is the only KV sub-tree a Sovereign can
    # WRITE (catalyst-api-write policy); the external-secrets role used by
    # vault-region1 is read-only. The path is cluster-shared — one seed
    # serves every Org's agenity install.
    anthropic:
      externalSecret:
        enabled: true
        secretStoreRef: vault-region1
        secretStoreKind: ClusterSecretStore
        remoteKey: catalyst/anthropic/token
        remoteProperty: apiKey
        remoteCredentialsProperty: credentialsJson
`, helmRepoBlock("bp-agenity"), opt.slug, opt.slug, opt.kubeConfigBlock(),
		orgZone, opt.slug, issuerHostLine, host)
}
