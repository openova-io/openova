# UAT — ground reality on `hw130.omantel.biz`

**Last verified live: 2026-06-12 13:23Z.** Login: `emrah.baysal@openova.io` → PIN to the mailbox.
Every row: click the URL yourself; ✅ only if it works **right now**. Full history: [archive](../archive/UAT-detail-2026-06-12.md).

## 1. SSO — type the URL → land signed in (zero clicks)

Rebuilt 2026-06-13 per [#3374](https://github.com/openova-io/openova/issues/3374) law §2.2 — **NOTHING is banked**:
a row may only show ✅ with a **same-day** lands-signed-in walk link; every merge touching
the SSO chain flips affected rows back to UNVERIFIED (mechanical — `scripts/uat-sso-flip.py`,
fired by `.github/workflows/sso-uat-flip.yaml`). Probe: `scripts/sso-zero-click-probe.mjs`
(headless walk of every row from a fresh handover session; a 302-to-realm wire-proof is never a pass).
Measured baseline 2026-06-13 (fresh session, probe + browser walk):
[probe JSON](../sessions/2026-06-13/evidence/sso-probe-baseline-hw130.json) +
[screenshots](../sessions/2026-06-13/evidence/). **Root-cause found**: the handover session
cookie was host-only on `console.<fqdn>` so the catalyst-pin chain never saw it — every
zero-click from a fresh handover session bounced to the PIN form (fix in #3374 PR; earlier
✅ rows relied on a pre-existing PIN/KC session).

<!-- sso-zero-click-table:begin -->
| # | App | Try it | Now | Proof (same-day only) |
|---|---|---|---|---|
| 1 | console | [open](https://console.hw130.omantel.biz/) | UNVERIFIED | 2026-06-13 probe: handover → /dashboard GREEN (pre-fix) |
| 2 | grafana | [open](https://grafana.hw130.omantel.biz/) | UNVERIFIED | 2026-06-13 probe: fresh session → PIN form (cookie-domain bug) |
| 3 | gitea | [open](https://gitea.hw130.omantel.biz/) | UNVERIFIED | same |
| 4 | harbor | [open](https://registry.hw130.omantel.biz/) | UNVERIFIED | same |
| 5 | openbao | [open](https://bao.hw130.omantel.biz/ui/) | KNOWN-BROKEN | 2026-06-13: bare /ui/ = token form ([shot](../sessions/2026-06-13/evidence/row05-openbao-BROKEN-token-form.png)); shim degraded to deep-link ([shot](../sessions/2026-06-13/evidence/row05-openbao-shim-DEGRADED-deeplink-fallback.png)) |
| 6 | pdns-admin | [open](https://pdns-admin.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: login form ([shot](../sessions/2026-06-13/evidence/row06-pdns-admin-BROKEN-login-form.png)); 1-click /oidc/login works ([shot](../sessions/2026-06-13/evidence/row06-pdns-admin-oidc-dashboard-1click.png)) |
| 7 | guacamole | [open](https://guacamole.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: bare / = Tomcat 404 ([shot](../sessions/2026-06-13/evidence/row07-guacamole-BROKEN-root-404.png)); /guacamole/ zero-clicks but NO admin ([shot](../sessions/2026-06-13/evidence/row07-guacamole-settings-bounced-noadmin.png)) |
| 8 | newapi | [open](https://newapi.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: upstream connect timeout — netpol blocks the gateway entity ([shot](../sessions/2026-06-13/evidence/row08-newapi-BROKEN-upstream-timeout.png)); OIDC pointed at non-existent `ops` realm |
| 9 | openova-flow | [open](https://openova-flow.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: NO auth at all — anonymous JSON API ([shot](../sessions/2026-06-13/evidence/row09-openova-flow-UNAUTH-json.png)) |
| 10 | hubble | [open](https://hubble.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: /oauth2/callback 500 `unauthorized_client` — chart secret never registered in KC ([shot](../sessions/2026-06-13/evidence/row10-hubble-BROKEN-oauth2-callback-500.png)) |
| 11 | keycloak admin | [open](https://auth.hw130.omantel.biz/) | KNOWN-BROKEN | 2026-06-13: master-realm local form is the only path ([shot](../sessions/2026-06-13/evidence/row11-keycloak-admin-BROKEN-local-form.png)) |
| 12 | marketplace | [open](https://marketplace.hw130.omantel.biz/) | UNVERIFIED | 2026-06-13 probe: anonymous storefront, no login form (by design) ([shot](../sessions/2026-06-13/evidence/row12-marketplace-anonymous-storefront.png)) |
| 13-15 | api. / pdns. APIs | — | n/a | token-authed API surfaces — excluded from the zero-click contract (#3374 §3) |
| 16 | tenant org console + apps | — | UNBUILT | per-Org realm dormant; gated on FUNNEL (#3376) |<!-- sso-zero-click-table:end -->

## 2. Admin by default — emrah.baysal administrates each app

Same #3374 law as §1: rows flip UNVERIFIED on every SSO-chain merge; ✅ needs a same-day walk link.
2026-06-13 admin walk (pre-existing-session, see §1 caveat): gitea [/admin](../sessions/2026-06-13/evidence/row03-gitea-admin.png) ·
grafana [/admin/users](../sessions/2026-06-13/evidence/row02-grafana-admin-users.png) ·
harbor [users](../sessions/2026-06-13/evidence/row04-harbor-admin-users.png) ·
pdns-admin [dashboard+admin nav](../sessions/2026-06-13/evidence/row06-pdns-admin-oidc-dashboard-1click.png) ·
guacamole = **NO admin** (openid-only chart, no permission store — #3374 row-7 gap).

| App | Try it | Now | Proof |
|---|---|---|---|
| gitea | [/admin](https://gitea.hw130.omantel.biz/admin) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-admin2-gitea.png) |
| harbor | [users](https://registry.hw130.omantel.biz/harbor/users) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-admin2-harbor.png) |
| grafana | [/admin/users](https://grafana.hw130.omantel.biz/admin/users) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-admin2-grafana.png) |
| openbao | [access](https://bao.hw130.omantel.biz/ui/vault/access) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-admin2-openbao.png) |
| pdns-admin | [users](https://pdns-admin.hw130.omantel.biz/admin/manage-user) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-admin2-pdns.png) |

## 3. Postgres — instances + cards

| Check | Try it | Now | Proof |
|---|---|---|---|
| 3 shared instances live | — | ✅ | kubectl: `shared-pg`, `shared-pg-b`, `shared-pg-c` healthy 13:23Z |
| 1 card per instance | [cards](https://console.hw130.omantel.biz/catalog/bp-cnpg) | ✅ | [7-card shot](../sessions/2026-06-12/evidence/hw130-7-instance-cards-PROOF.png) (pre-b/c; now renders 9) |
| gitea+harbor ON shared-pg | — | ✅ | 110+49 tables on the engine |
| keycloak+grafana on shared | — | ❌ | flips merged ([#3366](https://github.com/openova-io/openova/pull/3366)), live adoption unverified |
| pdns/pda/sme on shared | — | ❌ | declared ready-to-adopt only |

## 4. Multi-region

| Check | Now | Proof |
|---|---|---|
| mesh both regions | ✅ | 1/1 remote ready |
| cross-region sync replication | ✅ | `pg_stat_replication` streaming/sync |
| region-kill survives, 0 loss | ✅ | [hw128 walk](../sessions/2026-06-11/hw128-region-kill-walk-PASS.md) |
| gitea/openbao/etc per-app multi-region | ❌ | not executed — [#2745](https://github.com/openova-io/openova/issues/2745) |

## 5. Jobs

| Check | Try it | Now | Proof |
|---|---|---|---|
| both regions listed | [jobs](https://console.hw130.omantel.biz/jobs) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-jobs-page-postimport.png) — 127 rows incl. 60 region-b |
| search box | same | ❌ crashes | [#3367](https://github.com/openova-io/openova/issues/3367) |

## 6. Funnel — voucher → running tenant

| Step | Try it | Now | Proof |
|---|---|---|---|
| issue voucher | [BSS](https://console.hw130.omantel.biz/bss) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-funnel-1-bss-voucher.png) |
| marketplace + redeem + PIN checkout | [marketplace](https://marketplace.hw130.omantel.biz/) | ✅ | [shots 2-5](../sessions/2026-06-12/evidence/hw130-funnel-5-checkout.png) |
| tenant actually provisions | — | ❌ | one unbuilt wire — [#3319](https://github.com/openova-io/openova/issues/3319) |

## 7. vClusters — every app in dmz/mgmt/rtz

| Check | Now | Proof |
|---|---|---|
| zone declared for all apps | ✅ | 0 undeclared |
| apps running inside | ❌ 4/48 | coraza, sandbox, +LGTM merged ([#3361](https://github.com/openova-io/openova/pull/3361)) |
| carve-outs (4 apps) | awaiting founder | [#2745](https://github.com/openova-io/openova/issues/2745) |

## 8. Console + env

| Check | Try it | Now |
|---|---|---|
| mothership shows hw130 | [mothership](https://console.openova.io/sovereign) | ✅ ready |
| dashboard | [open](https://console.hw130.omantel.biz/dashboard) | ✅ (region-a only in treemap) |
| cloud 2 regions | [open](https://console.hw130.omantel.biz/cloud) | ✅ |
