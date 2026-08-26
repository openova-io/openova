# SPIRE

SPIFFE/SPIRE workload-identity chart — **retained opt-in, NOT the active identity system.** SPIRE/SPIFFE was dropped from the Catalyst bootstrap-kit by founder PR #665 (2026-05-03, "drop bp-spire — Cilium WireGuard is canonical east-west mesh"); the `chart/` here is kept only for possible future re-introduction. The **canonical** workload-identity system today is **Cilium WireGuard mesh + K8s ServiceAccount TokenReview** — see [`docs/SECURITY.md`](../../docs/SECURITY.md) §2. SPIRE does **not** issue SVIDs to Pods today.

**Status:** Deferred / opt-in — dropped from the bootstrap-kit per PR #665 (bootstrap-kit Slot 06 deleted); re-enable triggers in [`docs/SECURITY.md`](../../docs/SECURITY.md) §2. Chart wrapper at `chart/`. **Updated:** 2026-08-26.

---

## Why (historical design — superseded)

Catalyst's identity model has two systems (per [`docs/SECURITY.md`](../../docs/SECURITY.md) §1). SPIRE was the *originally-designed* workload system; it has since been superseded by Cilium WireGuard + K8s SA TokenReview (PR #665):

| Subject | System (canonical today) | Lifetime |
|---|---|---|
| **Workloads** (every Pod, every controller) | Cilium WireGuard mesh + K8s SA TokenReview (bound-tokens) | WG session-keys per node-pair; SA bound-tokens ~1h, kubelet-rotated |
| **Users** (every human) | Keycloak | 15-min JWT |

The SPIFFE ID shape below is preserved at namespace+ServiceAccount granularity — but verified via TokenReview, **not** via a SPIRE-issued SVID:

```
spiffe://<sovereign>/ns/<namespace>/sa/<service-account>
```

**Auth today (per [`docs/SECURITY.md`](../../docs/SECURITY.md) §2), not by SVID:** OpenBao authenticates clients via the `kubernetes` (TokenReview-backed) auth method; NATS JetStream relies on kernel-level WireGuard transport encryption + Account-level isolation (the `bp-spire` `dependsOn` was removed in PR #665); Catalyst REST APIs authenticate workloads by SA bound-token (TokenReview) and users by Keycloak JWT.

---

## Topology

| Layer | Replicas | Notes |
|---|---|---|
| SPIRE server | 1 (HA: 3) | On the Sovereign's mgt cluster. Upstream-bundle to a root SPIRE on the OpenOva publisher when present. |
| SPIRE agent | 1 per node | DaemonSet. Exposes Workload API (Unix socket) to Pods on that node. |

---

## Chart

The `chart/` directory wraps the upstream SPIFFE/SPIRE Helm chart with Catalyst-curated values. It is **not** part of the Catalyst bootstrap-kit — the `bp-spire` slot (Slot 06) was deleted in PR #665, so a fresh Sovereign does not install SPIRE. The chart is retained for opt-in re-introduction only, installed solely under a founder re-enable ruling (see [`docs/SECURITY.md`](../../docs/SECURITY.md) §2 for the re-enable triggers, and the provisioning runbook in [`docs/RUNBOOKS.md`](../../docs/RUNBOOKS.md)).

OCI artifact: `ghcr.io/openova-io/bp-spire:1.0.0`.

---

*Part of [OpenOva](https://openova.io)*
