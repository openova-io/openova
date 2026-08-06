package gitops

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// readIntCfg returns an int value from an opaque configSchema map, with
// validation against (min, max) range and fallback to dflt on any
// mismatch (missing key, wrong type, out-of-range). Logs Warn at the
// drop site so a frontend that ships a stale schema can't tunnel
// arbitrary values past the constraints. appSlug is used purely for
// log context. Used by generatePostgres / generateMySQL to bind the
// canonical replicas / disk_gb / backups_enabled configSchema fields
// from seed.go:699-701 into the rendered manifest (TBD-V27 #2042).
//
// JSON unmarshal of `map[string]any` decodes integers as float64 (Go
// json semantics) — the float64 → int branch handles that without
// requiring the caller to know the in-memory shape.
func readIntCfg(cfg map[string]any, key string, dflt, min, max int, appSlug string) int {
	if cfg == nil {
		return dflt
	}
	raw, ok := cfg[key]
	if !ok {
		return dflt
	}
	var v int
	switch t := raw.(type) {
	case int:
		v = t
	case int32:
		v = int(t)
	case int64:
		v = int(t)
	case float64: // JSON numbers decode here under map[string]any
		v = int(t)
	default:
		slog.Warn("readIntCfg: value has unexpected type — falling back to default",
			"app", appSlug, "key", key, "type", fmt.Sprintf("%T", raw), "default", dflt)
		return dflt
	}
	if v < min || v > max {
		slog.Warn("readIntCfg: value out of configSchema range — falling back to default",
			"app", appSlug, "key", key, "value", v, "min", min, "max", max, "default", dflt)
		return dflt
	}
	return v
}

// readBoolCfg returns a bool value from an opaque configSchema map.
// Same semantics as readIntCfg for missing/mistyped values.
func readBoolCfg(cfg map[string]any, key string, dflt bool, appSlug string) bool {
	if cfg == nil {
		return dflt
	}
	raw, ok := cfg[key]
	if !ok {
		return dflt
	}
	v, ok := raw.(bool)
	if !ok {
		slog.Warn("readBoolCfg: value has unexpected type — falling back to default",
			"app", appSlug, "key", key, "type", fmt.Sprintf("%T", raw), "default", dflt)
		return dflt
	}
	return v
}

// readStringCfg returns a string value from an opaque configSchema
// map. Same semantics as readIntCfg / readBoolCfg for missing/mistyped
// values. Used by the active-hot-standby cnpg-pair path (TBD-V17 #2068)
// to thread `primary_region` / `replica_region` picks from the
// Postgres-backed app's configSchema into the rendered bp-cnpg-pair
// HelmRelease.
func readStringCfg(cfg map[string]any, key, dflt, appSlug string) string {
	if cfg == nil {
		return dflt
	}
	raw, ok := cfg[key]
	if !ok {
		return dflt
	}
	v, ok := raw.(string)
	if !ok {
		slog.Warn("readStringCfg: value has unexpected type — falling back to default",
			"app", appSlug, "key", key, "type", fmt.Sprintf("%T", raw), "default", dflt)
		return dflt
	}
	return v
}

// logUnknownKeys emits a Warn for every key in cfg that is NOT in the
// known configSchema's KnownKeys list. Prevents a stale frontend from
// silently smuggling arbitrary keys (like extra YAML chunks) into the
// rendered manifest while still letting the generator render the
// known-good subset. Keys are sorted for deterministic logging.
func logUnknownKeys(cfg map[string]any, knownKeys []string, appSlug string) {
	if len(cfg) == 0 {
		return
	}
	known := make(map[string]struct{}, len(knownKeys))
	for _, k := range knownKeys {
		known[k] = struct{}{}
	}
	var unknown []string
	for k := range cfg {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return
	}
	sort.Strings(unknown)
	slog.Warn("logUnknownKeys: dropped configSchema keys not in canonical schema",
		"app", appSlug, "unknown_keys", unknown, "known_keys", knownKeys)
}

// ManifestGenerator generates Kubernetes manifests for tenant environments.
// Each tenant gets a real vCluster (not just a namespace).
type ManifestGenerator struct {
	BasePath string // e.g., "clusters/contabo-mkt/tenants"

	// RegistryMirror is the Sovereign-local Harbor host the per-tenant
	// vCluster images pull through (proxy-cache). Default
	// "harbor.openova.io".
	//
	// MIRROR-EVERYTHING (#3760, Refs #3376 #3754): vcluster 0.33.x renders
	// TWO ghcr.io images into the StatefulSet initContainers — the
	// `loft-sh/kubernetes` k8s distro AND the `loft-sh/vcluster-oss`
	// syncer — both of which the harbor-proxy-pull Kyverno ClusterPolicy
	// (Enforce) DENIES because they don't match the `*/proxy-*/*` glob.
	// generateVCluster re-tags both to `<mirror>/proxy-ghcr/loft-sh/...`,
	// lockstep with bp-dmz/mgmt/rtz-vcluster and the org-controller's
	// gitops.Render. Cutover Step-04 (ADR-0002) flips it to
	// harbor.<sovereign-fqdn> post-handover.
	RegistryMirror string

	// HelmReleaseAppVersions pins the per-Org HelmRelease-shaped catalog
	// apps' chart versions by app slug (e.g. {"openclaw": "0.2.13",
	// "stalwart-mail": "0.1.12"}) — #4706. A missing entry falls back to
	// the floating `version: "*"` (see pinHRChartVersion for why floating
	// is a hazard worth pinning away). Wired from
	// CATALYST_HR_APP_CHART_VERSIONS in services/provisioning/main.go.
	HelmReleaseAppVersions map[string]string

	// CloudProvider is the IaC cloud provider this Sovereign runs on —
	// "hetzner" or "huawei". It selects the block-storage StorageClass
	// the per-tenant CNPG pair PVCs bind to, because the StorageClass
	// name is provider-specific and a cross-provider hardcode renders
	// every customer-Org Postgres PVC permanently Pending.
	//
	// PILLAR-3 LANDMINE (#4060): generateCNPGPair previously hardcoded
	// `storageClass: hcloud-volumes`, which only exists on Hetzner
	// (hcloud-csi). On a Huawei (kom4dc) Sovereign only `evs-ssd` (the
	// open-source huaweicloud-csi-driver EVS class from
	// platform/huawei-evs-csi) exists, so every active-hot-standby
	// customer-Org CNPG PVC stayed Pending forever and the two-region
	// CNPG pair never materialized — Pillar-3 (two independent CNPG
	// clusters + region-kill failover) was SILENTLY dead for tenant
	// Orgs on the omantel.biz Sovereign.
	//
	// Populated from the CLOUD_PROVIDER env in main.go (wired by the
	// catalyst chart's provisioning Deployment from global.cloudProvider).
	// Empty / unknown defaults to "hetzner" so the legacy contabo/Hetzner
	// path renders hcloud-volumes unchanged (NO-REGRESS).
	CloudProvider string

	// ReplicaRegionKubeSecret is the flux-system Secret name that holds
	// the STANDBY region's (region-B) host-cluster kubeconfig. The
	// cross-region active-hot-standby standby Cluster CR must be reconciled
	// into region-B's apiserver — NOT region-A's — because region B is the
	// only place with nodes labelled `openova.io/region=<replica_region>`
	// that the chart's replica-side node-affinity requires. The host
	// helm-controller (region A) installs the replica HelmRelease INTO
	// region B via HR-level `spec.kubeConfig.secretRef: <this>` (#4282/#4275).
	//
	// THE BUG (#4282): before this field the per-Org bp-cnpg-pair emitted a
	// SINGLE HR (chart default `side: primary`) into ONE target cluster, so
	// the replica half either never rendered or — via the WP-tenant inline
	// path — landed in region A's cluster where its region-B affinity matched
	// 0/N nodes → the standby pod (`*-pgbasebackup`) hung Pending for 16h and
	// the HR install-timeouts. With no standby in region B the region-kill
	// pillar (#4275) had nothing to fail over to (live demo Org, 2026-06-25).
	//
	// Populated from CATALYST_REPLICA_REGION_KUBECONFIG_SECRET in main.go;
	// empty resolves to the deterministic default
	// `sovereign-replica-region-kubeconfig` (replicaRegionKubeSecretDefault).
	// The bootstrap mirrors region-B's host kubeconfig into flux-system under
	// this name (companion to the secondary-kubeconfig handler that already
	// persists it on the catalyst-api PVC at
	// /var/lib/catalyst/kubeconfigs/<depID>-<region>.yaml).
	ReplicaRegionKubeSecret string

	// ParentDomain is the per-Sovereign org-pool parent zone (e.g.
	// "omani.homes") the funnel stamps onto the public hostnames of the
	// HelmRelease-shaped per-Org apps (bp-openclaw → openclaw.<slug>.<parent>,
	// bp-stalwart-tenant → mail.<slug>.<parent>). It is the SAME
	// TENANT_PARENT_DOMAIN env the Handler reads for the tenant-public patch;
	// wired through the generator in main.go so the openclaw/stalwart overlays
	// (#4272/#4307) emit the correct console-isolation hostnames. Empty falls
	// back to parentDomainDefault so a render never produces a bare `<slug>.`
	// with no zone.
	ParentDomain string

	// SovereignFQDN is the per-Sovereign apex domain (e.g. "omantel.biz").
	// On a Sovereign where the per-Org Keycloak realm is DISABLED (the
	// intentional default — CATALYST_PER_ORG_REALM_ENABLED=false → the
	// org-controller never provisions a `keycloak.<slug>.<parent>` host), the
	// only resolvable OIDC issuer is the SHARED realm fronted at
	// `auth.<SovereignFQDN>` (the same host the console + every other app
	// authenticates against). bp-openclaw's controller /readyz runs
	// ensureKeys, which fetches the issuer's `.well-known/openid-configuration`
	// + JWKS; against the NXDOMAIN per-Org host it fails → 503 forever (the
	// live #4272 re-walk on funnel Org `g11clawmail`). When SovereignFQDN is
	// set, generateOpenClawHR points the issuer at the resolvable shared realm
	// so JWKS resolves and /readyz returns 200. Empty (Catalyst-Zero, or no
	// SOVEREIGN_FQDN wiring) falls back to the conventional per-Org realm host
	// — the legacy behavior, harmless on a cluster that DOES run per-Org realms.
	//
	// Populated from SOVEREIGN_FQDN in main.go (the same env the Handler reads
	// for the KC-broker URL + git-base-path guard). Never hardcoded (Inviolable
	// Principle 4).
	SovereignFQDN string

	// SharedRealmName is the realm segment of the shared-realm issuer
	// (`https://auth.<SovereignFQDN>/realms/<SharedRealmName>`). The canonical
	// Sovereign realm name is `sovereign` (platform/keycloak/blueprint.yaml
	// `realm: sovereign`; catalyst-api's CATALYST_KC_REALM default). Empty
	// resolves to sharedRealmNameDefault.
	SharedRealmName string

	// AppsSyncSourceRepo is the name of the Flux GitRepository CR (in
	// flux-system) that generateAppsSyncKustomization's `tenant-<slug>-apps`
	// Kustomization resolves its sourceRef against — the Gitea mono-repo that
	// holds the funnel-door apps tree (`./<basePath>/<slug>/apps`).
	//
	// #4761 LANDMINE: this sourceRef.name was HARDCODED — first to `flux-system`
	// (#4785: no such GitRepository exists on a Sovereign → the Kustomization
	// stuck permanently FALSE `GitRepository "flux-system" not found`), then
	// #4798 swapped the literal to `openova-org-tenants` (the Sovereign's
	// funnel-door mono-repo GitRepository). But a bare literal is still an
	// Inviolable-Principle-4 violation: the GitRepository name is
	// per-Sovereign-bootstrap state (a differently-bootstrapped Sovereign, or a
	// mothership whose source CR is named `flux-system`, names this repo
	// differently), so a hardcode renders the `tenant-<slug>-apps` Kustomization
	// permanently FALSE — the SAME "GitRepository not found" failure #4785 hit,
	// just re-hardcoded to a different name.
	//
	// Populated from CATALYST_APPS_SYNC_SOURCE_REPO in main.go. Empty resolves
	// to appsSyncSourceRepoDefault ("openova-org-tenants") so the post-#4798
	// funnel-door behavior is byte-unchanged for every existing Sovereign
	// (NO-REGRESS).
	AppsSyncSourceRepo string
}

// parentDomain resolves the funnel's org-pool parent zone for the
// HelmRelease-shaped per-Org apps, falling back to parentDomainDefault when the
// generator was constructed without a TENANT_PARENT_DOMAIN wiring.
func (g *ManifestGenerator) parentDomain() string {
	return ResolveParentDomain(g.ParentDomain)
}

// ResolveParentDomain is the SINGLE source of truth for the effective org-pool
// parent zone the per-Org apps render under: the supplied TENANT_PARENT_DOMAIN
// value when non-empty, else parentDomainDefault ("omani.homes"). It is exported
// so the provisioning-service wiring (main.go) can resolve the SAME effective
// value ONCE and hand it to BOTH the apps generator (this package) and the
// tenant.created Organization-CR handler — guaranteeing the per-Org DNS-writer
// pool (`Org.spec.tenantPublic.parentDomain`) can never diverge from the pool
// the apps-HTTPRoute actually renders under (#4421). Without this, the generator
// quietly defaulted to omani.homes while the handler saw an empty string, so a
// Sovereign with no TENANT_PARENT_DOMAIN minted apps on omani.homes but wrote no
// per-Org A-record there → the host fell through to a stale apex wildcard
// (49.12.16.160).
func ResolveParentDomain(parentDomain string) string {
	if pd := strings.TrimSpace(parentDomain); pd != "" {
		return pd
	}
	return parentDomainDefault
}

// sharedRealmIssuer returns the resolvable SHARED-realm OIDC issuer
// `https://auth.<SovereignFQDN>/realms/<SharedRealmName>` when SovereignFQDN is
// set, else "" (the caller falls back to the per-Org realm host). This is the
// issuer the HelmRelease-shaped per-Org apps (#4272 openclaw, #4307 stalwart)
// MUST use on a Sovereign whose per-Org Keycloak realm is disabled — the
// per-Org `keycloak.<slug>.<parent>` host is NXDOMAIN there, so its JWKS fetch
// (openclaw controller /readyz ensureKeys) fails → 503 forever. The shared
// realm at auth.<fqdn> is the SAME issuer the console + every other Sovereign
// app resolves, so its discovery + JWKS endpoints are live.
func (g *ManifestGenerator) sharedRealmIssuer() string {
	fqdn := strings.TrimSpace(g.SovereignFQDN)
	if fqdn == "" {
		return ""
	}
	realm := strings.TrimSpace(g.SharedRealmName)
	if realm == "" {
		realm = sharedRealmNameDefault
	}
	return fmt.Sprintf("https://auth.%s/realms/%s", fqdn, realm)
}

// parentDomainDefault is the org-pool parent zone the funnel stamps onto the
// HelmRelease-shaped per-Org app hostnames when ParentDomain is unset. Matches
// the catalog-canon default pool (docs/DOD.md §Domains-canon).
const parentDomainDefault = "omani.homes"

// sharedRealmNameDefault is the canonical Sovereign-wide Keycloak realm name
// the shared-realm issuer (`auth.<fqdn>/realms/<this>`) targets when
// SharedRealmName is unset. Matches platform/keycloak/blueprint.yaml
// (`realm: sovereign`) + catalyst-api's CATALYST_KC_REALM default.
const sharedRealmNameDefault = "sovereign"

// replicaRegionKubeSecretDefault is the deterministic flux-system Secret
// name the cross-region standby HelmRelease's spec.kubeConfig.secretRef
// targets when CATALYST_REPLICA_REGION_KUBECONFIG_SECRET is unset. The
// bootstrap mirrors region-B's host kubeconfig into flux-system under this
// name so the host helm-controller can install the replica CNPG Cluster
// INTO region B (#4282/#4275).
const replicaRegionKubeSecretDefault = "sovereign-replica-region-kubeconfig"

// defaultVClusterRegistryMirror is the bootstrap Harbor proxy host.
const defaultVClusterRegistryMirror = "harbor.openova.io"

// appsSyncSourceRepoDefault is the Flux GitRepository CR name the funnel-door
// `tenant-<slug>-apps` Kustomization resolves its sourceRef against when
// AppsSyncSourceRepo (CATALYST_APPS_SYNC_SOURCE_REPO) is unset. Matches the
// post-#4798 funnel-door mono-repo GitRepository so an unconfigured Sovereign
// renders byte-identically (NO-REGRESS). See ManifestGenerator.AppsSyncSourceRepo
// for why the name must be per-Sovereign-configurable rather than hardcoded
// (#4761 / Inviolable Principle 4).
const appsSyncSourceRepoDefault = "openova-org-tenants"

// Block-storage StorageClass names per cloud provider. These are the
// canonical Catalyst CSI classes installed by the per-provider CSI
// Blueprints (platform/hcloud-csi → hcloud-volumes,
// platform/huawei-evs-csi → evs-ssd).
const (
	hetznerBlockStorageClass = "hcloud-volumes"
	huaweiBlockStorageClass  = "evs-ssd"
)

// cnpgStorageClass resolves the provider-correct block-storage
// StorageClass for the customer-Org CNPG pair PVCs (#4060). Unknown /
// empty provider falls back to the Hetzner class so the legacy path is
// unchanged. Hetzner stays hcloud-volumes (NO-REGRESS); only Huawei
// flips to its EVS CSI class.
func (g *ManifestGenerator) cnpgStorageClass() string {
	switch strings.ToLower(strings.TrimSpace(g.CloudProvider)) {
	case "huawei":
		return huaweiBlockStorageClass
	case "hetzner", "":
		return hetznerBlockStorageClass
	default:
		// An unexpected provider string must not silently render a
		// nonexistent StorageClass; log loud and fall back to the
		// historical Hetzner default rather than wedge PVCs.
		slog.Warn("provisioning/gitops: unknown CLOUD_PROVIDER — defaulting CNPG storageClass to hcloud-volumes",
			"cloud_provider", g.CloudProvider, "storageClass", hetznerBlockStorageClass)
		return hetznerBlockStorageClass
	}
}

// replicaRegionKubeSecret resolves the flux-system Secret name holding the
// standby region's (region-B) kubeconfig for the cross-region active-hot-
// standby HelmRelease's HR-level spec.kubeConfig (#4282/#4275). Empty falls
// back to the deterministic default the bootstrap mirrors region-B's host
// kubeconfig into.
func (g *ManifestGenerator) replicaRegionKubeSecret() string {
	if s := strings.TrimSpace(g.ReplicaRegionKubeSecret); s != "" {
		return s
	}
	return replicaRegionKubeSecretDefault
}

// appsSyncSourceRepo resolves the Flux GitRepository CR name the funnel-door
// `tenant-<slug>-apps` Kustomization points its sourceRef at (#4761). Empty
// falls back to appsSyncSourceRepoDefault ("openova-org-tenants") so the
// post-#4798 behavior is byte-unchanged on any Sovereign that does not wire
// CATALYST_APPS_SYNC_SOURCE_REPO.
func (g *ManifestGenerator) appsSyncSourceRepo() string {
	if s := strings.TrimSpace(g.AppsSyncSourceRepo); s != "" {
		return s
	}
	return appsSyncSourceRepoDefault
}

func NewManifestGenerator(basePath string) *ManifestGenerator {
	return &ManifestGenerator{BasePath: basePath}
}

// registryMirror returns the Harbor proxy host every emitted image is re-tagged
// through, falling back to the bootstrap default when unset.
//
// #5439: the result is cutover-aware. The chart ALWAYS stamps
// VCLUSTER_IMAGE_REGISTRY, so on every Sovereign the configured value is the
// mothership literal from birth; re-emitting it after cutover writes the
// mothership back into a Flux-owned source on the next org mutation. With no
// pivot fact present the resolver returns the input unchanged, so pre-cutover
// output stays byte-identical. See cutover_aware_vcluster_5439.go.
func (g *ManifestGenerator) registryMirror() string {
	return vclusterImageRegistryFor(g.RegistryMirror)
}

func (g *ManifestGenerator) TenantDir(slug string) string {
	return fmt.Sprintf("%s/%s", g.BasePath, slug)
}

// GenerateAll produces all manifests for a tenant. Layout:
//
//	<basepath>/<slug>/
//	  kustomization.yaml          # host-scoped; included by Flux "tenants" Kustomization
//	  namespace.yaml              # host ns tenant-<slug>
//	  vcluster.yaml               # HelmRelease: creates the vCluster
//	  ingress.yaml                # host ingress → synced vCluster services
//	  apps-sync.yaml              # Flux Kustomization that applies apps/ INTO the vCluster
//	  apps/
//	    kustomization.yaml        # vcluster-scoped
//	    namespace.yaml            # in-vcluster ns "apps"
//	    db-*.yaml                 # databases
//	    app-*.yaml                # app deployments + services
func (g *ManifestGenerator) GenerateAll(slug, planSlug string, appSlugs []string) map[string]string {
	return g.GenerateAllWithAppConfigs(slug, planSlug, appSlugs, "", nil)
}

// GenerateAllWithPassword is like GenerateAll but reuses an existing DB
// password when provided. Day-2 installs pass the password that was minted on
// initial provision so app deployments keep connecting to the same DB.
// Passing "" generates a fresh password (initial provision path).
func (g *ManifestGenerator) GenerateAllWithPassword(slug, planSlug string, appSlugs []string, dbPassword string) map[string]string {
	return g.GenerateAllWithAppConfigs(slug, planSlug, appSlugs, dbPassword, nil)
}

// BoundaryIsVcluster is the funnel-side TIER GATE (#4297, keystone of EPIC
// #4293). It MUST stay in lockstep with the org-controller's authoritative
// gate `boundaryIsVcluster` in
// core/controllers/organization/internal/gitops/manifests.go (const
// allTiersVcluster + the same free/S/"" → host-ns, m/l/xl/flexi → vCluster
// switch). That gate lives in an `internal/` package the provisioning module
// cannot import, so this is a deliberate small duplicate, NOT a divergence —
// flip both together if the Sovereign-level policy changes.
//
// The funnel uses it to decide whether the per-Org app-install tree is
// REDIRECTED into the Org vCluster (paid M+ tiers — the apps-sync Kustomization
// carries spec.kubeConfig so the host Flux installs INTO the vcluster API) or
// reconciled straight into the host `<slug>` namespace (free/S tiers — NO
// kubeConfig, the org-controller's `<slug>` ns IS the boundary). Exported so
// the provisioning consumer can gate its vcluster-only waits (vcluster-HR
// Ready / kubeconfig-mirror / synced-pod-name match) on the same predicate.
func BoundaryIsVcluster(planSlug string) bool {
	switch strings.ToLower(strings.TrimSpace(planSlug)) {
	case "", "s", "free":
		// free/S → the host `<slug>` ns IS the boundary; apps reconcile there
		// directly (the org-controller still renders the ns + quota + np).
		return false
	default:
		// m/l/xl/flexi → dedicated Org vCluster; apps are redirected into it.
		return true
	}
}

// GenerateAllWithAppConfigs is the canonical entry point (TBD-V27 #2042).
// `appConfigs` carries the customer-chosen configSchema values keyed by
// app SLUG (e.g. {"postgres": {"replicas": 3, "disk_gb": 20,
// "backups_enabled": true}}). The backing-service renderers consume the
// matching map and thread the values into the rendered manifest:
//
//   - "replicas" (int) → Deployment.spec.replicas
//   - "disk_gb"  (int) → PersistentVolumeClaim.spec.resources.requests.storage
//   - "backups_enabled" (bool) → reserved for future CronJob (logged
//     today; no chart-side binding yet)
//
// Unknown keys are dropped with a Warn log so a stale frontend can't
// tunnel arbitrary YAML into the rendered manifest. Empty/nil
// appConfigs preserves the historical default behavior (replicas:1,
// 2Gi PVC) — every call site that doesn't ship customer values keeps
// working unchanged.
func (g *ManifestGenerator) GenerateAllWithAppConfigs(slug, planSlug string, appSlugs []string, dbPassword string, appConfigs map[string]map[string]any) map[string]string {
	// Workstream A (#4290 / EPIC #4293) — the per-Organization host namespace
	// + vCluster are produced by the org-controller (the SINGLE boundary
	// producer; core/controllers/organization/internal/gitops/manifests.go),
	// named `<slug>`. The funnel no longer builds a SECOND `tenant-<slug>`
	// boundary — it only renders the customer's app-install tree (apps/) and
	// the Flux apps-sync Kustomization that reconciles that tree INTO the
	// org-controller-owned `<slug>` vCluster. So hostNS is now `<slug>`, the
	// same namespace the org-controller's `vcluster` HelmRelease lives in.
	hostNS := slug
	appNS := "apps"

	// #4297 (keystone of EPIC #4293) — TIER GATE. Paid M+ Orgs get a dedicated
	// vCluster; the apps-sync Kustomization REDIRECTS the apps/ tree INTO it via
	// spec.kubeConfig (the host helm/kustomize controller installs into the
	// vcluster API). Free/S Orgs have NO vcluster — the org-controller's `<slug>`
	// host ns IS the boundary, so the apps-sync reconciles straight into it with
	// NO kubeConfig (a kubeConfig referencing the never-created `vc-vcluster`
	// mirror would StateError forever → host-tier apps would never deploy).
	isVcluster := BoundaryIsVcluster(planSlug)

	// --- databases required by selected apps ---
	needsRedis := false
	mysqlApps := []string{}
	postgresApps := []string{}
	for _, a := range appSlugs {
		spec := GetAppSpec(a)
		switch spec.NeedsDB {
		case "postgres":
			postgresApps = append(postgresApps, a)
		case "mysql":
			mysqlApps = append(mysqlApps, a)
		}
		if a == "chatwoot" {
			needsRedis = true
		}
	}
	if dbPassword == "" {
		dbPassword = randomHex(16)
	}

	// --- host-scoped files ---
	// Workstream A (#4290 / EPIC #4293): NO namespace.yaml + NO vcluster.yaml.
	// The org-controller is the SINGLE producer of the `<slug>` namespace +
	// the `vcluster` HelmRelease (from the Organization CR). The funnel emits
	// ONLY: the apps-sync Flux Kustomization (reconciles apps/ into that
	// vCluster), the provisioning ServiceAccount RBAC the sync needs, and the
	// host ingress for the synced services — all targeting `<slug>`.
	// #4297: the host kustomization.yaml is assembled LAST (below) so it can
	// include any host-reconciled HelmRelease the db block adds (e.g. the
	// bp-cnpg-pair HR, which must live on the host with HR-level kubeConfig —
	// never inside the vcluster-redirected apps/ tree).
	hostFiles := map[string]string{
		"ingress.yaml":           generateHostIngress(hostNS, slug, appSlugs),
		"apps-sync.yaml":         generateAppsSyncKustomization(hostNS, slug, g.BasePath, isVcluster, g.appsSyncSourceRepo()),
		"provisioning-rbac.yaml": generateProvisioningTenantRBAC(hostNS),
	}

	// --- in-vCluster files under apps/ ---
	vcFiles := map[string]string{
		"namespace.yaml": generateAppNamespace(appNS),
	}
	if len(postgresApps) > 0 {
		// TBD-V17 (#2068) — Pillar 3 generic install path for
		// bp-cnpg-pair. When the customer's Postgres-backed app
		// configSchema declares `active_hot_standby: true` AND a valid
		// distinct primary_region / replica_region pair, render a
		// bp-cnpg-pair HelmRelease (primary + replica CNPG Cluster CR
		// across two regions, synchronous WAL streaming over Cilium
		// ClusterMesh) instead of the single-Pod legacy postgres
		// Deployment. Applies to EVERY postgres-backed app in the
		// marketplace (Umami / NocoDB / Gitea / Plane / Twenty /
		// Listmonk / Chatwoot / canonical Postgres-backed bundle), not
		// just bp-wordpress-tenant — that was the audit gap closed by
		// this PR.
		//
		// Default-OFF: when active_hot_standby is absent or false (the
		// historical default for every tenant pre-#2068) the legacy
		// generatePostgres() path runs unchanged — zero regression for
		// any existing customer. Same for malformed region picks
		// (identical primary/replica, or either missing): fall back to
		// single-cluster shape rather than rendering a HelmRelease
		// bp-cnpg-pair's `required` template guard would reject.
		pgCfg := appConfigs["postgres"]
		enableHA := readBoolCfg(pgCfg, "active_hot_standby", false, "postgres")
		primaryRegion := strings.TrimSpace(readStringCfg(pgCfg, "primary_region", "", "postgres"))
		replicaRegion := strings.TrimSpace(readStringCfg(pgCfg, "replica_region", "", "postgres"))
		if enableHA && primaryRegion != "" && replicaRegion != "" && primaryRegion != replicaRegion {
			// #4297 keystone — the bp-cnpg-pair HelmRelease is a HelmRelease CR,
			// NOT a plain manifest. The apps-sync Kustomization REDIRECTS the
			// apps/ tree INTO the Org vCluster, but a vcluster runs NO
			// in-cluster helm-controller (#3055 StateError), so an HR CR landing
			// inside it would never reconcile. So the pair HRs are emitted as
			// HOST files (reconciled by the host flux-system kustomization,
			// alongside the apps-sync CR), NOT under apps/.
			//
			// #4293 BLOCKER-1 FIX — the CNPG Cluster CRs must stay HOST-SIDE for
			// BOTH tiers. bp-cnpg-pair ships ONLY `postgresql.cnpg.io/v1 Cluster`
			// CRs — no operator, no CRD. The cnpg-system operator + the Cluster
			// CRD are CLUSTER-SINGLETONS that live on the HOST (slot 16,
			// `target: host`); they do NOT exist inside a per-Org vCluster
			// apiserver (the vcluster syncs no CRDs and runs no cnpg operator).
			// The keystone's earlier shape gave the PRIMARY side an HR-level
			// kubeConfig → the host helm-controller ran `helm install` of the
			// primary Cluster CR INTO the vcluster, where `helm install` fails
			// `no matches for kind "Cluster" in version "postgresql.cnpg.io/v1"`
			// and (even if the CRD were synced) nothing would reconcile it →
			// the paid M+ active-hot-standby HA path WEDGES on every fresh prov.
			// Fix: the PRIMARY HR carries NO vcluster kubeConfig regardless of
			// tier, so it reconciles on region A's HOST where the operator+CRD
			// live and the chart installs the primary Cluster into the host
			// `<slug>` ns. The in-vcluster app pods reach the DB via the synced
			// `postgres` Service (sync.toHost.services is enabled on the
			// org-vcluster) — the credentials Secret (generateCNPGPairSecret)
			// lands inside the vcluster apps/ tree pointing at that Service.
			// This matches the EPIC's own "the per-component CNPG Clusters
			// already live host-side" migration note + the cluster-singleton
			// webhook invariant (exactly ONE cnpg operator, host-only). The
			// _ = isVcluster reference is retained below only for the apps-sync /
			// pod-name tier gate — the CNPG-pair primary is host-side for all.
			//
			// chartTargetNS — the namespace the chart installs into. For BOTH
			// tiers the primary Cluster lands in the host `<slug>` ns (where the
			// org-controller renders the boundary ns + quota), co-located with
			// the host cnpg-system operator that reconciles it. The app pods
			// inside the vcluster read the synced Service + the apps-tree
			// postgres-credentials Secret.
			//
			// #4282/#4275 — CROSS-REGION SPLIT-SIDE. A 2-region Sovereign is
			// TWO separate clusters joined by Cilium ClusterMesh; the chart is
			// already split-side (cnpgPair.side primary|replica — each side
			// renders ONLY its own Cluster CR, pinned to its region's nodes via
			// node-affinity). The keystone emitted ONE HR (chart default
			// side=primary) into ONE target cluster, so the standby Cluster
			// either never rendered or landed in region-A's cluster where its
			// region-B affinity matches 0/N nodes → the `*-pgbasebackup` pod
			// hangs Pending forever and the region-kill pillar has no standby to
			// fail over to. Fix: emit TWO HRs —
			//   • PRIMARY side → region A. For the vcluster tier its kubeConfig
			//     targets the Org vcluster (which lives on region A's host); the
			//     primary Cluster's region-A affinity matches the local nodes.
			//   • REPLICA side → region B. Its kubeConfig targets region-B's
			//     host-cluster kubeconfig (the flux-system mirror), so the host
			//     helm-controller installs the standby Cluster INTO region B,
			//     where the matching `openova.io/region=<replica_region>` nodes
			//     live. THIS is the cross-region placement the keystone's
			//     spec.kubeConfig mechanism extended to the standby region.
			// Both HRs carry the SAME full values (both regions + both storage
			// blocks) so the chart's validateRegions + the replica's
			// externalClusters source resolve; only `cnpgPair.side` and the
			// kubeConfig differ. This mirrors the bootstrap-kit slot-16b path
			// where each region's own Flux installs its side from the same chart.
			hostFiles["db-cnpg-pair-primary.yaml"] = generateCNPGPair(cnpgPairOpts{
				side:          "primary",
				ns:            slug,
				password:      dbPassword,
				apps:          postgresApps,
				cfg:           pgCfg,
				primaryRegion: primaryRegion,
				replicaRegion: replicaRegion,
				storageClass:  g.cnpgStorageClass(),
				// #4293 BLOCKER-1 — NO vcluster kubeConfig. The primary Cluster CR
				// reconciles on region A's HOST (where the cnpg-system operator +
				// the postgresql.cnpg.io CRD live), installing into the host
				// `<slug>` ns for BOTH tiers. Routing it into the vcluster (the
				// keystone's old shape) `helm install`-failed on the missing CRD.
				kubeSecret: "",
			})
			// The replica HR ALWAYS carries a kubeConfig — even for the host
			// tier — because the standby Cluster MUST land in region B's cluster,
			// a DIFFERENT physical cluster than the host Flux's own (region A).
			// For the host tier there is no vcluster, so the chart installs into
			// region B's host `<slug>` ns; for the vcluster tier region B has no
			// per-Org vcluster mirror, so the standby CNPG Cluster lands in region
			// B's host `<slug>` ns and streams WAL to the region-A primary over
			// ClusterMesh (CNPG replica clusters are mesh-reachable regardless of
			// which side runs inside a vcluster). The target namespace is `<slug>`
			// in region B either way.
			hostFiles["db-cnpg-pair-replica.yaml"] = generateCNPGPair(cnpgPairOpts{
				side:          "replica",
				ns:            slug,
				password:      dbPassword,
				apps:          postgresApps,
				cfg:           pgCfg,
				primaryRegion: primaryRegion,
				replicaRegion: replicaRegion,
				storageClass:  g.cnpgStorageClass(),
				kubeSecret:    g.replicaRegionKubeSecret(),
			})
			// The standalone postgres-credentials Secret the app pods read goes
			// INSIDE the vcluster (apps/ tree), co-located with the app pods. It
			// is authored with the apps-tree namespace ("apps"); the apps-sync
			// Kustomization rewrites it to `<slug>` on apply, matching the chart
			// targetNamespace above.
			vcFiles["db-cnpg-pair-secret.yaml"] = generateCNPGPairSecret(appNS, dbPassword, postgresApps)
		} else {
			if enableHA {
				// Operator opted in but didn't supply distinct region
				// pair — log loud so the gap is operator-visible rather
				// than silently degrading to single-cluster.
				slog.Warn("provisioning/gitops: active_hot_standby requested but region pair invalid — falling back to single-cluster postgres",
					"app", "postgres", "primary_region", primaryRegion, "replica_region", replicaRegion)
			}
			vcFiles["db-postgres.yaml"] = generatePostgres(appNS, dbPassword, postgresApps, pgCfg, g.registryMirror())
		}
	}
	if len(mysqlApps) > 0 {
		vcFiles["db-mysql.yaml"] = generateMySQL(appNS, dbPassword, mysqlApps, appConfigs["mysql"], g.registryMirror())
	}
	if needsRedis {
		vcFiles["db-redis.yaml"] = generateRedis(appNS, g.registryMirror())
	}
	for _, a := range appSlugs {
		// Shareable database slugs are emitted as db-*.yaml above; skip
		// them here so we don't also produce a stub app-*.yaml that
		// collides with the real db- manifest.
		if a == "mysql" || a == "postgres" || a == "redis" {
			continue
		}
		// HelmRelease-shaped apps (openclaw #4272, stalwart-mail #4307) are
		// emitted as HOST files below (the helm-controller runs on the host,
		// not inside the per-Org vcluster — #3055), NOT as a synced in-vcluster
		// Deployment. Skip them here so the generic one-Deployment template
		// doesn't render an invalid `image: Required value` manifest under
		// their name (the #941 live failure that flagged them non-deployable).
		if isHelmReleaseApp(a) {
			continue
		}
		spec := GetAppSpec(a)
		// MIRROR-EVERYTHING (#3785, Refs #3376 #3761): route the app image
		// (main container + the InitCommand initContainer that reuses it)
		// THROUGH the Sovereign Harbor proxy-cache. The vCluster syncer
		// schedules the backing Pod on the HOST cluster, where the
		// `harbor-proxy-pull` Kyverno ClusterPolicy (Enforce) DENIES any
		// image not matching `*/proxy-*/*` — so a raw `wordpress:6-apache`
		// pull is blocked and the purchased app never starts. proxyImage is
		// a no-op when registryMirror is empty or the image is already
		// proxied / on a registry without a Harbor proxy project.
		spec.Image = proxyImage(spec.Image, g.registryMirror())
		vcFiles[fmt.Sprintf("app-%s.yaml", a)] = generateAppDeployment(appNS, slug, planSlug, a, spec, dbPassword, g.parentDomain())
	}
	vcFiles["kustomization.yaml"] = generateKustomization(appNS, vcFiles)

	// HelmRelease-shaped per-Org apps (openclaw #4272, stalwart-mail #4307) —
	// emitted as HOST files (helm-controller runs on the host, NOT inside the
	// per-Org vcluster, #3055), tier-aware via the SAME HR-level kubeConfig the
	// bp-cnpg-pair path uses: vcluster tier installs the chart INTO the Org
	// vcluster through the flux-system `tenant-<slug>-kubeconfig` mirror; host
	// tier installs straight into the host `<slug>` ns (kubeSecret ""). Each HR
	// ships its own bp-* HelmRepository so a fresh funnel Org resolves the
	// sourceRef. Mirrors the BSS-door orgTenantBPOpenClaw / orgTenantBPStalwart
	// overlays so a cart Org gets the SAME releases the BSS door emits.
	hrKubeSecret := ""
	if isVcluster {
		hrKubeSecret = fmt.Sprintf("tenant-%s-kubeconfig", slug)
	}
	for _, a := range sortedHelmReleaseApps(appSlugs) {
		hostFiles[fmt.Sprintf("app-%s.yaml", a)] = generateHelmReleaseApp(a, helmReleaseAppOpts{
			slug:         slug,
			parentDomain: g.parentDomain(),
			kubeSecret:   hrKubeSecret,
			// #4706 — pin the chart version (empty → floating "*").
			chartVersion: g.HelmReleaseAppVersions[a],
			// #4272: the resolvable SHARED-realm OIDC issuer
			// (auth.<fqdn>/realms/sovereign) when this Sovereign runs no
			// per-Org realm. Empty falls the HR templates back to the per-Org
			// keycloak.<slug>.<parent> host (legacy / Catalyst-Zero).
			sharedRealmIssuer: g.sharedRealmIssuer(),
		})
	}

	// #4297: assemble the host kustomization.yaml LAST so it enumerates every
	// host-scoped file including any host-reconciled HelmRelease the db block
	// added above (the bp-cnpg-pair HR lives here, not in apps/). The host
	// kustomization carries no `namespace:` — the apps-sync CR + ingress are
	// flux-system / host-namespaced by their own metadata, and the cnpg-pair HR
	// stamps its own namespace.
	hostFiles["kustomization.yaml"] = generateKustomization("", hostFiles)

	// --- assemble paths prefixed by tenant dir ---
	dir := g.TenantDir(slug)
	result := make(map[string]string, len(hostFiles)+len(vcFiles))
	for name, content := range hostFiles {
		result[fmt.Sprintf("%s/%s", dir, name)] = content
	}
	for name, content := range vcFiles {
		result[fmt.Sprintf("%s/apps/%s", dir, name)] = content
	}
	return result
}

// --- host-scoped manifests ---
//
// Workstream A (#4290 / EPIC #4293) retired the funnel's boundary builders
// generateHostNamespace + generateVCluster. The org-controller
// (core/controllers/organization/internal/gitops/manifests.go) is the SINGLE
// producer of the `<slug>` namespace + `vcluster` HelmRelease, materialized
// from the Organization CR. The funnel renders only the apps-sync
// Kustomization, the provisioning RBAC, and the host ingress — all into the
// org-controller-owned `<slug>` namespace.

// generateAppsSyncKustomization emits the per-tenant Flux Kustomization CR
// that reconciles the tenant's apps/ tree into the vCluster.
//
// IMPORTANT: the CR lives in `flux-system`, NOT inside the tenant namespace.
// Placing it inside tenant-<slug> caused namespace GC to wedge forever on
// teardown: `finalizers.fluxcd.io` on the child CR can't finalize while its
// host namespace is already Terminating → NamespaceContentRemaining loops
// indefinitely (see issue #97). Keeping the CR in flux-system means the
// tenant NS has no Flux child blocking its GC; the CR is deleted out-of-band
// by the teardown handler and its finalizer completes against a still-live
// flux-system namespace.
//
// spec.targetNamespace stamps the destination ns. For BOTH tiers it is the
// org-controller-owned `<slug>` namespace — for the vcluster tier it is the
// in-vcluster namespace the synced resources land in; for the host tier it is
// the host namespace the resources are applied into directly.
//
// #4297 keystone — TIER-AWARE kubeConfig. For the VCLUSTER tier (isVcluster
// true) the Kustomization carries spec.kubeConfig.secretRef so the host Flux
// kustomize-controller reconciles the apps tree INTO the Org vCluster apiserver
// (the apps then run inside the vcluster, not on the host). For the HOST tier
// (free/S) there is NO vcluster — emitting a kubeConfig referencing the
// never-created `vc-vcluster` mirror would StateError forever and the host-tier
// Org's apps would never deploy. So host-tier omits kubeConfig entirely and the
// apps reconcile straight into the host `<slug>` ns (which IS the boundary).
//
// kubeConfig.secretRef (vcluster tier only): Flux's Kustomization API accepts
// only `name`+`key` on secretRef (no namespace override), so the secret must
// live in flux-system alongside the CR. The org-controller's `vcluster`
// HelmRelease exports the kubeconfig to `<slug>/vc-vcluster`; the provisioning
// workflow mirrors that into `flux-system/tenant-<slug>-kubeconfig` after the
// vcluster HelmRelease becomes Ready (handlers.mirrorVClusterKubeconfig). The
// mirror is deleted during teardown.
//
// Readiness gating: NO manifest-level dependsOn is used (Flux Kustomization
// dependsOn references other Kustomizations, not the vcluster HelmRelease). The
// gate is enforced in code: the consumer waits for the vcluster HR Ready and
// mirrors the kubeconfig BEFORE the apps are expected up; until the mirror
// exists this Kustomization simply StateErrors-then-retries (retryInterval 1m)
// — the intended self-healing for the vcluster tier. The host tier has no
// secret dependency at all, so it reconciles immediately.
//
// #4761 — sourceRef.name is threaded from ManifestGenerator.appsSyncSourceRepo()
// (CATALYST_APPS_SYNC_SOURCE_REPO, default "openova-org-tenants"), NOT a bare
// literal. The Flux GitRepository CR that backs the funnel-door apps tree is
// per-Sovereign-bootstrap state — hardcoding its name (first `flux-system`
// #4785, then `openova-org-tenants` #4798) leaves the `tenant-<slug>-apps`
// Kustomization permanently FALSE (`GitRepository "<name>" not found`) on any
// Sovereign that names the repo differently (Inviolable Principle 4).
func generateAppsSyncKustomization(ns, slug, basePath string, isVcluster bool, sourceRepo string) string {
	kubeConfig := ""
	if isVcluster {
		kubeConfig = fmt.Sprintf(`
  kubeConfig:
    secretRef:
      name: tenant-%s-kubeconfig
      key: config`, slug)
	}
	return fmt.Sprintf(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tenant-%s-apps
  namespace: flux-system
spec:
  interval: 5m
  retryInterval: 1m
  timeout: 5m
  prune: true
  wait: true
  targetNamespace: %s
  sourceRef:
    kind: GitRepository
    name: %s
    namespace: flux-system
  path: ./%s/%s/apps%s
`, slug, ns, sourceRepo, basePath, slug, kubeConfig)
}

func generateHostIngress(ns, slug string, appSlugs []string) string {
	// HelmRelease-shaped apps (openclaw #4272, stalwart-mail #4307) carry their
	// OWN chart-emitted HTTPRoute parented to cilium-gateway-console — they must
	// NOT also appear in this traefik host ingress (a second route to a service
	// that doesn't exist as `<app>-x-...-x-vcluster` would 404). Filter to the
	// Deployment-shaped apps that actually sync a Service into the vcluster.
	routable := make([]string, 0, len(appSlugs))
	for _, a := range appSlugs {
		if isHelmReleaseApp(a) {
			continue
		}
		routable = append(routable, a)
	}
	appSlugs = routable
	if len(appSlugs) == 0 {
		return ""
	}
	// Services synced from the vCluster use the pattern:
	//   <svc>-x-<vcluster-ns>-x-<vcluster-name>
	// Flux's Kustomization for this tenant sets spec.targetNamespace to the
	// host namespace ("tenant-<slug>"), which rewrites every resource's
	// metadata.namespace from "apps" (as generated) to "tenant-<slug>"
	// before applying to the vCluster. Net result: services sync as
	// <svc>-x-tenant-<slug>-x-vcluster, not <svc>-x-apps-x-vcluster.
	//
	// Observed live on tenant emrah5: ingress paths pointed at
	// wordpress-x-apps-x-vcluster → 404. Actual service name was
	// wordpress-x-tenant-emrah5-x-vcluster. Issue #117. Delegates to the ONE
	// canonical synced-name derivation (hostSyncedServiceName) the #4993
	// host-native app route also uses, so the two stay in lockstep.
	syncedName := func(app string) string {
		return hostSyncedServiceName(app, ns)
	}

	var paths string
	for i, app := range appSlugs {
		prefix := "/" + app
		if i == 0 {
			// root path routes to the first app for convenience
			paths += fmt.Sprintf(`          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: 80
`, syncedName(app))
		}
		paths += fmt.Sprintf(`          - path: %s
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: 80
`, prefix, syncedName(app))
	}

	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: tenant-ingress
  namespace: %s
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  ingressClassName: traefik
  rules:
    - host: %s.omani.rest
      http:
        paths:
%s  tls:
    - hosts:
        - %s.omani.rest
      secretName: tenant-%s-tls
`, ns, slug, paths, slug, slug)
}

// --- in-vCluster manifests (applied with vCluster kubeconfig) ---

func generateAppNamespace(ns string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
`, ns)
}

// generatePostgres / generateMySQL / generateRedis emit the single-Pod
// DB-backing Deployments that co-install with a customer's purchased app
// inside the per-Org vCluster. MIRROR-EVERYTHING (#3785, Refs #3376 #3761):
// the vCluster syncer schedules the BACKING Pod on the HOST cluster (the
// `tenant-<slug>` host namespace), where the `harbor-proxy-pull` Kyverno
// ClusterPolicy (Enforce) DENIES any image not matching `*/proxy-*/*`. A raw
// `postgres:16-alpine` / `mariadb:11` / `valkey/valkey:8-alpine` is therefore
// blocked, the DB never starts, and the app it backs can never run — the
// funnel terminal (#3376) stays connection-refused. proxyImage re-tags each
// through the registry-appropriate Sovereign Harbor proxy project
// (proxy-dockerhub here), identically to the app-deployment images; it is a
// no-op when the mirror is empty or the reference is already proxied.
func generatePostgres(ns, password string, apps []string, cfg map[string]any, registryMirror string) string {
	// Per-app database isolation: create db_<appSlug> for each postgres-backed
	// app so co-installed apps (e.g. gitea + nextcloud on the same tenant)
	// don't collide on a shared schema. The first database is also created by
	// POSTGRES_DB env so the cluster bootstraps cleanly; additional databases
	// plus grants are created by an init script in /docker-entrypoint-initdb.d/.
	sortedApps := sortStrings(append([]string{}, apps...))
	primaryDB := "appdb"
	if len(sortedApps) > 0 {
		primaryDB = "db_" + sortedApps[0]
	}
	initSQL := "-- per-app database bootstrap (postgres)\n"
	for _, a := range sortedApps {
		db := "db_" + a
		if db == primaryDB {
			// POSTGRES_DB already creates the primary DB with `app` as owner;
			// skip it here to avoid "already exists" errors on init.
			continue
		}
		initSQL += fmt.Sprintf(`CREATE DATABASE %s;
GRANT ALL PRIVILEGES ON DATABASE %s TO app;
`, db, db)
	}

	// TBD-V27 (#2042) — bind customer-chosen configSchema values.
	// Postgres' canonical configSchema (seed.go:699-701):
	//   replicas:        int, min 1, max 5, default 1
	//   disk_gb:         int, min 1, max 500, default 5
	//   backups_enabled: bool, default false (no chart-side binding yet —
	//                    logged for visibility, future CronJob slot)
	//
	// Unknown / mistyped values fall back to defaults with a Warn so a
	// stale frontend can't tunnel arbitrary integers past the configSchema
	// constraints. Per-field clamping mirrors the seed schema's Min/Max.
	replicas := readIntCfg(cfg, "replicas", 1, 1, 5, "postgres")
	diskGB := readIntCfg(cfg, "disk_gb", 5, 1, 500, "postgres")
	backupsEnabled := readBoolCfg(cfg, "backups_enabled", false, "postgres")
	// active_hot_standby / primary_region / replica_region are picked
	// up upstream in GenerateAllWithAppConfigs (#2068 generic cnpg-pair
	// install path). Adding them to the known-keys list here avoids a
	// false-positive "unknown key" Warn when the customer chose the
	// single-cluster shape but the orchestrator still threaded the
	// per-app config through.
	logUnknownKeys(cfg, []string{"replicas", "disk_gb", "backups_enabled", "active_hot_standby", "primary_region", "replica_region"}, "postgres")
	if backupsEnabled {
		// No chart-side binding yet — a follow-up will land a CronJob +
		// pgdump-to-SeaweedFS sidecar. Logging here makes the gap
		// operator-visible rather than silent.
		slog.Warn("generatePostgres: backups_enabled requested but no chart-side binding yet — value parsed but not rendered",
			"namespace", ns)
	}

	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: postgres-credentials
  namespace: %s
type: Opaque
stringData:
  POSTGRES_USER: app
  POSTGRES_PASSWORD: "%s"
  POSTGRES_DB: %s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgres-initdb
  namespace: %s
data:
  init.sql: |
%s
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-data
  namespace: %s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: %dGi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: %s
  labels:
    # #4422: the singleton-db carve-out the 'multi-replica-drainability' policy
    # excludes on lives on the Deployment's OWN metadata.labels (Kyverno's
    # resources.selector matches the resource's top-level labels). Mirrors the
    # pod-template label below + the cnpg.io/cluster exempt on backup-configured.
    openova.io/singleton-db: "true"
  annotations:
    openova.io/backups-enabled: "%t"
spec:
  replicas: %d
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
        # #4422: per-Org Kyverno 'multi-replica-drainability' (replicas-at-least-two)
        # demands spec.replicas>=2, but this is a single-PVC stateful DB with
        # strategy:Recreate and NO replication wired — a 2nd replica sharing one
        # RWO PVC would corrupt data. Stamp the singleton-db carve-out label so
        # the policy exempts it in ANY per-Org namespace (mirrors the cnpg.io/
        # cluster label-exempt on backup-configured #3971). The policy doc itself
        # lists 'explicitly-singleton services (databases ...)' as legitimate.
        openova.io/singleton-db: "true"
      annotations:
        # #4422: Kyverno 'backup-configured' (pvc-must-be-velero-backed, enforce)
        # blocks the PVC unless the owning Pod annotates its volume for Velero
        # file-level backup. Annotate pgdata so the PVC admits AND is backed up.
        velero.io/backup-volumes: pgdata
    spec:
      containers:
        - name: postgres
          image: %s
          ports:
            - containerPort: 5432
          envFrom:
            - secretRef:
                name: postgres-credentials
          # #4389: Kyverno 'probes-present' (svc-fail/enforce) blocks any
          # Deployment whose containers omit BOTH probes → the vcluster/apps
          # Kustomization fails dry-run and no app in the apply lands. pg_isready
          # readiness + TCP liveness (POSTGRES_USER from the credentials envFrom).
          livenessProbe:
            tcpSocket:
              port: 5432
            initialDelaySeconds: 30
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          readinessProbe:
            exec:
              command:
                - sh
                - -c
                - pg_isready -h 127.0.0.1 -U "$POSTGRES_USER"
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          resources:
            # #4422: per-Org LimitRange (#4292) enforces maxLimitRequestRatio
            # {cpu:1,memory:1} (Guaranteed QoS) — limits MUST equal requests or
            # the pod is forbidden ('cpu max limit to request ratio is 1, but
            # provided ratio is 10'). Render symmetric requests==limits.
            requests:
              cpu: 250m
              memory: 256Mi
            limits:
              cpu: 250m
              memory: 256Mi
          volumeMounts:
            # #5445 — mount a SUBDIRECTORY of the PVC, never its root.
            #
            # Every Huawei EVS volume is ext4, so the filesystem root always
            # contains a lost+found directory. Mounting the PVC root directly
            # at PGDATA therefore hands initdb a non-empty directory and it
            # refuses to initialise the cluster at all:
            #
            #   initdb: error: directory /var/lib/postgresql/data exists but is not empty
            #   initdb: detail: It contains a lost+found directory, perhaps due
            #                   to it being a mount point.
            #
            # Live on hw290 theta-corp this was postgres CrashLoopBackOff x50,
            # with umami and uptime-kuma crashlooping downstream of it — on an
            # Org where the GitOps write, the Flux apply and all 20 inventory
            # entries were green. It is the gap between provisioned and
            # working, and it is invisible to any check that stops at Flux.
            #
            # subPath is preferred over a PGDATA env override because it does
            # not depend on the image honouring PGDATA.
            - name: pgdata
              mountPath: /var/lib/postgresql/data
              subPath: pgdata
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
      volumes:
        - name: pgdata
          persistentVolumeClaim:
            claimName: postgres-data
        - name: initdb
          configMap:
            name: postgres-initdb
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: %s
spec:
  selector:
    app: postgres
  ports:
    - port: 5432
      targetPort: 5432
`, ns, password, primaryDB, ns, indentBlock(initSQL, "    "), ns, diskGB, ns, backupsEnabled, replicas, proxyImage("postgres:16-alpine", registryMirror), ns)
}

// generateCNPGPair renders the bp-cnpg-pair HelmRelease — the Pillar-3
// active-hot-standby Postgres install path for any postgres-backed
// marketplace app (TBD-V17 #2068). Before this PR the cluster-pair
// pattern existed only inline inside bp-wordpress-tenant's
// templates/cnpg-cluster.yaml; non-WP customer apps (Umami / NocoDB /
// Gitea / Plane / Twenty / Listmonk / Chatwoot / the canonical
// Postgres-backed bundle from CLAUDE.md §0 step 1b) had no install
// path to the cluster-pair shape, breaking Pillar 3 for every
// non-WordPress customer journey.
//
// The HelmRelease is targetNamespace=apps (matches the legacy
// generatePostgres deployment's ns + the postgres-credentials Secret
// the app expects) and chart-pinned via the bp-cnpg-pair HelmRepository
// the bootstrap-kit publishes on the tenant vCluster. Per-app database
// names (db_<appSlug>) are wired via cnpgPair.primary.bootstrap and
// per-database SQL extension blocks so co-installed postgres-backed
// apps still see their own database.
//
// The chart's own values.yaml (platform/cnpg-pair/chart/values.yaml)
// ships:
//   - replication.mode: sync (synchronous remote_apply per #2064 ship)
//   - replication.sync.commit: remote_apply
//   - replication.sync.numSync: 1
//   - clusterMesh.enabled: true
//   - clusterMesh.affinity: local
//
// We rely on those defaults rather than overriding so per-Sovereign
// overlays remain free to relax for forensic / lab runs. Our overlay
// supplies ONLY the per-app surface (region pair, instance counts,
// storage size, bootstrap db/owner credentials Secret).
//
// Image tag is intentionally NOT set here — the bp-cnpg-pair chart's
// values.yaml fail-fasts on an empty image.tag (Principle #4a) so the
// per-Sovereign overlay MUST pin a SHA digest. Leaving it empty in the
// orchestrator output keeps the contract that overlay layer is the
// single owner of image pins.
// #4297 — generateCNPGPair emits a HelmRelease CR. Unlike the plain-manifest db
// paths (generatePostgres/MySQL/Redis), an HR CR cannot simply be redirected
// INTO the vcluster by the apps-sync Kustomization: vclusters run NO in-cluster
// helm-controller, so the synced HR StateErrors (#3055). The robust model is
// HelmRelease.spec.kubeConfig — the HR CR + its HelmRepository live on the HOST
// (reconciled by the host helm-controller / flux-system kustomization), and the
// host helm-controller runs `helm install` INTO the vcluster API named by
// kubeSecret. The chart's resources (CNPG Clusters etc.) then land in the
// vcluster `targetNamespace` (= ns, "apps").
//
// The `postgres-credentials` Secret is NOT chart-templated — the marketplace
// app pods read its POSTGRES_* keys directly. It must therefore live INSIDE the
// vcluster alongside the app pods, so it is emitted SEPARATELY by
// generateCNPGPairSecret into the apps/ tree (Kustomization-redirected), NOT
// here on the host.
//
// ns is the chart TARGET namespace (= the Org `<slug>`): for the vcluster tier
// the host helm-controller installs the chart into the vcluster's `<slug>` ns
// (where the apps-sync-rewritten app pods + postgres-credentials Secret live);
// for the host tier the chart installs into the host `<slug>` ns.
//
// kubeSecret is the flux-system-co-located kubeconfig mirror the host
// helm-controller installs THROUGH:
//   - PRIMARY side, vcluster tier → `tenant-<slug>-kubeconfig` (the Org
//     vcluster, on region A's host). Empty for the host tier (no vcluster →
//     the HR reconciles on the host + chart installs into the host `<slug>`).
//   - REPLICA side → ALWAYS the region-B host-cluster kubeconfig
//     (`sovereign-replica-region-kubeconfig`), so the standby Cluster lands in
//     region B where its region-B node-affinity matches the local nodes
//     (#4282/#4275). Region B is a DIFFERENT physical cluster than region A's
//     host Flux, so a kubeConfig is mandatory even for the host tier.
//
// #4282/#4275 — SPLIT-SIDE. opt.side selects WHICH half of the pair this HR
// renders (the chart is side-gated: side=primary renders ONLY the primary
// Cluster + mesh Service, side=replica renders ONLY the replica Cluster +
// failover probe). The two sides MUST be installed into DIFFERENT regions'
// clusters; a single HR (the keystone shape) could only ever reach one. The
// HR / HelmRepository / releaseName are suffixed `-<side>` so the primary and
// replica HRs (both authored in flux-system) never collide.

// cnpgPairOpts is the option bag for generateCNPGPair — keeping the call site
// readable now that side + per-side kubeConfig are threaded through.
type cnpgPairOpts struct {
	side          string // "primary" | "replica" — selects the chart's side-gate
	ns            string // chart targetNamespace (= Org <slug>)
	password      string
	apps          []string
	cfg           map[string]any
	primaryRegion string
	replicaRegion string
	storageClass  string
	kubeSecret    string // flux-system kubeconfig mirror the HR installs THROUGH
}

func generateCNPGPair(opt cnpgPairOpts) string {
	ns := opt.ns
	side := strings.TrimSpace(opt.side)
	if side != "primary" && side != "replica" {
		// Defensive: callers pass a literal; an unexpected side would render a
		// chart the side-gate fails. Default to primary (the chart's own
		// default) + log so the gap is visible rather than emitting garbage.
		slog.Warn("generateCNPGPair: unexpected side — defaulting to primary",
			"side", opt.side)
		side = "primary"
	}

	sortedApps := sortStrings(append([]string{}, opt.apps...))
	primaryDB := "appdb"
	if len(sortedApps) > 0 {
		primaryDB = "db_" + sortedApps[0]
	}

	// configSchema knobs (same range gates as the legacy postgres
	// path so a customer who flips active_hot_standby on doesn't see
	// a different validation surface for `replicas` / `disk_gb`).
	// `replicas` here maps to CNPG `instances` per region; min 3 is
	// the bp-cnpg-pair chart's own configSchema floor for primary
	// (active-hot-standby HA requires a 3-node quorum per region). If
	// the customer chose 1-2 we clamp to 3 and log loud so the gap is
	// operator-visible.
	requested := readIntCfg(opt.cfg, "replicas", 3, 1, 5, "postgres")
	instances := requested
	if instances < 3 {
		slog.Warn("generateCNPGPair: replicas < 3 incompatible with active-hot-standby quorum — clamping to 3",
			"app", "postgres", "requested", requested, "clamped", 3)
		instances = 3
	}
	// Storage: CNPG ships its own PVC management per Cluster CR, so
	// we surface disk_gb 1:1 (default matches bp-cnpg-pair chart
	// default of 100Gi when customer doesn't pick — but we default to
	// the configSchema's 5GB floor for predictability + parity with
	// the legacy single-cluster path).
	diskGB := readIntCfg(opt.cfg, "disk_gb", 5, 1, 500, "postgres")

	// HR-level kubeConfig. Flux's HelmRelease secretRef accepts name+key only —
	// the namespace is implied from the HR's OWN namespace — so for the secretRef
	// to resolve the HR (+ its HelmRepository) must live alongside the mirror in
	// flux-system. PRIMARY side, host tier carries NO kubeConfig (no vcluster →
	// the HR reconciles on region A's host + chart installs into the host
	// `<slug>` ns). REPLICA side ALWAYS carries a kubeConfig (region B is a
	// separate cluster). When kubeSecret is set, author the HR in flux-system
	// next to the mirror; otherwise author it in the host `<slug>` ns.
	hrNamespace := ns
	kubeConfigBlock := ""
	if opt.kubeSecret != "" {
		hrNamespace = "flux-system"
		kubeConfigBlock = fmt.Sprintf(`
  kubeConfig:
    secretRef:
      name: %s
      key: config`, opt.kubeSecret)
	}

	// Per-side resource names so the primary + replica HRs (both in flux-system)
	// never collide. The releaseName stays distinct too so the host
	// helm-controller tracks two independent releases.
	hrName := "bp-cnpg-pair-" + side
	releaseName := "cnpg-pair-" + side

	return fmt.Sprintf(`# bp-cnpg-pair (%s side) — Pillar-3 active-hot-standby install path for
# postgres-backed marketplace apps (TBD-V17 #2068). A 2-region Sovereign is
# TWO separate clusters joined by Cilium ClusterMesh, so the pair is installed
# SPLIT-SIDE (chart 0.2.0): this HR renders ONLY the %s Cluster CR + its
# side-local resources, pinned to its region's nodes via node-affinity.
#
# #4282/#4275 — the %s side is installed INTO its OWN region's cluster via the
# HR-level spec.kubeConfig below. The PRIMARY side lands in region A (the host
# Flux's own cluster / the Org vcluster on it); the REPLICA side lands in
# region B (sovereign-replica-region-kubeconfig), where the
# openova.io/region=%s nodes the replica node-affinity requires actually live.
# Before this split the standby Cluster landed in region A and its
# region-B affinity matched 0/N nodes → the *-pgbasebackup pod hung Pending
# and the region-kill pillar had no standby to fail over to.
#
# Synchronous WAL replication over Cilium ClusterMesh; failover-readiness probe
# + audit ConfigMap wired by the chart's own side-gated templates
# (platform/cnpg-pair/chart/templates/*). The companion postgres-credentials
# Secret is emitted separately into the apps/ tree (generateCNPGPairSecret) so
# the in-vcluster app pods can read it.
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: %s
  namespace: %s
spec:
  type: oci
  interval: 15m
  # url is cutover-aware (#5527, cutover_aware_5527.go): public catalog
  # pre-cutover, Sovereign-local Harbor once the step-07 fact is stamped.
  url: %s
  secretRef:
    name: ghcr-pull
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %s
  namespace: %s
  labels:
    catalyst.openova.io/component: cnpg-pair
    catalyst.openova.io/cnpg-pair-side: %s
    openova.io/category: customer-facing-capability
    openova.io/pillar: "3"
spec:
  interval: 15m
  releaseName: %s
  targetNamespace: %s%s
  chart:
    spec:
      chart: bp-cnpg-pair
      # 0.2.7 — split-side topology (cnpgPair.side primary|replica): each
      # side renders ONLY its own Cluster CR so the replica's region-B
      # node-affinity matches the cluster it is APPLIED to. ≤0.1.x (the
      # keystone's 0.1.2 pin) rendered BOTH Clusters in one release + relied
      # on node-affinity to schedule each to its region — IMPOSSIBLE on two
      # separate ClusterMesh clusters, so the replica wedged Pending forever
      # in region A (#4282). The side-gate landed in 0.2.0; 0.2.7 is the
      # current published chart.
      version: 0.2.7
      sourceRef:
        kind: HelmRepository
        name: %s
        namespace: %s
  install:
    timeout: 15m
    remediation:
      retries: 3
  upgrade:
    timeout: 15m
    remediation:
      retries: 3
  values:
    cnpgPair:
      enabled: true
      # Which half of the pair this HR renders. Both HRs carry the SAME full
      # values (both regions + both storage blocks) so validateRegions + the
      # replica's externalClusters source resolve; only side + kubeConfig differ.
      side: %s
      primary:
        region: %s
        instances: %d
        storage:
          size: %dGi
          # Provider-aware block-storage class (#4060): hcloud-volumes on
          # Hetzner, evs-ssd on Huawei. A cross-provider hardcode pins
          # every customer-Org CNPG PVC Pending on the wrong cloud.
          storageClass: %s
        bootstrap:
          database: %s
          owner: app
      replica:
        region: %s
        instances: %d
        storage:
          size: %dGi
          storageClass: %s
      # replication.mode + clusterMesh + audit defaults come from the
      # chart's own values.yaml (sync remote_apply + numSync=1 +
      # clusterMesh.enabled + audit subjects). Per-Sovereign overlays
      # may patch values via Flux postBuild substitute; we intentionally
      # do NOT override here.
`,
		side, side, side, opt.replicaRegion, // header comment
		hrName, hrNamespace, // HelmRepository name/ns
		catalogOCIBase(), // cutover-aware OCI base (#5527)
		hrName, hrNamespace, side, releaseName, ns, kubeConfigBlock, // HR metadata + spec head
		hrName, hrNamespace, // sourceRef name/ns
		side,                                                              // cnpgPair.side
		opt.primaryRegion, instances, diskGB, opt.storageClass, primaryDB, // primary block
		opt.replicaRegion, instances, diskGB, opt.storageClass, // replica block
	)
}

// generateCNPGPairSecret emits the standalone postgres-credentials Secret the
// marketplace app pods read (POSTGRES_USER/PASSWORD/DB). It is NOT chart-
// templated, so it must live INSIDE the vcluster alongside the app pods — it is
// emitted into the apps/ tree (Kustomization-redirected into the vcluster),
// separate from the host-reconciled HelmRelease (#4297). ns is the in-vcluster
// app namespace ("apps"); targetNamespace on the apps-sync Kustomization rewrites
// it to the vcluster's `<slug>` on apply.
func generateCNPGPairSecret(ns, password string, apps []string) string {
	sortedApps := sortStrings(append([]string{}, apps...))
	primaryDB := "appdb"
	if len(sortedApps) > 0 {
		primaryDB = "db_" + sortedApps[0]
	}
	return fmt.Sprintf(`# postgres-credentials — read by the in-vcluster marketplace app pods
# (POSTGRES_HOST=postgres, POSTGRES_USER=app, etc.). Lives inside the vcluster
# (apps/ tree) so it co-locates with the app pods; the CNPG Clusters are
# installed by the host-reconciled bp-cnpg-pair HelmRelease (#4297).
apiVersion: v1
kind: Secret
metadata:
  name: postgres-credentials
  namespace: %s
type: Opaque
stringData:
  POSTGRES_USER: app
  POSTGRES_PASSWORD: "%s"
  POSTGRES_DB: %s
`, ns, password, primaryDB)
}

func generateMySQL(ns, password string, apps []string, cfg map[string]any, registryMirror string) string {
	// Per-app database isolation: create db_<appSlug> for each mysql-backed
	// app so co-installed apps (e.g. wordpress + ghost) don't collide on a
	// shared schema. MYSQL_DATABASE bootstraps the first one; an init script
	// in /docker-entrypoint-initdb.d/ creates the rest and grants them to the
	// `app` user.
	sortedApps := sortStrings(append([]string{}, apps...))
	primaryDB := "appdb"
	if len(sortedApps) > 0 {
		primaryDB = "db_" + sortedApps[0]
	}
	var initSQL string
	for _, a := range sortedApps {
		db := "db_" + a
		if db == primaryDB {
			continue
		}
		initSQL += fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`;\nGRANT ALL PRIVILEGES ON `%s`.* TO 'app'@'%%';\n", db, db)
	}
	initSQL += "FLUSH PRIVILEGES;\n"

	// TBD-V27 (#2042) — bind customer-chosen configSchema values.
	// MySQL shares the postgres seed schema (replicasField+diskField+
	// backupField from seed.go:699-701). MySQL primary-replica replication
	// is not configured in this manifest so replicas>1 currently runs
	// independent stateful pods sharing one PVC, which is wrong — clamp to
	// 1 for safety and log loud. disk_gb threads to the PVC unchanged.
	replicas := readIntCfg(cfg, "replicas", 1, 1, 5, "mysql")
	if replicas != 1 {
		slog.Warn("generateMySQL: customer requested replicas>1 but MySQL primary-replica is not yet wired — clamping to 1",
			"requested", replicas, "namespace", ns)
		replicas = 1
	}
	diskGB := readIntCfg(cfg, "disk_gb", 5, 1, 500, "mysql")
	backupsEnabled := readBoolCfg(cfg, "backups_enabled", false, "mysql")
	logUnknownKeys(cfg, []string{"replicas", "disk_gb", "backups_enabled"}, "mysql")

	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: mysql-credentials
  namespace: %s
type: Opaque
stringData:
  MYSQL_ROOT_PASSWORD: "%s"
  MYSQL_USER: app
  MYSQL_PASSWORD: "%s"
  MYSQL_DATABASE: %s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: mysql-initdb
  namespace: %s
data:
  init.sql: |
%s
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mysql-data
  namespace: %s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: %dGi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mysql
  namespace: %s
  labels:
    # #4422: the singleton-db carve-out the 'multi-replica-drainability' policy
    # excludes on lives on the Deployment's OWN metadata.labels (Kyverno's
    # resources.selector matches the resource's top-level labels). Mirrors the
    # pod-template label below + the cnpg.io/cluster exempt on backup-configured.
    openova.io/singleton-db: "true"
  annotations:
    openova.io/backups-enabled: "%t"
spec:
  replicas: %d
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: mysql
  template:
    metadata:
      labels:
        app: mysql
        # #4422: per-Org Kyverno 'multi-replica-drainability' (replicas-at-least-two)
        # demands spec.replicas>=2, but this is a single-PVC stateful DB with
        # strategy:Recreate and replicas explicitly clamped to 1 (primary-replica
        # is not yet wired) — a 2nd replica sharing one RWO PVC would corrupt data.
        # Stamp the singleton-db carve-out label so the policy exempts it in ANY
        # per-Org namespace (mirrors the cnpg.io/cluster label-exempt on
        # backup-configured #3971). The policy doc itself lists
        # 'explicitly-singleton services (databases ...)' as legitimate.
        openova.io/singleton-db: "true"
      annotations:
        # #4422: Kyverno 'backup-configured' (pvc-must-be-velero-backed, enforce)
        # blocks the PVC unless the owning Pod annotates its volume for Velero
        # file-level backup. Annotate mysqldata so the PVC admits AND is backed up.
        velero.io/backup-volumes: mysqldata
    spec:
      containers:
        - name: mysql
          image: %s
          ports:
            - containerPort: 3306
          envFrom:
            - secretRef:
                name: mysql-credentials
          # #4389: the Sovereign's Kyverno 'probes-present' policy
          # (containers-must-have-liveness-and-readiness, svc-fail/enforce)
          # blocks any Deployment whose containers omit BOTH probes — without
          # these the whole vcluster/apps Kustomization fails dry-run and
          # NEITHER mysql NOR the co-installed app (wordpress) lands. TCP
          # liveness + 'mariadb-admin ping' readiness (mariadb:11 ships the
          # client; root creds come from the mysql-credentials envFrom).
          livenessProbe:
            tcpSocket:
              port: 3306
            initialDelaySeconds: 30
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          readinessProbe:
            exec:
              command:
                - sh
                - -c
                - mariadb-admin ping -h 127.0.0.1 -uroot -p"$MYSQL_ROOT_PASSWORD"
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          resources:
            # #4422: per-Org LimitRange (#4292) enforces maxLimitRequestRatio
            # {cpu:1,memory:1} (Guaranteed QoS) — limits MUST equal requests or
            # the pod is forbidden ('cpu max limit to request ratio is 1, but
            # provided ratio is 10'), which is why the funnel WordPress mysql
            # Deployment created ZERO pods. Render symmetric requests==limits.
            requests:
              cpu: 250m
              memory: 256Mi
            limits:
              cpu: 250m
              memory: 256Mi
          volumeMounts:
            - name: mysqldata
              mountPath: /var/lib/mysql
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
      volumes:
        - name: mysqldata
          persistentVolumeClaim:
            claimName: mysql-data
        - name: initdb
          configMap:
            name: mysql-initdb
---
apiVersion: v1
kind: Service
metadata:
  name: mysql
  namespace: %s
spec:
  selector:
    app: mysql
  ports:
    - port: 3306
      targetPort: 3306
`, ns, password, password, primaryDB, ns, indentBlock(initSQL, "    "), ns, diskGB, ns, backupsEnabled, replicas, proxyImage("mariadb:11", registryMirror), ns)
}

// indentBlock prefixes every non-empty line of s with indent. Used to embed a
// multi-line SQL blob inside a YAML block scalar at the right indentation.
func indentBlock(s, indent string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out strings.Builder
	for i, ln := range lines {
		if i > 0 {
			out.WriteString("\n")
		}
		if ln == "" {
			continue
		}
		out.WriteString(indent)
		out.WriteString(ln)
	}
	return out.String()
}

func generateRedis(ns, registryMirror string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: %s
  labels:
    # #4422: singleton-db carve-out on the Deployment's OWN metadata.labels so
    # the 'multi-replica-drainability' policy's resources.selector excludes it.
    openova.io/singleton-db: "true"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
        # #4422: single-replica cache — exempt from 'multi-replica-drainability'
        # (replicas-at-least-two) via the singleton carve-out label, same as the
        # stateful DBs above (the policy doc lists singleton services as
        # legitimate). No PVC, so no velero.io/backup-volumes annotation needed.
        openova.io/singleton-db: "true"
    spec:
      containers:
        - name: redis
          image: %s
          ports:
            - containerPort: 6379
          # #4389: Kyverno 'probes-present' (svc-fail/enforce) blocks any
          # Deployment whose containers omit BOTH probes → the vcluster/apps
          # Kustomization fails dry-run. 'redis-cli ping' (valkey ships it) +
          # TCP liveness on 6379.
          livenessProbe:
            tcpSocket:
              port: 6379
            initialDelaySeconds: 15
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          readinessProbe:
            exec:
              command:
                - sh
                - -c
                - redis-cli -h 127.0.0.1 ping | grep -q PONG
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          resources:
            # #4422: per-Org LimitRange (#4292) maxLimitRequestRatio 1 →
            # symmetric requests==limits (Guaranteed QoS).
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 100m
              memory: 128Mi
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: %s
spec:
  selector:
    app: redis
  ports:
    - port: 6379
      targetPort: 6379
`, ns, proxyImage("valkey/valkey:8-alpine", registryMirror), ns)
}

// qosResources returns the (requests, limits) cpu/memory pair for an app
// container under a given plan slug (#4292). Fixed tiers (S/M/L/XL) render
// limits==requests so the pod is Guaranteed QoS — and so the per-Org
// LimitRange's maxLimitRequestRatio {cpu:1,memory:1} admits it. Flexi (and any
// unknown slug treated as Flexi-shaped here only for the empty case) keeps the
// historical asymmetric shape (requests<limits) → Burstable QoS, matching the
// on-demand plan. The request floor stays the app's own CPUMilli/RAMMI; the
// Guaranteed limit is raised to the request so the QoS class flips without
// shrinking any app below its declared need.
func qosResources(planSlug, reqCPU, reqMem string) (cpu string, mem string, guaranteed bool) {
	switch strings.ToLower(strings.TrimSpace(planSlug)) {
	case "flexi":
		// Burstable: historical asymmetric ceiling.
		return "500m", "512Mi", false
	default:
		// s/m/l/xl (and empty → smallest paid): Guaranteed — limit==request.
		return reqCPU, reqMem, true
	}
}

func generateAppDeployment(ns, slug, planSlug, appSlug string, spec AppSpec, dbPassword, parentDomain string) string {
	// QoS split (#4292): fixed tiers (S/M/L/XL) render limits==requests →
	// Guaranteed (also what the per-Org LimitRange maxLimitRequestRatio
	// {cpu:1,memory:1} requires to admit the pod); Flexi keeps the historical
	// asymmetric ceiling → Burstable.
	limCPU, limMem, _ := qosResources(planSlug, spec.CPUMilli, spec.RAMMI)

	// Alphabetize static env so the generated YAML is stable across commits
	// (Go map iteration is randomized → would cause noisy diffs on every
	// regenerate, which is hostile to PR review).
	staticKeys := make([]string, 0, len(spec.EnvVars))
	for k := range spec.EnvVars {
		staticKeys = append(staticKeys, k)
	}
	staticKeys = sortStrings(staticKeys)

	var envLines string
	for _, k := range staticKeys {
		val := strings.ReplaceAll(spec.EnvVars[k], "TENANT", slug)
		envLines += fmt.Sprintf("            - name: %s\n              value: \"%s\"\n", k, val)
	}

	// Each app gets its own database inside the shared server so tenants
	// can co-install multiple db-backed apps (e.g. wordpress + ghost on
	// mysql) without stepping on each other's tables. The db-*.yaml init
	// script creates db_<appSlug> and grants it to the `app` user.
	appDB := "db_" + appSlug
	switch spec.NeedsDB {
	case "postgres":
		switch spec.DBEnvStyle {
		case "listmonk":
			// Listmonk's config.toml is baked into the image with host=localhost.
			// The app reads the [db] block only; it does NOT honour DATABASE_URL.
			// We override individual fields via LISTMONK_db__* envs (documented
			// convention: [db] host -> LISTMONK_db__host, etc.). Issue #101.
			envLines += fmt.Sprintf(`            - name: LISTMONK_db__host
              value: "postgres"
            - name: LISTMONK_db__port
              value: "5432"
            - name: LISTMONK_db__user
              value: "app"
            - name: LISTMONK_db__password
              value: "%s"
            - name: LISTMONK_db__database
              value: "%s"
            - name: LISTMONK_db__ssl_mode
              value: "disable"
            - name: LISTMONK_db__max_open
              value: "25"
            - name: LISTMONK_db__max_idle
              value: "25"
            - name: LISTMONK_db__max_lifetime
              value: "300s"
`, dbPassword, appDB)
		default:
			envLines += fmt.Sprintf(`            - name: DATABASE_URL
              value: "postgresql://app:%s@postgres:5432/%s"
            - name: POSTGRES_HOST
              value: "postgres"
            - name: POSTGRES_PORT
              value: "5432"
            - name: POSTGRES_DATABASE
              value: "%s"
            - name: POSTGRES_USERNAME
              value: "app"
            - name: POSTGRES_PASSWORD
              value: "%s"
`, dbPassword, appDB, appDB, dbPassword)
		}
	case "mysql":
		switch spec.DBEnvStyle {
		case "bookstack":
			// linuxserver/bookstack docs advertise DB_USER/DB_PASS, but the
			// container's init script (init-bookstack-config) only copies
			// .env.example into place — it does NOT substitute DB_USER ->
			// DB_USERNAME or DB_PASS -> DB_PASSWORD. Laravel reads env vars
			// natively, but using the Laravel-native names DB_USERNAME and
			// DB_PASSWORD. Without those, Laravel falls back to the .env
			// placeholder values (database_username / database_user_password)
			// and the app fails with SQLSTATE[HY000] [1045] Access denied for
			// user 'database_username'@... — caught live on tenant
			// 'bookcheck' on 2026-05-06. We emit BOTH name pairs so the env
			// works regardless of which the LSIO upstream eventually wires.
			// APP_KEY must be a Laravel-style base64:<32-byte> string;
			// without it, init halts with "The application key is missing".
			envLines += fmt.Sprintf(`            - name: DB_HOST
              value: "mysql"
            - name: DB_PORT
              value: "3306"
            - name: DB_USER
              value: "app"
            - name: DB_USERNAME
              value: "app"
            - name: DB_PASS
              value: "%s"
            - name: DB_PASSWORD
              value: "%s"
            - name: DB_DATABASE
              value: "%s"
            - name: APP_URL
              value: "https://%s.omani.rest"
            - name: APP_KEY
              value: "%s"
`, dbPassword, dbPassword, appDB, slug, randomAppKey())
		case "ghost":
			envLines += fmt.Sprintf(`            - name: database__client
              value: "mysql"
            - name: database__connection__host
              value: "mysql"
            - name: database__connection__port
              value: "3306"
            - name: database__connection__user
              value: "app"
            - name: database__connection__password
              value: "%s"
            - name: database__connection__database
              value: "%s"
`, dbPassword, appDB)
		default:
			envLines += fmt.Sprintf(`            - name: WORDPRESS_DB_HOST
              value: "mysql"
            - name: WORDPRESS_DB_USER
              value: "app"
            - name: WORDPRESS_DB_PASSWORD
              value: "%s"
            - name: WORDPRESS_DB_NAME
              value: "%s"
            - name: MYSQL_HOST
              value: "mysql"
            - name: MYSQL_USER
              value: "app"
            - name: MYSQL_PASSWORD
              value: "%s"
            - name: MYSQL_DATABASE
              value: "%s"
`, dbPassword, appDB, dbPassword, appDB)
		}
	}

	// Optional per-app PVC mount (Ghost's /var/lib/ghost/content).
	var pvcManifest, volumeMounts, volumes string
	if spec.ContentPath != "" {
		pvcManifest = fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s-data
  namespace: %s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 2Gi
---
`, appSlug, ns)
		volumeMounts = fmt.Sprintf(`          volumeMounts:
            - name: content
              mountPath: %s
`, spec.ContentPath)
		volumes = fmt.Sprintf(`      volumes:
        - name: content
          persistentVolumeClaim:
            claimName: %s-data
`, appSlug)
	}

	// Optional initContainer for apps whose binary ships a --install flag
	// that must run once before the main container starts (listmonk — #101).
	var initContainers string
	if spec.InitCommand != "" {
		initContainers = fmt.Sprintf(`      initContainers:
        - name: %s-init
          image: %s
          command: ["sh", "-c"]
          args: ["%s"]
          env:
%s`, appSlug, spec.Image, spec.InitCommand, envLines)
	}

	// Cilium-Gateway HTTP exposure for the Deployment-shaped app (#3376 — the
	// funnel cart WordPress 404-without-route gap). A Sovereign runs the
	// Cilium Gateway API, NOT
	// traefik — the host generateHostIngress() networking.k8s.io/v1 Ingress is
	// INERT (never reconciled; its per-host `<app>-tls` Certificate sits False
	// forever, no HTTP-01 solver) and is dropped entirely by the de-vcluster'd
	// per-Org apps tree (#4384 GeneratePerOrgAppsTree). Without a route the
	// purchased app (e.g. WordPress) is healthy in-pod yet returns public 404.
	// This HTTPRoute is co-located with the Deployment+Service in the SAME
	// app-<x>.yaml doc so it lands wherever the app lands — INSIDE the Org
	// vCluster for the paid tier (the #4297 keystone redirects the apps tree
	// there via spec.kubeConfig), on the host `<slug>` ns for free/S — binding
	// the actual Service, never an empty host shell. It attaches the per-Org
	// host to the dedicated console Gateway whose `*.<pool>` wildcard TLS
	// listener terminates TLS (so NO per-host Certificate is needed), mirroring
	// generateOpenClawHR / generateStalwartHR / platform/keycloak httproute.yaml.
	// The reserved-entity gateway→pod hop (fromEntities:[ingress]) is already
	// admitted namespace-wide by the org-controller's ciliumNetworkPolicyTemplate
	// (endpointSelector:{}, fromEntities:[ingress,host,remote-node]) force-included
	// in every per-Org apps tree, so NO per-app CNP is emitted here.
	httpRoute := generateAppHTTPRoute(ns, slug, appSlug, parentDomain)

	return fmt.Sprintf(`%sapiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    openova.io/tenant: "%s"
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
        openova.io/tenant: "%s"
    spec:
%s      containers:
        - name: %s
          image: %s
          ports:
            - containerPort: %d
          env:
%s          # #4389: the Sovereign's Kyverno 'probes-present' policy
          # (containers-must-have-liveness-and-readiness, svc-fail/enforce)
          # rejects any Deployment whose containers omit BOTH probes — without
          # these the whole vcluster/apps Kustomization fails dry-run and the
          # app (+ any co-installed app in the same apply) never lands. A
          # generic TCP probe on the app's own port works for every
          # marketplace app (they all serve HTTP on spec.Port).
          livenessProbe:
            tcpSocket:
              port: %d
            initialDelaySeconds: 30
            periodSeconds: 15
            timeoutSeconds: 5
            failureThreshold: 6
          readinessProbe:
            tcpSocket:
              port: %d
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 6
          resources:
            requests:
              cpu: %s
              memory: %s
            limits:
              cpu: %s
              memory: %s
%s%s---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s
  ports:
    - port: 80
      targetPort: %d
%s`, pvcManifest,
		appSlug, ns, appSlug, slug,
		appSlug, appSlug, slug,
		initContainers,
		appSlug, spec.Image, spec.Port,
		envLines,
		spec.Port, spec.Port,
		spec.CPUMilli, spec.RAMMI,
		limCPU, limMem,
		volumeMounts, volumes,
		appSlug, ns, appSlug, spec.Port,
		httpRoute)
}

// appsGatewayName / appsGatewayNamespace are the dedicated console Gateway the
// per-Org Deployment-shaped apps attach to — the SAME Gateway the HelmRelease-
// shaped per-Org apps use (generateOpenClawHR / generateStalwartHR parentRef).
// Its `*.<pool>` wildcard TLS listener terminates TLS for every per-Org host, so
// a Deployment-shaped app needs NO per-host Certificate. Never hardcoded inline
// (Inviolable Principle #4) — surfaced as named constants so the route and the
// HR overlays stay in lockstep.
const (
	appsGatewayName      = "cilium-gateway-console"
	appsGatewayNamespace = "kube-system"
)

// generateAppHTTPRoute emits the Gateway-API HTTPRoute that routes a
// Deployment-shaped per-Org app's public host (<app>.<slug>.<parentDomain> —
// the SAME convention generateOpenClawHR uses for openclaw.<slug>.<parent>) to
// its in-boundary Service on :80. It is co-located with the Deployment+Service
// in app-<x>.yaml so it lands on whatever boundary the app lands on (the Org
// vCluster for paid tiers, the host `<slug>` ns for free/S) and references the
// PLAIN Service name (the same boundary), exactly as the chart-emitted openclaw
// HTTPRoute references its own plain `<fullname>-controller` Service.
//
// parentDomain is the Sovereign's org-pool parent zone (g.parentDomain(), e.g.
// "omani.homes"); it falls back to parentDomainDefault upstream so the host is
// never a bare `<app>.<slug>.`. The route attaches to the dedicated console
// Gateway (appsGatewayName/appsGatewayNamespace) whose wildcard TLS listener
// terminates TLS — no per-host cert. The gateway→pod reserved-entity hop is
// admitted by the org-controller's namespace-wide CNP, so no per-app CNP here.
func generateAppHTTPRoute(ns, slug, appSlug, parentDomain string) string {
	host := fmt.Sprintf("%s.%s.%s", appSlug, slug, parentDomain)
	return fmt.Sprintf(`---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: app-%s
  namespace: %s
  labels:
    app: %s
    openova.io/tenant: "%s"
    catalyst.openova.io/component: per-org-app-route
spec:
  parentRefs:
    - name: %s
      namespace: %s
  hostnames:
    - %s
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: %s
          port: 80
`, appSlug, ns, appSlug, slug,
		appsGatewayName, appsGatewayNamespace,
		host, appSlug)
}

// hostSyncedServiceName is the name the vCluster syncer reflects an in-vcluster
// Service to on the HOST cluster: `<svc>-x-<host-ns>-x-vcluster`. The host ns is
// the org-controller-owned `<slug>` boundary (the apps Kustomization's
// targetNamespace), so a WordPress Service authored inside the vcluster surfaces
// host-side as `wordpress-x-<slug>-x-vcluster`. Live-confirmed on hw240 acme:
// `wordpress-x-acme-x-vcluster`. Kept in lockstep with generateHostIngress's
// inline `syncedName` (issue #117) — the ONE canonical synced-name derivation.
func hostSyncedServiceName(appSlug, hostNS string) string {
	return fmt.Sprintf("%s-x-%s-x-vcluster", appSlug, hostNS)
}

// generateHostNativeAppRoute emits a HOST-NATIVE Gateway-API HTTPRoute (in the
// host `<slug>` ns) that routes a VCLUSTER-tier app's public host
// (`<app>.<slug>.<parentDomain>`) to the SYNCED Service the vcluster syncer
// reflects host-side (`<app>-x-<slug>-x-vcluster:80`).
//
// #4993 — the DURABLE FIX for the vcluster-tier 404. generateAppHTTPRoute
// co-locates the app's HTTPRoute with the Deployment+Service INSIDE the Org
// vcluster (the apps tree is kubeConfig-targeted into the vcluster for paid M+
// tiers). That in-vcluster route was expected to reach the host via
// `sync.toHost.customResources.httproutes`, but loft vcluster 0.33.4 registers
// NO httproute reflecting controller (only the CRD import + a quota evaluator —
// proven live on hw240: `vcluster-0 -c syncer` logs "Created service/…/networkpolicy
// syncer" but never "Created httproute syncer"), so the route never reaches the
// host Cilium Gateway and the app 404s even with pods Running. This host-native
// route lives in the ALWAYS-host-applied `vcluster/host-apps/` tree (like the
// org-controller CNP) and binds the SYNCED Service directly on the host — the
// SAME host-native model the per-Org console route already uses (tenant_route.go,
// `catalyst-ui-console-<slug>-…` in catalyst-system), never relying on vcluster
// sync. Live-proven shape on hw240 acme (`app-wordpress-hostnative-4991proof`:
// Accepted+ResolvedRefs, serves 200/302 at wordpress.acme.omani.homes).
//
// Parents to the dedicated console Gateway (appsGatewayName/appsGatewayNamespace)
// whose `*.<pool>` wildcard TLS listener terminates TLS — no per-host cert. The
// gateway→pod reserved-entity hop is admitted namespace-wide by the
// org-controller's ciliumNetworkPolicyTemplate (endpointSelector:{},
// fromEntities:[ingress,host,remote-node]) on `<slug>`, so no per-app CNP is
// needed. Emitted ONLY for the vcluster tier — the host tier (free/S) runs the
// app + its plain Service in the host `<slug>` ns directly, where the co-located
// generateAppHTTPRoute already routes and no synced name exists.
func generateHostNativeAppRoute(hostNS, slug, appSlug, parentDomain string) string {
	host := fmt.Sprintf("%s.%s.%s", appSlug, slug, parentDomain)
	synced := hostSyncedServiceName(appSlug, hostNS)
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: app-%s-hostroute
  namespace: %s
  labels:
    app: %s
    openova.io/tenant: "%s"
    catalyst.openova.io/component: per-org-app-hostroute
spec:
  parentRefs:
    - name: %s
      namespace: %s
  hostnames:
    - %s
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: %s
          port: 80
`, appSlug, hostNS, appSlug, slug,
		appsGatewayName, appsGatewayNamespace,
		host, synced)
}

// generateProvisioningTenantRBAC emits a Role + RoleBinding that gives the
// org-services/provisioning ServiceAccount the minimum tenant-scoped permissions it
// needs during teardown:
//
//   - patch/delete on the HelmRelease named "vcluster" (to strip finalizers
//     as a last-resort if the namespace won't GC).
//   - patch/delete on Flux Kustomization CRs (legacy pre-#97 tenants that
//     kept their sync CR inside the tenant NS instead of flux-system).
//   - get/list on secrets (DB password lookup for day-2 installs and
//     mirroring the vc-vcluster kubeconfig).
//
// These permissions are granted ONLY inside this tenant's namespace, which
// is why the whole thing is a Role and not a ClusterRole — see issue #75,
// which flagged the old cluster-wide delete on kustomizations as capable of
// wiping flux-system's own Kustomization CRs if a teardown bug rolled in.
func generateProvisioningTenantRBAC(ns string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: provisioning-tenant
  namespace: %s
  labels:
    openova.io/managed-by: provisioning
rules:
  - apiGroups: ["helm.toolkit.fluxcd.io"]
    resources: ["helmreleases"]
    verbs: ["get", "list", "watch", "patch", "delete"]
  - apiGroups: ["kustomize.toolkit.fluxcd.io"]
    resources: ["kustomizations"]
    verbs: ["get", "list", "watch", "patch", "delete"]
  - apiGroups: [""]
    # #3376 — the #4785 dual-namespace kubeconfig mirror
    # (mirrorVClusterKubeconfig, handlers.go) POSTs the
    # tenant-<slug>-kubeconfig Secret into BOTH flux-system AND this tenant
    # namespace, because the per-Org application HelmReleases carry a
    # namespace-less kubeConfig.secretRef that Flux resolves in the HR's OWN
    # (tenant) namespace. flux-system write is granted by the chart Role
    # org-provisioning-fluxsystem; the tenant-NS write was missing here, so
    # 'create secrets -n <slug>' 403'd -> "create mirror secret (<slug>) 403"
    # -> provisioning failed at the vcluster step and no per-Org app ever
    # reconciled (WordPress never Ready). create/update/patch are the minimum
    # for the upsert (POST, then PUT-on-409). NO delete — the mirror never
    # removes a tenant secret; teardown drops only the flux-system copy
    # (deleteVClusterKubeconfigMirror) and the tenant-NS copy is GC'd with the
    # namespace. Least-privilege + namespaced.
    resources: ["secrets"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: [""]
    # delete needed so waitForVclusterDNSOrKick can bounce vcluster-0 when
    # the syncer's initial DNS reconciliation doesn't publish the
    # kube-dns-x-kube-system-x-vcluster service. Issues #103, #105.
    resources: ["pods"]
    verbs: ["get", "list", "watch", "delete"]
  - apiGroups: [""]
    # services verb needed for waitForVclusterDNSOrKick to read the synced
    # kube-dns-x-kube-system-x-vcluster Service to know DNS is live.
    # Without this, the DNS probe returns 403 → we think DNS isn't synced
    # → we kick vcluster-0 unnecessarily → 150s wasted per tenant.
    # Also used by pod-truth reconciler to verify tenant apps are healthy
    # regardless of provision-record freshness. Issue #115.
    resources: ["services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["cert-manager.io"]
    resources: ["certificates", "certificaterequests"]
    # patch needed so stripCertificateFinalizers can drop
    # finalizer.cert-manager.io/certificate-secret-binding at teardown;
    # without it the tenant NS can't GC because cert-manager can't
    # reconcile the delete inside a Terminating NS. Issue #86.
    verbs: ["get", "list", "watch", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: provisioning-tenant
  namespace: %s
  labels:
    openova.io/managed-by: provisioning
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: provisioning-tenant
subjects:
  - kind: ServiceAccount
    name: provisioning
    namespace: org-services
`, ns, ns)
}

// generateKustomization builds a kustomization.yaml listing the given files.
// If ns is non-empty, every resource is namespaced to it.
func generateKustomization(ns string, files map[string]string) string {
	var resources string
	names := make([]string, 0, len(files))
	for name := range files {
		if name == "kustomization.yaml" {
			continue
		}
		names = append(names, name)
	}
	// deterministic order
	for _, name := range sortStrings(names) {
		resources += fmt.Sprintf("  - %s\n", name)
	}

	if ns != "" {
		return fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: %s
resources:
%s`, ns, resources)
	}
	return fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
%s`, resources)
}

func sortStrings(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	// simple insertion sort, no need to import sort for 10ish items
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// NOTE — the per-plan resource limits (planLimits/planLimit) were retired with
// the funnel's boundary builders in Workstream A (#4290). They only ever sized
// the funnel's own vCluster syncer Pod, which the org-controller now owns.
// Workstream B (#4293) introduces a catalog-slug-keyed planQuota that renders a
// ResourceQuota + LimitRange on the org-controller's `<slug>` host namespace —
// the proper cap that materializes the plan the customer paid for.

// parentKustomizationHeader is the canonical comment + kind preamble the
// org-tenants parent kustomization.yaml carries. It mirrors the bytes the
// catalyst-api writeParentTenantsIndex emits so a self-healed parent (see
// healParentKustomization) is byte-shaped like a freshly-generated one.
const parentKustomizationHeader = `# Generated by catalyst-api/org-tenant pipeline (#804/#889/#893).
# DO NOT EDIT — re-run the orchestrator on tenant signup/teardown
# to regenerate. Lists every per-tenant overlay subdirectory
# under this path so the parent Flux Kustomization
# (rendered by bp-catalyst-platform) can enumerate them.
# helmrepositories.yaml ships the shared bp-* HelmRepositories
# the per-tenant overlays sourceRef into.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - helmrepositories.yaml`

// isParentKustomization reports whether `content` looks like a parseable
// kustomize Kustomization (as opposed to a Gitea contents-API JSON envelope,
// empty bytes, or any other corruption). The org-tenants parent is committed
// by the provisioning pipeline as raw YAML; a value that does not carry the
// `kind: Kustomization` line on a line of its own — or that begins with a JSON
// object — is corrupt and must be rebuilt, never appended to.
//
// #4265: the Gitea contents API returns `{"content":"<base64>",...}`. Before
// the #4206 ReadFile-decode fix, that envelope was committed verbatim as the
// parent kustomization.yaml; appending `  - <slug>` onto it perpetuates the
// corruption and the whole org-tenants tree (prune=true) builds to empty,
// pruning every Org's namespace. Detecting it here lets Update/Remove rebuild
// a clean parent so the file self-heals on the next provision/teardown.
func isParentKustomization(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	// A JSON object (the Gitea contents-API envelope) is never a kustomization.
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}
	for _, ln := range strings.Split(content, "\n") {
		s := strings.TrimSpace(ln)
		if s == "kind: Kustomization" || s == "kind: \"Kustomization\"" || s == "kind: 'Kustomization'" {
			return true
		}
	}
	return false
}

// parentKustomizationSlugs extracts the per-tenant overlay subdir entries
// (`  - <slug>`) from a parent kustomization, EXCLUDING the always-present
// `helmrepositories.yaml` shared file. Order is preserved; used to salvage the
// live tenant list when rebuilding a corrupted parent so siblings are not lost.
func parentKustomizationSlugs(content string) []string {
	var slugs []string
	for _, ln := range strings.Split(content, "\n") {
		s := strings.TrimSpace(ln)
		if !strings.HasPrefix(s, "- ") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(s, "- "))
		item = strings.Trim(item, `"'`)
		if item == "" || item == "helmrepositories.yaml" {
			continue
		}
		slugs = append(slugs, item)
	}
	return slugs
}

// renderParentKustomization builds the canonical parent kustomization.yaml
// from a slug list: the fixed header (incl. the helmrepositories.yaml entry)
// followed by one `  - <slug>` line per tenant, deduplicated preserving first-
// seen order. This is the single shape both the happy-path Update and the
// self-heal rebuild produce.
func renderParentKustomization(slugs []string) string {
	var b strings.Builder
	b.WriteString(parentKustomizationHeader)
	seen := map[string]struct{}{}
	for _, s := range slugs {
		if s == "" || s == "helmrepositories.yaml" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		b.WriteString("\n  - ")
		b.WriteString(s)
	}
	b.WriteString("\n")
	return b.String()
}

// healParentKustomization returns `current` unchanged when it is a valid
// kustomization; otherwise it rebuilds a clean canonical parent from whatever
// `  - <slug>` entries can be salvaged from the corrupt bytes (#4265). A Gitea
// JSON envelope carries the real slugs base64-nested inside `content`, so a
// best-effort salvage there yields nothing usable and the rebuilt parent lists
// only helmrepositories.yaml + the slug the caller is about to add/keep — the
// org-tenants Kustomization then re-enumerates the live subdirs on the next
// reconcile rather than staying wedged on an unparseable file.
func healParentKustomization(current string) string {
	if isParentKustomization(current) {
		return current
	}
	return renderParentKustomization(parentKustomizationSlugs(current))
}

// UpdateParentKustomization adds a tenant entry to the parent kustomization.
//
// The "already listed" check used to be a substring match against the literal
// "  - <slug>", which falsely triggered when <slug> was a prefix of any
// existing entry (e.g. trying to add "test" when the file already listed
// "test11" or "test13"). Live race observed 2026-05-06: tenant "test"'s
// commit silently no-op'd the parent update, leaving its directory orphan
// and Flux's tenants Kustomization unable to apply it. Fix: exact line
// match on the resources entry.
//
// #4265: a corrupt parent (Gitea contents-API JSON envelope or any non-
// kustomization) is rebuilt into a clean canonical file BEFORE the append,
// instead of perpetuating the corruption by splicing `  - <slug>` onto garbage
// (which builds the whole prune=true org-tenants tree to empty and reaps every
// Org's namespace). The parent therefore self-heals on the next provision.
func UpdateParentKustomization(current, tenantSlug string) string {
	current = healParentKustomization(current)
	entry := fmt.Sprintf("  - %s", tenantSlug)
	for _, ln := range strings.Split(current, "\n") {
		if strings.TrimRight(ln, " \t") == entry {
			return current
		}
	}
	if strings.Contains(current, "resources: []") {
		return strings.Replace(current, "resources: []", fmt.Sprintf("resources:\n%s", entry), 1)
	}
	trimmed := strings.TrimRight(current, "\n")
	return trimmed + "\n" + entry + "\n"
}

// RemoveTenantFromParentKustomization removes a tenant entry from the parent
// kustomization. Returns the current content unchanged when the tenant isn't
// listed (idempotent teardown).
//
// #4265: like UpdateParentKustomization, a corrupt parent is healed into a
// clean canonical file before the removal so a teardown can never re-commit a
// JSON-envelope parent (which would zero the prune=true org-tenants tree).
func RemoveTenantFromParentKustomization(current, tenantSlug string) string {
	current = healParentKustomization(current)
	entry := fmt.Sprintf("  - %s", tenantSlug)
	if !strings.Contains(current, entry) {
		return current
	}
	lines := strings.Split(current, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimRight(ln, " \t") == entry {
			continue
		}
		kept = append(kept, ln)
	}
	out := strings.Join(kept, "\n")
	// Collapse to `resources: []` if the list is now empty so the file stays valid.
	if strings.Contains(out, "resources:\n") && !hasListItem(out, "resources:") {
		out = strings.Replace(out, "resources:\n", "resources: []\n", 1)
	}
	return out
}

func hasListItem(content, section string) bool {
	idx := strings.Index(content, section)
	if idx < 0 {
		return false
	}
	rest := content[idx+len(section):]
	for _, ln := range strings.Split(rest, "\n") {
		if strings.HasPrefix(strings.TrimLeft(ln, " "), "- ") {
			return true
		}
		if len(strings.TrimSpace(ln)) > 0 && !strings.HasPrefix(strings.TrimLeft(ln, " "), "#") {
			return false
		}
	}
	return false
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// randomAppKey generates a Laravel-style APP_KEY of the form
// "base64:<32-byte-base64>". BookStack (lscr.io/linuxserver/bookstack) and
// other Laravel apps refuse to start when APP_KEY is missing — the linuxserver
// container halts in init with "The application key is missing, halting init!".
func randomAppKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "base64:" + base64.StdEncoding.EncodeToString(b)
}

// ExtractDBPassword scans a tenant DB manifest (db-postgres.yaml or
// db-mysql.yaml as committed by GenerateAll) and returns the password string
// baked into the Secret. Returns "" when no password can be extracted — the
// caller should fall back to generating a fresh one, but note that this will
// orphan the existing DB pods' credentials.
func ExtractDBPassword(manifestContent string) string {
	for _, key := range []string{`POSTGRES_PASSWORD: "`, `MYSQL_ROOT_PASSWORD: "`} {
		idx := strings.Index(manifestContent, key)
		if idx < 0 {
			continue
		}
		rest := manifestContent[idx+len(key):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		return rest[:end]
	}
	return ""
}
