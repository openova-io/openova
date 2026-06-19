#!/usr/bin/env node
// uat-console-probe.mjs — live-env browser UAT for the console-structure
// runbooks (#3687 object-model · #3383 organizations-naming · #3646
// jobs-canvas · #3668 catalog-IaC · #3375 topology vocabulary).
//
// SIBLING of scripts/sso-zero-click-probe.mjs: that probe owns the #3374
// SSO landing matrix (every app's bare URL → signed-in). THIS probe owns
// the console's own surfaces — the operator opens the handover URL once
// (zero-click sovereign-admin session) and every console route must render
// the asserted screen. Each row captures a SCREENSHOT (the only acceptance
// evidence the founder accepts) AND asserts a positive landing marker + the
// ABSENCE of login / 404 / fabrication markers.
//
// WHY a probe and not the tests/e2e/playwright suite: that suite runs the
// Astro dev server against a MOCK_API fixture (MOCK_API=1, 127.0.0.1) — a
// UI smoke test, NOT the live-Sovereign UAT. The founder's metric is the
// 10-runbook browser walk on a FRESH prov; this probe automates it so the
// ~200 rows are re-runnable per env instead of hand-walked.
//
// ENV-INDEPENDENCE: the rows here assert the CONVERGED-CONSOLE STRUCTURE
// (sidebar, catalog grid, apps list, jobs canvas, organizations directory,
// topology vocabulary, IaC-editor affordance) — true on any converged prov,
// no funnel run required. The customer-Org rows (Acme exists / WordPress
// serves) belong to the #3376 funnel FLOW probe (stateful, ordered) and are
// intentionally NOT here. Naming rows (#3383) assert the ABSENCE of the
// banned "tenant"/"SME" persona words in the live UI.
//
// Usage:
//   node scripts/uat-console-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id <id> \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--runbook 3687,3646] [--rows 3687-02,3646-03] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all probed rows GREEN, 1 = ≥1 row RED, 2 = harness error.
// Selectors/markers are derived from docs/ledger/UAT.md row descriptions +
// the console's own data-testids exercised by the recorded walk — not guesses.
//
// Develop/validate selectors against ANY converged console (the mothership
// console.openova.io runs identical catalyst-ui/api code — "same code in
// every Sovereign"); ACCEPTANCE is the live Sovereign (hw1NN). Requires the
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

// ── handover URL (session bootstrap) — same mint as sso-zero-click-probe ─
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

// ── row definitions (id ↔ docs/ledger/UAT.md matrix row) ────────────────
// Each: { id, runbook, url, settleMs, positive:[ALL must hold], failure:[NONE may hold] }
const ROWS = [
  // ── #3687 object-model — console structure (env-independent) ──────────
  { id: '3687-01', runbook: '3687', url: handoverURL, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/dashboard' }, { kind: 'text', v: 'Dashboard' }],
    failure: [...LOGIN] },
  { id: '3687-02', runbook: '3687', url: `${C}/dashboard`, settleMs: 4000,
    positive: [{ kind: 'text', v: 'Apps' }, { kind: 'text', v: 'Catalog' }, { kind: 'text', v: 'Jobs' }, { kind: 'text', v: 'Organizations' }, { kind: 'text', v: 'Settings' }],
    failure: [...LOGIN] },
  { id: '3687-26', runbook: '3687', url: `${C}/dashboard`, settleMs: 5000,
    // Treemap Layer-1 default = Cluster; selector offers Sovereign/Region/Cluster/vCluster
    positive: [{ kind: 'text-regex', v: 'Cluster' }, { kind: 'text-regex', v: 'vCluster|Region|Sovereign' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3687-20', runbook: '3687', url: `${C}/catalog`, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/catalog' }, { kind: 'text-regex', v: 'Catalog|Blueprint' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3687-22', runbook: '3687', url: `${C}/apps`, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/apps' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3687-35', runbook: '3687', url: `${C}/organizations`, settleMs: 5000,
    positive: [{ kind: 'url-includes', v: '/organizations' }, { kind: 'text', v: 'Organizations' }],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── #3383 organizations — NO "tenant"/"SME" persona words ─────────────
  { id: '3383-01', runbook: '3383', url: `${C}/organizations`, settleMs: 5000,
    positive: [{ kind: 'text', v: 'Organizations' }],
    failure: [...LOGIN, { kind: 'text', v: 'SME tenant' }, { kind: 'text', v: 'Tenants' }, { kind: 'text', v: 'Onboard tenant' }] },
  { id: '3383-03', runbook: '3383', url: `${C}/dashboard`, settleMs: 4000,
    // left-nav section label must read "Organizations", never "Tenants"
    positive: [{ kind: 'text', v: 'Organizations' }],
    failure: [...LOGIN, { kind: 'text', v: 'Tenants' }] },
  { id: '3383-07', runbook: '3383', url: `${C}/bss/tenants`, settleMs: 5000,
    // legacy alias (PR #3390) must resolve + redirect to /organizations
    positive: [{ kind: 'url-includes', v: '/organizations' }],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── #3646 jobs-canvas — one honest canvas, Kind column, no fabrication ─
  { id: '3646-01', runbook: '3646', url: `${C}/jobs`, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/jobs' }],
    failure: [...LOGIN] },
  { id: '3646-02', runbook: '3646', url: `${C}/jobs`, settleMs: 6000,
    // populated canvas: real lifecycle installs render (Install <X> rows)
    positive: [{ kind: 'text-regex', v: 'Install |Flux|bootstrap' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3646-03', runbook: '3646', url: `${C}/jobs`, settleMs: 6000,
    // full header incl. Kind column (de-merged JobsPage)
    positive: [{ kind: 'text', v: 'Kind' }, { kind: 'text', v: 'Status' }, { kind: 'text', v: 'Started' }],
    failure: [...LOGIN] },

  // ── #3668 catalog-IaC — single-source editor, not a read-time skin ────
  { id: '3668-02', runbook: '3668', url: `${C}/catalog`, settleMs: 6000,
    positive: [{ kind: 'text-regex', v: 'Catalog|Blueprint' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3668-03', runbook: '3668', url: `${C}/catalog/grafana`, settleMs: 6000,
    // blueprint detail renders the Edit-IaC affordance (single-source CR edit)
    positive: [{ kind: 'text-regex', v: 'Edit IaC|Grafana' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3668-30', runbook: '3668', url: `${C}/catalog/postgres`, settleMs: 6000,
    positive: [{ kind: 'text-regex', v: 'Edit IaC|Postgres|PostgreSQL' }],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── #3375 topology vocabulary (one DR vocabulary in the UI) ───────────
  { id: '3375-17', runbook: '3375', url: `${C}/apps`, settleMs: 6000,
    // the apps grid renders the platform cards (entry point to Topology tabs)
    positive: [{ kind: 'url-includes', v: '/apps' }],
    failure: [...LOGIN, ...NOTFOUND] },
  { id: '3375-22', runbook: '3375', url: `${C}/settings`, settleMs: 5000,
    // Settings page renders (Organization/Sovereign/API-tokens sections)
    positive: [{ kind: 'url-includes', v: '/settings' }, { kind: 'text-regex', v: 'Sovereign|Organization|API' }],
    failure: [...LOGIN, ...NOTFOUND] },
];

// ── marker evaluation (from sso-zero-click-probe.mjs + count-gte) ───────
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'count-gte': { const [sel, n] = m.v.split('|'); return (await page.locator(sel).count()) >= Number(n); }
    case 'text': { const b = await page.textContent('body').catch(() => '') || ''; return b.includes(m.v); }
    case 'text-regex': { const b = await page.textContent('body').catch(() => '') || ''; return new RegExp(m.v).test(b); }
    case 'title-regex': return new RegExp(m.v).test(await page.title().catch(() => ''));
    default: throw new Error(`unknown marker kind ${m.kind}`);
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
    result.finalURL = page.url();
    await page.screenshot({ path: shot, fullPage: true }).catch((e) => result.details.push(`shot: ${e.message.split('\n')[0]}`));

    const pos = []; for (const m of row.positive) pos.push([m, await evalMarker(page, m)]);
    const neg = []; for (const m of row.failure) neg.push([m, await evalMarker(page, m)]);
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
const onlyRunbooks = args.runbook ? args.runbook.split(',').map((s) => s.trim()) : null;
const onlyRows = args.rows ? args.rows.split(',').map((s) => s.trim()) : null;
let rows = ROWS;
if (onlyRunbooks) rows = rows.filter((r) => onlyRunbooks.includes(r.runbook));
if (onlyRows) rows = rows.filter((r) => onlyRows.includes(r.id));

const browser = await chromium.launch({ headless: true, executablePath: resolveChromium() });
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });
const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.id.padEnd(9)} ${res.finalURL.padEnd(48)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
