# UAT — Regenerate the walkthrough doc set with fresh live evidence on the CURRENT env

> **Owning ticket: #3581.** This is the **founder-walkable acceptance walk** for the *set-wide
> UAT-regeneration discipline* — proving that, after a wipe → fresh prov, the entire UAT walkthrough
> doc set + `docs/ledger/UAT.md` banner names **only the env that is live right now**, carries **zero**
> predecessor-env evidence, is in the **click-by-click UI-walk-table** format, and that the structural
> enforcer (`scripts/reset-uat.py` + a drift guard) actually clears stale evidence on the live format.
>
> **Env under walk: `hw150.omantel.biz`** · cluster token `1290a8ef` · provisioned 2026-06-16 · single
> physical kom4dc region (2 VPCs `me-east-215-a` / `-b`). Live build pin
> `catalyst-api`/`catalyst-ui` = `80a9d2c`. Replace the env tokens below with whatever env is live when
> you walk this — the walk is **env-agnostic** (that is the generality proof in §G).

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
`https://console.hw150.omantel.biz/auth/handover?token=<RS256 JWT>` (`iss=console.openova.io`,
`sub=emrah.baysal@openova.io`, `role=sovereign-admin`). It 302-redirects to `/dashboard` and you are
signed in — **no login form**. Every app below is then opened at its **bare public URL** in the same
browser session.

---

## Part A — The live-env walk that PRODUCES the fresh evidence

Walk these in a browser against the live env. Each row you tick produces one `hw150-NN-*.png` under
`docs/sessions/2026-06-16/evidence/` that the regenerated `UAT.md` then links. This is the evidence
that *replaces* the wiped predecessor's screenshots.

| # | Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|---|
| A1 | `https://console.hw150.omantel.biz/auth/handover?token=<JWT>` | Paste the handover URL into a fresh tab, press Enter | 302 → `/dashboard`, signed in, **no login form**; left-rail nav (Dashboard, Cloud, Apps, Jobs, Compliance, Users, Organizations, Settings); env switcher reads **hw150.omantel.biz**; avatar **E** top-right | ☐ |
| A2 | `https://console.hw150.omantel.biz/dashboard` | Click the avatar **E** (top-right) | Menu opens: **"Signed in as emrah.baysal@openova.io"** + **Sign out** — confirms the landed identity is the owner-admin | ☐ |
| A3 | `https://console.hw150.omantel.biz/apps` | Look at the Applications grid + page title | The grid renders the INSTALLED deployments; the title flips to **"✓ Sovereign ready"** | ☐ |
| A4 | `https://grafana.hw150.omantel.biz/` | Type the bare URL in a fresh tab, Enter | Lands on **"Home — Dashboards — Grafana"**, **no login form**, avatar top-right (SSO landed signed-in) | ☐ |
| A5 | `https://registry.hw150.omantel.biz/` | Type the bare URL in a fresh tab, Enter | Auto-redirects to **`/harbor/projects`**, **no login form**; `emrah.baysal@openova.io` top-right | ☐ |
| A6 | `https://bao.hw150.omantel.biz/` | Type the bare URL in a fresh tab, Enter | OIDC round-trip completes → **`/ui/vault/secrets`** Secrets Engines, **no `/ui/vault/auth` login form** | ☐ |
| A7 | `https://gitea.hw150.omantel.biz/` | Type the bare URL in a fresh tab, Enter | Title **"emrah.baysal — Dashboard — Catalyst Gitea"**, logged in | ☐ |

> Capture each landed screen as `docs/sessions/2026-06-16/evidence/hw150-0N-*.png`. **Honesty rule:** a
> redirect that ends on a login screen / 404 / 500 / 503 is **❌**, not ✅. Any surface you do not walk
> this session stays **⏳** in the regenerated docs — never a carried green.

---

## Part B — Verify the OLD env's evidence is fully FLUSHED (the carryover check)

This is the heart of the ticket: prove the regenerated docs name **only** the live env. These are
verification steps a reader performs against the committed doc set — the "Go to" is a file/grep on the
repo (a documentation surface with no console UI, so flagged 🖥️ per the format law).

| # | Go to (file / command) | Then do | You should see | Result |
|---|---|---|---|---|
| B1 🖥️ | `docs/ledger/UAT.md` (open in the repo browser) | Read the H1 + the `>` banner block at the very top | H1 reads **"ground reality on `hw150.omantel.biz`"**; banner names **deployment `1290a8ef` · 2026-06-16 · build pin `80a9d2c`** — and **no `hw144` / `hw139` / `hw128`** anywhere in the banner | ☐ |
| B2 🖥️ | `grep -coi 'hw144\|hw139\|hw128' docs/ledger/UAT.md` | Run it | Output is **`0`** (was **46** before regen — the carryover is gone) | ☐ |
| B3 🖥️ | `grep -rl 'hw144\|hw139\|hw128' docs/ledger/uat-walkthrough/ docs/ledger/UAT.md` | Run it | **No output** — every managed file (`UAT.md` + the 11 walkthrough `.md`s) is re-anchored; not one predecessor FQDN survives | ☐ |
| B4 🖥️ | `docs/ledger/UAT.md` 🌟 4-North-Stars table | Read each "On this env" cell + its Evidence link | Every cell names hw150 and links an `hw150-*` screenshot OR reads **⏳ not yet walked this env**; **no cell** asserts a green it inherited from hw144 (e.g. the old "region-kill PASSED on hw144 RTO ≈ 4s" line is gone — replaced by the live result or ⏳) | ☐ |
| B5 🖥️ | Any screenshot link in `docs/ledger/UAT.md` | Click one | It resolves under `docs/sessions/2026-06-16/evidence/hw150-*.png` (NOT `2026-06-15/.../hw144-*`) and the image opens | ☐ |

---

## Part C — The structural enforcer actually works on the LIVE format

`scripts/reset-uat.py` is wired into `scripts/sovereign-lifecycle.sh fire` to flush `UAT.md` on every
re-prov. §3 of the ticket proves the *old* script was a **no-op** on today's prose format (it only
matched `TC-`/`SSO-`/`TOPO-`/`VC-`-prefixed rows; the current file has **0** such rows). This part
proves the upgraded enforcer clears the evidence regardless of row prefix.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| C1 🖥️ | `grep -cE '^\s*\|\s*(TC-\|SSO-\|TOPO-\|VC-)' docs/ledger/UAT.md` | Run it (documents WHY the old script failed) | **`0`** — the current file has no legacy-prefixed rows, so the old regex matched nothing → the "RESET" stamp lied while 46 hw144 lines survived | ☐ |
| C2 🖥️ | `cp docs/ledger/UAT.md /tmp/uat.before && python3 scripts/reset-uat.py hwTEST` | Run the **upgraded** enforcer against a throwaway env label | It restamps the H1 to `(RESET <today> — pending hwTEST)` **and** reports `N rows -> ⏳/—` with **N > 0** (it now clears the prose tables, not just the header) | ☐ |
| C3 🖥️ | `grep -coi 'hw150' docs/ledger/UAT.md` (after C2) | Run it | The env-specific tokens are blanked to the new label — the count drops sharply vs `/tmp/uat.before`; walk-table Result cells now read **⏳** and Evidence cells **—** | ☐ |
| C4 🖥️ | `diff /tmp/uat.before docs/ledger/UAT.md` then `git checkout docs/ledger/UAT.md` | Eyeball the diff, then revert the demo edit | The diff shows the env tokens + screenshot links cleared (proving the enforcer works on the live format); the revert restores the real hw150 page (the demo does NOT get committed — only the upgraded script does) | ☐ |

---

## Part D — The drift guard refuses a stale-evidence commit

A tiny guard makes "stamped but not walked" impossible to commit: it greps the doc set for any
`hwNNN`/FQDN token that is **not** the env declared in the `UAT.md` H1 and exits non-zero.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| D1 🖥️ | the drift guard (e.g. `python3 scripts/uat-drift-guard.py`) on the clean re-anchored set | Run it | Exit code **`0`** — the live set names only hw150, no mismatch | ☐ |
| D2 🖥️ | plant a stale token: append `hw128.omani.works` into a copy, run the guard on it | Run it | Exit code **non-zero** + a message naming the offending file/line — the guard CATCHES the carryover a human would miss | ☐ |
| D3 🖥️ | remove the planted token, re-run the guard | Run it | Back to exit **`0`** — confirms the guard is precise, not a blanket fail | ☐ |

---

## G — Generality proof (the DoD is satisfied by the MECHANISM, not by one env's page)

The whole point: this regen is **env-agnostic**. Prove it re-anchors to *any* env with the identical
commands — no hardcoded `hw150`.

| # | Go to (command) | Then do | You should see | Result |
|---|---|---|---|---|
| G1 🖥️ | `grep -rn 'hw150' scripts/reset-uat.py scripts/uat-drift-guard.py` | Run it | **No output** — the enforcer + guard carry NO hardcoded env; they key off the H1-declared env + a generic `hwNNN.<tld>` pattern | ☐ |
| G2 🖥️ | `python3 scripts/reset-uat.py hwANOTHER` (a second arbitrary label), then `git checkout docs/ledger/UAT.md` | Run it, read the stamp, revert | The H1 restamps to `pending hwANOTHER` (NOT a hw150-special path) — the same machinery re-anchors the *next* wipe's env; revert the demo | ☐ |
| G3 🖥️ | re-read the ticket §9 DoD box 7 | Confirm | The acceptance bar is the **mechanism** (enforcer + guard + template re-anchor on the next env by the same commands), so a fresh prov to `hwNNN+1` is one `reset-uat.py <env>` + this walk away from an honest page | ☐ |

---

## Automated cross-checks — already green, NOT acceptance

> These are unit-test-grade checks. They are **not** acceptance — acceptance is the founder walking
> Parts A–D + G above in a browser/terminal. Listed here only so a walker can sanity the substrate.

- `kubectl --kubeconfig /tmp/hw150.kubeconfig get hr -A` → 60/63 Ready (the 3 not-Ready are
  `bp-cluster-autoscaler-hcloud` / `bp-hcloud-ccm` / `bp-velero`, hcloud-provider charts inert on a
  Huawei prov — expected, not a regression).
- `kubectl --kubeconfig /tmp/hw150.kubeconfig -n catalyst-system get deploy catalyst-api -o jsonpath='{.spec.template.spec.containers[0].image}'`
  → `…/catalyst-api:80a9d2c` (the live build pin the banner must name — proves which build the walk witnessed).
- `curl -sk -o /dev/null -w '%{http_code}' https://console.hw150.omantel.biz/` → `200`
  (and `grafana`/`bao` → 302, `gitea` → 303, `registry` → 200 — the env is walkable).
- `grep -coi 'hw144\|hw139\|hw128' docs/ledger/UAT.md` → `0` after regen (the carryover gate, mirrored as B2).

---

### Acceptance

This walk is **accepted** when the founder, on the live env, ticks every ☐ in Parts A–D + G:
the `UAT.md` banner + every walk table name only the live env, every ✅ links a current-env screenshot,
every not-walked surface is ⏳, `grep` for any predecessor FQDN returns nothing, the upgraded enforcer
demonstrably clears the live format, the drift guard refuses a planted stale token, and the mechanism
re-anchors to a second arbitrary env unchanged. **PR-merge ≠ accepted; the witnessed walk on the
current env is acceptance.** This walk **re-fires on every wipe → fresh prov.**
