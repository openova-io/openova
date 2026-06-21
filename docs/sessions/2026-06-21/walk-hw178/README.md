# Cycle-2d zero-touch convergence — hw178

- **Deployment ID**: `02bc518fcb1b4cb3`
- **FQDN**: `hw178.omani.works` (console `https://console.hw178.omani.works`)
- **control-plane IP**: 212.72.24.36 | **front-door LB/ELB**: 212.72.24.35
- **main**: `116b616b0` (chart bp-huawei-evs-csi 1.0.2, the #4030/#4031 storage fix)
- **Created from**: hw177 (`068f217746ca7779`) request verbatim, subdomain hw177→hw178

## STEP-0 forensic (hw177 wedge)
hw177 wedged on a worker-kubelet INFRA flake — see [`../hw177-worker-wedge.md`](../hw177-worker-wedge.md).
No cloud-init log was uploaded (404 — workers died before the push). Verdict: infra
flake, not a main regression (hw174/hw176 brought workers up on the same main).

## STEP-1 wipe hw177
`POST .../068f217746ca7779/wipe?force=true` → HTTP 200, `verifiedZeroOrphans: true`,
`pdmReleased: true`, `localCleaned: true`. (tofuDestroyed:false due to the known
Huawei ELB listener-deletion race, but the providerPurge fallback force-deleted the
floating IP / LB / 2 VPCs / 2 subnets directly → zero orphans confirmed.)

## STEP-2 re-fire cycle-2d (hw178)
`POST .../deployments` → HTTP 201, new depID `02bc518fcb1b4cb3`, status `provisioning`.

## STEP-3 convergence — the hw177 wedge did NOT recur ✅
| Phase | hw178 result | hw177 (wedged) |
|---|---|---|
| Phase-0 tofu apply | clean (~8min): 2 VPCs, ELB 212.72.24.35, cp 212.72.24.36, DNS published | clean |
| kubeconfig PUT | ~05:13 (Phase-1 watcher) | PUT |
| Region-a nodes | **ALL 6 Ready by 05:20:26 and STAYED Ready** (1 cp + 5 workers) | 4/5 workers went NotReady ~30min in (kubelets died) |
| CoreDNS | **Running 1/1** | Pending 0/1 (no Ready workers) |
| (continued below) | | |

## STEP-3 storage — #4030 fix HELD, but exposed a NEW blocker #4037 (fixed live + PR #4038)
| Item | hw178 result |
|---|---|
| GitRepository `openova` | READY=True on `main@3be0656` (after a slow ~13min source-controller ghcr pull — kom4dc latency, not a stall) |
| HR flood | climbed to 67/region — PAST the old 40/50 wedge ceiling ✅ |
| **evs-ssd StorageClass** | **PRESENT (default), `evs.csi.huaweicloud.com`, Retain, WaitForFirstConsumer** — the #4030 fix HELD zero-touch ✅ (this was the cycle-2b FAILURE) |
| bp-huawei-evs-csi HR | installs (no VolumeSnapshotClass crash) ✅ |
| **NEW blocker #4037** | the 8 csi-evs-* pods → ImagePullBackOff: `ghcr.io/.../evs-csi-plugin` 401 (PRIVATE pkg, no imagePullSecret + huawei-evs-csi missing from ghcr-pull reflection list) → all 9 evs-ssd PVCs Pending |
| Live-patch validation | copied ghcr-pull secret to `huawei-evs-csi` + patched both pod specs → driver pods 5/5 + 3/3 Running, PVCs binding (`data-dmz/mgmt-vcluster-0`, `shared-pg-1` 10Gi, `shared-pg-c-1` 10Gi all Bound on evs-ssd) ✅ |
| Durable fix | chart 1.0.3 + cloudinit reflection-list (#4037, PR #4038) — imagePullSecrets:[ghcr-pull] on both pod specs |

**Storage verdict: the #4030 StorageClass fix is CORRECT and HELD zero-touch; #4037 is the next deterministic layer (private-image pull), now fixed durably. With #4037 merged, the NEXT zero-touch prov should bind PVCs without a live patch.**
