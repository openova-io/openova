# UAT — ground reality on `hw130.omantel.biz`

**Last verified live: 2026-06-12 13:23Z.** Login: `emrah.baysal@openova.io` → PIN to the mailbox.
Every row: click the URL yourself; ✅ only if it works **right now**. Full history: [archive](../archive/UAT-detail-2026-06-12.md).

## 1. SSO — type the URL → land signed in (zero clicks)

| App | Try it | Now | Proof |
|---|---|---|---|
| grafana | [open](https://grafana.hw130.omantel.biz/) | ✅ | [shot](../sessions/2026-06-12/evidence/hw130-grafana-root-zeroclick-PROOF.png) |
| harbor | [open](https://registry.hw130.omantel.biz/) | ✅ | lands /harbor/projects |
| gitea | [open](https://gitea.hw130.omantel.biz/) | ✅ | 303 → OIDC → in |
| pdns-admin | [open](https://pdns-admin.hw130.omantel.biz/) | ❌ 1 click | [OIDC click works](../sessions/2026-06-12/evidence/hw130-sso-pdns-admin-PASS.png) |
| openbao | [open](https://bao.hw130.omantel.biz/ui/) | ✅ via Open | console **Open** → lands [/ui/vault/secrets](https://bao.hw130.omantel.biz/ui/vault/secrets) signed-in, zero clicks ([proof](../sessions/2026-06-12/evidence/hw130-openbao-zeroclick-rewalk-vault-secrets.png), re-walked 14:58Z post-roll-storm; shim [#3231](https://github.com/openova-io/openova/pull/3231)). Bare /ui/ first visit still forms until the Open path runs |

## 2. Admin by default — emrah.baysal administrates each app

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

## 3a. #3370 Contexts — generic multi-application reusability (PR open, NOT merged — local-build evidence pending the post-merge live re-walk)

Every proof below ran the REAL code of the #3370 PR branch: the real `catalyst-api` binary + the real
`application-controller` against a REAL kube-apiserver (envtest v1.36) seeded with the hw130 bootstrap
state (slots 16a/16c/16d, SHARED_PG=true), and the console UI dev build on top of that stack. The live
hw130 console cannot carry these changes until the founder merges; each row re-walks live post-merge.

| DoD box | Check | Now | Proof |
|---|---|---|---|
| 1 | blueprint `shareable` + `contextSchema`; catalog badge (shareable vs non-shareable) | ✅ local | [catalog-tab badges](../sessions/2026-06-12/evidence/3370-G-catalog-tab-shareable-badges.png) · [bp-postgres hero badge](../sessions/2026-06-12/evidence/3370-B-catalog-bp-postgres.png) · [bp-valkey](../sessions/2026-06-12/evidence/3370-F1-catalog-bp-valkey-shareable.png) |
| 2 | 1 Application CR per postgres instance; `kubectl get hr -A` before/after byte-identical (adoption) | ✅ local | [envtest adoption walk](../sessions/2026-06-12/evidence/3370-envtest-adoption-no-duplicate-hr.txt) + unit tests `TestReconcile_BootstrapOwnedAdoption_*` |
| 3 | /apps: 3 instance cards with `⛓ N contexts` badges | ✅ local | [shot](../sessions/2026-06-12/evidence/3370-A-apps-instance-cards.png) |
| 4 | /catalog/bp-postgres: topologies + New instance + instance lines → /app/$id; Data-instances panel GONE | ✅ local | [shot](../sessions/2026-06-12/evidence/3370-B-catalog-bp-postgres.png) + deletion diff in the PR + deletion-lock test |
| 5 | /app/shared-pg Contexts tab (db/gitea · gitea → · secret · ready ×3); consumer `Depends on: <instance> / db:<ctx>` | ✅ local | [contexts tab](../sessions/2026-06-12/evidence/3370-C-app-shared-pg-contexts-tab.png) · [consumer page](../sessions/2026-06-12/evidence/3370-D-app-wiki-depends-on.png) |
| 6 | journey: topology at spawn; DEFAULT auto-creates backing as own card; ADVANCED reuse → new Context row | ✅ local | [dialog](../sessions/2026-06-12/evidence/3370-E1-new-instance-dialog-default.png) · [reuse dropdown](../sessions/2026-06-12/evidence/3370-E2-new-instance-dialog-reuse-dropdown.png) · [new Context row](../sessions/2026-06-12/evidence/3370-E3-demo-pg-new-context-row-shop.png) · [wire walk](../sessions/2026-06-12/evidence/3370-envtest-provisioning-journey.txt) |
| 7 | generality: valkey keyspace Context through the SAME mechanism, declarations only | ✅ local | [keyspace Context row](../sessions/2026-06-12/evidence/3370-F2-app-demo-cache-keyspace-context.png) + wire walk §5 |
| 8 | ≥2 embedded DBs eliminated (pda + SME seams behind enable_shared_pg) | PR open | byte-identical default renders proven in the PR; live elimination needs a SHARED_PG prov post-merge |
| — | Application CRD CEL admission on a real apiserver (bootstrap waiver accept/reject) | ✅ local | [CEL walk](../sessions/2026-06-12/evidence/3370-envtest-crd-cel.txt) |
| — | API wire: 3 instances + Contexts (§5 target table) + /sovereign/apps instance rows | ✅ local | [wire capture](../sessions/2026-06-12/evidence/3370-envtest-api-wire.txt) |

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
