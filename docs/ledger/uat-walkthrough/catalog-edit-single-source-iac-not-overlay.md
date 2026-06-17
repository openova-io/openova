# Catalog is a SINGLE-source IaC commit — UAT walk (web UI + git + kubectl)

## Status — last re-verified live: hw158 (2026-06-17) — 🟢 IaC spine FIXED (PR #3749), awaiting chart-roll re-walk

- **The IaC-spine FAIL the hw158 walk found is fixed in PR #3749 and verified LIVE on hw158** (chart-side; not yet merged — re-walk after the chart roll). The earlier verdict was ❌ FAIL because a console edit committed to Gitea but the `catalog-sovereign` Flux source could not authenticate, so the commit never reached the Blueprint CR (`spec.card.summary` stayed empty). Root cause was **NOT** the write-path (which already commits the full CR to `openova/openova@catalog-sovereign` via `writeCatalogSovereignAggregator`) — it was the Flux source auth:
  - **(a)** PR #3710 landed the four `catalog-sovereign-flux/*` templates but dropped the `catalogSovereign` values block → the GitRepository rendered with **no secretRef** → anonymous clone → 401. Fixed by adding `catalogSovereign.{gitRepository,repoBootstrap}` to `products/catalyst/chart/values.yaml`.
  - **(b)** Both Flux auth Secrets (`openova-catalog-sovereign-git-auth` + `openova-sme-tenants-git-auth`) were never created — their lookup-based templates read `catalyst-gitea-token` at Helm **render** time (before the pre-install PAT mint), so the lookup was empty. Fixed by a `post-install/post-upgrade` hook Job (`catalog-sovereign-flux/gitea-flux-auth-secrets-sync-job.yaml`) that reads the runtime-minted PAT via `secretKeyRef` and applies both Secrets.
- **Live proof on hw158 (auth fix applied by hand to the running env to prove it):** `gitrepository/openova-catalog-sovereign` → **READY=True**; `kustomization/catalog-sovereign` → **READY=True**; a catalog-sovereign aggregator commit drove `bp-alloy` `spec.card.summary` from empty → populated, with the CR **Flux-adopted** (`kustomize.toolkit.fluxcd.io/name=catalog-sovereign`) and `spec.source`/`manifests` surviving (not a lossy stub). The verify-edit was reverted; the fix lands zero-touch on a fresh prov / chart roll.
- **Still to re-walk on the merged chart:** §1 (icon render), §2 (`committed:true`/`false` envelope surfaced), §3 (full-CR seed), §4 (Edit-IaC), §6/§7 (per-field editors + icon picker) — the editor UI itself was already built (#3713/#3710); this PR unblocks the path it writes to.

> **Ticket:** [#3668](https://github.com/openova-io/openova/issues/3668) · folds #3657 #3672 #3676 #3682 · **Train:** next
>
> **What this proves (the slices shipped in this PR):**
> 1. the **edited icon actually renders** — the catalog detail hero + grid card resolve the IaC icon (`spec.card.iconLight`/`iconDark`) first, not the build-time bundled asset (was #3672 — the icon edit was theater);
> 2. the **git write is load-bearing** — it runs under its own ~15s budget (not the 1500ms commerce probe) and its verdict is surfaced, so a non-committed edit never reads as a durable green "Saved" (was #3676);
> 3. the **first edit seeds the FULL CR** from the live Blueprint — `spec.source`/`manifests`/`sso`/… survive instead of a lossy `version: 0.0.0` card-only stub (§5A / was #3668 spine);
> 4. the **full-CR IaC editor** is wired to the catalog page (reusing the shipping `YamlEditor`) so every field — `spec.source`, `spec.manifests`, `spec.sso`, `contextSchema` — is editable and commits to the SAME catalog-sovereign file the card edit writes (was #3682).
>
> **Format law:** UI rows + git/kubectl verification (the commit IS the acceptance). Replace
> `<fqdn>`/`<JWT>`/`<env>` with the live env at walk time (no prior-env evidence carries over). Tick **☑**/**☒**.
>
> **Now shipped (was deferred):** the **per-field-inline card editors** (§6) + the **visual icon picker** (§7) — DoD §9.8 + §5B picker. The single global "Edit" button + monolithic form is **GONE** (the exact "ugly no standard ux" the founder called out); each card field (name / summary / supported-topologies / icon) is edited independently in place, saving via the SAME `saveCatalogEdit` Gitea/store seam. The icon field opens a **profile-icon-picker-style gallery** of the already-available logos.
>
> **Still deferred to a follow-on row of this same ticket (enumerated, not claimed):** standing up the Flux `GitRepository`+`Kustomization` that reconciles `catalog-sovereign/*/blueprint.yaml` into the in-cluster `Blueprint` CR (DoD §9.1/9.4 + the `helm upgrade` revert-immunity) — landed separately in #3710.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in as emrah.baysal (admin).

## Section 1 — The edited icon RENDERS (DoD §9.5, was #3672)

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-alloy` | note the current hero logo | the rendered icon (bundled asset fallback when IaC empty) | ☐ |
| 1.2 | `/catalog/bp-alloy` → click the **hero logo** (`cif-icon-edit`) → in the **Light theme** picker paste a distinct image into the URL field — e.g. the red-dot data-URI `data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==` → **Save** (`cif-icon-save`) | the save reports success (see §2) | ☐ |
| 1.3 | reload `/catalog/bp-alloy` (light theme) | observe the hero | the hero logo is now the **red dot** (the IaC icon renders — not the bundled asset) | ☐ |
| 1.4 | `/catalog` (grid) | find the bp-alloy card | the **grid card** icon is also the red dot (same shared resolution path) | ☐ |
| 1.5 | `/catalog/bp-alloy` → click the **hero logo** again | observe the Light-theme picker preview | it **previews the current IaC icon** (the red-dot value) as the active selection, NOT the bundled asset | ☐ |

## Section 2 — The git write is LOAD-BEARING (DoD §9.6, was #3676)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog/bp-alloy` → click the **summary** (`cif-summary-edit`) → change it → **Save** (`cif-summary-save`) | save with Gitea reachable | the edit response carries `committed: true` (network tab on the `PUT .../sme/commerce/apps/…` shows `{stored:true, committed:true}`) | ☐ |
| 2.2 | local Gitea `catalog-sovereign/bp-alloy/blueprint.yaml` | `git log` / view history | a **commit** carrying the edited summary | ☐ |
| 2.3 | `kubectl --kubeconfig /tmp/<env>.kubeconfig scale deploy/gitea -n gitea --replicas=0` | take Gitea down, then edit the summary again → Save | the edit response carries `committed: false` + a `reason` — the UI does NOT show a green durable "Saved" (it is cache-only) | ☐ |
| 2.4 | restore Gitea (`kubectl scale deploy/gitea -n gitea --replicas=1`), re-save | save | `committed: true` again; the file lands | ☐ |

> The budget proof (a 3s-slow Gitea still commits; a 1500ms-shared ctx would have aborted) is the automated test `TestCommitCatalogAppEditToGit_SlowGiteaStillCommits` — it cannot blow the demo Gitea on purpose, so it is pinned in code, not walked.

## Section 3 — The first edit seeds the FULL CR (DoD §9.2/9.3 foundation, §5A)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | pick a blueprint with NO `catalog-sovereign` file yet (e.g. `bp-wordpress`) | `/catalog/bp-wordpress` → click the **summary** (`cif-summary-edit`) → change it → Save | committed:true | ☐ |
| 3.2 | local Gitea `catalog-sovereign/bp-wordpress/blueprint.yaml` | view the committed file | it carries the **full spec** — real `spec.version` (e.g. `1.x`, NOT `0.0.0`), `spec.source`, `spec.manifests` — seeded from the live CR, with the edited summary overlaid | ☐ |

## Section 4 — The full-CR IaC editor (DoD §9.7, was #3682)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 4.1 | `/catalog/bp-alloy` | click **Edit IaC ⟩** (`catalog-detail-edit-iac`) | the **full-CR YamlEditor** opens showing the complete `blueprint.yaml` (incl. `spec.source`, `spec.manifests`) | ☐ |
| 4.2 | in the editor | toggle **Show diff** | a side-by-side current-vs-proposed diff renders | ☐ |
| 4.3 | edit `spec.source.version` (a field the 7-field card form can NEVER touch) → **Commit IaC** | commit | "Committed to IaC ✓" — the response is committed:true | ☐ |
| 4.4 | local Gitea `catalog-sovereign/bp-alloy/blueprint.yaml` | `git log` | the new `spec.source.version` is in the committed file (the SAME file the card edit writes) | ☐ |
| 4.5 | `git grep "widgets/cloud-list/YamlEditor" src/pages/sovereign/CatalogDetail.tsx` | run | the catalog page imports the REUSED editor, not a new bespoke full-CR editor (DoD §9.7) | ☐ |

## Section 6 — PER-FIELD inline editing, NO global Edit button (DoD §9.8, §5A — founder item #1)

> *Founder, verbatim:* "why there is a global edit button instead of setting each catalog items fields to be edited independently and save globally, why this ugly no standard ux approach". The single global Edit button + monolithic form is gone; each field is edited in place and saved independently.

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 6.1 | `/catalog/bp-alloy` (as admin) | scan the hero | there is **NO** single global "Edit" button (`catalog-detail-edit` is gone); only the advanced **Edit IaC ⟩** button remains | ☐ |
| 6.2 | hover the **display name** | observe | a subtle inline **edit pencil** appears on the field (`cif-name-edit`) — the standard inline-edit affordance | ☐ |
| 6.3 | click the **display name** → it becomes an inline input → change it → **Save** (`cif-name-save`) | save | only the name commits; the hero title updates live; the summary / topologies / icon are untouched | ☐ |
| 6.4 | click the **summary** (`cif-summary-edit`) → change it → Save | save | only the summary commits (verify on the wire the `PUT …/apps` body still carries the prior `name` — the merge base preserves siblings) | ☐ |
| 6.5 | scroll to **Supported topologies** → click the editable summary row (`cif-topologies-edit`) | toggle a mode checkbox (e.g. `active-active`) → **Save** (`cif-topologies-save`) | only the topology set commits; the read-only grid above reflects the change after reload | ☐ |
| 6.6 | open the name editor → press **Escape** | — | the editor closes WITHOUT saving (standard inline-edit ergonomics; ⌘/Ctrl+Enter saves) | ☐ |
| 6.7 | sign in as a **non-admin** (or flip the tier) → `/catalog/bp-alloy` | observe | **none** of the per-field edit affordances render (`cif-name-edit` / `cif-summary-edit` / `cif-icon-edit` absent); the page is read-only | ☐ |

## Section 7 — VISUAL icon picker (DoD §5B picker — founder item #2)

> *Founder, verbatim:* "why I am not able to see the already uploaded logos? i need to have profile icon picker like approach." The icon field is a profile-icon-picker-style gallery of the vendored logos.

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 7.1 | `/catalog/bp-alloy` → click the **hero logo** (`cif-icon-edit`) | observe | TWO pickers open — **Light theme** + **Dark theme** — each showing a **GRID of real logo thumbnails** (`iconpicker-light-grid` / `iconpicker-dark-grid`), not a bare filename text box | ☐ |
| 7.2 | in the **Light theme** grid | click a vendored logo tile — e.g. **Grafana** (`iconpicker-light-tile-grafana`) | the tile highlights as selected; the **preview** updates to that logo | ☐ |
| 7.3 | **Save** (`cif-icon-save`) → reload `/catalog/bp-alloy` | observe the hero | the hero logo is now the picked Grafana mark (the gallery selection persisted to `icon_light` via the same Gitea/store seam) | ☐ |
| 7.4 | re-open the icon picker | observe the **Dark theme** picker | it renders its own grid; set a different dark logo, Save, toggle the console to dark theme (top-bar) | the dark-theme hero renders the dark icon (theme-aware) | ☐ |
| 7.5 | in either picker | use **Upload new** (`iconpicker-light-upload`) to pick a local PNG | the preview updates to the uploaded image (read as a self-contained `data:` URI) | ☐ |
| 7.6 | in either picker | paste a custom URL into the URL field, then click **Clear** (`iconpicker-light-clear`) | the URL field + preview reset to the empty "no icon" state (letter-mark fallback) | ☐ |

## Section 5 — Generality (DoD §9.9)

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 5.1 | `bp-wordpress` (structurally different) | repeat §2.1-2.2 editing the summary, and §4 editing its `manifests` | same edit→commit→read + same Edit-IaC commit — no blueprint-specific path | ☐ |
| 5.2 | `bp-postgres` (carries `shareable` + `contextSchema`) | §4: Edit IaC → edit `contextSchema` → Commit | the `contextSchema` survives the commit (today a card edit would have dropped it to a `version:0.0.0` stub) | ☐ |

## Appendix — automated (NOT acceptance)

- `resolveCatalogIcon.test.ts` (10 cases): IaC-first theme-aware icon resolution; bundled-asset fallback; bare-filename guard; letter-mark last.
- `catalogIconGallery.test.ts` (§5B picker): the gallery projects the vendored `ALL_COMPONENTS` logos into a sorted, de-duped, renderable-only list; excludes `logoUrl:null` components; `findGalleryIcon` matches a vendored url, not a custom URL.
- `IconPicker.test.tsx` (§5B picker, 7 cases): renders one tile per gallery logo; picking a tile / typing a URL / uploading / clearing fires `onChange`; previews + highlights the current selection; empty-state.
- `CatalogDetail.edit.test.tsx` (§5A + §5B, 10 cases): NO global `catalog-detail-edit` button; per-field affordances render for an admin only; editing name / summary / topologies / icon each saves ONLY that field merged onto the current values (siblings preserved); the icon field opens the visual gallery and a picked tile writes `icon_light`; Cancel discards.
- `catalog_edit_git_test.go` (new): `…_SlowGiteaStillCommits` (3s PutFile commits under the dedicated budget), `…_ErroringGiteaReportsNotCommitted` (verdict surfaced, not swallowed), `…_UnwiredReportsCacheOnly`, `…_ByteIdenticalIsDurable`, `…_FirstEditSeedsFullCRFromLiveSpec` (real version + manifests seeded, status/managedFields stripped).
- `catalog_iac_edit_test.go` (new): full-CR commit lands `spec.source`; 403 for non-admin; 400 on retargeted/malformed CR; 503 when Gitea unwired.
- `parseBlueprintCRToCatalog`: now reads `card.iconLight`/`iconDark` so a CR/Gitea-sourced theme icon survives into the wire shape.
