// The self-test project SELF-G02 (FND-161): a failing test makes its check FAIL, and a skipped one is no pass.
import { test, expect } from '../../fixtures.ts'

test('S7 a failing test makes its check FAIL', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('#session'), 'the self-test fails here on purpose').toHaveText('another session', { timeout: 1_000 })
})

test.skip('S8 a skipped test is recorded as SKIP', async () => {})
