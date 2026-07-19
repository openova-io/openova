# platform/wordpress-tenant

Catalyst Blueprint that provisions a turnkey, SSO-pre-wired WordPress
instance per Organization inside the Org's vcluster. Part of the
`#795 SME-tenant turnkey experience` epic, ticket #800 (SME-5).

## What's here

| Path | Contents |
|---|---|
| `blueprint.yaml` | Catalyst Blueprint metadata (configSchema, depends, placementSchema) |
| `chart/` | Helm chart `bp-wordpress-tenant` — see `chart/README.md` |
| `chart/templates/` | Deployment, Service, Ingress, PVC, CNPG Cluster, NetworkPolicy, ServiceAccount + 3 post-install Jobs (db-secret-sync, oidc-config, admin-user) |
| `chart/tests/` | observability-toggle.sh, oidc-config.sh, active-hot-standby-render.sh |
| `image/Dockerfile` | Custom WordPress runtime image — official base + PG PHP driver (see below) |

## Custom runtime image — `wordpress-tenant-pg` (#4152)

This Blueprint runs WordPress against **bp-cnpg PostgreSQL** (#800), not
MySQL. The `pg4wp` drop-in (`wp-content/db.php`, `DB_DRIVER='pgsql'`) needs
PHP's `pdo_pgsql` / `pgsql` extensions, which the official
`wordpress:6-php8.3-apache` image does NOT ship (`php -m` there shows only
`mysqli` / `pdo_sqlite`) — so a bare-official WordPress 500s on every request
against Postgres.

`image/Dockerfile` extends the official image with those two extensions (the
canonical docker-php ext-deps idiom that retains the runtime `libpq` after
build cleanup) and asserts at build time that `php -m` lists the driver.
`.github/workflows/build-bp-wordpress-tenant-pg.yaml` builds + pushes it to
`ghcr.io/openova-io/openova/wordpress-tenant-pg:<short-sha>` (+ `:latest`),
then digest-pins `chart/values.yaml` `wordpress.image.*` and dispatches
`blueprint-release`. The chart references the full ghcr path verbatim; the
`_helpers.tpl` `wordpressImage` helper skips the `global.imageRegistry` prefix
for a host-qualified repository so a harbor overlay can't double-prefix it (on
a Sovereign the containerd registry mirror rewrites `ghcr.io` →
`harbor.openova.io/proxy-ghcr` transparently).

## Operator install

```bash
helm install acme-wordpress oci://ghcr.io/openova-io/bp-wordpress-tenant \
  --version 0.1.0 \
  --namespace org-acme \
  --set orgDomain=acme.otech31.omani.works \
  --set keycloak.realmURL=https://auth.acme.otech31.omani.works/realms/org-acme \
  --set keycloak.clientSecretName=wordpress-oidc \
  --set adminUser.email=admin@acme.com
```

The Sovereign's tenant-provisioning pipeline (#804) wires this Helm
release into a Flux `HelmRelease` per Organization, registers the OIDC client
in the Org realm, seals the client secret into
`wordpress-oidc`, and renders the per-Org values overlay.

## See also

- `chart/README.md` — full value reference + boot sequence
- `docs/RUNBOOKS.md` §11 (umbrella shape, hollow-chart guard, observability toggles)
- `docs/PRINCIPLES.md` (no hardcoding, SHA-pinned images, target-state shape)
- Issue #795 (epic), #800 (this Blueprint)
