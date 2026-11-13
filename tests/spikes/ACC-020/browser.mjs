// Lab test ACC-020, browser part: opens the throwaway pages in headless
// Chromium against the agent on the session pod's IP and stores what each
// page recorded in window.__result. Throwaway; never merged.
import { chromium } from 'playwright';
import fs from 'node:fs';

const [base, outDir, runsFile] = process.argv.slice(2);
const runs = JSON.parse(fs.readFileSync(runsFile, 'utf8'));
const browser = await chromium.launch();
console.log(`chromium ${browser.version()}`);
for (const r of runs) {
  const page = await browser.newPage({ viewport: { width: 1100, height: 860 } });
  const consoleLines = [];
  page.on('console', (m) => consoleLines.push(m.text().slice(0, 200)));
  page.on('pageerror', (e) => consoleLines.push('pageerror: ' + String(e).slice(0, 200)));
  let res;
  try {
    await page.goto(`${base}/${r.path}`, { waitUntil: 'load', timeout: 30000 });
    await page.waitForFunction(() => window.__result && window.__result.done, null, { timeout: r.timeout, polling: 250 });
    res = await page.evaluate(() => window.__result);
  } catch (e) {
    res = { harnessError: String(e).slice(0, 300) };
    try { res.partial = await page.evaluate(() => window.__result); } catch { /* page gone */ }
  }
  res.console = consoleLines.slice(0, 20);
  await page.screenshot({ path: `${outDir}/${r.id}.png` }).catch(() => {});
  fs.writeFileSync(`${outDir}/${r.id}.json`, JSON.stringify(res, null, 2));
  console.log(`BROWSER ${r.id} ${JSON.stringify(res).slice(0, 4000)}`);
  await page.close();
  await new Promise((s) => setTimeout(s, 3000));
}
await browser.close();
