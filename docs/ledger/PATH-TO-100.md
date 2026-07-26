# PATH TO 100% — **hw290 current** (refreshed 2026-07-26)

> **Source of truth:** [`UAT.md`](UAT.md). **Current env = hw290** (`hw290.omani.works`, dep `31106b6aa4a7e8e7`, 2-region Huawei me-east-215 / -b-1).
> This file maps every non-green row to its gate + owner. A row stays non-green until the hw290 walk verifies it — **merge ≠ green** (founder rule).

---

## Where the ledger actually stands

| symbol | meaning | count |
|---|---|---|
| ✅ | green | **179** |
| ⚠️ | partial | 44 |
| ❌ | fail | 22 |
| ⛔ | superseded / not achievable as written | 19 |
| ◑ | partial | 10 |
| ☐ | unwalked | 6 |
| N/A | not applicable | 1 |

**179 / 281 = 63.7%.**

Two corrections worth stating plainly, because both changed the plan:

1. Earlier in this session the figure was quoted as **69.1%**, computed against a 259-row denominator. The ledger has **281** rows. 63.7% is the honest number.
2. The remaining work had been summarised as *"6 unwalked rows; everything else traces to #5387 or #4277."* That is wrong. Excluding the 19 ⛔ and the 1 N/A, **82 rows are actionable non-green**, and the 44 ⚠️ partials are by far the largest group. They had been treated as effectively done. They are not.

---

## Gate 1 — the Pillar-5 wedge (G11, 166, 165)

The cutover halts at step 3/11 and cannot proceed. Root cause established with live evidence.

Cutover step-03 `harbor-prewarm` runs the #4982 settled-roll pre-flight, which refuses while any non-suspended HelmRelease is not Ready. On hw290 region-a that predicate returns **7 offenders, every one a per-Org customer app**; region-b returns **0**. Three are `Stalled=True / RetriesExceeded` — Flux has **stopped retrying permanently**, so they will never clear themselves — and four are merely `dependency 'delta-corp/bp-keycloak' is not ready` behind one of them.

The pre-flight is **not** defective. Two hypotheses were tested against live state and disproved: it does not miscount suspended HRs (line 418 skips them correctly), and the inert `bp-*-hcloud` / `bp-catalyst-secondary-edge` slots are correctly skipped as suspended-by-design.

| gap | fix | state |
|---|---|---|
| Pre-flight said "wait for these to settle" even for releases that can never settle | #5391 → PR **#5392** | open, all gates green |
| No operator remedy for a stalled per-Org HR — one broken customer app can permanently prevent a Sovereign becoming sovereign | #5391 | **open — needs a decision, below** |
| Row 165: no per-row Re-run control for a failed cutover step | build in flight | in progress |

The row-165 finding is precise: **the backend re-drive capability already exists and is correct.** `runCutover(…, operatorRetry=true)` (`cutover.go:1413`) deletes and re-runs a genuinely-failed step, reached via the session-gated operator CTA and via `cutoverSourceRetriesFailedStep(source)` for `source=operator|reconcile`. What is missing is any UI that calls it — `grep -rli cutover` across **both** console trees returns one file, whose only match is an unrelated string in `TopologyPicker.svelte`.

**Open question needing a founder/architect decision (#5391):** should a per-Org *customer* app's HelmRelease gate the *platform's* cutover at all? Exempting them is not obviously safe — the tags a broken app would roll to still need mirroring — so this is deliberately not being resolved unilaterally.

---

## Gate 2 — per-Org app family (#5387: fixed and live; runtime proof in flight)

Rows 86, 90, 224, 226, 232, 233, 234, R16, R17, and the funnel partials 87, 89, 93.

Root cause was a **classifier gap, not a missing retry.** Gitea's `ErrPushOutOfDate` is absent from its `handleCreateOrUpdateFileError` mapping table, so a branch-head CAS loss escaped as a bare **500** whose body says `non-fast-forward` (hyphenated). `isGiteaRefRaceError` matched 409 / `422 sha does not match` / `not a fast forward` / `cannot lock ref` — none of those match. It was classified fatal, returned on attempt 1, and the rebase-safe CAS retry from #3376/#5234 sat unreachable behind the gap, having never executed once. The tell was in the live message: no "ref-race persisted after N attempts", because zero retries ran.

Compounding it, the writer raced **itself** — one `/provisioning/apps/install` per cart entry, each in a detached goroutine, all re-rendering the whole cart against the same branch head. That is why app count was never the trigger (epsilon-corp failed on a single-app cart).

Fixed in PR #5390, merged `05b9a2358`, and **live on hw290** as `services-provisioning:05b9a23` (chart 1.4.1222, rolled zero-touch by the GitOps loop at 19:21Z). Runtime proof walk in flight.

This does **not** repair Orgs already in the terminal state — `delta-corp` and `gamma-corp` broke before the fix and still hold the Gate-1 wedge.

---

## Gate 3 — founder-gated credential (#4277)

Rows 219, 220, 222 and G8/G9 need the Anthropic credential; `seedAnthropicToken` loud-skips without it. **No engineering work can clear these.** This is the one genuinely external dependency in the whole ledger.

---

## Gate 4 — filed defects with named owners

| rows | defect | state |
|---|---|---|
| 110, 112, 114, 115 | #5389 per-app Open/launch does not land in the app | filed, unfixed |
| 35, R9 | #5358 guacamole blank page after the SSO round-trip completes | filed, reopened on runtime evidence |
| G12 | #5388 region-kill failback left a data split-brain | filed, unfixed |
| R17 | #5364 org-delete leaves a half-teardown | filed, reopened on runtime evidence |
| 212, 213 | per-Org MCP absent — `bp-openova-mcp` was `DependencyNotReady` behind the platform HR | under investigation |
| — | #5385 deployment health aggregate reads stale-degraded (trust kubectl, not the badge) | filed, affects walk trust |

---

## Gate 5 — the 44 ⚠️ partials, which are the real bulk of the remaining work

By family: catalog 9 (123, 130, 140, 142, 143, 147, 148, 149, R21) · topology 8 (48, 52, 55, 59, 60, 69, 188, + 61 ❌) · meta/ledger 7 (180–186) · model 4 (3, 4, 22, 25) · SSO 4 (R5, 32, 33, 37) · e2e-journey 3 (216, 218, + 221/223 ◑) · funnel 3 (87, 89, 93) · placement 2 (111, 113) · and R2, R22, G4, G10, 121, 177, 192, 225, 228, 229, 241.

**These do not yet have per-row root causes, and that is the honest state.** They were stamped partial by walkers across five waves and have not been triaged since. Assigning a cause per row from memory would be exactly the speculation the principles forbid.

**Triaging this group is the highest-leverage remaining action** — 44 rows, against 3 in Gate 1 and 12 in Gate 2.

---

## Not achievable as written (⛔ 19)

Placement rows 98–109 are superseded by the #4325 devcluster reclassification (founder verdict). Plus R1/M1/G5 (janitor), R19 (sandbox — concept removed 2026-06-30), R20 (delivery), 94/95 (funnel).

These should not be chased. For the ledger to reach 100% they need either rewriting to match the shipped design or formal exclusion from the denominator. **That is a founder call, not an engineering one.**

---

## §854 disposition — closed, not a code gap

A live 2-region Sovereign runs **383 Services with zero NodePorts**. Full three-cluster audit committed at [`docs/sessions/2026-07-26/854-nodeport-audit-hw290.md`](../sessions/2026-07-26/854-nodeport-audit-hw290.md). The mothership's 5 belong to other products sharing our ops cluster; 3 are generated by cert-manager, which creates HTTP-01 solver Services as NodePort by default and which no chart declares. OpenOva's own ClusterIssuers are dns01-webhook-only, so a Sovereign cannot produce one. Guards: CI `check-no-nodeports.sh` + Kyverno Enforce (#5088), made tamper-evident by #5386.
