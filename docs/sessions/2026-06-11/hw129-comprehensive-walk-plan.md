# hw129 comprehensive walk plan — every open status/uat issue mapped to ONE pass

> Env: `hw129.omantel.biz` (`537bd50c8e9c3eb0`), fired 2026-06-11 ~14:45Z with
> `SHARED_PG=true` — the FIRST shared-Postgres prov AND the first prov carrying the
> #3307 VPC peering + the full 6-layer #3241 mesh chain in the image. Walk fires the
> moment status=ready. Each row = one issue driven to closure (or an honest
> keep-open verdict). Acceptance = clickable steps + screenshots, per UAT doctrine.

## A. The prov itself validates (no clicks needed)
| Issue | Check | Closes? |
|---|---|---|
| #3307-chain (#3241 closed) | `tofu` created the VPC peering + routes; mesh reaches 1/1 BOTH regions zero-touch (no API hand-fix like hw128) | re-validates closed work |
| #3232-chain (closed) | both region CNIs up | re-validates |

## B. Shared-PG — the headline (#3188 / #3285)
| # | Go to / do | You should see | Issue |
|---|---|---|---|
| B1 | (kubectl) `shared-data` ns | `shared-pg` CNPG cluster Running (the ADR-0010 engine) | #3285 |
| B2 | (kubectl) gitea + harbor namespaces | NO bundled `gitea-pg`/`harbor-pg` clusters; pods bind the REFLECTED `*-database-secret` | #3285 → close |
| B3 | console → `/catalog/bp-cnpg` (or bp-postgres) | **Data instances panel: 1 PostgreSQL engine instance, gitea + harbor listed as consumers** — the founder's multi-card multi-instance ask | #3188 major tick |
| B4 | gitea.hw129 + registry.hw129 UIs | both apps WORK on the shared engine (login + browse) | #3285 |

## C. SSO + console (re-confirm on fresh env + close residue)
| # | Go to / do | You should see | Issue |
|---|---|---|---|
| C1 | console login → real PIN from Stalwart | lands on /dashboard | regression guard |
| C2 | apps: grafana/openbao/guacamole/gitea/harbor via openova-sso | all land IN-app (hw128 parity) | #3263 rows |
| C3 | console AppDetail → **Launch** button on grafana | silent SSO lands in grafana FROM THE CONSOLE PATH (launch-url) | #3150 #2743 → close if PASS |
| C4 | AppDetail of newapi / flow-server / keycloak | Open button NOT dead (or honest non-UI presentation) | #3224 |
| C5 | openbao login page | still 1-click (shim unshipped) → keep open, re-evidence | #3226 |
| C6 | `pdns.hw129.omantel.biz` | currently the bare API host; redirect to pdns-admin NOT shipped → expect FAIL → drive the chart fix after walk | #3225 |

## D. Catalog + gitea bootstrap
| # | Check | Issue |
|---|---|---|
| D1 | console Catalog tab count ≥ 60 (hw128 showed 63) — Gitea public-catalog mirrored | #3159 → close if ≥60 |
| D2 | gitea org `catalog-sovereign` exists + populated | #2879 → close if present |

## E. Platform structural re-verdicts
| # | Check | Issue |
|---|---|---|
| E1 | sso-bridge pod resolves SVC-DNS (its 0.2.13 reconciler works → gitea/harbor SSO configured zero-touch is the proof) | #2861 → close if SSO bootstrap worked |
| E2 | kyverno: the 3 baseline Enforce patterns ADMIT real workloads (hook Job admitted = #3297 datapoint; verify the other two patterns per issue body) | #3248 |
| E3 | jobs page: BOTH regions (hw128 parity) | #3263 row |

## F. Known keep-open (walk only re-evidences; closure needs code)
- #3195/#2922 — promotion automation (Continuum) — operator-driven today (3s manual promote)
- #3277 — mothership stale running jobs post-handover
- #2847 — Velero OBS backup blueprint (feature)
- #3140 — wipe destroy on empty regions (mothership code; merge AFTER convergence)
- #3263 — closes only when its remaining rows (funnel F1, vCluster T4) walk

Refs #3188 #3285 #3263 #3150 #2743 #3224 #3225 #3226 #3159 #2879 #2861 #3248
