# Post-mortem — orphan-sweep janitor self-reaped every fresh Sovereign (2026-06-26)

**Severity:** P0 — every fresh kom4dc Sovereign self-destructed ~2.5 min after convergence; ~5 hours lost; 0 UAT walkthroughs in that window.
**Owner:** platform (self-inflicted regression introduced earlier the same session).
**Status:** root-caused, fixed (#4457), durable deploy-pipeline fix (#4465), clean slate verified, re-prov in flight.

## Impact
- Fresh prov `b9f9590b558262b6` (omantel.biz) reached `status: ready` at 10:45:37Z, then went fully unreachable ~10:48 (console/api/region apiservers → HTTP 000).
- Misdiagnosed initially (by sub-agents) as a kom4dc EIP-poison blip → wasted cycles chasing EIP rotation.
- Net: a full fix-cycle (11 PRs) consumed ~5 hours; **zero** UAT rows walked.

## Timeline (UTC)
- 10:45:37 — dep `ready`, all pillars green, console/api/marketplace 200 + valid LE TLS.
- 10:47:34–36 — catalyst-api logs `[JANITOR] sweep orphan keypair … deleted` → `sweep orphan VPC: deleted peering …`.
- 10:48:03–04 — all 12 ECS nodes `DELETED` in a ~1-second window (Huawei-API by-ID probe); apiserver EIPs (bound to the control-plane ECS) released; ELBs `ACTIVE` but all backends `OFFLINE`.
- ~11:00–14:00 — diagnosis (Huawei-API forensic pinned the janitor), fix train, perfect-wipe verification, deploy-pipeline clobber fix, re-prov staged.

## Root cause
`products/catalyst/bootstrap/api/internal/handler/janitor.go` — the three cloud-resource sweeps (EIPs/keypairs/VPCs) built their protected `activePrefixes` set from an **allowlist** of states `{pending, provisioning, tofu-applying, flux-bootstrapping, phase1-watching, wiping}` that **omitted `ready`**. The instant a deployment flipped to `ready`, its 8-char prefix dropped out of protection and its `catalyst-…-<prefix>-*` keypair/VPC-peering/VPC/EIPs were reaped as "orphans," cascading the node deletion. The VPC sweep was added by #4435 (the "max-5-VPC" mitigation) — turning a latent gap into node-killing.

## Why it shipped (three contributing failures)
1. **Highest-risk change, lowest-risk treatment.** A janitor that *deletes cloud infrastructure* was merged on green CI with no live gate. CI passes; runtime self-destructs. This directly violated an existing written lesson (live-convergence gate for stateful/lifecycle changes).
2. **Fragile-by-construction.** An allowlist of "active" states will always miss one (`ready`/`adopted`/`cutover`). Deletion logic must be **protect-by-default** (denylist of genuinely-dead states), failing safe.
3. **Plumbing perfectionism over the goal.** After the janitor fix rolled, ~2 more hours went to serializing 11 PRs through deploy-bot version races and image-pin clobbers, instead of re-proving immediately and walking incrementally.

## VPC-quota guard chain — where each check should fire (and where it failed)
The founder warned about the kom4dc max-5-VPC bottleneck. Note the prov did NOT fail on the quota — it converged. The failure was the cleanup automation built to manage the quota. The defense-in-depth, layer by layer:

| # | Guard | Where | This incident |
|---|---|---|---|
| 1 | **Pre-flight VPC-quota count** before provisioning | `provisioner.go:1841` (#4435) — counts project VPCs, fails fast if over limit | FIRED correctly; the prov had headroom and started cleanly |
| 2 | **Orphan-VPC sweep** reclaims leaked VPCs from prior wipes | `janitor.go` SweepOrphanVPCs (#4435) | **FAILED — root cause.** Allowlist of "active" states omitted `ready` → swept the LIVE dep's VPC/peering/EIPs → node cascade-delete |
| 3 | **Protect-by-default** (never reap a non-wiped dep) | `buildActivePrefixes()` denylist (#4457) | NOW HOLDS — live-proven (`skipped (active deployment)` in logs on the re-prov) |
| 4 | **Active-dep do-not-touch allowlist** (status-independent) | tracked #4466 | follow-up hardening (belt-and-suspenders on top of #4457) |
| 5 | **Log-only / dry-run before any real delete** | tracked #4466 | follow-up — would have caught #2 before it deleted anything |
| 6 | **Post-wipe orphan verification** via direct cloud API | a20803b8 (this session) | FIRED — confirmed zero orphans (283 stranded EVS force-deleted), project = 1 VPC (bastion) |

The single point of failure was layer 2 using an enumerate-the-safe-states allowlist. Layer 3 (#4457) converts it to protect-by-default; layers 4-5 (#4466) add log-only + an explicit active-dep exclusion so an inference bug can never again reach a live env.

## Fixes
- **#4457 (janitor, fail-safe inversion):** single `buildActivePrefixes()` helper used by all three sweeps — `case "wiped"` → eligible, `default` → protected. Any non-wiped status (incl. `ready`/`failed`/`adopted`/`cutover`) is protected; future statuses fail safe. Verified in code + rolled live on the mothership (`fcb305f`).
- **#4465 (deploy-bump clobber):** the `deploy-bump` action's whole-file-snapshot re-apply was reverting concurrent pin bumps (it had silently staled the org-controller image to a pre-fix commit). Replaced with per-line `git cherry-pick -X theirs`.
- **Perfect wipe verified:** dead dep wiped; Huawei-API confirmed zero orphans (283 stranded EVS force-deleted); project now holds one VPC (bastion only). Clean slate.

## Prevention (enforced going forward)
1. **Never merge infra-deleting / lifecycle (wipe/sweep/reclaim/scale-0) automation on CI-green.** Required: protect-by-default design; **log-only/dry-run** for a full live cycle before it is ever allowed to delete; live-convergence gate before AND after merge.
2. **Goal-first:** re-prov on the env-survival fix and walk **incrementally**; never land-everything-first.
3. **The single live Sovereign is precious:** never run cleanup automation against its Huawei project without an explicit do-not-touch allowlist for the active deployment.

## Follow-ups
- Harden the janitor beyond the inversion: log-only-until-proven mode + an explicit active-deployment do-not-touch allowlist (tracked separately).
- Janitor cred-source gap (can't sweep after a wipe deletes the tfvars) + a standalone detached-EVS-orphan sweep (283 stranded here) — quota-hygiene hardening.
