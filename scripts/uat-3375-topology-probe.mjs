#!/usr/bin/env node
// uat-3375-topology-probe.mjs — live-env browser UAT for the #3375
// TOPOLOGY/DR one-vocabulary runbook (the app-detail Topology tab + the
// canonical placement/DR vocabulary + the per-region 2-region map).
//
// SIBLING of scripts/uat-console-probe.mjs: that probe owns the console
// STRUCTURE rows for #3375 (3375-17 apps-grid entry, 3375-22 Settings) and
// the #3687/#3383/#3646/#3668 structure runbooks. THIS probe drills into the
// per-app **Topology tab** surfaces that #3375 is actually about — the
// canonical one-vocabulary placement editor (`singleton` / `active-passive`
// / `active-hot-standby` / `active-active`), the declared-topology strip, the
// Continuum DR read-back, the Switchover control, and the cloud 2-region map.
// Machinery (mintHandoverURL, resolveChromium, evalMarker, probeRow with
// per-row screenshots, the main loop) is copied verbatim from
// uat-console-probe.mjs; the ADDITION here is an optional `row.steps` array
// (click / waitFor) so a row can navigate into a tab before asserting.
//
// HONEST CONTEXT (founder rule "absent feature = FAILED, never carry a stale
// ✅"): the markers below are derived from the LIVE hw167 DOM (build pinned
// on that Sovereign), not from this branch's source tree — the Topology-tab
// strings ("Declared topology", "Change placement", "Replication lag", the
// `topology-editor-*` testids) are rendered by a NEWER catalyst-ui build that
// is deployed on hw167 but not yet in `core/console/src`. So every selector
// here was validated against the running console, not guessed.
//
//   * hw167 is single-PHYSICAL-region (2-VPC mimic me-east-215-a / -b) and
//     bp-continuum is currently False/oscillating, so the RUNTIME DR state
//     (live primary/replica, lag) reads "n/a — bootstrap component". Rows
//     that assert the *vocabulary + editor surfaces* are GREEN; rows that
//     assert a *live* primary/replica/lag/armed-Switchover are RED-by-design
//     (honest gaps, not faked green). The split is annotated per row.
//   * The region-kill + Switchover EXECUTION are destructive operator walks
//     — this probe NEVER performs them. It asserts the controls are PRESENT
//     (or honestly ABSENT for a singleton / no-pair app) only.
//
// Usage:
//   node scripts/uat-3375-topology-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--rows 3375-07,3375-13] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all probed rows GREEN, 1 = ≥1 row RED, 2 = harness error.
// PROBE_CHROMIUM wins for the Chromium binary; otherwise any installed
// chromium-*/chrome build under ~/.cache/ms-playwright is used. Requires the
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

// ── handover URL (session bootstrap) — same mint as uat-console-probe ───
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

// The four canonical topology spellings — the single vocabulary #3375 enforces.
// `active-hotstandby` / `single-region` are the BANNED editor dialects; if any
// of those leak onto a rendered surface the row must catch it as a failure.
const BANNED_VOCAB = [
  { kind: 'text', v: 'active-hotstandby' },
  { kind: 'text', v: 'single-region' },
];

// Steps to open the Topology tab on an app-detail page (validated on hw167:
// the tab strip renders a button data-testid="app-tab-topology"; the panel
// data-testid="app-tab-topology-panel" mounts on click).
const OPEN_TOPOLOGY = [
  { action: 'click', selector: '[data-testid="app-tab-topology"]' },
  { action: 'waitFor', selector: '[data-testid="app-tab-topology-panel"]', timeout: 8000 },
];

// ── row definitions (id ↔ docs/ledger/UAT.md 3375-NN matrix row) ────────
// Each: { id, runbook:'3375', url, settleMs, steps?:[…], positive:[ALL must
// hold], failure:[NONE may hold], note }. `note` records the honest GREEN-vs-RED
// rationale so the verdict is self-documenting in the run log.
const ROWS = [
  // ── Section A — ONE topology vocabulary (catalog new-instance picker) ──
  // 3375-01/04: the catalog bp-postgres detail renders + offers a create
  // path. (The create DIALOG's <select> completeness — all 4 modes — is the
  // #3376 funnel-flow's mutation concern; here we assert the catalog detail
  // surface + the canonical "Supported topologies" key renders the vocabulary.)
  { id: '3375-01', runbook: '3375', url: `${C}/catalog/bp-postgres`, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/catalog/bp-postgres' }, { kind: 'text-regex', v: 'Postgres|PostgreSQL' }],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'GREEN-expected: catalog bp-postgres detail renders (entry point to the New-instance topology picker).' },
  { id: '3375-04', runbook: '3375', url: `${C}/catalog/bp-postgres`, settleMs: 6000,
    // The canonical 4-spelling vocabulary appears in the "Supported topologies"
    // key on the blueprint detail — assert all four canonical spellings render
    // and the banned dialects do NOT.
    positive: [
      { kind: 'text', v: 'singleton' },
      { kind: 'text', v: 'active-hot-standby' },
      { kind: 'text', v: 'active-passive' },
      { kind: 'text', v: 'active-active' },
    ],
    failure: [...LOGIN, ...NOTFOUND, ...BANNED_VOCAB],
    note: 'GREEN-expected: all 4 canonical spellings render on the bp-postgres detail; banned active-hotstandby/single-region absent.' },

  // ── Section A/B/C — shared-pg Topology tab: the canonical placement editor ─
  // 3375-07: open the shared-pg Topology tab → the "Change placement" editor
  // renders with ALL FOUR canonical mode radios (one vocabulary) + both region
  // checkboxes. This is the core "one DR vocabulary in the UI" assertion.
  { id: '3375-07', runbook: '3375', url: `${C}/app/shared-pg`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'text', v: 'Change placement' },
      { kind: 'selector', v: '[data-testid="topology-editor-mode-singleton"]' },
      { kind: 'selector', v: '[data-testid="topology-editor-mode-active-active"]' },
      { kind: 'selector', v: '[data-testid="topology-editor-mode-active-hot-standby"]' },
      { kind: 'selector', v: '[data-testid="topology-editor-mode-active-passive"]' },
      { kind: 'count-gte', v: '[data-testid$="-checkbox"]|2' }, // both region checkboxes
    ],
    failure: [...LOGIN, ...NOTFOUND, ...BANNED_VOCAB],
    note: 'GREEN-expected: the Change-placement editor renders all 4 canonical mode radios + 2 region checkboxes in ONE vocabulary.' },

  // 3375-10: the declared-topology strip renders the canonical mode for
  // shared-pg. (shared-pg declares `singleton` — the strip renders the
  // canonical spelling. The cross-surface VALUE mismatch vs the Overview
  // PLACEMENT badge is the live-defect captured in the runbook; this probe
  // asserts the strip + canonical-vocab RENDER. We do NOT assert the value
  // agrees — that is a known RED runtime defect tracked in the walkthrough.)
  { id: '3375-10', runbook: '3375', url: `${C}/app/shared-pg`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'text', v: 'Declared topology' },
      { kind: 'selector', v: '[data-testid="topology-tab-declared-class"]' },
      { kind: 'text', v: 'singleton' },
    ],
    failure: [...LOGIN, ...NOTFOUND, ...BANNED_VOCAB],
    note: 'GREEN-expected: declared-topology strip renders canonical "singleton" (cross-surface VALUE mismatch vs Overview is a separate known RED in the runbook).' },

  // 3375-13: Switchover on shared-pg. The runbook asserts it should be
  // "present and armed". On hw167 there is NO live Continuum for shared-pg
  // (bootstrap HelmRelease, no Application CR), so NO Switchover button
  // renders — RED-BY-DESIGN. This row's positive (an armed Switchover) is
  // genuinely unmet; left RED honestly.
  { id: '3375-13', runbook: '3375', url: `${C}/app/shared-pg`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [{ kind: 'selector', v: 'button:has-text("Switchover")' }],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'RED-by-design (honest): shared-pg has NO live Continuum (bootstrap HR, no Application CR) → no armed Switchover button. The runbook asserts armed; genuinely unmet, left RED.' },

  // 3375-14: a SINGLETON app (cilium) honestly HIDES the Switchover (no
  // cross-region failover for a singleton). The positive here is the
  // ABSENCE of any Switchover button + the canonical singleton strip — a
  // genuine GREEN (honest hiding, not armed against a phantom region).
  { id: '3375-14', runbook: '3375', url: `${C}/app/bp-cilium`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'selector', v: '[data-testid="topology-tab-declared-class"]' },
      { kind: 'text', v: 'singleton' },
      { kind: 'count-eq', v: 'button:has-text("Switchover")|0' }, // honestly hidden
    ],
    failure: [...LOGIN, ...NOTFOUND, ...BANNED_VOCAB],
    note: 'GREEN-expected: cilium is singleton → Switchover honestly HIDDEN (0 buttons), declared-topology strip canonical.' },

  // ── Section E — grafana DR honesty (declared hot-standby, no live pair) ──
  // 3375-18: grafana declares active-hot-standby and is OBSERVED live, with
  // an honest "No live DR pair" strip + an Effective class that admits the
  // mandate is unbuilt (DEGRADED). Assert the canonical declared mode + the
  // honest no-pair read-back render — a genuine GREEN (honesty satisfied).
  { id: '3375-18', runbook: '3375', url: `${C}/app/bp-grafana`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'text', v: 'Declared topology' },
      { kind: 'text', v: 'active-hot-standby' },
      { kind: 'selector', v: '[data-testid="topology-tab-observed-strip"]' },
      { kind: 'text-regex', v: 'No live DR pair|no live (Continuum|pair)' },
    ],
    failure: [...LOGIN, ...NOTFOUND, ...BANNED_VOCAB],
    note: 'GREEN-expected: grafana declares canonical active-hot-standby, honest OBSERVED no-pair strip renders (no faked green DR).' },

  // 3375-19: grafana's Switchover honesty — the no-pair state surfaces the
  // dedicated continuum-dr-switchover-no-pair marker and NO armed/enabled
  // Switchover button (never an armed button that 404s). GREEN = the no-pair
  // marker present AND zero enabled Switchover buttons.
  { id: '3375-19', runbook: '3375', url: `${C}/app/bp-grafana`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'selector', v: '[data-testid="continuum-dr-switchover-no-pair"]' },
      { kind: 'count-eq', v: 'button:has-text("Switchover"):not([disabled])|0' },
    ],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'GREEN-expected: grafana no-pair Switchover honesty marker present + zero ENABLED Switchover buttons (no phantom-armed control).' },

  // 3375-20: replication-lag field on shared-pg. The runbook asserts a live
  // numeric seconds value (or explicit "no replica"); hw167 renders the
  // mode-derived placeholder "n/a (mode)". The field RENDERS (testid present)
  // but the VALUE is the hardcoded-placeholder this row guards against →
  // RED-BY-DESIGN (the placeholder is the failure). We assert the field is
  // present AND that it is NOT a live numeric — i.e. the row stays RED because
  // the live value is absent.
  { id: '3375-20', runbook: '3375', url: `${C}/app/shared-pg`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'selector', v: '[data-testid="topology-tab-replication-lag"]' },
      { kind: 'text-regex', v: 'Replication lag[^0-9]*[0-9]+\\s*(s|sec|ms)|no replica' }, // a LIVE value
    ],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'RED-by-design (honest): the replication-lag field renders but reads the placeholder "n/a (mode)" (no live seconds / no-replica) — exactly the hardcoded-placeholder failure this row guards. Left RED.' },

  // ── Section F — integrity: the cloud 2-region map is honest (no phantom) ─
  // 3375-21: the cloud/regions view renders the TRUE 2-region topology —
  // both me-east-215-a and me-east-215-b present with a 2/2 cluster count.
  { id: '3375-21', runbook: '3375', url: `${C}/cloud`, settleMs: 7000,
    positive: [
      { kind: 'url-includes', v: '/cloud' },
      { kind: 'text', v: 'me-east-215-a' },
      { kind: 'text', v: 'me-east-215-b' },
      { kind: 'text-regex', v: '2\\s*/\\s*2|Region 2|2 regions' },
    ],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'GREEN-expected: cloud view renders the true 2-region map (me-east-215-a + -b, 2/2) — no phantom region.' },

  // ── Section H — region-kill / Switchover controls PRESENT only (no run) ──
  // 3375-29: the shared-pg Topology baseline that a region-kill would anchor
  // on. The runbook asserts a LIVE Continuum (Ready / lease holder / standby /
  // lag) baseline. hw167's shared-pg reads "n/a — bootstrap component", so the
  // live baseline is genuinely absent → RED-BY-DESIGN. We assert the status
  // panel RENDERS (the surface exists) AND a live Continuum baseline (Ready +
  // lease) — the latter is unmet, so the row stays honestly RED.
  { id: '3375-29', runbook: '3375', url: `${C}/app/shared-pg`, settleMs: 5000, steps: OPEN_TOPOLOGY,
    positive: [
      { kind: 'selector', v: '[data-testid="topology-tab-status-panel"]' },
      { kind: 'text-regex', v: 'Ready|lease|primary.*region|region.*primary' }, // a LIVE Continuum baseline
      { kind: 'text-regex', v: 'Replication lag[^0-9]*[0-9]+' }, // a live lag number
    ],
    failure: [...LOGIN, ...NOTFOUND],
    note: 'RED-by-design (honest): the Topology status panel renders, but shared-pg shows no live Continuum baseline (n/a — bootstrap HR), so the region-kill anchor (Ready/lease/lag) is absent. Kill itself is a destructive operator walk — NOT performed. Left RED.' },
];

// ── marker evaluation (from uat-console-probe.mjs + count-eq) ───────────
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'count-gte': { const [sel, n] = m.v.split('|'); return (await page.locator(sel).count()) >= Number(n); }
    case 'count-eq': { const [sel, n] = m.v.split('|'); return (await page.locator(sel).count()) === Number(n); }
    case 'text': { const b = await page.textContent('body').catch(() => '') || ''; return b.includes(m.v); }
    case 'text-regex': { const b = await page.textContent('body').catch(() => '') || ''; return new RegExp(m.v).test(b); }
    case 'title-regex': return new RegExp(m.v).test(await page.title().catch(() => ''));
    default: throw new Error(`unknown marker kind ${m.kind}`);
  }
}

// Run a row's optional navigation steps (click a tab, wait for a panel) before
// the markers are evaluated. Failures are recorded but non-fatal — the marker
// pass that follows is the real verdict (a missing panel surfaces as a missing
// positive selector).
async function runSteps(page, row, result) {
  for (const s of row.steps ?? []) {
    try {
      if (s.action === 'click') {
        await page.locator(s.selector).first().click({ timeout: s.timeout ?? 8000 });
      } else if (s.action === 'waitFor') {
        await page.locator(s.selector).first().waitFor({ state: 'visible', timeout: s.timeout ?? 8000 });
      }
    } catch (e) {
      result.details.push(`step ${s.action} ${s.selector}: ${e.message.split('\n')[0]}`);
    }
  }
  if ((row.steps ?? []).length) await page.waitForTimeout(1500);
}

async function probeRow(ctx, row) {
  const page = await ctx.newPage();
  const shot = `${SHOTS}/${ENVTAG}-${row.id}.png`;
  const result = { id: row.id, runbook: row.runbook, status: 'RED', finalURL: '', shot, note: row.note ?? '', details: [] };
  try {
    await page.goto(row.url, { waitUntil: 'load', timeout: 45000 }).catch((e) => result.details.push(`goto: ${e.message.split('\n')[0]}`));
    await page.waitForTimeout(row.settleMs);
    await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
    await runSteps(page, row, result);
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
const onlyRows = args.rows ? args.rows.split(',').map((s) => s.trim()) : null;
let rows = ROWS;
if (onlyRows) rows = rows.filter((r) => onlyRows.includes(r.id));

const browser = await chromium.launch({ headless: true, executablePath: resolveChromium() });
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });
// Bootstrap the zero-click session once (handover URL → /dashboard) so every
// subsequent app-detail row reuses the authed context cookies.
{
  const boot = await authedCtx.newPage();
  await boot.goto(handoverURL, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await boot.waitForTimeout(6000);
  console.log(`[boot ] handover → ${boot.url()}`);
  await boot.close().catch(() => {});
}
const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.id.padEnd(9)} ${res.finalURL.padEnd(46)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
console.log('Note: RED-by-design rows (3375-13/20/29 shared-pg runtime DR · grafana not these) are HONEST gaps —');
console.log('shared-pg runs as a bootstrap HelmRelease with no Application CR, so live primary/replica/lag/Switchover are n/a on hw167.');
process.exit(red.length ? 1 : 0);
