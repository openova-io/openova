# Design note — admin-editable catalog (topology / name / theme icons)

**Status:** SCOPE (proposal only — not built). Tracking issue: **#3593**.
**Author:** session 2026-06-15 (catalog + provisioning UX work, alongside #3591 / #3592).
**Related:** #3373 (placement), #3375 (topology/DR), #3591 (catalog create-flow placement).

---

## Problem

The console catalog is **statically seeded** and not admin-editable for its
display + topology metadata. There are two seed sources today:

1. **SME tenant catalog** — `core/services/catalog/handlers/seed.go` seeds the
   Mongo/FerretDB-backed `store.App` rows that the console renders via
   `GET /catalog/apps` (`core/services/catalog/handlers/handlers.go`). This is
   what `core/console` shows on the Apps/Catalog pages.
2. **Sovereign Blueprint catalog-seed** —
   `products/catalyst/chart/templates/catalog-seed/blueprints.yaml` renders
   `Blueprint` CRs (with `topology.supported`, `placementSchema.modes`,
   `endpoints`, `sso`) that the catalyst-api chained catalog client picks up.

The founder wants a **real catalog** where the admin edits each app's
supported topology, name, light/dark theme icons, etc. — overlaying the seeded
defaults rather than forcing a code/chart change per edit.

## What already exists (do not rebuild)

`core/services/catalog/handlers/routes.go` already exposes a full admin CRUD
surface, gated by `requireAdmin` (accepts `superadmin` **or** `sovereign-admin`
per `handlers.go`):

```
POST   /catalog/admin/apps              (CreateApp)
PUT    /catalog/admin/apps/{id}         (UpdateApp)
PATCH  /catalog/admin/apps/{slug}/publish  (SetAppPublished)   # #710
DELETE /catalog/admin/apps/{id}         (DeleteApp)
```

So **persistence + a mutating API for catalog entries already exist**
(Mongo-backed `store.App`, see `core/services/catalog/store/store.go`). The
publish-toggle (`#710`) is already the operator's per-app curation knob.

### The two real gaps

1. **Schema gap.** `store.App` (store.go) has **no** topology field and no
   light/dark icon split. Today it has: `Icon` (single string/emoji),
   `IconBg`, plus card metadata. There is no `SupportedTopologies`,
   `DefaultTopology`, `IconLight`, or `IconDark`.
2. **UI gap.** There is **no admin-edit UI**. `requireAdmin` mutations are only
   reachable by hand (curl) today. The Sovereign console (`core/console`) is
   the tenant portal; the operator surface lives under `core/admin` /
   operator-console image.

---

## Proposal

### 1. Data model (additive to `store.App`)

Extend `core/services/catalog/store/store.go` `App` with **optional, additive**
fields so existing rows keep working (zero migration risk — Go zero-values +
`omitempty`):

```go
type App struct {
    // ... existing fields ...

    // SupportedTopologies is the set of BCP modes the operator allows for
    // this app's create flow. Values: "single-region", "active-active",
    // "active-hotstandby". Empty => default ["single-region"] (back-compat).
    SupportedTopologies []string `bson:"supported_topologies,omitempty" json:"supported_topologies,omitempty"`
    // DefaultTopology is the pre-selected mode in the create flow. Must be a
    // member of SupportedTopologies. Empty => "single-region".
    DefaultTopology     string   `bson:"default_topology,omitempty" json:"default_topology,omitempty"`
    // IconLight / IconDark are theme-specific icon URLs (or asset keys). When
    // both empty, the console falls back to the existing Icon / logo. When one
    // is set, the console uses it for that theme and falls back for the other.
    IconLight           string   `bson:"icon_light,omitempty" json:"icon_light,omitempty"`
    IconDark            string   `bson:"icon_dark,omitempty" json:"icon_dark,omitempty"`
}
```

**Overlay-over-seed semantics.** `seed.go` continues to seed the canonical
defaults on a fresh Sovereign. `SeedIfEmpty` already runs idempotent migrations
on the "already populated" path; an admin's `PUT /catalog/admin/apps/{id}` edit
writes the row and the seed migrations **must not** clobber operator-edited
fields. Recommended: a per-field "operator-managed" guard (the publish flag
already follows this pattern — seed sets the default once, the operator owns it
thereafter). Concretely: the migration helpers in `seed.go` should only
backfill these fields when **absent**, never overwrite a non-empty
operator-set value.

**Where topology actually binds.** For the SME flow, the create-flow placement
(#3591) translates topology to the canonical `app_configs.postgres`
{active_hot_standby, primary_region, replica_region} contract the provisioning
gitops generator consumes. `SupportedTopologies` therefore feeds the
**create-flow SELECT** (narrowing what #3591's modal offers per app); it is not
a new provisioning input. The Blueprint-CR `topology.supported` in
`blueprints.yaml` remains the source for the catalyst-api/Blueprint path —
keeping them in lockstep is the same discipline the catalog-seed-lockstep guard
already enforces.

### 2. API

Reuse the existing endpoints — **no new routes required**:

- `PUT /catalog/admin/apps/{id}` (`UpdateApp`) already accepts a full `App`
  body. The four new fields ride along automatically once added to the struct.
- `PATCH /catalog/admin/apps/{slug}/publish` stays the publish toggle.

Add only **server-side validation** in `UpdateApp`:
- `DefaultTopology ∈ SupportedTopologies` (or empty).
- each `SupportedTopologies[i] ∈ {single-region, active-active, active-hotstandby}`.
- `IconLight` / `IconDark` are URL-or-empty (no inline data beyond an asset key).

Auth is already correct (`requireAdmin` = superadmin / sovereign-admin).

### 3. Admin-edit UI

A new operator surface (under `core/admin`, the operator/sovereign-admin
console — **not** the tenant `core/console`). One "Catalog" admin page:

- A table of catalog apps (reuse `GET /catalog/apps`, including unpublished).
- Per-row **Edit** opens a form with SELECT/checkbox inputs (no free-text for
  fixed sets, consistent with the #3591 founder rule):
  - **Name** (text — display name is genuinely free-form).
  - **Supported topologies** (multi-select / checkbox set of the 3 modes).
  - **Default topology** (SELECT, constrained to the checked set).
  - **Light icon URL** / **Dark icon URL** (URL inputs with a live preview
    swatch in both theme backgrounds).
  - **Published** toggle (wired to the existing `PATCH .../publish`).
- **Save** → `PUT /catalog/admin/apps/{id}`.

The console catalog (`core/console` CatalogPage / AppDetail) then:
- renders `IconLight` / `IconDark` per the active theme (fallback to `Icon`/logo);
- the #3591 placement step reads `SupportedTopologies` to narrow the Topology
  SELECT (today it offers all three because the field doesn't exist yet — see
  `supportedTopologies()` in `CatalogPage.svelte`, which is the single
  integration point).

---

## Phasing (when built — out of this issue's scope)

1. **Schema + validation** — add the 4 fields + `UpdateApp` validation + the
   "backfill-only, never-clobber" seed guard. Pure backend; no UI.
2. **Console read path** — CatalogPage/AppDetail theme-icon rendering +
   `supportedTopologies()` reads `SupportedTopologies`.
3. **Admin UI** — the `core/admin` Catalog editor page.

Each phase is independently shippable and back-compatible (empty fields ⇒
exactly today's behavior).

## Risks / notes

- **Seed-vs-edit precedence** is the only real hazard: the seed migrations in
  `seed.go` run every startup. They MUST treat the new fields as
  backfill-only. The publish flag (`#710`) is the working precedent.
- **Two catalog sources** (SME `store.App` vs Blueprint CR) must stay in
  lockstep for topology; the catalog-seed-lockstep CI guard already covers the
  Blueprint side. An admin edit to `store.App.SupportedTopologies` only affects
  the SME create-flow SELECT, not the Blueprint CR — acceptable, and explicitly
  scoped here.
- **Icons**: storing URLs (asset keys) keeps the row small and avoids base64
  blobs in Mongo; the asset itself can live in the Sovereign's object store /
  the console's static assets.

## Acceptance (for the eventual build, not this issue)

An operator opens the `core/admin` Catalog editor, changes WordPress's name +
sets a dark-theme icon + restricts it to `single-region` + `active-hotstandby`,
saves; the tenant console then renders the new name + dark icon and the #3591
create-flow Topology SELECT offers exactly those two modes — with **zero** chart
or code change.
