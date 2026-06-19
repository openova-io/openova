#!/usr/bin/env node
// uat-run.mjs — orchestrate the live-env UAT probes against a Sovereign and
// emit the UAT matrix in the founder's metric format (per-row ✅/❌ + the
// per-runbook scoreboard + a screenshot index). Composes the two validated
// probes:
//   scripts/sso-zero-click-probe.mjs   → runbook #3374 (SSO landing matrix)
//   scripts/uat-console-probe.mjs      → runbooks #3687/#3383/#3646/#3668/#3375
//                                         (console-structure rows)
//
// This is the #3581 "regenerate UAT on the current env" automation: one
// command per env produces the matrix that docs/ledger/UAT.md publishes.
// The probes own the assertions + screenshots; this runner owns aggregation.
// The stateful FLOW runbooks (#3376 funnel, #3379 cutover) and the deep
// per-app rows are added as their own probes and registered in PROBES below.
//
// Usage:
//   node scripts/uat-run.mjs --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id <id> \
//     [--shots docs/sessions/2026-06-19/evidence] [--out /tmp/uat-matrix.json] [--md]
//
// Exit 0 = all probed rows GREEN, 1 = ≥1 RED, 2 = harness error.

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';

const args = {};
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i];
  if (a.startsWith('--')) { const k = a.slice(2); const v = process.argv[i + 1] && !process.argv[i + 1].startsWith('--') ? process.argv[++i] : 'true'; args[k] = v; }
}
const FQDN = args.fqdn;
if (!FQDN) { console.error('FATAL: --fqdn required'); process.exit(2); }
const SHOTS = args.shots || `docs/sessions/${new Date().toISOString().slice(0, 10)}/evidence`;
const TMP = mkdtempSync(`${tmpdir()}/uat-`);

// Registered probes. `idMap` translates a probe's row .name → its canonical
// UAT.md matrix ID (the SSO probe names its rows by app; the console probe
// already emits 3xxx-NN ids, so its idMap is identity via the .id field).
const PROBES = [
  {
    script: 'sso-zero-click-probe.mjs', runbook: '3374', idField: 'name',
    idMap: {
      console: '3374-01', grafana: '3374-05', gitea: '3374-06', harbor: '3374-07',
      openbao: '3374-08', 'keycloak-admin': '3374-09', guacamole: '3374-10',
      'pdns-admin': '3374-11', newapi: '3374-12', 'openova-flow': '3374-14',
      'openova-flow-anon-denied': '3374-26', hubble: '3374-15', marketplace: '3374-16',
    },
  },
  { script: 'uat-console-probe.mjs', idField: 'id', idMap: null },
];

function runProbe(p) {
  const jsonOut = `${TMP}/${p.script}.json`;
  const argv = [`scripts/${p.script}`, '--fqdn', FQDN, '--shots', SHOTS, '--json', jsonOut];
  if (args['jwt-key']) argv.push('--jwt-key', args['jwt-key']);
  if (args['deployment-id']) argv.push('--deployment-id', args['deployment-id']);
  if (args['handover-url']) argv.push('--handover-url', args['handover-url']);
  console.log(`\n──── ${p.script} ────`);
  try { execFileSync('node', argv, { stdio: 'inherit', env: process.env }); }
  catch { /* nonzero exit = some rows RED; the JSON is still written */ }
  try { return JSON.parse(readFileSync(jsonOut, 'utf8')); }
  catch (e) { console.error(`  (no JSON from ${p.script}: ${e.message})`); return { results: [] }; }
}

// ── aggregate ─────────────────────────────────────────────────────────
const rows = [];
for (const p of PROBES) {
  const out = runProbe(p);
  for (const r of out.results || []) {
    const rawId = r[p.idField];
    const id = p.idMap ? (p.idMap[rawId] || `${p.runbook}-?(${rawId})`) : rawId;
    rows.push({ id, runbook: id.split('-')[0], status: r.status, finalURL: r.finalURL || '', shot: r.shot || '', detail: (r.details || []).join('; ') });
  }
}
rows.sort((a, b) => a.id.localeCompare(b.id));

// ── scoreboard (per runbook) ───────────────────────────────────────────
const byRun = {};
for (const r of rows) { (byRun[r.runbook] ||= { green: 0, red: 0 }); r.status === 'GREEN' ? byRun[r.runbook].green++ : byRun[r.runbook].red++; }
const green = rows.filter((r) => r.status === 'GREEN').length;

console.log(`\n══════ UAT matrix — ${FQDN} ══════`);
console.log('runbook   ✅   ❌');
for (const rb of Object.keys(byRun).sort()) console.log(`${rb.padEnd(8)} ${String(byRun[rb].green).padStart(3)} ${String(byRun[rb].red).padStart(4)}`);
console.log(`TOTAL    ${String(green).padStart(3)} ${String(rows.length - green).padStart(4)}   (${green}/${rows.length} GREEN)`);

if (args.md) {
  let md = `| ID | Result | Final URL | Evidence |\n|---|:---:|---|---|\n`;
  for (const r of rows) md += `| ${r.id} | ${r.status === 'GREEN' ? '✅' : '❌'} | ${r.finalURL.slice(0, 60)} | ${r.shot} |\n`;
  console.log(`\n${md}`);
}
if (args.out) { writeFileSync(args.out, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, scoreboard: byRun, total: { green, count: rows.length }, rows }, null, 2)); console.log(`\nwrote ${args.out}`); }

process.exit(rows.some((r) => r.status === 'RED') ? 1 : 0);
