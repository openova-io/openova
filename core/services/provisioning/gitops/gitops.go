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
}

// defaultVClusterRegistryMirror is the bootstrap Harbor proxy host.
const defaultVClusterRegistryMirror = "harbor.openova.io"

func NewManifestGenerator(basePath string) *ManifestGenerator {
	return &ManifestGenerator{BasePath: basePath}
}

// registryMirror returns the configured Harbor proxy host, falling back
// to the bootstrap default when unset.
func (g *ManifestGenerator) registryMirror() string {
	if g.RegistryMirror == "" {
		return defaultVClusterRegistryMirror
	}
	return g.RegistryMirror
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
	hostNS := "tenant-" + slug
	appNS := "apps"

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
	hostFiles := map[string]string{
		"namespace.yaml":         generateHostNamespace(hostNS, slug),
		"vcluster.yaml":          generateVCluster(hostNS, slug, planSlug, g.registryMirror()),
		"ingress.yaml":           generateHostIngress(hostNS, slug, appSlugs),
		"apps-sync.yaml":         generateAppsSyncKustomization(hostNS, slug, g.BasePath),
		"provisioning-rbac.yaml": generateProvisioningTenantRBAC(hostNS),
	}
	hostFiles["kustomization.yaml"] = generateKustomization("", hostFiles)

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
			vcFiles["db-cnpg-pair.yaml"] = generateCNPGPair(appNS, dbPassword, postgresApps, pgCfg, primaryRegion, replicaRegion)
		} else {
			if enableHA {
				// Operator opted in but didn't supply distinct region
				// pair — log loud so the gap is operator-visible rather
				// than silently degrading to single-cluster.
				slog.Warn("provisioning/gitops: active_hot_standby requested but region pair invalid — falling back to single-cluster postgres",
					"app", "postgres", "primary_region", primaryRegion, "replica_region", replicaRegion)
			}
			vcFiles["db-postgres.yaml"] = generatePostgres(appNS, dbPassword, postgresApps, pgCfg)
		}
	}
	if len(mysqlApps) > 0 {
		vcFiles["db-mysql.yaml"] = generateMySQL(appNS, dbPassword, mysqlApps, appConfigs["mysql"])
	}
	if needsRedis {
		vcFiles["db-redis.yaml"] = generateRedis(appNS)
	}
	for _, a := range appSlugs {
		// Shareable database slugs are emitted as db-*.yaml above; skip
		// them here so we don't also produce a stub app-*.yaml that
		// collides with the real db- manifest.
		if a == "mysql" || a == "postgres" || a == "redis" {
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
		vcFiles[fmt.Sprintf("app-%s.yaml", a)] = generateAppDeployment(appNS, slug, a, spec, dbPassword)
	}
	vcFiles["kustomization.yaml"] = generateKustomization(appNS, vcFiles)

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

func generateHostNamespace(ns, slug string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    openova.io/tenant: "%s"
    openova.io/managed-by: provisioning
`, ns, slug)
}

func generateVCluster(ns, slug, planSlug, registryMirror string) string {
	limits := planLimits(planSlug)
	return fmt.Sprintf(`apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: vcluster
  namespace: %s
spec:
  interval: 10m
  chart:
    spec:
      chart: vcluster
      version: "0.33.*"
      sourceRef:
        kind: HelmRepository
        name: loft
        namespace: vcluster-system
  values:
    controlPlane:
      distro:
        k8s:
          enabled: true
          # MIRROR-EVERYTHING (#3760): the k8s-distro image is
          # initContainers[0] of the vcluster StatefulSet (vcluster 0.33.x
          # renders ghcr.io/loft-sh/kubernetes:vX). The harbor-proxy-pull
          # Kyverno ClusterPolicy (Enforce) DENIES it off ghcr.io — pull
          # through the Sovereign Harbor proxy-cache so it matches the
          # */proxy-*/* glob, lockstep with bp-dmz/mgmt/rtz-vcluster.
          image:
            registry: %s
            repository: proxy-ghcr/loft-sh/kubernetes
      backingStore:
        database:
          embedded:
            enabled: true
      statefulSet:
        # MIRROR-EVERYTHING (#3760): the syncer image is initContainers[1]
        # (+ the main container). Pull it through the Sovereign Harbor
        # proxy-cache too — ghcr.io is denied by harbor-proxy-pull.
        image:
          registry: %s
          repository: proxy-ghcr/loft-sh/vcluster-oss
        resources:
          requests:
            cpu: 100m
            memory: 192Mi
          limits:
            cpu: %s
            memory: %s
        persistence:
          volumeClaim:
            size: 5Gi
      service:
        enabled: true
        spec:
          type: ClusterIP
    exportKubeConfig:
      context: vcluster
      server: https://vcluster.%s:443
      insecure: false
      additionalSecrets:
        - name: vc-vcluster
          server: https://vcluster.%s:443
          insecure: false
          context: vcluster
    sync:
      toHost:
        services:
          enabled: true
        ingresses:
          enabled: false
      fromHost:
        ingressClasses:
          enabled: true
`, ns, registryMirror, registryMirror, limits.CPULimit, limits.MemoryLimit, ns, ns)
}

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
// spec.targetNamespace is informational here because reconciliation is
// redirected into the vCluster via spec.kubeConfig; we still set it so the
// intent ("these resources belong to tenant-<slug>") is visible in the CR.
//
// kubeConfig.secretRef: Flux's Kustomization API only accepts `name` and
// `key` on secretRef (no namespace override), so the secret must live in
// flux-system. The vcluster HelmRelease writes the kubeconfig to
// `tenant-<slug>/vc-vcluster`; the provisioning workflow mirrors that secret
// into `flux-system/tenant-<slug>-kubeconfig` after HelmRelease becomes
// Ready (see handlers.MirrorVClusterKubeconfig). The mirror is deleted
// during teardown.
func generateAppsSyncKustomization(ns, slug, basePath string) string {
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
    name: flux-system
    namespace: flux-system
  path: ./%s/%s/apps
  kubeConfig:
    secretRef:
      name: tenant-%s-kubeconfig
      key: config
`, slug, ns, basePath, slug, slug)
}

func generateHostIngress(ns, slug string, appSlugs []string) string {
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
	// wordpress-x-tenant-emrah5-x-vcluster. Issue #117.
	syncedName := func(app string) string {
		return fmt.Sprintf("%s-x-%s-x-vcluster", app, ns)
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

func generatePostgres(ns, password string, apps []string, cfg map[string]any) string {
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
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          ports:
            - containerPort: 5432
          envFrom:
            - secretRef:
                name: postgres-credentials
          resources:
            requests:
              cpu: 50m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 256Mi
          volumeMounts:
            - name: pgdata
              mountPath: /var/lib/postgresql/data
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
`, ns, password, primaryDB, ns, indentBlock(initSQL, "    "), ns, diskGB, ns, backupsEnabled, replicas, ns)
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
func generateCNPGPair(ns, password string, apps []string, cfg map[string]any, primaryRegion, replicaRegion string) string {
	sortedApps := sortStrings(append([]string{}, apps...))
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
	requested := readIntCfg(cfg, "replicas", 3, 1, 5, "postgres")
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
	diskGB := readIntCfg(cfg, "disk_gb", 5, 1, 500, "postgres")

	return fmt.Sprintf(`# bp-cnpg-pair — Pillar-3 active-hot-standby install path for
# postgres-backed marketplace apps (TBD-V17 #2068). Renders the
# primary + replica CNPG Cluster CR pair across the two regions the
# customer picked at signup. Synchronous WAL replication over Cilium
# ClusterMesh; failover-readiness probe + audit ConfigMap wired by the
# chart's own templates (platform/cnpg-pair/chart/templates/*).
#
# postgres-credentials Secret carries the same shape the legacy
# single-cluster path emits so co-installed marketplace apps' env
# bindings (POSTGRES_HOST=postgres, POSTGRES_USER=app, etc.) keep
# working. The Service the apps connect to is the primary Cluster
# CR's bootstrap-managed RW endpoint (cnpgPair.primary.bootstrap.database
# = primary DB name).
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
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-cnpg-pair
  namespace: %s
spec:
  type: oci
  interval: 15m
  url: oci://ghcr.io/openova-io
  secretRef:
    name: ghcr-pull
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cnpg-pair
  namespace: %s
  labels:
    catalyst.openova.io/component: cnpg-pair
    openova.io/category: customer-facing-capability
    openova.io/pillar: "3"
spec:
  interval: 15m
  releaseName: cnpg-pair
  targetNamespace: %s
  chart:
    spec:
      chart: bp-cnpg-pair
      # 0.1.2 — first version with synchronous replication default
      # (remote_apply, numSync=1 — #2064 ship). Pinned via the
      # bootstrap-kit slot when one lands (currently install via the
      # vCluster HelmRepository above).
      version: 0.1.2
      sourceRef:
        kind: HelmRepository
        name: bp-cnpg-pair
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
      primary:
        region: %s
        instances: %d
        storage:
          size: %dGi
          storageClass: hcloud-volumes
        bootstrap:
          database: %s
          owner: app
      replica:
        region: %s
        instances: %d
        storage:
          size: %dGi
          storageClass: hcloud-volumes
      # replication.mode + clusterMesh + audit defaults come from the
      # chart's own values.yaml (sync remote_apply + numSync=1 +
      # clusterMesh.enabled + audit subjects). Per-Sovereign overlays
      # may patch values via Flux postBuild substitute; we intentionally
      # do NOT override here.
`, ns, password, primaryDB, ns, ns, ns, ns, primaryRegion, instances, diskGB, primaryDB, replicaRegion, instances, diskGB)
}

func generateMySQL(ns, password string, apps []string, cfg map[string]any) string {
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
    spec:
      containers:
        - name: mysql
          image: mariadb:11
          ports:
            - containerPort: 3306
          envFrom:
            - secretRef:
                name: mysql-credentials
          resources:
            requests:
              cpu: 50m
              memory: 128Mi
            limits:
              cpu: 500m
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
`, ns, password, password, primaryDB, ns, indentBlock(initSQL, "    "), ns, diskGB, ns, backupsEnabled, replicas, ns)
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

func generateRedis(ns string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
        - name: redis
          image: valkey/valkey:8-alpine
          ports:
            - containerPort: 6379
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 200m
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
`, ns, ns)
}

func generateAppDeployment(ns, slug, appSlug string, spec AppSpec, dbPassword string) string {
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
%s          resources:
            requests:
              cpu: %s
              memory: %s
            limits:
              cpu: 500m
              memory: 512Mi
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
`, pvcManifest,
		appSlug, ns, appSlug, slug,
		appSlug, appSlug, slug,
		initContainers,
		appSlug, spec.Image, spec.Port,
		envLines,
		spec.CPUMilli, spec.RAMMI,
		volumeMounts, volumes,
		appSlug, ns, appSlug, spec.Port)
}

// generateProvisioningTenantRBAC emits a Role + RoleBinding that gives the
// sme/provisioning ServiceAccount the minimum tenant-scoped permissions it
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
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
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

// --- plan resource limits (apply to vCluster control plane + tenant apps) ---

type planLimit struct {
	CPU         string
	Memory      string
	CPULimit    string
	MemoryLimit string
}

func planLimits(slug string) planLimit {
	switch slug {
	case "s":
		return planLimit{CPU: "500m", Memory: "512Mi", CPULimit: "1000m", MemoryLimit: "1Gi"}
	case "m":
		return planLimit{CPU: "1000m", Memory: "1Gi", CPULimit: "2000m", MemoryLimit: "2Gi"}
	case "l":
		return planLimit{CPU: "2000m", Memory: "2Gi", CPULimit: "4000m", MemoryLimit: "4Gi"}
	case "xl":
		return planLimit{CPU: "4000m", Memory: "4Gi", CPULimit: "8000m", MemoryLimit: "8Gi"}
	default:
		return planLimit{CPU: "500m", Memory: "512Mi", CPULimit: "1000m", MemoryLimit: "1Gi"}
	}
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
func UpdateParentKustomization(current, tenantSlug string) string {
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
func RemoveTenantFromParentKustomization(current, tenantSlug string) string {
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
