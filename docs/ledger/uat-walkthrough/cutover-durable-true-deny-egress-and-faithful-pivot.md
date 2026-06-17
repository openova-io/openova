# Cutover: durable fact · true deny-egress · faithful pivot — user acceptance walk (browser)

## Status — format: browser-walk (agreed standard), last revamped 2026-06-17 on hw158

> **The prior curl/kubectl format is REPLACED.** The previous revision of this runbook proved the cutover almost entirely with `kubectl get cm self-sovereign-cutover-status`, `kubectl get ccnp cutover-egress-block`, `kubectl get secret cutover-complete`, authenticated `ghcr` HEADs, `kubectl logs … grep cutover-resume`, and inline CR-status prose — that command-output format is **banned**. This revision is **100% browser**: every row is a clickable hw158 link, a browser action, and a rendered screen to SEE, with a screenshot evidence path. No curl, no kubectl, no git, no command output anywhere. **Status reset to `☐`** (pending a fresh browser walk on hw158). A login-screen redirect = FAIL; a rendered screen = ✅; `GAP` = no UI surface for that intent (a finding, never a reason to drop to a terminal).

> **THE HEADLINE FINDING (honest, per the agreed rules):** **#3379 has almost NO end-user-UI surface.** The cutover is **triggered by the post-handover backend path** (`ReceiveTofuArchive` seals the tofu archive → the engine auto-fires; the in-cluster trigger is an internal API, not a console button) and its acceptance proofs — the **600s deny-egress hold**, the **registry pivot to local Harbor**, the **durable OpenBao seal of `cutoverComplete`**, the egress-block CCNP, the re-keyed `ghcr-pull` secret — are **backend facts with no console screen**. The live console (`core/console/src`) has **no Sovereignty page, no "Achieve True Sovereignty" CTA, no cutover progress/status card, and no `cutoverComplete` indicator** (verified: `settings.astro` has no Sovereignty section; no cutover/sovereignty component exists anywhere under `core/console/src`). **The ONLY genuine browser surface is `/jobs`**, where the cutover group + its 11 step rows render via the activity bridge. Therefore most rows below are `GAP` — **cutover acceptance is partly operator-CLI / backend, not end-user-UI**, and that gap IS the deliverable's finding.

> **Issue [#3379](https://github.com/openova-io/openova/issues/3379)** · **Slug:** `cutover-durable-true-deny-egress-and-faithful-pivot` · **Pillar:** 5 (Sovereign independence) · ADR-0002 · Principle #11 · DoD §7.
> Absorbs the five faces: **#3667** durable fact (sealed `cutoverComplete`) · **#3671** faithful pivot (`registriesYamlActive=v2`) · **#3678** true deny-egress (600s default-deny hold) · **#3681** audit fidelity (`cutoverStartedAt` once) · **#3695** host gitops loop + zero residual tether.
> **Env: `hw158.omani.works`** (dep `ab2135d4cf2d01e4`); cutover chart `bp-self-sovereign-cutover@0.1.75` (live). All links below point at the live env.
> **Maps to:** [`../UAT.md`](../UAT.md) — the cutover / Sovereign-independence open-list row.
> **Index:** [`README.md`](README.md). Prior-env (hw150-train) and prior-format evidence is void (law §2.2): every row starts unwalked.

**North Star (Pillar 5, founder):** *a franchised Sovereign severs all 8 mothership tethers and keeps serving — proven by a 600s deny-egress hold during which the cluster reconciles green against ONLY its local Gitea + Harbor.*

**The contract:** the operator drives the cutover, watches it progress, and confirms completion **from the browser where a screen exists**. Where the only realisation of an intent is a backend fact (a sealed secret, a CCNP, a containerd pivot, a deny-egress call-home assertion), there is **no page to open** → that row is `GAP` (a finding). A row flips ✅ only when a real browser renders the named screen and a same-day screenshot lands under `docs/sessions/2026-06-17/evidence/`.

**How to read the tables:** every row is ONE browser action. **Tested page** is a clickable link to the live page; **Description** is the action + the screen you must SEE; **Status** is `☐` until a browser walk flips it ✅ (or ❌ on a real defect, or `GAP` where there is no UI); **Evidence** is the screenshot path the walker fills under `docs/sessions/2026-06-17/evidence/` named `3379-<row-id>.png`.

---

## Section 1 — TRIGGER: the operator drives "Achieve True Sovereignty" from a console screen

*The intended surface is a Sovereignty section (Settings → Sovereignty) with an "Achieve True Sovereignty" / cutover CTA the operator clicks to start the 11-step cutover.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/settings](https://console.hw158.omani.works/settings) | Open Settings → look for a **Sovereignty section** with an **"Achieve True Sovereignty"** (cutover) call-to-action. **GAP:** the live console Settings page has **no Sovereignty section and no cutover CTA** — the cutover is fired by the post-handover backend (`ReceiveTofuArchive`), not from a console button. Record the absence as the finding (no UI to trigger the cutover end-user-side). | GAP | `docs/sessions/2026-06-17/evidence/3379-settings-no-sovereignty-section.png` (screenshot the Settings page proving no Sovereignty/cutover surface) |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Sweep the whole console nav (Dashboard, Apps, Jobs, Domains, Team, Billing, Settings) for **any** "Sovereignty" / "cutover" / "Achieve True Sovereignty" entry. **GAP:** none exists anywhere in the live console — there is no end-user surface to initiate or own the cutover. The cutover trigger is operator-CLI / backend-only (a finding). | GAP | `docs/sessions/2026-06-17/evidence/3379-nav-no-sovereignty-entry.png` |

> **Finding (Section 1):** the cutover **trigger has no end-user UI** on hw158 — it is the post-handover internal path. Building a Settings → Sovereignty "Achieve True Sovereignty" CTA (with the progress/status surface in Section 2) is the open UI work this ticket would need to be a browser-walkable deliverable.

---

## Section 2 — STATUS INDICATOR: a cutover progress card renders; `cutoverComplete` shows in the UI

*The intended surface is a progress/status indicator (a card or panel) that renders the running cutover, its current step, and — once done — `cutoverComplete=true` ("Sovereign — tethers severed").*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/settings](https://console.hw158.omani.works/settings) | After triggering, look for a **cutover progress card** showing the live step (e.g. "Step 2 / 11 — harbor-prewarm", a percent bar). **GAP:** no such card exists on hw158 — there is no console rendering of cutover progress. The progress is a backend status ConfigMap with no page. (Finding.) | GAP | `docs/sessions/2026-06-17/evidence/3379-no-progress-card.png` |
| [console.hw158/settings](https://console.hw158.omani.works/settings) | Look for the terminal-state indicator: **`cutoverComplete` → a "Sovereign — tethers severed" badge** (the CTA hidden once complete). **GAP:** `cutoverComplete` is a **durable OpenBao-sealed fact (#3667)** with **no console surface** — the operator cannot see completion in the browser; it is confirmed CLI-side / backend-only. (Finding — the durable-seal proof is intentionally not an end-user screen.) | GAP | `docs/sessions/2026-06-17/evidence/3379-no-cutovercomplete-indicator.png` |

> **Finding (Section 2):** the **progress indicator and the `cutoverComplete` end-state have no end-user UI**. The audit-fidelity contract (#3681 — `cutoverStartedAt` written once, true T0 vs `cutoverLastAttemptStartedAt` on resume) and the durable-seal contract (#3667) are **backend facts**; a Sovereignty card surfacing "Started <true T0> · elapsed · complete" is the open UI work.

---

## Section 3 — THE /jobs CANVAS shows the 11 cutover steps (the one real browser surface)

*This is the genuine end-user surface: the `/jobs` activity canvas ingests the cutover **group** and renders its **11 `cutover-step-*` rows** via the activity bridge — install ≠ execution, honest per-step status.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Open `/jobs` (zero-login, signed in as the owner). The **canvas table renders** — a populated activity list, not a spinner, empty state, or login redirect. | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-canvas-loads.png` |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Find the **`cutover` group** row and expand it → it renders **11 `cutover-step-*` rows**: `gitea-mirror`, `harbor-projects`, `harbor-prewarm`, `registry-pivot`, `catalyst-api-env-patch`, `crossplane-provider-pivot`, `egress-block-test`, `flux-gitrepository-patch`, `gitea-token-mint`, `helmrepository-patches`, `vcluster-registry-pivot` — the 11-step execution tree, not one opaque "install" row. | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-cutover-11-steps.png` |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Read the **cutover group status** while steps are still pending/failing. It reads **`failed` / `running` honestly** from its real step children — **NOT a premature `Succeeded`** (the dormant-install→premature-green confusion stays fixed). On hw158 the cutover wedged at step `harbor-prewarm`, so the group + that step row must read failed, not green. | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-cutover-not-premature.png` |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Scroll to the **`cutover-step-harbor-prewarm`** row specifically → it shows an **honest failed status** (this is the step that blocked on hw158). Open its detail → the failure reason renders (no fabricated green). | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-harbor-prewarm-failed.png` |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | On the **failed** `cutover-step-*` row, a **Re-run button is present** (per-row, gated to Failed). Confirm the control renders on the failing cutover step — the operator can re-drive a failed cutover step from the browser. | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-cutover-rerun-present.png` |

> **Note (Section 3):** `/jobs` is the **only** browser surface that reflects the cutover at all, and only as a **read-model of the 11 steps** (ingestion + honest status + per-row remediation, shared with the [`jobs-one-honest-canvas-…`](jobs-one-honest-canvas-no-fabrication-with-remediation.md) runbook, #3646). It does **not** render the deny-egress hold, the registry pivot result, or `cutoverComplete` — those are Sections 4–5 GAPs.

---

## Section 4 — POST-CUTOVER end-state shown in the UI (after a complete cutover)

*After all 11 steps + the 600s hold + the Ready gate, the intended UI shows the Sovereign as independent: a "tethers severed" state, and the `/jobs` cutover group fully green.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | (After a COMPLETE cutover) the **`cutover` group reads all-11-green** on `/jobs` — every `cutover-step-*` row Succeeded, including `egress-block-test` and `registry-pivot`. On hw158 this is **not reachable**: the cutover is handover-gated and wedged at step `harbor-prewarm`, so the group is not green. Walk this row only on an env where the cutover completed. | ☐ | `docs/sessions/2026-06-17/evidence/3379-jobs-cutover-all-green.png` |
| [console.hw158/settings](https://console.hw158.omani.works/settings) | (After a COMPLETE cutover) Settings shows a steady **"Sovereign — tethers severed"** state with the cutover CTA hidden. **GAP:** no such end-state indicator exists in the live console (same gap as Section 2) — post-cutover independence is a backend fact with no end-user screen. (Finding.) | GAP | `docs/sessions/2026-06-17/evidence/3379-no-post-cutover-sovereign-state.png` |

---

## Section 5 — Backend-only proofs (NO end-user UI — GAP findings, never kubectl)

*The core integrity guarantees of #3379 are realised as backend facts. None has a console screen. They are listed here as explicit `GAP` findings so the absence is recorded — they are NOT to be checked with kubectl/curl in this browser walk.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| (no UI) | **#3678 true deny-egress** — the 600s hold is a default-deny-egress CCNP (`cutover-egress-block`) with an active call-home assertion that FAILS the proof if a mothership host is reachable. **GAP:** a NetworkPolicy + an in-Job assertion — **no console surface**. Acceptance is operator-CLI / backend, not end-user-UI. | GAP | n/a (no UI surface — backend deny-egress proof) |
| (no UI) | **#3671 faithful registry pivot** — `registriesYamlActive=v2` flips node containerd to local Harbor; step-04 counts per-node v2 acks. **GAP:** a containerd `registries.yaml` rewrite on each node — **no console surface**. (Finding.) | GAP | n/a (no UI surface — node containerd pivot) |
| (no UI) | **#3667 durable seal** — `cutoverComplete=true` is sealed in OpenBao (`secret/catalyst/cutover-complete`) so a chart upgrade cannot revert it. **GAP:** a sealed secret — **no console surface** (also Section 2). (Finding.) | GAP | n/a (no UI surface — durable OpenBao seal) |
| (no UI) | **#3681 audit fidelity** — `cutoverStartedAt` written once (true T0); resume advances a separate `cutoverLastAttemptStartedAt`. **GAP:** status-ConfigMap fields — **no console surface** (a Sovereignty "Started <T0> · elapsed" card would surface it; not built). (Finding.) | GAP | n/a (no UI surface — status-CM audit fields) |
| (no UI) | **#3695 zero residual tether** — every external-registry workload re-keyed to local Harbor; the `ghcr-pull` secret re-keyed off `ghcr.io`; no live pod references a mothership registry. **GAP:** pod image refs + a secret key — **no console surface**. (Finding.) | GAP | n/a (no UI surface — workload registry re-key) |

---

## Acceptance summary

- **Section 1 (trigger):** 2 `GAP` rows — the console has **no Sovereignty section / no "Achieve True Sovereignty" CTA**; the cutover is fired by the post-handover backend, not an end-user button.
- **Section 2 (status indicator):** 2 `GAP` rows — **no cutover progress card, no `cutoverComplete` indicator** in the console; progress + completion are backend facts.
- **Section 3 (/jobs canvas):** **5 ☐ browser rows** — the ONE real surface: `/jobs` renders the cutover group + its 11 step rows with honest status and per-row Re-run. These are the only browser-walkable acceptance rows for #3379.
- **Section 4 (post-cutover end-state):** 1 ☐ (`/jobs` all-green, only reachable on a completed-cutover env) + 1 `GAP` (no "tethers severed" Settings indicator).
- **Section 5 (backend proofs):** 5 `GAP` rows — the deny-egress hold, registry pivot, durable seal, audit-fidelity fields, and zero-residual re-key are **backend-only**, no UI.

**TALLY: 6 ☐ browser rows + 10 GAP. Nothing is banked.** Curl/kubectl evidence from the prior format is discarded. The **dominant result is GAP**: **#3379's cutover acceptance is partly operator-CLI / backend, not end-user-UI** — that is the honest finding per the agreed rules. The only browser-walkable acceptance is the `/jobs` cutover-step canvas (Section 3); a row there flips ✅ only when a fresh browser walk renders the screen and the screenshot lands under `docs/sessions/2026-06-17/evidence/` linked from [`../UAT.md`](../UAT.md). A redirect ending on any login screen is ❌. To make the trigger / progress / completion end-user-walkable, the open UI work is a **Settings → Sovereignty surface** (CTA + progress card + `cutoverComplete` badge) — none of which exists in the live console today.

**Index:** [`README.md`](README.md).
