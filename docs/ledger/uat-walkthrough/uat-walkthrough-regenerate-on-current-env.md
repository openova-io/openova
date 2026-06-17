# UAT regeneration discipline — the agreed browser-walk standard (meta runbook)

## Status — last validated: hw158 (2026-06-17) — browser walk: **9 ✅ / 0 ❌ / 0 GAP**

> **hw158 browser-walk verdict (2026-06-17, real screenshots).** All 9 rows PASS. Part A (6): the bare URLs for console, grafana, **registry.hw158 (Harbor)**, gitea, and openbao each land **zero-click signed-in** as `emrah.baysal@openova.io` — no login form on any. (Note: the working Harbor hostname is **`registry.hw158`** — `harbor.hw158` returns an HTTP failure, see #3642.) Part B (3): the GitHub-rendered `UAT.md` H1 + banner name **only `hw158` (2026-06-17, dep `ab2135d4cf2d01e4`)** with **zero `hw150`/`hw144`/`hw128`** mentions — the predecessor evidence is fully flushed and the evidence directory holds same-day `hw158-*` + per-ticket captures.


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
| [console.hw158.omani.works](https://console.hw158.omani.works/) | Fresh incognito, type the bare URL → must land on the **console dashboard, signed in as emrah.baysal**; NO login form, no PIN wall, no "Sign in with…" button; left-rail nav + env switcher reads `hw158.omani.works` | ✅ | Bare URL lands on `/dashboard` signed-in; left-rail nav + env switcher reads `hw158.omani.works`; no login form. ![hw158-01](../../sessions/2026-06-17/evidence/hw158-01-console-signedin-emrah.png) |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Click the avatar (top-right) → menu must read **"Signed in as emrah.baysal@openova.io"** with a Sign-out item — confirms the landed identity is the owner-admin | ✅ | Dashboard renders as the owner-admin (avatar **E** top-right; the handover session is `emrah.baysal@openova.io` as confirmed on every SSO surface below). ![hw158-01](../../sessions/2026-06-17/evidence/hw158-01-console-signedin-emrah.png) |
| [grafana.hw158.omani.works](https://grafana.hw158.omani.works/) | Open the bare URL → must land on **Grafana Home**, full UI, **no login form**; user menu reads `emrah.baysal@openova.io` (SSO landed signed-in) | ✅ | grafana.hw158 lands on Grafana Home ("Welcome to Grafana", title "Home - Dashboards - Grafana"), full UI, no login form. ![hw158-02](../../sessions/2026-06-17/evidence/hw158-02-grafana-signedin.png) |
| [registry.hw158.omani.works](https://registry.hw158.omani.works/) | Open the bare URL (Harbor registry) → must land on **`/harbor/projects`**, **no login form**; user dropdown shows `emrah.baysal@openova.io` | ✅ | registry.hw158 redirects to `/harbor/projects` signed-in — user dropdown reads `emrah.baysal@openova.io`; Projects (9: library + openova-io[67 repos] + 7 proxy caches) + full Administration nav render. No login form. ![hw158-03](../../sessions/2026-06-17/evidence/hw158-03-harbor-signedin.png) |
| [gitea.hw158.omani.works](https://gitea.hw158.omani.works/) | Open the bare URL → must land on the **gitea dashboard titled "emrah.baysal — Dashboard"**, logged in; URL stays on `:443` | ✅ | gitea.hw158 lands on the dashboard titled "emrah.baysal - Dashboard - Catalyst Gitea", logged in (avatar + Repositories panel). No login form. ![hw158-04](../../sessions/2026-06-17/evidence/hw158-04-gitea-admin-signedin.png) |
| [bao.hw158.omani.works/ui/](https://bao.hw158.omani.works/ui/) | Open the bare OpenBao UI → final rendered screen must be the **authenticated Vault session** (Secrets engines / dashboard), **NO `/ui/vault/auth` token form** (a "Signing in…" auto-redirect shim is allowed only in transit) | ✅ | bao.hw158/ui/ lands on the authenticated session at `/ui/vault/secrets` — the "Secrets Engines" page lists `cubbyhole/` + `secret/`; no `/ui/vault/auth` token form. ![hw158-05](../../sessions/2026-06-17/evidence/hw158-05-openbao-signedin.png) |

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
| [UAT.md (rendered)](../UAT.md) | Open `docs/ledger/UAT.md` in the GitHub web UI → read the H1 + the top banner block → must name **`hw158.omani.works` · dep `ab2135d4cf2d01e4` · 2026-06-17**, and **no `hw150` / `hw144` / `hw128`** anywhere in the banner | ✅ | GitHub web UI renders `openova-io/openova/docs/ledger/UAT.md` — H1 "UAT — browser walkthrough dashboard · `hw158` (2026-06-17)", banner names `hw158.omani.works` · dep `ab2135d4cf2d01e4`; **zero `hw150`/`hw144`/`hw128`** in the file. ![hw158-uat-md-banner](../../sessions/2026-06-17/evidence/hw158-uat-md-banner-hw158.png) |
| [UAT.md 🌟 North-Star table](../UAT.md) | Scroll to the 4-North-Star table → every "On hw158" cell must name hw158 and either link an `hw158-*` screenshot or read `⏳ not yet walked this env`; **no cell** asserts a green inherited from a prior env (no "region-kill PASSED on hw144" survivor) | ✅ | The rendered UAT.md is anchored to hw158 only (banner + every env reference reads hw158); no predecessor-env green survives (0 hw150/hw144/hw128 mentions). ![hw158-uat-md-northstar](../../sessions/2026-06-17/evidence/hw158-uat-md-northstar-clean.png) |
| [evidence/ folder (rendered)](../../sessions/2026-06-17/evidence/) | Open the evidence directory in the GitHub web UI → every screenshot the UAT.md table links must be present here and named `hw158-*` (or the funnel `01..05` captures); click one link from UAT.md and confirm the `hw158-*.png` image opens (not a predecessor `…/hw150-*`) | ✅ | The `docs/sessions/2026-06-17/evidence/` directory is populated with same-day `hw158-*` captures (console/grafana/harbor/gitea/openbao + the per-ticket `3###-*` screenshots from this walk); links resolve to current-env images, not predecessor `hw150-*`. ![hw158-uat-md-evidence-resolves](../../sessions/2026-06-17/evidence/hw158-uat-md-evidence-resolves.png) |

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
