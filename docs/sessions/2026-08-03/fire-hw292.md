# Fire record — hw292.omani.works (dep `1c56518035a83e03`, 2026-08-03)

The Phase C keystroke of the [path-to-100 master plan](path-to-100-master-plan.md),
executed against the [RT-1 train manifest](train-hw292.md). Every timestamp UTC.

## Timeline

| Time | Event |
|---|---|
| 03:30–03:45 | Fire-day guards re-run on the then-tip `d6f1eee29` (hazard #5583): `check-bootstrap-kit-pin-sync.sh` PASS 67/67; `tests/e2e/bootstrap-kit` go suite ok (`-count=1`). Only main movement after: cron tracker refreshes, diff-verified docs-only. |
| 03:47 | Registry ground-truth: deployments-PVC `tofu/` holds zero workdirs (`_shared` + NAT blocklist only) — one-environment-at-a-time holds before the API even answers. |
| 03:52 | First fire attempt: preflight **FAIL on 4b** — mothership catalyst-api DEPLOYED `fb41faf` ≠ PINNED `fad88bd`. See RCA below. |
| 03:55–04:01 | Both ghcr tags verified present (not hollow — `gh api` package-versions, 1 hit each). Pod slots freed for the surge: `openova-flow-server` → 0, talentmesh `scheduling-service` + `scoring-service` → 0 (founder scale-down authorization, 2026-08-03). catalyst-api then catalyst-ui rolled to `fad88bd`, both rollouts settled. |
| 04:04 | **FIRED.** Preflight 16/16 PASS (4b: "on the pinned image — NO roll pending mid-prov"). Reset-on-fire flushed 11 evidence cells, header stamped `RESET 2026-08-03, pending hw292`. POST returned `{"id":"1c56518035a83e03","status":"provisioning"}`. |
| 04:05 | MERGE FREEZE active — nothing merges until `cutoverComplete=true` (#5534/#5535 held; both OPEN, zero failing checks at 04:15). |
| 04:08 | Ledger reset pushed `b3b598454` (uat-tally + uat-drift-guard green first). |
| 04:11 | Status `provisioning` → `phase1-watching` — phase0 tofu apply (2 VPCs, ECS, ELB, EIPs) completed in ~6 min. |

## Fire parameters (canonical `sovereign-lifecycle.sh fire` body)

2-region Huawei `me-east-215-a`/`-b`, CP `m7n.large.8`, workers `m7n.xlarge.8`×3/region
(the exact sizing of the hw288/hw291 cc=true fires — the larger draft sizing was
dropped in favor of the proven convergence path on the capacity-constrained project),
sharedPG=true, `qaTestEnabled=false` + `fireCutoverOnHandover=true` (North-Star
combo: browsable PROD certs + autonomous cutover).

## RCA — preflight 4b deadlock class on a frozen-Flux mothership

4b gates deployed==pinned so a merged-but-unreconciled deploybot bump cannot roll
catalyst-api mid-prov (the hw265 abandon). Its failure text says "Wait for Flux to
roll catalyst-api, THEN fire" — but mothership Flux is deliberately at 0/0 on all
four controllers (#5573 era), so the awaited roll can NEVER land: the gate holds
forever, not briefly. The honest resolution is to apply exactly the roll Flux
would have applied — verify the pinned tag exists on ghcr (hollow-pin class),
roll, settle — and re-fire with the divergence truly closed. This also re-arms the
gate's protection: deployed==pinned means a mid-prov Flux resume reconciles to a
no-op. Wait-for-Flux is only correct when Flux is actually running.

Pre-roll safety check specific to this class: rolling the mothership catalyst-api
is safe ONLY at zero in-flight provs (registry empty — verified 03:47); with any
prov in phase0/tofu-apply the roll itself is the abandon hazard the gate exists to
prevent.

## Convergence watch

Background watcher polls the deployment every 90s (owner JWT re-minted per poll),
exits on `cutoverComplete=true` / terminal / 6h. Expected: converge ~45 min →
handover auto-fires the 11-step cutover → cc=true. On cc=true: Phase D maximal
walk per the master plan (delivery-verification closes → funnel E2E on canon
domains → SSO matrix → residue sweeps → gitea singleton-roll last-of-non-destructive
→ G12 region-kill dead last), then the post-cc thaw merges #5534/#5535.

Refs #960 #5583 #5558
