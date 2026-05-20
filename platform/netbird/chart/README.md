# bp-netbird

Catalyst-authored Blueprint chart for [NetBird](https://netbird.io) — a
WireGuard-based zero-trust mesh + remote-access overlay. Operators,
engineers, and customer admins enroll devices via Keycloak SSO and reach
Sovereign-internal services from any laptop or mobile device.

## What ships

| Resource | Purpose |
|---|---|
| `Deployment/<rel>-management` + `PersistentVolumeClaim` | NetBird management API + UI; SQLite-backed state on hcloud-volumes PVC |
| `Deployment/<rel>-signal` | WebRTC signaling for WireGuard handshake brokering |
| `Deployment/<rel>-coturn` | TURN/STUN server for NAT traversal fallback |
| `Service/<rel>-{management,signal,coturn}` | ClusterIP fronts; coturn flips to LoadBalancer per Sovereign overlay |
| `HTTPRoute/<rel>` | Cilium Gateway exposure (mgmt UI + signal gRPC) |
| `NetworkPolicy/<rel>-management-egress` | Default-deny + selective egress to Keycloak / signal / coturn / NATS |
| `ConfigMap/bp-netbird-realm-patch` | `catalyst.openova.io/keycloak-config: realm-patch` — adds NetBird OIDC client + realm role |
| `SealedSecret/<oidc>` + `SealedSecret/<coturn-auth>` | Placeholders for OIDC client secret + coturn static-auth secret |

## Default-OFF gate

`netbird.enabled: false` in `values.yaml`. `helm template` renders
**zero** resources by default. Operator opts in via per-Sovereign
overlay at `clusters/<sovereign>/bootstrap-kit/<NN>-netbird.yaml` once
the dependencies (bp-keycloak + bp-cilium + bp-sealed-secrets) are
ready.

## SHA-pinned images

All three container images are upstream:

- `netbirdio/management`
- `netbirdio/signal`
- `coturn/coturn`

Per `docs/PRINCIPLES.md` #4a, `image.tag` is empty in
`values.yaml` and the helm-template render fails-fast when a Sovereign
overlay leaves them empty (see `_helpers.tpl::bp-netbird.imageRef`). CI
populates the SHA tags via `yq eval -i .image.tag = "<sha>"` when
promoting a build into `clusters/<sovereign>/`.

## Keycloak OIDC integration

The `bp-netbird-realm-patch` ConfigMap is consumed by `bp-keycloak`'s
post-deploy `keycloak-config-cli` Job (sorted by name; the `bp-`
prefix groups Catalyst-owned patches together). It adds:

- the `netbird` OIDC client (confidential, with `audience-netbird` and
  `groups` protocol mappers)
- the `netbird-user` and `netbird-admin` realm roles
- default `netbird-users` and `netbird-admins` groups bound to the
  matching realm roles

This mirrors the [Guacamole pattern](../guacamole/chart/templates/keycloak-realm-config.yaml)
from slice K+P+X1+G #1164. Two-step bootstrap: chart applies → operator
extracts the generated client-secret from KC admin API → kubeseal →
re-apply. The first user to land an OIDC handshake against the NetBird
management API becomes the account owner (per
`oidc.adminUserIdClaim: sub`).

## Setup-key flow

NetBird's per-org join tokens (setup-keys) flow through the
catalyst-api → Keycloak OIDC chain for seamless onboarding. New
operators authenticate to NetBird via Keycloak SSO, request a
setup-key from the management UI (or via the `catalyst-api` REST
proxy under `/api/v1/sovereigns/{id}/netbird/setup-keys`), then enroll
their device with `netbird up --setup-key <key> --management-url
https://netbird.<sovereign-fqdn>`.

## NAT-traversal

The coturn Deployment defaults to ClusterIP. Per-Sovereign overlays
should flip `netbird.coturn.service.type: LoadBalancer` once the cloud
LB is available so peers behind symmetric NATs can reach the TURN
server from the public internet. The static-auth secret is shared with
the NetBird management Deployment via `netbird-coturn-auth` SealedSecret.

## Tests

`bash tests/render.sh` exercises three contracts:

1. **Default-OFF**: zero K8s resources rendered (CC3 default-OFF gate).
2. **Fail-fast on empty image tag**: render aborts with the exact
   `bp-netbird: ... image tag is empty` message when `enabled: true`
   without a SHA stamp.
3. **Full-ON canonical bundle**: every required kind appears at least
   once and the realm-config wires the OIDC client.

`helm lint` clean.

## See also

- `DESIGN.md` — design rationale, Sovereign-mesh role, and ADR-0001
  alignment.
- `blueprint.yaml` — Blueprint manifest (catalyst.openova.io/v1alpha1).
- [Guacamole pattern](../../guacamole/chart/templates/keycloak-realm-config.yaml)
  — the canonical Keycloak realm-config integration this chart mirrors.
