# #3374 — SSO zero-login everywhere + admin-by-default — operator walk

> **THIS IS THE CANONICAL #3374 WALKTHROUGH** (the doc the issue body §10
> references). It is the click-by-click acceptance walk for the bare-URL→
> authenticated-as-admin contract end-to-end: the silent console front
> door, the ~11 external surfaces, the historic-failure rows, the one
> admin-authority proof, the dead-grant log proof, and the dual generality
> proof.
>
> **Status: UNVERIFIED — pending a fresh-prov walk on the CURRENT env.**
> The code/chart mechanism landed (PR for #3374); per #3374 §2 law #2
> ("NOTHING is banked") every row below is UNVERIFIED until walked fresh,
> in ONE session, at acceptance time, on the current env, with screenshots
> committed under `docs/sessions/<date>/evidence/`. No row may show ✅ from
> a past env's walk.

**North Star #3 (founder, verbatim):** *"NO login UI anywhere — URL →
signed in as emrah.baysal as ADMIN; proof = surfing admin panels."*

**The law (one sentence):** in a **fresh incognito window**, type the bare
root URL of ANY surface → land **signed in** as the owner with **admin**
rights — no login UI, no PIN/token form, no second click. A 302-to-realm
wire-proof is NOT a pass (#3150). A redirect that ends on a login screen, a
setup wizard, or a 404/500/503 is a **fail**.

---

## What changed in this PR (the mechanism the walk validates)

| Layer | Defect (#3374 §3) | Fix in this PR |
|---|---|---|
| **A — one generic interception** | 5 bespoke per-app mechanisms; the generic gate fronted 1 of ~9 apps | `bp-oidc-gate` `instances:` now also front **powerdns-admin** (replacing its structurally-fragile bespoke root-redirect); pda's own HTTPRoute disabled (slot 11a `gateway.enabled:false`) so the gate is the single hostname owner. A disabled **`sso-generality-probe`** instance proves a NEW app needs ONLY a name+hostname (DoD box 10a). |
| **B — silent console front door** | sessionless visitor → 6-digit-PIN wall | `router.tsx` on `whoami` 401 attempts a **silent OIDC round-trip** (`initiateLogin` → KC catalyst-pin IDR) BEFORE falling to `/login`; `/login` is now an explicit fallback, never the default. One-shot guard prevents loops; cleared on successful callback. |
| **B — owner seed** | `orgEmail:""` → `OPERATOR_EMAIL` unset → owner seed skipped → `/users` empty | cloud-init substitute now threads **`ORG_EMAIL`/`ORG_NAME`** (the same `${org_email}` tofu value that already fed the realm seed) → `sovereign-fqdn` CM `orgEmail` → `OPERATOR_EMAIL` env → the pre-existing `runBakeTimeOwnerSeed` seeds the owner `UserAccess` CR idempotently on every Pod start. |
| **C — one admin authority** | 3 disjoint systems; the console asserted a self-signed `catalyst-owner` constant; the sso-bridge grant code was dead theater | the keycloak realm import now defines the `catalyst-{viewer..owner}` composite realm roles AND composites `realmRoles:["catalyst-admin"]` onto `/sovereign-admins` (ONE membership → console + KC console + every app). `auth.go` PIN-mint now **derives** tier from live KC `/sovereign-admins` membership (non-member → viewer, never silently owner). The dead `grant_operator_admin`/`grant_operator_realm_roles` are DELETED. |

---

## Pre-walk: sign in once (the only auth action in the whole walk)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/auth/handover?token=<RS256 JWT>` | Paste the handover URL into a fresh tab, press Enter (`iss=console.openova.io`, `sub=emrah.baysal@openova.io`, `role=sovereign-admin`) | 302 → `/dashboard`, signed in, **no login form** | ☐ |

---

## Walk A — the console silent front door + owner seed (Layer B)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/` | In a **fresh incognito** window (NO cookie), type the bare console URL, press Enter | A brief OIDC redirect to `auth.<fqdn>` then back — lands on `/dashboard` **signed in**, the PIN form is **never shown**. Network trace shows the silent `catalyst-ui` authorize round-trip, NOT a stop on `/login`. | ☐ |
| (same window) clear cookies / wait out the session, re-open `https://console.<fqdn>/` | Re-open the bare URL | Lands signed-in again via the silent round-trip — still no PIN form | ☐ |
| `https://console.<fqdn>/users` | Open the Users page | Exactly one row: the owner email, `tier=owner`, badge **owner**. (kubectl cross-check: `kubectl get cm sovereign-fqdn -n catalyst-system -o jsonpath='{.data.orgEmail}'` is non-empty; `GET .../admin/user-access` returns ≥1 item.) | ☐ |
| `https://console.<fqdn>/dashboard` | Click the avatar (top-right) | Menu reads "Signed in as <owner email>"; the BSS menu + RBAC nav are reachable — admin-ness is the **realm claim** (`sovereign-admins`/`catalyst-admin`), proven by surfing the admin panels | ☐ |

---

## Walk B — every external surface lands signed-in admin (§3-d, 11 rows)

Each row: fresh incognito tab → bare URL → land signed-in as owner → open the
admin surface. TWO screenshots per app (landing + admin), ZERO login UI.

| # | Go to (bare URL) | You should see (authenticated landing + admin) | Result |
|---|---|---|---|
| 1 | `https://console.<fqdn>/` | `/dashboard` signed-in, no PIN (Walk A) | ☐ |
| 2 | `https://grafana.<fqdn>/` | Home/Dashboards, no login form; `/api/user` → `isGrafanaAdmin=true` | ☐ |
| 3 | `https://gitea.<fqdn>/` | owner Dashboard, logged in; admin panel `/-/admin` reachable | ☐ |
| 4 | `https://registry.<fqdn>/` | `/harbor/projects`, no login form; Administration menu visible (`sysadmin` via `sovereign-admins`) | ☐ |
| 5 | `https://bao.<fqdn>/ui/` | `/ui/vault/secrets` (Secrets Engines), **no token form**, no `/ui/vault/auth` | ☐ |
| 6 | `https://pdns-admin.<fqdn>/` | **zero-click** → `/dashboard`, no login form; the OIDC callback does NOT loop (now fronted by the generic gate, not the bespoke redirect) | ☐ |
| 7 | `https://guacamole.<fqdn>/` | Connections list, no Guacamole login, no Tomcat 404 | ☐ |
| 8 | `https://newapi.<fqdn>/` | a 2nd independent bare-URL visit lands signed-in in `/console` (role=100) — no "already bound", no `/login?expired` (folds #3563) | ☐ |
| 9 | `https://openova-flow.<fqdn>/` | proxied app surface, authenticated (the original generic-gate app) | ☐ |
| 10 | `https://hubble.<fqdn>/` | Hubble Service Map, authenticated (under the gate) | ☐ |
| 11 | `https://auth.<fqdn>/` | the Keycloak admin console accepts the operator (sovereign-admin via the `/sovereign-admins` group's `realm-management:realm-admin`) | ☐ |
| 12 | `https://marketplace.<fqdn>/` | anonymous storefront by design — confirm NO spurious login UI | ☐ |

---

## Walk C — the one-admin-authority proof (Layer C, folds #3679/#3685)

| Step | Command / action | Expected | Result |
|---|---|---|---|
| C1 | `kubectl exec keycloak-0 -- curl .../users/{owner-id}/role-mappings/realm/composite` | INCLUDES `catalyst-admin` — conferred via the `/sovereign-admins` group composite (NOT a per-email grant) | ☐ |
| C2 | Neutralize the `auth.go` self-signed assertion (the PIN tier is now group-derived) and STILL surf the console admin panels | admin nav reachable because the principal carries `sovereign-admins`/`catalyst-admin` FROM THE REALM | ☐ |
| C3 | `kubectl logs <sso-bridge pod>` across a full 60s tick | ZERO `skipping admin grant` / `skipping realm-role grant` lines (the dead path is gone). CI tripwire: `scripts/check-no-dead-grant-theater.sh` | ☐ |
| C4 | Set `sovereign-fqdn` `orgEmail` empty, restart catalyst-api | the owner's admin status is UNCHANGED (no privilege outcome depends on the email — admin follows the group) | ☐ |
| C5 | unit test | `go test ./internal/handler/ -run TestPinVerify_NonAdminGetsViewerNotOwner` — a user NOT in `/sovereign-admins` does NOT receive owner/admin claims | ☑ (green in CI) |

---

## Walk D — the dual generality proof (DoD box 10, the #3370 bar)

| Step | Action | Expected | Result |
|---|---|---|---|
| D1 (interception) | Add a brand-new throwaway app to `bp-oidc-gate` `instances:` with ONLY `name`+`hostname`+`upstream` (the shipped `sso-generality-probe` instance is the template — flip `enabled:true`, point `upstream` at any Service) | its bare URL lands zero-click with NO app-specific redirect/shim code — `git diff` is a single `instances:` entry | ☐ |
| D2 (admin) | That same app maps admin on `groups ∋ sovereign-admins` (the universal JMESPath) | the SAME owner is admin there with ZERO console/realm change — `git diff` shows only the new app's declaration | ☐ |

---

## Automated cross-checks (NOT acceptance — demoted per the UAT format law)

- `scripts/sso-zero-click-probe.mjs --fqdn <fqdn> --handover-url '…'` — the per-app synthetic walk (positive + `--expect-broken` negative test). Wired to CI/cron in `.github/workflows/sso-probe.yaml`.
- `scripts/check-no-dead-grant-theater.sh` — static tripwire that the dead admin-by-default grant theater stays deleted (runs on every PR touching the SSO chain).
- `go test ./internal/handler/ ./internal/keycloak/` — `TestPinVerify_StampsTierAndRealmRoleClaims` (admin via group/operator-email) + `TestPinVerify_NonAdminGetsViewerNotOwner` (law #4: non-member → viewer).
