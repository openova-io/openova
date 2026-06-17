# 3657 — The catalog edit is an IaC commit, edited in place — UAT walk (web UI + git)

> **Ticket:** [#3657](https://github.com/openova-io/openova/issues/3657) · **Car:** T6 · **PR:** #3649 (inline UI) + #3651 (git write) · **Train:** `train/hw150`
>
> **What this proves:** a catalog entry is edited **in place on its own page** (no chip + popup), the
> edit lands as a **git commit** to the local catalog repo, and an out-of-band git edit round-trips to
> the UI — the catalog is genuinely IaC (founder #1 + #8).
>
> **Format law:** UI rows + a git-side verification (the commit IS the acceptance). Replace
> `<fqdn>`/`<JWT>`. Tick **☑**/**☒**.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in as emrah.baysal (admin).

## Section 1 — Edit in place on the detail page; no popup, no grid chip-modal (founder #1)

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-alloy` | click the admin **Edit** button in the hero (`catalog-detail-edit`) | the edit form opens **inline on the page** (name / summary / topologies / icons) — no modal | ☐ |
| 1.2 | `/catalog` (grid) | click a card's **Edit** chip | it NAVIGATES to that card's detail page — no popup opens on the grid | ☐ |

## Section 2 — The edit lands as a git commit (founder #8)

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog/bp-alloy` → Edit → change the summary → **Save** | save | the page refreshes in place with the new summary | ☐ |
| 2.2 | local Gitea `catalog-sovereign/bp-alloy/blueprint.yaml` | view the repo history | a **commit** carrying the edited summary (not just an overlay-store row) — `git log` shows it | ☐ |

## Section 3 — Out-of-band git edit round-trips to the UI

| Step | Go to / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | local Gitea: edit `catalog-sovereign/bp-alloy/blueprint.yaml` directly (change the name) → commit | wait for the projector read | — | ☐ |
| 3.2 | reload `/catalog/bp-alloy` | observe | the UI shows the git-side name (git-first read; the UI holds no truth of its own) | ☐ |

## Section 4 — Generality

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 4.1 | a **second** Blueprint, a **different** field (the light/dark icon) | edit → Save → check git | the same edit→commit→read works — not bp-alloy-specific | ☐ |

## Appendix — automated (NOT acceptance)
- `CatalogPage.edit.test.tsx`: Edit chip navigates to the detail page, no popup mounts.
- `catalog_edit_git_test.go` (7 cases): YAML merge, write→commit→read-back, git-wins-over-store, commerce-body decode+commit, no-op when unwired.
