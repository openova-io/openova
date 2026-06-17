# UAT regeneration discipline — the agreed browser-walk standard (meta runbook)

## Status — last validated: hw159.omani.works (2026-06-18) — browser walk: **9 ✅ / 0 ❌ / 0 GAP**

> **hw159 browser-walk verdict (2026-06-18, real screenshots).** All 9 rows PASS on the fresh
> `hw159.omani.works` env (dep `c117f6fd4e2eb2dd`, kom4dc, 2 VPCs `me-east-215-a` / `-b`). **Part A (6):**
> the zero-click handover lands on `/dashboard` signed-in (env switcher `hw159.omani.works`, avatar `E`,
> no login form); the avatar menu reads **"Signed in as emrah.baysal@openova.io"**; and the bare URLs for
> **grafana**, **registry.hw159 (Harbor)**, **gitea**, and **openbao** each land **zero-click signed-in** as
> `emrah.baysal@openova.io` — no login form on any. (As on prior envs, the working Harbor hostname is
> **`registry.hw159`**; `harbor.hw159` is not the SSO host.) **Part B (3):** the GitHub-rendered `UAT.md`
> H1 + banner + 🌟 North-Star table name **only `hw159` (2026-06-18, dep `c117f6fd4e2eb2dd`)** with
> **zero `hw150`/`hw144`/`hw128`** mentions (verified live: hw159×17, hw150/hw144/hw128 = 0; the 4 `hw158`
> tokens are the explicit "still carries stale hw158 markers" disclaimers in the scoreboard caption, not
> leaked predecessor evidence). The wiped-predecessor evidence is fully flushed.

> **Prior hw158 verdict (2026-06-17) — superseded by the hw159 walk above; retained for history.** All 9
> rows passed on hw158 (dep `ab2135d4cf2d01e4`) the same way.


> **This is the META / discipline runbook for the whole UAT walkthrough set — owning ticket #3581.**
> It does NOT test a single feature surface; it defines **how every other runbook in this folder is
> walked and how [`../UAT.md`](../UAT.md) is regenerated on each fresh env.** The discipline it
> codifies **IS the agreed browser-walk format**: every per-ticket runbook is a 100% browser walk, and
> screenshots are the only acceptance evidence.

> **The prior curl/script/kubectl format of this very runbook is REPLACED.** Earlier revisions of this
> doc walked the discipline itself with `grep` / `python3 scripts/…` / `kubectl get hr` and pasted
> command output as "evidence". **That is exactly what the agreed standard bans for walk rows.** Command
> output is NOT acceptance. The single genuinely-script step in the regeneration loop (`reset-uat.py`) is
> an **operator-tooling action**, documented in its own clearly-separated section below — never mixed
> into a browser-walk row. Every browser-walk row starts at `☐` and is satisfied only by a real browser
> landing on a rendered screen, captured as a screenshot.

> **Env: `hw158.omani.works`** (dep `ab2135d4cf2d01e4`, provisioned 2026-06-17, single physical kom4dc
> region, 2 VPCs `me-east-215-a` / `-b`). The predecessor env to prove flushed is **hw150** (wiped/void).
> **Maps to:** the whole [`../UAT.md`](../UAT.md) regeneration (this is the discipline that produces it).
> **Index:** [`README.md`](README.md). Prior-env and prior-format evidence is void: every row starts unwalked.

---

## The agreed standard, in one paragraph

A UAT walk is **100% browser**. Each per-ticket runbook is a table of `| Tested page | Description |
Status | Evidence |` rows. **Tested page** is a clickable link to a live page on the current env;
**Description** is the action a human takes plus the screen they must SEE; **Status** is `☐` until a
real browser walk flips it `✅` (or `❌` on a witnessed defect, or `GAP` where the surface exposes no
web-UI at all); **Evidence** is the screenshot path the walker fills under
`docs/sessions/<date>/evidence/`. **A redirect that ends on a login screen / PIN form / 404 / 500 / 503
is a `FAIL`, not a pass.** A rendered, working screen is `✅`. `curl`, `kubectl`, `grep`, and
command-output transcripts are **NOT** acceptance evidence for any walk row — they may appear only as
operator-tooling notes, never as the proof a screen renders.

---

## The two laws this discipline enforces (founder, verbatim)

- *"never combine the evidences from old environments to the new environments, each new environment
  flushes all the evidences and seek for new evidence!!!"* (2026-06-14, the hw138 "fake snapshots"
  rebuke — `feedback_each_new_env_flushes_all_evidence.md`).
- *"each table testing activity must be related to a step in the catalyst ui … go to this url and
  login, and then open that page and click this etc each test case case by case and link by link."*
  (2026-06-03 — `feedback_uat_doc_must_be_ui_walk.md`).

---

## Part A — The browser walk that PRODUCES the fresh evidence

Open each surface below in a **fresh incognito window** against the live env. Each row you tick produces
one screenshot under `docs/sessions/2026-06-17/evidence/` that the regenerated `UAT.md` then links. This
screenshot is the evidence that **replaces** the wiped predecessor's screenshots — there is no
curl/kubectl fallback. A handover URL signs you in once for the whole walk
(`https://console.hw158.omani.works/auth/handover?token=<RS256 JWT>`, `sub=emrah.baysal@openova.io`,
`role=sovereign-admin`); it 302-redirects to `/dashboard` with **no login form**, and every app below is
then opened at its **bare public URL** in the same browser session.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | — | ☐ | — |
| — | Click the avatar (top-right) → menu reads **"Signed in as emrah.baysal@openova.io"** with a Sign-out item — confirms the landed identity is the owner-admin | ☐ | — |
| — | Open the bare URL → lands on **Grafana Home** ("Welcome to Grafana", full UI, Profile avatar), `?orgId=1`, **no login form** (SSO landed signed-in) | ☐ | — |
| — | Open the bare URL (Harbor registry) → lands on **`/harbor/projects`** (9 projects, 69 repos, Administration nav), **no login form**; user dropdown shows **`emrah.baysal@openova.io`** | ☐ | — |
| — | Open the bare URL → lands on the **gitea dashboard titled "emrah.baysal - Dashboard - Catalyst Gitea"**, logged in; URL stays on `:443` | ☐ | — |
| — | Open the bare OpenBao UI → final rendered screen is the **authenticated Vault session** (`/ui/vault/secrets`, "Secrets Engines" with cubbyhole/ + secret/ kv engines), **NO `/ui/vault/auth` token form** | ☐ | — |

> **Honesty rule:** a redirect that ends on a login screen / 404 / 500 / 503 is **`FAIL`**, not `✅`. Any
> surface you do not walk this session stays `☐` in the regenerated docs — never a carried green, never a
> curl `302` substituted for a rendered screen.

---

## Part B — Verify the OLD env's evidence is fully FLUSHED (in the browser)

The heart of the ticket: prove the regenerated `UAT.md` and its linked evidence name **only** the live
env. Walk this in the GitHub web UI (the repo browser) — open the rendered file, read it on screen, and
screenshot what you see. No `grep`; you are reading the rendered page with your eyes.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [UAT.md (rendered on GitHub)](https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md) | — | ☐ | — |
| [UAT.md 🌟 North-Star table](https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md) | — | ☐ | — |
| [UAT.md (rendered on GitHub)](https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md) | — | ☐ | — |

---

## Part C — Operator-tooling note (NOT a browser-walk row; NOT acceptance)

> ⚙️ **This section is operator tooling, deliberately separated from the browser-walk rows above.** The
> regeneration loop has exactly **one** genuinely-script step: `scripts/reset-uat.py`, which a wipe →
> fresh-prov runs to flush `UAT.md` back to `☐` for the new env. It is **plumbing the operator runs at
> prov time, not a test a human walks in a browser**, so it lives here and produces **no walk evidence**.
> Its correctness is a code/unit concern; the acceptance for the discipline is Parts A and B above.

The reset is wired into `scripts/sovereign-lifecycle.sh fire`, so on every fresh prov the flush happens
automatically before the walk begins:

```
# Runs automatically inside `sovereign-lifecycle.sh fire <env>` — operator does not run it by hand.
python3 scripts/reset-uat.py <new-env>
#   → restamps the UAT.md H1 to "(RESET <date> — pending <new-env>)"
#   → flips every per-ticket walk-table Status cell back to ☐ and clears Evidence cells to —
#   → keys off the env label argument (no hardcoded env) so it re-anchors to ANY next env
```

After the reset runs at prov time, the operator performs the **browser walk** (Parts A and B) on the new
env and pastes the fresh screenshots. The script clears the slate; the browser walk fills it. The two are
never conflated: the script is tooling, the screenshots are acceptance.

---

## Acceptance

This discipline is **accepted on the current env** when the operator, in a browser, ticks every `☐` in
Parts A and B: every external surface lands on a **rendered, signed-in admin screen** (screenshot
linked), any unwalked surface is honestly `☐`, the rendered `UAT.md` banner + North-Star table name
**only** the live env with zero predecessor evidence, and every linked screenshot resolves under
`docs/sessions/2026-06-17/evidence/hw158-*.png`. The `reset-uat.py` operator step (Part C) is plumbing,
not a walk row — it is not part of the screenshot acceptance. **PR-merge ≠ accepted; the witnessed
browser walk on the current env is acceptance.** This walk **re-fires on every wipe → fresh prov**, and
the `reset-uat.py` step re-blanks the set so the next walk starts clean.
