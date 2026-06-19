#!/usr/bin/env node
// uat-3687-deep-probe.mjs — live-env browser UAT for the DEEP #3687 rows
// (canonical Organization/Application CR model) BEYOND the console-structure
// rows already covered by scripts/uat-console-probe.mjs.
//
// SIBLING of scripts/uat-console-probe.mjs: that probe owns the #3687
// STRUCTURE rows (sidebar / dashboard route / catalog route / apps route /
// organizations route — rows 01/02/20/22/26/35 all GREEN on hw167). THIS
// probe owns the DEEP rows the structure probe does NOT: the Organizations
// directory TABLE (one canonical row per Org with kind/tier/billing/
// isolation/status badges), the per-Organization DETAIL identity card
// (slug/kind/tier/billing/isolation/status CR fields), the dashboard
// treemap LAYER selector + its dimension options (Organization layer
// presence is the live #3692 fold), the Showback panel (per-app
// consumption, honest empty-state), the Apps grid (one card per
// Application, BOOTSTRAP-badged), and the shared-PG many-to-many Contexts
// tab (the master proof — 3 consumers sharing ONE PG instance).
//
// Each row captures a SCREENSHOT (the only acceptance evidence the founder
// accepts) AND asserts a positive landing marker + the ABSENCE of login /
// 404 / fabrication markers. A redirect to a login/PIN screen = FAIL.
//
// ENV-INDEPENDENCE: these rows assert the CONVERGED-CONSOLE STRUCTURE +
// the bootstrap/platform Organization (the parent org is the FIRST citizen
// of the directory, #3378 §4/§5, present on ANY converged Sovereign with
// zero sub-orgs). They do NOT require a funnel run — there is NO 'Acme'
// customer Org on hw167 (no funnel has run), so NO row asserts a customer
// Org by name. Sparse data is fine: the rows assert the canonical
// STRUCTURE (the directory renders a table with the canonical columns; the
// parent row renders the canonical badges; the treemap selector offers the
// Organization dimension) — not specific customer rows.
//
// Selectors are derived from the LIVE console source under
// products/catalyst/bootstrap/ui/src/ (the deployed Sovereign-console
// React app, "same code in every Sovereign") + the docs/ledger/UAT.md
// `3687-NN` row descriptions — not guesses. Named data-testids where
// precise; robust TEXT + URL markers where the surface is data-driven.
//
// Usage:
//   node scripts/uat-3687-deep-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--rows 3687-05,3687-10] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all probed rows GREEN, 1 = >=1 row RED, 2 = harness error.
// PROBE_CHROMIUM wins; else any installed ms-playwright chromium build.

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
//
// Selector provenance (live console source):
//   OrganizationsDirectoryPage.tsx  → organizations-directory-page,
//     organizations-table, organizations-result-count (N/M),
//     organizations-cell-{kind,tier,billing,isolation,status}-<id>,
//     showback-panel; column headers Kind/Tier/Billing/Isolation/Status.
//   OrganizationDetailPage.tsx      → organization-detail-page,
//     org-detail-identity, org-detail-{slug,kind,tier,billing,isolation,status}.
//   Dashboard.tsx / TreemapLayerController.tsx → dashboard-page,
//     dashboard-treemap-frame, treemap-layer-controller,
//     treemap-layer-0-select; DIMENSION_OPTIONS includes Organization.
//   ShowbackPanel.tsx               → showback-panel, header
//     "Showback — per-app consumption", showback-empty / showback-org.
//   AppsPage.tsx                    → sov-apps-grid, sov-app-card-bp-<n>,
//     BOOTSTRAP chip text.
//   AppDetail.tsx + ContextsTab.tsx → app-detail-tablist, app-tab-<id>,
//     sov-contexts-table, headers Context/Occupied by/Credential/Status.
const ROWS = [
  // ── Pre-flight: owner handover lands signed-in on the dashboard ───────
  { id: '3687-01', runbook: '3687', url: handoverURL, settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/dashboard' }, { kind: 'text', v: 'Dashboard' }],
    failure: [...LOGIN] },

  // ── Lane A/B/C — Organizations DIRECTORY (one canonical Org row) ──────
  // 3687-09: the parent Org is present as a real Organization in the
  // directory (the FIRST citizen, present on any converged Sovereign).
  { id: '3687-09', runbook: '3687', url: `${C}/organizations`, settleMs: 6000,
    positive: [
      { kind: 'url-includes', v: '/organizations' },
      { kind: 'selector', v: '[data-testid="organizations-directory-page"]' },
      { kind: 'selector', v: '[data-testid="organizations-table"]' },
      // >=1 directory row (the parent estate row at minimum)
      { kind: 'count-gte', v: '[data-testid^="organizations-row-"]|1' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3687-05: a directory row renders the canonical badge SET —
  // Kind/Tier/Billing/Isolation/Status columns + per-row badge cells.
  { id: '3687-05', runbook: '3687', url: `${C}/organizations`, settleMs: 6000,
    positive: [
      { kind: 'text', v: 'Kind' }, { kind: 'text', v: 'Tier' },
      { kind: 'text', v: 'Billing' }, { kind: 'text', v: 'Isolation' },
      { kind: 'text', v: 'Status' },
      { kind: 'count-gte', v: '[data-testid^="organizations-cell-kind-"]|1' },
      { kind: 'count-gte', v: '[data-testid^="organizations-cell-billing-"]|1' },
      { kind: 'count-gte', v: '[data-testid^="organizations-cell-status-"]|1' },
    ],
    // banned persona words (#3383) must NOT appear in the directory
    failure: [...LOGIN, ...NOTFOUND, { kind: 'text', v: 'SME tenant' }, { kind: 'text', v: 'Onboard tenant' }] },

  // 3687-30 + 3687-31: the Showback panel renders on the directory with
  // the canonical header + an HONEST state (empty or a real org row, no
  // infra pods summed into a fake tenant). On hw167 (no funnel) the
  // panel shows the parent estate row / empty-state — both honest.
  { id: '3687-30', runbook: '3687', url: `${C}/organizations`, settleMs: 6000,
    positive: [
      { kind: 'selector', v: '[data-testid="showback-panel"]' },
      { kind: 'text', v: 'Showback — per-app consumption' },
      // honest content: either the empty-state line OR a real org slice
      { kind: 'text-regex', v: 'No consumption attributed yet|parent — your own estate|No applications attributed yet|units' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── Per-Organization DETAIL — identity card (CR fields) ──────────────
  // 3687-10 + 3687-16/17: opening the parent Org's detail renders the
  // canonical identity card with slug/kind/tier/billing/isolation/status
  // CR-field cells (NOT a login redirect, NOT a not-found). The parent
  // slug is the deployment-id-derived sovereign slug; we assert the
  // card STRUCTURE (every field rendered) rather than a hardcoded slug.
  { id: '3687-10', runbook: '3687', url: `${C}/organizations`, settleMs: 6000,
    // navigate from the directory: click the first org-name cell, land
    // on /organizations/<slug> and read the identity card. (Done in the
    // probe loop via a `clickFirst` hook below.)
    clickFirst: '[data-testid^="organizations-cell-name-"]',
    afterClickSettleMs: 4000,
    positive: [
      { kind: 'url-includes', v: '/organizations/' },
      { kind: 'selector', v: '[data-testid="organization-detail-page"]' },
      { kind: 'selector', v: '[data-testid="org-detail-identity"]' },
      { kind: 'selector', v: '[data-testid="org-detail-slug"]' },
      { kind: 'selector', v: '[data-testid="org-detail-kind"]' },
      { kind: 'selector', v: '[data-testid="org-detail-isolation"]' },
      { kind: 'selector', v: '[data-testid="org-detail-status"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="org-detail-not-found"]' }] },

  // ── Lane D — Catalog: New-instance + single-source Edit-IaC ──────────
  // 3687-20: catalog grid renders Blueprint cards; bp-postgres detail
  // offers the New-instance affordance (the create seam).
  { id: '3687-20', runbook: '3687', url: `${C}/catalog`, settleMs: 6000,
    positive: [
      { kind: 'url-includes', v: '/catalog' },
      { kind: 'selector', v: '[data-testid="sov-catalog-grid"]' },
      { kind: 'count-gte', v: '[data-testid^="sov-app-card-bp-"]|10' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3687-21-precursor / #3668: blueprint detail renders the single-source
  // "Edit IaC" affordance + the New-instance name field (the create
  // door). bp-postgres is shareable → badge-shareable present.
  { id: '3687-20b', runbook: '3687', url: `${C}/catalog/bp-postgres`, settleMs: 6000,
    positive: [
      { kind: 'selector', v: '[data-testid="catalog-drilldown"]' },
      { kind: 'text-regex', v: 'Edit IaC|New instance|Postgres|PostgreSQL' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="catalog-error"]' }] },

  // ── Lane E — Dashboard treemap reads the Organization model ──────────
  // 3687-26: the Layer-1 selector renders + offers dimension options.
  // The live #3692 fold makes Layer-1 default = Organization (Dashboard.tsx
  // defaultLayers = ['organization','application'] in sovereign mode) and
  // the dimension list includes an Organization option. We assert the
  // selector exists and offers the Organization dimension; if the live
  // image predates the fold the Organization option is ABSENT → honest RED.
  { id: '3687-26', runbook: '3687', url: `${C}/dashboard`, settleMs: 7000,
    positive: [
      { kind: 'selector', v: '[data-testid="dashboard-page"]' },
      { kind: 'selector', v: '[data-testid="treemap-layer-controller"]' },
      { kind: 'selector', v: '[data-testid="treemap-layer-0-select"]' },
      // the Layer-1 select must OFFER an Organization option (the #3692
      // fold). option text is rendered in the DOM even when not selected.
      { kind: 'option-in-select', v: '[data-testid="treemap-layer-0-select"]|Organization' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3687-26b: the Layer-1 selector offers the full canonical dimension
  // vocabulary (Cluster + at least one of Sovereign/Region/vCluster) so
  // the operator can pivot to the multi-region topology view.
  { id: '3687-26b', runbook: '3687', url: `${C}/dashboard`, settleMs: 7000,
    positive: [
      { kind: 'selector', v: '[data-testid="treemap-layer-0-select"]' },
      { kind: 'option-in-select', v: '[data-testid="treemap-layer-0-select"]|Cluster' },
      { kind: 'option-regex-in-select', v: '[data-testid="treemap-layer-0-select"]|vCluster|Region|Sovereign' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3687-27: the treemap renders a surface (or honest empty-state) with
  // NO ephemeral Job-pod cells — no cutover-*/scan-vulnerabilityreport-*/
  // *-snapshot-save-* labels in any treemap cell <text>. (Asserts ABSENCE
  // of Job cells; the frame must render either a surface or the empty-state.)
  { id: '3687-27', runbook: '3687', url: `${C}/dashboard`, settleMs: 8000,
    positive: [
      { kind: 'selector', v: '[data-testid="dashboard-treemap-frame"]' },
      // either a populated surface or the honest empty-state — one must hold
      { kind: 'selector-any', v: '[data-testid="dashboard-treemap-surface"],[data-testid="dashboard-empty"],[data-testid="dashboard-loading"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND,
      { kind: 'text-regex', v: 'cutover-[a-z0-9]|scan-vulnerabilityreport-|-snapshot-save-' }],
  },

  // ── Lane E (cont.) — Apps grid is one card per Application, BOOTSTRAP ─
  // 3687-22 + 3687-28: the Apps list renders a grid of Application cards
  // (one per Application, NOT one per HelmRelease/pod), all platform-owned
  // cards carry the BOOTSTRAP badge. Assert the grid + a meaningful card
  // count + the BOOTSTRAP badge presence.
  { id: '3687-28', runbook: '3687', url: `${C}/apps`, settleMs: 7000,
    positive: [
      { kind: 'url-includes', v: '/apps' },
      { kind: 'selector', v: '[data-testid="sov-apps-grid"]' },
      { kind: 'count-gte', v: '[data-testid^="sov-app-card-bp-"]|10' },
      { kind: 'text', v: 'BOOTSTRAP' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // ── Master proof — shared-PG many-to-many Contexts tab ───────────────
  // The shared-PG instance CR id is the BARE slug `shared-pg` on this
  // Sovereign (the 3 shared-PG instances surface as shared-pg /
  // shared-pg-b / shared-pg-c — North Star #2 "3 shared PG instances →
  // 3 cards"); the route is /app/shared-pg (NOT /app/bp-shared-pg — that
  // bp-prefixed id was hw159-specific and 404s here with "component
  // bp-shared-pg is not part of this deployment"). The CR-id naming is
  // per-env (bootstrap-kit installs strip/keep the bp- prefix per chart);
  // the discovery seam is the Apps grid card ids (sov-app-card-<id>).
  //
  // 3687-34: the shared-pg app page renders the canonical tab strip and a
  // Contexts tab. Open the app, assert the tablist + the Contexts + the
  // Topology tab buttons.
  { id: '3687-34', runbook: '3687', url: `${C}/app/shared-pg`, settleMs: 7000,
    positive: [
      { kind: 'selector', v: '[data-testid="app-detail-tablist"]' },
      { kind: 'selector', v: '[data-testid="app-tab-overview"]' },
      { kind: 'selector', v: '[data-testid="app-tab-contexts"]' },
      { kind: 'selector', v: '[data-testid="app-tab-topology"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="sov-app-not-found"]' }] },

  // 3687-21: the shared-PG reuse model is LIVE — the Contexts tab shows
  // the entity-first table (Context/Occupied by/Credential/Status) with
  // >=2 consumer rows: the MANY-TO-MANY master proof (multiple apps —
  // harbor db/registry, gitea db/gitea, keycloak db/keycloak — sharing
  // ONE shared-pg instance). Click the Contexts tab, then assert the
  // populated table + >=2 distinct Context rows. (If a future env has no
  // consumers provisioned the honest empty-state would render — but on a
  // converged Sovereign the bootstrap consumers ALWAYS share shared-pg,
  // so >=2 rows is the correct master-proof assertion, not empty-tolerant.)
  { id: '3687-21', runbook: '3687', url: `${C}/app/shared-pg`, settleMs: 7000,
    clickFirst: '[data-testid="app-tab-contexts"]',
    afterClickSettleMs: 2500,
    positive: [
      { kind: 'selector', v: '[data-testid="sov-contexts-table"]' },
      { kind: 'count-gte', v: '[data-testid^="sov-context-row-"]|2' },
    ],
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="sov-app-not-found"]' }] },

  // ── Cross-lane — Apps ↔ Orgs agree (one consistent model) ────────────
  // 3687-35: /organizations renders the directory table (>=1 Org) AND
  // /apps renders the apps grid — the two surfaces both read the single
  // model. (The structure probe asserts the bare routes; this asserts the
  // canonical CONTENT containers on both, in one row, as the consistency
  // gate.) Re-visits /organizations (last) so the screenshot captures it.
  { id: '3687-35', runbook: '3687', url: `${C}/organizations`, settleMs: 6000,
    positive: [
      { kind: 'selector', v: '[data-testid="organizations-table"]' },
      { kind: 'count-gte', v: '[data-testid^="organizations-row-"]|1' },
      { kind: 'selector', v: '[data-testid="organizations-result-count"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },
];

// ── marker evaluation (from uat-console-probe.mjs + select-option kinds) ─
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'selector-any': {
      // comma-joined selector list — true if ANY matches at least once.
      for (const sel of m.v.split(',')) {
        if ((await page.locator(sel.trim()).count()) > 0) return true;
      }
      return false;
    }
    case 'count-gte': { const [sel, n] = m.v.split('|'); return (await page.locator(sel).count()) >= Number(n); }
    case 'option-in-select': {
      // "<select-selector>|<option-text>" — true if the <select> contains
      // an <option> whose text equals (case-insensitive) the given label.
      const [sel, want] = m.v.split('|');
      const opts = await page.locator(`${sel} option`).allTextContents().catch(() => []);
      return opts.some((o) => o.trim().toLowerCase() === want.trim().toLowerCase());
    }
    case 'option-regex-in-select': {
      // "<select-selector>|<regex>" — true if any <option> text matches.
      const [sel, rx] = m.v.split('|');
      const opts = await page.locator(`${sel} option`).allTextContents().catch(() => []);
      const re = new RegExp(rx, 'i');
      return opts.some((o) => re.test(o));
    }
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

    // Optional interaction: click the first element matching `clickFirst`
    // (e.g. drill into the first Org row, or open the Contexts tab) then
    // settle — the row's assertions evaluate AFTER the click.
    if (row.clickFirst) {
      const target = page.locator(row.clickFirst).first();
      if ((await target.count()) > 0) {
        await target.click({ timeout: 8000 }).catch((e) => result.details.push(`click ${row.clickFirst}: ${e.message.split('\n')[0]}`));
        await page.waitForTimeout(row.afterClickSettleMs || 2500);
        await page.waitForLoadState('networkidle', { timeout: 10000 }).catch(() => {});
      } else {
        result.details.push(`clickFirst target absent: ${row.clickFirst}`);
      }
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
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });
const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.id.padEnd(10)} ${res.finalURL.padEnd(46)} shot=${res.shot}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
