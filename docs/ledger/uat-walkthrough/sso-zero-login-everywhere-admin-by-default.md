# SSO: zero-login everywhere, admin-by-default — user acceptance walk (web UI)

> **Issue #3374** (absorbs + closes #3563, #3686, #3679, #3685, #3693).
> **Env: `console.<fqdn>` (current env — replace `<fqdn>` with the live Sovereign FQDN, e.g. `hw150.omantel.biz`).**
> Acceptance = an operator walking these clickable rows in ONE fresh session on the CURRENT env.
> **NOTHING is banked** (law §2.2): every row starts UNVERIFIED or KNOWN-BROKEN; no ✅ may be carried from a past env. Each ✅ must trace to a same-day screenshot under `docs/sessions/<date>/evidence/` and be linked from `docs/ledger/UAT.md`.

**North Star #3 (founder, verbatim):** *"NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN, proof = surfing admin panels."*

**The contract:** in a **fresh incognito window**, type the surface's **bare URL** → land **signed in** as the owner with **admin** rights. Zero clicks, no login / PIN / token form. A redirect that ends on a login screen, a PIN page, a "Sign in with…" button, a setup wizard, or a 404 / 500 / 503 is a **fail**. A 302-to-realm wire-proof is NOT acceptance — only a lands-signed-in walk + an admin-surface screenshot is (the #3150 lesson).

**How to read the table:** every row is ONE UI action. `Go to (URL)` · `Then do (click / type)` · `You should see (screen)` · `Result ☐`. Open each surface's bare URL in its OWN fresh incognito tab — the contract is per-URL, not "click Open from the console".

---

## Part 1 — The console front door: silent SSO, no PIN wall, owner pre-seeded (layer B · folds #3693)

The console's own entry must establish the first session by a silent OIDC round-trip — never a 6-digit-PIN form — and the owner must be listed on `/users` from minute zero.

| Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|
| `https://console.<fqdn>/` | **Fresh incognito, NO cookie.** Type the bare console URL, press Enter | Silent OIDC round-trip → land on **`/dashboard`, signed in as the owner**. **NO "Sign in" page, NO "Enter your email to receive a 6-digit PIN" form, NO "Send code" button.** Left-rail nav (Dashboard, Cloud, Apps, Jobs, Compliance, Users, Organizations, Settings); env switcher reads `<fqdn>`; avatar top-right | ☐ |
| `https://console.<fqdn>/dashboard` | Click the avatar (top-right, testid `profile-menu-avatar`) | Menu reads **"Signed in as emrah.baysal@openova.io"** + a **Sign out** item — confirms the landed identity is the owner (NOT a self-signed shortcut masking realm state) | ☐ |
| `https://console.<fqdn>/users` | Look at the user-access table on first load | Exactly one row: **`emrah.baysal@openova.io` · tier=owner · badge "owner"**. The page does **NOT** render "No user access entries yet. Click '+ New'…" (DoD D21) | ☐ |
| *(then)* clear cookies / wait out the 15-min session TTL, re-open `https://console.<fqdn>/` | Re-open the bare console URL in a fresh tab | Lands signed-in on `/dashboard` **again** — no PIN, no login form (steady-state re-entry is silent SSO, not the PIN wall) | ☐ |

**Automated cross-checks (NOT acceptance):**
- `kubectl get cm sovereign-fqdn -n catalyst-system -o jsonpath='{.data.orgEmail}'` → **non-empty**, equals the operator's email (was `""` on hw150 — the root cause of the empty `/users`).
- `curl .../api/v1/deployments/<id>/admin/user-access` (authed) → `{"items":[<owner>]}` (≥1 item).
- catalyst-api log carries NO `"bake-time owner seed skipped — OPERATOR_EMAIL unset"` line.
- Network trace on row 1 shows the silent OIDC round-trip (`/auth/callback`), NOT a stop on `/login`.

---

## Part 2 — The 11 external surfaces: every bare URL lands signed-in admin (layer A · §3-d)

One generic interception (`bp-oidc-gate`) must front every app that cannot natively auto-land form-free; each bare URL lands authenticated with no app login form and no per-app landing shim.

| Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|
| `https://grafana.<fqdn>/` | Fresh incognito, type the bare URL | Lands on **Grafana Home / Dashboards**, **no login form** (not even one keystroke away — `disable_login_form` proven). Open **Administration** nav → present (admin). `/api/user` → `login=emrah.baysal@openova.io`, `isGrafanaAdmin=true` | ☐ |
| `https://gitea.<fqdn>/` | Fresh incognito, type the bare URL | Title **"emrah.baysal — Dashboard — Catalyst Gitea"**, logged in. Open **Site Administration** → reachable (admin via `--admin-group sovereign-admins`). No `:30443` in any redirect (the #3310 `:443` class holds) | ☐ |
| `https://registry.<fqdn>/` | Fresh incognito, type the bare URL | Auto-redirects to **`/harbor/projects`**, no login form; `emrah.baysal@openova.io` top-right; **Administration** menu (Users / Robot Accounts / Registries) visible. `/api/v2.0/users/current` → `username=emrah.baysal@openova.io`, `sysadmin:true` (the admin-group mapping promotes to sysadmin — not just login) | ☐ |
| `https://bao.<fqdn>/ui/` | Fresh incognito, type the bare URL **with the `/ui/` path** | OIDC round-trip completes → lands on **`/ui/vault/secrets`** (Secrets Engines: `cubbyhole/`, `secret/`). **NO `/ui/vault/auth` token form.** This is the founder-witnessed failure (bare URL showed the token form) — it must be DEAD. No `sso-landing.yaml` shim page renders | ☐ |
| `https://pdns-admin.<fqdn>/` | Fresh incognito, type the bare URL | **ZERO clicks** → PowerDNS-Admin dashboard, authenticated. **No "Sign in with OIDC" button to click.** Network trace shows `/oidc/authorized` reached **exactly once** — the callback does NOT loop (ERR_TOO_MANY_REDIRECTS is a fail; the `/login` redirect that looped pda 0.1.11 must NOT have returned) | ☐ |
| `https://guacamole.<fqdn>/` | Fresh incognito, type the bare URL | Lands on the **Guacamole connections list**, authenticated. No Guacamole login form, no Tomcat 404 (the WAR serves at `/guacamole/`; `redirect_uri=/` → 404 is the historic fail) | ☐ |
| `https://newapi.<fqdn>/` | Fresh incognito **(1st visit)**, type the bare URL | Custom-OAuth ("sovereign") flow runs → binds the SSO identity → lands signed-in in **`/console`** (role=100, admin) | ☐ |
| `https://newapi.<fqdn>/` | **2nd independent fresh incognito visit (the #3563 regression test)**, type the bare URL | Lands signed-in in **`/console`** again (role=100). **NOT** "This OpenOva SSO account has already been bound"; **NOT** `/login?expired=true`; `localStorage.user` is NOT null. The landing is idempotent across visits (`GET /api/user/self` short-circuits, or `GET /api/user/logout` clears the stale session so the callback takes the LOGIN branch not the BIND branch) | ☐ |
| `https://openova-flow.<fqdn>/` | Fresh incognito, type the bare URL | Brief OIDC redirect (the generic `oidc-gate-openova-flow` pod) → OpenOva Flow UI, authenticated; no app login form | ☐ |
| `https://hubble.<fqdn>/` | Fresh incognito, type the bare URL | Hubble UI, **authenticated** (no longer an open/unauthed external route) — OR the external route is removed with cited slot-intent justification. Not anonymous JSON, not an open dashboard | ☐ |
| `https://auth.<fqdn>/admin/sovereign/console/` | Fresh incognito, type the bare URL | Keycloak admin console **accepts the owner** (realm-admin via the `/sovereign-admins` → `realm-management:realm-admin` composite). No local-account form left as the only path | ☐ |
| `https://marketplace.<fqdn>/` | Fresh incognito, type the bare URL | Anonymous storefront renders (by design until checkout). Confirm **no spurious login UI** appears on the landing | ☐ |

**Automated cross-checks (NOT acceptance):**
- `kubectl get pods -n oidc-gate` → one gate pod per gated app (≥ openbao, powerdns-admin, guacamole, hubble-ui, newapi), not just `oidc-gate-openova-flow`.
- `kubectl get httproute -A` confirms the deleted bespoke interceptions are gone (openbao `sso-landing`, etc.).
- The bespoke shims are removed from the charts (diff): openbao `sso-landing.yaml`; the dead pdns-admin `/login` redirect block.

---

## Part 3 — One admin authority: the realm group, not a self-signed constant or dead grant (layer C · folds #3679, #3685)

Admin-ness everywhere derives from ONE source — membership in the Keycloak `/sovereign-admins` group, composited to confer both KC-console admin and console (`catalyst-admin`) admin — never a self-signed `owner` JWT and never the dead per-Client grant.

| Go to (URL) / command | Then do | You should see | Result |
|---|---|---|---|
| `kubectl exec keycloak-0 -- curl .../users/{owner}/role-mappings/realm/composite` | Inspect the seeded owner's effective realm roles | Includes **`catalyst-admin`** (conferred via the `/sovereign-admins` group composite) — NOT only `uma_authorization, default-roles-sovereign, offline_access` (the hw150 disjunction) | ☐ |
| `https://console.<fqdn>/` then the BSS / RBAC admin nav | With the self-signed `owner`/`catalyst-owner` constant **neutralized** (the test build), open the admin-gated nav | Admin nav **visible + usable** — the console treats the owner as admin because the principal carries `sovereign-admins`/`catalyst-admin` FROM THE REALM, not because `auth.go` asserted a self-signed `owner` constant | ☐ |
| `kubectl logs -n sso-bridge deploy/sso-bridge-reconciler --tail=80` | Read a full reconcile tick on a fresh prov | **ZERO** `"no operator email; skipping admin grant"` and `"skipping realm-role grant"` lines (the dead `grant_operator_admin` / `grant_operator_realm_roles` paths are DELETED, not no-ops) | ☐ |
| `grep -rn "admin" platform/*/chart` (per-Client role check) | Confirm no app reads a per-Client `admin` role | No app chart references a per-Client KC `admin` role — every app keys on the `groups` claim (`sovereign-admins`). The orphaned per-Client `admin` role artifact is gone | ☐ |
| `kubectl get cm sovereign-fqdn -o jsonpath='{.data.orgEmail}'` then re-walk Part 2 | Set `orgEmail` empty (or note it empty), re-open grafana/gitea/harbor/openbao | The owner is **still admin everywhere** — no privilege outcome depends on the `orgEmail` ConfigMap key; the `/sovereign-admins` realm seed is the single source | ☐ |
| `go test -race ./products/catalyst/bootstrap/api/...` (auth.go unit test) | Run the new unit test | A user **NOT** in `/sovereign-admins` does **NOT** receive `owner`/`catalyst-owner` claims (the PIN mint mirrors live KC state, no hardcoded constants) | ☐ |

---

## Part 4 — Tenant tier: per-Org realm, identical bare-URL contract (law §7 · gated on FUNNEL #3376)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<org-slug>.<org-domain>/` | After a FUNNEL voucher redeem creates the first tenant Org, fresh incognito, type the org console bare URL | Lands signed-in as the org owner — admin-by-default in THEIR realm (a per-Org Keycloak realm exists; the owner is seeded into that realm's admin group). No login form | ☐ |
| `https://<purchased-app>.<org-slug>.<org-domain>/` | Fresh incognito, type the purchased app's bare URL | Lands signed-in as the org owner, admin — the identical zero-click contract, via the org's own realm (the app's OIDC client is in THAT realm, not the sovereign realm) | ☐ |

**Cross-check:** `kubectl exec keycloak-0 -- curl .../realms` shows the per-Org realm; its `/sovereign-admins`-equivalent group seeds the org owner.

---

## Part 5 — Generality proof (the #3370 bar): one mechanism, ANY new app

The contract must be generic across BOTH interception AND admin authority — a brand-new app inherits zero-click AND admin-by-default with no app-specific code.

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| edit `clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml` | Add `{name: throwaway, clientId: throwaway, hostname: throwaway.<fqdn>, upstream: …}` to `instances:` — a single entry | `git diff` shows ONLY one `instances:` entry and nothing else (no per-app redirect, no shim HTML, no httproute filter) | ☐ |
| `https://throwaway.<fqdn>/` | After a fresh prov / reconcile, fresh incognito, type the bare URL | Lands **zero-click authenticated** through the same generic gate — no app-specific interception code was written | ☐ |
| add the throwaway app's admin mapping keyed on `groups ∋ sovereign-admins` | Wire the standard group rule | `git diff` shows ONLY the new app's declaration; the SAME owner is **admin** there with zero console/realm change | ☐ |

---

## Acceptance summary

- **Part 1 (console front door + owner seed):** ___/4 ☐ — silent SSO, no PIN wall, `/users` lists the owner.
- **Part 2 (11 external surfaces):** ___/12 ☐ — every bare URL lands signed-in admin; openbao no token form, pdns zero-click no loop, guacamole no 404, **newapi revisit signed-in (#3563)**.
- **Part 3 (one admin authority):** ___/6 ☐ — realm composite confers `catalyst-admin`, self-signed constant neutralized, dead grant subsystem removed, no `orgEmail` dependency.
- **Part 4 (tenant tier):** ___/2 ☐ — per-Org realm, identical contract (gated on FUNNEL #3376).
- **Part 5 (generality proof):** ___/3 ☐ — new throwaway app: zero interception code + zero admin-mapping code.

**One acceptance session walks EVERY row fresh on the current env.** A row may only ever show ✅ with a same-day walk link; a chart roll touching the app or the SSO chain flips it UNVERIFIED (the probe, DoD box 9, enforces this mechanically). Acceptance is the founder walking the clickable rows above — the automated cross-checks are supporting evidence, demoted per the founder's UAT format law.
