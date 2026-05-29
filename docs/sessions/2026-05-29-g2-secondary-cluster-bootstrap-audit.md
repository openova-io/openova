# G2 — Secondary cluster bootstrap audit (CNI + Flux + per-region slot gating)

**Date:** 2026-05-29 (scaffold session)  
**Refs:** #2574 (this issue), #2526 (multi-region 5-pillar walk parent)  
**Status:** Scope-only scaffold; ship work pending.

## Finding (concrete, verifiable on any current HCS prov)

The Huawei cloud-init emits the same Flux GitRepository + Kustomization manifest on EVERY control-plane (primary + every secondary). Each CP's Flux therefore syncs the SAME `clusters/_template/bootstrap-kit/` slot set. Yet no bootstrap-kit slot today gates on the `SOVEREIGN_REGION_ROLE` postBuild substitute (`primary` / `secondary`) that the cloud-init templates carry.

```bash
$ grep -rn "SOVEREIGN_REGION_ROLE" clusters/_template/
# → no results
```

Therefore: a secondary cluster also installs slot 13 (`bp-catalyst-platform`), slot 14 (`bp-continuum`), slot 15 (`bp-sandbox`), and every other mothership-only chart. Each becomes a separate Helm release competing for `catalyst-system` resources, separate cert-manager Certificates, separate handover-JWT signing-key state, etc.

This is what blocks #2526's Pillar 3 (active-hot-standby across two regions). Until secondary regions install ONLY the per-region slot subset, cross-region behaviour is undefined.

## Correct topology (per `docs/SOVEREIGN-MULTI-REGION-DOD.md` §A4)

| Slot family | Primary | Secondary |
|---|---|---|
| CNI + L7 (`bp-cilium`, `bp-gateway-api`) | ✅ | ✅ |
| Cert / DNS / secrets (`bp-cert-manager`, `bp-external-dns`, `bp-external-secrets`, `bp-openbao`, `bp-powerdns`, `bp-reflector`) | ✅ | ✅ |
| Storage (`bp-seaweedfs`, `bp-cnpg`, `bp-valkey`, `bp-nats-jetstream`) | ✅ | ✅ (each region has its own quorum) |
| Observability + security (`bp-falco`, `bp-trivy`, `bp-sigstore`, `bp-syft-grype`, `bp-kyverno`, `bp-kyverno-policies`, `bp-loki`, `bp-tempo`, `bp-mimir`, `bp-alloy`, `bp-grafana`) | ✅ | ✅ |
| vClusters | MGMT (58) + DMZ (54) | DMZ (54) + RTZ (59) |
| Catalyst control plane (slots 13–15: `bp-catalyst-platform`, `bp-continuum`, `bp-sandbox`) | ✅ | ❌ |
| Sovereignty cutover (slot 60+ `bp-self-sovereign-cutover`) | ✅ | ❌ |
| Gateway TLS (`sovereign-tls/`) — listener cert | per-zone primary | per-zone if region serves SME tenants |

## Concrete fix shape

1. **Per-slot annotation `catalyst.openova.io/region-role: primary | secondary | both`** on every bootstrap-kit slot's HelmRelease.
2. **Kustomize patch in `clusters/_template/bootstrap-kit/kustomization.yaml`** that filters slots by the `SOVEREIGN_REGION_ROLE` substitute — only HRs whose annotation `region-role` matches the substitute (or is `both`) get applied.

Initial slot inventory (subject to review):
- `region-role: both`: 01-cilium, 02-cert-manager, 03-flux, 04-gateway-api, 05-kyverno, 05a-kyverno-policies, 06-reloader, 07-reflector, 08-openbao, 09-external-secrets, 10-gitea, 11-powerdns, 12-external-dns, 16-cnpg, 17-valkey, 18-nats-jetstream, 19-seaweedfs, 20–29 observability + security, 54-dmz-vcluster, 58-mgmt-vcluster (primary side via cnpg quorum requirement — TBD), 59-rtz-vcluster
- `region-role: primary`: 13-bp-catalyst-platform, 14-bp-continuum, 15-bp-sandbox, 60+ bp-self-sovereign-cutover, sovereign-tls/

## Ship plan

PR fields to populate when this lands:
- Files: every `clusters/_template/bootstrap-kit/*.yaml` (add annotation), `clusters/_template/bootstrap-kit/kustomization.yaml` (selector logic)
- Lockstep version bump: bp-catalyst-platform chart since its embedded bootstrap-kit snapshot changes
- Verification: hw52 multi-region prov → `kubectl get hr -A --kubeconfig <region-b>.yaml` should NOT have `bp-catalyst-platform`/`bp-continuum`/`bp-sandbox`
- Integration: `sovereign-dod-verify.sh` (G18) extended with L3.5 "per-region slot conformance" assertion

## Out of scope (separate issues, ladder to #2526)

- **#2575 (G23 stub)**: Per-region CNPG with `ReplicaCluster` synchronous replication over Cilium ClusterMesh → Pillar 3 region-kill test.
- **#2576 (G24 stub)**: HCS LoadBalancer (existing G3 #2536 dup) wiring for `clustermesh-apiserver` Service.

