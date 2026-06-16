# Catalog is a SINGLE-source IaC commit — UAT walk (web UI + git + kubectl)

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
> **Deferred to follow-on rows of this same ticket (enumerated, not claimed):** standing up the Flux `GitRepository`+`Kustomization` that reconciles `catalog-sovereign/*/blueprint.yaml` into the in-cluster `Blueprint` CR (DoD §9.1/9.4 + the `helm upgrade` revert-immunity), and the per-field-inline card editors + the visual icon picker (DoD §9.8 + §5B picker). The slices above are the foundational, independently-walkable vertical.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in as emrah.baysal (admin).

## Section 1 — The edited icon RENDERS (DoD §9.5, was #3672)

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-alloy` | note the current hero logo | the rendered icon (bundled asset fallback when IaC empty) | ☐ |
| 1.2 | `/catalog/bp-alloy` → **Edit** (`catalog-detail-edit`) | set **Light-theme icon** to a distinct image — e.g. the red-dot data-URI `data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==` → **Save** | the save reports success (see §2) | ☐ |
| 1.3 | reload `/catalog/bp-alloy` (light theme) | observe the hero | the hero logo is now the **red dot** (the IaC icon renders — not the bundled asset) | ☐ |
| 1.4 | `/catalog` (grid) | find the bp-alloy card | the **grid card** icon is also the red dot (same shared resolution path) | ☐ |
| 1.5 | `/catalog/bp-alloy` → **Edit** again | observe the Light-theme icon field | it is **pre-filled with the current IaC icon** (the red-dot value), NOT the bundled asset | ☐ |

## Section 2 — The git write is LOAD-BEARING (DoD §9.6, was #3676)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog/bp-alloy` → Edit → change the summary → **Save** | save with Gitea reachable | the edit response carries `committed: true` (network tab on the `PUT .../sme/commerce/apps/…` shows `{stored:true, committed:true}`) | ☐ |
| 2.2 | local Gitea `catalog-sovereign/bp-alloy/blueprint.yaml` | `git log` / view history | a **commit** carrying the edited summary | ☐ |
| 2.3 | `kubectl --kubeconfig /tmp/<env>.kubeconfig scale deploy/gitea -n gitea --replicas=0` | take Gitea down, then edit the summary again → Save | the edit response carries `committed: false` + a `reason` — the UI does NOT show a green durable "Saved" (it is cache-only) | ☐ |
| 2.4 | restore Gitea (`kubectl scale deploy/gitea -n gitea --replicas=1`), re-save | save | `committed: true` again; the file lands | ☐ |

> The budget proof (a 3s-slow Gitea still commits; a 1500ms-shared ctx would have aborted) is the automated test `TestCommitCatalogAppEditToGit_SlowGiteaStillCommits` — it cannot blow the demo Gitea on purpose, so it is pinned in code, not walked.

## Section 3 — The first edit seeds the FULL CR (DoD §9.2/9.3 foundation, §5A)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | pick a blueprint with NO `catalog-sovereign` file yet (e.g. `bp-wordpress`) | `/catalog/bp-wordpress` → Edit → change the summary → Save | committed:true | ☐ |
| 3.2 | local Gitea `catalog-sovereign/bp-wordpress/blueprint.yaml` | view the committed file | it carries the **full spec** — real `spec.version` (e.g. `1.x`, NOT `0.0.0`), `spec.source`, `spec.manifests` — seeded from the live CR, with the edited summary overlaid | ☐ |

## Section 4 — The full-CR IaC editor (DoD §9.7, was #3682)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 4.1 | `/catalog/bp-alloy` | click **Edit IaC ⟩** (`catalog-detail-edit-iac`) | the **full-CR YamlEditor** opens showing the complete `blueprint.yaml` (incl. `spec.source`, `spec.manifests`) | ☐ |
| 4.2 | in the editor | toggle **Show diff** | a side-by-side current-vs-proposed diff renders | ☐ |
| 4.3 | edit `spec.source.version` (a field the 7-field card form can NEVER touch) → **Commit IaC** | commit | "Committed to IaC ✓" — the response is committed:true | ☐ |
| 4.4 | local Gitea `catalog-sovereign/bp-alloy/blueprint.yaml` | `git log` | the new `spec.source.version` is in the committed file (the SAME file the card edit writes) | ☐ |
| 4.5 | `git grep "widgets/cloud-list/YamlEditor" src/pages/sovereign/CatalogDetail.tsx` | run | the catalog page imports the REUSED editor, not a new bespoke full-CR editor (DoD §9.7) | ☐ |

## Section 5 — Generality (DoD §9.9)

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 5.1 | `bp-wordpress` (structurally different) | repeat §2.1-2.2 editing the summary, and §4 editing its `manifests` | same edit→commit→read + same Edit-IaC commit — no blueprint-specific path | ☐ |
| 5.2 | `bp-postgres` (carries `shareable` + `contextSchema`) | §4: Edit IaC → edit `contextSchema` → Commit | the `contextSchema` survives the commit (today a card edit would have dropped it to a `version:0.0.0` stub) | ☐ |

## Appendix — automated (NOT acceptance)

- `resolveCatalogIcon.test.ts` (10 cases): IaC-first theme-aware icon resolution; bundled-asset fallback; bare-filename guard; letter-mark last.
- `catalog_edit_git_test.go` (new): `…_SlowGiteaStillCommits` (3s PutFile commits under the dedicated budget), `…_ErroringGiteaReportsNotCommitted` (verdict surfaced, not swallowed), `…_UnwiredReportsCacheOnly`, `…_ByteIdenticalIsDurable`, `…_FirstEditSeedsFullCRFromLiveSpec` (real version + manifests seeded, status/managedFields stripped).
- `catalog_iac_edit_test.go` (new): full-CR commit lands `spec.source`; 403 for non-admin; 400 on retargeted/malformed CR; 503 when Gitea unwired.
- `parseBlueprintCRToCatalog`: now reads `card.iconLight`/`iconDark` so a CR/Gitea-sourced theme icon survives into the wire shape.
