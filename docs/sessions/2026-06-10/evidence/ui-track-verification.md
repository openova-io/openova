# UI track — verification (2026-06-10)

The founder's three UI asks, verified done in code + tests on the production
React tree (`products/catalyst/bootstrap/ui`):

## 1. Open button right-aligned on the hero (founder's #1 ask)
"Open button … aligned on the right hand side on the same line where the logo
is positioned" → "Logo left, Open right, the rest in between."

- `AppDetail.tsx` hero is a flex row: `.hero-logo` (left) · `.hero-body` (middle)
  · `.hero-launch` (right). The `.hero-launch` rule (AppDetail.tsx:1559-1561):
  `display: flex; flex-direction: column; gap: 0.2rem; margin-left: auto;
   flex-shrink: 0; text-align: right;` — `margin-left: auto` right-aligns it as
  the last child; responsive override at :1564 stacks it below on narrow screens.
- Test: `AppDetail.test.tsx` asserts `findByTestId('hero-launch')` +
  `btn-launch-app` reads "Open <App>". **28/28 AppDetail tests pass.**

## 2. Catalog class page — every app gets its own page (#3165)
- `CatalogDetail.tsx` rebuilt instances-centered, AppDetail design language
  (breadcrumb, hero, instances table body, topologies, deps, bootstrap-singleton).
  Merged #3166. **12/12 CatalogDetail tests pass.** Backed by live data
  (`GET /api/v1/catalog/bp-postgres` → 200, 78 CRs). status/uat (UI-walk pending).

## 3. Sandbox plans scoped out of the generic plan picker (#3156)
- Fix merged #3157 + regression re-pin #3179; generic Org-provisioning picker
  shows the 5 SME tiers (S/M/L/XL/Flexi), Sandbox tiers scoped out. status/uat
  (screenshot evidence `hw124-plans-5tiers-no-sandbox-3156.png`).

Remaining for all three = the operator UI-walk (PIN session) = acceptance, not code.
