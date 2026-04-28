// Playwright config — Group L UI smoke tests (issues #142, #143, #144).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode"), all environment-
// dependent values come from env vars with sensible local-dev defaults:
//
//   BASE_URL                — root for the deployed apps (default
//                             http://localhost:4321 — Catalyst UI vite dev port)
//   ADMIN_BASE_URL          — root for the SME admin Astro app (default
//                             http://localhost:4323 — see core/admin/package.json)
//   MARKETPLACE_BASE_URL    — root for the SME marketplace Astro app (default
//                             http://localhost:4322 — Astro default for second app)
//
// Workers=1 because the wizard test mutates a shared zustand store via
// localStorage; running siblings in parallel could cross-contaminate.
//
// Retries=1 to absorb transient flake (animation timing, font load) without
// masking real regressions.
//
// Reporter `list` matches the rest of the repo's CI ergonomics (see
// tests/dod/dod_test.go output style).

import { defineConfig, devices } from '@playwright/test'

const BASE_URL = process.env.BASE_URL || 'http://localhost:4321'

export default defineConfig({
  testDir: './tests',
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
