/**
 * e2e/gate/playwright.config.ts — the browser harness of the gate (FND-161).
 *
 * One project per gate check whose implementation is a folder of
 * specifications (e2e/gate/checks.ts), named by the check id, with the
 * folder as its testDir. Run it through e2e/gate/run.sh, which names the
 * checks on its command line. Inputs: e2e/gate/env.ts, and
 *
 *   GATE_MANIFEST  the JSON export of the gate manifest (acceptance-gate.py export)
 *   GATE_RUN_IN    the place of the run, a value of runs_in (default cloud)
 *   GATE_CHECKS    the checks to run, separated by commas (run.sh sets it; empty: all)
 *   GATE_RESULTS   the folder of the result files, one <check id>.json each
 *   GATE_OUTPUT    the folder of the traces and screenshots of the failed tests
 *   GATE_SELF_TEST 1: the stand-in manifest and server of e2e/gate/self-test
 */

import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig, devices } from '@playwright/test'
import { browserChecks, readManifest, selectChecks } from './checks.ts'
import { consoleURL } from './env.ts'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../../../../..')
const selfTest = process.env.GATE_SELF_TEST === '1'
const manifest = selfTest ? join(here, 'self-test', 'manifest.json') : process.env.GATE_MANIFEST
if (!manifest) throw new Error('GATE_MANIFEST is not set: the harness reads the JSON export of the gate manifest')
const named = (process.env.GATE_CHECKS ?? '').split(',').map((s) => s.trim()).filter((s) => s !== '')
const checks = selectChecks(browserChecks(readManifest(manifest), process.env.GATE_RUN_IN || 'cloud'), named)
if (checks.length === 0) throw new Error(`no check of ${manifest} is a folder of the harness that is expected-fail or required here`)

export default defineConfig({
  // The top level matches no file: only the projects of the checks run.
  testDir: here,
  testMatch: /^$/,
  outputDir: process.env.GATE_OUTPUT || join(here, 'test-results'),
  timeout: 120_000,
  expect: { timeout: 15_000 },
  // A gate check is judged on one attempt, one test at a time: specifications may share the Sovereign's state.
  retries: 0,
  workers: 1,
  fullyParallel: false,
  forbidOnly: true,
  reporter: [
    ['list'],
    ['./reporter.ts', { outputDir: process.env.GATE_RESULTS || join(here, 'results'), repoRoot }],
  ],
  globalSetup: selfTest ? './self-test/server.ts' : undefined,
  use: {
    baseURL: consoleURL('/'),
    headless: true,
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
    // The self-test sends every host of its stand-in domain to its local server.
    launchOptions: selfTest ? { args: ['--host-resolver-rules=MAP *.self-test.invalid 127.0.0.1'] } : {},
  },
  projects: checks.map((c) => ({
    name: c.id,
    testDir: join(repoRoot, c.folder),
    testMatch: '**/*.spec.ts',
    use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } },
  })),
})
