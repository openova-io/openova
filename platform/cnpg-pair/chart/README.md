# bp-cnpg-pair — chart

Active-hotstandby CNPG cluster-pair across two Hetzner regions, WAL
streaming over Cilium ClusterMesh.

Slice C-DB-1 of EPIC-6 (#1101). Companion to:

- **bp-cnpg** — installs the CNPG operator + CRDs (umbrella over
  upstream `cnpg/cloudnative-pg 0.28.0`). This Blueprint deploys
  the operator that reconciles every Cluster CR this chart emits.
- **bp-cilium** (with ClusterMesh) — provides the inter-region
  transport. Per ADR-0001 §9 ClusterMesh is the ONLY canonical
  inter-region path; public TLS for replication is NOT supported.
- **bp-hcloud-csi** — provides the `hcloud-volumes` StorageClass.
- **bp-continuum** (K-Cont-1+, EPIC-6) — orchestrates switchover
  against the Cluster CRs this chart creates. Continuum reads the
  audit-config ConfigMap from this chart for the audit subjects
  + types this Blueprint contributes.

---

## What the chart renders (when `cnpgPair.enabled=true`)

| Resource | File | Purpose |
|---|---|---|
| `Cluster` (CNPG) — primary | `templates/primary-cluster.yaml` | Read-write Postgres in region A. Region-pinned via `openova.io/region` node-affinity. Carries `spec.managed.services.additional` declaring the ClusterMesh-global `<name>-mesh` Service when `clusterMesh.enabled=true`. |
| `Cluster` (CNPG) — replica | `templates/replica-cluster.yaml` | Read-only Postgres in region B. `replica.enabled: true` + `bootstrap.pg_basebackup` referencing the primary externalCluster + `externalClusters[]` pointing at the ClusterMesh-global `<name>-mesh` Service. |
| `Deployment` — failover-readiness probe | `templates/failover-readiness.yaml` | Single-replica probe Pod in the replica region; flips Ready when WAL lag < `walStreaming.targetLagSeconds`. |
| `NetworkPolicy` × 3 | `templates/networkpolicy.yaml` | Default-deny exception: replica → primary 5432, probe → replica 5432, probe Egress to DNS + replica. |
| `ConfigMap` — audit-config | `templates/audit-config.yaml` | Declares `audit.subject` + `audit.eventTypes` for K-Cont-2 + U-DR-1. |

**Total when ON:** 7 chart-rendered resources (1 primary Cluster + 1 replica Cluster + 1 Deployment + 3 NetworkPolicies + 1 ConfigMap). The replication Service IS materialised at runtime — it is created by the CNPG operator from the primary Cluster's `spec.managed.services.additional` block — but does NOT count as a chart-rendered Kubernetes resource (and therefore is invisible to `helm template` / `kustomize build`).

When `cnpgPair.enabled=false`: ZERO resources.

> **Chart 0.1.1 fix.** Chart 0.1.0 hand-rendered a standalone Service in `templates/service-replication.yaml` which CNPG refused to reconcile (`refusing to reconcile service: <name>-r, not owned by the cluster`). The Service is now CNPG-owned via `spec.managed.services.additional` (CNPG ≥ 1.22 feature) and named `<name>-mesh` to avoid colliding with the auto-created `<name>-r`. The `helpers.tpl::cnpg-pair.replicationServiceName` helper still resolves to `<release>-bp-cnpg-pair-primary-mesh`, and the replica's externalCluster connection points at this name.

---

## Failover semantics (Continuum reads, this chart emits)

1. CNPG reconciles the primary Cluster CR → primary Postgres serves R/W.
2. Replica Cluster CR's instances bootstrap from the primary via the
   ClusterMesh-shared Service. WAL streams over the private mesh.
3. The failover-readiness probe Pod polls
   `pg_last_xact_replay_timestamp()` on the replica's `-r` Service
   every 10s; when lag < `walStreaming.targetLagSeconds` the Pod's
   exec readinessProbe surfaces Ready=True.
4. Continuum K-Cont-2 reads the probe's Ready condition + the
   primary Cluster CR's `status.currentLSN` before allowing
   operator-initiated switchover.
5. On switchover, Continuum:
   - Patches `replica.enabled: false` on the replica Cluster CR
     (CNPG promotes — `pg_promote()`).
   - Patches `replica.enabled: true` on the old primary CR
     (CNPG demotes; the externalClusters[] now points at the new
     primary via the same Service alias).
   - Flips lua-record probes via PDM `/v1/commit` so resolver
     clients converge on the new primary's Cilium HTTPRoute hostname.
   - Audits on `catalyst.audit` with the audit-types declared in
     this chart's audit-config + Continuum's own `continuum-*` set.

---

## Installation

This chart is published to GHCR by the existing
`.github/workflows/blueprint-release.yaml` event-driven workflow
(triggers on push to `platform/cnpg-pair/chart/**` on main). The
artifact is `ghcr.io/openova-io/bp-cnpg-pair:<chart-version>`.

A platform overlay or Application install consumes the chart with
`cnpgPair.enabled=true` and the operator-supplied region pair +
image tag. Example values overlay:

```yaml
cnpgPair:
  enabled: true
  primary:
    region: hz-fsn-rtz-prod
  replica:
    region: hz-hel-rtz-prod
  image:
    repository: ghcr.io/cloudnative-pg/postgresql
    tag: "16.3-23"          # pin to digest in production
```

`primary.region == replica.region` raises a render-time error;
empty `image.tag` raises a render-time error (per Inviolable
Principle #4a — no `:latest` in production).

---

## Tests

`chart/tests/cnpg-pair-render.sh` covers:

1. **OFF** (`enabled=false`) → ZERO resources rendered.
2. **ON** (`enabled=true` + region pair + image tag) → 8 resources.
3. **Empty image.tag** → fails with the documented error.
4. **Same-region pair** → fails with the documented error.
5. **ClusterMesh disabled** → no Service rendered (forensic mode).

CI runs the script via the blueprint-release workflow's
`tests/*.sh` gate.

---

## See also

- `DESIGN.md` (sibling) — replication topology, lag-threshold
  rationale, ClusterMesh assumption, single-region → pair
  migration sketch, deferred C-DB-3 acceptance test plan.
- `docs/EPICS-1-6-unified-design.md` §9.4 — CNPG cluster-pair spec.
- `docs/MULTI-REGION-DNS.md` — lua-record context for failover.
- `docs/BLUEPRINT-AUTHORING.md` — Blueprint manifest contract.
- `docs/adr/0001-catalyst-control-plane-architecture.md` §9 — multi-
  region inviolable rules (ClusterMesh-only).
