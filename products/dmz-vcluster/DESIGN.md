# DMZ vCluster — Design

## Problem

OpenOva Sovereigns are zero-trust by default. But customer workloads
that need direct internet exposure — public APIs, webhook endpoints,
customer-facing dashboards — create a real attack surface. If those
workloads run in the same management cluster as the catalyst-api,
catalyst-controllers, and Keycloak, a compromise of any tenant Pod
becomes a beach-head into the management plane.

## Solution

A DMZ vCluster — an isolated virtual Kubernetes cluster running INSIDE
the management cluster's host namespace. Tenant workloads land in the
vCluster (own apiserver, own etcd, own controller manager); the host
cluster's openova-system namespace acts as a syscall-isolation +
NetworkPolicy boundary; and the tenant's only path to the public
internet is via the designated egress gateway.

## Where it sits in the architecture

```
[Public internet]
       │
       │ Cilium Gateway (HTTPRoute)
       ▼
[host:gateway-system] ── allowed by NetworkPolicy
       │
       ▼
[host:dmz openova-system] ◄─── default-deny + allow-essentials
       │     │
       │     └─ syncer → vCluster apiserver (port 8443)
       │
       └─ tenant Pods (own Cilium identity)
              │
              │ egress (everything blocked except…)
              ▼
       [host:egress-system] ── SNAT to reserved public IP
              │
              ▼
       [Public internet]
```

## Isolation primitives

| Layer | Mechanism |
|---|---|
| Compute | vCluster's own apiserver + etcd + controller manager (loft-sh upstream) |
| Identity | Dedicated host openova-system namespace → distinct Cilium identity |
| Network ingress | Default-deny NetworkPolicy + allow-essentials only from cilium-gateway namespace |
| Network egress | Default-deny NetworkPolicy + allow only to DNS + designated egress gateway |
| Pod security | runAsNonRoot, drop ALL caps, seccompProfile RuntimeDefault, readOnlyRootFilesystem |
| Storage | hcloud-volumes PVC for vCluster etcd (no shared PV with management) |
| Auth | Separate Keycloak realm OR realm role per tenancy model (operator picks per overlay) |

## Why under products/ and not platform/

Per ADR-0001 §3.2.8 + the EPIC-5 leftovers brief: customer-facing
capabilities with their own Blueprint + UI surface live under
`products/`. NetBird (which is operator-facing) lives under
`platform/`. DMZ vCluster is fundamentally a tenant-facing primitive —
the customer chooses to run their workload in a DMZ vCluster, the
operator chooses to run NetBird.

The Continuum chart is the precedent we follow: chart + DESIGN.md
under `products/<name>/`. There is no Go binary for DMZ vCluster
(yet) — the heavy lifting is the upstream loft-sh/vcluster install via
HelmRelease.

## Default-OFF + fail-fast

Same 3-contract chart-discipline as bp-guacamole + bp-netbird:

1. `dmz.enabled: false` → zero resources rendered.
2. `enabled: true` with empty `image.tag` → render aborts.
3. Full-ON renders the canonical bundle.

## Out-of-scope (deferred to follow-ups)

- Cilium ClusterMesh per-tenant pinning: today the DMZ vCluster runs
  on a single Sovereign; multi-region active-hotstandby for DMZ
  tenants is an EPIC-6 follow-up.
- Per-tenant Keycloak realm provisioning automation (operator
  manually provisions today via the bp-keycloak realm-config
  ConfigMap pattern; full automation lives in a future controller).
- Tenant-side observability stack (own Hubble UI inside the
  vCluster) — operator opts in via a per-tenant overlay alongside
  the EPIC-5 H7 Hubble UI work.
- Egress-gateway chart (separate `bp-egress-gateway` Blueprint;
  references but does not provide).
