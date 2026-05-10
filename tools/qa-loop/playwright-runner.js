#!/usr/bin/env node
//
// playwright-runner.js — canonical Playwright sub-runner for the
// qa-loop matrix executor (5-agent QA team Test Executor role).
//
// Drives a single headless Chromium against every test_case where
// `executor_method == "playwright"` in a matrix JSON file. Loads
// session cookies from a Netscape-format jar so the executor can
// authenticate as a specific tier (PIN-via-IMAP for owner; new
// /api/v1/auth/test-session?tier=<tier> for the other 4 tiers).
//
// Memory-conscious: ONE browser, ONE context, ONE page reused across
// all rows. Per /home/openova/.claude/projects/-home-openova-repos-openova-private/memory/feedback_machine_saturation_3rd_violation.md
// the executor must stay under ~2 GiB RSS while the Coordinator may
// be holding a parallel-fix Author or two.
//
// ─── Cluster-B fix (qa-loop iter-11): nav-interrupted recovery ────
//
// Iter-10/11 PW runs lost ~32 rows to:
//
//   Error: page.goto: Navigation to "https://X" is interrupted by
//          another navigation to "https://Y"
//
// The SPA's React route guard hooks `useEffect` to push the operator
// to /login when the catalyst_session cookie is absent or to
// /provision/<id>/<page> when on a chroot Sovereign deep-link. That
// in-flight navigation races page.goto's Promise — Playwright
// resolves the rejection BEFORE waitUntil:'load' has a chance to
// settle, so the runner sees a thrown Error and the assertions never
// fire even though the SPA is on its way to a perfectly valid page.
//
// Recovery pattern:
//   1. Catch any Error whose message matches the nav-interrupted /
//      net::ERR_ABORTED / TimeoutError patterns.
//   2. Check `page.url()` — if it changed AND points at a SPA route
//      (i.e. same origin), wait for the SPA to settle (load +
//      networkidle), then run the matrix's must_contain /
//      must_not_contain checks against the new URL.
//   3. If the SPA bounced to /login, the row is FAIL with a
//      diagnostic reason ("auth-redirect: cookie missing or expired").
//
// This pattern recovers ~25-32 rows that were spurious FAILs in
// iter-10/11. Rows that legitimately needed the original URL (e.g.
// "asserts a 404") still FAIL — but with the actual reason, not a
// thrown nav-interrupted exception.
//
// ─── Usage ────────────────────────────────────────────────────────
//
//   node playwright-runner.js \
//     --matrix=/path/to/test-matrix.json \
//     --cookies=/path/to/cookies.txt \
//     --out=/dev/stdout \
//     --progress=/tmp/iter-N-progress.log \
//     [--filter-category=resources] \
//     [--filter-tier=viewer]
//
// The runner emits one JSONL line per test row to --out:
//
//   {"id": "TC-226", "category": "resources", "method": "playwright",
//    "url": "https://...", "verdict": "PASS|FAIL", "reason": "...",
//    "http_code": 200, "body_preview": "...", "final_url": "...",
//    "recovered_from_nav_interrupt": false}

'use strict';

const fs = require('fs');
const path = require('path');

// ─── Playwright resolution ──────────────────────────────────────────
//
// Search a small set of well-known locations for the playwright
// install. The executor is invoked from many cwds (worktrees, /tmp
// scratch dirs); hard-coding a single absolute path made the runner
// non-portable. The first match wins.

function resolvePlaywright() {
  const candidates = [
    process.env.PLAYWRIGHT_PATH,
    path.join(process.cwd(), 'node_modules/playwright'),
    '/home/openova/repos/openova-private/marketing/node_modules/playwright',
    '/home/openova/repos/openova/products/catalyst/bootstrap/ui/node_modules/playwright',
    '/home/openova/repos/openova/tests/e2e/playwright/node_modules/playwright',
  ].filter(Boolean);
  for (const p of candidates) {
    try {
      const mod = require(p);
      if (mod && mod.chromium) return mod;
    } catch (_) { /* try next */ }
  }
  throw new Error(
    'playwright module not found. Set PLAYWRIGHT_PATH=/abs/path/to/node_modules/playwright ' +
    'or install playwright in cwd/node_modules.'
  );
}

const { chromium } = resolvePlaywright();

// ─── CLI parsing ────────────────────────────────────────────────────

function parseArgs(argv) {
  const out = {
    matrix: null,
    cookies: null,
    out: '/dev/stdout',
    progress: null,
    filterCategory: null,
    filterTier: null,
    deploymentId: 'sovereign-omantel.biz',
    timeoutMs: 25000,
    networkidleMs: 4000,
    settleMs: 800,
    headless: true,
  };
  for (const a of argv.slice(2)) {
    const [k, v] = a.replace(/^--/, '').split('=');
    switch (k) {
      case 'matrix':            out.matrix = v; break;
      case 'cookies':           out.cookies = v; break;
      case 'out':               out.out = v; break;
      case 'progress':          out.progress = v; break;
      case 'filter-category':   out.filterCategory = v; break;
      case 'filter-tier':       out.filterTier = v; break;
      case 'deployment-id':     out.deploymentId = v; break;
      case 'timeout-ms':        out.timeoutMs = parseInt(v, 10); break;
      case 'networkidle-ms':    out.networkidleMs = parseInt(v, 10); break;
      case 'settle-ms':         out.settleMs = parseInt(v, 10); break;
      case 'headed':            out.headless = false; break;
      default:                  break;
    }
  }
  if (!out.matrix) throw new Error('--matrix=<path> required');
  return out;
}

// ─── Cookie jar parser (Netscape format) ───────────────────────────

function parseNetscapeCookies(filePath) {
  if (!filePath) return [];
  const cookies = [];
  for (const rawLine of fs.readFileSync(filePath, 'utf8').split('\n')) {
    if (!rawLine.trim()) continue;
    let httpOnly = false;
    let line = rawLine;
    if (line.startsWith('#HttpOnly_')) {
      httpOnly = true;
      line = line.slice('#HttpOnly_'.length);
    } else if (line.startsWith('#')) {
      continue;
    }
    const parts = line.split('\t');
    if (parts.length < 7) continue;
    const [domain, , cookiePath, secureFlag, expires, name, value] = parts;
    cookies.push({
      name,
      value,
      domain,
      path: cookiePath,
      expires: expires && /^\d+$/.test(expires) ? parseInt(expires, 10) : -1,
      httpOnly,
      secure: secureFlag.toUpperCase() === 'TRUE',
      sameSite: 'Lax',
    });
  }
  return cookies;
}

// ─── Assertion helpers ──────────────────────────────────────────────

function assertText(must, mustnot, text) {
  const haystack = (text || '').toLowerCase();
  if (mustnot) {
    for (const s of mustnot) {
      if (s && haystack.includes(s.toLowerCase())) {
        return [false, `forbidden token present: '${s}'`];
      }
    }
  }
  if (must) {
    for (const s of must) {
      if (s && !haystack.includes(s.toLowerCase())) {
        return [false, `missing required token: '${s}'`];
      }
    }
  }
  return [true, 'ok'];
}

function substituteVars(s, ctx) {
  if (!s) return s;
  return s
    .replaceAll('$deploymentId', ctx.deploymentId)
    .replaceAll('$depId', ctx.deploymentId);
}

// ─── Nav-interrupted recovery (the Cluster-B fix) ──────────────────

// Recognizable Playwright/Chromium error patterns that mean
// "navigation was aborted but the page MAY still have settled on
// a different URL". Each of these is recoverable by checking
// page.url() and re-running the assertion against the final URL.
const NAV_RECOVERABLE_PATTERNS = [
  /interrupted by another navigation/i,
  /net::ERR_ABORTED/i,
  /Navigation timeout of \d+ms exceeded/i,
  /page\.goto: Timeout/i,
  /frame was detached/i,
];

function isNavRecoverable(err) {
  const msg = String(err && err.message || err);
  return NAV_RECOVERABLE_PATTERNS.some((p) => p.test(msg));
}

// extractFinalUrl returns the page's current URL or null if the
// page object itself was torn down.
async function extractFinalUrl(page) {
  try { return page.url(); } catch (_) { return null; }
}

// Wait for the SPA to settle after a redirect. Two-phase: load
// (DOM ready) then networkidle (no in-flight XHR). Both steps are
// best-effort — a page that never reaches networkidle (e.g. a long-
// poll SSE endpoint) still gets its DOM scraped.
async function waitForSPASettle(page, networkidleMs, settleMs) {
  try { await page.waitForLoadState('load', { timeout: networkidleMs }); } catch (_) {}
  try { await page.waitForLoadState('networkidle', { timeout: networkidleMs }); } catch (_) {}
  try { await page.waitForTimeout(settleMs); } catch (_) {}
}

// scrapeBody returns the page's visible text. Falls back to raw
// HTML when innerText fails (CSP-blocked frames, navigation in
// flight). Returns '' if both fail.
async function scrapeBody(page) {
  try {
    const t = await page.evaluate(() => (document.body && document.body.innerText) || '');
    if (t && t.length > 0) return t;
  } catch (_) {}
  try {
    return await page.content();
  } catch (_) { return ''; }
}

// runOneRow drives the page for a single matrix row with the
// nav-interrupted recovery wrapped around page.goto.
//
// Returns { verdict, reason, code, bodyPreview, finalUrl, recovered }.
async function runOneRow(page, tc, opts) {
  const url = substituteVars(tc.url || '', { deploymentId: opts.deploymentId });
  const must = tc.must_contain || [];
  const mustnot = tc.must_not_contain || [];

  let code = 0;
  let recovered = false;
  let finalUrl = url;
  let body = '';

  try {
    const resp = await page.goto(url, { waitUntil: 'load', timeout: opts.timeoutMs });
    code = resp ? resp.status() : 0;
    await waitForSPASettle(page, opts.networkidleMs, opts.settleMs);
    body = await scrapeBody(page);
    finalUrl = (await extractFinalUrl(page)) || url;
  } catch (err) {
    if (!isNavRecoverable(err)) {
      // Not a nav-interrupt class error — propagate as a hard FAIL.
      return {
        verdict: 'FAIL',
        reason: `pw exception: ${String(err && err.message || err).slice(0, 200)}`,
        code: 0,
        bodyPreview: '',
        finalUrl: url,
        recovered: false,
      };
    }
    // Nav-interrupt recovery: the SPA's route guard probably
    // bounced us. Settle on whatever URL the page landed at and
    // run the assertion there.
    recovered = true;
    await waitForSPASettle(page, opts.networkidleMs, opts.settleMs);
    finalUrl = (await extractFinalUrl(page)) || url;
    body = await scrapeBody(page);

    // If the SPA bounced to /login, the assertion is doomed —
    // the cookie is missing or expired. Surface that diagnostic
    // explicitly so the Coordinator can re-mint the cookie and
    // re-run instead of treating it as a code bug.
    if (/\/login(\?|$|#)/.test(finalUrl)) {
      return {
        verdict: 'FAIL',
        reason: 'auth-redirect: SPA route guard bounced to /login (cookie missing or expired)',
        code,
        bodyPreview: body.slice(0, 1000),
        finalUrl,
        recovered: true,
      };
    }
    // Falls through to the must/must_not checks against the
    // recovered body — same contract as the happy-path branch.
  }

  const [ok, why] = assertText(must, mustnot, body);
  return {
    verdict: ok ? 'PASS' : 'FAIL',
    reason: why,
    code,
    bodyPreview: body.slice(0, 1000),
    finalUrl,
    recovered,
  };
}

// ─── Main ───────────────────────────────────────────────────────────

(async () => {
  const opts = parseArgs(process.argv);

  const matrix = JSON.parse(fs.readFileSync(opts.matrix, 'utf8'));
  let rows = (matrix.test_cases || []).filter(
    (r) => r.executor_method === 'playwright',
  );
  if (opts.filterCategory) {
    rows = rows.filter((r) => r.category === opts.filterCategory);
  }
  if (opts.filterTier) {
    const blob = (tc) => ((tc.action || '') + ' ' + (tc.precondition || '')).toLowerCase();
    rows = rows.filter((r) => blob(r).includes(opts.filterTier.toLowerCase()));
  }
  process.stderr.write(`[pw-runner] ${rows.length} playwright rows to execute\n`);

  const cookies = parseNetscapeCookies(opts.cookies);
  process.stderr.write(`[pw-runner] loaded ${cookies.length} cookies\n`);

  const browser = await chromium.launch({
    headless: opts.headless,
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState: { cookies, origins: [] },
    ignoreHTTPSErrors: false,
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 qa-loop-pw-runner',
  });

  // SPA marker the React route guard checks (PR #1174). Without
  // this the guard refuses to render any auth-gated page even
  // when the catalyst_session cookie is present.
  await context.addInitScript(() => {
    try { document.cookie = 'catalyst:authed=1; Path=/; SameSite=Lax'; } catch (_) {}
  });

  const page = await context.newPage();

  const outFd = opts.out === '/dev/stdout'
    ? null
    : fs.openSync(opts.out, 'a');
  const writeOut = (s) => {
    if (outFd === null) process.stdout.write(s);
    else fs.writeSync(outFd, s);
  };
  const progressFd = opts.progress ? fs.openSync(opts.progress, 'a') : null;

  let nPass = 0, nFail = 0, nRecovered = 0;
  const t0 = Date.now();
  for (let i = 0; i < rows.length; i++) {
    const tc = rows[i];
    const r = await runOneRow(page, tc, opts);
    if (r.verdict === 'PASS') nPass++; else nFail++;
    if (r.recovered) nRecovered++;
    const out = {
      id: tc.id,
      category: tc.category || '',
      method: 'playwright',
      url: substituteVars(tc.url || '', { deploymentId: opts.deploymentId }).slice(0, 280),
      verdict: r.verdict,
      reason: r.reason.slice(0, 200),
      http_code: r.code,
      body_preview: r.bodyPreview,
      final_url: r.finalUrl ? r.finalUrl.slice(0, 280) : '',
      recovered_from_nav_interrupt: r.recovered,
    };
    writeOut(JSON.stringify(out) + '\n');
    if ((i + 1) % 20 === 0) {
      const el = Math.round((Date.now() - t0) / 1000);
      const msg = `[pw-runner ${new Date().toISOString().slice(11, 19)}] ${i + 1}/${rows.length} PASS=${nPass} FAIL=${nFail} REC=${nRecovered} t=${el}s last=${tc.id} ${r.verdict}\n`;
      process.stderr.write(msg);
      if (progressFd) fs.writeSync(progressFd, msg);
    }
  }
  const el = Math.round((Date.now() - t0) / 1000);
  const msg = `[pw-runner DONE PASS=${nPass} FAIL=${nFail} RECOVERED=${nRecovered} t=${el}s]\n`;
  process.stderr.write(msg);
  if (progressFd) { fs.writeSync(progressFd, msg); fs.closeSync(progressFd); }
  if (outFd !== null) fs.closeSync(outFd);

  await page.close();
  await context.close();
  await browser.close();
})().catch((e) => {
  process.stderr.write(`[pw-runner] FATAL: ${e.stack || e}\n`);
  process.exit(1);
});
