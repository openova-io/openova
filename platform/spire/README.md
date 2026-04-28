# SPIRE

SPIFFE/SPIRE workload identity. **Catalyst control plane component** (per [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §2.3 — Per-Sovereign supporting services). Issues short-lived (5-min auto-rotated) X.509 SVIDs to every Pod across every host cluster a Sovereign owns.

**Status:** Accepted. Chart wrapper at `chart/`. **Updated:** 2026-04-28.

---

## Why

Catalyst's identity model has two systems (per [`docs/SECURITY.md`](../../docs/SECURITY.md) §1):

| Subject | System | Lifetime |
|---|---|---|
| **Workloads** (every Pod, every controller) | SPIFFE/SPIRE | 5-min SVID |
| **Users** (every human) | Keycloak | 15-min JWT |

SPIRE issues SVIDs scoped by SPIFFE ID:

```
spiffe://<sovereign>/ns/<namespace>/sa/<service-account>
```

OpenBao authenticates clients by SVID. JetStream authenticates clients by SVID. Catalyst REST APIs authenticate workloads by SVID + users by JWT.

---

## Topology

| Layer | Replicas | Notes |
|---|---|---|
| SPIRE server | 1 (HA: 3) | On the Sovereign's mgt cluster. Upstream-bundle to a root SPIRE on the OpenOva publisher when present. |
| SPIRE agent | 1 per node | DaemonSet. Exposes Workload API (Unix socket) to Pods on that node. |

---

## Chart

The `chart/` directory wraps the upstream SPIFFE/SPIRE Helm chart with Catalyst-curated values. Installed by the Catalyst bootstrap kit during Phase 0 (per `docs/SOVEREIGN-PROVISIONING.md` §3) — after Cilium, cert-manager, Flux, and Crossplane have come up.

OCI artifact: `ghcr.io/openova-io/bp-spire:1.0.0`.

---

*Part of [OpenOva](https://openova.io)*
