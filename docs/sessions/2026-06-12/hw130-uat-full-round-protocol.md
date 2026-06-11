# hw130 FULL UAT ROUND — sub-agent verification protocol (founder direction 2026-06-12)

> Trigger: hw130 (`SHARED_PG=true`, omantel.biz) reaches `ready`. The round is complete
> only when EVERY row of `docs/ledger/UAT.md` carries ☑/✖/⛔-with-reason + evidence.
> NO lifecycle ops until then. Founder verdict driving this: "I still see nothing
> tangible from end user perspective … complete a full round of UAT.md."

## Dispatch plan (read-only verifiers; dispatcher re-checks live state before recording)

| Wave | Agent | Rows | Method |
|---|---|---|---|
| 1 (parallel) | verifier-topology | §1 Pillar-2/3 rows: 2-region nodes, mesh 1/1 both ways, cnpg-pair streaming, peering | kubectl/API, read-only |
| 1 (parallel) | verifier-shared-pg | §1/§3 shared-PG rows: NO bundled gitea-pg/harbor-pg (#3309), consumers bound to shared-pg, hub secrets reflected | kubectl, read-only |
| 1 (parallel) | verifier-jobs-api | jobs API both regions; HR convergence counts; pdns redirect (curl, expects 302→:443 — #3310) | curl/API |
| 2 (sequential, shared browser) | verifier-console | console PIN login, dashboard treemap both regions, Cloud graph 2/2, Apps Deployments/Catalog counts, Data-instances card (engine + consumers post-#3309), jobs UI region column | Playwright |
| 3 (sequential, shared browser) | verifier-sso | §2 rows: grafana/openbao/gitea/harbor/guacamole in-app landings + AppDetail Launch button | Playwright + Stalwart PIN |
| 4 (sequential, shared browser) | verifier-funnel | §1 Pillar-1: BSS voucher → marketplace.hw130 redeem → checkout → Org creation → tenant console login | Playwright |
| 5 (dispatcher only, LAST, destructive) | region-kill re-proof (only if founder wants it re-run on hw130; hw128's PASS stands) | §1 Pillar-3 failover row | ECS stop/promote/verify/restore |

Rules: browser waves NEVER overlap (one Playwright session); each verifier returns
row-verdicts + screenshot paths ONLY (no fixes, no kubectl writes); dispatcher applies
the #3301 flake protocol (post-roll envoy bounce + 8-probe stability) BEFORE wave 2 and
retries flake-shaped failures once before recording ✖; every ✖ gets an issue ref or a
new issue with the wire evidence.

Refs #3263 #3188 #3285 #3301
