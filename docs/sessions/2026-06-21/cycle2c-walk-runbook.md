# Cycle-2d zero-touch walk runbook — hw178

> Cycle-2c (hw177) WEDGED on a worker-node/kubelet infra flake (forensic:
> [`hw177-worker-wedge.md`](hw177-worker-wedge.md)) → wiped (verifiedZeroOrphans)
> → re-fired as **cycle-2d / hw178** on the same storage-fixed `main@116b616b0`
> (chart `bp-huawei-evs-csi 1.0.2`, the #4030 fix).

## Env
- **Deployment ID**: `02bc518fcb1b4cb3`
- **FQDN**: `hw178.omani.works` (console: `https://console.hw178.omani.works`)
- **Provider**: Huawei kom4dc me-east-215, 2 regions (me-east-215-a / -b), shared-pg ON
- **main**: `116b616b0` (chart 1.0.2 #4031 storage fix + cycle-1 fixes #4014/#4015/#4016/#4017/#4019 + #4022 idc driver)
- **Created from**: hw177 (`068f217746ca7779`) request body verbatim, subdomain hw177→hw178

## Convergence gate (STEP 3) — must pass BEFORE walking
1. All region-a + region-b nodes **Ready** — and STAY Ready (watch the workers; the
   hw177 wedge was kubelets posting ~30 min then going silent → re-confirm at 2 polls).
2. CoreDNS `Running` (1/1), endpoints present.
3. GitRepository `openova` `READY=True` on `main@116b616b0`.
4. **STORAGE (the #4030 proof)**: `evs-ssd` StorageClass present BOTH regions +
   EVS-CSI driver pods Running + the 6 stateful PVCs Bound
   (`gitea-pg-1`/`harbor-pg-1`/`guacamole-pg-1` + `data-{dmz,mgmt,rtz}-vcluster-0`),
   NO `local-path`.
5. HRs PAST the old 40/50 ceiling (hw176 reached 67/region) → console HTTP 200.

## Per-fix walk (STEP 4) — handover-mint + Playwright, LIVE evidence each
| Fix | Ticket | What to prove on the live surface |
|---|---|---|
| SSO | #3374 | bare console URL → signed-in as owner, NO login/PIN form; avatar "Signed in as…"; Grafana/Gitea/Harbor/OpenBao/Keycloak bare URLs land authed |
| STORAGE | #3971/#4022/#4031 | `evs-ssd` default SC + 6 PVCs Bound + NO local-path (kubectl-confirmed) |
| TOPOLOGY | #4015/#3375 | cloud view shows Region 2/2 active-active; per-app 2-target placement (region-a active + region-b standby as ONE placement) |
| FUNNEL | #4016 | provisioning controller healthy (no CrashLoop); if feasible drive coupon→Org→ACTIVE |
| RECON-ACTIONS | #4014 | reconcile/suspend app actions return no 502/403 |
| CLOUD-VIEW LB | #4019 | cloud view shows the real ELB front-door (not 0/0) |
| TENANT-STRINGS | #4017 | NO "tenant"/"SME" banned-term leak in the console bundle/UI |
| JOBS-FINITE | — | Jobs page lists finite Job runs; no infra Job pod masquerading as an app/endpoint |

## Walk mechanics
- Mint handover JWT: `python3 /tmp/hw-keys/mint_handover.py hw178.omani.works 02bc518fcb1b4cb3`
  (iss=console.openova.io, sub+email=emrah.baysal@openova.io, email_verified, aud=console.<fqdn>,
  sovereign_fqdn, deployment_id, role=sovereign-admin, jti=uuid, exp=now+300).
- Playwright `https://console.hw178.omani.works/auth/handover?token=<jwt>` — NO curl precheck.
  Session ~5 min; re-mint per burst.
- Screenshot each fix; record verdict + filename in [`walk-hw178/`](walk-hw178/) + UAT.md.

## Escalation guard
If hw178 ALSO loses its workers the same way (kubelets post ~30 min then go silent,
no MemoryPressure/DiskPressure transition) → that is **SYSTEMATIC kom4dc worker-VM
instability** — STOP, do NOT re-fire a 3rd time, escalate with the evidence.
