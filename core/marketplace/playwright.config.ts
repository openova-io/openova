// Playwright config — Marketplace customer-journey regression gate.
//
// Codifies the 17-step storefront → signup → provisioning → console flow
// originally walked manually by a fix-author agent during PR #1635.
//
// Environment contract (per docs/INVIOLABLE-PRINCIPLES.md #4 — never hardcode):
//
//   MARKETPLACE_BASE_URL — root of the built marketplace app
//                          (default: http://localhost:4321 — Astro `astro preview`
//                          binds 4321 by default for the marketplace package).
//
// The test is designed to run against `npm run build && npm run preview`
// locally — no API server required because every backend call is intercepted
// via `page.route()` in the spec itself. This keeps the regression check
// hermetic: a fresh checkout + `npm run build && npm run preview &&
// npx playwright test customer-journey` is enough.
//
// Workers=1 because the journey mutates the shared `localStorage` cart;
// running siblings in parallel would cross-contaminate.
//
// Retries are kept at the same posture as the rest of the repo
// (tests/e2e/playwright/playwright.config.ts) — 1 on CI, 0 locally.

import { defineConfig, devices } from '@playwright/test'

const BASE_URL = process.env.MARKETPLACE_BASE_URL || 'http://localhost:4321'

export default defineConfig({
  testDir: './playwright',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: {
    baseURL: BASE_URL,
    headless: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    viewport: { width: 1280, height: 800 },
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
