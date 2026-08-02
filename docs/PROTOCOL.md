# EXECUTION PROTOCOL — Road to cutoverComplete=true, Agenity+MCP, and a Green UAT Ledger

> **Status**: canonical execution protocol. Home: `docs/PROTOCOL.md` (this file — linked from `CLAUDE.md` and `docs/ledger/TRACKER.md`).
> **Derived from**: the 2026-07-12 retrospective (`docs/sessions/2026-07-12/completion-matrix-and-retrospective.md`) + 10 adversarially-verified root causes (8 CONFIRMED, 2 PARTIAL-with-corrections). Revised 2026-07-14 after gap review (env contention, founder-rule conflicts, milestone evidence gates, WIP disposition, closure-audit scope, off-repo enforcement, protect-list durability).
> **Authority**: founder mandates in force — release-train, janitor hygiene, root-cause-over-symptom, WBS critical-path, perfect tickets (#3370), NodePorts forbidden, done = operator-walked live evidence.

---

## 1. North star + current durable state

### 1.0 STEP 0 — re-verify the state table before executing ANYTHING (mandatory, every session)

The §1.2 table is a **snapshot**, not live state, and it has already drifted twice since it was written (working checkout moved `fix/4973-4975-4961-comprehensive-registry-pivot` → `uat-hw250` → `fix/5046-nodeport-doc-drift` as of 2026-07-14). A fresh session that executes against stale facts ("hw248 is next", "branch X is the active WIP") repeats the exact amnesia class in `CLAUDE.md` §Session-lessons. Before acting on any row of §1.2, re-derive it:

1. `git branch --show-current` + `git branch -a --sort=-committerdate | head` — actual checkout + latest `uat-hw<NNN>` branch (the highest `uat-hw<NNN>` names the current/most-recent walked env).
2. `gh issue list --state open` + `gh pr list --state open` — issue/PR reality (open counts, #5036/#4752/#5042/#4675 states, any re-opens from the C7 gate).
3. Live cloud inventory + `GET /sovereign/api/v1/deployments` (owner-scoped — mint cookie as `emrah.baysal@openova.io` per memory canon) — which envs actually exist, which is converged, which is production.
4. `docs/ledger/TRACKER.md` + `docs/ledger/UAT.md` heads — current lane state and walk stamps.

Any discrepancy between the re-derived state and §1.2 → update §1.2 in the same session (this file is itself under RT-6 lockstep). **Never fire, wipe, walk, or dispatch based on the unverified snapshot.**

### 1.1 North star (unchanged, restated)

1. **Keystone**: `cutoverComplete=true` proven **ZERO-TOUCH** on a **fresh 2-region Huawei kom4dc Sovereign** — full 11-step chain (`platform/self-sovereign-cutover/chart`, currently 0.1.126) including the 10-minute deny-egress hold against `github.com`, `ghcr.io`, `harbor.openova.io`.
2. **Then**: Agenity running on the environment + provisioning applications through Agenity with MCP integration (#4111, #4277).
3. **Then**: the ~281-row UAT ledger (`docs/ledger/UAT.md`) walked green with live evidence.

**Critical reading correction (Cause 9)**: "then" is NOT a hard dependency chain. Only the keystone requires fresh-prov fires. Agenity and the majority of UAT rows are walkable **today on any converged env** — the founder's own release-train principle endorses exactly this. (§2.2 and D2 are written to honor this: the Agenity walk is anchored to *any converged env*, never to the keystone retry loop.)

### 1.2 Durable state as of 2026-07-14 (snapshot — Step 0 re-verifies before use)

| Surface | State |
|---|---|
| Registry-pivot family | #5038 (redirect-aware lint ruling) merged 2026-07-12T18:31Z — **unproven live**. #5036 (design-call issue) OPEN. Branch `fix/4973-4975-4961-comprehensive-registry-pivot` still exists with unmerged commits (behind tracker-refresh noise) — **disposition owned by W7, Lane A** (see §2.3). |
| Working checkout | As of 2026-07-14: `fix/5046-nodeport-doc-drift`. Latest UAT branch: `uat-hw250` (tracker refreshes through 07-13). The retrospective-era assumption "hw248 is next" is stale. |
| Furthest cutover ever | hw246: green through step-7, died solely on the lint-vs-redirect contradiction that #5038 resolves. |
| Bootstrap class | #4752 + #5042 OPEN, **zero fix PRs**. `infra/providers/_shared/cloudinit-control-plane.tftpl:1579` (flux-install curl) and `:1584` (flux wait) still single-shot, un-retried. Forensic path still blind: `cloudinit_log.go:58-62` PUT gate + 40×30s uploader cap in `infra/providers/huawei/main.tf:~977-991`. hw247 wiped with zero forensic value; the next fresh fire will hit the same coin-flip. |
| Substrate safety | #4675 OPEN — production deleted TWICE by in-band quota reclaim (#4614 06-28, #4675 07-01). `provisioner.go:1997-2021` VPCReclaimHook still fires in-band; protect-set derives from the same in-memory store that returns false-empty after a catalyst-api recycle. |
| Hygiene gates | `scripts/prov-preflight.sh` self-declares MANDATORY, has **zero callers** — `scripts/sovereign-lifecycle.sh fire()` (lines 48–71) POSTs after only a UAT reset. EVS fire-side gate absent (hw244 wedged at 332/400, #5028). Wipe-side EVS drain (#4677→PR #4678 `wipe_drain.go`) and backstop (PR #5029 `wipe_evs_backstop.go`) already landed. |
| UAT ledger | Best-ever 67% (hw241, killed by pod-thrash). ~38–40 flush-everything resets; hw240 reset flushed 137 walked cells. 58 ⚠️ rows code-done-awaiting-walk + 19 SSO rows re-stamp-only. |
| Issue hygiene | 38 open; ~54 of 89 (retrospective count) LIKELY-DONE awaiting close evidence. Two mass-close batches (19 + 56 ≈ 75 issues) on merge-evidence, **never systematically audited — W5 one-time sweep owns this**; deferred proof for #4961/#4973/#4975 never landed. |
| Ledger truth | `docs/ledger/TRACKER.md:12` shows "41/41 = 100% DONE" while keystone tasks #950/#951 sit open — the 41-gate denominator is stale and must be rescoped. The rendering script `refresh-dod-dashboard.sh` is **host-local, not repo-tracked** — W6 moves it into `scripts/`. |
| Canonical docs | `docs/STATUS.md` self-dated 2026-05-20 (zero Huawei/kom4dc); `docs/DOD.md` + ADR-0002 + `CLAUDE.md` spec "eight sequential Jobs" vs the shipped 11-step chain; `docs/RUNBOOKS.md` 78× Hetzner vs 3× Huawei. STONE principles live only in `~/.claude/.../memory/`. The never-touch protect-list exists nowhere durable — §5.0 fixes this. |
| Agenity | Mechanism merged+wired; proven live twice (06-28 dep 91dc0591; 07-03 zero-touch hw220). Gaps: (i) no durable per-Sovereign Anthropic credential, (ii) open founder design decision on cloud-init credential propagation, (iii) `bp-agenity` not auto-installed on clean provs. #4111/#4277 blocked-on-founder ≥2 weeks. |

### 1.3 Founder-gated register (the ONLY items requiring founder input — 48h escalation applies to each)

| ID | Item | Consequence while unresolved |
|---|---|---|
| **A1** | Durable per-Sovereign Anthropic credential + cloud-init credential-propagation design call (#4111/#4277) | D2 walk blocked; everything up to the walk (A2 install path, credential seam) proceeds |
| **F2** | OPTIONAL canon change: surface-aware (instead of full) UAT flush on **fire** — would amend the founder's 2026-06-08 RESET-UAT-ON-FIRE rule + `each_new_env_flushes_all_evidence` memory canon | Until granted, **fire-triggered full flush stays in force unmodified** (see §8 C6); C6's surface-aware reset applies to merge-triggered restamps only |

No other item on this plan needs founder signal. Everything else is agent-executable under the standing autonomy mandate.

---

## 2. WBS — dependency chain and CRITICAL PATH

### 2.1 The critical path (serial, in order)

```
CP-0  Bootstrap forensics + retry class fix (#4752 + #5042, ONE PR)
      └─ every subsequent fire is coin-flip-wedged AND forensic-blind without it
CP-1  Offline 11-step cutover rehearsal harness (CI)
      └─ converts defect discovery from 1/prov-fire to N/CI-run
CP-2  Converged-env cutover RE-FIRE proving #5038 live — ON THE NAMED ENV,
      ONLY AFTER Lane B evidence extraction on that env is committed (§2.1a)
CP-3  Release-train fresh prov (next hw<NNN> per Step 0): full zero-touch fire →
      cutoverComplete=true with deny-egress-hold evidence   ← THE KEYSTONE
CP-4  Post-keystone UAT completion sweep on the SAME env (task #949 pattern)
```

**Why CP-0 before any fire**: hw247 (#5042) proved a prov can wedge at flux-install with a 404 log — the fire is wasted AND teaches nothing. One PR: rt-retry-wrap every network-dependent runcmd stage in `cloudinit-control-plane.tftpl` (the `:1332` #4753 pattern generalized to `:1579`/`:1584` and peers) with FAILED sentinels; "flux never installed" named terminal state in phase1-watch; mirror the #3380 GET decoupling into `PutCloudInitLog` (`cloudinit_log.go:58-62`); replace the 40×30s uploader cap with until-loop + cloud-init failure-trap final push; add `scripts/lint-cloudinit.sh` CI rule rejecting new un-retried network-dependent runcmd items.

**Why CP-1 before CP-3**: the treadmill's arithmetic is settled — ~24 provs at ~1 newly-discoverable cutover defect per fire vs. the single cutover-FIRST hw242 fire exposing 5 defects at once (#5011/#5012/#5014/#5017/#5022). The harness: kind/vcluster + the **actual step Jobs** + synthetic deny-egress CCNP + the kyverno policy set, seeded from `platform/self-sovereign-cutover/chart/tests/cutover-contract.sh` (52 cases on main) but extended from render-assertions to **Job-execution assertions**. Every future cutover defect gets a harness case BEFORE its fix PR merges.

**Why CP-2 before CP-3**: #5038 is merged-unproven. Burning a fresh prov to test it violates release-train ("never fire a fresh prov for one passenger"). The converged re-fire path is proven (hw231, 07-09): `kubectl -n catalyst create token bp-self-sovereign-cutover-runner` → POST internal trigger → poll step Jobs + CM `self-sovereign-cutover-status`; re-POST resumes/409; prewarm ~16m.

### 2.1a CP-2 env contract — naming, sequencing vs Lane B, and the no-env contingency

A cutover re-fire is a **potentially destructive event for the host env** (step-08 defect stack, EVS freeze, registry-pivot side-effects have all wedged envs before). Therefore:

1. **Name the env.** CP-2 and Lane B run on **the same, explicitly named env**: under the §5.0 one-environment-at-a-time rule exactly one Sovereign exists, and it is by definition both the production Sovereign and the only possible CP-2 host (as of 2026-07-15 that is **hw255**). The name is written into the CP-2 train manifest (`docs/sessions/<date>/train-<env>.md`) before the trigger, and the destructive-event risk of a re-fire is accepted knowingly — there is no separate protected env to fall back to.
2. **HARD ORDERING — Lane B before CP-2 on a shared env.** "Walk everything before any wipe" (`feedback_canonical_wipe_endpoints.md` +walk_everything_before_any_wipe) applies to cutover fires too: the re-fire may cost the env. The CP-2 trigger is GATED on Lane B having walked and **committed** (UAT stamps + evidence in `docs/sessions/<date>/evidence/`, pushed) every row walkable on that env — the 58 ⚠️ code-done rows + 19 SSO re-stamp rows, minus any rows structurally requiring a fresh prov. The CP-2 train manifest must contain the line `LANE-B-EXTRACTED: <UAT commit SHA>`; its absence makes the trigger a protocol violation (grep-auditable, same pattern as the §5.2 pre-fire evidence line). This is codified as **RT-10** in §4.
3. **Contingency — NO converged env survives.** If Step 0 finds no converged env (all wedged/wiped), CP-2 is **impossible, not skippable-silently**: its verification objective (#5038 proven live at the previous hw246 failure point) is **explicitly promoted onto the CP-3 train manifest as a named passenger**, and the manifest records `CP-2-COLLAPSED: no converged env survived <date>`. This is sanctioned by RT-2's existing keystone exception ("OR the fire is the keystone attempt itself with the full batch aboard") and satisfies RT-3 (the enumeration returned empty). What remains forbidden is the *silent* collapse: firing CP-3 without the manifest recording that #5038 rides unproven. In this branch, CP-1 harness coverage of the step-08 lint path becomes MANDATORY before the CP-3 fire (the harness is then the only pre-fire proof surface for #5038).

### 2.2 Onward path after the keystone — Agenity is NOT keystone-gated

```
CP-3 keystone ── CP-4 UAT sweep on the keystone env (never wipe it — STONE)

Agenity lane (runs the moment A1 lands, on ANY converged env — hw250-class,
              pre- or post-keystone, whichever exists first):
   A1 durable credential registered (founder-gated — §1.3, Lane C escalation)
   A2 bp-agenity per-Org install on the chosen converged env
   A3 walk: chat + MCP + provision an application THROUGH Agenity
      (evidence: screenshot + wire capture + resulting app HTTP 200)
   A4 (re-affirmation only, NOT a gate): once the keystone env exists,
      re-stamp A3's walk on it — a walk, not a rebuild; if the keystone
      lags, A3 evidence from the converged env is FULL D2 credit
```

**Explicit anti-serialization rule (Cause 9 applied to itself)**: if A1 lands before CP-3 completes, the A2/A3 walk fires immediately on the best converged env — it does NOT wait for the cutover retry loop. Anchoring Agenity to "the keystone env" was the exact false serialization this plan exists to kill.

The founder-gated items are exactly the §1.3 register (A1 mandatory, F2 optional). Everything else is agent-executable.

### 2.3 WBS items off the critical path (feed it, don't block it)

| ID | Work | Feeds | Files/issues |
|---|---|---|---|
| W1 | Refuse-fire-over-reclaim + ground-truth protect set (seeded from the §5.0 repo-tracked protect-list) | protects every CP fire | `provisioner.go:1997-2021`, #4675, #4614 |
| W2 | Preflight mechanical wiring + EVS/EIP fire-side gates | protects every CP fire | `scripts/sovereign-lifecycle.sh`, `scripts/prov-preflight.sh`, #5028 |
| W3 | ADR-0013 (sovereign-ref canon, amends ADR-0012) + widened `check-kyverno-proxy-images.sh` | prevents registry-family recurrence | #5036, #5038, `docs/adr/` |
| W4 | Surface-aware `scripts/reset-uat.py` restamp path (merge-triggered ONLY — §8 C6) + restamp cron | preserves CP-4 evidence | Cause 6, F2 |
| W5 | Close-evidence GitHub Action + issue re-opens (#4961/#4973/#4975) + **one-time mass-close audit sweep** (below) | ledger truth | Cause 7 |
| W6 | Operating-canon docs commit + `.github/ISSUE_TEMPLATE/` + **repo-track the DoD dashboard** (below) | model continuity (§7) | Cause 8, 10 |
| W7 | **In-flight branch disposition**: `fix/4973-4975-4961-comprehensive-registry-pivot`. Audit `git log main..<branch> --no-merges` (filter tracker auto-refresh noise); for each substantive commit decide: (a) already merged via another PR → note SHA-equivalence, (b) still-needed registry-pivot work → cherry-pick onto a fresh branch and board the CP-2/CP-3 train as a manifest passenger, (c) superseded by #5038's ruling → record in #5036 and drop. Then delete the branch (local + origin). Disposition table committed to `docs/sessions/<date>/wip-disposition.md`. Same audit pattern applies to any other stale WIP branch Step 0 surfaces. Owner: **Lane A** (it is registry-pivot work feeding CP-2). | no orphaned WIP; model-switch test | branch `fix/4973-4975-4961-comprehensive-registry-pivot`, #4961/#4973/#4975, #5036 |

**W5 one-time mass-close audit sweep (closes the rest of Cause 7)**: C7's gate only protects FUTURE closes and its re-opens cover only 3 issues. The two historical mass-close batches (19 + 56 ≈ 75 issues, closed on merge-evidence) get a one-time sweep: for EACH closed issue, classify as (a) **evidence-backed** — a live-walk artifact dated after the deploy-bump exists on the issue → leave closed, link the artifact; (b) **re-open** — no evidence and the surface is load-bearing/unverified → `gh issue reopen` + label `close-theater-audit`; (c) **walk-debt** — code is plausibly deployed but unwalked → leave closed, add the corresponding row to the Lane B walk-debt list with the issue number so the walk stamp closes the loop. Output: classification table committed to `docs/sessions/<date>/close-audit.md` + TRACKER.md summary row (`closed-audited: X evidence-backed / Y re-opened / Z walk-debt`). Lane B's batch-close work consumes ONLY the walk-debt output of this sweep — never the raw "LIKELY-DONE" list (which the sweep supersedes).

---

## 3. Parallel tracks the dependency chain permits TODAY — kill all false serialization

Every lane below is independent; a single orchestrator with 2–3 sub-agents (cap per `feedback_cap_2_3_business_priority_gate.md`) runs them concurrently. **File-editing sub-agents dispatch in `isolation: worktree`.**

**Lane A — keystone (CP-0 → CP-3) + W7 branch disposition.** Owner: orchestrator. The only lane that fires provs or cutover triggers. On the shared env, Lane A's CP-2 trigger is HARD-GATED behind Lane B's committed evidence (§2.1a, RT-10).

**Lane B — converged-env UAT sweep + evidence-batched closes — RUNS FIRST on the shared env.** On the named env (§2.1a; hw250 per the 2026-07-14 snapshot), walk the 58 ⚠️ code-done rows + re-affirm the 19 SSO re-stamp rows; commit stamps + evidence, then hand the `LANE-B-EXTRACTED` SHA to Lane A. Batch-close issues **with live evidence per close** (#4913 template), consuming the W5 sweep's walk-debt classification (not the raw ~54 LIKELY-DONE list). Includes #4788's 3 Governance rows (206/207/239) — the fix ALREADY shipped (PR #4789, 2026-07-05; the retrospective's "never shipped" line is the error); they are pure walk-evidence debt.

**Lane C — Agenity unblock.** (a) Standing escalation NOW via `chepherd.alert_human` for the §1.3 register (A1 credential + design decision; F2 flush-canon question rides the same escalation) — founder-gated items may never idle >48h unescalated (encode in TRACKER.md); (b) meanwhile ship the non-gated engineering: `bp-agenity` auto-install path on clean provs + durable per-Sovereign credential registration seam, so the moment the credential lands, A3 is a walk on any converged env (§2.2), not a build.

**Lane D — enforcement mechanisms (W1, W2, W5 incl. the mass-close sweep).** Pure repo work, no env contention.

**Lane E — canon + harness (W3, W4, W6, CP-1).** Pure repo/CI work. CP-1 harness construction runs entirely offline and MUST NOT wait for any fire.

**False serializations explicitly killed**: UAT-behind-cutover (Lane B needs no cutover — and on the shared env it runs BEFORE the cutover re-fire); Agenity-behind-cutover (needs a converged env + credential, both available — §2.2 anti-serialization rule); docs-behind-pillar-work (Lane E is what makes pillar work stop repeating); harness-behind-next-fire (backwards — the harness exists to make the next fire the last).

---

## 4. Release-train protocol (enforceable checklist)

Every prov fire is a **train departure**. Before ANY `POST /sovereign/api/v1/deployments` (RT-10 additionally covers cutover re-fires):

- [ ] **RT-1 Manifest**: written train manifest in `docs/sessions/<date>/train-<env>.md` listing every fix PR aboard, each with merge SHA **and** proof the deployed pins carry it (`gh api /repos/openova-io/openova/packages/...` chart-pin check — merged ≠ deployed, `feedback_post_merge_dod_chain.md`).
- [ ] **RT-2 No single-passenger trains**: ≥2 independent fix-verifications aboard, OR the fire is the keystone attempt itself with the full batch aboard (including any CP-2-collapsed passengers per §2.1a-3, named in the manifest).
- [ ] **RT-3 Converged-env exploitation FIRST**: enumerate what the current best env can still verify (cutover re-fire via runner-SA trigger, UAT rows, Agenity). Fresh fire only for what structurally requires fresh bootstrap. If the enumeration returns empty (no converged env), record `CP-2-COLLAPSED` in the manifest per §2.1a-3.
- [ ] **RT-4 Harness-first**: any cutover-step fix aboard has a CP-1 harness case (or at minimum a `cutover-contract.sh` case) merged BEFORE the fire — authored from the spec, not the post-mortem.
- [ ] **RT-5 No mid-prov merges** to catalyst-api/bootstrap paths while a prov is in flight (`feedback_never_merge_catalyst_api_prs_right_before_firing_a_prov.md`).
- [ ] **RT-6 Live-patch lockstep**: any live patch on a running env becomes a merged PR **within the same session** (`feedback_every_live_patch_must_become_a_merged_pr_immediately.md`). A live patch without a durable twin is a defect.
- [ ] **RT-7 Never wipe a converged env** while it retains verification value (STONE, hw218/hw241 lessons). Wipe gate: UAT evidence extracted + train manifest for the successor exists + no in-flight verification. Never delete pods on RWO-EVS to "force" anything (`feedback_never_restart_sovereign_catalyst_api_rwo_evs_pvc.md`).
- [ ] **RT-8 Cadence cap**: max 2 fires/day (07-10's five same-day resets is the anti-pattern). Encode in `docs/RUNBOOKS.md`.
- [ ] **RT-9 Debug-before-wipe** (founder #3132): a FAILED env's cloud-init log is fetched (`GET /api/v1/deployments/{id}/cloudinit-log`) and its diagnostic value extracted before any wipe. Post-CP-0, a 404 here is itself a P0 defect, not a shrug.
- [ ] **RT-10 Walk-before-cutover-fire (NEW, §2.1a)**: a cutover re-fire on a converged env is treated as potentially env-destroying. It requires (a) the env NAMED in a train manifest, (b) all walkable UAT evidence on that env extracted and committed first (`LANE-B-EXTRACTED: <SHA>` line in the manifest), (c) confirmation the target is not on the §5.0 protect-list. Extends RT-7/RT-9 discipline from wipes to cutover triggers.
- [ ] **RT-11 Mid-cutover merge-hold (hw250 2026-07-14, #5051)**: from the moment step-03 (harbor-prewarm) starts until step-08's deny-egress hold completes, HOLD every merge that publishes a chart or image the Sovereign consumes (Blueprint Release, Build & Deploy Catalyst). A mid-cutover publish advances the HR/pod-spec versions PAST what step-03 mirrored — step-06 Phase-3a and step-07's #4996 image-warmed gate then fail-closed on the version gap (bp-catalyst-platform:1.4.1111 chart + catalyst-api:e960e04 image both hit this live on hw250). Recovery when it happens anyway: `skopeo copy` the exact missing chart/image (ghcr → `registry.<fqdn>/openova-io/...`, `--all` for images per #4975) and let the runner's re-POST retry the step. RT-5's mid-prov rule, generalized to mid-cutover.

**Runner-wedge recovery (hw250 2026-07-14, #5051 — keep next to RT-10/RT-11):** NEVER delete a cutover step Job out-of-band; the runner's in-memory attempt lock survives the deletion (every re-POST returns 409 `cutover-in-progress`, no reset endpoint, no catalyst-api restart allowed on RWO-EVS). If it happens anyway: create a **same-named stub Job** (`backoffLimit: 0`, one container, `exit 1`) — the runner's watch sees the Failed condition, marks `failedStep`, releases the lock, and the next re-POST starts a fresh attempt from the currently-installed chart. Proven live on hw250.

---

## 5. Janitor / hygiene protocol (enforceable pre-flight gate)

### 5.0 Never-touch protect-list (durable, named — commit VERBATIM into `docs/RUNBOOKS.md` janitor chapter and keep here)

Production was deleted TWICE by automation (#4614, #4675). The protect-list therefore lives on the same durable, repo-tracked surface as the quota table — never only in `~/.claude` memory or code constants:

| Resource | Identity | Rule |
|---|---|---|
| Bastion node | `bastion-openova`, EIP `212.72.24.20` | NEVER wipe/scale/modify without explicit founder say-so (the one founder-protected Huawei resource) |
| The single live Sovereign | **exactly ONE deployment may exist at any time** (founder verbatim, 2026-07-15, #5111: *"You are allowed to create 1 environment at a time and that is the exact definition of flight checklist"*); as of 2026-07-15: **hw255** (Step 0 re-verifies) | It IS production. NEVER an in-band reclaim/janitor victim, NEVER wiped mid-cycle. It is wiped ONLY as the explicit, verified wipe-before-fire step of the next prov cycle — and a fire while it still exists is itself the violation (#4614/#4675 were both dual-env accidents; the hw240+hw255 coexistence of 2026-07-15 was the same class) |
| Any Sovereign serving a real Organization / shared infra the platform did not create | per live inventory | protect-by-default (`feedback_goal_first_reprov_walk_incrementally_never_infra_delete_on_ci.md`) |

Maintenance rule: when the live env changes (wipe→fire cycle completes), updating this table (here + RUNBOOKS.md) is part of the cycle checklist. There is NO standing multi-env protect-list — a second "protected coexisting Sovereign" entry (the former hw240 row) is definitionally a one-env-rule violation. The W1 code protect-set (`provisioner.go`) **seeds from this repo-tracked list** and cross-checks live cloud inventory (Huawei ECS/VPC API) — never the in-memory deployments store alone.

### 5.1 The kom4dc numbers (codify in `docs/RUNBOOKS.md`, sourced from memory + `docs/runbooks/preflight-sovereign-provision.md` #4485 — promote that file into the canonical read path)

| Resource | Limit | Consumption | Rule |
|---|---|---|---|
| VPC | **5 total** | **2 per 2-region prov** | ≥2 free before fire; at 3+ used with a live prod present, wipe-first is MANDATORY |
| EVS | **400 volumes** | hw244 wedged at 332 (#5028: 413 VolumeLimitExceeded) | ≥100 headroom before fire |
| EIP | pool-limited | shared with any coexisting env | wipe old env BEFORE fire (hw182 lesson) |
| Wipe-release lag | **~15 min** | quota frees AFTER wipe returns | poll-until-headroom, never fire on assumed headroom (hw96/97) |

### 5.2 Mechanical enforcement (this closes Cause 4)

1. **Wire the gate**: `scripts/sovereign-lifecycle.sh fire()` execs `scripts/prov-preflight.sh` as a hard exit-gate (auto-derive COOK via the existing `_tok()` path — kill the setup-friction excuse). A CI check (`check-no-nodeports.sh` pattern) asserts the wiring never regresses.
2. **Extend the preflight**: VPC-count, EVS-count, EIP-free checks + a 20-min poll-until-headroom loop codifying the wipe-release lag + a protect-list check (refuse if the fire/wipe target matches §5.0).
3. **Server-side twins**: `EVSQuotaHook` + `EIPQuotaHook` beside `VPCQuotaHook` in `provisioner.go` — unskippable even when a session bypasses the script.
4. **Refuse, don't reclaim** (Cause 3): flip `provisioner.go:1997-2021` — when over-quota AND any non-wiped deployment exists, **REFUSE the fire** with a named error; destructive reclaim is never in-band at fire time. Protect-set seeds from the §5.0 repo-tracked list AND cross-checks **live cloud inventory** (Huawei ECS/VPC API), never the in-memory deployments store alone (#4675: false-empty store = production deleted). Close #4675 with this.
5. **Wipe gate**: `POST /deployments/{id}/wipe` additionally requires UAT-evidence-extracted OR another-converged-env-exists proof (beyond the existing `wipeMinLifeProtection`).
6. **Pre-fire evidence line** in the session log: `PRE-FLIGHT PASS vpc=X/5 evs=Y/400 eip=Z free` — its absence in `docs/sessions/` on any fire is a protocol violation (grep-auditable; today's count: zero hits, ~20 fires).

---

## 6. Perfect-ticket template (distilled from #3370 — 24/25 gold standard)

Ship as `.github/ISSUE_TEMPLATE/dispatch.md` + labeler rule: **`status/in-progress` may not be applied to an issue lacking a checkable DoD section.**

**Mandatory fields for every sub-agent dispatch ticket:**

1. **Supersede banner** — "This body is the complete spec and supersedes all prior comments. Any deviation, invention, or special-casing fails the ticket."
2. **End goal** — one paragraph, the operator-visible outcome.
3. **The model (the law)** — numbered invariants the implementation must satisfy; includes explicit **deletion mandates** for rejected prior art.
4. **Measured current state** — live command outputs with env + date (`kubectl get ... → 0`), naming each violation to fix.
5. **Exact target state** — a table the founder can diff reality against.
6. **Mechanism / code-paths-to-cite** — exact files the implementer must read and cite BEFORE writing (e.g. `core/controllers/application/internal/render/fanout.go`); "decide FROM CODE, not from imagination."
7. **History / MUST-PRESERVE** — prior PRs to build on vs delete; the precise failure class this ticket exists to kill; MUST-PRESERVE constraints (orchestrator owns regressions — `feedback_orchestrator_owns_regressions_convey_no_regress_principle.md`).
8. **Access runbook** — env, kubeconfig path, credential locations (PVC `catalyst-api-deployments` paths), console URLs. A dispatched agent asks for nothing.
9. **Execution constraints** — worktree isolation, commit identity (`-c user.name=hatiyildiz -c user.email=269457768+hatiyildiz@users.noreply.github.com`), `Refs #N` never `Closes`, lockstep gates (chart bump + blueprint.yaml + pin-sync-audit + `go test -race`), merge policy.
10. **DoD — ALL boxes binary and founder-walkable**, each with its evidence artifact (screenshot / kubectl output / HTTP code) and where it commits (`docs/sessions/<date>/evidence/` + `docs/ledger/UAT.md` link).
11. **Explicitly excluded** — scope fence with follow-on pointers.

**Anti-changelog rule (Cause 10)**: a ticket filed <60m before its own fix PR is a **defect-record**, not a dispatch instrument — for same-sitting fixes use the consolidated one-issue-per-symptom-wave pattern (#5012), and never grant such records the dispatch template's authority. Dispatch instruments are written BEFORE work starts, by definition.

---

## 7. MODEL-CONTINUITY — which context lives on which durable surface

**Rule zero**: any knowledge load-bearing for more than one session MUST live on a repo-tracked surface. Private auto-memory is a cache, never the system of record — it is invisible to peers, fresh containers, sub-agents, CI, and any non-Fable model (Cause 8: six lesson classes were VIOLATED AGAIN from memory-only storage). **Rule zero applies to enforcement scripts too**: a gate that lives at `/home/openova/bin/` is destroyed with the host and invisible to a fresh container — load-bearing scripts move into `scripts/` with, at most, a thin host-cron wrapper exec'ing the repo copy.

| Surface | What lives there | Enforcement reach |
|---|---|---|
| `CLAUDE.md` (repo) | Repo structure, terminology pointers, platform-specific hard rules (NodePorts, autonomy, domains canon), the read-order list | Every session, every model, every peer |
| `.claude/` (repo-tracked, NEW) | The generic engineering rules currently dangling as §-references (§0/§3/§5/§9/§11/§13) — chepherd's regeneration of `~/.claude/CLAUDE.md` clobbered the user-global anchor; a repo-tracked file cannot be destroyed by respawn | Every session in this repo |
| `docs/PROTOCOL.md` (this doc) | Release-train (incl. RT-10), janitor gates + §5.0 protect-list, Step-0 re-verification rule, dispatch template pointer, parallel-lanes map, founder-gated register | Read-order item; referenced by TRACKER.md |
| `docs/PRINCIPLES.md` | STONE principles verbatim (release-train, never-wipe-converged, merged≠deployed≠verified, live-patch twins) + the repo-wide-enumeration-first rule (Cause 5) | Canonical doc #5 in the mandated read order |
| `docs/RUNBOOKS.md` | Huawei/kom4dc pre-flight chapter with the §5.1 quota table AND the §5.0 named protect-list (promote `docs/runbooks/preflight-sovereign-provision.md` content); cutover-FIRST prov sequence; converged re-fire runbook (incl. RT-10 walk-first gate); 2-fires/day cap | Canonical doc #6 |
| `docs/STATUS.md` / `docs/DOD.md` | Re-stamped to hw24x/hw25x truth: Huawei kom4dc substrate, 11-step cutover, Sandbox-Pillar-4 scope removal | Canonical docs #2/#4 |
| `docs/adr/` | ADR-0013: sovereign-ref canon amending ADR-0012 — "deny-egress hold is the sovereignty proof; containerd redirect is the mechanism; lint rejects only refs outside HOST_PROJECT_MAP" (#5038's ruling); ADR-0014: 11-step chain superseding ADR-0002's 8-step spec | Immutable, additive |
| `docs/ledger/TRACKER.md` | Live lanes (§3), rescoped gate denominator, weekly rework%-vs-progress% row, founder-gated-items age column (per §1.3 register) with 48h escalation trigger, close-audit summary row (W5) | Cron-refreshed, dashboard-visible |
| `scripts/refresh-dod-dashboard.sh` (repo-tracked, MOVED from `/home/openova/bin/` as part of W6) + `docs/ledger/gates.yaml` (NEW — the gate-set/denominator definition) | The DoD dashboard renderer + the machine-readable definition of WHICH gates constitute the north-star denominator (fixes the "41/41 = 100%" theater at its definition, not just its rendering). Host cron becomes a thin wrapper: `exec <repo>/scripts/refresh-dod-dashboard.sh` | Survives host death; auditable in PR review; a fresh container can regenerate the dashboard |
| `docs/ledger/UAT.md` + `TRUST.md` | Per-row walk stamps with triggering-merge SHA (surface-aware on MERGE-triggered restamps only — §8/C6; fire-triggered full flush per founder canon unless F2 granted); per-fix proof stays attached to ISSUES so it survives env death | Cron + walk agents |
| `.github/` (CI + templates) | The mechanical gates: cutover harness workflow, `lint-cloudinit.sh`, widened `check-kyverno-proxy-images.sh`, close-evidence Action, preflight-wiring regression check, `ISSUE_TEMPLATE/dispatch.md` + labeler | Model-independent — enforces even on a zero-context session |
| `~/.claude/skills/` | Procedures, not facts. **Fix `/close` skill now** — it still enforces the only-the-user-closes rule repealed 2026-06-05 | Session-local; must never contradict repo canon |
| `~/.claude/projects/.../memory/` | Session heuristics, live-env quirks, in-flight investigation state. On writing any rule-shaped memory: same session must open the PR moving it to its repo surface above | Fable-session-local ONLY |

**Continuity test**: a fresh Opus container with ONLY the repo checkout must be able to (a) recite the STONE principles, (b) run the pre-flight gate, (c) produce a #3370-grade ticket from the template, (d) know the 11-step cutover is the spec, (e) re-derive live state via Step 0 (§1.0) instead of trusting the snapshot table, and (f) find the disposition of any in-flight branch in `docs/sessions/*/wip-disposition.md`. If any of those requires auto-memory, the mapping is broken — file it as a canon defect.

---

## 8. Counter-measures — one per confirmed cause, each pinned to an enforcement surface

| # | Cause | Counter-measure | Enforcement surface |
|---|---|---|---|
| C1 | One-defect-per-prov treadmill | Offline 11-step Job-execution harness in CI (CP-1), seeded from `cutover-contract.sh`; cutover-FIRST prov sequence; weekly rework% row whose 2-week breach forces a harness/ADR cycle | `.github/workflows/cutover-harness.yaml` + `docs/RUNBOOKS.md` + `docs/ledger/TRACKER.md` |
| C2 | Forensic-blind single-shot bootstrap | ONE class-fix PR closing #4752+#5042: rt-retry all network-dependent runcmd stages (`cloudinit-control-plane.tftpl:1579/:1584` + peers), FAILED sentinels, "flux never installed" terminal state, PUT-gate decoupling (`cloudinit_log.go:58-62`), until-loop uploader (`huawei/main.tf`), `scripts/lint-cloudinit.sh` CI rule | `infra/providers/_shared/cloudinit-control-plane.tftpl` + `.github/` CI |
| C3 | Self-inflicted substrate destruction | Refuse-fire-over-reclaim in `provisioner.go:1997-2021`; protect-set seeds from the §5.0 **repo-tracked named protect-list** and cross-checks **live cloud inventory**, never the in-memory store alone (closes #4675); converged-env wipe gate on the wipe endpoint; protect-by-default regression tests extending `reclaim_protect_4614_test.go` | `docs/RUNBOOKS.md` §protect-list + `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go` + required CI check on janitor/reclaim/wipe paths |
| C4 | Unenforced hygiene gates | `fire()` execs `prov-preflight.sh` as hard exit-gate (auto-COOK); VPC/EVS/EIP checks + 20-min headroom poll + protect-list check; `EVSQuotaHook`/`EIPQuotaHook` server-side twins; CI wiring-regression check | `scripts/sovereign-lifecycle.sh` + `provisioner.go` + `.github/` CI |
| C5 | Registry-pivot whack-a-mole | ADR-0013 sovereign-ref canon (amending ADR-0012); `check-kyverno-proxy-images.sh` widened to render EVERY chart in `platform/`+`products/`, fail-closed at PR time (the curated-4-chart list hid ~11 violators behind `allowExistingViolations:true`); 2-consecutive-fire recurrence of any symptom class mandates repo-wide-enumeration-first (#5010 pattern); W7 disposes of the orphaned registry-pivot WIP branch | `docs/adr/0013-*.md` + `scripts/check-kyverno-proxy-images.sh` (required check) + `docs/PRINCIPLES.md` |
| C6 | Evidence-destruction loop | **Scoped to MERGE-triggered restamps ONLY**: surface-aware `reset-uat.py` restamp path + cron — on a chart/dir merge, flush/flip ONLY rows whose owning surface appears in the merged diff (allowlist map), stamping the triggering merge SHA (kills the SSO-restamp over-flip class, `feedback_dispatch_file_editing_agents_in_worktree_and_sso_restamp_overflips.md`). **FIRE-triggered resets remain FULL FLUSH** per the founder's 2026-06-08 RESET-UAT-ON-FIRE rule + `each_new_env_flushes_all_evidence` — this plan does NOT override that canon; the honest consequence is that C6 only *narrows* (not breaks) the evidence-destruction loop, and the remaining levers are (a) per-fix proof attached to ISSUES surviving env death, (b) the ≤2 fires/day cap, (c) release-train batching reducing fire count, (d) RT-10 extraction-before-refire. If full loop-breakage is wanted, that is **F2 in the founder-gated register (§1.3)** — escalated via Lane C, never self-granted. | `scripts/reset-uat.py` + restamp cron + `docs/RUNBOOKS.md` + §1.3 F2 |
| C7 | Closure theater | GitHub Action on issue-close: re-open + label any close whose final comment lacks live evidence (URL/HTTP code, kubectl output, or UAT row stamp dated AFTER the fix's deploy-bump commit); hard-reject the phrase "Live-proof deferred"; re-open #4961/#4973/#4975 now; **W5 one-time audit sweep over BOTH historical mass-close batches (~75 issues) classifying each as evidence-backed / re-open / walk-debt (§2.3)**; rescope the gate denominator via repo-tracked `docs/ledger/gates.yaml` + `scripts/refresh-dod-dashboard.sh` (moved into the repo — the host-local copy at `/home/openova/bin/` is retired to a thin exec-wrapper) | `.github/workflows/close-evidence-gate.yaml` + `scripts/refresh-dod-dashboard.sh` + `docs/ledger/gates.yaml` + `docs/sessions/<date>/close-audit.md` |
| C8 | Memory-only enforcement | The operating-canon commit (§7 table): STONE→PRINCIPLES.md, quota table + protect-list→RUNBOOKS.md, ADR-0013/0014, STATUS/DOD re-stamp, repo-tracked `.claude/` rules file, repo-tracked dashboard script + gates.yaml, `/close` skill fix | The 7 canonical docs + `docs/adr/` + `.claude/` + `scripts/` — all in the mandated read order |
| C9 | False serialization | Parallel-lanes block (§3) encoded in `docs/ledger/TRACKER.md`; **Agenity walk anchored to ANY converged env, keystone re-affirmation only (§2.2, D2)**; standing 48h-age escalation for founder-gated items via `chepherd.alert_human`; retire the retrospective's "#4788 never shipped" error (PR #4789 merged 07-05) and move those rows to the walk-debt lane | `docs/ledger/TRACKER.md` (cron-rendered, dashboard-visible) |
| C10 | Ticket-as-changelog | `.github/ISSUE_TEMPLATE/dispatch.md` (§6 anatomy) + labeler rule blocking `status/in-progress` without a binary DoD section; sanctioned #5012-style consolidated defect-records for same-sitting fixes | `.github/ISSUE_TEMPLATE/` + labeler workflow |

---

## 9. Evidence-gated Definition of Done per deliverable

**Universal rule**: done = operator-walked live evidence. PR merge is never done. Agent reports are CLAIMS — the orchestrator re-queries live state before propagating any "done". **This rule binds the intermediate CP milestones exactly as it binds D1–D4** — "CP-N done" without its §9.0 artifact is the merge-as-verification pattern C7 forbids.

### 9.0 Milestone evidence gates (CP-0 / CP-1 / CP-2) — NEW

**CP-0 done** requires ONE of:
- [ ] Retry/sentinel machinery observed firing in a **live cloud-init log** from a real prov (log excerpt showing a retried stage or a FAILED sentinel + successful final log push, committed to `docs/sessions/<date>/evidence/`), OR
- [ ] A **harness-injected failure**: CI/kind run that severs the network mid-flux-install and demonstrates (a) the retry loop re-attempting, (b) the "flux never installed" terminal state surfacing in phase1-watch, (c) the failure-trap log push completing — run output committed.
- [ ] Plus: `scripts/lint-cloudinit.sh` CI rule demonstrated red on a synthetic un-retried runcmd item (CI run link).

**CP-1 done** requires:
- [ ] The harness workflow green in CI executing the actual step Jobs (not render-only), AND
- [ ] At least one **historical defect reproduced red-then-green** (e.g. a step-08 deny-egress or lint case from the hw242/hw246 stack) — proving the harness detects the class it exists for. CI run links committed to `docs/sessions/<date>/`.

**CP-2 done** requires:
- [ ] The RT-10 preconditions met and recorded (named env, `LANE-B-EXTRACTED: <SHA>` in the train manifest).
- [ ] The cutover chain progressing **past the previous hw246 failure point**: the step-08 lint Job `Completed` (`kubectl -n catalyst get job <step-08-job> -o yaml` excerpt) + the lint log excerpt showing redirect-aware acceptance under #5038's ruling, committed to `docs/sessions/<date>/evidence/`.
- [ ] CM `self-sovereign-cutover-status` output committed (whatever terminal state is reached — even a NEW later-step defect is CP-2 SUCCESS for #5038's purposes; file the new defect as a harness case + fix PR per RT-4).
- [ ] If CP-2 collapsed per §2.1a-3: the manifest's `CP-2-COLLAPSED` record + the mandatory CP-1 harness case covering the step-08 lint path stand in as the pre-fire proof — and #5038's live proof then rides D1's evidence.

### D1 — Keystone: cutoverComplete=true, zero-touch, fresh 2-region kom4dc
- [ ] Train manifest (§4 RT-1) committed pre-fire; `PRE-FLIGHT PASS vpc/evs/eip` line in the session log.
- [ ] Zero human interventions between `POST /deployments` and `cutoverComplete=true` — the session transcript is the proof of zero-touch.
- [ ] All 11 step Jobs Completed: `kubectl -n catalyst get jobs -l app.kubernetes.io/part-of=self-sovereign-cutover` output committed.
- [ ] Deny-egress hold: CCNP manifest + timestamps proving ≥10 min hold + cluster-green during hold (`kubectl get hr,kustomizations -A` all Ready inside the window).
- [ ] `kubectl -n catalyst get cm self-sovereign-cutover-status -o yaml` showing `cutoverComplete: "true"`, committed to `docs/sessions/<date>/evidence/`.
- [ ] Post-hold: one Flux reconcile served exclusively from local Gitea+Harbor (source-controller logs excerpt).
- [ ] Issues in the fix train closed ONLY with this evidence linked (C7 gate enforces).

### D2 — Agenity on a converged environment + app provisioning through Agenity with MCP
- [ ] Durable per-Sovereign Anthropic credential registered (founder-provided A1; 48h escalation active until landed).
- [ ] `bp-agenity` installed per-Org on **any converged env** (the keystone env if it exists, otherwise the best surviving converged env — §2.2; the walk NEVER waits on the cutover retry loop); HR Ready output committed.
- [ ] Live walk: chat responds (screenshot + wire capture); MCP tool call visibly executes (tool-call payload in evidence).
- [ ] **An application provisioned end-to-end THROUGH Agenity**: the conversation, the resulting Application CR (`kubectl get applications.apps.openova.io -A`), and the app's HTTP 200 at its tenant URL (`<orgslug>.omani.homes` canon — never openova.io) — screenshots + wire capture.
- [ ] Evidence from a non-keystone converged env is FULL credit for this deliverable; once the keystone env exists, a re-affirmation walk (A4) re-stamps it there — a walk, not a gate.
- [ ] #4111/#4277 closed with this evidence on the issues themselves.

### D3 — UAT ledger walked green
- [ ] Every ☐/⏳ row stamped from a live walk on the keystone env — or on a surviving converged env for rows whose owning surface has not changed since their last green (the C6 surface-aware rule, which governs **merge-triggered** restamps) — each stamp carrying env + date + evidence link.
- [ ] Count includes ALL row-ID formats (numeric + R#/G# — `feedback_uat_count_all_row_id_formats_not_just_numeric.md`).
- [ ] Zero rows evidenced from a wiped env: fire-triggered resets remain FULL FLUSH per founder canon (§1.3 F2 is the only path to changing that), and `reset-uat.py` runs before every fire. Superseded rows (99–107, 17) per founder verdict.
- [ ] `docs/ledger/TRUST.md` surfaces flipped VERIFIED-PASS by the read-only walk agent, never by the fix author.

### D4 — Protocol adoption itself (this document)
- [ ] All §8 enforcement surfaces merged AND demonstrated firing once (e.g. close-evidence Action re-opens a test close; preflight gate refuses a synthetic over-quota fire in CI; dashboard renders from repo-tracked `scripts/refresh-dod-dashboard.sh` + `gates.yaml`).
- [ ] W5 close-audit table committed with all ~75 historical closes classified; every (b)-class issue re-opened; walk-debt rows visible in Lane B's list.
- [ ] W7 wip-disposition table committed; `fix/4973-4975-4961-comprehensive-registry-pivot` deleted (local + origin) with every substantive commit accounted for.
- [ ] The §7 continuity test (a)–(f) passes on a fresh zero-memory session.
- [ ] `docs/ledger/TRACKER.md` renders the lanes + rework% row + founder-gated-age column (A1, F2); the false "41/41 = 100%" banner is gone; §5.0 protect-list present in RUNBOOKS.md.

---

*Precision notes carried from adversarial verification, to prevent re-deriving errors: #4788's fix shipped as PR #4789 (07-05) — walk-debt, not code-debt. #4675's second victim was production omantel.biz (dep 2c3f7c34), not hw204. Wipe-side EVS drain/backstop already landed (PRs #4678, #5029) — the missing pieces are fire-side. hw242's step-08 death was a mixed netpol+registry stack. The contract script is 52 cases on main. "One defect per fire" is the median, not a law — which is exactly why the harness (C1) beats another fire. The §1.2 snapshot drifts within days (three checkout moves observed 07-12→07-14) — which is why Step 0 (§1.0) is mandatory, not advisory.*

---

## 10. 🗿 STONE — the completion measurement is FROZEN (founder, 2026-08-02)

Founder verbatim: *"You cannot keep chaning measurement!!!! When ever i ask you must usr the 100 same measurement approch. Carve this on stone."*

Every completion-percentage report to the founder computes EXACTLY this, in this order — never a new formula, denominator, or differently-derived headline:

1. **Source**: `origin/main:docs/ledger/UAT.md`, ALL row-ID formats (numeric/R#/G#), verdict read from the STATUS column only (`scripts/uat-tally.py` semantics).
2. **Carry-forward is ALWAYS applied**: every ☐ row inherits its most recent non-☐ verdict from the ledger's git history (walking back through resets and mechanical flips until a real verdict is found). A newer stricter verdict is never overridden by an older greener one; the raw flushed number is never the headline.
3. **THE headline number** = carry-forward ✅ / (total − ⛔ − N/A). One number, one formula.
4. **Format 1**: the fixed per-EPIC family table (Console/UI/Cloud-view · Multi-region/DR · Sovereignty-Cutover · Marketplace+Funnel · Robustness/Ops · Governance · SSO/Auth · Agentic · Overall) with columns tot/✅/⚠️/◑/☐/❌/⛔/pct/what-is-still-wrong.
5. **Format 2**: durable Δ vs the stored baseline (memory `project_completion_matrix_canonical_format.md`), artifact-per-cell — the durable score moves ONLY on walk evidence, never mid-conversation, never in response to pressure in either direction.
6. No other percentage (raw-flushed %, half-credit %, structural %) is ever presented as the completion number.

Why: across July the founder was shown raw-flushed, pct-formula, durable, and carry-forward numbers in different sessions — each defined, but the headline appeared to jump (91→57) without the product changing. One frozen measurement ends that.
