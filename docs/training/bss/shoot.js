// Screenshot campaign for the BSS training deck. Reads the session cookies from
// cookies.json (written from the signed-in MCP browser), walks every operator
// page, tab and the important dialogs, and saves 1440x900 PNGs to shots/.
const { chromium } = require('playwright');
const fs = require('fs');
const SP = '$SCRATCH';
const DIR = SP + '/shots'; fs.mkdirSync(DIR, { recursive: true });
const B = 'https://chargeback.hw307.omani.works';
const C = 'cd894f94-0983-441d-bdfb-cd0151e7d5e6'; // Gulf Retail Group
const CONTRACT = '9f24ab0f-2014-4bd4-817e-21b3976d7373';
const PARTNER = '8e047691-702d-4d0c-880e-2cd4e5935bca';
const BOOK_CLOUD = 'ef6c2463-7cdd-4394-bfcd-ecfd1073549f', BOOK_PLANS = 'da9a7841-a40c-4d3f-8260-c366e089a276', BOOK_PAYG = 'e92cd22f-d0f9-45db-817a-c907aa6443cd';
const ST_ISSUED = 'a352e6d7-437c-4510-8142-2eb533bea0c5', ST_DRAFT = '35b5160c-51fd-4899-af61-6d97091bb927', ST_PAID = '90281379-7491-4ef8-91d0-c87625733111';

const click = (name) => async (page) => { await page.getByRole('button', { name }).first().click({ timeout: 8000 }); await page.waitForTimeout(1500); };
const clickText = (text) => async (page) => { await page.getByText(text, { exact: false }).first().click({ timeout: 8000 }); await page.waitForTimeout(2500); };
const firstLink = (sel) => async (page) => { await page.locator(sel).first().click({ timeout: 8000 }); await page.waitForTimeout(3000); };

const shots = [
  ['01-overview', '/overview'], ['02-explore', '/explore'], ['03-customers', '/customers'], ['04-customer-new', '/customers/new'],
  ...['overview','cost','resources','sources','statements','account','budgets','discounts','contract','users','settings','audit'].map((t, i) => [`05-customer-${String(i+1).padStart(2,'0')}-${t}`, `/customers/${C}?tab=${t}`]),
  ['06-customer-import', '/customers/import'],
  ['07-leads', '/leads'], ['08-estimate', '/estimate'],
  ['09-resources', '/resources'], ['10-resource-detail', '/resources', firstLink('table tbody tr a')],
  ['11-pricebooks', '/pricebooks'], ['12-pricebook-cloud', `/pricebooks/${BOOK_CLOUD}`], ['13-pricebook-plans', `/pricebooks/${BOOK_PLANS}`],
  ['14-pricebook-plans-terms-dialog', `/pricebooks/${BOOK_PLANS}`, click(/Add tiers or allowance|Edit shapes/)],
  ['15-pricebook-payg', `/pricebooks/${BOOK_PAYG}`], ['16-pricebook-settings-dialog', `/pricebooks/${BOOK_PAYG}`, click(/Edit settings/)],
  ['17-discounts', '/discounts'], ['18-tax', '/tax'],
  ['19-contracts', '/contracts'], ['20-contract-new-dialog', '/contracts', click(/New contract|Add contract|Create/)],
  ['21-contract-detail', `/contracts/${CONTRACT}`], ['22-contract-edit-dialog', `/contracts/${CONTRACT}`, click(/^Edit$|Edit contract|Edit terms/)],
  ['23-contract-items-dialog', `/contracts/${CONTRACT}`, click(/items|Add line|Add committed/i)], ['24-contract-sla-dialog', `/contracts/${CONTRACT}`, click(/Issue SLA credit/)],
  ['25-allocation', '/allocation'], ['26-budgets', '/budgets'], ['27-anomalies', '/anomalies'], ['28-recommendations', '/recommendations'], ['29-reports', '/reports'],
  ['30-statements', '/statements'], ['31-statement-issued', `/statements/${ST_ISSUED}`], ['32-statement-draft', `/statements/${ST_DRAFT}`], ['33-statement-paid', `/statements/${ST_PAID}`],
  ['34-billing', '/billing'], ['35-collections', '/collections'],
  ['36-finance-journal', '/finance/journal'], ['37-finance-reconciliation', '/finance/reconciliation'], ['38-finance-periods', '/finance/periods'], ['39-finance-accounts', '/finance/accounts'], ['40-finance-outbox', '/finance/outbox'],
  ['41-partners', '/partners'], ['42-partner-detail', `/partners/${PARTNER}`],
  ['43-capacity-pools', '/capacity?tab=pools'], ['44-capacity-placements', '/capacity?tab=placements'], ['45-capacity-shapes', '/capacity?tab=shapes'], ['46-capacity-regions', '/capacity?tab=regions'],
  ['47-capacity-pool-detail', '/capacity?tab=pools', firstLink('table tbody tr a')],
  ['48-access', '/access'], ['49-notifications', '/notifications'],
  ['50-my-overview', '/my/overview'], ['51-partner-overview', '/partner/overview'],
];

(async () => {
  const browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
  const cookies = JSON.parse(fs.readFileSync(SP + '/cookies.json', 'utf8')).cookies;
  await ctx.addCookies(cookies.map(c => ({ name: c.name, value: c.value, domain: c.domain, path: c.path, expires: c.expires, httpOnly: c.httpOnly, secure: c.secure, sameSite: c.sameSite })));
  const page = await ctx.newPage();
  const log = [];
  for (const [name, path, action] of shots) {
    try {
      await page.goto(B + path, { waitUntil: 'load', timeout: 45000 });
      await page.waitForTimeout(3500);
      if (action) { try { await action(page); } catch (e) { log.push(`${name}: action failed: ${String(e).split('\n')[0].slice(0, 100)}`); } }
      await page.screenshot({ path: `${DIR}/${name}.png` });
      const h1 = (await page.locator('h1').first().textContent().catch(() => '')) || '';
      log.push(`${name}: ${page.url().replace(B, '')} | ${h1.trim().slice(0, 50)}`);
    } catch (e) { log.push(`${name}: ERROR ${String(e).split('\n')[0].slice(0, 120)}`); }
  }
  fs.writeFileSync(SP + '/shots.log', log.join('\n'));
  console.log(log.join('\n'));
  await browser.close();
})();
