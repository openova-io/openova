#!/usr/bin/env node
// uat-3646-deep-probe.mjs — DEEP live-env browser UAT for runbook #3646
// (one honest /jobs canvas, no fabrication, with remediation).
//
// SIBLING of scripts/uat-console-probe.mjs: that probe owns the #3646
// STRUCTURE rows (3646-01/02/03 — the /jobs route renders, a populated
// canvas, the de-merged header incl. the Kind column) and asserts them
// with a single goto + screenshot per row. THIS probe owns the DEEP
// #3646 rows that need INTERACTION before the assertion holds:
//
//   • search            — type `openbao` into the search box and assert
//                         the table filters to the OpenBao install row
//                         (result-count shrinks, the row link is present).
//   • Kind filter       — assert the Kind <select> renders + offers the
//                         lifecycle kind, and that selecting a kind
//                         re-filters the table. task/cron/reconciler are
//                         reported HONESTLY (GAP when the dropdown does
//                         not offer them on this pin) — never faked green.
//   • Status=failed     — set the Status filter to `failed` and assert
//                         the table shows the genuinely-failing rows.
//   • Re-run + gating   — on the Failed rows assert a per-row Retry
//                         button is PRESENT/enabled, and on Succeeded
//                         rows assert NO Retry button renders (the
//                         control is gated to Failed). The button is
//                         NEVER clicked — clicking it fires a real
//                         …/retry POST (a mutation); this probe asserts
//                         the gating, not the fire.
//
// The machinery (Chromium resolve, arg parse, handover-JWT mint, marker
// eval, per-row screenshot + GREEN/RED) is COPIED VERBATIM from
// uat-console-probe.mjs; the only extension is probeRow runs an optional
// `row.steps` array of browser actions (click / fill / select / waitFor)
// BEFORE evaluating the positive/failure markers.
//
// Selectors/markers are derived from the LIVE console source, not guesses:
//   products/catalyst/bootstrap/ui/src/pages/sovereign/JobsTable.tsx
//     data-testid="jobs-search"          — the search <input type=search>
//     data-testid="jobs-filter-kind"     — the Kind <select> (dynamic opts)
//     data-testid="jobs-filter-status"   — the Status <select> (fixed opts:
//                                          running pending succeeded failed
//                                          healthy degraded failing)
//     data-testid="jobs-result-count"    — "<visible>/<total>" live count
//     data-testid="jobs-table"           — the <table>
//     tr[data-status="failed"]           — a failed row
//     data-testid^="jobs-retry-"         — a per-row Retry button (failed)
//     data-testid^="jobs-cell-actions-empty-" — the "—" gate cell (non-failed)
//   products/catalyst/bootstrap/ui/src/pages/sovereign/RetryJobButton.tsx
//     button[data-testid="jobs-retry-<jobName>"] — present only when
//     isJobRetryable(status) (failed/degraded/failing). The label for an
//     install/lifecycle kind is "Retry reconcile".
//
// Usage:
//   node scripts/uat-3646-deep-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--rows 3646-D01,3646-D05] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all probed rows GREEN, 1 = ≥1 row RED, 2 = harness error.
// Develop/validate selectors against ANY converged console (the mothership
// console.openova.io runs identical catalyst-ui code — "same code in every
// Sovereign"); ACCEPTANCE is the live Sovereign (hw1NN). Requires the
// `playwright` package (resolvable from repo root) + Chromium.

import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import { writeFileSync, mkdirSync, readdirSync, statSync } from 'node:fs';
import crypto from 'node:crypto';

// Resolve a Chromium binary. PROBE_CHROMIUM wins; otherwise (shared
// runners/bastions where the playwright-pinned build is absent but another
// build is installed) pick any installed chromium-*/chrome — undefined lets
// playwright use its own default when nothing else is found.
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
const SHOTS = args.shots || `docs/sessions/${new Date().toISOString().slice(0, 10)}/evidence`;
try { mkdirSync(SHOTS, { recursive: true }); } catch { /* exists */ }
const ENVTAG = FQDN.split('.')[0]; // hw167

// ── handover URL (session bootstrap) — same mint as uat-console-probe ────
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
if (!handoverURL) { console.error('FATAL: provide --handover-url or --jwt-key (+ --deployment-id)'); process.exit(2); }

const JOBS = `${C}/jobs`;

// ── shared login/fabrication failure markers (NONE may hold on an authed row) ─
const LOGIN = [
  { kind: 'text', v: 'Enter the 6-digit' },      // PIN form = FAIL (redirect-to-login)
  { kind: 'selector', v: 'input[type="password"]' },
  { kind: 'text', v: 'Sign in to continue' },
];
const NOTFOUND = [
  { kind: 'text', v: 'HTTP Status 404' },
  { kind: 'text', v: 'Page not found' },
  { kind: 'text', v: 'upstream connect error' },
];

// ── row definitions (id ↔ runbook intent) ───────────────────────────────
// Each: { id, runbook, url, settleMs, steps?, positive:[ALL hold], failure:[NONE hold] }
// steps run AFTER the goto+settle, BEFORE marker eval. Step actions:
//   { action:'fill',     selector, value }   — type into an input
//   { action:'select',   selector, value }   — pick a <select> option value
//   { action:'click',    selector }          — click an element
//   { action:'waitFor',  selector }          — wait for a selector to attach
//   { action:'settle',   value:<ms> }        — extra dwell after an interaction
const SEARCH = '[data-testid="jobs-search"]';
const F_KIND = '[data-testid="jobs-filter-kind"]';
const F_STATUS = '[data-testid="jobs-filter-status"]';
const COUNT = '[data-testid="jobs-result-count"]';
const TABLE = '[data-testid="jobs-table"]';
const RETRY_ANY = '[data-testid^="jobs-retry-"]';                 // a per-row Retry button (failed/degraded/failing)
const ACTIONS_EMPTY_ANY = '[data-testid^="jobs-cell-actions-empty-"]'; // the "—" gate cell (non-retryable rows)

const ROWS = [
  // ── 3646-D00 — toolbar + table render with the de-merged controls ─────
  { id: '3646-D00', runbook: '3646', url: JOBS, settleMs: 7000,
    positive: [
      { kind: 'url-includes', v: '/jobs' },
      { kind: 'selector', v: TABLE },
      { kind: 'selector', v: SEARCH },
      { kind: 'selector', v: F_KIND },
      { kind: 'selector', v: F_STATUS },
      { kind: 'sel-text-regex', v: `${COUNT}|^\\d+/\\d+$` }, // "N/M" live count
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── 3646-D01 — populated canvas: N real rows map to HelmRelease installs ─
  { id: '3646-D01', runbook: '3646', url: JOBS, settleMs: 7000,
    positive: [
      { kind: 'count-gte', v: `${TABLE} tbody tr[data-status]|10` }, // ≥10 real activity rows
      { kind: 'text-regex', v: 'Install |Flux|bootstrap|OpenBao|Cilium' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="jobs-table-empty"]' }] },

  // ── 3646-D02 — Kind filter renders + honestly offers `lifecycle` ──────
  // The Kind <select> options are DYNAMIC (only kinds present render). On
  // a HelmRelease-only pin the dropdown offers exactly All + lifecycle.
  // GREEN = the Kind select exists and offers `lifecycle`. The full option
  // list is logged in details so task/cron/reconciler absence is visible
  // (HONEST GAP, not a fake pass).
  { id: '3646-D02', runbook: '3646', url: JOBS, settleMs: 7000,
    positive: [
      { kind: 'selector', v: F_KIND },
      { kind: 'option-present', v: `${F_KIND}|lifecycle` },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── 3646-D03 — Kind filter INTERACTS: select lifecycle → table re-filters ─
  { id: '3646-D03', runbook: '3646', url: JOBS, settleMs: 7000,
    steps: [
      { action: 'waitFor', selector: F_KIND },
      { action: 'select', selector: F_KIND, value: 'lifecycle' },
      { action: 'settle', value: 1500 },
    ],
    positive: [
      { kind: 'count-gte', v: `${TABLE} tbody tr[data-status]|1` }, // lifecycle rows survive the filter
      // every visible Kind chip reads `lifecycle` once the filter is applied
      { kind: 'sel-text-regex', v: `${TABLE} tbody tr:first-child [data-kind]|^lifecycle$` },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── 3646-D04 — search `openbao` → table filters to the OpenBao install ─
  { id: '3646-D04', runbook: '3646', url: JOBS, settleMs: 7000,
    steps: [
      { action: 'waitFor', selector: SEARCH },
      { action: 'fill', selector: SEARCH, value: 'openbao' },
      { action: 'settle', value: 1500 },
    ],
    positive: [
      { kind: 'text-regex', v: 'OpenBao|openbao' },            // the OpenBao row survives
      { kind: 'count-gte', v: `${TABLE} tbody tr[data-status]|1` },
      // the result count shrinks to a small N/M (single-digit visible)
      { kind: 'sel-text-regex', v: `${COUNT}|^[0-9]/\\d+$` },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="jobs-table-empty"]' }] },

  // ── 3646-D05 — Status=failed → genuinely-failing rows + Retry PRESENT ──
  { id: '3646-D05', runbook: '3646', url: JOBS, settleMs: 7000,
    steps: [
      { action: 'waitFor', selector: F_STATUS },
      { action: 'select', selector: F_STATUS, value: 'failed' },
      { action: 'settle', value: 1500 },
    ],
    positive: [
      { kind: 'count-gte', v: `${TABLE} tbody tr[data-status="failed"]|1` }, // ≥1 honest failed row
      { kind: 'count-gte', v: `${RETRY_ANY}|1` },                            // ≥1 per-row Retry button
      { kind: 'text-regex', v: 'Retry reconcile|Re-run|Run now|Re-submit' }, // the gated label renders
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── 3646-D06 — gating proof: Status=failed rows show ONLY Retry, no "—" ─
  // With the table scoped to failed rows, every visible row is retryable,
  // so a Retry button is present and the "—" gate cell is ABSENT.
  { id: '3646-D06', runbook: '3646', url: JOBS, settleMs: 7000,
    steps: [
      { action: 'waitFor', selector: F_STATUS },
      { action: 'select', selector: F_STATUS, value: 'failed' },
      { action: 'settle', value: 1500 },
    ],
    positive: [
      { kind: 'count-gte', v: `${RETRY_ANY}|1` },        // Retry present on the failed view
      { kind: 'count-eq', v: `${ACTIONS_EMPTY_ANY}|0` }, // NO "—" gate cell among failed rows
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── 3646-D07 — gating proof (inverse): Status=succeeded → NO Retry ────
  // A green row has nothing to re-drive; the control is gated OFF and the
  // "—" placeholder renders instead. This is the half that proves the
  // button is GATED, not always-on.
  { id: '3646-D07', runbook: '3646', url: JOBS, settleMs: 7000,
    steps: [
      { action: 'waitFor', selector: F_STATUS },
      { action: 'select', selector: F_STATUS, value: 'succeeded' },
      { action: 'settle', value: 1500 },
    ],
    positive: [
      { kind: 'count-gte', v: `${TABLE} tbody tr[data-status="succeeded"]|1` }, // ≥1 succeeded row present
      { kind: 'count-eq', v: `${RETRY_ANY}|0` },          // NO Retry button on green rows
      { kind: 'count-gte', v: `${ACTIONS_EMPTY_ANY}|1` }, // the "—" gate cell renders instead
    ],
    failure: [...LOGIN, ...NOTFOUND] },
];

// ── marker evaluation (from uat-console-probe.mjs + interaction-aware kinds) ─
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'count-gte': { const [sel, n] = splitLast(m.v); return (await page.locator(sel).count()) >= Number(n); }
    case 'count-eq': { const [sel, n] = splitLast(m.v); return (await page.locator(sel).count()) === Number(n); }
    case 'text': { const b = await page.textContent('body').catch(() => '') || ''; return b.includes(m.v); }
    case 'text-regex': { const b = await page.textContent('body').catch(() => '') || ''; return new RegExp(m.v).test(b); }
    case 'title-regex': return new RegExp(m.v).test(await page.title().catch(() => ''));
    // sel-text-regex: "<selector>|<regex>" — the FIRST match's textContent
    // (trimmed) matches the regex. Used to read the live "N/M" result count
    // and the first row's Kind chip after a filter interaction.
    case 'sel-text-regex': {
      const [sel, re] = splitLast(m.v);
      const t = (await page.locator(sel).first().textContent().catch(() => '') || '').trim();
      return new RegExp(re).test(t);
    }
    // option-present: "<select-selector>|<value>" — true when the <select>
    // offers an <option> with that value. Drives the honest Kind-offering
    // assertion (lifecycle present; task/cron absence is logged, not faked).
    case 'option-present': {
      const [sel, val] = splitLast(m.v);
      const n = await page.locator(`${sel} option[value="${val}"]`).count().catch(() => 0);
      return n > 0;
    }
    default: throw new Error(`unknown marker kind ${m.kind}`);
  }
}

// Split a "<selector>|<arg>" marker value on the LAST `|` so a selector
// that itself contains `|` (none here, but defensive) stays intact.
function splitLast(s) {
  const i = s.lastIndexOf('|');
  return [s.slice(0, i), s.slice(i + 1)];
}

// Run one interaction step. Best-effort: a failed step is recorded in
// details and the assertion then decides RED (a missing control surfaces
// as a missing positive, not a silent pass).
async function runStep(page, step, result) {
  try {
    switch (step.action) {
      case 'waitFor':
        await page.locator(step.selector).first().waitFor({ state: 'attached', timeout: 12000 });
        break;
      case 'fill': {
        const el = page.locator(step.selector).first();
        await el.waitFor({ state: 'visible', timeout: 12000 });
        await el.fill(step.value);
        break;
      }
      case 'select': {
        const el = page.locator(step.selector).first();
        await el.waitFor({ state: 'attached', timeout: 12000 });
        await el.selectOption(step.value);
        break;
      }
      case 'click': {
        const el = page.locator(step.selector).first();
        await el.waitFor({ state: 'visible', timeout: 12000 });
        await el.click();
        break;
      }
      case 'settle':
        await page.waitForTimeout(Number(step.value) || 1000);
        break;
      default:
        result.details.push(`step: unknown action ${step.action}`);
    }
  } catch (e) {
    result.details.push(`step ${step.action}(${step.selector ?? step.value}): ${e.message.split('\n')[0]}`);
  }
}

async function probeRow(ctx, row) {
  const page = await ctx.newPage();
  const shot = `${SHOTS}/${ENVTAG}-${row.id}.png`;
  const result = { id: row.id, runbook: row.runbook, status: 'RED', finalURL: '', shot, details: [] };
  try {
    await page.goto(row.url, { waitUntil: 'load', timeout: 45000 }).catch((e) => result.details.push(`goto: ${e.message.split('\n')[0]}`));
    await page.waitForTimeout(row.settleMs);
    await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});

    // Interaction steps run BEFORE the assertion (the #3646 deep rows need
    // the table filtered/searched first), then the screenshot captures the
    // POST-interaction state the markers assert against.
    if (row.steps) for (const step of row.steps) await runStep(page, step, result);

    result.finalURL = page.url();
    await page.screenshot({ path: shot, fullPage: true }).catch((e) => result.details.push(`shot: ${e.message.split('\n')[0]}`));

    const pos = []; for (const m of row.positive) pos.push([m, await evalMarker(page, m)]);
    const neg = []; for (const m of row.failure) neg.push([m, await evalMarker(page, m)]);
    const allPositive = pos.every(([, ok]) => ok);
    const anyFailure = neg.some(([, hit]) => hit);
    for (const [m, ok] of pos) if (!ok) result.details.push(`missing positive ${m.kind}:${m.v}`);
    for (const [m, hit] of neg) if (hit) result.details.push(`FAILURE marker ${m.kind}:${m.v}`);
    result.status = allPositive && !anyFailure ? 'GREEN' : 'RED';

    // Diagnostic breadcrumbs (logged regardless of GREEN/RED) — the live
    // result count + the Kind dropdown's full option set make GAPs honest.
    const cnt = (await page.locator(COUNT).first().textContent().catch(() => '') || '').trim();
    if (cnt) result.details.push(`count=${cnt}`);
    const kinds = await page.locator(`${F_KIND} option`).allTextContents().catch(() => []);
    if (kinds.length) result.details.push(`kindOpts=[${kinds.map((k) => k.trim()).join(',')}]`);
    const retryN = await page.locator(RETRY_ANY).count().catch(() => 0);
    result.details.push(`retryBtns=${retryN}`);
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
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });

// Bootstrap the zero-click session ONCE (the handover sets the session
// cookie on the context); every /jobs row then reuses it — same pattern as
// uat-console-probe.mjs's authed context.
const boot = await authedCtx.newPage();
await boot.goto(handoverURL, { waitUntil: 'load', timeout: 45000 }).catch((e) => console.error(`handover goto: ${e.message.split('\n')[0]}`));
await boot.waitForTimeout(6000);
await boot.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
const bootURL = boot.url();
console.log(`[boot ] handover → ${bootURL}`);
await boot.close().catch(() => {});

const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.id.padEnd(9)} ${res.finalURL.padEnd(40)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, bootURL, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
