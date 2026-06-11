# hw126 Grafana SSO login walk — 2026-06-11

**Verdict: FAIL** — PIN auth completes end-to-end, but the OAuth continuation
back to Grafana is dropped; the user lands authenticated in the **console
dashboard**, not in Grafana.

Refs #3263 #2737

## What was walked (real UI, real email, real PIN)

| # | Action | Result | Evidence |
|---|--------|--------|----------|
| 1 | `browser_navigate` to `https://grafana.hw126.omantel.biz` | Grafana login page with "Sign in with OpenOva SSO" button | `hw126-grafana-01-login.png` |
| 2 | Click "Sign in with OpenOva SSO" | Redirects through KC `catalyst-pin` broker → `console.hw126.omantel.biz/login` PIN-email screen (carrying `next=https://api.hw126.omantel.biz/oidc/auth?...redirect_uri=auth.hw126.../broker/catalyst-pin/endpoint`) | `hw126-grafana-02-pin-email-entry.png` |
| 3 | Type `emrah.baysal@openova.io`, click "Send code" | Advances to `/login/verify` with a `requestId`; PIN email triggered | — |
| 4 | Poll `emrah.baysal@openova.io` mailbox over IMAP (`mail.openova.io:993`, read-only) | New email arrived first poll: UID 598 (baseline 597), subject `Your OpenOva sign-in code: 429166` | — |
| 5 | Type PIN `429166` into the 6-digit input | Auto-submits → `POST /api/v1/auth/pin/verify` → `200 {"ok":true}` (12 bytes) | — |
| 6 | Observe landing | **Lands on `console.hw126.omantel.biz/dashboard`** (console treemap, "E" avatar = authenticated) — NOT Grafana | `hw126-grafana-sso-FAIL.png` |

Final URL: `https://console.hw126.omantel.biz/dashboard`
Landed authenticated: **yes, but into the console** (wrong app).
PIN read from mailbox: **yes** (6 digits, real Stalwart inbox).

## Root cause

The `next` continuation URL that must complete the OAuth roundtrip is
**cross-host** — it points to `https://api.hw126.omantel.biz/oidc/auth?...`
(whose `redirect_uri` is `https://auth.hw126.omantel.biz/realms/sovereign/broker/catalyst-pin/endpoint`).

`VerifyPinPage.tsx` reads this `next` (line 32) and, on a successful verify,
hands it to `sanitizeNextParam(next)` (line 139). `sanitizeNextParam`
(`app/auth-gate.ts:67`) is a CWE-601 open-redirect guard that **rejects any
absolute URL with a scheme** (line 77) and anything not starting with a single
`/` (line 81). The cross-host `https://api.hw126...` value is therefore
rejected → returns `undefined` → `target` falls back to `sovereignDefault`
(`/dashboard`, line 137-139). The browser never returns to the KC
`catalyst-pin` broker endpoint, KC never issues Grafana an authorization code,
and Grafana never logs the user in.

The `POST /api/v1/auth/pin/verify` request body is `{email, pin, requestId}`
only — it does **not** carry the `next` URL, and the 200 response is a bare
`{"ok":true}` with **no** `kcBrokerURL` (the field the silent-SSO path at
`VerifyPinPage.tsx:113` would `window.location.replace` to). So neither the
client-side `next` follow nor the server-side `kcBrokerURL` redirect fires —
both legs of the SSO continuation are absent on this prov.

## Fix direction (not applied here — this is a verification artifact)

Two non-exclusive options:
1. **Server-side (preferred, matches the documented G113 design):** catalyst-api's
   `pin/verify` should return `kcBrokerURL` (the `next` OAuth `/oidc/auth`
   continuation) when the login was initiated by an SSO broker handshake, so
   `VerifyPinPage.tsx:113-115` hard-navigates the browser back into the OAuth
   roundtrip. Today it returns a bare `{"ok":true}`.
2. **Client-side:** allow `sanitizeNextParam` to accept an absolute `next` that
   targets a **trusted same-Sovereign host** (the `api.<sov-fqdn>` /
   `auth.<sov-fqdn>` family of the current origin) instead of rejecting every
   absolute URL, so the OAuth continuation completes while genuinely-external
   hosts stay blocked.

Same failure class as `feedback_sso_wireproof_not_landed_loggedin.md`: the
302-to-issuer wiring is correct, but the app never lands the user logged in.
