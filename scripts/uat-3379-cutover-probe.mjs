#!/usr/bin/env node
// uat-3379-cutover-probe.mjs — live-env browser UAT for the #3379 cutover
// (Pillar-5 sovereignty) runbook
// `docs/ledger/uat-walkthrough/cutover-durable-true-deny-egress-and-faithful-pivot.md`
// + the `3379-NN` rows in `docs/ledger/UAT.md`.
//
// SIBLING of scripts/uat-console-probe.mjs (console structure) and
// scripts/sso-zero-click-probe.mjs (SSO landing). THIS probe owns the
// cutover SURFACES that the operator can SEE WITHOUT executing the
// (destructive, handover-gated) cutover:
//
//   • Settings → Sovereignty section renders ("Cluster sovereignty")
//   • the "Achieve True Sovereignty" CTA is present (tethered env)
//   • the tethered/sovereign state badge ("Tethered" on a fresh prov)
//   • the Settings sidebar / TOC "Sovereignty" anchor is a first-class
//     nav entry (not backend-only)
//   • the confirm modal renders the canonical 11-step cutover chain
//   • /jobs renders (the only canvas the cutover would surface on)
//
// ⛔ SCOPE: this probe NEVER triggers the cutover. The cutover EXECUTION
// (11 sequential Jobs + a 10-minute deny-egress NetworkPolicy hold) is a
// destructive, handover-gated operator action. Rows that require the
// cutover to have RUN (the 11 cutover-step-* rows, the per-step progress
// card, cutoverComplete=true, the deny-egress / registry-pivot proofs)
// are marked NOT-REACHED honestly with an explicit reason — NEVER faked
// GREEN. Rows whose ONLY realisation is a backend fact (sealed secret,
// CCNP, containerd pivot) are marked GAP-backend (a finding, not a ❌).
//
// Statuses a row can carry:
//   GREEN        — surface rendered + all positive markers held + no
//                  failure marker (login / 404). Acceptance.
//   RED          — a real defect: a surface that SHOULD render did not,
//                  or a login/404 marker held.
//   NOT-REACHED  — correct surface is gated behind a driven cutover
//                  execution (a separate gated walk). Screenshot captured,
//                  not asserted GREEN. (`reason` records why.)
//   GAP-backend  — no browser surface exists for this intent (backend
//                  fact only). A finding. No screenshot asserted.
//   GAP-missing-ui — a browser surface is INTENDED but unbuilt on the
//                  reachable (tethered, view-only) env.
//
// Markers/testids are derived from the bootstrap operator UI source
// (products/catalyst/bootstrap/ui/src) — the DEPLOYED console is that
// React app, NOT core/console (Astro). Sources of truth:
//   • pages/sovereign/SettingsPage.tsx     (SECTIONS[] + #sovereignty mount + TOC)
//   • widgets/sovereignty/SovereigntyCard.tsx     (badge + CTA + modal testids)
//   • widgets/sovereignty/CutoverProgressCard.tsx (progress card testids)
//   • shared/types/cutover.ts                     (canonical 11 CUTOVER_STEPS)
//   • pages/sovereign/SovereignSidebar.tsx        (settings nav + sub-nav)
//
// Usage:
//   node scripts/uat-3379-cutover-probe.mjs \
//     --fqdn hw167.omantel.biz \
//     --jwt-key /tmp/hw-priv.pem --deployment-id 28d4e96f96407bbb \
//     [--handover-url 'https://console.<fqdn>/auth/handover?token=...'] \
//     [--rows 3379-01,3379-02] \
//     [--shots docs/sessions/2026-06-19/evidence] [--json out.json]
//
// Exit codes: 0 = no RED rows (GREEN + NOT-REACHED + GAP all OK),
//             1 = ≥1 RED row (a real defect), 2 = harness error.

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

// ── handover URL (session bootstrap) — same mint as the sibling probes ──
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
  // a console roll (e.g. a mid-walk catalyst-ui upgrade) drops the session
  // and bounces to /login?next=… — catch the URL too so even a gated row
  // records the base-surface outage rather than masking it as N/RCH.
  { kind: 'url-regex', v: '/login(\\?|$)' },
];
const NOTFOUND = [
  { kind: 'text', v: 'HTTP Status 404' },
  { kind: 'text', v: 'Page not found' },
  { kind: 'text', v: 'upstream connect error' },
];

// ── row definitions (id ↔ docs/ledger/UAT.md `3379-NN` row) ─────────────
// Each: { id, runbook:'cutover', url, settleMs,
//         steps?:[{do, sel?, text?, ms?}]   — actions BEFORE marker eval,
//         positive:[ALL must hold], failure:[NONE may hold],
//         expect:'GREEN'|'NOT-REACHED'|'GAP-backend'|'GAP-missing-ui',
//         reason? }
//
// `steps` action vocab:
//   {do:'goto', url}              navigate (rarely needed; url handles it)
//   {do:'click', sel}            click first match of CSS selector
//   {do:'scroll', sel}          scrollIntoView the first match
//   {do:'waitFor', sel, ms?}    wait for selector visible (default 8s)
//   {do:'waitText', text, ms?}  wait until body contains text
//   {do:'wait', ms}             dumb settle
//
// Rows expecting GREEN are ASSERTED (positive must hold, failure must not).
// Rows expecting NOT-REACHED / GAP-* are RECORDED ONLY — a screenshot is
// taken and the honest reason logged; they never gate the exit code and
// never claim a surface that requires a driven cutover.

const ROWS = [
  // ════════════════════════════════════════════════════════════════════
  // Section 1 — TRIGGER: the Sovereignty section + "Achieve True
  // Sovereignty" CTA + state badge render WITHOUT executing the cutover.
  // These are browser-reachable on a tethered env → ASSERTED GREEN.
  // ════════════════════════════════════════════════════════════════════

  // 3379-01 — Settings → Sovereignty section + "Achieve True Sovereignty" CTA.
  // Navigate to /settings, click the Sovereignty TOC anchor, assert the
  // SovereigntyCard ("Cluster sovereignty" heading) + the CTA button text.
  { id: '3379-01', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'GREEN',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="settings-sovereignty"]', ms: 10000 },
      { do: 'scroll', sel: '[data-testid="sovereignty-card"]' },
      { do: 'wait', ms: 800 },
    ],
    positive: [
      { kind: 'url-includes', v: '/settings' },
      { kind: 'selector', v: '[data-testid="sovereignty-card"]' },
      { kind: 'text', v: 'Cluster sovereignty' },
      { kind: 'selector', v: '[data-testid="cutover-start-button"]' },
      { kind: 'text', v: 'Achieve True Sovereignty' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3379-02 — Sweep the nav + Settings sidebar/TOC for the "Sovereignty"
  // entry (the cutover trigger is a first-class console surface). The
  // Settings TOC has data-testid="settings-toc-sovereignty" with the
  // visible label "Sovereignty"; the section card id="sovereignty".
  { id: '3379-02', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'GREEN',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-toc"]', ms: 20000 },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="settings-toc-sovereignty"]' },
      { kind: 'text', v: 'Sovereignty' },
      // the section mount is a bare <div data-testid="settings-sovereignty">
      // wrapping <SovereigntyCard/> (NOT a SectionCard, so no
      // settings-section-sovereignty testid is emitted — SettingsPage.tsx).
      { kind: 'selector', v: '[data-testid="settings-sovereignty"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3379-02b (extra) — the state BADGE renders + reads "Tethered" on a
  // fresh (un-cut-over) prov. Asserts the tethered/sovereign indicator is
  // present AND honestly reads tethered (no premature "Sovereign").
  { id: '3379-02b', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'GREEN',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="sovereignty-badge"]', ms: 10000 },
      { do: 'scroll', sel: '[data-testid="sovereignty-badge"]' },
      { do: 'wait', ms: 600 },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="sovereignty-badge"]' },
      { kind: 'text', v: 'Tethered' },
      // data-cutover-state attribute exposes the honest machine state
      { kind: 'selector', v: '[data-testid="sovereignty-card"][data-cutover-state="tethered"]' },
    ],
    // a fresh prov must NOT show the completed state
    failure: [...LOGIN, ...NOTFOUND, { kind: 'selector', v: '[data-testid="sovereignty-stats"]' }] },

  // 3379-02c (extra) — the confirm modal renders the canonical 11-step
  // chain WITHOUT firing it. Open the CTA → assert the modal + a couple of
  // canonical step labels + the egress-block warning + Cancel (we click
  // Cancel; we NEVER click "Start cutover").
  { id: '3379-02c', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'GREEN',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="cutover-start-button"]', ms: 10000 },
      { do: 'click', sel: '[data-testid="cutover-start-button"]' },
      { do: 'waitFor', sel: '[data-testid="cutover-confirm-modal"]', ms: 8000 },
      { do: 'wait', ms: 600 },
    ],
    positive: [
      { kind: 'selector', v: '[data-testid="cutover-confirm-modal"]' },
      // canonical step labels from CUTOVER_STEPS (11-step chain)
      { kind: 'text', v: 'Mirror GitOps repository' },
      { kind: 'text', v: 'Egress-block self-test' },
      { kind: 'text', v: 'Pivot Crossplane providers' },
      // the destructive-op warning + the (un-clicked) Start control
      { kind: 'selector', v: '[data-testid="cutover-confirm-button"]' },
      { kind: 'selector', v: '[data-testid="cutover-confirm-cancel"]' },
    ],
    failure: [...LOGIN, ...NOTFOUND],
    // close the modal without firing — handled in the runner post-eval
    closeModal: true },

  // ════════════════════════════════════════════════════════════════════
  // Section 2 — STATUS INDICATOR: per-step progress card + cutoverComplete.
  // The CutoverProgressCard ([data-testid="cutover-progress-card"]) ONLY
  // mounts when a cutover is in-flight or complete. On a fresh tethered
  // env it is correctly absent → NOT-REACHED (gated behind a driven
  // cutover). We screenshot the Sovereignty section to record the honest
  // tethered state.
  // ════════════════════════════════════════════════════════════════════

  // 3379-03 — cutover progress card ("Step N of 11", percent bar).
  { id: '3379-03', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'progress card (cutover-progress-card / "Step N of 11" / %% bar) mounts only while a cutover is in-flight or complete; requires a driven cutover execution (separate gated walk). Tethered env: card correctly absent.',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="sovereignty-card"]', ms: 10000 },
      { do: 'scroll', sel: '[data-testid="sovereignty-card"]' },
      { do: 'wait', ms: 600 },
    ],
    // recorded-only probe: was the card present? (expected absent on tethered)
    positive: [{ kind: 'selector', v: '[data-testid="cutover-progress-card"]' }],
    failure: [...LOGIN] },

  // 3379-04 — terminal "Sovereign — tethers severed" / cutoverComplete.
  { id: '3379-04', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'the cutoverComplete end-state (sovereignty-stats / "Sovereignty achieved" / Crown "Sovereign" badge) only renders after a COMPLETE cutover; requires a driven cutover execution (separate gated walk). Tethered env honestly shows "Tethered".',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="sovereignty-card"]', ms: 10000 },
      { do: 'scroll', sel: '[data-testid="sovereignty-card"]' },
      { do: 'wait', ms: 600 },
    ],
    positive: [{ kind: 'selector', v: '[data-testid="sovereignty-stats"]' }],
    failure: [...LOGIN] },

  // ════════════════════════════════════════════════════════════════════
  // Section 3 — /jobs canvas. The canvas itself renders WITHOUT a cutover
  // → that base row is ASSERTED GREEN. The cutover GROUP + the 11
  // cutover-step-* rows only exist if the cutover FIRED → NOT-REACHED.
  // ════════════════════════════════════════════════════════════════════

  // 3379-05 — /jobs canvas renders (populated activity list, not spinner/login).
  { id: '3379-05', runbook: 'cutover', url: `${C}/jobs`, settleMs: 6000,
    expect: 'GREEN',
    steps: [{ do: 'wait', ms: 2000 }],
    positive: [
      { kind: 'url-includes', v: '/jobs' },
      // populated canvas: real lifecycle installs render + the Status header
      { kind: 'text-regex', v: 'Install |Flux|bootstrap|Blueprint' },
      { kind: 'text', v: 'Status' },
    ],
    failure: [...LOGIN, ...NOTFOUND] },

  // 3379-06 — the cutover GROUP + 11 cutover-step-* rows on /jobs.
  { id: '3379-06', runbook: 'cutover', url: `${C}/jobs`, settleMs: 6000,
    expect: 'NOT-REACHED',
    reason: 'the /jobs cutover group + its 11 cutover-step-* rows only ingest once the cutover has been FIRED; on a fresh tethered env there are 0 [data-testid^="cutover-step-"] rows (the self-sovereign-cutover Blueprint is INSTALLED so its name appears in the App/Kind filter + its install-job step labels, but NO live cutover EXECUTION rows render). Requires a driven cutover execution (separate gated walk).',
    steps: [{ do: 'wait', ms: 2000 }],
    // recorded-only authoritative probe: count live cutover EXECUTION rows by
    // testid (NOT body text — "cutover"/step-name tokens collide with the
    // INSTALLED self-sovereign-cutover Blueprint's filter entry + install
    // job labels). 0 rows = correctly absent on a tethered env.
    positive: [{ kind: 'selector', v: '[data-testid^="cutover-step-"]' }],
    failure: [...LOGIN] },

  // 3379-07 / 3379-08 — (the runbook's 11-step-tree detail rows) — same
  // gating as 3379-06; recorded NOT-REACHED, no separate surface to add.
  { id: '3379-07', runbook: 'cutover', url: `${C}/jobs`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'per-step cutover tree detail (gitea-mirror … crossplane-provider-pivot) is part of the same /jobs cutover group; only present after a driven cutover execution (separate gated walk). 0 live cutover-step-* execution rows on the tethered env.',
    steps: [{ do: 'wait', ms: 1500 }],
    positive: [{ kind: 'selector', v: '[data-testid^="cutover-step-"]' }],
    failure: [...LOGIN] },

  { id: '3379-08', runbook: 'cutover', url: `${C}/jobs`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'per-step cutover status (pending/running/done/failed transitions) is observable only on a driven cutover execution (separate gated walk). 0 live cutover-step-* execution rows on the tethered env.',
    steps: [{ do: 'wait', ms: 1500 }],
    positive: [{ kind: 'selector', v: '[data-testid^="cutover-step-"]' }],
    failure: [...LOGIN] },

  // 3379-09 — per-row Re-run button on a FAILED cutover step.
  { id: '3379-09', runbook: 'cutover', url: `${C}/jobs`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'the per-row Re-run/Retry control is gated to a FAILED cutover-step-* row, which can only exist after a driven cutover execution that fails a step (separate gated walk). No cutover-step-* rows on the tethered env (a generic Retry affordance exists on the canvas, but not on any cutover step).',
    steps: [{ do: 'wait', ms: 1500 }],
    // authoritative: a Re-run on a cutover step requires a cutover-step-* row
    positive: [{ kind: 'selector', v: '[data-testid^="cutover-step-"]' }],
    failure: [...LOGIN] },

  // ════════════════════════════════════════════════════════════════════
  // Section 4 — POST-CUTOVER end-state (after a COMPLETE cutover).
  // ════════════════════════════════════════════════════════════════════

  // 3379-10 — /jobs cutover group reads all-11-green.
  { id: '3379-10', runbook: 'cutover', url: `${C}/jobs`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'the all-11-green cutover group (every cutover-step-* Succeeded, incl. egress-block-test + registry-pivot) is reachable ONLY on an env where the cutover completed; requires a driven cutover execution (separate gated walk). 0 live cutover-step-* execution rows on the tethered env.',
    steps: [{ do: 'wait', ms: 1500 }],
    positive: [{ kind: 'selector', v: '[data-testid^="cutover-step-"]' }],
    failure: [...LOGIN] },

  // 3379-11 — Settings "Sovereign — tethers severed" steady end-state.
  { id: '3379-11', runbook: 'cutover', url: `${C}/settings`, settleMs: 5000,
    expect: 'NOT-REACHED',
    reason: 'the steady post-cutover "Sovereign — tethers severed" rendering (Crown badge + CTA hidden + sovereignty-stats) is reachable ONLY after a complete cutover; requires a driven cutover execution (separate gated walk). Tethered env shows the "Tethered" badge + CTA.',
    steps: [
      { do: 'waitFor', sel: '[data-testid="settings-page"]', ms: 20000 },
      { do: 'click', sel: '[data-testid="settings-toc-sovereignty"]' },
      { do: 'waitFor', sel: '[data-testid="sovereignty-card"]', ms: 10000 },
      { do: 'scroll', sel: '[data-testid="sovereignty-card"]' },
      { do: 'wait', ms: 600 },
    ],
    positive: [{ kind: 'selector', v: '[data-testid="sovereignty-card"][data-cutover-state="sovereign"]' }],
    failure: [...LOGIN] },

  // ════════════════════════════════════════════════════════════════════
  // Section 5 — Backend-only proofs (NO browser surface — GAP findings).
  // Recorded as GAP-backend; no screenshot asserted (nothing to render).
  // ════════════════════════════════════════════════════════════════════

  { id: '3379-12', runbook: 'cutover', expect: 'GAP-backend',
    reason: '#3678 true deny-egress — the 600s default-deny-egress CCNP (cutover-egress-block) + in-Job call-home assertion. NetworkPolicy + Job, no console surface.' },
  { id: '3379-13', runbook: 'cutover', expect: 'GAP-backend',
    reason: '#3671 faithful registry pivot — registriesYamlActive=v2 rewrites node containerd registries.yaml to local Harbor; per-node v2 acks. No console surface.' },
  { id: '3379-14', runbook: 'cutover', expect: 'GAP-backend',
    reason: '#3667 durable seal — cutoverComplete=true sealed in OpenBao (secret/catalyst/cutover-complete) so a chart upgrade cannot revert it. Sealed secret, no console surface.' },
  { id: '3379-15', runbook: 'cutover', expect: 'GAP-backend',
    reason: '#3681 audit fidelity — cutoverStartedAt written once (true T0); resume advances cutoverLastAttemptStartedAt. status-ConfigMap fields, no console surface.' },
  { id: '3379-16', runbook: 'cutover', expect: 'GAP-backend',
    reason: '#3695 zero residual tether — every external-registry workload + the ghcr-pull secret re-keyed to local Harbor; no live pod references a mothership registry. Pod image refs + secret key, no console surface.' },
];

// ── marker evaluation (from the sibling probes + count-gte) ─────────────
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

// ── step executor — navigates INTO a surface before markers are read ────
async function runSteps(page, row, result) {
  for (const s of row.steps || []) {
    try {
      switch (s.do) {
        case 'goto':
          await page.goto(s.url, { waitUntil: 'load', timeout: 45000 });
          break;
        case 'click':
          await page.locator(s.sel).first().click({ timeout: s.ms || 8000 });
          break;
        case 'scroll':
          await page.locator(s.sel).first().scrollIntoViewIfNeeded({ timeout: s.ms || 5000 });
          break;
        case 'waitFor':
          await page.locator(s.sel).first().waitFor({ state: 'visible', timeout: s.ms || 8000 });
          break;
        case 'waitText':
          await page.waitForFunction(
            (t) => document.body && document.body.innerText.includes(t),
            s.text, { timeout: s.ms || 8000 });
          break;
        case 'wait':
          await page.waitForTimeout(s.ms || 500);
          break;
        default:
          result.details.push(`step: unknown do=${s.do}`);
      }
    } catch (e) {
      // a missing step target is itself diagnostic (e.g. CTA absent) — log
      // but keep going so the screenshot + markers still capture the state.
      result.details.push(`step ${s.do}(${s.sel || s.text || s.ms}): ${e.message.split('\n')[0]}`);
    }
  }
}

async function probeRow(ctx, row) {
  // Section-5 GAP-backend rows have no URL / no browser surface — record
  // the finding directly, no page.
  if (!row.url) {
    return {
      id: row.id, runbook: row.runbook, status: row.expect || 'GAP-backend',
      finalURL: '(no UI surface)', shot: '', reason: row.reason || '', details: [],
    };
  }

  const page = await ctx.newPage();
  const shot = `${SHOTS}/${ENVTAG}-${row.id}.png`;
  const result = {
    id: row.id, runbook: row.runbook, status: 'RED', finalURL: '',
    shot, reason: row.reason || '', details: [],
  };
  try {
    await page.goto(row.url, { waitUntil: 'load', timeout: 45000 })
      .catch((e) => result.details.push(`goto: ${e.message.split('\n')[0]}`));
    await page.waitForTimeout(row.settleMs || 4000);
    await page.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});

    // navigate into the surface (Settings → Sovereignty, open modal, …)
    await runSteps(page, row, result);

    result.finalURL = page.url();
    await page.screenshot({ path: shot, fullPage: true })
      .catch((e) => result.details.push(`shot: ${e.message.split('\n')[0]}`));

    // evaluate markers
    const pos = []; for (const m of (row.positive || [])) pos.push([m, await evalMarker(page, m)]);
    const neg = []; for (const m of (row.failure || [])) neg.push([m, await evalMarker(page, m)]);
    const allPositive = pos.length > 0 && pos.every(([, ok]) => ok);
    const anyFailure = neg.some(([, hit]) => hit);

    if (row.expect === 'GREEN') {
      // asserted: surface must render, no login/404
      for (const [m, ok] of pos) if (!ok) result.details.push(`missing positive ${m.kind}:${m.v}`);
      for (const [m, hit] of neg) if (hit) result.details.push(`FAILURE marker ${m.kind}:${m.v}`);
      result.status = allPositive && !anyFailure ? 'GREEN' : 'RED';
    } else {
      // NOT-REACHED / GAP-missing-ui — recorded only. A login/404 redirect
      // is still a real defect (the BASE surface broke), so flag RED on
      // that; otherwise keep the honest gated status. Note whether the
      // gated surface happened to be present (it should NOT be on tethered).
      if (anyFailure) {
        for (const [m, hit] of neg) if (hit) result.details.push(`FAILURE marker ${m.kind}:${m.v}`);
        result.status = 'RED';
        result.details.push('base surface redirected to login/404 — defect, not the gated cutover surface');
      } else {
        result.status = row.expect || 'NOT-REACHED';
        result.details.push(allPositive
          ? 'NOTE: gated cutover surface UNEXPECTEDLY present on a tethered env — investigate'
          : 'gated surface correctly absent (cutover not executed)');
      }
    }

    // explicitly dismiss any opened confirm modal — we NEVER fire the cutover
    if (row.closeModal) {
      await page.locator('[data-testid="cutover-confirm-cancel"]').first()
        .click({ timeout: 4000 }).catch(() => {});
    }
  } catch (e) {
    result.details.push(`error: ${e.message.split('\n')[0]}`);
    // on a hard error, GREEN rows are RED; gated rows keep their honest status
    result.status = row.expect === 'GREEN' ? 'RED' : (row.expect || 'NOT-REACHED');
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

// Bootstrap the zero-click session ONCE (handover URL mints the cookie),
// then every row reuses the authed context — same pattern as the sibling
// probes. The cutover surfaces all live behind the sovereign-admin session.
{
  const boot = await authedCtx.newPage();
  await boot.goto(handoverURL, { waitUntil: 'load', timeout: 45000 }).catch(() => {});
  await boot.waitForTimeout(4000);
  await boot.waitForLoadState('networkidle', { timeout: 15000 }).catch(() => {});
  await boot.close().catch(() => {});
}

const results = [];
for (const row of rows) {
  const res = await probeRow(authedCtx, row);
  results.push(res);
  const tag = res.status === 'GREEN' ? 'GREEN'
    : res.status === 'RED' ? 'RED  '
    : res.status.startsWith('GAP') ? 'GAP  '
    : 'N/RCH';
  const suffix = res.details.length ? '  // ' + res.details.join('; ') : (res.reason ? '  // ' + res.reason : '');
  console.log(`[${tag}] ${res.id.padEnd(10)} ${(res.finalURL || '').padEnd(46)}${res.shot ? ' shot=' + res.shot : ''}${suffix}`);
}
await browser.close();

if (args.json) writeFileSync(args.json, JSON.stringify({ fqdn: FQDN, at: new Date().toISOString(), shots: SHOTS, results }, null, 2));

const red = results.filter((r) => r.status === 'RED').map((r) => r.id);
const green = results.filter((r) => r.status === 'GREEN').length;
const notReached = results.filter((r) => r.status === 'NOT-REACHED').length;
const gap = results.filter((r) => r.status.startsWith('GAP')).length;
console.log(`\nTALLY: ${green} GREEN · ${red.length} RED · ${notReached} NOT-REACHED · ${gap} GAP  (of ${results.length})${red.length ? `\nRED: ${red.join(', ')}` : ''}`);
process.exit(red.length ? 1 : 0);
