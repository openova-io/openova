# hw256 walk plan — fix-train validation + ledger re-sweep

**Env**: hw256.omani.works (dep `ea60815abb0cc186`, fired 2026-07-15T15:58:50Z, sharedPG=true, fireCutoverOnHandover=true, 2-region me-east-215-a/b).
**Gate**: walks start ONLY after `cutoverComplete=true` (zero-touch chain; merge-hold in force until then — hw252 lesson).
**Ledger state**: reset-on-fire flushed 164 cells (`edecb4be6`); all prior evidence preserved in git history.

## Wave 0 — fix-train validation (each row cites its fix; walk = the fix's live proof)

| # | Validates | Method | Pass bar |
|---|---|---|---|
| V1 | #5107 region-b probe (self-healing verdict) | deployment record post-ready | `regions[b].hrReady ~58/65`, `secondaryDegraded=false`, single 201 PUT-back |
| V2 | #5099/#5101 shared-pg default-ON | kubectl ns shared-data + Application CRs | shared-pg trio rendered: CNPG cluster present, 8 Application CRs (not 5), slots 16a/16c/16d live |
| V3 | #5109/#5104-A per-Org apps namespace | funnel walk: purchase app → flux | `catalyst-tenant-<slug>-apps` Ready=True; purchased app pod Running; NO "namespaces not found" |
| V4 | #5118/#5104-B topology surcharge | funnel walk: plan-M + hot-standby checkout | review total == checkout ORDER SUMMARY == billed amount == 14 OMR; order record `topology=active-hot-standby` |
| V5 | #5116/#5104-C mail Date header | IMAP fetch of the PIN mail | `Date:` header present + RFC-5322 parseable |
| V6 | #5115/#5113 catalog card-save commit leg | catalog-edit walk rows 127-158 | committed YAML carries spec.version + canonical name + no uid/rv leak; aggregator failure → committed:false; live CR NEVER pruned |
| V7 | #5117/#5114 bp-openova-mcp slot 13d | kubectl + HTTP probe | HR Ready in catalyst-system; `GET /healthz` 200; `POST /mcp` tools/list bearer-scoped; **Service=ClusterIP, HTTPRoute present, ZERO NodePort** |
| V8 | #5117 sandbox retirement | kubectl get hr -A | NO bp-sandbox HR anywhere (slot 19a tombstoned) |
| V9 | cutover 0.1.136 flap armor (#5103) | cutover status-CM | 11/11 steps success; prewarm survived any transit flap without FATAL |
| V10 | row 3 / #4546 owner-redeem redirect | browser, MARKETPLACE customer session (per dc0f787dd method) | owner of live Org → /redeem/?code → lands per-Org console via /launching, never the funnel |

## Wave 1 — G12 region-kill (first cc=true env with a healthy region-b plane)

Kill region-a primary post-cc → region-b promotes (bar: RTO ≤30s, RPO=0, split-brain guard) → restore + re-gate. Unblocks rows 66/187 family + G12.

## Wave 2 — full re-sweep

Re-stamp the 164 reset cells: fleet read-only walkers (backing-state rows) → authed browser wave (session-walled rows; mint owner session via PIN flow on hw256) → mutation walks (Edit-IaC, funnel E2E ×2 TLDs, catalog-edit). Anchors: walkers own evidence format (env+date+proof), no fabrication, §854 scan must return 0 NodePorts (G11).

## Standing founder-decision register (unchanged by this cycle)

- iogrid/iogrid#844 (ACME solver ClusterIP + 3 iogrid TLS architecture calls) — founder's repo/merge.
- `iogrid/proxy-gateway-socks5` swap — staged manifest, awaits founder go (#5088).
- `cinova/catalog-svc` — ⛔ SME never-touch until founder lifts (#5088).
- #4277/#4111 Anthropic credential (Pillar-4 D2 walk gate).
