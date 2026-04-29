# UI Regression Guards — Catalyst Bootstrap UI

Mapping each Playwright cosmetic + step-flow regression guard to the
user's original complaint and the source-of-truth file the guard
protects.

- **Test file**: `products/catalyst/bootstrap/ui/e2e/cosmetic-guards.spec.ts`
- **Playwright config**: `products/catalyst/bootstrap/ui/playwright.config.ts`
- **CI workflow**: `.github/workflows/cosmetic-guards.yaml`
- **Annotation**: every test is tagged `@cosmetic-guard` so the CI step can filter via `--grep "@cosmetic-guard"`.
- **Companion suite**: `tests/e2e/playwright/` (issues #142/#143/#144 and the broader E2E agent #184). The cosmetic-guards suite is intentionally narrower — only the regressions the user has called out repeatedly.

## Running locally

```bash
cd products/catalyst/bootstrap/ui
npm install               # installs @playwright/test
npx playwright install    # one-time browser download
npm run dev               # starts vite on http://localhost:5173/sovereign/
# (in a second terminal)
npx playwright test e2e/cosmetic-guards.spec.ts
```

If something else has already claimed port 5173 (e.g. another vite
instance), Vite will auto-bump to 5174/5175/etc. Override the test
host accordingly:

```bash
PLAYWRIGHT_HOST=http://localhost:5174 npx playwright test e2e/cosmetic-guards.spec.ts
```

The config reads `PLAYWRIGHT_HOST` (default `http://localhost:5173`) and
`PLAYWRIGHT_BASEPATH` (default `/sovereign`) from the environment, per
INVIOLABLE-PRINCIPLES.md #4 (never hardcode).

## Pass / fail semantics — what "green" means

Regression guards are by design RED while the regression they describe
is in the codebase. A test in this suite turns green only when the
canonical shape it asserts is the actual shape rendered by the wizard
or admin page.

- **Tests 1, 2, 4 (StepComponents card geometry / luminance)**: green
  on main today — the canonical 108px height + per-brand logoTone +
  visible-glyph contract is currently honoured. Any future regression
  in these flips them red.
- **Tests 3, 5, 7, 8, 9 (logo brand surfaces, step order, step gating,
  recommended SKU, per-provider catalog)**: green on main today.
- **Tests 10, 11 (provision SPA route, no DAG)**: green on main today.
- **Test 6 (no "Choose Your Stack" / "Always Included" tab labels)**:
  RED on main today and intentionally so — the legacy tab strip is
  still in `StepComponents.tsx`. Flips green when stepComponentsCopy.ts
  drops `tabChooseLabel` / `tabAlwaysLabel` and StepComponents.tsx
  drops the top-level `role="tablist"` div.
- **Tests 12, 13, 14 (sidebar / AppDetail / JobsPage)**: RED on main
  today — the canonical Sovereign-side `Sidebar.tsx` / `AppDetail.tsx`
  / `JobsPage.tsx` are in flight on a separate branch (companion agent
  scope). Flip green when those files land + the data-testids in the
  table below are present.
- **Test 15 (no Phase 0 banners)**: RED on main today — `PhaseBanners.tsx`
  is still imported by `AdminPage.tsx`. Flips green when the import +
  file are removed and per-job cards take over.

A passing local run with all 15 green means every regression class the
user has shouted about is currently absent. A failing test names the
exact source-of-truth file the implementing agent needs to edit.

## The 15 guards

Every row names: the user's complaint (paraphrased), the canonical
reference, and the file that must NOT regress.

| # | User complaint | Canonical reference | Source-of-truth file | Restored by commit |
|---|----------------|---------------------|----------------------|--------------------|
| 1 | "Card height grew again — should be 108, not 130" | SME marketplace `.app-card` height | `src/pages/wizard/steps/StepComponents.tsx` `.corp-comp-card { height: 108px }` | `691467b4` |
| 2 | "Description text is squished — there's a 70px column wasted on the right" | SME contract minus the `.app-body { padding-right: 72px }` waste | `src/pages/wizard/steps/StepComponents.tsx` `.corp-comp-body` | (cosmetic refactor #175) |
| 3 | "Logo tiles are all white — Temporal/FerretDB/Alloy disappeared" | Each project's homepage / press kit surface | `src/pages/wizard/steps/logoTone.ts` `LOGO_SURFACE` | (logoTone introduction) |
| 4 | "Temporal logo isn't visible — looks like a blank blue square" | `LOGO_SURFACE` brand surface MUST contrast against the glyph | `src/pages/wizard/steps/StepComponents.tsx` `<ComponentLogo>` | (logoTone introduction) |
| 5 | "Wizard steps were in the wrong order somehow" | `WIZARD_STEPS` array | `src/app/layouts/WizardLayout.tsx` | (wizard step refactor #174) |
| 6 | "Don't show the old Choose-Your-Stack / Always-Included tab labels" | SME marketplace single-grid layout | `src/pages/wizard/steps/stepComponentsCopy.ts` (`tabChooseLabel` / `tabAlwaysLabel` retire) + StepComponents.tsx top-level `role="tablist"` retire | (in flight — companion agent) |
| 7 | "Domain step came before Components — that's backwards" | Step order: Components precedes Domain | `src/app/layouts/WizardLayout.tsx` (`WIZARD_STEPS`, `clickable = done`) | (#174) |
| 8 | "Hetzner CPX32 is what we sell — make it the recommended SKU" | `PROVIDER_NODE_SIZES.hetzner` `recommended:true` exactly on `cpx32` | `src/shared/constants/providerSizes.ts` | (provider catalog refactor) |
| 9 | "Huawei SKUs leaked into the Hetzner dropdown" | Per-provider SKU vocabularies are disjoint | `src/pages/wizard/steps/StepProvider.tsx` `skuOptions(provider)` reads `PROVIDER_NODE_SIZES[provider]` only | (provider refactor) |
| 10 | "Provision page has `.html` in the URL — looks like a static page" | tanstack-router SPA route `/provision/$deploymentId` | `src/app/router.tsx` `provisionRoute` + `vite.config.ts` `base: '/sovereign/'` | (DAG retirement) |
| 11 | "The bubble/edge graph is back — get rid of it" | AdminPage card grid replaces the legacy DAG | `src/pages/provision/ProvisionPage.tsx` re-exports `AdminPage` | (DAG retirement) |
| 12 | "Admin sidebar should look exactly like core/console" | `core/console/src/components/Sidebar.svelte` (`<aside class="...w-56...">` + 7-item nav) | `src/pages/sovereign/Sidebar.tsx` | (in flight — companion agent) |
| 13 | "Per-app page should be sectioned, not tabbed" | `core/console/src/components/AppDetail.svelte` sections (hero / About / Connection / Bundled / Tenant / Configuration / Jobs) | `src/pages/sovereign/AppDetail.tsx` | (in flight — companion agent) |
| 14 | "Jobs are expand-in-place cards, not a separate route" | `core/console/src/components/JobsPage.svelte` (button rows + inline expansion) | `src/pages/sovereign/JobsPage.tsx` + `JobCard.tsx` | (in flight — companion agent) |
| 15 | "Get rid of the Hetzner infra + Cluster bootstrap banners" | Per-job cards on AdminPage replace the Phase 0 banners | `src/pages/sovereign/AdminPage.tsx` (drop `<PhaseBanners>` import + delete `PhaseBanners.tsx`) | (in flight — companion agent) |

## Tests that need a `data-testid` PR first

Per INVIOLABLE-PRINCIPLES.md #2 (never compromise quality), no test is
tagged `.skip()` even when its target component is mid-refactor. Each
test fails LOUD with an explicit error message naming the missing
`data-testid` so the implementing agent has a precise target.

The list below is the authoritative set of `data-testid` attributes the
companion-agent's UI work MUST add for the guards to flip green:

| `data-testid` | Goes on | Required by test |
|---------------|---------|-------------------|
| `admin-sidebar` | `<aside>` root of `src/pages/sovereign/Sidebar.tsx` | #12 |
| `job-row-<id>` | The `<button>` row in `src/pages/sovereign/JobsPage.tsx` | #14 |
| `job-expansion-<id>` | The inline expansion node sibling to `job-row-<id>` | #14 |

The `data-testid="component-card-<id>"` and `data-testid="logo-<id>"`
attributes used by tests #1–#4 already exist in the current
`StepComponents.tsx`.

## Why this lives in `products/catalyst/bootstrap/ui/e2e/`, not `tests/e2e/playwright/`

The repo-level `tests/e2e/playwright/` is owned by the broader E2E
suite (issues #142/#143/#144 + #184) and pulls together the wizard,
admin voucher UI, and unified Blueprint card grid. Co-locating the
narrower cosmetic guards next to the UI source they protect:

- keeps the import path to canonical references (e.g. `LOGO_SURFACE`)
  trivially short,
- lets a UI engineer run the guards via `npm run dev` + `npx playwright
  test` from a single working directory,
- and makes the GitHub Actions path filter (`products/catalyst/bootstrap/ui/**`)
  trigger the exact suite that reasons about that tree.

The companion E2E suite agent (#184) and this suite share the
`/sovereign` basepath contract; nothing in either file depends on the
other.
