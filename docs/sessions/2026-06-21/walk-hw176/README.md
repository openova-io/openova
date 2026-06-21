# Cycle-2b zero-touch walk — hw176

- **Deployment ID**: `12523e0c7611356b`
- **FQDN**: `hw176.omani.works` (console: `https://console.hw176.omani.works`)
- **Provider**: Huawei (kom4dc me-east-215), 2 regions (me-east-215-a / me-east-215-b), shared-pg ON
- **Front-door ELB**: `212.72.24.30`
- **Created from**: hw175 (`1ad6033b5838d25d`) request body verbatim, subdomain hw175→hw176
- **Catalyst image**: `10b061b` (carries cycle-1 fixes #4014/#4015/#4016/#4017/#4019); GitRepository confirmed `main@10b061b`
- **Storage fix under test**: bp-huawei-evs-csi 1.0.1, image `evs-csi-plugin:v0.1.11-idc.1`, `EVS_CSI_ENABLED`/`idc`/`SOVEREIGN_CNPG_STORAGE_CLASS=evs-ssd` all default-correct on main (#4022)

## HEADLINE — did the #3971 EVS-CSI storage fix hold ZERO-TOUCH? ❌ NO

**The storage fix did NOT hold.** A NEW deterministic blocker replaced the old one (#4030, fix shipped in PR #4031).

- Phase-0 tofu apply: clean (2 VPCs, ELB 212.72.24.30, 12 worker VMs both regions).
- Phase-1: both regions Cilium-up, kubeconfigs PUT, Flux GitRepo on `main@10b061b`.
- HRs flooded to **67/region** — PAST the old 40/50 wedge ceiling (storage-ordering improvements DID help here).
- BUT `bp-huawei-evs-csi` HelmRelease → **InstallFailed**: `no matches for kind "VolumeSnapshotClass" in version "snapshot.storage.k8s.io/v1"`.
  - `helm install` fails at manifest-build → driver never installs → **no `evs-ssd` StorageClass** in either region.
  - Root cause: chart 1.0.1 `templates/volumesnapshotclass.yaml` renders a `VolumeSnapshotClass` (default `enabled: true`); the `snapshot.storage.k8s.io` CRDs are installed NOWHERE in the bootstrap-kit.
- Cascade: all 6 stateful PVCs Pending (`gitea-pg-1`/`harbor-pg-1`/`guacamole-pg-1` + `data-{dmz,mgmt,rtz}-vcluster-0`) → `bp-gitea` not ready → `bp-catalyst-platform` blocked → **console HTTP 000**.

## Per-fix walk verdicts (cycle-2b)
| Fix | Ticket | Verdict | Note |
|---|---|---|---|
| STORAGE | #3971 / #4022 | ❌ re-wedged zero-touch | NEW root cause #4030 (VolumeSnapshotClass w/o snapshot CRDs); fix PR #4031 |
| SSO | #3374 | ⛔ BLOCKED | console HTTP 000 behind storage wedge — un-walkable |
| TOPOLOGY | #4015 | ⛔ BLOCKED | console down |
| FUNNEL | #4016 | ⛔ BLOCKED | console down (Bug-3 CNPG-storageClass moot until evs-ssd lands) |
| RECON-ACTIONS | #4014 | ⛔ BLOCKED | console down |
| CLOUD-VIEW LB | #4019 | ⚠️ PARTIAL | DNS/ELB front-door 212.72.24.30 confirmed in dep-record + DNS; UI un-walkable |
| TENANT-STRINGS | #4017 | ⛔ BLOCKED | console down |
| JOBS-FINITE | — | ⛔ BLOCKED | console down |

Health-gate honored: did NOT run a UAT walk on the wedged env — every ❌ would be env-state noise. The cycle-1 fixes ship on `main@10b061b` (image present) and remain to be walked on the FIRST prov that converges past storage (i.e. after #4031 merges → cycle-2c).

## Convergence record
- Region-A / Region-B HR: flooded to 67 each; ~35/67 ready then stalled at storage tier.
- evs-ssd StorageClass: ABSENT both regions (EVS-CSI install failed).
- 12 stateful PVCs: Pending (no SC to bind).

## Next
- Merge #4031 (chart 1.0.2, volumeSnapshotClass default off).
- Wait for Blueprint Release republish of bp-huawei-evs-csi 1.0.2.
- Re-fire cycle-2c → expect evs-ssd to land, PVCs bind, console up → THEN walk the cycle-1 fix-wave.
