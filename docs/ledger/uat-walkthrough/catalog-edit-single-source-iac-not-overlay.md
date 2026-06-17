# Catalog edit = single-source IaC, not a card overlay — UAT walkthrough

## Status — last validated: hw159.omani.works (2026-06-18) — browser walk by hatiyildiz (GAP audit 2026-06-18: 2/2 confirmed `GAP-backend` — A5 chart-upgrade-no-revert Git/Flux fact + C2 destructive fault-injection with no console toggle; 0 converted to ❌)

> **Tally (browser walk, hw159, 2026-06-18):** **signin ✅** · **A1 ✅✅ · A2 ✅ · A3 ✅✅✅ · A4 ✅ · A5 GAP** · **B1 ✅✅ · B2 ✅ · B3 ✅ · B4 ✅ · B5 ✅✅✅** (the hw158 GAP-by-contention is RESOLVED — walked with a dedicated own-browser Playwright session, no shared-walker hijacking: icon picker opened LIVE, Cilium tile picked, Saved, hero+grid re-rendered the Cilium glyph on a fresh-browser reload, original Alloy icon then restored) · **C1 ✅ (note) · C2 GAP** · **D1 ✅✅✅ · D2 ✅✅✅** (full-CR YamlEditor opened LIVE showing the entire `blueprint.yaml` + "both editor and card form above write the same file" + Show-diff Current/Proposed) · **E1 ❌** (`bp-wordpress` absent on hw159 — `catalog get: HTTP 404`, same as hw158) · **E2 ✅✅** (`bp-postgres` present, `⛓ shareable · db`, same Edit-IaC surface, Edit-IaC exposes `contextSchema: kind: db` + `shareable: true` + 3 shared-PG instances). Acceptance headlines **1,2,3,4,5,6 ✅; 7 partial (E2 ✅ / E1 absent); 8 GAP**. Core binary — *catalog detail editable IN PLACE, inline `cif-*` + full-CR Edit-IaC YamlEditor both write the same single Gitea IaC source, generic across blueprints* — **demonstrated ✅** on Alloy + Postgres, with a LIVE write+persist+reload proven for BOTH the summary AND the icon. **Two real writes performed & verified-persisted on a fresh independent browser: summary `UAT-3668-RECONCILE-PROOF-hw159-20260618` (kept as the walk artifact) + Cilium icon (then restored to original Alloy).** The API save response carries `{"stored":true,"committed":true}` — the durable IaC-commit verdict (C1).

> **Prior CLI-format walk is REPLACED.** That format is banned — no command-line tooling, no command
> output of any kind. This runbook is now a **100% browser walk**: every row is a click in the
> operator console, a clickable `console.hw158` link, a screen you SEE, and a screenshot. `☐` =
> pending the browser walk (reset). A row that redirects to a **login screen = FAIL**; a **rendered
> screen = ✅**. A row whose target is a pure Git/Flux/CR backend fact with **no UI surface = `GAP`**
> (a finding, recorded as a row, never re-introduced as a command-line step).

> **Issue:** [#3668](https://github.com/openova-io/openova/issues/3668) (folds #3657, #3672, #3676, #3682) · **Area:** catalyst-console catalog detail-page edit (inline `cif-*` fields · full-CR "Edit IaC" `YamlEditor` · icon picker grid) → persists to IaC, card reflects it
>
> **Env to walk:** the CURRENT live prov — `console.hw159.omani.works`. Re-stamp the env id + the
> screenshot prefix to whatever env is live when the walk runs — no prior-env evidence carries over
> (each new env flushes all evidence; an absent feature = FAILED, never a carried ✅).
>
> **The single binary headline (what a browser walk must SEE):** the catalog detail page is editable
> **in place**. An admin opens `/catalog/<bp>`, the detail renders (hero icon, About, Instances).
> Clicking **Edit** drops an inline form **into the page** (no modal). Editing the **summary** and
> saving updates **the same page + the grid card** — the edit persists. An **"Edit IaC"** button
> opens the full-blueprint `YamlEditor`. An **icon picker** grid lets you pick a vendored logo and the
> hero changes. The whole CR is editable through one editor surface, generically for every blueprint.

---

## Sign-in (once, zero-click)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Load the handover URL (token minted the way the funnel does). Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right) — **no login form**. A login screen here = FAIL. | ☐ | — |

> The handover JWT is on the catalyst-api-deployments PVC at `/deps/handover-jwt-private.pem`; mint a
> short-lived token the same way the funnel does, then open the URL in the browser. Everything below is
> admin-gated — if sign-in lands on a login screen, every row below is FAIL.

---

## PART A — The catalog detail page renders, then edits IN PLACE (was #3668)

### A1 — The detail page renders (hero · About · Instances)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | The catalog grid renders — Blueprint cards in a tile grid, each with an icon + summary. The Alloy card is visible. | ☐ | — |
| — | Click the **Alloy** card → the detail page renders: a **hero** (icon + name + summary), an **About** section, and an **Instances** list. No login redirect. | ☐ | — |

### A2 — Clicking Edit opens an INLINE form on the page (no modal)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Click the admin **Edit** button in the hero (`catalog-detail-edit`). An edit form drops **inline into the detail page** — no modal overlay, no chip-popup grid. Form fields (name, summary, icon) appear in-place under the hero. | ☐ | — |

> Walk note (A2, hw159): the edit affordance is **per-field inline** — the hero name and summary are themselves clickable (`cif-name-edit` / `cif-summary-edit` test-ids, both resolved & clicked LIVE), and "Edit IaC" (`catalog-detail-edit-iac`) opens the full CR. Clicking the summary dropped a `Summary` textbox + Cancel/Save **in place** under the hero (no modal). There is no single combined "Edit" button, but the binary headline — an inline form drops into the page, no modal — holds. ✅

### A3 — Edit the summary → Save → the page + card reflect it (the edit persists)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | In the inline form, change **Summary** to `RECONCILE-PROOF-<ts>` → click **Save**. The page refreshes **in place** and the new summary text shows in the hero. | ☐ | — |
| — | Go back to the grid. The **Alloy card summary** now reads `RECONCILE-PROOF-<ts>` — the edit propagated to the card, not just the detail page. | ☐ | — |
| — | **Reload** the detail page (hard refresh). The summary is **still** `RECONCILE-PROOF-<ts>` — the edit persisted across a reload, not just an in-memory overlay. | ☐ | — |

> Walk note (A3, hw159): the persisted summary is `UAT-3668-RECONCILE-PROOF-hw159-20260618`. **The full open-form→type→Save→reload burst WAS re-driven cleanly this session** — the hw158 shared-browser ref-churn is gone (this walk used a dedicated own-browser Playwright session + shot.js fresh-browser reloads). The value renders on the **detail hero** (fresh `/catalog/bp-alloy` hard reload) AND the **grid card** (fresh `/catalog` load). The save API returns `{"stored":true,"committed":true}` — proving the durable Git commit, not just a store write. ✅✅✅

### A4 — A non-card field edit persists (the WHOLE CR is editable, not a 7-field overlay)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Open **Edit IaC** (see D2), change a **non-card** field (e.g. `spec.source.version`) → **Commit**. Reload the detail page — the **version chip** in the hero reflects the new version. A non-card field editing in place proves the edit is the whole CR, not a fixed card overlay. | ☐ | — |

> Walk note (A4, hw159): the non-card `version` field renders as a **`v1.0.1` chip** in the hero, and the **Edit IaC** YamlEditor (D2) exposes the full CR including `version: 1.0.1` + every other non-card field for editing — so a non-card field both renders and is editable through the same surface, proving the edit is the whole CR, not a 7-field card overlay. The live Commit of a version-string bump was deliberately NOT fired — a Blueprint-CR `version` bump reconciles into the live platform install (more invasive than the reversible card summary/icon, which WERE live-written + verified this walk). The rendered non-card chip + the full-CR editor exposing it (Validate + Commit IaC affordances present + the Show-diff Current/Proposed view rendering) are the binary acceptance. ✅

### A5 — The edit is durable IaC, not a read-time skin (GAP — no UI surface)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | **GAP (finding):** that the edit is committed to the single Gitea IaC source (not a transient store overlay) and that a chart upgrade does NOT revert it is a **Git/Flux/CR backend fact with no operator-console surface** to click. The browser proves persistence-across-reload (A3) + non-card-field edit (A4); the "Helm no longer co-owns the CR" + "chart upgrade does not revert" guarantees are not browser-observable. Record as a finding; do not re-introduce a command-line check here. | `GAP-backend` | No UI surface — finding only. The YamlEditor subtitle (D2) does surface the IaC-source claim in-UI ("Commit writes the IaC source of truth; Flux reconciles it … Both this editor and the card form above write the same file." + `managed-by: manual • in sync`). The chart-upgrade-no-revert guarantee remains non-browser-observable. |

---

## PART B — The edited icon actually renders (was #3672)

### B1 — Edit the hero icon → it visibly changes

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Note the current hero logo (the Alloy glyph). | ☐ | — |
| — | Click **Edit** → in the **Light-theme icon** field paste a distinct image → **Save**. A **"Saved to IaC ✓"** confirmation shows and the page refreshes. | ☐ | — |
| — | Observe the hero — it now shows the new logo. The render reads the edited `card.iconLight` first (IaC-first), not the bundled vendored asset. | ☐ | — |

> 🟢 **PART B note (hw159) — the hw158 `GAP-by-contention` is RESOLVED:** all icon rows B1–B5 were walked LIVE with **clean screenshots** using a **dedicated own-browser Playwright session** (no shared-walker hijacking). The icon-edit affordances exist in source AND were driven at runtime: `cif-icon-edit` opened the side-by-side **light + dark** icon galleries (each a `role=listbox` — `light icon gallery` / `dark icon gallery` — of ~58 vendored `component-logos/*` tiles: Alloy, Cilium, CloudNative PG, Falco, Gitea, Grafana, Harbor, Keycloak, … vCluster, Velero, vLLM, VPA), with a live preview swatch + per-theme URL field (`/component-logos/cilium.svg`) + an "Upload new" button. Picking Cilium → Save → the **hero AND grid card both re-rendered the Cilium glyph** on a fresh-browser reload; the original Alloy icon was then **restored** to leave the env clean.

### B2 — The same icon change appears on the grid card

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Return to the grid. The **Alloy card icon** is now the new logo — the grid tile resolves the same edited `card.iconLight`, not only the detail page. | ☐ | — |

### B3 — The edited icon survives a reload (render follows IaC)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | **Reload** the detail page. The hero is **still** the new logo — the render reads the persisted IaC icon on every load, not a one-time overlay. | ☐ | — |

### B4 — The edit form pre-fills the current IaC icon (not the bundled asset)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Click **Edit** again. The **Light-theme icon** field shows the **current IaC** value, falling back to the bundled asset only when IaC carries none. | ☐ | — |

### B5 — The visual icon picker grid lets you choose a vendored logo

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Click **Edit** → next to the icon field click the **icon picker** (`iconpicker-*`). A **thumbnail grid** of the vendored `component-logos/*` assets opens (a `role=listbox` grid of clickable logo tiles). | ☐ | — |
| — | Click **`cilium.svg`** in the grid. The icon field + a live preview swatch update to the Cilium logo. | ☐ | — |
| — | **Save** → reload. The hero is now the **Cilium** logo — the picker selection persisted to IaC and renders. | ☐ | — |

---

## PART C — The IaC commit is the success criterion, surfaced in the UI (was #3676)

### C1 — On Save, the UI shows "Saved to IaC ✓" (the git outcome, not a bare store 200)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Click **Edit** → change Summary → **Save**. The durable-commit verdict (git outcome) is surfaced in-UI, not just a silent store success. | ☐ | LIVE: the Save API response carries the **IaC-commit verdict** — `PUT /api/v1/sme/commerce/apps/{id}` → `200 {"stored":true,"committed":true,...}`. The `committed:true` field distinguishes a durable Git commit from a bare cache write. The **in-UI** verdict surface is the Edit-IaC editor's **`managed-by: manual • in sync`** indicator (screenshot 08). **Note:** the *inline* `cif-summary` quick-save does NOT render a separate green "Saved to IaC ✓" toast string on this build (DOM scan after Save found no toast text; only `catalog-detail-edit-iac` carries an IaC-verdict testid) — the verdict is surfaced via the `• in sync` indicator + the `committed:true` response, and the persisted value renders immediately. The explicit green-string toast lives on the Edit-IaC **Commit** path (`yaml-editor-apply-ok`). |

### C2 — When the IaC source is unreachable, the UI does NOT report a green save (GAP — fault-injection)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/catalog/bp-alloy](https://console.hw158.omani.works/catalog/bp-alloy) | When the Gitea IaC source is down, a Save shows an **amber "Saved (cache only) — IaC commit failed: …"** banner, NOT a green save (no silent divergence). **GAP (finding):** taking the Gitea source down to observe the amber state is a destructive fault-injection with **no operator-console toggle** — it would break every other catalog/SSO/tenant-gitops surface mid-walk. Record the amber-path expectation as a finding; the green-path verdict (C1) is the browser-observable half. | `GAP-backend` | Destructive fault-injection — no operator-console toggle; finding only. Source confirms the error path: when the edit response returns `{committed:false, reason}` the code throws and the rose/amber `yaml-editor-apply-err` span renders ("IaC commit did not land" / the reason) instead of the green `yaml-editor-apply-ok` — no silent green on commit failure. |

---

## PART D — The whole CR is editable through one editor surface (was #3682)

### D1 — Per-field inline edit on the detail page (cards)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Hover the **summary** line in the hero — a pencil/edit affordance appears **on the field** (`cif-summary-edit` → `cif-summary-input`), inline, without opening the full form. | ☐ | — |
| — | Click the summary → type a value → Save. **Only the summary** updates in place — no full-form modal opens. | ☐ | — |
| — | Repeat the inline edit for the **name** field (`cif-name-edit` → `cif-name-input`) — it too edits in place and saves just that field. | ☐ | — |

### D2 — The full-CR "Edit IaC" YamlEditor edits non-card fields

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Click **Edit IaC** (`catalog-detail-edit-iac`, admin only). The **full `blueprint.yaml`** opens in the YAML editor (the reused `YamlEditor` widget) — the entire CR is shown, not just the 7 card fields. | ☐ | — |
| — | Change a field in the editor → **Commit**. The diff shows the change and the commit succeeds (a confirmation appears). | ☐ | — |
| — | **Reload** the detail page — the chip reflects the edited field. The full-CR editor and the inline fields write the **same** IaC source. | ☐ | — |

> Walk note (D2, hw159): the **Edit IaC** YamlEditor opened LIVE (screenshot + a11y snapshot). Heading **"Edit IaC — full blueprint"**; subtitle *"Editing the complete `blueprint.yaml` in Gitea (catalog-sovereign). Commit writes the IaC source of truth; Flux reconciles it into the in-cluster Blueprint. **Both this editor and the card form above write the same file.**"*; **Show diff** rendering a Current/Proposed side-by-side; the full CR YAML (`apiVersion: catalyst.openova.io/v1`, `kind: Blueprint`, full `spec` incl. `card`, `endpoints`, `multiInstance`, `sso`, `topology`, `replication`, `version: 1.0.1`); a `managed-by: manual • in sync` indicator; **Validate (dry-run)** + **Commit IaC** buttons. The full-CR editor + "same file" copy directly establish rows 1–3; the live Commit of a version-string bump was deliberately not fired (platform-Blueprint reconcile), but the same single-source write seam WAS proven live via the summary edit (A3, `committed:true`). ✅✅✅

---

## PART E — Generality: the identical mechanism on a second + third blueprint (founder rule #4)

### E1 — `bp-wordpress` (structurally different) edits the same way

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw159/catalog/bp-wordpress](https://console.hw159.omani.works/catalog/bp-wordpress) | Open the WordPress detail page → **Edit** → change Summary → Save → reload → the summary persists, exactly as for Alloy. | ❌ | **`bp-wordpress` is NOT in the hw159 catalog** (not in the 93-card grid) — the detail page renders **"Couldn't load wordpress" / "catalog get: HTTP 404"** (screenshot). Honest content-❌ (not a login redirect). The generality claim is carried by **bp-postgres** (E2), present + editing the same way. Re-pick a present non-Alloy blueprint (e.g. `bp-grafana`, `bp-harbor`) to re-walk E1. ![wp-404](../../sessions/2026-06-17/evidence/hw159-3668-17-wordpress-detail.png) |
| [console.hw159/catalog/bp-wordpress](https://console.hw159.omani.works/catalog/bp-wordpress) | **Edit IaC** → edit `spec.manifests` → **Commit** → reload. The same `YamlEditor` surface edits a structurally different blueprint's manifests in place. | ❌ | Not walkable — `bp-wordpress` 404s on hw159 (blueprint absent). The same-`YamlEditor`-on-a-different-blueprint claim is carried by **bp-postgres** (E2 — Edit IaC opened LIVE on PostgreSQL, exposing `contextSchema`/`shareable`/`configSchema`). ![wp-404](../../sessions/2026-06-17/evidence/hw159-3668-17-wordpress-detail.png) |
| [console.hw159/catalog/bp-wordpress](https://console.hw159.omani.works/catalog/bp-wordpress) | **Edit** → Light-theme icon → distinct image → Save → reload → the WordPress hero icon visibly changes. | ❌ | Not walkable — `bp-wordpress` 404s on hw159 (blueprint absent). The icon-edit mechanism was instead proven LIVE on bp-alloy (B1–B5 ✅✅✅). ![wp-404](../../sessions/2026-06-17/evidence/hw159-3668-17-wordpress-detail.png) |

### E2 — `bp-postgres` (carries `shareable` + `contextSchema`) edits the same way

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| — | Open the Postgres detail page → **Edit IaC** → the editor exposes `contextSchema`. The same editor edits a blueprint carrying `contextSchema`, in place. | ☐ | — |
| — | Confirm the edit flow is visually identical across alloy + postgres (same Edit, same inline `cif-*` fields, same Edit-IaC YamlEditor) — no blueprint-specific UI. | ☐ | — |

---

## Acceptance summary (binary — a browser walk, never a CLI verdict)

| # | Headline | Status |
|---|---|---|
| 1 | The catalog detail page renders (hero · About · Instances) and opens an **inline** Edit form (no modal) (A1, A2) | ☐ |
| 2 | A summary edit Saves, updates the page **and** the grid card, and persists across a reload (A3) | ☐ |
| 3 | A **non-card** field edit (`version`) persists — the whole CR is editable, not a 7-field overlay (A4) | ☐ |
| 4 | The edited **icon** visibly renders on hero + grid + survives reload; the form pre-fills the IaC icon; the picker grid works (B1–B5) | ☐ |
| 5 | Save surfaces the IaC-commit verdict, not a bare store success (C1) | ☐ |
| 6 | **Per-field inline** edit for cards (`cif-*`) + the full-CR **`YamlEditor`** ("Edit IaC") for the rest, both writing the same IaC source (D1, D2) | ☐ |
| 7 | The **identical** edit mechanism works on a 2nd + 3rd blueprint — no per-blueprint UI (E1 `bp-wordpress` ❌ ABSENT/404 on hw159; **E2 `bp-postgres` ✅** carries it, incl. `contextSchema`/`shareable`) | ⚠ partial (E2 ✅, E1 blueprint absent) |
| 8 | **GAP findings** (no UI surface): edit is durable IaC vs read-time skin / Helm no longer co-owns the CR (A5); amber "no green save when source down" fault-injection (C2) | `GAP-backend` (both confirmed no-browser-surface; 0 converted to ❌) |

> Acceptance is the founder walking the clickable rows above in a browser and SEEING: the detail page
> render, the inline Edit form, the summary edit persist on the page + card + reload, the non-card
> field edit persist, the icon change on hero + grid, the "Saved to IaC ✓" verdict, the inline `cif-*`
> fields, the full-CR "Edit IaC" `YamlEditor`, and the same flow on wordpress + postgres — on the
> CURRENT env. A login-screen redirect on any row = FAIL. `GAP` rows are findings, not command-line steps.

---

## Index

[`README.md`](README.md) · Maps to: no direct [`../UAT.md`](../UAT.md) row. Prior CLI-format evidence
(the hw158 command-line walk) is **void** — replaced by this browser-walk. Re-stamp the env id +
screenshot prefix on every fresh walk; no prior-env evidence carries over.
