#!/usr/bin/env node
// uat-3376-funnel-probe.mjs — live-env browser UAT for the #3376 FUNNEL
// SURFACES (stranger-with-voucher → marketplace → 6-step wizard → checkout
// → per-Org console → running app).
//
// SCOPE — surfaces, NOT a driven provision. The FULL funnel terminal
// (redeem a freshly-minted voucher → Org goes ACTIVE → the customer's OWN
// console + purchased app SERVE) provisions real infrastructure and is a
// separate, heavier walk that this probe does NOT drive. THIS probe
// validates the funnel surfaces that RENDER without provisioning:
//
//   • the anonymous marketplace storefront           (marketplace.<fqdn>/)
//   • the redeem page — junk-code honest reject       (/redeem/?code=…)
//   • the 6-step wizard RENDERING, anonymous:
//       plans → apps → add-ons → BCP/topology → review → checkout
//   • the BSS vouchers admin surface (authed)         (console.<fqdn>/bss/vouchers)
//
// The rows whose acceptance REQUIRES a completed provision (the Org active,
// console.<slug> / wordpress.<slug> serving, the post-auth due-zero summary,
// the in-page provisioning timeline, the returning-customer redirect landing,
// the generality 2nd-Org re-walk) are emitted as status NOT-REACHED with the
// honest reason "requires a driven provision (separate walk)" — they are
// NEVER faked GREEN. A NOT-REACHED row does NOT count against the exit code
// (it is an explicit out-of-scope marker), but it DOES capture a screenshot.
//
// SIBLINGS: scripts/uat-console-probe.mjs (console structure) and
// scripts/sso-zero-click-probe.mjs (per-app SSO landing). This probe copies
// their machinery verbatim and adds (a) a `steps` array for wizard click/fill
// navigation and (b) per-row `ctx: 'anon' | 'authed'` — the storefront +
// redeem + wizard run in a FRESH anonymous context (no handover), the BSS
// page runs in the AUTHED handover-session context (mirror of how
// sso-zero-click-probe runs anon vs authed contexts).
//
// Markers are derived from the marketplace source — NOT guesses:
//   core/marketplace/src/pages/index.astro        (storefront hero + Get Started)
//   core/marketplace/src/pages/redeem.astro       (#redeem-not-valid / #redeem-manual-form)
//   core/marketplace/src/components/PlanStep.svelte    (<h1>Pick a plan</h1>, .pcard)
//   core/marketplace/src/components/AppsStep.svelte    (<h1>Build your stack</h1>, .app-card)
//   core/marketplace/src/components/AddonsStep.svelte  (select.domain-tld pool)
//   core/marketplace/src/components/BCPStep.svelte     (Single-region / Active-hot-standby radios)
//   core/marketplace/src/components/ReviewStep.svelte  (<h1>Review & launch</h1>, Monthly total)
//   core/marketplace/src/components/CheckoutStep.svelte(you@company.com / Send sign-in code)
//   products/catalyst/bootstrap/ui/src/pages/sovereign/bss/VouchersPage.tsx
//   products/catalyst/bootstrap/ui/src/pages/sovereign/organizations/BillingModeGate.tsx
//   (matches core/marketplace/playwright/customer-journey.spec.ts's proven selectors).
//
// IMPORTANT — trailing slash: the live Astro build serves the wizard routes
// WITH a trailing slash (`/plans/`, `/redeem/`, …); the no-slash form 301s.
// Every URL here uses the trailing-slash form.
//
// IMPORTANT — redeem visibility: redeem.astro ships ALL outcome <div>s in the
// static HTML, all-but-loading carrying the `hidden` class; client JS un-hides
// exactly one after the redeem-preview fetch resolves. A raw body-text check
// would match a HIDDEN div, so the junk-code assertion uses `selector-visible`
// (Playwright isVisible() respects display:none/`.hidden`).
//
// Usage:
//   node scripts/uat-3376-funnel-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--rows 3376-01,3376-06] [--shots docs/sessions/2026-06-19/evidence] \
//     [--json out.json]
//
// Exit codes: 0 = every in-scope (GREEN/RED) row is GREEN, 1 = ≥1 in-scope
// row RED, 2 = harness error. NOT-REACHED rows never affect the exit code.
// PROBE_CHROMIUM auto-resolves (else any installed chromium-* build).

import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import { writeFileSync, mkdirSync, readdirSync, statSync } from 'node:fs';
import crypto from 'node:crypto';

// ── Chromium resolution (verbatim from the sibling probes) ──────────────
function resolveChromium() {
  if (process.env.PROBE_CHROMIUM) return process.env.PROBE_CHROMIUM;
  try {
    const base = `${process.env.HOME}/.cache/ms-playwright`;
    for (const d of readdirSync(base)) {
      if (!d.startsWith('chromium-')) continue;
      for (const sub of ['chrome-linux64', 'chrome-linux']) {
        const p = `${base}/${d}/${sub}/chrome`;
        try { statSync(p); return p; } catch { /* next */ }
      }
    }
  } catch { /* no cache dir */ }
  return undefined;
}

// ── args ──────────────────────────────────────────────────────────────
const args = {};
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i];
  if (a.startsWith('--')) {
    const k = a.slice(2);
    const v = process.argv[i + 1] && !process.argv[i + 1].startsWith('--') ? process.argv[++i] : 'true';
    args[k] = v;
  }
}
const FQDN = args.fqdn;
if (!FQDN) { console.error('FATAL: --fqdn is required (e.g. hw167.omantel.biz)'); process.exit(2); }
const C = `https://console.${FQDN}`;
const M = `https://marketplace.${FQDN}`;
const SHOTS = args.shots && args.shots !== 'true' ? args.shots : `docs/sessions/${new Date().toISOString().slice(0, 10)}/evidence`;
try { mkdirSync(SHOTS, { recursive: true }); } catch { /* exists */ }
const ENVTAG = FQDN.split('.')[0]; // hw167

// ── handover URL (authed session bootstrap) — same mint as the siblings ─
function b64url(buf) {
  return Buffer.from(buf).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function mintHandoverURL() {
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: 'RS256', typ: 'JWT' };
  const claims = {
    iss: 'https://console.openova.io',
    sub: args['operator-email'] || 'emrah.baysal@openova.io',
    email: args['operator-email'] || 'emrah.baysal@openova.io',
    aud: [C], sovereign_fqdn: FQDN, deployment_id: args['deployment-id'] || '',
    role: 'sovereign-admin', email_verified: true,
    iat: now, exp: now + 300, jti: crypto.randomUUID(),
  };
  const signingInput = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(claims))}`;
  const sig = execFileSync('openssl', ['dgst', '-sha256', '-sign', args['jwt-key']], { input: signingInput });
  return `${C}/auth/handover?token=${signingInput}.${b64url(sig)}`;
}
const handoverURL = args['handover-url'] || (args['jwt-key'] ? mintHandoverURL() : null);
if (!handoverURL) { console.error('FATAL: provide --handover-url or --jwt-key (+ --deployment-id) for the authed BSS row'); process.exit(2); }

// ── shared login / fabrication failure markers ──────────────────────────
// On an AUTHED row, any of these = the handover session did not take (a
// redirect-to-login is a FAIL). The marketplace storefront/redeem/wizard are
// BY DESIGN anonymous, so a header "Sign in" nav LINK is intended UX there —
// the ANON rows do NOT use LOGIN markers; they assert positive renders only
// (mirrors sso-zero-click-probe's marketplace row which only reds a password
// input). A rendered PIN/password FORM is the violation we guard on authed.
const LOGIN = [
  { kind: 'text', v: 'Enter the 6-digit' },      // PIN form = redirect-to-login
  { kind: 'selector', v: 'input[type="password"]' },
  { kind: 'text', v: 'Sign in to continue' },
];
const NOTFOUND = [
  { kind: 'text', v: 'HTTP Status 404' },
  { kind: 'text', v: 'Page not found' },
  { kind: 'text', v: 'upstream connect error' },
];
// A deliberately-junk voucher so the redeem-preview returns 404 → the
// honest "Voucher not valid" state. Never collides with a real code.
const JUNK_CODE = 'NOTAREALVOUCHERCODE-ZZZ999';

// ── row definitions (id ↔ docs/ledger/UAT.md 3376-NN row) ───────────────
// status semantics:
//   scope: 'render'   → a surface that renders WITHOUT a provision; graded
//                       GREEN/RED and counts toward the exit code.
//   scope: 'provision'→ acceptance REQUIRES a completed provision; emitted
//                       NOT-REACHED with `reason`, screenshot still taken,
//                       does NOT affect the exit code.
// Each render row: { id, scope:'render', ctx, url, settleMs,
//   steps?: [{do:'click'|'fill'|'goto'|'waitFor', sel?, value?, timeout?}],
//   positive:[ALL must hold], failure:[NONE may hold] }
const ROWS = [
  // ════════════════════════════════════════════════════════════════════
  // SECTION A — operator BSS voucher surface (AUTHED). On a single-org
  // (parent showback) Sovereign the issuance FORM is dormant (BillingMode
  // Gate), so the GREEN bar is "the BSS billing/vouchers admin surface
  // RENDERS signed-in (form in real-mode OR the showback notice) — NOT a
  // login wall". The absence of the mint form on showback is the funnel's
  // own gap (rows 3376-02/03 below), not a probe failure.
  // ════════════════════════════════════════════════════════════════════
  { id: '3376-00', scope: 'render', ctx: 'authed', url: handoverURL, settleMs: 6000,
    desc: 'handover establishes the authed sovereign-admin session (console dashboard)',
    positive: [{ kind: 'url-includes', v: '/dashboard' }, { kind: 'text', v: 'Dashboard' }],
    failure: [...LOGIN] },
  { id: '3376-01', scope: 'render', ctx: 'authed', url: `${C}/bss/vouchers`, settleMs: 7000,
    desc: 'BSS → Vouchers renders signed-in: the native issuance surface (real mode) OR the showback notice (parent), never a login/PIN wall',
    // SPA route /bss/vouchers redirects client-side to /organizations/billing/vouchers.
    // GREEN = signed-in admin surface present in EITHER billing mode.
    positive: [
      { kind: 'url-regex', v: '/(bss/vouchers|organizations/billing/vouchers)' },
      { kind: 'selector-any', v: '[data-testid="bss-vouchers-page"]|[data-testid="billing-showback-notice"]|[data-testid="billing-mode-gate"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3376-02', scope: 'provision', ctx: 'authed', url: `${C}/bss/vouchers`, settleMs: 6000,
    desc: 'type a weak code `1234` → inline entropy rejection',
    reason: 'the in-console voucher issuance FORM is dormant on the parent (showback mode); minting + the entropy gate require a real-billing customer Org — not driven here' },
  { id: '3376-03', scope: 'provision', ctx: 'authed', url: `${C}/bss/vouchers`, settleMs: 6000,
    desc: 'empty code → server auto-generates a high-entropy <CODE>',
    reason: 'no in-console mint form on showback parent; producing a fresh <CODE> to drive a brand-new stranger walk requires real-billing issuance — not driven here' },

  // ════════════════════════════════════════════════════════════════════
  // SECTION B.1 — redeem on the sovereign-clean storefront (ANON)
  // ════════════════════════════════════════════════════════════════════
  { id: '3376-04', scope: 'provision', ctx: 'anon', url: `${M}/redeem/?code=${JUNK_CODE}`, settleMs: 6000,
    desc: 'redeem a VALID minted code → "Voucher valid · NNNN OMR"',
    reason: 'requires a freshly-minted valid voucher (Section A is showback) — the valid-redeem state cannot be shown without a driven provision' },
  { id: '3376-05', scope: 'render', ctx: 'anon', url: `${M}/redeem/?code=${JUNK_CODE}`, settleMs: 7000,
    desc: 'redeem a JUNK code → honest "Voucher not valid" reject (no tombstone / no detail leak), with a "Browse plans" CTA',
    // redeem.astro ships every outcome div hidden; assert VISIBILITY of the
    // not-valid panel (client un-hides it after the 404 from redeem-preview).
    positive: [
      { kind: 'selector-visible', v: '#redeem-not-valid' },
      { kind: 'text', v: 'Voucher not valid' },
      { kind: 'text', v: 'Browse plans without a voucher' },
    ],
    failure: [{ kind: 'selector-visible', v: '#redeem-valid' }] },
  { id: '3376-06', scope: 'render', ctx: 'anon', url: `${M}/`, settleMs: 6000,
    desc: 'the anonymous marketplace storefront renders THIS Sovereign’s chrome (sovereign-clean, no mothership literal)',
    positive: [
      { kind: 'title-regex', v: 'Build Your Tenant|OpenOva' },
      { kind: 'text', v: 'Build your cloud tenant' },
      { kind: 'text', v: 'Get Started' },
    ],
    // sovereign-clean: the rendered storefront must not surface the mothership host.
    failure: [{ kind: 'text', v: 'console.openova.io' }, { kind: 'text', v: 'omantel.openova.io' }] },
  { id: '3376-07', scope: 'render', ctx: 'anon', url: `${M}/redeem/?code=${JUNK_CODE}`, settleMs: 6000,
    desc: '"Browse plans without a voucher" CTA links into the plan picker (/plans)',
    steps: [
      { do: 'waitFor', sel: 'a[href="/plans"]', timeout: 12000 },
      { do: 'click', sel: 'a[href="/plans"]' },
      { do: 'waitForURL', value: '/plans', timeout: 12000 },
    ],
    positive: [{ kind: 'url-includes', v: '/plans' }, { kind: 'text', v: 'Pick a plan' }],
    failure: [...NOTFOUND] },

  // ════════════════════════════════════════════════════════════════════
  // SECTION B.2 — the 6-step wizard RENDERS (ANON deep-link per step). The
  // Layout returning-user redirect only fires when an sme-token is present
  // (a fresh anon ctx has none), so each wizard route renders directly.
  // ════════════════════════════════════════════════════════════════════
  { id: '3376-08', scope: 'render', ctx: 'anon', url: `${M}/plans/`, settleMs: 6000,
    desc: 'plan grid: tiers S/M/L/XL/Flexi render with a Popular tier; pick M (Selected) → Continue to Stack',
    steps: [
      { do: 'waitFor', sel: '.pcard', timeout: 12000 },
      // pick the Popular (M) tier's Select button — the popular card carries .pcard-hat
      { do: 'click', sel: '.pcard-wrapper:has(.pcard-hat) .pcard-cta' },
    ],
    positive: [
      { kind: 'text', v: 'Pick a plan' },
      { kind: 'count-gte', v: '.pcard|5' },
      { kind: 'text', v: 'Popular' },
      { kind: 'selector', v: '.pcard.selected' },
      { kind: 'selector', v: 'a.float-cta[href="/apps"]' },
    ],
    failure: [...NOTFOUND] },
  { id: '3376-09', scope: 'render', ctx: 'anon', url: `${M}/apps/`, settleMs: 7000,
    desc: 'app catalog renders the apps incl. WordPress; selecting one enables Continue',
    positive: [
      { kind: 'text', v: 'Build your stack' },
      { kind: 'count-gte', v: '.app-card|3' },
      { kind: 'text-regex', v: 'WordPress' },
      { kind: 'selector', v: 'a.float-cta' },
    ],
    failure: [...NOTFOUND] },
  { id: '3376-10', scope: 'render', ctx: 'anon', url: `${M}/addons/`, settleMs: 6000,
    desc: 'add-ons step renders; the subdomain TLD pool offers omani.homes (+ omani.rest/trade/works)',
    positive: [
      { kind: 'selector', v: 'select.domain-tld' },
      { kind: 'selector', v: 'select.domain-tld option[value="omani.homes"]' },
    ],
    failure: [...NOTFOUND] },
  { id: '3376-11', scope: 'render', ctx: 'anon', url: `${M}/bcp/`, settleMs: 6000,
    desc: 'BCP topology step renders BOTH Single-region AND Active-hot-standby radio options (the Pillar-2 BCP choice at signup); selecting hot-standby reveals the region pickers',
    steps: [
      { do: 'waitFor', sel: 'input[name="topology"]', timeout: 12000 },
      // select the active-hot-standby radio (2nd topology card) to reveal Regions
      { do: 'click', sel: '.topology-card:has-text("Active-hot-standby") input[type="radio"]' },
    ],
    positive: [
      { kind: 'text', v: 'Business continuity' },
      { kind: 'text', v: 'Topology' },
      { kind: 'text', v: 'Single-region' },
      { kind: 'text', v: 'Active-hot-standby' },
      { kind: 'count-gte', v: 'input[name="topology"]|2' },
      { kind: 'selector-visible', v: '#primary-region' },   // region picker un-hides on hot-standby
      { kind: 'selector-visible', v: '#replica-region' },
    ],
    failure: [...NOTFOUND] },
  { id: '3376-12', scope: 'render', ctx: 'anon', url: `${M}/review/`, settleMs: 6000,
    desc: 'review & launch summary renders with a Monthly total and the Proceed-to-Checkout CTA',
    positive: [
      { kind: 'text', v: 'Review & launch' },
      { kind: 'text-regex', v: 'Monthly total' },
      { kind: 'text-regex', v: 'Proceed to Checkout' },
    ],
    failure: [...NOTFOUND] },
  { id: '3376-13', scope: 'render', ctx: 'anon', url: `${M}/checkout/`, settleMs: 7000,
    desc: 'checkout PRE-sign-in surface renders the email field + "Send sign-in code" (passwordless — no password field)',
    positive: [
      { kind: 'selector', v: 'input[placeholder*="you@company.com" i], input[type="email"]' },
      { kind: 'text-regex', v: 'Send sign-in code|Send code' },
    ],
    // passwordless contract: no password input anywhere on checkout.
    failure: [{ kind: 'selector', v: 'input[type="password"]' }, ...NOTFOUND] },

  // ════════════════════════════════════════════════════════════════════
  // SECTION B.3–B.6 + C — REQUIRE A DRIVEN PROVISION (not faked GREEN).
  // ════════════════════════════════════════════════════════════════════
  { id: '3376-14', scope: 'provision', ctx: 'anon', url: `${M}/checkout/`, settleMs: 5000,
    desc: 'email-code sign-in (no password) → due-zero summary: "Credit covers this order — 0 OMR due"',
    reason: 'requires a driven provision: the post-auth due-zero summary is gated behind a real email-code round-trip + a started order with an applied voucher credit (separate walk)' },
  { id: '3376-15', scope: 'provision', ctx: 'anon', url: `${M}/checkout/`, settleMs: 5000,
    desc: 'in-page provisioning timeline advances to Done (Creating Org → vCluster → Deploying WordPress → TLS → Health)',
    reason: 'requires a driven provision: the in-page timeline needs a placed order driving real org/vCluster provisioning (separate walk)' },
  { id: '3376-16', scope: 'provision', ctx: 'anon', url: `${C}/`, settleMs: 4000,
    desc: 'after Launch, redirect lands on the per-Org console with publicly-trusted TLS (console.<slug>.<pool-tld>)',
    reason: 'requires a driven provision: a provisioned customer Org whose own console serves externally (separate walk)' },
  { id: '3376-17', scope: 'provision', ctx: 'anon', url: `${C}/`, settleMs: 4000,
    desc: 'per-Org console landing → signed-in zero-click as the Org owner (no login/PIN)',
    reason: 'requires a driven provision: a serving per-Org console with its own realm SSO (separate walk)' },
  { id: '3376-18', scope: 'provision', ctx: 'anon', url: `${C}/`, settleMs: 4000,
    desc: 'Org-console Applications view shows the purchased WordPress card Running/Healthy',
    reason: 'requires a driven provision: a serving per-Org console + a deployed WordPress Application (separate walk)' },
  { id: '3376-19', scope: 'provision', ctx: 'anon', url: `${M}/`, settleMs: 4000,
    desc: 'TERMINAL: the purchased WordPress app SERVES at wordpress.<slug>.<pool-tld> (live rendered site)',
    reason: 'requires a driven provision: the terminal acceptance (purchased app running inside the customer’s own Org) needs a completed provision (separate walk)' },
  { id: '3376-20', scope: 'provision', ctx: 'anon', url: `${M}/`, settleMs: 5000,
    desc: 'returning signed-in customer re-opening the marketplace is sent to their OWN Org console (never the mothership)',
    reason: 'requires a driven provision: a signed-in customer session + a serving per-Org redirect target (the no-mothership-bounce half renders, but the landing needs a provision)' },
  { id: '3376-21', scope: 'provision', ctx: 'anon', url: `${M}/checkout/`, settleMs: 5000,
    desc: 'authenticated-redeem rate-limit: >5 rapid re-submits → "please wait before retrying"',
    reason: 'requires a driven provision: a started order behind the email-code sign-in to exercise the redeem-spam path (separate walk)' },
  { id: '3376-22', scope: 'provision', ctx: 'anon', url: `${M}/`, settleMs: 4000,
    desc: 'GENERALITY: a 2nd voucher with a different slug + different pool-TLD provisions a 2nd Org',
    reason: 'requires a driven provision: a second minted voucher + a second full funnel run (separate walk)' },
  { id: '3376-23', scope: 'provision', ctx: 'anon', url: `${M}/`, settleMs: 4000,
    desc: 'GENERALITY: the 2nd Org console lands signed-in on a DIFFERENT TLD (omani.rest)',
    reason: 'requires a driven provision: a second provisioned Org on a different pool-TLD (separate walk)' },
  { id: '3376-24', scope: 'provision', ctx: 'anon', url: `${M}/`, settleMs: 4000,
    desc: 'GENERALITY: the 2nd Org’s purchased app serves at its own different-TLD FQDN',
    reason: 'requires a driven provision: a second provisioned Org’s serving app (separate walk)' },
];

// ── marker evaluation (sibling-probe kinds + selector-visible/-any) ─────
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'selector-visible': return await page.locator(m.v).first().isVisible().catch(() => false);
    case 'selector-any': {
      // pipe-delimited list — true if ANY selector matches ≥1 node.
      for (const sel of m.v.split('|')) {
        if ((await page.locator(sel).count().catch(() => 0)) > 0) return true;
      }
      return false;
    }
    case 'count-gte': { const [sel, n] = m.v.split('|'); return (await page.locator(sel).count()) >= Number(n); }
    case 'text': { const b = await page.textContent('body').catch(() => '') || ''; return b.includes(m.v); }
    case 'text-regex': { const b = await page.textContent('body').catch(() => '') || ''; return new RegExp(m.v).test(b); }
    case 'title-regex': return new RegExp(m.v).test(await page.title().catch(() => ''));
    default: throw new Error(`unknown marker kind ${m.kind}`);
  }
}

// ── one wizard step (click/fill/goto/waitFor) ───────────────────────────
async function runStep(page, s) {
  switch (s.do) {
    case 'goto': await page.goto(s.value, { waitUntil: 'load', timeout: 45000 }); break;
    case 'waitFor': await page.locator(s.sel).first().waitFor({ state: 'visible', timeout: s.timeout || 12000 }); break;
    case 'waitForURL': await page.waitForURL((u) => u.toString().includes(s.value), { timeout: s.timeout || 12000 }); break;
    case 'click': await page.locator(s.sel).first().click({ timeout: s.timeout || 12000, force: true }); break;
    case 'fill': await page.locator(s.sel).first().fill(s.value, { timeout: s.timeout || 12000 }); break;
    default: throw new Error(`unknown step ${s.do}`);
  }
}

async function probeRow(ctx, row) {
  const shot = `${SHOTS}/${ENVTAG}-${row.id}.png`;
  const result = { id: row.id, scope: row.scope, ctx: row.ctx, desc: row.desc, status: 'RED', finalURL: '', shot, details: [] };

  // provision-scope rows: capture the surface they POINT at, then mark
  // NOT-REACHED with the honest reason — never graded GREEN.
  if (row.scope === 'provision') {
    const page = await ctx.newPage();
    try {
      await page.goto(row.url, { waitUntil: 'load', timeout: 45000 }).catch((e) => result.details.push(`goto: ${e.message.split('\n')[0]}`));
      await page.waitForTimeout(row.settleMs || 4000);
      result.finalURL = page.url();
      await page.screenshot({ path: shot, fullPage: true }).catch(() => {});
    } finally { await page.close().catch(() => {}); }
    result.status = 'NOT-REACHED';
    result.details.push(row.reason);
    return result;
  }

  const page = await ctx.newPage();
  try {
    await page.goto(row.url, { waitUntil: 'load', timeout: 45000 }).catch((e) => result.details.push(`goto: ${e.message.split('\n')[0]}`));
    await page.waitForTimeout(row.settleMs);
    await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
    // wizard navigation steps (click/fill) — failures are recorded, not fatal.
    if (row.steps) {
      for (const s of row.steps) {
        try { await runStep(page, s); } catch (e) { result.details.push(`step ${s.do}${s.sel ? ' ' + s.sel : ''}: ${e.message.split('\n')[0]}`); }
      }
      await page.waitForTimeout(1500);
    }
    result.finalURL = page.url();
    await page.screenshot({ path: shot, fullPage: true }).catch((e) => result.details.push(`shot: ${e.message.split('\n')[0]}`));

    const pos = []; for (const m of row.positive) pos.push([m, await evalMarker(page, m)]);
    const neg = []; for (const m of (row.failure || [])) neg.push([m, await evalMarker(page, m)]);
    const allPositive = pos.every(([, ok]) => ok);
    const anyFailure = neg.some(([, hit]) => hit);
    for (const [m, ok] of pos) if (!ok) result.details.push(`missing positive ${m.kind}:${m.v}`);
    for (const [m, hit] of neg) if (hit) result.details.push(`FAILURE marker ${m.kind}:${m.v}`);
    result.status = allPositive && !anyFailure ? 'GREEN' : 'RED';
  } catch (e) {
    result.details.push(`error: ${e.message.split('\n')[0]}`);
  } finally {
    await page.close().catch(() => {});
  }
  return result;
}

// ── main ────────────────────────────────────────────────────────────────
const onlyRows = args.rows ? args.rows.split(',').map((s) => s.trim()) : null;
let rows = ROWS;
if (onlyRows) rows = rows.filter((r) => onlyRows.includes(r.id));

const browser = await chromium.launch({ headless: true, executablePath: resolveChromium() });
// Two contexts: a never-authenticated ANON context (storefront/redeem/wizard)
// and the AUTHED handover-session context (BSS). The authed session is
// established by visiting the handover URL first (row 3376-00).
const anonCtx = await browser.newContext({ ignoreHTTPSErrors: false });
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });

const results = [];
for (const row of rows) {
  const ctx = row.ctx === 'anon' ? anonCtx : authedCtx;
  const res = await probeRow(ctx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : res.status === 'NOT-REACHED' ? 'N/REACH' : 'RED   ';
  console.log(`[${mark}] ${res.id.padEnd(9)} ${(res.ctx).padEnd(6)} ${res.finalURL.padEnd(50)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, results }, null, 2));

const green = results.filter((r) => r.status === 'GREEN');
const red = results.filter((r) => r.status === 'RED');
const notReached = results.filter((r) => r.status === 'NOT-REACHED');
const inScope = green.length + red.length;
console.log(`\n── SURFACES (in-scope, no provision): ${green.length}/${inScope} GREEN${red.length ? `; RED: ${red.map((r) => r.id).join(', ')}` : ''}`);
console.log(`── NOT-REACHED (requires a driven provision — separate walk): ${notReached.length} [${notReached.map((r) => r.id).join(', ')}]`);
process.exit(red.length ? 1 : 0);
