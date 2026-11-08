# bp-wordpress-tenant

Catalyst Blueprint scratch chart that installs a turnkey,
SSO-pre-wired WordPress instance per Organization inside the Org's
vcluster.

This is a **scratch chart** — there is no first-party Helm chart
published by the WordPress project (the upstream ships only a Docker
image at `wordpress:6-php8.3-apache`). The `common` library subchart is
declared as a Helm dependency so the BLUEPRINT-AUTHORING.md hollow-
chart guard (issue #181) is satisfied; bp-newapi follows the same
pattern.

## What it provisions

| Resource | Purpose |
|---|---|
| `Deployment` (single replica) | The WordPress Pod. Two initContainers: one seeds `wp-content/` from the image onto the PVC; the other downloads + installs `openid-connect-generic` (Keycloak SSO) and `pg4wp` (Postgres adapter) from wordpress.org / GitHub. |
| `Service` (ClusterIP, :80) | In-vcluster service for the ingress to target. |
| `Ingress` (Traefik, host `wordpress.<orgDomain>`) | Customer-facing entry point. cert-manager issues TLS via the operator-supplied `ClusterIssuer`. |
| `PersistentVolumeClaim` (10Gi default, RWO) | Backs `/var/www/html/wp-content` so themes, plugins, and uploads persist across pod restarts and image upgrades. `helm.sh/resource-policy: keep` so `helm uninstall` never drops customer content. |
| `Cluster.postgresql.cnpg.io` (1 instance, 10Gi) | Tenant-isolated Postgres provisioned by bp-cnpg. The CNPG-emitted `<cluster>-app` Secret carries the password. |
| `Secret wordpress-database-secret` (placeholder) | Reflector-managed bridge that the WordPress Pod reads via `secretKeyRef`. Populated by the post-install `db-secret-sync` Job. |
| `Job <release>-db-secret-sync` (post-install/upgrade) | Mirrors `<cluster>-app.password` into `wordpress-database-secret.password`. Eliminates the otech30-class Reflector race documented in `bp-gitea`. |
| `Job <release>-oidc-config` (post-install/upgrade) | Runs the canonical `wordpress:cli` image: `wp core install` (idempotent), `wp plugin install openid-connect-generic --activate` (idempotent), `wp option update openid_connect_generic_settings <json>` with the per-tenant Keycloak realm + client + secret, `wp option update default_role`, `wp theme activate`, `wp option update siteurl/home`. Idempotent — re-running on `helm upgrade` is safe. |
| `Job <release>-admin-user` (post-install/upgrade, hook weight 15) | Pre-seeds the Org admin into `wp_users` + `wp_usermeta` with the `administrator` role + the SSO email mapping. The user can log in via Keycloak only. |
| `NetworkPolicy` | Restricts egress to: bp-cnpg :5432, Keycloak :8443/:8080, kube-dns, and HTTPS to public IPs (for plugin/theme fetches at first install). Ingress allowed only from the configured ingress namespace (default `traefik`). |
| `ServiceAccount` | Default SA for the WordPress Pod. The post-install Jobs use a dedicated SA + Role + RoleBinding scoped to the tenant namespace. |

## Boot sequence (per docs/PRINCIPLES.md #2)

```
helm install
  ├─ pre-install: namespace, ServiceAccount, Role/RoleBinding hooks (weight 0)
  ├─ install:     Deployment, Service, Ingress, PVC, NetworkPolicy,
  │               Cluster.postgresql.cnpg.io, wordpress-database-secret (empty)
  ├─ post-install hook weight 5:  db-secret-sync Job
  │     └─ waits for CNPG <cluster>-app, PATCHes wordpress-database-secret
  ├─ post-install hook weight 10: oidc-config Job (wp-cli)
  │     └─ wp core install, wp plugin install openid-connect-generic
  │        --activate, wp option update openid_connect_generic_settings,
  │        wp theme activate, wp option update siteurl/home
  └─ post-install hook weight 15: admin-user Job
        └─ INSERT/UPDATE wp_users row for the Org admin's email
```

After all hooks complete, the Org admin browses to
`https://wordpress.<orgDomain>` → openid-connect-generic redirects to
Keycloak → returns to `/wp-admin` authenticated as administrator. No
WP install wizard, no manual config.

## Required values

| Value | Description |
|---|---|
| `orgDomain` | The Organization's domain (e.g. `acme.<otech-fqdn>` or BYO `acme.com`). Used to derive the default ingress host as `wordpress.<orgDomain>`. |
| `oidc.issuerURL` | Discovery URL of the per-tenant Keycloak realm. Example: `https://keycloak.acme.<otech-fqdn>/realms/org-acme`. The wp-cli Job derives the OIDC `endpoint_*` URLs from this. |
| `oidc.clientSecretName` | K8s Secret carrying the OIDC client secret (key `client-secret`). Provisioned by `bp-keycloak`'s tenant-realm ConfigMap (PR #918) at the same time as the realm import. |
| `adminUser.email` | Email of the Org admin (must match the `email` claim Keycloak issues for that user). The admin-user Job pre-seeds a wp_user with this email and the administrator role. |

> **Back-compat (chart 0.1.x):** `keycloak.{realmURL,clientID,clientSecretName}` is still accepted as an alias when the modern `oidc.*` block is at its values.yaml defaults. New overlays MUST emit `oidc.*` — the legacy block is removed in chart `0.3.0`.

## Override surface

All other values have sensible defaults; common overrides include:

| Value | Default | Notes |
|---|---|---|
| `global.imageRegistry` | `""` | Set to the Sovereign's Harbor proxy-cache hostname post-handover. |
| `wordpress.image.tag` | `6-php8.3-apache` | The chart pins the manifest-list digest alongside; change `tag`+`digest` together. |
| `database.cnpgClusterName` | `wordpress-db` | Per-tenant unique within the Org namespace. |
| `database.cluster.storageSize` | `10Gi` | Postgres storage size. |
| `persistence.wpContent.size` | `10Gi` | wp-content PVC size. |
| `persistence.wpContent.storageClass` | `local-path` | Set to a RWX class if you want to scale `replicas > 1`. |
| `defaultTheme` | `twentytwentyfive` | Any wordpress.org theme slug bundled with the official image. |
| `ingress.tls.issuer` | `letsencrypt-prod` | cert-manager `ClusterIssuer`. |

See `values.yaml` for the full schema, including NetworkPolicy egress
peers, OIDC role mapping, and probe tuning.

## Why Postgres (and not MySQL)?

Issue #800 specifies "bp-cnpg Postgres in tenant namespace". The
official `wordpress` image targets MySQL/MariaDB; we run it against
Postgres via the `pg4wp` mu-plugin (a `wp-content/db.php` drop-in that
intercepts `wpdb` at the PHP level and translates queries). This keeps
the per-Org footprint to one database operator (bp-cnpg) instead
of sprouting a separate MySQL operator per Organization — see the upstream
project at
https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress.

The `pg4wp` install is performed by the same `wp-plugin-install`
initContainer that installs `openid-connect-generic`, so the chart
needs no special image build.

## Capabilities gate

`Cluster.postgresql.cnpg.io` is rendered behind a Capabilities check on
`postgresql.cnpg.io/v1`, so a cold install before bp-cnpg is
reconciling skips the Cluster CR (and the Pod waits in
`Pending`/`CrashLoopBackOff` until bp-cnpg lands and the Cluster is
re-rendered on the next reconcile). The Sovereign's bootstrap order
MUST land bp-cnpg before bp-wordpress-tenant.

## Multi-region topology (0.5.0, ADR-0015 D2)

Placement is expressed ONLY through the shared contract, one HelmRelease of
this chart **per region**, identical except for `self`:

```yaml
openova:
  topology:
    mode: active-passive            # singleton | active-passive | active-hot-standby
    self: hw-me-east-215-a-rtz-prod # the region this release is applied in
    primary: hw-me-east-215-a-rtz-prod
    peers: [hw-me-east-215-b-rtz-prod]
database:
  topology:
    promotion: {mechanism: manual}  # #5623 — REQUIRED for a multi-region contract
```

Helpers come from the library chart `platform/topology-lib` (vendored at
build). Each release renders **only its own half**:

| Region role | CNPG Cluster | WordPress | Hook Jobs | Service |
|---|---|---|---|---|
| primary (`self == primary`) | `wordpress-db-<h(self)>`, `bootstrap.initdb`, synchronous standby = declared peers (ADR-0004, `dataDurability: required`) | `replicas: N` against the local `-rw` | rendered | mesh-global, local backends |
| replica | `wordpress-db-<h(self)>`, `bootstrap.pg_basebackup` from the primary, no sync fence | `replicas: 0` | not rendered | mesh-global, zero local backends ⇒ proxies to the primary |

- **Naming scheme**: `<database.cnpgClusterName>-<first 8 hex of sha256(region-id)>`
  (`printf %s <region-id> | sha256sum | cut -c1-8`). Stable, DNS-safe, ≤ 50
  chars, computable without the chart. `database.topology.clusterNames`
  (region-id → name) overrides it per region.
- Every member declares `spec.replica.{self,primary,source}` and
  `externalClusters` for every member over `<member>-mesh` (ClusterMesh
  global; its own via CNPG `managed.services.additional`, `selectorType: rw`;
  the peers' as zero-backend stubs). A `CiliumNetworkPolicy` admits 5432 to /
  from the peer members only.
- **Shared Secrets** — identical bytes in every region, must exist before the
  Cluster can start: `<cnpgClusterName>-replication-ca` (`ca.crt`, `ca.key`:
  every member's `clientCASecret`) and `<cnpgClusterName>-app-credentials`
  (basic-auth: the DB owner). Set
  `database.topology.sharedSecrets.externalSecret.storeRef.name` to pull them
  from a (Cluster)SecretStore (`<prefix>/replication-ca`, `<prefix>/db-owner`).
- **Traffic follows the primary**: WordPress cannot serve against a read-only
  replica (sessions, options, wp-cron, login all write), and pointing a
  passive region's Pods at the primary's DB over the mesh would pay the
  inter-region RTT on every query and write uploads to the wrong region's
  PVC. So only the primary runs Pods; any other region's gateway reaches them
  through the global Service.
- `wp-content` is a **region-local** volume (`templates/pvc.yaml`, declared in
  `blueprint.yaml` `spec.regionLocalVolumes`, `replication: none`): media do
  not follow a primary move until ADR-0015 D6 lands.

### Moving the primary (Git-level, lossless)

1. Commit `openova.topology.primary: <new>` to the **current primary's**
   region file. CNPG demotes that Cluster in place and publishes
   `status.demotionToken`; its WordPress scales to 0.
2. Read that Cluster's `status.demotionToken`; commit
   `openova.topology.primary: <new>` **and**
   `database.topology.promotionToken: <token>` to the **new primary's** region
   file. CNPG promotes after replaying up to the token (RPO 0); WordPress
   scales up there and the hook Jobs run there.
3. Commit `openova.topology.primary: <new>` to every remaining peer.

Never commit step 2 before step 1 is reconciled: without a token CNPG
promotes immediately while the old primary is still writable. An unplanned
region loss has no token: commit `primary: <survivor>` to the survivor only.

### Migration from 0.4.x

`database.mode` and `pg.activeHotStandby.*` are **removed**; setting either
fails the render with the migration text (no silent downgrade).

- **Single-region installs**: nothing to do — no contract ⇒ the 0.4.x
  singleton render (one Cluster `wordpress-db`, WordPress serving).
- **Former `active-hot-standby` installs** (one release rendering
  `wordpress-db` + `wordpress-db-replica` into region A): give region A's
  release the contract with `self` = region A and pin its existing Cluster
  name — `database.topology.clusterNames: {<region-A-id>: wordpress-db}` — so
  Helm keeps the primary and its data (only the never-scheduled
  `wordpress-db-replica` is pruned). Seed the two shared Secrets in both
  regions FROM THE EXISTING CLUSTER: `wordpress-db-app-credentials` =
  `username`/`password` of the live `wordpress-db-app` Secret (bootstrap
  secrets are only read at creation, so the role keeps its current password
  and WordPress must be given that one), and `wordpress-db-replication-ca` =
  `ca.crt`/`ca.key` of `wordpress-db-ca` (or a new shared CA — CNPG then
  re-issues the replication leaf). Then add region B's release with
  `self` = region B: its Cluster `wordpress-db-<h(B)>` clones the primary over
  the mesh. Until region B is streaming, the primary's sync fence blocks
  writes, exactly as ADR-0004 requires.
