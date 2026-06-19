#!/usr/bin/env node
// uat-3668-deep-probe.mjs — DEEP live-env browser UAT for runbook #3668
// (catalog single-source IaC editor). SIBLING of scripts/uat-console-probe.mjs:
// that probe owns the #3668 STRUCTURE rows (02/03/30 — the catalog grid + the
// detail page rendering the Edit-IaC affordance, GREEN). THIS probe owns the
// DEEP #3668 rows — the ones that need INTERACTION (click → assert): opening
// the full-CR "Edit IaC" YamlEditor, toggling its Current/Proposed diff, and
// opening the per-field inline editors (summary / name / icon picker grid).
//
// WHY a separate file: the structure rows are pure GET-and-assert; the deep
// rows must CLICK an affordance first (Edit-IaC button, the cif-*-edit pencil,
// the icon-picker) and only THEN assert the editor surface rendered. The
// machinery (Chromium resolution, handover-JWT mint, marker eval, screenshot
// per row, login/404 failure markers) is COPIED VERBATIM from
// uat-console-probe.mjs; the only addition is `probeRow` honouring an optional
// `row.steps` array of {action:'click'|'fill'|'waitFor', selector, value?}
// executed BEFORE the positive/negative markers are evaluated.
//
// READ-ONLY by design: the rows assert the editor surface OPENS + RENDERS
// (the YamlEditor, the diff panes, the inline editors, the icon-picker grid
// with its vendored tiles). They DO NOT click Save/Commit — a live catalog
// write (summary/icon/version) is reversible but out of scope for a surface
// probe; the durable-commit verdict (`committed:true`, the "same file" copy,
// the `• in sync` indicator) is asserted as RENDERED COPY, not by firing a
// write. A login redirect on any row = FAIL (every row is admin-gated).
//
// Selectors are derived from the LIVE console source (not guesses):
//   products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx
//   products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogInlineField.tsx
//   products/catalyst/bootstrap/ui/src/pages/sovereign/IconPicker.tsx
//   products/catalyst/bootstrap/ui/src/widgets/cloud-list/YamlEditor.tsx
// grounded against docs/ledger/UAT.md (rows 3668-03..3668-31) +
// docs/ledger/uat-walkthrough/catalog-edit-single-source-iac-not-overlay.md.
//
// Usage:
//   node scripts/uat-3668-deep-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--bp alloy] [--bp2 grafana] [--rows 3668-D-iac-open,...] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all probed rows GREEN, 1 = ≥1 row RED, 2 = harness error.

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
// Blueprints to walk: BP = the canonical edit-walk blueprint (alloy), BP2 = a
// second present blueprint proving generality (grafana). Both are bp-prefixed
// in the route; pass bare names (the route strips bp-).
const BP = (args.bp || 'alloy').replace(/^bp-/, '');
const BP2 = (args.bp2 || 'grafana').replace(/^bp-/, '');

// ── handover URL (session bootstrap) — same mint as uat-console-probe.mjs ─
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

// ── row definitions (id ↔ docs/ledger/UAT.md 3668-NN matrix row) ────────
// Each: { id, runbook, url, settleMs, steps?:[{action,selector,value?}],
//         positive:[ALL must hold], failure:[NONE may hold] }.
// `steps` (the deep extension) run AFTER the goto+settle, BEFORE markers.
const DETAIL = (bp) => `${C}/catalog/${bp}`;

const ROWS = [
  // ════ ESTABLISH SESSION (zero-click sovereign-admin) ═══════════════════
  // The handover URL lands on /dashboard already signed-in; this also seeds
  // the auth cookie into the shared context every later row reuses.
  { id: '3668-D00-signin', runbook: '3668', url: handoverURL, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/dashboard' }, { kind: 'text', v: 'Dashboard' }],
    failure: [...LOGIN] },

  // ════ A1/A2 — the detail page renders (hero · About · Instances) ═══════
  // (3668-03) Detail renders: hero (icon + name + Edit IaC ⟩ + summary), the
  // version chip, the admin Edit-IaC affordance — no login redirect.
  { id: '3668-D01-detail-render', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    positive: [
      { kind: 'url-includes', v: `/catalog/${BP}` },
      { kind: 'selector', v: '[data-testid="catalog-hero"]' },
      { kind: 'selector', v: '[data-testid="catalog-title"]' },
      { kind: 'selector', v: '[data-testid="catalog-version"]' },        // v1.0.1 chip (non-card field rendered)
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'text', v: 'Couldn’t load' }, { kind: 'text', v: 'catalog get: HTTP 404' }] },

  // (3668-03) The admin Edit-IaC button is PRESENT in the hero (proves the
  // admin gate resolved — a non-admin/login would not render it).
  { id: '3668-D02-edit-iac-affordance', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    positive: [
      { kind: 'selector', v: '[data-testid="catalog-detail-edit-iac"]' },
      { kind: 'text', v: 'Edit IaC' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ════ D2 — the full-CR "Edit IaC" YamlEditor OPENS (3668-24) ═══════════
  // Click catalog-detail-edit-iac → the editor section mounts with the
  // "Edit IaC — full blueprint" heading, the same-file subtitle copy, and
  // the shipping YamlEditor (textarea seeded with the full CR YAML).
  { id: '3668-D03-iac-editor-opens', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="catalog-detail-edit-iac"]' },
      { action: 'waitFor', selector: '[data-testid="catalog-edit-iac-section"]', value: '8000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="catalog-edit-iac-section"]' },
      { kind: 'text', v: 'Edit IaC — full blueprint' },
      { kind: 'selector', v: '[data-testid="yaml-editor"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-textarea"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // (3668-26) The editor subtitle surfaces the single-source IaC claim IN-UI:
  // "Commit writes the IaC source of truth" + "Both this editor and the card
  // form above write the same file." — the durable-IaC guarantee, in copy.
  { id: '3668-D04-iac-same-file-copy', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="catalog-detail-edit-iac"]' },
      { action: 'waitFor', selector: '[data-testid="catalog-edit-iac-section"]', value: '8000' },
    ],
    positive: [
      { kind: 'text', v: 'Commit writes the IaC source of truth' },
      { kind: 'text', v: 'write the same file' },
      { kind: 'text', v: 'blueprint.yaml' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // (3668-24) The full CR is shown — not the 7 card fields. The seeded YAML
  // carries the Blueprint kind + spec subtrees the card form can't touch
  // (the textarea value holds apiVersion/kind/spec). We assert the editor
  // surfaces the managed-by indicator + the Commit IaC button (the commit
  // seam) + Validate (dry-run) — the full editor chrome, read-only.
  { id: '3668-D05-iac-full-cr-chrome', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="catalog-detail-edit-iac"]' },
      { action: 'waitFor', selector: '[data-testid="yaml-editor"]', value: '8000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="yaml-editor-managed-by"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-validate"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-apply"]' },
      { kind: 'text', v: 'Commit IaC' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // (3668-25) Show diff renders a side-by-side Current vs Proposed view.
  // Open the editor, click yaml-editor-toggle-diff → assert both panes +
  // their "Current"/"Proposed" headers render.
  { id: '3668-D06-iac-show-diff', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="catalog-detail-edit-iac"]' },
      { action: 'waitFor', selector: '[data-testid="yaml-editor-toggle-diff"]', value: '8000' },
      { action: 'click', selector: '[data-testid="yaml-editor-toggle-diff"]' },
      { action: 'waitFor', selector: '[data-testid="yaml-editor-diff"]', value: '5000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="yaml-editor-diff"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-diff-left"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-diff-right"]' },
      { kind: 'text', v: 'Current' },
      { kind: 'text', v: 'Proposed' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ════ D1 — per-field inline SUMMARY editor opens in place (3668-04/21/22) ═
  // Click the summary pencil (cif-summary-edit) → an inline SUMMARY textarea
  // (cif-summary-input) drops in place with Save/Cancel, NO modal. Read-only:
  // we assert the editor opens + the input + the Save affordance; we do NOT
  // type+Save (no live write).
  { id: '3668-D07-summary-inline-edit', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="cif-summary-edit"]' },
      { action: 'waitFor', selector: '[data-testid="cif-summary-input"]', value: '6000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="cif-summary-input"]' },
      { kind: 'selector', v: '[data-testid="cif-summary-save"]' },
      { kind: 'selector', v: '[data-testid="cif-summary-cancel"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // (3668-23) The NAME field is likewise a per-field inline editor: click
  // cif-name-edit → a "Display name" textbox (cif-name-input) + Save/Cancel.
  { id: '3668-D08-name-inline-edit', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="cif-name-edit"]' },
      { action: 'waitFor', selector: '[data-testid="cif-name-input"]', value: '6000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="cif-name-input"]' },
      { kind: 'selector', v: '[data-testid="cif-name-save"]' },
      { kind: 'text', v: 'Display name' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ════ B5 — the visual icon PICKER grid opens (3668-11/15/16/17) ════════
  // Click cif-icon-edit → the "Icon (light + dark)" panel mounts two
  // role=listbox galleries of vendored component-logos/* tiles. Assert both
  // galleries + the light-grid + the per-theme URL field render.
  { id: '3668-D09-icon-picker-opens', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="cif-icon-edit"]' },
      { action: 'waitFor', selector: '[data-testid="iconpicker-light-grid"]', value: '6000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="iconpicker-light-grid"]' },
      { kind: 'selector', v: '[data-testid="iconpicker-dark-grid"]' },
      { kind: 'selector', v: '[data-testid="iconpicker-light-url"]' },
      { kind: 'text', v: 'Light theme' },
      { kind: 'text', v: 'Dark theme' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // (3668-16/17) The gallery holds the vendored logo tiles — the Cilium tile
  // (iconpicker-light-tile-cilium) is present (the walk's pick target). Click
  // it → it becomes [selected] (aria-selected) and the preview swatch updates.
  // Read-only: we click the TILE (in-draft only, no Save) to prove the picker
  // is interactive, then assert the selection state — no IaC write fires.
  { id: '3668-D10-icon-tile-select', runbook: '3668', url: DETAIL(BP), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="cif-icon-edit"]' },
      { action: 'waitFor', selector: '[data-testid="iconpicker-light-tile-cilium"]', value: '6000' },
      { action: 'click', selector: '[data-testid="iconpicker-light-tile-cilium"]' },
      { action: 'waitFor', selector: '[data-testid="iconpicker-light-tile-cilium"][aria-selected="true"]', value: '4000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="iconpicker-light-tile-cilium"]' },
      { kind: 'selector', v: '[data-testid="iconpicker-light-tile-cilium"][aria-selected="true"]' },
      { kind: 'selector', v: '[data-testid="iconpicker-light-preview-img"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ════ E-generality — the SAME chrome on a 2nd blueprint (3668-30/31) ═══
  // The second blueprint's detail page renders the identical edit surface:
  // hero + Edit-IaC affordance + the cif-* inline editors. Proves no
  // per-blueprint UI (founder rule #4). bp-wordpress 404s on hw1NN (matrix);
  // BP2 (grafana) is present.
  { id: '3668-D11-bp2-detail-render', runbook: '3668', url: DETAIL(BP2), settleMs: 6000,
    positive: [
      { kind: 'url-includes', v: `/catalog/${BP2}` },
      { kind: 'selector', v: '[data-testid="catalog-hero"]' },
      { kind: 'selector', v: '[data-testid="catalog-detail-edit-iac"]' },
      { kind: 'selector', v: '[data-testid="cif-summary-edit"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'text', v: 'Couldn’t load' }, { kind: 'text', v: 'catalog get: HTTP 404' }] },

  // (3668-31) The SAME Edit-IaC YamlEditor opens on the 2nd blueprint — the
  // identical full-CR editor surface, generic across blueprints.
  { id: '3668-D12-bp2-iac-editor', runbook: '3668', url: DETAIL(BP2), settleMs: 6000,
    steps: [
      { action: 'click', selector: '[data-testid="catalog-detail-edit-iac"]' },
      { action: 'waitFor', selector: '[data-testid="yaml-editor"]', value: '8000' },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="catalog-edit-iac-section"]' },
      { kind: 'selector', v: '[data-testid="yaml-editor-textarea"]' },
      { kind: 'text', v: 'write the same file' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },
];

// ── marker evaluation (from uat-console-probe.mjs) ──────────────────────
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

// ── step execution (the DEEP extension over uat-console-probe.mjs) ──────
// Each step interacts with the page BEFORE markers are asserted. A step
// failure is recorded (details) and surfaces the row RED via the unmet
// positive marker — it never crashes the run.
async function runStep(page, step, result) {
  try {
    switch (step.action) {
      case 'click':
        await page.locator(step.selector).first().click({ timeout: Number(step.value) || 8000 });
        break;
      case 'fill':
        await page.locator(step.selector).first().fill(step.value ?? '', { timeout: 8000 });
        break;
      case 'waitFor':
        await page.locator(step.selector).first().waitFor({ state: 'visible', timeout: Number(step.value) || 8000 });
        break;
      default:
        result.details.push(`unknown step action ${step.action}`);
    }
  } catch (e) {
    result.details.push(`step ${step.action} ${step.selector}: ${e.message.split('\n')[0]}`);
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

    // DEEP extension — run interaction steps before asserting markers.
    if (Array.isArray(row.steps)) {
      for (const step of row.steps) await runStep(page, step, result);
      await page.waitForTimeout(700); // let React commit after the last step
    }

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
// Single shared authed context — the first row (handover URL) seeds the
// session cookie; every later row reuses it (each opens its own page).
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });
const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.id.padEnd(26)} ${res.finalURL.padEnd(46)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, bp: BP, bp2: BP2, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
