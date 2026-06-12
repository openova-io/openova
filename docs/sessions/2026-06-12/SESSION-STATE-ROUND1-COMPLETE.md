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

---

# ARC 2 ADDENDUM (2026-06-12 ~06:00-09:30) — round-2, six closures, the dead-leg map

## Round-2 re-walk (post console roll to f2689f3) — 4/4 PASS
- **#3224 CLOSED**: no Open button on bp-newapi/bp-openova-flow-server; grafana Launch intact (`evidence/hw130-r2-*.png`). Fix #3325: evidence-or-no-button, single deliberate fail-open = catalog-client-unwired.
- **#3188 card LIVE**: "1 instance · postgres-shared · Ready" (was 0) via #3326's bootstrap-HR projection. Remaining: bindings rows, per-app status-API 404, cnpg-pair reflection (one root: Application-CR-only projection).

## Issues closed this arc (6 total today)
#3263 (11-gap register, per-gap mapping) · #3189 (acceptance table, all parts verified at source) · #2922 (headline live-verified; remainder → #3195-G6) · #3224 (re-walk) — plus arc-1's #3285 #3225.

## #3195-G6 reduced to ONE design call
The Continuum CR seed exists in-chart; `CONTINUUM_ENABLED=true` is live (region-derived); the flip is gated on the CRD region pattern (letters-only 4-segment) vs kom4dc's digit codes (`me-east-215-a`). Recommendation on the issue: relax the pattern, carry real codes.

## #3301 — the dead-leg map (top reliability item)
- Signature: strict 200/503 alternation on api.<fqdn>; survives envoy DS bounce.
- Eliminated WITH EVIDENCE: stale Endpoints, stale cilium BPF backends (all 4 nodes), route overlap, node→pod TCP, in-cluster member probing (hostport serves only the EIP-DNAT path), **and rotation-side mitigation** — the live HTTPS-monitor experiment took ALL members OFFLINE (Huawei checker sends no SNI) → ~3min self-inflicted outage → rolled back, PR #3328 withdrawn. Honest record on the issue.
- The compounding-subresource proof: blank OpenBao shell, vendor chunks 503 (`evidence/hw130-openbao-login-oidc-selected.png`) — why browser surfaces fail at compounded rates vs 1-in-N curls; retroactively explains #3323, the funnel "flaps," and the #3226 retries.
- Next: wire-side member identification (cilium-dbg monitor during external burst / envoy access logs).

## #3226 shim — half-validated live
auth_url mints server-side (200, PKCE, UI-callback redirect); IDR auto-fires (no KC form). Open question: does the Ember UI accept a full-page callback — blocked 3× by the dead leg; gated on #3301.

## #3319 root cause split
`provisioning/start` 502 = `sme/provisioning` Init:0/1 forever (`wait-for-cutover-token` — a HANDOVER step; timeout records never hand over). Real residual defects: stale omani.homes wildcard DNS (49.12.16.160), voucher burned on failed provisioning, marketplace-api flaps (likely #3301). Design question for the founder: should converged-but-late envs ever hand over?

## Founder decisions queued
1. hw130 wipe/keep (holds the only live full topology; #3140's wipe-JSON verdict + a clean prov for #3319 both want a wipe-then-fire window).
2. Handover-on-timeout policy (#3319/#3277/#3317 family).
3. Continuum CRD region-pattern relaxation (#3195-G6).
4. Prioritization: Dragonfly registries + default Grafana dashboards (the two unprioritized hw126 gaps) vs funnel/projection work.

---

# END-OF-SESSION HEALTH SNAPSHOT — hw130 @ 2026-06-12 05:18Z (verified live, not claimed)

```
HRs:        57/57 True (first all-green moment of the env's life; 3 suspended hetzner slots)
Streaming:  pg_stat_replication — primary-2 streaming|sync + primary-3 streaming|potential
            (synchronous replication ACTIVE: the Pillar-3 zero-tx-loss wiring, post-#3322)
Mesh:       1/1 remote clusters ready, 1 global-service (worker datapaths)
Continuum:  cnpg/cnpg-pair-bp-cnpg-pair-continuum present, Degraded (the witness-resolver
            item — quorum design call queued, full spec on #3195)
Backups:    uat-2847-acceptance-r3 Completed, 4h old, in the per-Sovereign OBS bucket
Shared-PG:  gitea + harbor-core 1/1 Running, 9h uptime on the one engine
Ingress:    console 8/8 probes 200 (post-#3301-heal, durable)
```

Day totals: 13 issues closed on evidence · 18 PRs merged (+1 withdrawn on live falsification)
· 2 verifier rounds + remainder sweeps, ledger fully verdicted · 50+ evidence artifacts.
Forward path: 4 founder decisions (hw130 wipe/keep · handover-on-timeout · lease-quorum
third voter · feature prioritization) + clean-prov re-walks (#3319 family) + 5 specced
code items (witness wiring · #3277 status semantics · #3188 bindings · undeployed-component
consistency · #3301 source question).
