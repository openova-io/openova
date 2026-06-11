# SESSION STATE — UAT ROUND-1 COMPLETE on hw130 (2026-06-12, ~05:45 local)

> The durable resume document for this session's second arc (post-hw128). Read with
> `docs/ledger/UAT.md` (the verdicted contract) + `docs/sessions/2026-06-11/SESSION-STATE-PRE-COMPACTION.md` (the first arc).

## Live environment
- **hw130.omantel.biz** (`526540f27f4acfbc`) — record status `failed` (Phase-1 watch TIMEOUT; the env itself is FULLY CONVERGED: 57 HRs Ready; timeout = recoverable per doctrine; #3317 now auto-rescues such records). Kubeconfigs: `/tmp/hw130-a.kubeconfig` + `/tmp/hw130-b.kubeconfig` (also on the mothership PVC). JWT key `/tmp/hw-priv.pem`. **DO NOT WIPE** — wipe-last rule (memory `feedback_walk_everything_before_any_wipe`) + it carries the only live shared-PG topology.
- Known env scars (timeout-record, handover never fired): console treemap + jobs UI see only region-a (the secondary-kubeconfig fan-out is a handover step). /cloud sees 2/2 (different source). These rows are environmental, not UI bugs.

## Round-1 verdicts (all by read-only verifier sub-agents; evidence in `docs/sessions/2026-06-12/evidence/`, 30+ screenshots)
- **SSO ×5: PASS, 0 flakes** (grafana 1-click silent via Launch; gitea/harbor/guacamole 0-click; openbao 2-click quirk → #3226 open).
- **Shared-PG: PASS at the DATA level** — gitea 110 tables + registry 49 on ONE `shared-pg`; zero bundled clusters; #3285 CLOSED.
- **Funnel: front-half PASS** with real artifacts (voucher WALKMART2026 → redeem → real-PIN checkout → credit-covers-order → exhausted 1/1); **breaks at `POST /api/provisioning/start` 502 + stale omani.homes wildcard DNS (49.12.16.160 Hetzner)** → **#3319** (the last seam before a running tenant).
- **Topology: cloud-graph 2/2 PASS**; mesh rescued by #3317's roll (`fullyMeshed=2`; agent tunnel connecting at write time — watcher `bbvhutxj6`); pair self-assembly follows via the two-stage flip.
- **vCluster containment: 5/5 PASS** (dmz/mgmt/rtz isolated; coraza HA).
- **#3224: fail-open root-cause spec'd** (gate in image; catalog lookup 404 → URL shown anyway). **#3301: did NOT reproduce** (16/16 stable).

## The night's source-fix chain (all merged)
#3309 one-flag shared-PG seams · #3313 chart-minted role secrets · #3314 hub direct-render (+#3316 marker hotfix) · #3317 timeout-record mesh rescue · #3307 VPC-peering IaC · #3310 pdns port · #3315 wipe peering-purge (PR, merge-ready). Wipe-leak forensics on #3140.

## Open seams, priority order
1. **#3319 funnel handoff** (provisioning/start 502 + pool DNS) — blocks tenant rows + Tier-3 SSO.
2. **#3188 card projection** — bootstrap-HR engines aren't Application CRs → card reads 0; needs the instances-projection wire.
3. **#3224 fail-open** — spec on the issue.
4. Round-2: re-verify TC-13 post-mesh, the ⛔-stamped rows as their gates lift.

## Hard-won rules now in memory
walk-everything-before-any-wipe · wipe pipelines stop before the wipe step · `git diff --check` before committing conflict resolutions · reflector can't chain mirrors + never retries late sources · CNPG never mints managed-role secrets · timeout ≠ dead.
