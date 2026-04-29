// playwright.config.ts — config for the cosmetic + step-flow regression
// guard suite that lives next to the Catalyst bootstrap UI source.
//
// Why a SECOND Playwright config (alongside tests/e2e/playwright)?
// ---------------------------------------------------------------
// The repo-level suite at tests/e2e/playwright/ runs the cross-app smoke
// tests for issues #142/#143/#144 and is owned by the broader E2E-suite
// agent (issue #184). This config is narrower — it owns ONLY the
// cosmetic + step-flow regression guards in `e2e/cosmetic-guards.spec.ts`
// for a specific list of defects the user has called out repeatedly:
// card height drift, logo-tile colour drift, step ordering, no-DAG-on-
// provision, sidebar-matches-canonical, expand-in-place jobs, etc.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL is
// driven by env vars with sensible local-dev defaults. Vite serves the
// app under the `/sovereign/` basepath (see vite.config.ts), so the
// default BASE_URL points at the dev port AND the basepath.

import { defineConfig, devices } from '@playwright/test'

// Defaults match `vite.config.ts` (server.port = 5173, base = '/sovereign/').
// Both are overridable for CI runners or when another vite instance has
// claimed 5173 — the CI workflow at .github/workflows/cosmetic-guards.yaml
// sets PLAYWRIGHT_HOST explicitly.
const HOST = process.env.PLAYWRIGHT_HOST ?? 'http://localhost:5173'
const BASEPATH = process.env.PLAYWRIGHT_BASEPATH ?? '/sovereign'

export default defineConfig({
  testDir: './e2e',
  // 30 s per test is enough for a wizard walk-through; bumped from the
  // Playwright default of 10 s because some of the screenshot-based
  // luminance assertions wait for fonts + images to settle.
  timeout: 30_000,
  expect: { timeout: 5_000 },

  // Workers=1: the cosmetic guards mutate the wizard's zustand store via
  // localStorage (next/back walk-through); running siblings in parallel
  // would cross-contaminate the wizard state between tests. Mirrors the
  // tests/e2e/playwright/ choice for the same reason.
  fullyParallel: false,
  workers: 1,

  // No retries locally (a flake here means a real defect leaked); one
  // retry in CI to absorb font-load / image-decode timing variance
  // without masking a true regression.
  retries: process.env.CI ? 1 : 0,

  reporter: [['list']],

  use: {
    // Trailing slash on baseURL is REQUIRED for WHATWG URL resolution
    // to keep `page.goto('wizard')` resolving as
    //   http://host:port/sovereign/wizard
    // (without it, "wizard" replaces "sovereign" in the last path
    // segment per RFC 3986). See playwright docs on baseURL.
    baseURL: `${HOST}${BASEPATH}/`,
    headless: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    // 1440 x 900 — the canonical desktop viewport the user reviews
    // visual fidelity at (per CLAUDE.md memory feedback_parallel_agents_e2e:
    // "screenshots at 1440px, compare to canonical").
    viewport: { width: 1440, height: 900 },
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
