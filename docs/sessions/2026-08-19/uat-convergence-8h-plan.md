# UAT 286/286 — Retrospective & 8-Hour Convergence Plan

**2026-08-19/20 · derived from `origin/main`, 19-agent forensic workflow `wf_7eb86a00-b1a`, every load-bearing claim adversarially verified (1 refuted → strengthened the diagnosis)**

**Baseline: 243 ✅ / 18 ❌ / 25 ☐ = 285 of 286 stamped · 85.0% · honest 8-hour ceiling: 97.2%**

---

## The one thing only the founder can do

- Anthropic credential (the ONLY external input for G8/G9/220/221): a USABLE, UNEXPIRED full {"claudeAiOauth":{...}} JSON blob — key-only sk-ant is refused by the classifier. Deliver FRESH around H5:00 (the access token lives ~4h and nothing refreshes the OpenBao catalyst/anthropic/token path); an early copy at H0 additionally enables PATH-A auto-seed at prov time. Be ready to re-issue once if the walk slips past token expiry.
- #5393 founder-gated call: execute if it gates a targeted row this window (per the brief); otherwise explicitly defer so it is not silently blocking.
- Nothing else — Sovereign wipes/provs and every credential on our own pre-live infra are autonomous per the 2026-06-04 and 2026-08-06 hard principles.

---

## Score trajectory (moves ONLY on merged reclassification or live walk — never on open PRs)

| Checkpoint | Expected |
|---|---|
| H0 baseline (origin/main 4eaec1a3c) | 243/286 = 85.0% (18 ❌ / 25 ☐) |
| H0:30 — PR 6479 merged (Lane A first merge) | 243 ✅ unchanged = 85.0%; fail composition reclassifies per the merged ledger's guard-checked tally — verified branch parse says 18❌ → 14❌ + 4⛔ (G8/G9/220/221); the brief's 10+8 figure did not match the verified parse, so report the merged tally, not the brief. Reclassification only — zero green movement. |
| H3:00 — merge train complete, prov converged | 85.0% — open/merged PRs and a converged env move nothing; scores move only on stamps. |
| H5:00 — 25 SSO rows re-walked | 268/286 = 93.7% IF all 25 re-confirm green (they were live-green on hw296/hw298; each stamp = env+date+proof). Partial walks report only stamped rows. |
| H5:30 — rows 115, 218 walked; 228/234 stamped by construction | 272/286 = 95.1% (conditional on live green; a permission-only 115 check is a FAIL, not a pass) |
| H7:00 — G8/G9/220/221 walked inside token life; 213/223 via handover bearer | 276-278/286 = 96.5-97.2%. G8/G9/220/221 conditional on a usable founder blob + outcome=seeded; 213/223 conditional on the handover-bearer path passing the row's own -32000/-32003 assertions. |
| H8 close — honest floor/ceiling | Ceiling 278/286 = 97.2% (remaining 8: 90/95 post-cutover, G7 m-plan-gated, 60/222-class real defects — next window). Floor if the prov fails twice: 243/286 = 85.0% with Lane A/E hardening banked; no projected greens are ever reported as score. |

---

## Critical path

1. H0:00-0:30 — reset-uat + canonical wipe of prior env + LE-TLD/capacity preflight (autonomous; not the bastion)
2. H0:30 — fire fresh prov on current origin/main (catalog already carries guacamole 0.2.43 pin + agenity orgTag 37fe649 — the merge train is NOT a prov dependency)
3. H0:30-2:30 — converge; H2:30-3:00 delivery verification (bp-agenity card seeded, guacamole seed job rendered, SSE phases green)
4. H3:00-5:00 — re-walk the 25 SSO rows (26-28, 30-45, 109-114): largest single reclaim, +8.7 pts, needs only re-confirmation on an env carrying merge 8423fa355
5. H5:00-5:30 — rows 115 + 218 live walks; stamp 228/234 by construction from this prov cycle's own evidence
6. H5:30-5:45 — seed catalyst-system/sovereign-anthropic-credentials on the Sovereign (PATH B, fresh founder blob); verify classifier outcome=seeded and ExternalSecret READY
7. H5:45-7:00 — walk G8/G9/220/221 inside the ~4h token life; 213/223 MCP leg via handover-signed bearer (shred key after), since #6362 is open
8. H7:00-8:00 — 3-cell restamps + stamp guard BEFORE push + push; closing measurement via the repaired origin/main tally (Lane E)

---

# 8-Hour Convergence Plan — five parallel lanes

**Ground rules:** score moves ONLY on merged reclassification or live walk evidence (env + date + proof, 3-cell restamp, guard before push). One env at a time, wipe-before-fire. Never NodePort. Browser walkers serialized (shared browser). Gate on exit codes, never printed output; `fail=0 pending=N` is not green.

**Key sequencing insight (verified):** the catalog on main **already** carries every fix the walk needs — guacamole 0.2.43 pin (`8d8857c84`), agenity emitter in orgTag `37fe649`. The merge train is **NOT** a prov dependency; its cutover-chart fixes (0.1.193/0.1.194) only affect post-cutover rows (90/95) that are out-of-window anyway. So the prov fires at H0:30, not after the train.

## 🔴 CRITICAL PATH: Lane B → Lane C (prov → converge → SSO walk → credential seed → G8/G9/220/221)

## Lane A — Merge train (worker, H0–H3, then FREEZE)

- **H0:00–0:30** — PR **6479**: run `python3 scripts/uat-partition.py --write` on the branch, commit regenerated WBS §1 with UAT.md, confirm `--check` green, **merge**. This is the only score-adjacent merge: it reclassifies founder-gated fails ❌→⛔. Per the adversarially-verified branch parse that is **18 ❌ → 14 ❌ + 4 ⛔** (220/221/G8/G9); the brief's "10-fail + 8-blocked" figure does not match the verified parse — report the merged ledger's own guard-checked tally, whatever it says at merge time. Green count is unchanged (243 ✅). Simultaneously PR **6476**: reword "resolves #6475" in body AND commit message (amend + force-push), merge on green.
- **H0:30–1:30** — PR **6480**: re-run cancelled Playwright run 32211529417 (5 shards died on the 30 m timeout, zero code failures); merge on full green. If shards time out again, split the shard — don't raise the timeout blindly. In parallel, **recreate 6470** from origin/main: cherry-pick chart commits, **drop the TRACKER.md hunk entirely** (cron re-conflicts it every 15 min; that's why checks were suppressed — dirty PRs get zero check-runs), sync agenity catalog-seed pins to 0.5.30 (blueprints.yaml lines 6038/6068) via the release writer. #6375 is closed, so the version-claim gate clears.
- **H1:30–2:15** — merge 6470 (umbrella 1.4.1521). Rebase **6473**, sync cutover seed pins to 0.1.193 (lines 5118/5135), re-run, merge (1.4.1523).
- **H2:15–3:00** — rebase **6474** onto post-6473 main (same-line Chart.yaml conflicts guaranteed — serial only), pins to 0.1.194, merge (1.4.1524).
- **H3:00 onward — SSO-surface MERGE FREEZE** until Lane C's SSO stamps are pushed. Any merge touching the SSO surface mechanically re-flips 25 rows to ☐ (that's the TRUST rule, and it's correct).

## Lane B — Fresh prov + converge (orchestrator) 🔴

- **H0:00–0:30** — Preflight: `python3 scripts/reset-uat.py <env>`; wipe the prior env via canonical `POST …/deployments/{id}/wipe` (autonomous — not the bastion; debug-before-wipe if it failed: pull cloudinit-log first). LE TLD rotation check (≥4 provs this week on the TLD → flip `parent_domains_yaml`). Capacity check on the Huawei project.
- **H0:30** — **FIRE** `POST /sovereign/api/v1/deployments` on current main. If the founder's Anthropic blob has already landed, PATH A auto-seed fires at kubeconfig postback (requires mothership Secret + catalyst-api restart — only do this if the blob arrived; otherwise skip, PATH B covers it later with zero mothership risk).
- **H0:30–2:30** — Converge. Watchers must self-test before watching (canon).
- **H2:30–3:00** — Delivery verification before any walk: bp-agenity card present in per-Org catalog seed; guacamole 0.2.43 seed job rendered; SSE phases green. If the prov fails: cloudinit-log first, then one wipe+refire max (one env at a time); a second failure drops 213/223 from the window.

## Lane C — The walk (single serialized walker) 🔴

- **H3:00–5:00** — **25 SSO rows** (26–28, 30–45, 109–114): the largest single reclaim (+8.7 pts). These were measured-live green on hw296/hw298 and only need re-confirmation on an env carrying merge `8423fa355` — this prov does. Full evidence per row.
- **H5:00–5:30** — Row **115** (Guacamole ALL CONNECTIONS non-empty via authed API + browser; a permission-only check is a FAIL), row **218** (bp-agenity card + wizard opens, chart resolves not-404). Rows **228/234** close by construction on this very prov cycle (wipe→re-prov janitor sweep; funnel Org with mail) — stamp them from this cycle's evidence.
- **H5:30–5:45** — **Seed the Anthropic credential, PATH B**: `kubectl -n catalyst-system create secret generic sovereign-anthropic-credentials --from-literal=apiKey=… --from-file=credentialsJson=…` on the Sovereign with the founder's **fresh** blob. Reconciler live-reads within ≤10 min (or trigger via Org-create). Verify: catalyst-api log `outcome=seeded` (NOT `unusable-credential-seeded`/`withheld`); per-Org `agenity-anthropic-token` ExternalSecret READY=True; `seed-claude-creds` init completes.
- **H5:45–7:00** — Walk **G8/G9/220/221 immediately** (access token lives ~4 h, nothing refreshes the OpenBao path). Then **213/223** MCP leg with a **handover-signed bearer** (the row's own adjudicated auth path while #6362 is open; shred the extracted key after — prefer the issuance endpoint). Own-Org success as control; cross-Org −32000/−32003 assertions.
- **H7:00–8:00** — Restamp per canon (3 cells: env, verdict, evidence), run the stamp guard BEFORE push, push. Run the repaired tally (Lane E) as the closing measurement.

## Lane D — Real-defect fixes, honest go/no-go (fixer)

- **#6362 per-realm JWKS** (durable fix for 213/223's stamped failure): **GO** if started by H1 (~3 h). Merged ≠ delivered — this env won't receive it; the walk uses the handover bearer regardless. Value: next train.
- **PATH-TO-100.md:29 doc fix** (10 min): **GO** — replace "deployment POST body" (field does not exist) with the two verified paths.
- **#6360 full prewarm image set** (rows 90/95, ~4 h): **NO-GO for this window's score** — post-cutover rows; cutover won't complete + walk in-window. PR 6474 already lands the vacuity guard; full fix is next-window backlog.
- **G7 / row 60 / 222**: **NO-GO** — G7 is walk-gated on an m-plan funnel purchase (fold into a future walk), 60's remainder and 222 are unscoped for 8 h. No theater fixes.

## Lane E — Signal + WBS repair (loop maintainer)

- **H0:00–0:30** — Repoint the supervisory tally to origin/main content: `git fetch && git show origin/main:scripts/uat-tally.py + git show origin/main:docs/ledger/UAT.md` → verified to emit 243/18/25 = 85.0%. Signal restored in 10 minutes.
- **H0:30–1:00** — Fix the checkout: preserve local state (**including untracked `docs/ledger/uat-cycles.csv`**, modified `uat-raw.csv`, staged `uat-snapshot.py` — the verifier caught that the untracked CSV was missed in the original stash list), `git switch main && git pull --ff-only`. **Quarantine/delete the bogus hw299 `198,41` cycle row** — it's stale-tally output masquerading as a measurement, not trend data.
- **H1:00–1:30** — Staleness guard: before any tally, assert `HEAD == origin/main` post-fetch; refuse with the divergence count otherwise (this exact 1680-commit drift then fails loud).
- **H7:00–8:00** — Regenerate WBS §1 from final UAT (`uat-partition.py --write`); reconcile trend CSVs with the current env boundary; issue the 6-hourly convergence report separating real greens from pending.

## What this window does NOT claim

No green without a stamp. No cutover-dependent rows (90/95, 164-class) — cutover on the fresh env is a stretch goal at H7:30 only if every pre-cutover stamp is pushed, and its rows score in the NEXT window. Open PRs move nothing.

---

# Retrospective — how 97.6% became 85.0% (and why that was the system working)

## The headline, honestly

The score on `origin/main` `docs/ledger/UAT.md` is **243 ✅ / 18 ❌ / 25 ☐ over 286 rows = 85.0%**, down from a peak of **279 ✅ / 7 ❌ = 97.6%**. Adversarial re-derivation of the full 40-commit ledger history confirms: **this is measurement honesty, not a code regression.** Both drop commits are docs-only (`git show --name-only` proves zero non-`docs/` files), so no code change rode in with the score change.

| Event | Commit | Rows | What happened |
|---|---|---|---|
| Step 1: carried-credit reclaim | `c46685d42` | 11 rows ✅→❌ (90, 95, 115, 213, 218, 223, 228, 234, G7, G8, G9) | `uat-drift-guard` refused 21 greens carried from wiped hw296; re-walk on live hw298 unmasked **pre-existing** defects (each with a filed issue: #6360, #6353, #4293, #5991, #6362, #4435, #4307) plus 2 precondition-absent rows |
| Step 2: TRUST invalidation | `4eaec1a3c` | 25 SSO rows ✅→☐ (26–28, 30–45, 109–114) | Mechanical application of the TRUST rule ("every PR against a surface flips it back to UNVERIFIED until re-walked"), triggered 41 s after SSO-chain merge `8423fa355` (PR #6363). Every cell preserves its prior proof verbatim |

The 40-commit sweep found **7 commits with pass-flips, all with documented causes, zero silent regressions**. The one flip not named in its commit message (row 242 in `7130c712b`) is documented in its own evidence cell and re-walked green two restamps later. The 18 ❌ are real product defects — but they **predate** both commits.

## Process failures of the last 48 h

**1. The supervisor-signal defect (the big one).** The loop that reported "198 pass / 41 fail (69.2%)" was tallying a checkout parked on branch `docs/row92-source-resolved` — HEAD from 2026-08-10, **1680 commits behind origin/main**. Every UAT script resolves `REPO_ROOT` relative to its own file, so the loop faithfully counted a 9-day-old ledger with a tally script that was itself stale (missing the ⏳ glyph → 3 rows printed as N/A). Worse — verification found the contamination went **downstream**: an untracked `docs/ledger/uat-cycles.csv` in that worktree contains a fabricated-looking cycle row `2026-08-16,hw299,…,198,41,…,69.2` — the stale tally was recorded as if it were a real hw299 measurement. (This also refuted one forensic sub-claim — "no CSV contains 198/41" — the only refutation across all ten angles; it strengthened the diagnosis.) The supervisor was steering on a signal that read clean because the wrong thing was measured — the exact class our memory canon warns about.

**2. Carried-credit accumulation.** 47 rows were carried from wiped hw295 (`9f76e121a`), then hw296 credit was carried onto hw298 until the drift-guard forced the reckoning. Carrying is allowed under the 2026-08-11 "wipe is not a failure" policy, but 48 h of accumulated carry meant the correction landed as one visible 11-row cliff instead of a gradual drip.

**3. Delivery-gating not surfaced in the score.** hw298 is post-cutover and severed from GitHub (step-05 pivots Flux to local Gitea), so merged fixes #6353/#6363 **structurally cannot reach it**. Four of the 18 ❌ are delivery-gated, not code-gated — the ledger says so in-cell, but the headline number doesn't distinguish, which read as "things are breaking" when the truth was "fixes are merged and waiting for an env."

**4. A wrong runbook line.** `PATH-TO-100.md:29` told operators to supply the Anthropic credential "in the deployment POST body." **No such field exists** (provisioner `Request` struct grepped clean with controls). The real paths are a mothership Secret+env (boot snapshot, restart required) or a per-Sovereign `catalyst-system/sovereign-anthropic-credentials` Secret (live-read every 10 min, no restart).

**5. PR hygiene debt.** PR 6470 went `mergeable_state=dirty` on a `TRACKER.md` hunk that a 15-minute cron re-conflicts forever — and GitHub suppresses **all** check-runs on dirty PRs, which looked like CI breakage. Two chart PRs (6473/6474) moved chart versions without moving catalog-seed pins. One PR body used "resolves #6475" in prose.

## Guards that worked, and guards now in place

The gates **caught everything**: `uat-drift-guard` refused stale greens (21→0), the catalog-seed lockstep tests caught both pin drifts, the auto-close-keyword gate caught the prose, `check-uat-partition-derived` caught WBS §1 drift. The TRUST rule fired mechanically within 41 seconds of a surface-touching merge. Nothing red is unexplained.

Newly required from this retro: a **staleness guard** in the supervisory loop (assert `HEAD == origin/main` after fetch, refuse with divergence count), tally via `git show origin/main:` instead of worktree paths, and quarantine of the bogus hw299 cycle row before it poisons trend analysis.

## The recovery math

The drop is cheap to reverse because most of it is re-confirmation, not fixing: 25 SSO rows were measured-live green as recently as hw296/hw298 and carry their prior evidence in-cell; rows 115/218 are fixed on main with pins already in the catalog (guacamole 0.2.43, agenity via orgTag `37fe649`); 228/234 close by construction on the next prov cycle; G8/G9/220/221 need exactly one external input — a usable Anthropic OAuth blob. That is **~28–33 rows** reachable with one fresh prov and one credential, against 5 rows (90, 95, G7, 60, 222-class) that need real engineering or preconditions beyond this window.

---

## Risks & mitigations

- Fresh prov fails or converges slowly (quota, cloud-init chain, LE rate limit) — the whole critical path slips. Mitigation: TLD-rotation + capacity preflight before fire; debug-before-wipe (cloudinit-log first); at most one wipe+refire (one-env-at-a-time); if still down at H3, drop 213/223 from the window and walk what the env gives.
- Anthropic token expires before/during the G8/G9/220/221 walk (~4h life, no refresher on the OpenBao path). Mitigation: PATH-B seed at H5:30 with a FRESH founder blob, walk those 4 rows immediately after outcome=seeded; pre-arranged one-time re-issue from the founder.
- A merge touching the SSO surface after the re-walk mechanically re-flips 25 rows to UNVERIFIED (TRUST rule — correct behavior, catastrophic timing). Mitigation: hard SSO-surface merge freeze from H3:00 until Lane C stamps are pushed; all merges front-loaded H0-H3.
- Merge-train serialization breaks: 6470/6473/6474 edit the same Chart.yaml version lines and seed-pin lines 5118/5135; TRACKER.md re-conflicts every 15 min via cron. Mitigation: recreate 6470 WITHOUT the TRACKER hunk; strict serial rebase-after-merge; sync seed pins with the release writer's own tooling, never by hand.
- Playwright e2e 30m timeouts recur (6480/6473 class), tempting a merge on pending. Mitigation: split the shard, never raise timeout blindly; hard rule fail=0 pending=N is NOT green.
- 213/223 walked with a console token re-fails (#6362 open — MCP trusts only 2 handover RS256 keys). Mitigation: walk with a handover-signed bearer per the row's adjudication (shred key after, prefer issuance endpoint); ship #6362 in Lane D for the durable fix; never stamp green from a vacuous pass.
- Supervisor-signal regression: the loop drifts back to a stale checkout or the contaminated uat-cycles.csv poisons trend data. Mitigation: tally exclusively via git show origin/main:; staleness guard refusing on HEAD != origin/main; quarantine the bogus hw299 198/41 row before any reconciliation.
- Walk-evidence theater: parallel browser walkers hijack tabs; HTTP/2 pins one backend making repeat-loads unrepresentative. Mitigation: one serialized walker; fresh-TCP curl for any round-robin claim; 3-cell restamp + stamp guard before every push.
- Expectation mismatch on 6479: reporting the brief's 10-fail+8-blocked when the verified branch parse says 14❌+4⛔ would be a fabricated improvement. Mitigation: report only the merged ledger's guard-checked tally.
- hw298/hw296 nostalgia: any attempt to deliver fixes to the severed post-cutover env wastes the window (step-05 severed GitHub; step-01 re-mirror is not a fix path this window). Mitigation: all delivery via the fresh prov only; never wipe a working Sovereign to deploy code — the prior env is wiped under the one-env rule, not for delivery.

---

*Full per-angle findings: workflow journal `wf_7eb86a00-b1a` (19 agents, 0 errors, 380 tool calls). Verdicts: all load-bearing claims CONFIRMED except one (the "no CSV contains 198/41" sub-claim — REFUTED: the stale worktree's untracked `uat-cycles.csv` contains exactly that row recorded as a bogus hw299 measurement; quarantine it).*
