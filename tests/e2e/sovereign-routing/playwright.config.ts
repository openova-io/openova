// Playwright config for the Sovereign wizard routing smoke test.
//
// Per docs/INVIOLABLE-PRINCIPLES.md §4, every URL is values/env-driven:
//   SOVEREIGN_BASE_URL — defaults to https://console.openova.io
//   SOVEREIGN_BASE_PATH — defaults to /sovereign (matches chart values
//                         routing.basePath)
//
// Local-against-cluster invocation:
//   npm install
//   npx playwright install chromium
//   SOVEREIGN_BASE_URL=https://console.openova.io npm test
//
// CI invocation runs the same way against whatever environment the
// runner has TLS access to — typically a kind/k3d cluster brought up
// with the chart rendered against test values.
//
// The test does NOT spin up its own webServer because the system under
// test is the rendered bp-catalyst-platform chart on a real cluster;
// pretending to start the wizard via `vite preview` would test the
// build, not the chart's nginx + ingress wiring.

import { defineConfig, devices } from '@playwright/test'

const baseURL = process.env.SOVEREIGN_BASE_URL ?? 'https://console.openova.io'

export default defineConfig({
  testDir: '.',
  testMatch: /.*\.spec\.ts/,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // Sovereign installs use Let's Encrypt staging certs during the first
    // few minutes; ignore TLS errors so the test isn't flaky in that
    // window. Real prod certs validate fine without this flag.
    ignoreHTTPSErrors: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
