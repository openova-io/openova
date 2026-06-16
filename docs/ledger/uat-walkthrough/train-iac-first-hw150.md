# IaC-first train — hw150 final UAT walk (web UI)

> **Scope:** the founder walks every **car** of the *IaC-first standard-engine train* on the next
> fresh prov (**hw150**). Cars: **T0** (#3649 — topology dialect + inline catalog edit, items 1/2/3),
> **T1** (#3650 — cutover bulletproof, #0/#3/#3647), **T2** (de-hardcode, #4), **T3** (unified Flux
> activities, #5/#3646), **T4** (UI faithfulness, #6), **T5** (Continuum/DR switchover, #7/#3492),
> **T6** (catalog-edit→git, #8). Train plan: [`docs/sessions/2026-06-16/train-iac-first-hw150.md`](../../sessions/2026-06-16/train-iac-first-hw150.md).
>
> **Format law (founder, 2026-06-03):** every row is **ONE UI action** — *Go to a URL → do one
> click/type → see one screen/state.* Routes/labels are quoted from the deployed console source
> (`products/catalyst/bootstrap/ui/src/`) with a `file:line` citation where the surface already
> exists on `main`. Surfaces still landing with their car carry **`(route TBD — fill at merge)`** and
> are finalised when that car's PR merges. `grep`/`go test`/`kubectl` are the **Appendix — automated,
> NOT acceptance.**
>
> **The spine being proven (all 7 cars are one principle):** IaC in git is the single source of
> truth; the engine is standard (no per-app code); the UI is a thin two-way skin (renders IaC, edit =
> commit, reconcile = Flux); every activity is a reconciliation. Each section is one corollary made
> walkable.
>
> **Replace at walk time:** `<fqdn>` = hw150 sovereign FQDN (e.g. `hw150.omani.works`); `<JWT>` = the
> RS256 handover token (`iss=console.openova.io`, `sub=emrah.baysal@openova.io`, `role=sovereign-admin`);
> `<region-a>`/`<region-b>` = the two region codes (e.g. `me-east-215-a`/`-b`). Tick **☑** pass, **☒** fail.

**Sign-in (once, whole walk).** Open `https://console.<fqdn>/auth/handover?token=<JWT>` → it 302s to
`/dashboard` and you are **signed in with NO login form** (avatar **E** top-right). Zero-click entry;
do not re-authenticate between sections.

---

## T0 — Topology dialect + inline catalog edit (#3649 · founder items 1/2/3)

**Proves:** (#3) a postgres instance creates at any *supported* topology without the
`active-hotstandby not in supported` error; (#2) the AppDetail placement strip never offers an
unsupported mode (no contradiction with the declared header); (#1) the catalog entry is editable
**in place on its own page** — no separate chip + popup.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 0.1 | `/catalog/bp-postgres` | **+ New instance** → topology **active-hot-standby** → pick `<region-a>`+`<region-b>` → **Create** | install starts; **NO** `topology "active-hotstandby" not in supported [singleton active-hot-standby]` error (#3) | ☐ |
| 0.2 | `/app/<a postgres instance>` → **Topology & DR** tab | inspect the *Change placement* picker | it offers **only** the Blueprint's declared modes; active-active is **greyed/absent** for a singleton·active-hot-standby Blueprint — matches the declared header (#2) | ☐ |
| 0.3 | `/catalog/bp-alloy` | click the admin **Edit** button in the hero (`CatalogDetail.tsx` `catalog-detail-edit`) | the edit form opens **inline on the page** (name / summary / supported topologies / light+dark icon) — **no modal, no separate chip** (#1) | ☐ |
| 0.4 | (same page) | change the display name → **Save** | the page refreshes in place with the new name; reload still shows it | ☐ |
| 0.5 | `/catalog` (as admin) | click the **Edit** chip on a card | it **navigates** to that card's detail page — **no popup** opens on the grid (#1) | ☐ |

## T1 — Cutover bulletproof: zero external deps, proven (#3650 · founder #0/#3, #3647)

**Proves:** after handover+cutover the Sovereign has **no external dependency**, and the gate proves
it by **rolling a pod under deny-egress** and requiring a fresh pull from the **local Harbor**.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 1.1 | `/jobs` (after handover fires the cutover) | watch the cutover activity (see T3 — it is now visible) | all 11 cutover steps reach **Succeeded**, including **step-08 egress-block** | ☐ |
| 1.2 | cutover status surface `(route TBD — fill at merge)` | read `cutoverComplete` | **`cutoverComplete = true`** — set ONLY because a pod was force-rolled under deny-egress and pulled from local Harbor (the fresh-pull proof) | ☐ |
| 1.3 | `/catalog/bp-grafana` → open Grafana, then (operator) delete a Grafana pod | watch it reschedule | the new pod reaches **Running** by pulling from `registry.<fqdn>` (local Harbor) — **not** ghcr.io; no `ImagePullBackOff` (this is the #3647 fix, live) | ☐ |

> Appendix check (NOT acceptance): under the 600s deny-egress hold, `egress to github.com/ghcr.io/harbor.openova.io` is blocked while apiserver/DNS/intra-cluster stay reachable (`enableDefaultDeny.egress:false` preserved, #3640).

## T2 — De-hardcode: the engine is standard, not per-app (founder #4)

**Proves:** the "operator produces instances" concept and the SSO admin-seed are **Blueprint-declared
capabilities**, applying uniformly — no `postgres`/`newapi` literals in the engine.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog` | find a **non-postgres** operator Blueprint that declares it produces instances | it shows the **engine-card / + New instance** treatment — the same as postgres, with **no postgres-specific** code path `(route TBD — fill at merge)` | ☐ |
| 2.2 | a Blueprint declaring the standard `sso-bootstrap` contract | open it after install | its first admin is seeded via the Sovereign SSO by the **standard** mechanism (not a newapi-specific Job) `(route TBD — fill at merge)` | ☐ |
| 2.3 | `/catalog/bp-postgres` and `/catalog/bp-newapi` | confirm both still work | postgres still produces instances; newapi still seeds its admin — behaviour preserved through the generalisation | ☐ |

## T3 — Unified Flux activities: every activity is a job on the canvas (founder #5 · #3646)

**Proves:** every platform activity — HelmRelease installs, the cutover steps, a DR switchover — is a
projection of a Flux object / CR reconciliation, shown on one activity canvas with dependency edges.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 3.1 | `/jobs` | open the activity view during/after cutover | the **11 cutover steps** appear as job rows **with their dependsOn edges** — previously invisible (#3646) | ☐ |
| 3.2 | `/jobs` | trigger a DR switchover (T5) and watch | the **switchover** appears as a job/activity with its dependencies — "DR switchover is a flux/reconcile job" (founder #5) `(route TBD — fill at merge)` | ☐ |
| 3.3 | `/jobs` | inspect any HelmRelease-backed row | it reads as a Flux reconciliation — confirming the helm-fed jobs ARE flux, now alongside the others | ☐ |

## T4 — UI faithfulness: never assert-then-retract (founder #6)

**Proves:** the UI renders nothing definitive until its source resolves — no flash, no stale state.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 4.1 | `/catalog/bp-grafana` (multi-instance) | open the page cold | the **+ New instance** button does **not** flash before the list loads; it appears once, after the list resolves (#3648) | ☐ |
| 4.2 | `/catalog/<a singleton app with 1 instance>` | open the page | **no** "+ New instance" button (a singleton that's already full can't create a second) — it never appears-then-disappears | ☐ |
| 4.3 | `/catalog/bp-harbor` then **Open** | open an app card | the **Open** button is present as soon as the URL is known — no late pop-in `(Open-button-late — fill at merge)` | ☐ |
| 4.4 | `/jobs` | open after some jobs completed | completed jobs show **Succeeded** immediately — **not** Pending/Running then a late flip `(jobs-stale — fill at merge)` | ☐ |

## T5 — Standard Continuum/DR switchover (founder #7 · #3492/#3375)

**Proves:** for any app placed active-hot-standby on a 2-region Sovereign, the DR panel shows a
**live Continuum record** and the **Switchover** action actually promotes the replica — generic, not
per-app.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 5.1 | `/app/<an active-hot-standby app>` → **Disaster Recovery** | read the panel | a **live Continuum record** (primary `<region-a>`, replica `<region-b>`, healthy) — **not** "No live Continuum record yet" (#7) | ☐ |
| 5.2 | (same) | click **Switchover…** → confirm | the replica is promoted (primary flips to `<region-b>`); `lastSwitchover` updates; the app stays writable | ☐ |
| 5.3 | `/app/<a different active-hot-standby app>` | repeat 5.1 | a live record there too — proving it's **generic** (grafana, gitea, postgres — same machinery, no per-app code) | ☐ |

## T6 — Catalog edit → git (founder #8)

**Proves:** the catalog edit is an **IaC change** (a commit to the local catalog git), and an
out-of-band git edit round-trips to the UI — IaC is the single source of truth.

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 6.1 | `/catalog/bp-alloy` → **Edit** → change summary → **Save** | then open the local Gitea catalog repo | the edit landed as a **git commit** to the catalog entry's source (not just an overlay row) `(repo path TBD — fill at merge)` | ☐ |
| 6.2 | local Gitea: edit the same entry's metadata directly (out-of-band) → wait for reconcile | reload `/catalog/bp-alloy` | the UI **shows the git-side change** — the UI holds no truth of its own (#8) | ☐ |

---

## Acceptance roll-up

A car is **shipped** only when its section above is walked green on hw150 with a screenshot, per the
5-pillar contract (PR-merge ≠ shipped). The train merges as one batch; this walk is the gate. When a
car merges, replace its `(... TBD — fill at merge)` placeholders with the verbatim route/label +
`file:line` from the merged console/api source.

| Car | PR | Section | Walked on hw150 |
|---|---|---|---|
| T0 | #3649 | T0 | ☐ |
| T1 | #3650 | T1 | ☐ |
| T2 | _(de-hardcode)_ | T2 | ☐ |
| T3 | _(unified activities)_ | T3 | ☐ |
| T4 | #3649-line (UI) | T4 | ☐ |
| T5 | _(Continuum/DR)_ | T5 | ☐ |
| T6 | _(catalog→git)_ | T6 | ☐ |

## Appendix — automated checks (NOT acceptance)

- T1: `helm template` of `platform/self-sovereign-cutover/chart` renders clean; the egress CCNP keeps `enableDefaultDeny.egress:false`; the fresh-pull proof job force-rolls a pod and fails the gate on `ImagePullBackOff`.
- T2/T3/T5/T6: `go test ./...` green in the changed packages (per-PR).
- T0/T4: `npx vitest run src/pages/sovereign` + `npx tsc --noEmit` green (per PR #3649).
