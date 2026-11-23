// The self-test project SELF-G01 (FND-161): every fixture of the harness does what it says, for real,
// against the stand-in server of e2e/gate/self-test/server.ts and the kubectl of the machine.
import { test, expect, consoleURL, openHost, pressInCanvas, regionKubectl, typeIntoCanvas } from '../../fixtures.ts'

test('S1 the page of a test is signed in as the run user on the console host', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('#host')).toHaveText('console.self-test.invalid')
  await expect(page.locator('#session')).toHaveText('session-of-run')
})

test.describe('as the viewer identity', () => {
  test.use({ identity: 'viewer' })
  test('S2 test.use signs the page in as the identity it names', async ({ page }) => {
    await page.goto(consoleURL('/dashboard'))
    await expect(page.locator('#session')).toHaveText('session-of-viewer')
  })
})

test('S3 signIn opens a further context signed in as the second identity', async ({ page, signIn }) => {
  const second = await (await signIn('second')).newPage()
  await second.goto(consoleURL('/'))
  await expect(second.locator('#session')).toHaveText('session-of-second')
  await page.goto('/')
  await expect(page.locator('#session')).toHaveText('session-of-run')
})

test('S4 openHost opens another host of the Sovereign under the same sign-on', async ({ page }) => {
  await page.goto('/')
  const other = await openHost(page, 'access', '/terminal')
  await expect(other.locator('#host')).toHaveText('access.self-test.invalid')
  await expect(other.locator('#session')).toHaveText('session-of-run')
})

test('S5 keyboard input reaches a canvas element', async ({ page }) => {
  await page.goto('/')
  const screen = page.locator('#screen')
  await typeIntoCanvas(screen, 'ls -l')
  await pressInCanvas(screen, 'Enter')
  await expect(page.locator('#typed')).toHaveText('ls -l[Enter]')
})

test('S6 regionKubectl runs kubectl with the kubeconfig of each region', async () => {
  const jsonpath = ['config', 'view', '-o', 'jsonpath={.clusters[0].name}']
  expect(await regionKubectl('a', jsonpath)).toEqual({ code: 0, stdout: 'self-test-region-a', stderr: '' })
  expect(await regionKubectl('b', jsonpath)).toEqual({ code: 0, stdout: 'self-test-region-b', stderr: '' })
  const bad = await regionKubectl('a', ['no-such-command'])
  expect(bad.code).not.toBe(0)
})
