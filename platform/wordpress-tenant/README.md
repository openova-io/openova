# platform/wordpress-tenant

Catalyst Blueprint that provisions a turnkey, SSO-pre-wired WordPress
instance per SME tenant inside the SME's vcluster. Part of the
`#795 SME-tenant turnkey experience` epic, ticket #800 (SME-5).

## What's here

| Path | Contents |
|---|---|
| `blueprint.yaml` | Catalyst Blueprint metadata (configSchema, depends, placementSchema) |
| `chart/` | Helm chart `bp-wordpress-tenant` v0.1.0 — see `chart/README.md` |
| `chart/templates/` | Deployment, Service, Ingress, PVC, CNPG Cluster, NetworkPolicy, ServiceAccount + 3 post-install Jobs (db-secret-sync, oidc-config, admin-user) |
| `chart/tests/` | observability-toggle.sh (per #182) |

## Operator install

```bash
helm install acme-wordpress oci://ghcr.io/openova-io/bp-wordpress-tenant \
  --version 0.1.0 \
  --namespace sme-acme \
  --set smeDomain=acme.otech31.omani.works \
  --set keycloak.realmURL=https://auth.acme.otech31.omani.works/realms/sme \
  --set keycloak.clientSecretName=wordpress-oidc \
  --set adminUser.email=admin@acme.com
```

The Sovereign's tenant-provisioning pipeline (#804) wires this Helm
release into a Flux `HelmRelease` per SME, registers the OIDC client
in the SME realm, seals the client secret into
`wordpress-oidc`, and renders the per-SME values overlay.

## See also

- `chart/README.md` — full value reference + boot sequence
- `docs/BLUEPRINT-AUTHORING.md` §11 (umbrella shape, hollow-chart guard, observability toggles)
- `docs/INVIOLABLE-PRINCIPLES.md` (no hardcoding, SHA-pinned images, target-state shape)
- Issue #795 (epic), #800 (this Blueprint)
