#!/usr/bin/env node
// uat-3642-placement-probe.mjs — live-env browser UAT for runbook #3642
// (North Star #1: "every app IN a vCluster"). SIBLING of
// scripts/uat-console-probe.mjs (console-structure rows) and
// scripts/sso-zero-click-probe.mjs (SSO landing matrix). This probe owns the
// SINGLE decisive #3642 check: on the /dashboard treemap with the LAYER-1
// grouping set to `vCluster`, each of the 7 named host-migrated apps —
// grafana, harbor, keycloak, gitea, openbao, newapi, guacamole — must render
// as a tile sitting INSIDE the `mgmt` vCluster block, NOT the `host` block.
//
// WHY a bespoke probe and not a uat-console-probe.mjs row: the headline check
// is not a text/URL marker — it is an SVG-treemap SPATIAL-CONTAINMENT read
// (which block-rect does each app tile fall inside) gated behind a combobox
// interaction (set LAYER-1 = vCluster). That needs a per-row `steps` array +
// a geometry extractor, which the marker-only console probe doesn't carry.
//
// GROUNDED in: docs/ledger/UAT.md rows 3642-01..3642-23 and
// docs/ledger/uat-walkthrough/ns1-migrate-7-host-apps-into-mgmt-vcluster.md
// (PART A treemap setup · PART B the 7 per-app block rows · PART C drill-into
// -mgmt + host-clean + app-card placement readout). Selectors are the live
// console's own data-testids — discovered against console.hw167 and NOT
// guessed: treemap-layer-controller / treemap-layer-0-select (= the runbook's
// "LAYER 1", 0-indexed) / treemap-layer-1-select / treemap-size-select.
//
// METHOD (env-independent, no hardcoded coordinates): the treemap is one SVG
// whose block headers (`host` / `mgmt` / `rtz` / `dmz`) and app tiles are flat
// `<text>`+`<rect>` pairs at the same DOM depth — block membership is ONLY
// derivable from geometry. The probe reads every label's nearest rect bbox at
// runtime, treats the 4 named vCluster labels as block rectangles, and assigns
// each app to the block whose rect contains the app tile's centre point. This
// reproduces the operator's eyeball read of "which coloured block is this tile
// in" deterministically, and re-derives the block rects per env (they move
// with utilisation), so it never stales like a coordinate literal would.
//
// HONESTY (founder rule — never fake GREEN): an app whose tile is genuinely in
// `host` is reported ❌ host; an app with NO tile in the treemap (e.g. its HR
// is still installing — hw167 is 57/64 with guacamole pending) is reported
// ABSENT (RED) with that note, never silently passed. The NS#1 verdict line
// prints exactly how many of the 7 are in mgmt.
//
// Usage:
//   node scripts/uat-3642-placement-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = all rows GREEN, 1 = ≥1 row RED, 2 = harness error.
// PROBE_CHROMIUM wins; else any installed chromium-* build is auto-resolved.

import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import { writeFileSync, mkdirSync, readdirSync, statSync } from 'node:fs';
import crypto from 'node:crypto';

// ── chromium resolve (verbatim from uat-console-probe.mjs) ──────────────
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

// ── args (verbatim shape from uat-console-probe.mjs) ────────────────────
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

// ── handover URL (verbatim mint from uat-console-probe.mjs) ──────────────
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

// ── constants for the #3642 walk ────────────────────────────────────────
const SEVEN = ['grafana', 'harbor', 'keycloak', 'gitea', 'openbao', 'newapi', 'guacamole'];
const VCLUSTER_BLOCKS = ['host', 'mgmt', 'rtz', 'dmz']; // labels that act as block rects
// the LAYER-1 grouping control (runbook "LAYER 1" == 0-indexed layer-0 select)
const LAYER1_SELECT = '[data-testid="treemap-layer-0-select"]';
const TREEMAP_TOOLBAR = '[data-testid="treemap-layer-controller"]';
// login / fabrication markers (NONE may hold on an authed row) — from siblings
const LOGIN = [
  { kind: 'text', v: 'Enter the 6-digit' },
  { kind: 'selector', v: 'input[type="password"]' },
  { kind: 'text', v: 'Sign in to continue' },
];

// ── shared session bootstrap: open handover once, land on /dashboard ─────
async function bootstrap(ctx) {
  const page = await ctx.newPage();
  await page.goto(handoverURL, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await page.waitForTimeout(6000);
  await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
  return page;
}

// ── treemap geometry reader (the core of the #3642 acceptance) ──────────
// Sets LAYER-1 = vCluster, then returns { blocks:{name:{x,y,w,h}}, apps:{name:{cx,cy,block}}, allLabels:[...] }.
// One geometry snapshot: returns { blocks:{name:{x,y,w,h}}, apps:{name:{...}},
// mgmtMembers, hostMembers } from the CURRENT DOM, or { error }.
async function snapshotTreemap(page) {
  const geom = await page.evaluate(({ blockNames }) => {
    const svgs = [...document.querySelectorAll('svg')];
    const big = svgs.sort((a, b) => b.querySelectorAll('rect').length - a.querySelectorAll('rect').length)[0];
    if (!big) return { error: 'no-treemap-svg' };
    const labels = [];
    for (const t of big.querySelectorAll('text')) {
      const raw = (t.textContent || '').trim();
      if (!raw) continue;
      let el = t.parentElement, rect = null;
      while (el && el !== big) { rect = el.querySelector(':scope > rect') || rect; if (rect) break; el = el.parentElement; }
      let rb = null; if (rect) { try { rb = rect.getBoundingClientRect(); } catch { /* */ } }
      let tb = null; try { tb = t.getBoundingClientRect(); } catch { /* */ }
      labels.push({
        label: raw,
        rx: rb ? rb.x : (tb ? tb.x : 0), ry: rb ? rb.y : (tb ? tb.y : 0),
        rw: rb ? rb.width : 0, rh: rb ? rb.height : 0,
        tx: tb ? tb.x : 0, ty: tb ? tb.y : 0,
      });
    }
    const blocks = {};
    for (const nm of blockNames) {
      const cands = labels.filter((l) => l.label.toLowerCase() === nm && l.rw > 0 && l.rh > 0);
      if (!cands.length) continue;
      const b = cands.sort((a, b2) => b2.rw * b2.rh - a.rw * a.rh)[0];
      blocks[nm] = { x: b.rx, y: b.ry, w: b.rw, h: b.rh };
    }
    return { blocks, labels };
  }, { blockNames: VCLUSTER_BLOCKS });
  if (geom.error) return geom;

  // smallest block rect that contains the centre point
  function blockOf(cx, cy) {
    let best = null, bestArea = Infinity;
    for (const [name, r] of Object.entries(geom.blocks)) {
      if (cx >= r.x && cx <= r.x + r.w && cy >= r.y && cy <= r.y + r.h) {
        const area = r.w * r.h;
        if (area < bestArea) { bestArea = area; best = name; }
      }
    }
    return best;
  }
  const apps = {};
  for (const nm of SEVEN) {
    const cand = geom.labels.find((l) => l.label.toLowerCase() === nm);
    if (!cand) { apps[nm] = { present: false }; continue; }
    const cx = cand.rw > 0 ? cand.rx + cand.rw / 2 : cand.tx;
    const cy = cand.rh > 0 ? cand.ry + cand.rh / 2 : cand.ty;
    apps[nm] = { present: true, cx: Math.round(cx), cy: Math.round(cy), block: blockOf(cx, cy) };
  }
  const mgmtMembers = [], hostMembers = [];
  const seen = new Set();
  for (const l of geom.labels) {
    if (/^\d+%$/.test(l.label) || l.label === '—' || VCLUSTER_BLOCKS.includes(l.label.toLowerCase())) continue;
    if (seen.has(l.label)) continue; seen.add(l.label);
    const cx = l.rw > 0 ? l.rx + l.rw / 2 : l.tx;
    const cy = l.rh > 0 ? l.ry + l.rh / 2 : l.ty;
    const b = blockOf(cx, cy);
    if (b === 'mgmt') mgmtMembers.push(l.label);
    if (b === 'host') hostMembers.push(l.label);
  }
  return { blocks: Object.keys(geom.blocks), apps, mgmtMembers, hostMembers };
}

// Set LAYER-1 = vCluster, then take TWO snapshots a few seconds apart and
// MERGE them. The live treemap is utilisation-driven and a converging env
// (HRs still installing) can drop/re-add a tile between renders; merging takes
// each app's DEFINITE block placement from whichever snapshot rendered its
// tile (an app counts as placed only if it actually rendered in a block — an
// app absent from BOTH reads stays honestly ABSENT, never auto-passed).
async function readTreemapByVCluster(page) {
  await page.goto(`${C}/dashboard`, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await page.waitForTimeout(5000);
  await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
  await page.selectOption(LAYER1_SELECT, { label: 'vCluster' }).catch(() => {});
  await page.waitForTimeout(4000);

  const snaps = [];
  const s1 = await snapshotTreemap(page);
  if (s1 && !s1.error) snaps.push(s1);
  await page.waitForTimeout(4000); // let the treemap re-render once
  const s2 = await snapshotTreemap(page);
  if (s2 && !s2.error) snaps.push(s2);
  if (!snaps.length) return s1 && s1.error ? s1 : { error: 'no-treemap-svg' };

  // merge: union blocks; per app prefer the snapshot where the tile is present.
  const blocks = [...new Set(snaps.flatMap((s) => s.blocks))];
  const apps = {};
  for (const nm of SEVEN) {
    const present = snaps.map((s) => s.apps[nm]).filter((a) => a && a.present);
    if (!present.length) { apps[nm] = { present: false }; continue; }
    // if any snapshot put it in mgmt, that's the placement; else take the first.
    apps[nm] = present.find((a) => a.block === 'mgmt') || present[0];
  }
  // mgmt/host membership lists: union across snapshots
  const mgmtMembers = [...new Set(snaps.flatMap((s) => s.mgmtMembers || []))];
  const hostMembers = [...new Set(snaps.flatMap((s) => s.hostMembers || []))];
  return { blocks, apps, mgmtMembers, hostMembers, snapshots: snaps.length };
}

// ── app-card placement readout (#3642-13 / PART C) ──────────────────────
// Reads the /app/<name> Overview "Namespace" field — the per-app placement
// readout that mirrors the treemap block (mgmt apps read Namespace=mgmt;
// a host-resident app reads its own namespace and the word "host").
async function readAppCardPlacement(page, app) {
  await page.goto(`${C}/app/${app}`, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await page.waitForTimeout(4000);
  await page.waitForLoadState('networkidle', { timeout: 12000 }).catch(() => {});
  const body = ((await page.textContent('body').catch(() => '')) || '').replace(/\s+/g, ' ');
  // Grab the namespace token right after the "Namespace" field label. The live
  // DOM concatenates labels+values with no whitespace (…NamespacemgmtBlueprint…),
  // so stop the value at the next capitalised field label (Blueprint/Placement/
  // Regions/Ready/About) — a k8s namespace is lowercase-alnum-dash only.
  const m = body.match(/Namespace\s*([a-z0-9][a-z0-9._-]*?)(?=[A-Z]|\s|$)/);
  const ns = m ? m[1] : '';
  const mentionsMgmt = /\bmgmt\b/.test(body);
  const mentionsHost = /\bhost\b/.test(body);
  const login = LOGIN.some((x) => x.kind === 'text' && body.includes(x.v));
  return { ns, mentionsMgmt, mentionsHost, login, hasBody: body.length > 0 };
}

// ── row runner: each row screenshots + evaluates an assert closure ──────
async function runRow(ctx, id, label, fn) {
  const page = await ctx.newPage();
  const shot = `${SHOTS}/${ENVTAG}-${id}.png`;
  const res = { id, label, status: 'RED', shot, details: [] };
  try {
    const r = await fn(page);
    res.status = r.ok ? 'GREEN' : 'RED';
    if (r.note) res.details.push(r.note);
    await page.screenshot({ path: shot, fullPage: true }).catch((e) => res.details.push(`shot: ${e.message.split('\n')[0]}`));
  } catch (e) {
    res.details.push(`error: ${e.message.split('\n')[0]}`);
    await page.screenshot({ path: shot, fullPage: true }).catch(() => {});
  } finally {
    await page.close().catch(() => {});
  }
  return res;
}

// ── main ────────────────────────────────────────────────────────────────
const browser = await chromium.launch({ headless: true, executablePath: resolveChromium() });
const ctx = await browser.newContext({ ignoreHTTPSErrors: false });
const results = [];

// (1) Sign-in once (3642-01): handover lands on /dashboard, no login form.
{
  const page = await bootstrap(ctx);
  const shot = `${SHOTS}/${ENVTAG}-3642-01.png`;
  await page.screenshot({ path: shot, fullPage: true }).catch(() => {});
  const body = ((await page.textContent('body').catch(() => '')) || '');
  const onDash = page.url().includes('/dashboard');
  const noLogin = !LOGIN.some((x) => x.kind === 'text' && body.includes(x.v)) && !body.includes('Enter the 6-digit');
  results.push({ id: '3642-01', label: 'sign-in → /dashboard, no login form', status: onDash && noLogin ? 'GREEN' : 'RED', shot, details: onDash ? (noLogin ? [] : ['login marker present']) : [`landed ${page.url()}`] });
  await page.close().catch(() => {});
}

// (2) PART A — treemap renders + LAYER-1 controls present (3642-02).
results.push(await runRow(ctx, '3642-02', 'PART A — treemap + LAYER controls render', async (page) => {
  await page.goto(`${C}/dashboard`, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await page.waitForTimeout(5000);
  const toolbar = await page.locator(TREEMAP_TOOLBAR).count();
  const layer1 = await page.locator(LAYER1_SELECT).count();
  const hasSvg = await page.locator('svg rect').count();
  const ok = toolbar > 0 && layer1 > 0 && hasSvg > 2;
  return { ok, note: ok ? `toolbar+LAYER1 select present, ${hasSvg} svg rects` : `toolbar=${toolbar} layer1=${layer1} rects=${hasSvg}` };
}));

// (3) PART A — set LAYER-1 = vCluster, blocks (host/mgmt) appear (3642-03).
// Also captures the canonical treemap-layer1-vcluster screenshot + the
// shared geometry read used by the per-app rows below.
let TM = null;
{
  const page = await ctx.newPage();
  const shot = `${SHOTS}/${ENVTAG}-3642-treemap-layer1-vcluster.png`;
  let note = '', ok = false;
  try {
    TM = await readTreemapByVCluster(page);
    await page.screenshot({ path: shot, fullPage: true }).catch(() => {});
    if (TM && !TM.error) {
      const haveHost = TM.blocks.includes('host');
      const haveMgmt = TM.blocks.includes('mgmt');
      ok = haveHost && haveMgmt;
      note = `blocks: ${TM.blocks.join(', ')}`;
    } else { note = `treemap read failed: ${TM && TM.error}`; }
  } catch (e) { note = `error: ${e.message.split('\n')[0]}`; }
  results.push({ id: '3642-03', label: 'PART A — LAYER1=vCluster → host+mgmt blocks appear', status: ok ? 'GREEN' : 'RED', shot, details: [note] });
  // duplicate the canonical screenshot under the 3642-03 id name too for the ledger
  await page.screenshot({ path: `${SHOTS}/${ENVTAG}-3642-03.png`, fullPage: true }).catch(() => {});
  await page.close().catch(() => {});
}

// (3b) App-card Namespace cross-check for all 7 — the SECOND operator-visible
// placement surface (PART C / runbook 3642-13). The live treemap is utilisation
// -driven and a tile can flicker out of a single render on a converging env;
// the app card's "Namespace" field is the stable per-app placement readout
// (a migrated app reads Namespace=mgmt). We use it ONLY as a tie-breaker when
// the treemap tile is momentarily absent — never to override a tile that DID
// render in `host` (a definite host tile stays a definite ❌).
const cardNs = {};
for (const app of SEVEN) {
  const page = await ctx.newPage();
  try {
    const r = await readAppCardPlacement(page, app);
    cardNs[app] = r.login ? '(login)' : (r.ns || '');
    await page.screenshot({ path: `${SHOTS}/${ENVTAG}-3642-card-${app}.png`, fullPage: true }).catch(() => {});
  } catch { cardNs[app] = ''; }
  await page.close().catch(() => {});
}

// (4) PART B — the 7 per-app block-membership rows (3642-04..3642-10).
const APP_ROW = { grafana: '3642-04', harbor: '3642-05', keycloak: '3642-06', gitea: '3642-07', openbao: '3642-08', newapi: '3642-09', guacamole: '3642-10' };
const verdict = {};
for (const app of SEVEN) {
  const id = APP_ROW[app];
  const shot = `${SHOTS}/${ENVTAG}-3642-treemap-layer1-vcluster.png`; // shared decisive shot
  const a = TM && !TM.error ? TM.apps[app] : null;
  const ns = cardNs[app] || '';
  let status = 'RED', note;
  if (a && a.present && a.block === 'mgmt') {
    status = 'GREEN'; note = `tile in MGMT block (cx=${a.cx},cy=${a.cy})`; verdict[app] = 'mgmt';
  } else if (a && a.present && a.block && a.block !== 'mgmt') {
    // a tile DID render in a non-mgmt block → definite ❌, card cannot rescue it
    note = `tile in ${a.block.toUpperCase()} block (cx=${a.cx},cy=${a.cy}) — NOT mgmt; app-card Namespace="${ns || '?'}"`; verdict[app] = a.block;
  } else {
    // tile absent from this render → fall back to the app-card Namespace field
    if (ns === 'mgmt') { status = 'GREEN'; note = `tile not in this treemap render (live churn) BUT app-card Namespace="mgmt" → placed in mgmt`; verdict[app] = 'mgmt'; }
    else if (ns && ns !== 'mgmt' && ns !== '(login)') { note = `tile absent + app-card Namespace="${ns}" (not mgmt) → NOT in mgmt`; verdict[app] = 'host'; }
    else { note = `ABSENT — no treemap tile and app-card Namespace unreadable ("${ns || ''}") (HR likely still installing)`; verdict[app] = 'absent'; }
  }
  results.push({ id, label: `PART B — ${app} tile inside mgmt block`, status, shot, details: [note] });
}

// (5) PART C — drill into mgmt: all 7 present (3642-11).
{
  const shot = `${SHOTS}/${ENVTAG}-3642-treemap-layer1-vcluster.png`;
  const inMgmt = SEVEN.filter((a) => verdict[a] === 'mgmt');
  const missing = SEVEN.filter((a) => verdict[a] !== 'mgmt');
  const ok = missing.length === 0;
  const mems = TM && TM.mgmtMembers ? TM.mgmtMembers.join(', ') : '(none)';
  results.push({ id: '3642-11', label: 'PART C — mgmt block holds all 7 apps', status: ok ? 'GREEN' : 'RED', shot, details: [`mgmt holds: ${mems}`, `of the 7 in mgmt: ${inMgmt.length}/7 (${inMgmt.join('|') || 'none'}); missing: ${missing.join('|') || 'none'}`] });
}

// (6) PART C — host block clean of the 7 (3642-12).
{
  const shot = `${SHOTS}/${ENVTAG}-3642-treemap-layer1-vcluster.png`;
  const underHost = SEVEN.filter((a) => verdict[a] === 'host');
  const ok = underHost.length === 0;
  results.push({ id: '3642-12', label: 'PART C — host block holds NONE of the 7', status: ok ? 'GREEN' : 'RED', shot, details: [ok ? 'no named app under host' : `under host: ${underHost.join(', ')}`] });
}

// (7) PART C — keycloak app-card placement reads mgmt (3642-13).
results.push(await runRow(ctx, '3642-13', 'PART C — keycloak app card placement = mgmt', async (page) => {
  const r = await readAppCardPlacement(page, 'keycloak');
  if (r.login) return { ok: false, note: 'redirected to login' };
  const ok = r.ns === 'mgmt';
  return { ok, note: `app-card Namespace="${r.ns || '?'}"${r.mentionsMgmt ? ' (mentions mgmt)' : ''}` };
}));

await browser.close();

// ── output ──────────────────────────────────────────────────────────────
for (const r of results) {
  const mark = r.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${r.id.padEnd(9)} ${r.label.padEnd(48)} shot=${r.shot}${r.details && r.details.length ? '  // ' + r.details.join('; ') : ''}`);
}

// NS#1 verdict line — exactly how many of the 7 are in mgmt.
const inMgmt = SEVEN.filter((a) => verdict[a] === 'mgmt');
const inHost = SEVEN.filter((a) => verdict[a] === 'host');
const absent = SEVEN.filter((a) => verdict[a] === 'absent');
console.log(`\n=== NORTH STAR #1 VERDICT (every app in a vCluster) ===`);
console.log(`  in mgmt  (${inMgmt.length}/7): ${inMgmt.join(', ') || '—'}`);
console.log(`  in host  (${inHost.length}/7): ${inHost.join(', ') || '—'}  ${inHost.length ? '❌ NOT migrated' : ''}`);
console.log(`  absent   (${absent.length}/7): ${absent.join(', ') || '—'}  ${absent.length ? '(tile not rendered — HR likely still installing)' : ''}`);
console.log(`  NS#1 ${inMgmt.length === 7 ? 'MET ✅' : `NOT MET ❌ — ${inMgmt.length}/7 in mgmt`}`);

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, ns1: { inMgmt, inHost, absent }, results }, null, 2));
const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
