/**
 * e2e/gate/fixtures.ts — what a gate specification imports (FND-161).
 *
 *   import { test, expect, consoleURL } from '../fixtures.ts'
 *
 *   test('13 Catalog grid renders the Blueprint cards', async ({ page }) => { ... })
 *
 * A test's title starts with the ledger row id and a space; the row map
 * (docs/ledger/uat-row-map.csv) names the file of each converted row.
 *
 * - page / context: signed in as the run's user. `test.use({ identity:
 *   'viewer' })` signs them in as another identity: 'run' (the run's user,
 *   sovereign-admins and sovereign-viewers), 'second' (a second member of
 *   sovereign-admins, tier admin) or 'viewer' (sovereign-viewers only).
 * - signIn(identity): a further browser context signed in as that identity,
 *   closed after the test.
 * - openHost(page, sub, path): another host of the same Sovereign (for
 *   example the access gateway) in a new page of the same context, so under
 *   the same sign-on.
 * - typeIntoCanvas(locator, text) / pressInCanvas(locator, key): keyboard
 *   input to a canvas element (a remote terminal or desktop).
 * - regionKubectl(region, args): kubectl with the kubeconfig the gate
 *   workflow holds for region a or b, to apply a fixture or read an object.
 *   It exists only inside the gate workflow.
 */

import { execFile } from 'node:child_process'
import { test as base, expect, type BrowserContext, type Locator, type Page } from '@playwright/test'
import { consoleURL, fqdn, hostURL, kubeconfigOf, scheme, sessionOf, type Identity, type Region } from './env.ts'

export { expect, consoleURL, hostURL }
export type { Identity, Region }

/** The browser state of a signed-in identity: the console session cookie on the Sovereign's domain. */
export function stateOf(who: Identity) {
  return {
    cookies: [{
      name: 'catalyst_session',
      value: sessionOf(who),
      domain: `.${fqdn()}`,
      path: '/',
      expires: -1,
      httpOnly: true,
      secure: scheme() === 'https',
      sameSite: 'Lax' as const,
    }],
    origins: [],
  }
}

interface GateFixtures {
  identity: Identity
  signIn: (who: Identity) => Promise<BrowserContext>
}

export const test = base.extend<GateFixtures>({
  identity: ['run', { option: true }],
  storageState: async ({ identity }, provide) => {
    await provide(stateOf(identity))
  },
  signIn: async ({ browser, contextOptions }, provide) => {
    const opened: BrowserContext[] = []
    await provide(async (who: Identity) => {
      const ctx = await browser.newContext({ ...contextOptions, storageState: stateOf(who) })
      opened.push(ctx)
      return ctx
    })
    for (const ctx of opened) await ctx.close()
  },
})

/** Opens another host of the Sovereign in a new page of the same browser context (the same sign-on). */
export async function openHost(page: Page, sub: string, path = '/'): Promise<Page> {
  const other = await page.context().newPage()
  await other.goto(hostURL(sub, path))
  return other
}

/** Focuses a canvas element and types text into it, key by key. */
export async function typeIntoCanvas(canvas: Locator, text: string): Promise<void> {
  await canvas.click()
  await canvas.page().keyboard.type(text)
}

/** Focuses a canvas element and presses one key or chord (for example 'Enter', 'Control+C'). */
export async function pressInCanvas(canvas: Locator, key: string): Promise<void> {
  await canvas.click()
  await canvas.page().keyboard.press(key)
}

export interface KubectlResult {
  code: number
  stdout: string
  stderr: string
}

/** Runs kubectl --kubeconfig <the region's kubeconfig> <args>; never throws on a non-zero exit. */
export function regionKubectl(region: Region, args: string[], timeoutMs = 60_000): Promise<KubectlResult> {
  const kubeconfig = kubeconfigOf(region)
  return new Promise((resolve) => {
    execFile('kubectl', ['--kubeconfig', kubeconfig, ...args], { timeout: timeoutMs, maxBuffer: 32 << 20 }, (err, stdout, stderr) => {
      const code = err ? (typeof err.code === 'number' ? err.code : 1) : 0
      resolve({ code, stdout: String(stdout), stderr: String(stderr) || (err && typeof err.code !== 'number' ? err.message : '') })
    })
  })
}
