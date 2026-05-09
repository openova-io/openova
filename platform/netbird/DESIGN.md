# NetBird — Design

## Why NetBird in OpenOva

OpenOva Sovereigns are zero-trust by default (see
`platform/network-policies/`'s default-deny CCNPs). Operators and
engineers nevertheless need a single safe surface to reach internal
Sovereign-only services (Hubble UI, Harbor, Continuum dashboards,
private CRD editors) without poking holes in the per-Sovereign Cilium
Gateway or carrying long-lived kubeconfig credentials around.

NetBird is the canonical Sovereign-mesh + remote-access answer:

- WireGuard-based — every peer-to-peer flow is encrypted at the
  kernel transport level, complementing the Cilium WireGuard mesh that
  protects east-west traffic inside the cluster.
- Keycloak SSO — every device-enrollment goes through the same OIDC
  chain that gates the rest of the Sovereign console (no separate
  user store).
- Per-Sovereign management — one NetBird per Sovereign per ADR-0001
  §11. There is no Manara-style multi-cluster fan-out; Sovereigns
  stay self-sufficient.

## Where it sits in the architecture

```
[Operator laptop / mobile]
         │
         │ WireGuard (UDP 51820)
         │ NAT-traversal fallback via coturn (UDP 3478)
         ▼
[NetBird coturn Service (LoadBalancer per overlay)]
         │
[NetBird signal Service (ClusterIP via HTTPRoute)]
[NetBird management Service (ClusterIP via HTTPRoute)]
         │
         │ OIDC handshake
         ▼
[Keycloak — bp-keycloak's `catalyst` realm]
         │
         │ realm-patch ConfigMap (label
         │   catalyst.openova.io/keycloak-config: realm-patch)
         ▼
[bp-keycloak post-deploy keycloak-config-cli Job]
```

The realm-patch ConfigMap mirrors the Guacamole pattern from slice
K+P+X1+G #1164 — each Sovereign-installed Blueprint that needs a KC
client emits its own ConfigMap, the bp-keycloak post-deploy Job
iterates them by name and merges each `realm-patch.json` payload into
the realm.

## Why under platform/ and not products/

ADR-0001 §3.2.8 distinguishes platform Blueprints (Sovereign-required
infrastructure) from product Blueprints (customer-facing capabilities
with their own Blueprint + UI surface). NetBird straddles the line: it
is operator-facing (not customer-facing) but it is bundled with every
Sovereign as a Sovereign-mesh primitive. The EPIC-5 leftovers brief
locates it under `platform/` for that reason — alongside bp-cilium,
bp-keycloak, and bp-guacamole that other operators rely on.

## Default-OFF + fail-fast

Same 3-contract chart-discipline as bp-guacamole:

1. `netbird.enabled: false` → zero resources rendered.
2. `enabled: true` with empty `image.tag` → render aborts with
   `bp-netbird: management image tag is empty — SHA-pinned image
   required`.
3. Full-ON renders the canonical bundle (10+ resources: 3 Deployments,
   3 Services, 1 PVC, 1 HTTPRoute, 1 NetworkPolicy, 1 ConfigMap,
   2 SealedSecrets).

## Out-of-scope (deferred to follow-ups)

- Cilium L7 OIDC enforcement at the gateway boundary (today the
  HTTPRoute forwards unauthenticated; NetBird's own OIDC layer gates
  the management API). Same caveat as the Hubble UI overlay.
- Postgres-backed datastore for HA (currently SQLite via PVC).
- catalyst-api REST proxy for setup-key issuance — sketched in the
  README; landed in a follow-up slice.
