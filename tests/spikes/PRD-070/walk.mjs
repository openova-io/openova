// Browser walk for spike PRD-070.
// For each test user: sign in to the chat front end through the test realm,
// send one marked message, and wait until the stub has a request carrying the
// marker. For the first user, wait until the first access token has expired
// and send a second message in the same page.
// Usage: node walk.mjs <base-url> <capture-file> <out-dir> <expiry-wait-seconds>
import fs from 'node:fs';
import { chromium } from 'playwright';

const [base, captureFile, outDir, expiryWaitArg] = process.argv.slice(2);
const expiryWait = Number(expiryWaitArg || '0');
const users = [
  { username: 'alice', password: 'alice-spike-070' },
  { username: 'bob', password: 'bob-spike-070' },
];
const REPLY = 'stub reply from spike PRD-070';
const redact = (s) => String(s).replace(/eyJ[\w-]+\.[\w-]+\.[\w-]+/g, '<jwt>');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function findCapture(marker) {
  if (!fs.existsSync(captureFile)) return null;
  for (const line of fs.readFileSync(captureFile, 'utf8').split('\n')) {
    if (!line.trim()) continue;
    const entry = JSON.parse(line);
    if (entry.method === 'POST' && entry.body.includes(marker)) return entry;
  }
  return null;
}

async function waitForCapture(marker, timeoutMs) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const hit = findCapture(marker);
    if (hit) return hit;
    await sleep(500);
  }
  return null;
}

async function promptBox(page) {
  const box = page.locator('#prompt-textarea, [data-testid="text-input"]').first();
  await box.waitFor({ state: 'visible', timeout: 90000 });
  return box;
}

async function send(page, user, marker, steps) {
  const step = { user, marker, sent_at: Date.now() / 1000 };
  try {
    const box = await promptBox(page);
    await box.click();
    await box.fill(`${marker}: what is the capital of Oman?`);
    await page.keyboard.press('Enter');
    step.sent_at = Date.now() / 1000;
    const hit = await waitForCapture(marker, 60000);
    step.captured = Boolean(hit);
    step.captured_at = hit ? hit.received_at : null;
    try {
      await page.getByText(REPLY).last().waitFor({ timeout: 30000 });
      step.reply_shown = true;
    } catch {
      step.reply_shown = false;
    }
  } catch (err) {
    step.error = redact(err.message).slice(0, 500);
  }
  await sleep(2000);
  step.page_text_tail = redact(await page.evaluate(() => document.body.innerText)).slice(-600);
  await page.screenshot({ path: `${outDir}/${marker}.png`, fullPage: true });
  steps.push(step);
  console.log(`walk: ${marker} captured=${step.captured} reply_shown=${step.reply_shown}` +
    (step.error ? ` error=${step.error}` : ''));
  return step;
}

const browser = await chromium.launch({
  args: ['--host-resolver-rules=MAP kc.spike.test 127.0.0.1'],
});
const result = { base, users: [], steps: [] };

for (const u of users) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await ctx.newPage();
  const record = { user: u.username, http_errors: [], console_errors: [] };
  page.on('response', (r) => {
    if (r.url().startsWith(base) && r.status() >= 400) {
      record.http_errors.push(`${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}`);
    }
  });
  page.on('console', (m) => {
    if (m.type() === 'error') record.console_errors.push(redact(m.text()).slice(0, 300));
  });
  try {
    await page.goto(`${base}/oauth/openid`, { waitUntil: 'domcontentloaded', timeout: 60000 });
    await page.locator('#username').waitFor({ timeout: 60000 });
    record.realm_login_url = redact(page.url().split('?')[0]);
    await page.fill('#username', u.username);
    await page.fill('#password', u.password);
    await page.click('#kc-login');
    await page.waitForURL((url) => url.toString().startsWith(base) && !url.pathname.startsWith('/oauth'), {
      timeout: 60000,
    });
    await promptBox(page);
    record.signed_in = true;
    record.landing_path = new URL(page.url()).pathname;
    await page.screenshot({ path: `${outDir}/${u.username}-signed-in.png`, fullPage: true });
    console.log(`walk: ${u.username} signed in, landing ${record.landing_path}`);
  } catch (err) {
    record.signed_in = false;
    record.sign_in_error = redact(err.message).slice(0, 500);
    record.page_text_tail = redact(await page.evaluate(() => document.body.innerText).catch(() => '')).slice(-600);
    await page.screenshot({ path: `${outDir}/${u.username}-sign-in-failed.png`, fullPage: true }).catch(() => {});
    console.log(`walk: ${u.username} sign-in FAILED: ${record.sign_in_error}`);
  }
  if (record.signed_in) {
    await send(page, u.username, `prd070-${u.username}-1`, result.steps);
    if (u.username === 'alice' && expiryWait > 0) {
      console.log(`walk: waiting ${expiryWait}s so that alice's first access token expires`);
      await sleep(expiryWait * 1000);
      await send(page, u.username, `prd070-${u.username}-2`, result.steps);
    }
  }
  result.users.push(record);
  await ctx.close();
}
await browser.close();
fs.writeFileSync(`${outDir}/walk.json`, JSON.stringify(result, null, 2));
