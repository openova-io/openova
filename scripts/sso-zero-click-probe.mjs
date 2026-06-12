#!/usr/bin/env node
// sso-zero-click-probe.mjs — per-app synthetic zero-click SSO walk (#3374).
//
// THE CONTRACT IT PROBES: typing the bare root URL of every external app
// on a Sovereign lands the operator signed in — no login UI, no token
// form, no second click (founder north star, #3374 §1). A 302-to-realm
// wire-proof is NOT a pass (#3150 lesson) — every row asserts an
// AUTHENTICATED LANDING MARKER in the final rendered page and the
// ABSENCE of login-form markers.
//
// Session model: ONE fresh Chromium context per run. The operator
// session is established exactly the way the acceptance walk does it —
// the handover URL into the console — then every bare URL must land
// signed in via the silent Keycloak chain (catalyst-pin). App-local
// state (bao localStorage token, app cookies) starts EMPTY, so a green
// row proves the zero-click chain, not a stale session.
//
// Anonymous rows (marketplace; the openova-flow "no anonymous data"
// assertion) run in a SECOND, never-authenticated context.
//
// Usage:
//   node scripts/sso-zero-click-probe.mjs \
//     --fqdn hw130.omantel.biz \
//     --handover-url 'https://console.hw130.omantel.biz/auth/handover?token=...' \
//     [--jwt-key /tmp/hw-priv.pem --deployment-id <id>]   # mint instead
//     [--rows grafana,openbao] [--json out.json]
//     [--expect-broken openbao,pdns-admin,...]  # negative validation:
//          exit 0 ONLY if exactly these rows are RED (proves the probe
//          reds on broken apps — #3374 DoD-6)
//
// Exit codes: 0 = all probed rows green (or --expect-broken satisfied),
// 1 = contract violations, 2 = harness error.
//
// Runs in CI (workflow_dispatch / cron in sso-probe.yaml) and on-env
// (bastion cron with --jwt-key). Requires the `playwright` package
// (resolvable from the repo root) + Chromium.

import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import crypto from 'node:crypto';

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
if (!FQDN) {
  console.error('FATAL: --fqdn is required (e.g. hw130.omantel.biz)');
  process.exit(2);
}
const host = (sub) => `https://${sub}.${FQDN}`;

// ── handover URL (session bootstrap) ─────────────────────────────────
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
    aud: [`https://console.${FQDN}`],
    sovereign_fqdn: FQDN,
    deployment_id: args['deployment-id'] || '',
    role: 'sovereign-admin',
    email_verified: true,
    iat: now,
    exp: now + 300,
    jti: crypto.randomUUID(),
  };
  const signingInput = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(claims))}`;
  const sig = execFileSync('openssl', ['dgst', '-sha256', '-sign', args['jwt-key']], { input: signingInput });
  return `https://console.${FQDN}/auth/handover?token=${signingInput}.${b64url(sig)}`;
}
const handoverURL = args['handover-url'] || (args['jwt-key'] ? mintHandoverURL() : null);
if (!handoverURL) {
  console.error('FATAL: provide --handover-url or --jwt-key (+ --deployment-id)');
  process.exit(2);
}

// ── row definitions ───────────────────────────────────────────────────
// Each row: { name, url, ctx: 'authed'|'anon', settleMs,
//   positive: [{kind:'url-includes'|'text'|'selector', v}],   ALL must hold
//   loginMarkers: [{kind, v}] }                                NONE may hold
// loginMarkers are the FAILURE detectors (login UI / token form / error
// page); positive markers are the authenticated-landing proof.
const ROWS = [
  {
    name: 'console', url: handoverURL, ctx: 'authed', settleMs: 6000,
    positive: [{ kind: 'url-includes', v: '/dashboard' }, { kind: 'text', v: 'Dashboard' }],
    loginMarkers: [{ kind: 'text', v: 'Sign in' }],
  },
  {
    name: 'grafana', url: `${host('grafana')}/`, ctx: 'authed', settleMs: 8000,
    positive: [{ kind: 'selector', v: 'button[aria-label="Profile"], [aria-label="Profile"]' }],
    loginMarkers: [
      { kind: 'selector', v: 'input[name="user"]' },
      { kind: 'url-includes', v: '/login' },
    ],
  },
  {
    name: 'gitea', url: `${host('gitea')}/`, ctx: 'authed', settleMs: 8000,
    positive: [{ kind: 'selector', v: 'a[href="/notifications"]' }],
    loginMarkers: [
      { kind: 'selector', v: 'form[action="/user/login"]' },
      { kind: 'selector', v: '#user_name' },
    ],
  },
  {
    name: 'harbor', url: `${host('registry')}/`, ctx: 'authed', settleMs: 10000,
    positive: [{ kind: 'url-includes', v: '/harbor/projects' }, { kind: 'text', v: 'Projects' }],
    loginMarkers: [{ kind: 'selector', v: 'input[name="login_username"], #login_username' }],
  },
  {
    // The founder-witnessed #3374 failure: bare /ui/ must land in the
    // signed-in vault UI — the token form is the hard red.
    name: 'openbao', url: `${host('bao')}/ui/`, ctx: 'authed', settleMs: 12000,
    positive: [{ kind: 'url-regex', v: '/ui/vault/(secrets|dashboard)' }],
    loginMarkers: [
      { kind: 'text', v: 'Sign in to OpenBao' },
      { kind: 'url-includes', v: '/vault/auth' },
    ],
  },
  {
    // DoD-3: zero clicks AND the OIDC callback still works (the
    // 0.1.11 regression test) — landing on /dashboard/ proves the
    // full /oidc/login -> KC -> /oidc/authorized -> /login -> session
    // chain executed.
    name: 'pdns-admin', url: `${host('pdns-admin')}/`, ctx: 'authed', settleMs: 12000,
    positive: [{ kind: 'url-includes', v: '/dashboard' }],
    loginMarkers: [
      { kind: 'selector', v: 'input[name="username"]' },
      { kind: 'text', v: 'Sign in using OpenID Connect' },
      { kind: 'text', v: 'ERR_TOO_MANY_REDIRECTS' },
    ],
  },
  {
    name: 'guacamole', url: `${host('guacamole')}/`, ctx: 'authed', settleMs: 15000,
    positive: [{ kind: 'url-includes', v: '/guacamole/' }, { kind: 'text', v: 'Connections' }],
    loginMarkers: [
      { kind: 'text', v: 'HTTP Status 404' },
      { kind: 'selector', v: 'input[name="username"]' },
    ],
  },
  {
    name: 'newapi', url: `${host('newapi')}/`, ctx: 'authed', settleMs: 10000,
    // new-api ships its own login UI; until the native-OIDC seam is
    // walked the honest positive is "a rendered app with NO login UI
    // and NO gateway error" — the login/error markers carry the row.
    positive: [{ kind: 'selector', v: 'body' }],
    loginMarkers: [
      { kind: 'text', v: 'upstream connect error' },
      { kind: 'text', v: 'Sign in' },
      { kind: 'text', v: 'Log in' },
      { kind: 'url-includes', v: '/login' },
    ],
  },
  {
    // Authed read: the gate must pass the operator through to the
    // flow-server (JSON descriptor / API surface).
    name: 'openova-flow', url: `${host('openova-flow')}/`, ctx: 'authed', settleMs: 10000,
    positive: [{ kind: 'text', v: 'openova-flow-server' }],
    loginMarkers: [{ kind: 'text', v: 'Internal Server Error' }],
  },
  {
    // Security half of row 9/10 (#3374 §3): an ANONYMOUS browser must
    // NOT receive flow data — it must be bounced into the KC chain.
    // RED today: the route serves the JSON descriptor with no auth.
    name: 'openova-flow-anon-denied', url: `${host('openova-flow')}/`, ctx: 'anon', settleMs: 8000,
    positive: [{ kind: 'url-regex', v: '(auth\\.|/realms/|/oauth2/|/protocol/openid-connect/)' }],
    loginMarkers: [{ kind: 'text', v: 'openova-flow-server' }],
  },
  {
    name: 'hubble', url: `${host('hubble')}/`, ctx: 'authed', settleMs: 12000,
    positive: [{ kind: 'text-regex', v: 'Hubble|Service Map|Namespace' }],
    loginMarkers: [
      { kind: 'text', v: 'Internal Server Error' },
      { kind: 'text', v: 'unauthorized_client' },
    ],
  },
  {
    // The IdP's own console: bare auth.<fqdn>/ must land the
    // sovereign-admin in the SOVEREIGN realm admin console — the
    // master-realm local form is the red.
    name: 'keycloak-admin', url: `${host('auth')}/`, ctx: 'authed', settleMs: 12000,
    positive: [{ kind: 'url-includes', v: '/admin/sovereign/console' }],
    loginMarkers: [
      { kind: 'url-includes', v: '/admin/master/console' },
      { kind: 'selector', v: 'input#username' },
    ],
  },
  {
    // By-design anonymous storefront — the assertion is that NO
    // spurious login FORM appears (#3374 §3 row 12). A header
    // "Sign in" nav LINK is intended storefront UX; a rendered
    // password input is the violation.
    name: 'marketplace', url: `${host('marketplace')}/`, ctx: 'anon', settleMs: 8000,
    positive: [{ kind: 'title-regex', v: 'Build Your Tenant|OpenOva' }],
    loginMarkers: [{ kind: 'selector', v: 'input[type="password"]' }],
  },
];

// ── marker evaluation ─────────────────────────────────────────────────
async function evalMarker(page, m) {
  switch (m.kind) {
    case 'url-includes': return page.url().includes(m.v);
    case 'url-regex': return new RegExp(m.v).test(page.url());
    case 'selector': return (await page.locator(m.v).count()) > 0;
    case 'text': {
      const body = await page.textContent('body').catch(() => '') || '';
      return body.includes(m.v);
    }
    case 'text-regex': {
      const body = await page.textContent('body').catch(() => '') || '';
      return new RegExp(m.v).test(body);
    }
    case 'title-regex': return new RegExp(m.v).test(await page.title().catch(() => ''));
    default: throw new Error(`unknown marker kind ${m.kind}`);
  }
}

async function probeRow(ctx, row) {
  const page = await ctx.newPage();
  const result = { name: row.name, url: row.url.replace(/token=[^&]+/, 'token=<redacted>'), status: 'RED', finalURL: '', details: [] };
  try {
    await page.goto(row.url, { waitUntil: 'load', timeout: 45000 }).catch((e) => {
      result.details.push(`goto: ${e.message.split('\n')[0]}`);
    });
    // Let the silent SSO bounce chain settle (KC 302s + SPA boot).
    await page.waitForTimeout(row.settleMs);
    await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
    result.finalURL = page.url();

    const positives = [];
    for (const m of row.positive) positives.push([m, await evalMarker(page, m)]);
    const logins = [];
    for (const m of row.loginMarkers) logins.push([m, await evalMarker(page, m)]);

    const allPositive = positives.every(([, ok]) => ok);
    const anyLogin = logins.some(([, hit]) => hit);
    for (const [m, ok] of positives) if (!ok) result.details.push(`missing positive marker ${m.kind}:${m.v}`);
    for (const [m, hit] of logins) if (hit) result.details.push(`LOGIN-UI marker present ${m.kind}:${m.v}`);
    result.status = allPositive && !anyLogin ? 'GREEN' : 'RED';
  } catch (e) {
    result.details.push(`error: ${e.message.split('\n')[0]}`);
  } finally {
    await page.close().catch(() => {});
  }
  return result;
}

// ── main ──────────────────────────────────────────────────────────────
const only = args.rows ? args.rows.split(',').map((s) => s.trim()) : null;
const rows = only ? ROWS.filter((r) => only.includes(r.name)) : ROWS;

// PROBE_CHROMIUM: optional explicit Chromium binary (e.g. when the
// resolvable playwright package's pinned browser build isn't installed
// but another build is — common on shared runners/bastions).
const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.PROBE_CHROMIUM || undefined,
});
const authedCtx = await browser.newContext({ ignoreHTTPSErrors: false });
const anonCtx = await browser.newContext({ ignoreHTTPSErrors: false });

// Establish the operator session FIRST (console row is also the probe
// of the handover entry itself).
const results = [];
for (const row of rows) {
  const ctx = row.ctx === 'anon' ? anonCtx : authedCtx;
  const res = await probeRow(ctx, row);
  results.push(res);
  const mark = res.status === 'GREEN' ? 'GREEN' : 'RED  ';
  console.log(`[${mark}] ${res.name.padEnd(26)} ${res.finalURL}${res.details.length ? '  // ' + res.details.join('; ') : ''}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), results }, null, 2));

const red = results.filter((r) => r.status === 'RED').map((r) => r.name);
if (args['expect-broken']) {
  // Negative validation (#3374 DoD-6): the probe proves it REDS on
  // broken rows — pass ONLY if the red set matches expectation.
  const expected = args['expect-broken'].split(',').map((s) => s.trim()).sort();
  const actual = [...red].sort();
  const match = JSON.stringify(expected) === JSON.stringify(actual);
  console.log(`\nnegative-validation: expected RED=[${expected}] actual RED=[${actual}] -> ${match ? 'MATCH' : 'MISMATCH'}`);
  process.exit(match ? 0 : 1);
}
console.log(`\n${results.length - red.length}/${results.length} rows GREEN${red.length ? `; RED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
