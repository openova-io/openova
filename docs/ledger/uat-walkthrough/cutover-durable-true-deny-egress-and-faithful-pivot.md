# Cutover: durable fact · true deny-egress · faithful pivot — user acceptance walk (browser)

## Status — last validated: hw159.omani.works (2026-06-18) — browser walk: **3 ✅ / 5 ☐ / 8 GAP** (cutover NOT triggered — view-only) — RE-WALKED 2026-06-18 (fresh screenshots, parallel walker #2)

> **hw159 RE-WALK (2026-06-18, walker #2, fresh real screenshots) — view-only, the cutover was deliberately NOT triggered (major destructive op).** CONFIRMS the prior 3✅/5☐/8GAP verdict against the live env (dep `c117f6fd4e2eb2dd`, region `me-east-215-a`, status `ready`). PASS (3): the **Settings → Sovereignty section EXISTS** — the **"Cluster sovereignty"** panel renders at the bottom of `/settings` (reachable via the Settings sidebar **"Sovereignty"** anchor, which highlights when selected) with a **"TETHERED"** badge + an **"Achieve True Sovereignty"** button ("Runs the 8-step cutover. ~10 minutes. Includes a 10-minute egress-block self-test against github.com + ghcr.io + harbor.openova.io") — Section 1 **both rows ✅** (evidence `hw159-3379-sovereignty-section.png`); and the **`/jobs` canvas renders** (full columns NAME·KIND·APP·DEPS·PARENT·STATUS·STARTED·DURATION·ACTIONS, **72** `Install <Blueprint>` lifecycle rows, not a spinner/login) — Section 3 row 1 ✅ (evidence `hw159-3379-jobs-canvas-full.png`). **NOT REACHED (5 ☐):** filtering `/jobs` for **"cutover"** returns **"No jobs match the current filters" (0/72)** — because the cutover was **not fired on this fresh still-tethered env**, the `/jobs` canvas has **no Cutover group / no `cutover-step-*` rows** at all (proof `hw159-3379-jobs-cutover-filter.png`) — so the 11-step tree, the per-step status, and the failed-row Re-run (Section 3 rows 2–5) and the all-11-green end-state (Section 4) are **not reachable view-only**. **GAP (8):** the granular **"Step N/11" progress card** + the terminal **"tethers severed"/`cutoverComplete`** value are not surfaced (Sections 2 + 4 — the badge reads "Tethered"); the 5 **backend-only proofs** (deny-egress hold, registry pivot, durable OpenBao seal, audit-fidelity fields, zero-residual re-key) remain backend facts with no UI (Section 5). **Headline: the cutover TRIGGER (Settings→Sovereignty CTA) is browser-present and the `/jobs` canvas renders; the per-step cutover tree is gated behind firing the cutover, which this view-only walk did not do.**

> **Prior hw158 verdict (2026-06-17): 8 ✅ / 1 ☐ / 7 GAP — superseded by the hw159 view-only walk above. The hw158 8✅ included Section-3's 11-step tree because its cutover was WEDGED (visible); on hw159 the cutover is untriggered so that tree is not present.**

> **The prior curl/kubectl format is REPLACED.** The previous revision of this runbook proved the cutover almost entirely with `kubectl get cm self-sovereign-cutover-status`, `kubectl get ccnp cutover-egress-block`, `kubectl get secret cutover-complete`, authenticated `ghcr` HEADs, `kubectl logs … grep cutover-resume`, and inline CR-status prose — that command-output format is **banned**. This revision is **100% browser**: every row is a clickable hw158 link, a browser action, and a rendered screen to SEE, with a screenshot evidence path. No curl, no kubectl, no git, no command output anywhere. A login-screen redirect = FAIL; a rendered screen = ✅; `GAP` = no UI surface for that intent (a finding, never a reason to drop to a terminal).

> **PRIOR HEADLINE (now SUPERSEDED by the 2026-06-17 re-walk above):** the prior revision asserted "#3379 has almost NO end-user-UI surface … the live console has no Sovereignty page, no Achieve True Sovereignty CTA." The live hw158 walk DISPROVES this: a Settings → Sovereignty section with the "Achieve True Sovereignty" CTA + a Tethered badge IS present, and `/jobs` renders the 11-step cutover tree. The remaining gaps are the **step-progress card** and the **completed `cutoverComplete`/severed value** (not reachable on the wedged env), plus the 5 inherently-backend integrity proofs.

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
| [console.hw159/settings#sovereignty](https://console.hw159.omani.works/settings#sovereignty) | Open Settings → look for a **Sovereignty section** with an **"Achieve True Sovereignty"** (cutover) call-to-action. **PRESENT (live, re-walked 2026-06-18):** the "Cluster sovereignty" panel renders with a **"TETHERED"** badge and an **"Achieve True Sovereignty"** button ("Runs the 8-step cutover. ~10 minutes. Includes a 10-minute egress-block self-test against github.com + ghcr.io + harbor.openova.io"). *(View-only: the CTA was NOT clicked.)* | ✅ | [hw159-3379-sovereignty-section](../../sessions/2026-06-17/evidence/hw159-3379-sovereignty-section.png) |
| [console.hw159/settings#sovereignty](https://console.hw159.omani.works/settings#sovereignty) | Sweep the console nav + Settings sidebar for a **"Sovereignty" / "cutover" / "Achieve True Sovereignty"** entry. **PRESENT:** the Settings left-nav has a dedicated **"Sovereignty"** anchor link (`#sovereignty`) that scrolls to + highlights the Cluster-sovereignty panel — the cutover trigger is a first-class console surface, not backend-only. | ✅ | [hw159-3379-sovereignty-section](../../sessions/2026-06-17/evidence/hw159-3379-sovereignty-section.png) |

> **Finding (Section 1) — UPDATED 2026-06-17:** the cutover trigger **now HAS an end-user UI** on hw158 — a Settings → Sovereignty "Achieve True Sovereignty" CTA + a "Tethered" badge. This OVERTURNS the prior "no UI / backend-only trigger" finding. The remaining open UI work is the **progress/status granularity** in Section 2 (a "Step N/11" card) and the completed-state "tethers severed" rendering.

---

## Section 2 — STATUS INDICATOR: a cutover progress card renders; `cutoverComplete` shows in the UI

*The intended surface is a progress/status indicator (a card or panel) that renders the running cutover, its current step, and — once done — `cutoverComplete=true` ("Sovereign — tethers severed").*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw159/settings#sovereignty](https://console.hw159.omani.works/settings#sovereignty) | Look for a **cutover progress card** showing the live step (e.g. "Step 2 / 11 — harbor-prewarm", a percent bar). **GAP (live hw159):** the Sovereignty section renders a **state badge** ("Tethered") + the "Achieve True Sovereignty" CTA, but **no live per-step progress card** ("Step N/11", percent bar). The per-step status lives on the `/jobs` canvas (Section 3), not a Settings progress card. | GAP | [hw159-3379-sovereignty-cta-tethered](../../sessions/2026-06-17/evidence/hw159-3379-sovereignty-cta-tethered.png) |
| [console.hw159/settings#sovereignty](https://console.hw159.omani.works/settings#sovereignty) | Look for the terminal-state indicator: **`cutoverComplete` → a "Sovereign — tethers severed" badge** (CTA hidden once complete). **GAP (live hw159):** the badge reads **"Tethered"** (cutover never triggered on this fresh env) with the CTA shown; there is no distinct `cutoverComplete` indicator beyond the Tethered/severed badge, and the severed end-state is not observable view-only. The durable-seal proof (#3667) is intentionally backend-only. | GAP | [hw159-3379-sovereignty-cta-tethered](../../sessions/2026-06-17/evidence/hw159-3379-sovereignty-cta-tethered.png) |

> **Finding (Section 2):** the **progress indicator and the `cutoverComplete` end-state have no end-user UI**. The audit-fidelity contract (#3681 — `cutoverStartedAt` written once, true T0 vs `cutoverLastAttemptStartedAt` on resume) and the durable-seal contract (#3667) are **backend facts**; a Sovereignty card surfacing "Started <true T0> · elapsed · complete" is the open UI work.

---

## Section 3 — THE /jobs CANVAS shows the 11 cutover steps (the one real browser surface)

*This is the genuine end-user surface: the `/jobs` activity canvas ingests the cutover **group** and renders its **11 `cutover-step-*` rows** via the activity bridge — install ≠ execution, honest per-step status.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Open `/jobs` (zero-login, signed in as the owner). The **canvas table renders** — a populated activity list, not a spinner, empty state, or login redirect. | ☐ | — |
| — | Find the **`cutover` group** row and expand it → it renders **11 `cutover-step-*` rows**: `gitea-mirror`, `harbor-projects`, `harbor-prewarm`, `registry-pivot`, `catalyst-api-env-patch`, `crossplane-provider-pivot`, `egress-block-test`, `flux-gitrepository-patch`, `gitea-token-mint`, `helmrepository-patches`, `vcluster-registry-pivot` — the 11-step execution tree, not one opaque "install" row. | ☐ | — |
| — | — | ☐ | — |
| — | — | ☐ | — |
| — | On the **failed** `cutover-step-*` row, a **Re-run button is present** (per-row, gated to Failed). Confirm the control renders on the failing cutover step — the operator can re-drive a failed cutover step from the browser. | ☐ | — |

> **Note (Section 3):** `/jobs` is the **only** browser surface that reflects the cutover at all, and only as a **read-model of the 11 steps** (ingestion + honest status + per-row remediation, shared with the [`jobs-one-honest-canvas-…`](jobs-one-honest-canvas-no-fabrication-with-remediation.md) runbook, #3646). It does **not** render the deny-egress hold, the registry pivot result, or `cutoverComplete` — those are Sections 4–5 GAPs.

---

## Section 4 — POST-CUTOVER end-state shown in the UI (after a complete cutover)

*After all 11 steps + the 600s hold + the Ready gate, the intended UI shows the Sovereign as independent: a "tethers severed" state, and the `/jobs` cutover group fully green.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | (After a COMPLETE cutover) the **`cutover` group reads all-11-green** on `/jobs` — every `cutover-step-*` row Succeeded, including `egress-block-test` and `registry-pivot`. On hw158 this is **not reachable**: the cutover is handover-gated and wedged at step `harbor-prewarm`, so the group is not green. Walk this row only on an env where the cutover completed. | ☐ | Re-walked hw158 2026-06-17 — **confirmed NOT reachable on this env.** `/jobs` directly shows `Harbor Prewarm` = **Failed** and `Egress Block Test` / `Registry Pivot` / `Vcluster Registry Pivot` = **Pending** — the cutover group is NOT all-green (wedged at harbor-prewarm). All-green requires a completed-cutover env. (Evidence: the Section-3 `/jobs` captures show the wedged step tree.) `docs/sessions/2026-06-17/evidence/3379-jobs-cutover-all-green.png` |
| [console.hw158/settings](https://console.hw158.omani.works/settings) | (After a COMPLETE cutover) Settings shows a steady **"Sovereign — tethers severed"** state with the cutover CTA hidden. **GAP:** no such end-state indicator exists in the live console (same gap as Section 2) — post-cutover independence is a backend fact with no end-user screen. (Finding.) | GAP | Re-walked hw158 2026-06-17. The Settings → Sovereignty section DOES exist (badge + CTA), but on this env the badge reads **"Tethered"** with the CTA shown — the cutover is wedged at harbor-prewarm, so the **post-cutover "Sovereign — tethers severed" + CTA-hidden** end-state is **not reachable** here. The end-state screen is GAP on hw158 (needs a completed-cutover env to observe the severed value). ![3379-no-post-cutover-sovereign-state](../../sessions/2026-06-17/evidence/3379-no-post-cutover-sovereign-state.png) |

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

- **Section 1 (trigger):** **2 ✅ rows (re-walk 2026-06-17 — OVERTURNS the prior GAP).** The Settings → Sovereignty section EXISTS with an **"Achieve True Sovereignty"** CTA + a "Tethered" badge, reachable from the Settings sidebar "Sovereignty" nav link. The end-user cutover trigger is browser-present.
- **Section 2 (status indicator):** 2 `GAP` rows — the Sovereignty section renders a **state badge** ("Tethered"), but a granular **"Step N/11" progress card** and a distinct `cutoverComplete` indicator are not surfaced. (Status-badge surface exists; step-progress + completed value GAP on this wedged env.)
- **Section 3 (/jobs canvas):** **5 ✅ rows (re-walk 2026-06-17).** `/jobs` renders the cutover group + its 11 `Cutover` step rows with honest status (`Harbor Prewarm` Failed, downstream Pending, no premature green) and per-row Retry/Re-run on the failed step.
- **Section 4 (post-cutover end-state):** 1 ☐ (`/jobs` all-green — **confirmed NOT reachable**, cutover wedged at harbor-prewarm) + 1 `GAP` (the "tethers severed" value isn't observable on this env — the badge reads "Tethered").
- **Section 5 (backend proofs):** 5 `GAP` rows — the deny-egress hold, registry pivot, durable seal, audit-fidelity fields, and zero-residual re-key are **backend-only**, no UI.

**TALLY (re-walk hw158 2026-06-17): 8 ✅ · 1 ☐ · 7 GAP.** The prior "6 ☐ + 10 GAP / dominant GAP / almost no UI" verdict is **superseded** — the cutover **trigger (Settings → Sovereignty CTA)** and the **per-step `/jobs` canvas** are now genuinely browser-walkable and pass on hw158. The remaining gaps are the **step-progress card** + the **completed `cutoverComplete`/"tethers severed" value** (not reachable on the harbor-prewarm-wedged env) and the 5 inherently-backend integrity proofs (Section 5). A redirect ending on any login screen is ❌. Open UI work to close the remaining gaps: a step-progress card + a completed-state "Sovereign — tethers severed" rendering in the existing Sovereignty section.

**Index:** [`README.md`](README.md).
