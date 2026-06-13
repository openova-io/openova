# UAT — ground reality on `hw133.omani.works` (fresh zero-touch prov, 2026-06-13)

**Last verified live: 2026-06-13 ~15:12Z on `hw133.omani.works`** (`40c4e17667b600eb`, fresh 2-region prov,
SHARED_PG=true, converged **zero-touch** with the #3409 powerdns fix — no hand-patch, no manual unwedge).
Login: none — a signed `/auth/handover` token lands directly in the console as `emrah.baysal@openova.io`
(role `sovereign-admin`). Walk evidence + screenshots: [`hw133-zerotouch-walk.md`](../sessions/2026-06-13/evidence/hw133-zerotouch-walk.md).

> **Wipe-and-rewalk contract:** every wipe empties this file; ✅ rows below were all walked **today on hw133**
> after the env converged from scratch. Prior-env (hw130) results are void and were cleared.
> Every row: click the URL yourself; ✅ only if it works **right now**.

## 0. Fresh-prov zero-touch convergence (founder mandate — "new envs follow the same approach zero-touch")

| Go to | Action | You should see | Result (2026-06-13, hw133) |
|---|---|---|---|
| `console.hw133.omani.works/auth/handover?token=…` | paste handover JWT | `302 → /dashboard`, no login form, avatar **E** | ✅ [01](../sessions/2026-06-13/evidence/hw133-walk/hw133-01-dashboard-signed-in-admin.png) |
| `/apps` | Deployments tab | "✓ Sovereign ready", 49 INSTALLED incl. PowerDNS | ✅ [02](../sessions/2026-06-13/evidence/hw133-walk/hw133-02-apps-49-installed-ready.png) |
| `/organizations` | view | one menu, parent-org first row, showback day-one | ✅ [03](../sessions/2026-06-13/evidence/hw133-walk/hw133-03-organizations-parent-showback.png) |

**Regression found + fixed this run:** #3405 rerouted powerdns images to `harbor.openova.io/proxy-docker` (not
bootstrap-pullable) → hw131/hw132 wedged (powerdns never Ready → no cert → no TLS). **#3409** reverted to docker.io
+ kyverno powerdns exclude → hw133 converged. Forensic: [`hw131-zerotouch-convergence-failure.md`](../sessions/2026-06-13/evidence/hw131-zerotouch-convergence-failure.md).

## 1. SSO — type the URL → land signed in (zero clicks) — walked on hw133

| # | App | Try it | Now | Proof (hw133, 2026-06-13) |
|---|---|---|---|---|
| 1 | console | [open](https://console.hw133.omani.works/) | ✅ | handover → `/dashboard`, signed-in as emrah, admin avatar [01](../sessions/2026-06-13/evidence/hw133-walk/hw133-01-dashboard-signed-in-admin.png) |
| 2 | grafana | [open](https://grafana.hw133.omani.works/) | ✅ | bare URL → Grafana home signed-in, Profile present [04](../sessions/2026-06-13/evidence/hw133-walk/hw133-04-grafana-zero-click-signed-in.png) |
| 3 | gitea | [open](https://gitea.hw133.omani.works/) | ✅ | bare URL → "emrah.baysal - Dashboard - Catalyst Gitea" [05](../sessions/2026-06-13/evidence/hw133-walk/hw133-05-gitea-zero-click.png) |
| 4 | harbor | [open](https://registry.hw133.omani.works/) | ✅ | bare URL → `/harbor/projects` signed-in [06](../sessions/2026-06-13/evidence/hw133-walk/hw133-06-harbor-zero-click.png) |
| 5 | openbao | [open](https://bao.hw133.omani.works/ui/) | ❌ BROKEN | OIDC callback reaches sovereign realm + gets a code, but the UI errors `Cannot read properties of null (reading 'postMessage')` on a **doubled** `/oidc/oidc/callback` path → bounces to the auth form [07](../sessions/2026-06-13/evidence/hw133-walk/hw133-07-openbao-BROKEN-postmessage.png) |
| 6 | pdns-admin | [open](https://pdns-admin.hw133.omani.works/) | ❌ BROKEN | `/oidc/login` returns **HTTP 500** [08](../sessions/2026-06-13/evidence/hw133-walk/hw133-08-pdns-admin-BROKEN-500.png) |

**SSO score on the fresh env: 4/6 zero-click** (console, grafana, gitea, harbor). openbao (doubled-oidc-callback +
postMessage) and pdns-admin (500) remain broken — these are the two hard apps #3374 still owes; both confirmed live, not assumed.

## 2. Postgres — shared instances (#3370 substrate) — walked on hw133

| Check | Where | Now | Proof (hw133) |
|---|---|---|---|
| 3 shared-PG instances live | `/organizations` showback | ✅ | `shared-pg`, `shared-pg-b`, `shared-pg-c` all in `shared-data` ns [03](../sessions/2026-06-13/evidence/hw133-walk/hw133-03-organizations-parent-showback.png) |
| showback day-one | `/organizations` | ✅ | parent-org per-app table, 13578 units / 13229m CPU [03](../sessions/2026-06-13/evidence/hw133-walk/hw133-03-organizations-parent-showback.png) |

## 3. Remaining deeper walks (substrate up on hw133, end-to-end walk pending)

| Item | State on hw133 | Pending |
|---|---|---|
| #3376 FUNNEL (voucher→tenant→signed-in) | **front-half walked** — storefront "Build your cloud tenant" → 6-step wizard → [Plans](../sessions/2026-06-13/evidence/hw133-walk/hw133-09-funnel-plans.png) → [Checkout sign-in prompt](../sessions/2026-06-13/evidence/hw133-walk/hw133-10-funnel-checkout-signin.png) all render | back-half: email magic-code → voucher → tenant provision → land-signed-in (NOT yet walked) |
| #3375 region-kill DR | cnpg-pair primary present, #3307 peering at boot | continuum-driven kill + promote walk |
| #3379 cutover | dormant at slot-06a | post-handover 8-tether pivot + 10-min egress hold |
| #3373 vCluster re-home | mgmt/dmz/rtz vclusters live | founder decision: OSS Pro-gating (license / host-bridge / host-side) |
