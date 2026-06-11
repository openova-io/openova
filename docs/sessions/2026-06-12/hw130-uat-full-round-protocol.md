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

## Executable verifier briefs (dispatch verbatim on HW130-READY)

### wave1-topology (parallel, read-only kubectl)
Kubeconfigs: fetch via mothership PVC (`kubectl -n catalyst exec deploy/catalyst-api -- cat /var/lib/catalyst/kubeconfigs/<id>.yaml` and `<id>-me-east-215-b-1.yaml`). Verify + return verdict-per-row with command outputs: (a) both regions' nodes Ready; (b) `cilium-dbg status` on one agent PER REGION shows `1/1 remote clusters ready`; (c) peer secret key matches the live `cluster-name` of the peer (no digit mismatch); (d) cnpg-pair: primary Cluster healthy region-a, replica `ready>=1` + `pg_stat_replication` shows `streaming` on the primary; (e) the tofu-created VPC peering exists ACTIVE (name `catalyst-hw130-…-peer-*`). NO writes of any kind.

### wave1-shared-pg (parallel, read-only kubectl)
On the primary kubeconfig: (a) `shared-data` has `shared-pg` Cluster healthy + Database CRs `shared-pg-gitea`/`shared-pg-registry` APPLIED; (b) **NO `gitea-pg` Cluster in ns gitea, NO `harbor-pg` in ns harbor** (#3309's one-flag contract); (c) `gitea-database-secret` in ns gitea has `host=shared-pg-rw.shared-data.svc.cluster.local` (likewise harbor); (d) gitea + harbor pods Running and their DB env/secret refs point at the reflected secret. Return per-check verdicts + raw outputs.

### wave1-endpoints (parallel, curl only)
(a) `https://pdns.hw130.omantel.biz/` → 302 with `Location: https://pdns-admin.hw130.omantel.biz/` **port-free or :443** (#3310, closes #3225); (b) jobs API: both `install-me-east-215-b-1:*` and primary jobs present; (c) 8-probe stability sweep over console/auth/api/gitea/registry/grafana/bao hosts — record any 503 (the #3301 ledger); (d) all hosts resolve + TLS valid.

### wave2-console (sequential, Playwright — dispatcher runs flake-protocol first)
Real-PIN login (mailbox `emrah.baysal@openova.io`, secret `stalwart-user-credentials` key `emrah.baysal` ns `stalwart`). Rows: dashboard treemap shows BOTH region groups; Cloud page graph 2/2 regions; Apps Deployments≈49 + Catalog≥60; `/catalog/bp-cnpg` Data-instances panel (engine card + post-#3309 instance/consumer state — record EXACTLY what renders); jobs UI Region column shows both; `/app/bp-newapi` + `/app/bp-openova-flow-server` show **NO Open button** while `/app/bp-grafana` shows it (#3224). Screenshot EVERY row to evidence/.

### wave3-sso (sequential, Playwright)
Per app — grafana (via AppDetail **Launch**), openbao, gitea, harbor, guacamole: land AUTHENTICATED in-app; screenshot each; openbao records the 1-click residue honestly (#3226 stays open unless zero-click). Retry once on a 503 flake; record every flake hit.

### wave4-funnel (sequential, Playwright)
BSS → create voucher → `marketplace.hw130.omantel.biz/redeem/?code=…` → checkout → Organization created → tenant console reachable → tenant login. Tenant Org domain per canon (omani.homes pool). Screenshot every step; any FAIL gets the exact failing request captured.
