# Playwright UI smoke tests — Group L (#142, #143, #144)

Lightweight end-to-end smoke tests for the three Catalyst surfaces flagged
as missing UI coverage in the verify-sweep:

| Spec | Issue | Surface tested |
|---|---|---|
| `tests/sovereign-wizard.spec.ts`  | [#142](https://github.com/openova-io/openova/issues/142) | Catalyst bootstrap UI — `/sovereign/wizard` loads + step shell renders |
| `tests/admin-vouchers.spec.ts`    | [#143](https://github.com/openova-io/openova/issues/143) | SME Admin — `/billing` Vouchers (PromoCode) UI |
| `tests/marketplace-cards.spec.ts` | [#144](https://github.com/openova-io/openova/issues/144) | Unified `bp-<x>` Blueprint catalog + StepComponents card grid |

## Run locally

```bash
cd tests/e2e/playwright
npm install
npx playwright install chromium

# In a second shell, boot whichever app you want to test:
#   cd products/catalyst/bootstrap/ui && npm run dev    # → http://localhost:4321
#   cd core/admin                     && npm run dev    # → http://localhost:4323
#   cd core/marketplace               && npm run dev    # → http://localhost:4322

BASE_URL=http://localhost:4321 \
ADMIN_BASE_URL=http://localhost:4323 \
MARKETPLACE_BASE_URL=http://localhost:4322 \
  npx playwright test --reporter=list
```

Tests self-skip when their target app isn't reachable, so a partial run
(e.g. only the wizard up) is still informative.

## CI

`.github/workflows/playwright-smoke.yaml` runs the Catalyst UI suite
(`#142` + `#144`) on every PR touching the relevant paths. The admin and
marketplace specs are skipped in that workflow because spinning up all
three Astro apps + Catalyst API + Postgres in a single GHA job is the
job of the full E2E pipeline, not this smoke.

## Scope notes

- **`#143`**: shipped admin UI uses `active` toggle, not `ISSUED/REVOKED`
  status. Test asserts the actual UI shape (see docstring in spec).
- **`#144`**: "marketplace card grid" = Catalyst wizard's StepComponents.
  All Blueprints today are `visibility: unlisted`; test asserts the data
  layer (catalog.generated.ts) and the documented EmptyState in the UI.
  When `visibility: listed` is published on a Blueprint, the third test
  in this spec will assert the rendered card without code changes.
