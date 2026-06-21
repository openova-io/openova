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
