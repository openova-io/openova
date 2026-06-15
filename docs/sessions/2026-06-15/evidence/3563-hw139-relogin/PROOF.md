# #3563 — newapi zero-click SSO re-login: live proof on hw139 (dep c89aa7059556b342)

Date: 2026-06-15. Sovereign: hw139.omani.works. App: `https://newapi.hw139.omani.works/`.

## Baseline (the defect, reproduced live, chart 1.4.92 / bridge 89b7ad4)

A bare-URL visit fired the OAuth flow (no login form — good), Keycloak silently
re-authenticated via `kc_idp_hint=catalyst-pin`, the SPA POSTed the callback, and
it **failed**:

```
GET /api/oauth/state            => 200 OK
GET /api/oauth/sovereign?code=… => 200  {"message":"This OpenOva SSO account has already been bound","success":false}
console: [ERROR] This OpenOva SSO account has already been bound
```

`localStorage.user = null`, navbar shows "Sign in / Sign up" — NOT signed in.

### Why (verified against upstream Calcium-Ion/new-api v0.13.2)

`controller/oauth.go HandleOAuth` selects bind-vs-login purely off the session:

```go
username := session.Get("username")
if username != nil { handleOAuthBind(c, provider); return } // BIND → IsUserIDTaken → MsgOAuthAlreadyBound
// else: findOrCreateOAuthUser → loads the existing binding's user and logs in (LOGIN)
```

`GenerateOAuthCode` (`/api/oauth/state`) sets `oauth_state` but **never clears
`username`**. A re-visit still carries the prior visit's session cookie with
`username` set → the callback lands in BIND mode → "already bound" → no `/console`
session. NewAPI is a pinned mirror (not a fork), so the binary cannot be patched.

DB state (hw139): user `sovereign_2` (id=2, role=100) is bound via
`user_oauth_bindings` → provider `sovereign` (provider_user_id
`c60bcd85-01e9-41db-8726-268ea270856c`), so `IsUserIDTaken` returns true.

## The fix, executed live (== what sso_init.go 0.1.17 automates)

```
GET /api/user/logout            => 200  {"success":true}      // session.Clear()+Save() — wipes stale username
GET /api/oauth/state            => 200  data=NPu1wcM4uXE7      // fresh CSRF state, username now nil
→ KC authorize (kc_idp_hint=catalyst-pin, silent) → /oauth/sovereign?code=a07fb230-…&state=NPu1wcM4uXE7
SPA POST /api/oauth/sovereign   => LOGIN branch (username nil) → findOrCreateOAuthUser loads sovereign_2
```

Result — **landed signed-in**:

- URL: `https://newapi.hw139.omani.works/console/token` (inside the app, NOT /login?expired)
- `localStorage.user = {"display_name":"emrah.baysal@openova.io","id":2,"role":100,"status":1,"username":"sovereign_2"}`
- `/console` dashboard: "👋Good afternoon，sovereign_2", user menu "S sovereign_2", the **Admin** sidebar section present (role=100). No "already bound".

Screenshot: `01-relogin-signed-in-console-PASS.png`.

## What ships

`platform/newapi/internal/handler/sso_init.go` 0.1.17 makes the bare-URL landing
page do this automatically: (1) GET /api/user/self → /console if already signed
in; (2) else GET /api/user/logout then the OAuth flow (login branch, not bind).
Chart 1.4.92 → 1.4.93; the sandbox-bridge CI re-tags the sidecar + blueprint-release
auto-bumps the 80-newapi.yaml pin in lockstep on merge.
