# Catalog edit = single-source IaC, not a card overlay — UAT walkthrough

> **Issue:** [#3668](https://github.com/openova-io/openova/issues/3668) (folds #3657, #3672, #3676, #3682) · **Area:** catalyst-console catalog-edit IaC / Gitea / Flux GitRepository / Blueprint CR
>
> **Env to walk:** the CURRENT live prov (today: `console.hw150.omantel.biz`, deployment
> `catalyst-hw150-omantel-biz-1290a8ef`, kubeconfig `/tmp/hw150.kubeconfig`). Re-stamp the env id +
> screenshot prefix to whatever env is live when the walk runs — **no prior-env evidence is carried
> over** (each new env flushes all evidence; an absent feature = FAILED, never a carried ✅).
>
> **The single binary headline:** after this ticket lands, the catalog is a **thin two-way skin over
> ONE IaC source of truth in Gitea**. A console edit (or an out-of-band `git push`) to
> `catalog-sovereign/<bp>/blueprint.yaml` reconciles **through Flux into the in-cluster `Blueprint`
> CR** — so the SAME bytes drive render + install. The **edited icon visibly changes**, the **git
> commit is the success criterion** (never a swallowed best-effort write), and the **whole CR is
> editable** (per-field inline for cards + the full-CR `YamlEditor` for the rest), generically for
> every blueprint.
>
> **Format law:** UI rows + git/kubectl verification (the commit + the CR moving IS the acceptance).
> Replace `<fqdn>`/`<JWT>`/`<env>`. Tick **☑** pass / **☒** fail. The appendix lists automated checks
> — those are NOT acceptance.

---

## Sign-in (once, zero-click)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/auth/handover?token=<handover-JWT>` | nothing — just load it | Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right), no login form | ☐ |

> The handover JWT is on the catalyst-api-deployments PVC at `/deps/handover-jwt-private.pem`; mint a
> short-lived token the same way the funnel does. Everything below is admin-gated.

---

## PART A — The render source IS the Gitea IaC, reconciled by Flux (spine; was #3668)

### A1 — A Flux source reconciles `catalog-sovereign` → Blueprint CRs

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get gitrepository,kustomization -A | grep catalog-sovereign` | a **READY=True** Flux resource sourcing the `catalog-sovereign` Gitea Org (today: **empty** — FAIL) | ☐ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig describe kustomization <the-catalog-one> -n flux-system` | `Ready=True`, last applied revision = the `catalog-sovereign` head; events show `Blueprint` CRs applied | ☐ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get blueprint bp-alloy -o jsonpath='{.metadata.labels}'` | the CR is **no longer** `app.kubernetes.io/managed-by: Helm` + `catalyst.openova.io/managed-by: catalog-seed`; it is reconciled by the Flux source (today: Helm/seed-owned — FAIL) | ☐ |

### A2 — A console edit reaches the in-cluster CR (not just a read-time overlay)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | click the admin **Edit** button in the hero (`catalog-detail-edit`) | the edit form opens **inline on the page** (no modal, no grid chip-popup) | ☐ |
| same | change Summary → `RECONCILE-PROOF-<ts>` → **Save** | page refreshes in place; new summary shown | ☐ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'` | shows `RECONCILE-PROOF-<ts>` within one reconcile interval — **the CR moved** (today: empty; the CR never gets the edit — FAIL) | ☐ |
| terminal | `kubectl ... get blueprint bp-alloy -o jsonpath='{.spec.card.description}'` | is consistent with the edit (today: the ORIGINAL seed text "Grafana Alloy — telemetry collector…", diverged from Gitea — FAIL) | ☐ |

### A3 — The committed file is a FULL CR, not a `version: 0.0.0` card stub

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea web (`https://gitea.<location>.<fqdn>` or the in-cluster `gitea-http`) → `catalog-sovereign/bp-alloy` | open `blueprint.yaml` history → the latest commit | the commit carries `RECONCILE-PROOF-<ts>` AND a **full CR** — real `spec.version` (e.g. `1.0.1`, not `0.0.0`), `spec.source`, `spec.manifests`, `spec.placementSchema`, `spec.sso` all present (today: a card-only stub with `version: 0.0.0` — FAIL) | ☐ |

### A4 — A non-card field round-trips (out-of-band git → CR → UI)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea → `catalog-sovereign/bp-wordpress/blueprint.yaml` | hand-edit `spec.source.version` to a distinct value → commit | — | ☐ |
| terminal (after reconcile) | `kubectl ... get blueprint bp-wordpress -o jsonpath='{.spec.source.version}'` | matches the hand-edited value (a **non-card** field — unreachable by the 7-field overlay) | ☐ |
| `/catalog/bp-wordpress` | reload | the version chip reflects the hand-edited version — render follows IaC, not a build-time/seed value | ☐ |

### A5 — Helm no longer owns the catalog CR (a chart upgrade does not revert the edit)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | record `kubectl ... get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'` (= `RECONCILE-PROOF-<ts>`) | the edited value | ☐ |
| terminal | trigger a reconcile/upgrade of `bp-catalyst-platform` (e.g. `flux reconcile hr bp-catalyst-platform -n flux-system --with-source`) | upgrade completes | ☐ |
| terminal | re-read the same jsonpath | STILL `RECONCILE-PROOF-<ts>` — the edit is **not reverted** by the chart (today: a `helm upgrade` re-renders the seed CR over the edit — FAIL) | ☐ |

---

## PART B — The edited icon actually renders (was #3672)

### B1 — Edit the hero icon to a distinct image → it visibly changes

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | note the hero logo | the Alloy glyph (today: the **bundled** asset, regardless of IaC) | ☐ |
| same → **Edit** → Light-theme icon field | paste a distinct image, e.g. `data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==` (1×1 red dot) → **Save** | "Saved to IaC ✓", page refreshes | ☐ |
| same | observe the hero | the hero shows the **red dot** (today: still the Alloy glyph — FAIL, the render reads `findComponent(name).logoUrl`, never `card.iconLight`) | ☐ |

### B2 — The same change appears on the grid card

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog` | find the Alloy card in the grid | the card icon is the **red dot** (the grid card resolves from the catalog API `card.iconLight`, not only the commerce-store overlay) | ☐ |

### B3 — Out-of-band icon edit in Gitea changes the rendered hero

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` | set `spec.card.iconLight` to a different distinct image → commit | — | ☐ |
| `/catalog/bp-alloy` | reload (after reconcile/read) | the hero shows the **git-side** image — render follows IaC, not the console bundle | ☐ |

### B4 — The edit form pre-fills the current IaC icon (not the bundled asset)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` (already edited above) → **Edit** | look at the Light-theme icon field | it shows the **current IaC** value (the red dot / git-side URI), not the bundled `/component-logos/alloy.svg` (today: pre-fills the build-time `logoUrl`, dark always blank — FAIL) | ☐ |

### B5 — The visual picker lets you choose a vendored logo

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → next to the icon field | click **Choose** | a thumbnail grid of the 46 vendored `public/component-logos/*` assets opens (today: text field + blind Upload only — no grid) | ☐ |
| same | click `cilium.svg` | the field + a live preview swatch update to `cilium.svg` | ☐ |
| same | **Save** → reload | the hero is the **Cilium** logo | ☐ |

---

## PART C — The IaC commit is the success criterion (write-budget; was #3676)

### C1 — With Gitea reachable, the commit succeeds under its own budget

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → Summary `BUDGET-PROOF-<ts>` → **Save** | save | UI shows **"Saved to IaC ✓"** (the git outcome is surfaced, not the store's 200) | ☐ |
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` log | view the latest commit | carries `BUDGET-PROOF-<ts>` | ☐ |

### C2 — With Gitea DOWN, the UI does NOT report a green save (no silent divergence)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig scale deploy/gitea -n gitea --replicas=0` | gitea Pod terminates | ☐ |
| `/catalog/bp-alloy` → **Edit** → Summary `OFFLINE-<ts>` → **Save** | save | **amber** "Saved (cache only) — IaC commit failed: …", NOT a green "Saved" (today: green "Saved" while Gitea got nothing — FAIL) | ☐ |
| terminal | `kubectl ... scale deploy/gitea -n gitea --replicas=1`; wait Ready | gitea up | ☐ |
| `/catalog/bp-alloy` | follow the UI's retry instruction (or observe the durable retry) | `OFFLINE-<ts>` is now committed to Gitea (git log shows it) — the source + cache reconverge, no permanent divergence | ☐ |

> The slow-Gitea path (a `PutFile` that takes 3s) is exercised by the unit test in the appendix —
> under the old shared 1500ms probe budget it silently no-ops; under the dedicated ~15s
> `catalogEditGitBudget` it commits.

---

## PART D — The whole CR is editable, one editor (editor surface; was #3682)

### D1 — Per-field inline edit on the detail page (cards)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | hover the summary line | a pencil/edit affordance appears **on the field** | ☐ |
| same | click the summary → type `INLINE-<ts>` → Save | **only the summary** updates in place — no full-form modal (today: clicking the text does nothing; you must use the global **Edit** button — FAIL) | ☐ |
| same | repeat for **category**, **docs**, and the **topology list** | each edits in place and saves just that field — proving the card surface widened beyond the original 7 fields (category/docs/license/family/tags were uneditable before) | ☐ |

### D2 — The full-CR IaC editor (the reused `YamlEditor`) edits non-card fields

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | click **Edit IaC** (admin only) | the full `blueprint.yaml` opens in the YAML editor with a **side-by-side diff** + schema validation (today: no such action — FAIL) | ☐ |
| same | change `spec.source.version` → **Commit** | the diff shows the change; commit succeeds | ☐ |
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` log | view the latest commit | carries the new `spec.source.version` — a **non-card** field the old 7-field form could never touch | ☐ |
| `/catalog/bp-alloy` | reload | the version chip reflects the edited version | ☐ |
| terminal | `git grep -n "widgets/cloud-list/YamlEditor" products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx` | the catalog page imports the **reused** `YamlEditor`, not a new bespoke full-CR editor — one mechanism | ☐ |

---

## PART E — Generality: identical mechanism on a second + third blueprint (founder rule #4)

### E1 — `bp-wordpress` (structurally different, edit its `manifests`)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-wordpress` → **Edit** → Summary `GEN-WP-<ts>` → Save | save | "Saved to IaC ✓"; `kubectl ... get blueprint bp-wordpress -o jsonpath='{.spec.card.summary}'` = `GEN-WP-<ts>` after reconcile | ☐ |
| same → **Edit IaC** | edit `spec.manifests` → Commit | lands in `catalog-sovereign/bp-wordpress/blueprint.yaml` (git log) via the SAME `YamlEditor`/`edit-pr` path | ☐ |
| same → **Edit** → Light-theme icon → distinct image → Save → reload | observe hero | the icon visibly changes — same one icon-resolution path, no `bp-alloy`-specific branch | ☐ |

### E2 — `bp-postgres` (carries `shareable` + `contextSchema`, edit its `contextSchema`)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-postgres` → **Edit IaC** | edit `spec.contextSchema.kind` (or a `contextSchema` field) → Commit | lands in `catalog-sovereign/bp-postgres/blueprint.yaml` (git log); `kubectl ... get blueprint bp-postgres -o jsonpath='{.spec.contextSchema}'` reflects it after reconcile — `contextSchema`/`source`/`manifests` **survive** the edit (today an edit would drop them to a `version:0.0.0` stub — FAIL) | ☐ |
| terminal | `git diff` of the implementation | shows **zero** blueprint-specific code paths — the same `writeCatalogEditToGit`/reconcile, icon resolution, budget+result-propagation, and `YamlEditor`/`edit-pr` handle alloy + wordpress + postgres alike | ☐ |

---

## Acceptance summary (binary)

| # | Headline | Result |
|---|---|---|
| 1 | A READY Flux source reconciles `catalog-sovereign` → Blueprint CRs (A1) | ☐ |
| 2 | A console edit reaches the in-cluster CR; the committed file is a full CR not a `0.0.0` stub (A2, A3) | ☐ |
| 3 | A non-card field (`spec.source.version`) round-trips git ↔ CR ↔ UI (A4) | ☐ |
| 4 | A chart upgrade does NOT revert a console edit — Helm no longer owns the CR (A5) | ☐ |
| 5 | The edited icon visibly renders (hero + grid + out-of-band), the form pre-fills the IaC icon, the picker works (B1–B5) | ☐ |
| 6 | "Saved to IaC ✓" on success; amber/no-green-save when Gitea is down; retry reconverges — no silent divergence (C1, C2) | ☐ |
| 7 | Per-field inline for cards (widened set) + full-CR `YamlEditor` for the rest, both committing the same Gitea file (D1, D2) | ☐ |
| 8 | Identical mechanism on `bp-wordpress` + `bp-postgres`, `git diff` shows zero per-blueprint branches (E1, E2) | ☐ |

---

## Appendix — automated checks (NOT acceptance)

- `go test -race ./products/catalyst/bootstrap/api/internal/handler/...` — incl.:
  - the slow-Gitea injection (`PutFile` sleeps 3s) asserting `committed:true` under `catalogEditGitBudget` (today: deadline at 1500ms → swallowed);
  - the erroring-Gitea injection (`PutFile` errors) asserting the edit response reports `committed:false` + reason (today: 200 OK, store-only);
  - the catalog-edit merge preserving `source`/`manifests`/`placementSchema`/`sso`/`contextSchema` (today: dropped to a `0.0.0` stub);
  - `parseBlueprintCRToCatalog` reading `card.iconLight`/`iconDark`.
- `npm test` (`products/catalyst/bootstrap/ui`) — `CatalogDetail.test.tsx` asserting the hero `<img src>` follows `card.iconLight`; the `IconPicker` test; per-field inline summary save; `YamlEditor` mounting on a blueprint CR + commit.
- `scripts/expected-bootstrap-deps.yaml` lockstep for any new `dependsOn`; the new Flux-source slot present in the bootstrap-kit `kustomization.yaml` (drift guard); kustomize build green; pin-sync-audit green for the chart/blueprint.yaml/slot-pin bump.

> Token-passing / `must_contain` style checks are forbidden as acceptance (PR #1362 shape). Acceptance
> is the founder walking the clickable rows above and seeing the CR move, the icon change, the save
> states, and the full-CR editor commit — on the CURRENT env.
