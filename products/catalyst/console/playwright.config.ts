import { defineConfig, devices } from '@playwright/test';

// G117 Playwright config — runs the Astro dev server with MOCK_API=1 so the
// specs hit the in-tree fixture (`tests/e2e/fixtures/mock-blueprints.yaml`)
// not a live catalyst-api. CI mirrors this; live-Sovereign verification
// happens at Wave-2 closeout per the brief.
export default defineConfig({
  testDir: './tests/e2e',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4323',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: 'MOCK_API=1 npm run dev -- --port 4323 --host 127.0.0.1',
    url: 'http://127.0.0.1:4323/catalog/grafana',
    reuseExistingServer: !process.env.CI,
    timeout: 90_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
