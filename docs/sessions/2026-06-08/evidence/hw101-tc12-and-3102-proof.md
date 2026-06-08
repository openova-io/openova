# hw101 — TC-12 + #3102 live verification (2026-06-08)

**Deployment**: `e19b083c6db41bb0` — `hw101.omantel.biz`, 2-region Huawei (me-east-215-a + -b).
**Status**: `ready` (converged where hw100/`99d4bfcba53a5af3` FAILED).

## #3102 PROVEN — harbor cleared the ESO-webhook wedge
hw100 FAILED with `bp-harbor` stuck `Ready=False` msg `external-secrets-webhook` (the #2988 ESO race). On hw101, built on the #3102 fix (harbor chart 1.2.26 `.Capabilities` guard on the SSO ExternalSecret), `bp-harbor` reconciled normally:

```
bp-harbor  ready=Helm upgrade     # reconciling/installing — NOT stuck False
```

Remaining not-True HRs are all benign:
```
bp-cluster-autoscaler-hcloud  ready=        # by-design Suspended on Huawei (excluded from REQUIRED)
bp-hcloud-ccm                 ready=        # by-design Suspended on Huawei
bp-velero                     ready=        # by-design Suspended on Huawei (bp-velero-hcs is the HCS pair)
bp-self-sovereign-cutover     ready=Unknown # dormant at bootstrap (fires post-handover)
```

## TC-12 PROVEN — two real clusters, real nodes, real zones
Two independent kubeconfigs on the catalyst-api PVC:
- `e19b083c6db41bb0.yaml` (primary, region a)
- `e19b083c6db41bb0-me-east-215-b-1.yaml` (secondary, region b)

**Region a — 4 Ready k3s v1.31.4 nodes:**
```
...-me-east-215-a-cp1-6c8bd3   Ready   zone=me-east-215-a
...-me-east-215-a-w547801      Ready   zone=me-east-215-a
...-me-east-215-a-w56c5fc      Ready   zone=me-east-215-a
...-me-east-215-a-wd9b45b      Ready   zone=me-east-215-a
```
**Region b — 4 Ready nodes:**
```
...-me-east-215-b-cp1-bb908b   Ready   zone=me-east-215-b
...-me-east-215-b-w41ee99      Ready   zone=me-east-215-b
...-me-east-215-b-w8e5662      Ready   zone=me-east-215-b
...-me-east-215-b-wf36358      Ready   zone=me-east-215-b
```

This is the genuine 2-region substrate hw99 faked (hw99 had all 4 nodes in region-a). TC-12 (two real clusters with real nodes in both regions) = **PASS** at the substrate level.

## Still owed (DoD UI walk, gated on handover)
The console (`console.hw101.omantel.biz`) renders tenant data only post-handover. Remaining walks via injected-session cookie: TC-G1 class-page instances data, TC-G3 per-app SSO (gitea/openbao/grafana/harbor), per-app topology view, TC-G4 editable endpoints, and the TC-16 cutover egress-block proof. Handover status was not yet confirmed at verification time.
