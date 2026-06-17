# UAT — Regenerate the walkthrough doc set with fresh live evidence on the CURRENT env

## Status — last validated: hw158 (2026-06-17, re-validated on the RECOVERED env after #3758 merged to main)

- **Tally (this discipline walk, grep/script/kubectl — NO browser): 14 ✅ · 0 ❌ · 7 N/A** (the 7 N/A are the Part-A browser rows A1–A7; this is a discipline runbook walked with grep/scripts/kubectl per the dispatch — the screens those rows would capture already exist as `hw158-01..05`/`-09`/`-12` and resolve, but a no-browser session cannot *witness* the landed screen, so they are honestly N/A, not ✅).
- **Re-validation (2026-06-17, this session): the prior 3 ❌ (B2 / B3 / D1) ALL FLIPPED → ✅ after #3758 (`9e276d6fc`, "fix(uat): unblock the predecessor-env drift-guard merge gate") merged to `main`.** The fix (a) reworded the H1 to `# UAT — live walkthrough on \`hw158\` (2026-06-17)` so the guard's `header_env()` regex `on\s+\`?(hw\d+)` now auto-parses `hw158`, (b) stripped the bare `hw144 / hw150 / hw130` literals out of the UAT.md void-prior disclaimer, and (c) archived the 19 re-introduced stale files + skips this meta runbook. Re-checked live on `main` (HEAD `ff63a8e8a`, with `9e276d6fc` confirmed ancestor): `python3 scripts/uat-drift-guard.py` (BARE) → **exit `0`** "OK — 12 ledger file(s) carry no stale walk-evidence (current env: hw158)"; `grep -coi 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` → **`0`**; `grep -rl …` → **no output (exit 1)**; `head -1` → `# UAT — live walkthrough on \`hw158\` (2026-06-17)`.
- **Headline verdict: ✅ PASS — the regeneration MECHANISM is real and works, AND all three literal/enforcer gates now pass clean on the live page.** (Was 🟡 PARTIAL before #3758.)
  - ✅ **The regen happened:** [`../UAT.md`](../UAT.md) IS re-anchored to **hw158** — H1 = `# UAT — live walkthrough on \`hw158\` (2026-06-17)`, the 4-North-Star table + all 14 rows name `hw158` (`hw158.omani.works`, dep `ab2135d4cf2d01e4`); every evidence token resolves to an existing `hw158-NN-*.png` under `../../sessions/2026-06-17/evidence/`; **zero predecessor screenshot tokens** (`grep -oE 'hw1(44|50|39|28|30)-[0-9]+'` → none). `scripts/reset-uat.py` + `scripts/uat-drift-guard.py` both exist (exec), both are substantive (not no-ops), `reset-uat` IS wired into `scripts/sovereign-lifecycle.sh fire`, and the drift-guard `--self-test` is **ALL PASS**.
  - ✅ **B2 / B3 PASS (literal):** the bare env tokens are gone from the UAT.md void-prior disclaimer (#3758) → `grep -coi 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` = **`0`** and `grep -rl …` → **no output**. The results file is now literally grep-clean.
  - ✅ **D1 PASS (header auto-parse fixed):** the **bare** `python3 scripts/uat-drift-guard.py` on the clean set now exits **`0`** ("OK — 12 ledger file(s) carry no stale walk-evidence (current env: hw158)"). The H1 reword `UAT — live walkthrough on \`hw158\`` matches `header_env()`'s `on\s+\`?(hw\d+)`, so it auto-resolves `hw158` — no `--env` flag needed any more.
- **Structural enforcers (the deliverable's heart) — re-verified working this session:** `reset-uat.py hwTEST` restamped the H1 to `(RESET … — pending hwTEST)` and flipped cells across 11 files (hw158 count 67→23); `reset-uat.py hwANOTHER` cleared **140 cells across 11 files** to `pending hwANOTHER`; the drift-guard **catches** a planted `hw128` token **BARE** (exit 1) and clears on removal (exit 0) — the `--env` workaround the prior PARTIAL needed is no longer required. No demo edit committed (all reverted via `git checkout`; `git status --short docs/ledger/` clean).
- **Substrate drift note (NOT acceptance):** live `kubectl get hr -A` on `/tmp/hw158-kc.yaml` now reads **49/64 Ready** (not the banner's 61/64) — `bp-catalyst-platform`/`bp-gitea`/`bp-grafana`/`bp-keycloak`/`bp-guacamole`/`bp-newapi`/`bp-oidc-gate`/`bp-continuum` among the not-Ready, i.e. the env has degraded since the walk was recorded. Console still `200`, grafana/bao `302`, gitea `303`, registry `200` (walkable). This does not change the regen-discipline verdict but the banner's HR tally is stale.
- **Maps to:** the whole [`../UAT.md`](../UAT.md) (this is the meta/discipline runbook for the set, owning ticket #3581).
- **Evidence:** [`../UAT.md`](../UAT.md) banner + every `hw158-*` screenshot; [`README.md`](README.md) (the authoritative index, which itself exists + is honestly stamped to hw158 with the prior hw150 declared void).
- **No CI drift-guard wired:** `grep -rln 'uat-drift-guard' .github/` → **nothing**; the only UAT-related CI workflow is `.github/workflows/sso-uat-flip.yaml`, which runs `scripts/uat-sso-flip.py` (the #3374 SSO-row flipper), **not** `uat-drift-guard.py`. So the staleness guard is a local/manual gate today, not a CI backstop — a real gap vs the guard's own docstring framing ("THIS guard is the CI backstop").
- **Guard now SKIPS this meta-runbook (fixed in #3758):** the prior PARTIAL noted the guard false-positived on this self-referential meta-doc because its *instruction* columns legitimately quote predecessor tokens as **test fixtures** (`grep 'hw150'`, `append hw128.omani.works`). #3758 added a skip for this meta runbook to the guard, so the bare `python3 scripts/uat-drift-guard.py` is now clean across all 12 ledger files (this file no longer trips it) — confirmed this session: bare exit `0`, "12 ledger file(s) carry no stale walk-evidence". The test fixtures in this runbook's instruction columns are preserved intact (the guard skips the file rather than the runbook being rewritten to silence it).
- **Index:** [`README.md`](README.md).

> **Owning ticket: #3581.** This is the **founder-walkable acceptance walk** for the *set-wide
> UAT-regeneration discipline* — proving that, after a wipe → fresh prov, the entire UAT walkthrough
> doc set + `docs/ledger/UAT.md` banner names **only the env that is live right now**, carries **zero**
> predecessor-env evidence, is in the **click-by-click UI-walk-table** format, and that the structural
> enforcer (`scripts/reset-uat.py` + a drift guard) actually clears stale evidence on the live format.
>
> **Env under walk: `hw158.omani.works`** · deployment `ab2135d4cf2d01e4` · provisioned 2026-06-17 · single
> physical kom4dc region (2 VPCs `me-east-215-a` / `-b`). Replace the env tokens below with whatever env is live when
> you walk this — the walk is **env-agnostic** (that is the generality proof in §G). The **predecessor env to prove
> flushed** is now **hw150** (the prior train env, wiped); earlier the predecessors were hw144/hw139/hw128.

**The two laws this walk enforces (founder, verbatim):**
- *"never combine the evidences from old environments to the new environments, each new environment
  flushes all the evidences and seek for new evidence!!!"* (2026-06-14, the hw138 "fake snapshots"
  rebuke — `feedback_each_new_env_flushes_all_evidence.md`).
- *"each table testing activity must be related to a step in the catalyst ui … go to this url and
  login, and then open that page and click this etc each test case case by case and link by link."*
  (2026-06-03 — `feedback_uat_doc_must_be_ui_walk.md`).

**Result legend (used in every table below):**
**✅** witnessed live on this env this session (screenshot linked) · **❌** witnessed-failed live ·
**⏳** not yet walked this env (honest pending — NOT a fail-by-omission, NOT an inherited pass).

**Sign-in (once, for the whole walk):** open the handover URL
`https://console.hw158.omani.works/auth/handover?token=<RS256 JWT>` (`iss=console.openova.io`,
`sub=emrah.baysal@openova.io`, `role=sovereign-admin`). It 302-redirects to `/dashboard` and you are
signed in — **no login form**. Every app below is then opened at its **bare public URL** in the same
browser session.

---

## Part A — The live-env walk that PRODUCES the fresh evidence

Walk these in a browser against the live env. Each row you tick produces one `hw158-NN-*.png` under
`docs/sessions/2026-06-17/evidence/` that the regenerated `UAT.md` then links. This is the evidence
that *replaces* the wiped predecessor's screenshots.

| # | Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|---|
| A1 | `https://console.hw158.omani.works/auth/handover?token=<JWT>` | Paste the handover URL into a fresh tab, press Enter | 302 → `/dashboard`, signed in, **no login form**; left-rail nav (Dashboard, Cloud, Apps, Jobs, Compliance, Users, Organizations, Settings); env switcher reads **hw158.omani.works**; avatar **E** top-right | **N/A** — no-browser session (dispatch: "NO browser"). Live reachability witnessed via `curl -sk -o /dev/null -w '%{http_code}' https://console.hw158.omani.works/` → **`200`**. The captured landed screen already exists: `docs/sessions/2026-06-17/evidence/hw158-01-console-signedin-emrah.png` (resolves). |
| A2 | `https://console.hw158.omani.works/dashboard` | Click the avatar **E** (top-right) | Menu opens: **"Signed in as emrah.baysal@openova.io"** + **Sign out** — confirms the landed identity is the owner-admin | **N/A** — no-browser. Evidence artifact present: `hw158-01-console-signedin-emrah.png`. |
| A3 | `https://console.hw158.omani.works/apps` | Look at the Applications grid + page title | The grid renders the INSTALLED deployments; the title flips to **"✓ Sovereign ready"** | **N/A** — no-browser; not separately screenshotted (covered by the `hw158-13` placement treemap). |
| A4 | `https://grafana.hw158.omani.works/` | Type the bare URL in a fresh tab, Enter | Lands on **"Home — Dashboards — Grafana"**, **no login form**, avatar top-right (SSO landed signed-in) | **N/A** — no-browser. `curl … grafana.hw158.omani.works/` → **`302`** (SSO redirect, walkable). Artifact: `hw158-02-grafana-signedin.png` (resolves). |
| A5 | `https://registry.hw158.omani.works/` | Type the bare URL in a fresh tab, Enter | Auto-redirects to **`/harbor/projects`**, **no login form**; `emrah.baysal@openova.io` top-right | **N/A** — no-browser. `curl … registry.hw158.omani.works/` → **`200`**. Artifact: `hw158-03-harbor-signedin.png` (resolves). |
| A6 | `https://bao.hw158.omani.works/` | Type the bare URL in a fresh tab, Enter | OIDC round-trip completes → **`/ui/vault/secrets`** Secrets Engines, **no `/ui/vault/auth` login form** | **N/A** — no-browser. `curl … bao.hw158.omani.works/` → **`302`** (OIDC redirect, walkable). Artifact: `hw158-05-openbao-signedin.png` (resolves). |
| A7 | `https://gitea.hw158.omani.works/` | Type the bare URL in a fresh tab, Enter | Title **"emrah.baysal — Dashboard — Catalyst Gitea"**, logged in | **N/A** — no-browser. `curl … gitea.hw158.omani.works/` → **`303`** (SSO redirect, walkable). Artifact: `hw158-04-gitea-admin-signedin.png` (resolves). |

> Capture each landed screen as `docs/sessions/2026-06-17/evidence/hw158-0N-*.png`. **Honesty rule:** a
> redirect that ends on a login screen / 404 / 500 / 503 is **❌**, not ✅. Any surface you do not walk
> this session stays **⏳** in the regenerated docs — never a carried green.

---

## Part B — Verify the OLD env's evidence is fully FLUSHED (the carryover check)

This is the heart of the ticket: prove the regenerated docs name **only** the live env. These are
verification steps a reader performs against the committed doc set — the "Go to" is a file/grep on the
repo (a documentation surface with no console UI, so flagged 🖥️ per the format law).

| # | Go to (file / command) | Then do | You should see | Result |
|---|---|---|---|---|
| B1 🖥️ | `docs/ledger/UAT.md` (open in the repo browser) | Read the H1 + the `>` banner block at the very top | H1 reads **"UAT — … `hw158`"**; banner names **`hw158.omani.works` · deployment `ab2135d4cf2d01e4` · 2026-06-17** — and **no `hw150` / `hw144` / `hw139` / `hw128`** anywhere in the banner | **✅** (was ❌; FLIPPED by #3758). `head -1` → `# UAT — live walkthrough on \`hw158\` (2026-06-17)` ✅; banner names `hw158.omani.works` · dep `ab2135d4cf2d01e4` · 2026-06-17 ✅; the predecessor literals were stripped from the void-prior disclaimer → `grep -ni 'hw144\|hw150\|hw130' docs/ledger/UAT.md` returns nothing in the banner. The H1 + identity + banner are now fully clean. |
| B2 🖥️ | `grep -coi 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` | Run it | Output is **`0`** — the predecessor carryover is gone | **✅** (was ❌; FLIPPED by #3758). Observed this session on `main`: `grep -coi 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` → **`0`**. The void-prior disclaimer no longer names bare `hwNNN` tokens, so the results file is literally grep-clean. |
| B3 🖥️ | `grep -rl 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` | Run it | **No output** — `UAT.md` is re-anchored to hw158; not one predecessor FQDN survives in the results file. (The folder runbooks may retain a clearly-labelled *historical-baseline* `hw150` mention in their Status header / before-state columns — those are framed as void-prior-env, not current claims; see [`README.md`](README.md).) | **✅** (was ❌; FLIPPED by #3758). Observed this session: `grep -rl 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` → **no output (exit 1)**. Not one predecessor token survives in the results file. |
| B4 🖥️ | `docs/ledger/UAT.md` 🌟 4-North-Stars table | Read each "On hw158" cell + its Evidence link | Every cell names hw158 and links an `hw158-*` screenshot OR reads **⏳ not yet walked this env**; **no cell** asserts a green it inherited from hw150/hw144 (e.g. an old "region-kill PASSED on hw144 RTO ≈ 4s" line is gone — replaced by the live hw158 result RTO ≈ 1.4 s or ⏳) | **✅** — all 4 cells name hw158, every Evidence ref is an `hw158-*` token, **no inherited green**. Row 4 reads `Live region-kill ✅ … pg_is_in_recovery t→f in RTO ≈ 1.4s, RPO = 0 … hw158-06 + hw158-region-kill-walk-PASS.md` — the live hw158 result, NOT the old `hw144 RTO≈4s` line. `grep -ni 'RTO' UAT.md` confirms only `RTO ≈ 1.4s` (no `4s` / hw144 RTO survives). |
| B5 🖥️ | Any screenshot link in `docs/ledger/UAT.md` | Click one | It resolves under `docs/sessions/2026-06-17/evidence/hw158-*.png` (NOT a predecessor `…/hw150-*` / `…/hw144-*`) and the image opens | **✅** (resolves; note: refs are bare ``\`hw158-NN\``` inline-code tokens, not clickable md links). Every distinct `hw158-NN` token in UAT.md maps to an existing file: e.g. `hw158-01`→`hw158-01-console-signedin-emrah.png`, `hw158-06`→`hw158-06-cloud-2region.png`, …, `hw158-20`→`hw158-20-pdns-admin-redirect-uri-STILL-FAIL.png` (19/19 tokens OK via `ls docs/sessions/2026-06-17/evidence/`). The funnel artifacts (`01-redeem-voucher-valid.png` … `05-post-launch-redirect.png`, `04-org-cr-kubectl-proof.txt`) + `hw158-region-kill-walk-PASS.md` also resolve. **Zero predecessor screenshot tokens:** `grep -oE 'hw1(44\|50\|39\|28\|30)-[0-9]+' docs/ledger/UAT.md` → **none**. |

---

## Part C — The structural enforcer actually works on the LIVE format

`scripts/reset-uat.py` is wired into `scripts/sovereign-lifecycle.sh fire` to flush `UAT.md` on every
re-prov. §3 of the ticket proves the *old* script was a **no-op** on today's prose format (it only
matched `TC-`/`SSO-`/`TOPO-`/`VC-`-prefixed rows; the current file has **0** such rows). This part
proves the upgraded enforcer clears the evidence regardless of row prefix.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| C1 🖥️ | `grep -cE '^\s*\|\s*(TC-\|SSO-\|TOPO-\|VC-)' docs/ledger/UAT.md` | Run it (documents WHY the old script failed) | **`0`** — the current file has no legacy-prefixed rows, so the old `TC-`/`SSO-`/`TOPO-`/`VC-` regex matched nothing → a "RESET" stamp would have lied while the predecessor's lines survived | **✅** — `grep -cE '^\s*\|\s*(TC-\|SSO-\|TOPO-\|VC-)' docs/ledger/UAT.md` → **`0`**. Confirms the prose format carries zero legacy-prefixed rows, so the *old* reset regex was a no-op — which is exactly why the upgraded `reset_prose_row()` path in `reset-uat.py` was needed. |
| C2 🖥️ | `cp docs/ledger/UAT.md /tmp/uat.before && python3 scripts/reset-uat.py hwTEST` | Run the **upgraded** enforcer against a throwaway env label | It restamps the H1 to `(RESET <today> — pending hwTEST)` **and** reports `N rows -> ⏳/—` with **N > 0** (it now clears the prose tables, not just the header) | **✅** — re-observed this session. `reset-uat.py hwTEST` cleared cells across **11 files** (`docs/ledger/UAT.md: 27`); `head -1` after → `# UAT — live walkthrough (RESET 2026-06-17 — pending hwTEST)`. N=27 in UAT.md > 0 ✅. (Reverted via `git checkout docs/ledger/`.) |
| C3 🖥️ | `grep -coi 'hw158' docs/ledger/UAT.md` (after C2) | Run it | The env-specific tokens are blanked to the new label — the count drops sharply vs `/tmp/uat.before`; walk-table Result cells now read **⏳** and Evidence cells **—** | **✅** — re-observed: `grep -coi 'hw158' /tmp/uat.before` → **`67`**; after C2 → **`23`** (sharp drop). Sampled north-star rows post-reset: `\| 1 \| **Every app runs IN a vCluster** … \| ⏳ \| — \|` (all 4 rows Result→⏳, Evidence→—). |
| C4 🖥️ | `diff /tmp/uat.before docs/ledger/UAT.md` then `git checkout docs/ledger/UAT.md` | Eyeball the diff, then revert the demo edit | The diff shows the env tokens + screenshot links cleared (proving the enforcer works on the live format); the revert restores the real hw158 page (the demo does NOT get committed — only the upgraded script does) | **✅** — `diff` showed `✅ PASS … hw158-13` → `⏳ \| —` across the 4 north-star rows + 14 detail rows + ROW-0 CRD/controller rows (env tokens + `hw158-NN` screenshot refs cleared). `git -c user.name=hatiyildiz … checkout docs/ledger/UAT.md docs/ledger/uat-walkthrough/README.md` → `grep -coi 'hw158'` back to **`67`**; final `git status --short docs/ledger/` (excl. this runbook) = **clean**. Demo NOT committed. |

---

## Part D — The drift guard refuses a stale-evidence commit

A tiny guard makes "stamped but not walked" impossible to commit: it greps the doc set for any
`hwNNN`/FQDN token that is **not** the env declared in the `UAT.md` H1 and exits non-zero.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| D1 🖥️ | the drift guard (e.g. `python3 scripts/uat-drift-guard.py`) on the clean re-anchored set | Run it | Exit code **`0`** — the live set names only hw158, no mismatch | **✅** (was ❌; FLIPPED by #3758). Observed this session on `main`: `python3 scripts/uat-drift-guard.py` (BARE, no `--env`) → **exit `0`**, `uat-drift-guard: OK — 12 ledger file(s) carry no stale walk-evidence (current env: hw158).` Root cause of the prior ❌ was the H1 not matching `header_env()`'s `on\s+\`?(hw\d+)` regex; #3758 reworded the H1 to `# UAT — live walkthrough on \`hw158\`` so the env now auto-resolves. `--self-test` → **ALL PASS** (5/5). The `--env` workaround is no longer needed. |
| D2 🖥️ | plant a stale token: append `hw128.omani.works` into a copy, run the guard on it | Run it | Exit code **non-zero** + a message naming the offending file/line — the guard CATCHES the carryover a human would miss | **✅** (BARE — the auto-parse fix from #3758 means no `--env` needed). Appended `\| 99 \| planted \| ✅ — stale proof (hw128-09) ([x](../sessions/2026-06-15/evidence/hw128-x.png)) \| ✅ \|` to UAT.md → `python3 scripts/uat-drift-guard.py` → **exit `1`** + `FAIL — 1 ✅ row(s) cite a wiped/predecessor env`. The guard CATCHES the planted hw128 carryover. (Done in-repo then reverted; `git status --short docs/ledger/UAT.md` clean after.) |
| D3 🖥️ | remove the planted token, re-run the guard | Run it | Back to exit **`0`** — confirms the guard is precise, not a blanket fail | **✅** (BARE). `git -c user.name=hatiyildiz … checkout docs/ledger/UAT.md` (removes the planted row) → `python3 scripts/uat-drift-guard.py` → **exit `0`**, `OK — 12 ledger file(s) carry no stale walk-evidence (current env: hw158)`. Confirms the guard is precise (clears the instant the stale token is gone), not a blanket fail. |

---

## G — Generality proof (the DoD is satisfied by the MECHANISM, not by one env's page)

The whole point: this regen is **env-agnostic**. Prove it re-anchors to *any* env with the identical
commands — no hardcoded `hw150`.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| G1 🖥️ | `grep -rn 'hw150' scripts/reset-uat.py scripts/uat-drift-guard.py` | Run it | **No output** — the enforcer + guard carry NO hardcoded env; they key off the H1-declared env + a generic `hwNNN.<tld>` pattern | **✅** — `grep -rn 'hw150' scripts/reset-uat.py scripts/uat-drift-guard.py` → **no output**. Confirmed by reading both: `reset-uat.py` keys off `sys.argv[1]` (env label) + a generic `ENV_REF` regex (`hw\d+`, deployment-hex, `sessions/<date>`, `.png/.txt/.json`); `uat-drift-guard.py` keys off `--env`/`$UAT_CURRENT_ENV`/the H1 token + a generic `HW_TOKEN = \bhw\d+\b`. No env label is hardcoded. |
| G2 🖥️ | `python3 scripts/reset-uat.py hwANOTHER` (a second arbitrary label), then `git checkout docs/ledger/UAT.md` | Run it, read the stamp, revert | The H1 restamps to `pending hwANOTHER` (NOT a hw150-special path) — the same machinery re-anchors the *next* wipe's env; revert the demo | **✅** — re-observed this session: `reset-uat.py hwANOTHER` → `UAT reset: 140 evidence cell(s) -> ⏳/☐ across 11 file(s) … pending hwANOTHER`; `head -1` → `# UAT — live walkthrough (RESET 2026-06-17 — pending hwANOTHER)` — the identical machinery re-anchored to a *second* arbitrary label with no special-casing. Reverted via `git -c user.name=hatiyildiz … checkout docs/ledger/UAT.md docs/ledger/uat-walkthrough/` (all restored — final `git status --short docs/ledger/` clean). |
| G3 🖥️ | re-read the ticket §9 DoD box 7 | Confirm | The acceptance bar is the **mechanism** (enforcer + guard + template re-anchor on the next env by the same commands), so a fresh prov to `hwNNN+1` is one `reset-uat.py <env>` + this walk away from an honest page | **✅ (mechanism present; one wiring gap noted)** — the mechanism exists end-to-end: `reset-uat.py` (clears) + `uat-drift-guard.py` (backstop, `--self-test` ALL PASS) + `reset-uat` IS wired into `scripts/sovereign-lifecycle.sh fire` (line 5/24/64: `fire -> runs scripts/reset-uat.py FIRST`). So a fresh prov to `hwNNN+1` = `sovereign-lifecycle.sh fire …` auto-resets, then this walk. **Caveat:** the guard is NOT in CI (`grep -rln 'uat-drift-guard' .github/` → none; the only UAT CI is `sso-uat-flip.yaml` running `uat-sso-flip.py`), so the "CI backstop" the guard's docstring claims is not actually a CI gate yet — it's a local/manual run. |

---

## Automated cross-checks — already green, NOT acceptance

> These are unit-test-grade checks. They are **not** acceptance — acceptance is the founder walking
> Parts A–D + G above in a browser/terminal. Listed here only so a walker can sanity the substrate.

- `kubectl --kubeconfig /tmp/hw158-kc.yaml get hr -A` → **OBSERVED 2026-06-17 this discipline walk: 49/64 Ready**
  (NOT the banner's 61/64 — the env has DEGRADED since the original walk). Not-Ready now include core charts
  `bp-catalyst-platform` / `bp-gitea` / `bp-grafana` / `bp-keycloak` / `bp-guacamole` / `bp-newapi` /
  `bp-oidc-gate` / `bp-continuum` in addition to the expected-inert hcloud charts
  (`bp-cluster-autoscaler-hcloud` / `bp-hcloud-ccm` / `bp-velero`; `bp-velero-hcs` Ready). The HTTP surfaces
  are still up (console `200`, grafana/bao `302`, gitea `303`, registry `200`), but **the banner's 61/64 HR
  tally is now stale** — flag for a re-stamp. This does not change the regen-discipline verdict (this runbook
  tests the *doc mechanism*, not HR health), but it is an honest live-state discrepancy.
- `kubectl --kubeconfig /tmp/hw158-kc.yaml -n catalyst-system get deploy catalyst-api -o jsonpath='{.spec.template.spec.containers[0].image}'`
  → the live build pin the banner must name (re-read the actual pin on whatever env you walk — proves which build the walk witnessed).
- `curl -sk -o /dev/null -w '%{http_code}' https://console.hw158.omani.works/` → `200`
  (and `grafana`/`bao` → 302, `gitea` → 303, `registry` → 200 — the env is walkable).
- `grep -coi 'hw150\|hw144\|hw139\|hw128' docs/ledger/UAT.md` → `0` after regen (the carryover gate, mirrored as B2).

---

### Acceptance

This walk is **accepted** when the founder, on the live env, ticks every ☐ in Parts A–D + G:
the `UAT.md` banner + every walk table name only the live env, every ✅ links a current-env screenshot,
every not-walked surface is ⏳, `grep` for any predecessor FQDN returns nothing, the upgraded enforcer
demonstrably clears the live format, the drift guard refuses a planted stale token, and the mechanism
re-anchors to a second arbitrary env unchanged. **PR-merge ≠ accepted; the witnessed walk on the
current env is acceptance.** This walk **re-fires on every wipe → fresh prov.**
