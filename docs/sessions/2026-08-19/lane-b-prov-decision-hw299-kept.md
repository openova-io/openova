# Lane B (prov executor) — 2026-08-19: NO FIRE. hw299 was alive all along; the "dead env" was a mothership DNS outage

**Verdict: the planned wipe(hw298)→fire(hw300) cycle was NOT executed, because live state re-verification (PROTOCOL Step-0) refuted the plan's premises.** No env was wiped, no env was fired, zero of the 2-fires/day budget consumed.

## What the plan assumed vs what live state showed

| Plan assumption (derived from origin/main UAT.md, wf_7eb86a00-b1a) | Live state 2026-08-19 ~06:00Z |
|---|---|
| hw298 (dep `2540d866403f1f7c`) is the current env, post-cutover severed, needs wipe | hw298's record is GONE from the mothership store (`/var/lib/catalyst/deployments/` has no `2540d866403f1f7c.json`) — wiped 2026-08-16 |
| No live successor; fire hw300 on omani.works | **hw299.omantel.biz (dep `e13300fc41a33a57`) is `status: ready`**, fired 2026-08-16 08:14Z, ready 09:03:30Z, 2-region me-east-215-a/-b, 5+5 workers — the ONLY record in the store |
| hw299 is "tainted" (bogus CSV row) / believed reaped (PR #6468: "NXDOMAIN, all doors 000") | hw299's cluster is healthy (all nodes Ready), console answered **200 via LB IP `212.72.24.35`** the whole time. Only public DNS was dead |

## Root cause of the 3-day "hw299 is dead" belief — mothership PowerDNS outage

Chain, each link verified live:

1. `omantel.biz` NS = ns1/ns2.openova.io → **45.151.123.50** = mothership `powerdns-anycast` LB Service.
2. Mothership `openova-system/pdns-pg-1` (CNPG) was **ImagePullBackOff** since ~2026-08-16 evening: its image `harbor.openova.io/proxy-ghcr/cloudnative-pg/postgresql:16` returned **503** from Harbor.
3. Harbor (also on the mothership, ns `openova-harbor`) was itself down in a **circular pull dependency**: every Harbor component's image references `harbor.openova.io/proxy-dockerhub/goharbor/...` — Harbor pulls Harbor through Harbor. After the 2026-08-16 pod-restart wave, nothing could self-recover (harbor-redis/pg/registry/jobservice all ImagePullBackOff, harbor-core CrashLoop on dead redis).
4. → `powerdns` CrashLoopBackOff (554 restarts, "connection refused" to pdns-pg-rw) → dnsdist had no backend → **SERVFAIL for every mothership-served zone** → every `hw299.omantel.biz` door read 000/NXDOMAIN → PR #6468 concluded "reaped".

This is the canon class `never declare an env dead without probing its own record+fqdn` + `check external vantage before alarm`: the deployment record said `ready`, ping to the CP EIP answered, and `curl --resolve` against the LB IP returned 200 — three probes that were cheaper than the wrong conclusion.

**Fix applied 2026-08-19 ~06:25Z (live patches on the mothership):**
- `cluster.postgresql.cnpg.io/pdns-pg` (openova-system) + `cluster.postgresql.cnpg.io/harbor-pg` (openova-harbor): `spec.imageName` → `ghcr.io/cloudnative-pg/postgresql:16` (upstream; identical content the proxy would have served).
- Harbor deploy/sts images → upstream `docker.io/goharbor/*:v2.14.3` (core, jobservice, portal, registry+registryctl, redis), breaking the circular dependency.
- Stuck pods deleted for controller recreation.

**Result:** `dig @1.1.1.1 console.hw299.omantel.biz` → `212.72.24.35`; console/marketplace/api doors all **200** via public DNS (re-verified 2026-08-19 ~06:45Z).

Follow-up (source-side): the mothership manifests carrying the self-referencing image refs live outside this repo (mothership Flux is suspended; live patch is the operative fix). The Harbor-pulls-Harbor bootstrap defect needs a durable source fix — Harbor's own components and the DNS-critical pdns-pg must NEVER pull through the Harbor they serve.

## Why hw299 was KEPT (no wipe, no hw300 fire)

- `cutoverComplete=false`, zero cutover steps run — **pre-cutover, GitHub-tethered**: Flux GitRepository `openova` is READY at `main@d6cf5317a` — the current origin/main HEAD at verification. Every merged fix the 8-hour plan needs (guacamole producer `8423fa355`, catalog 0.2.43 pin, agenity emitter) is ALREADY DELIVERED to this env; live check confirms `bp-guacamole@0.2.43` upgrade succeeded and the `bp-agenity` 0.5.28 card is listed.
- Wiping a working, converged, current-HEAD Sovereign to fire an identical successor is the stoned anti-pattern (`feedback_never_wipe_a_working_sovereign_to_deploy_code`, RT-7) and would have cost ~2.5 h of the critical path plus a fire from the 2/day budget.
- The "skip hw299, use hw300+" instruction traced to the bogus `uat-cycles.csv` row (`2026-08-16,hw299,…,198,41,…,69.2` — stale-tally output masquerading as a measurement). That row is DATA to quarantine (Lane E), not a reason to rename or destroy a live Sovereign.

## Ledger actions (this commit)

- `scripts/reset-uat.py hw299` (carry-forward, AFTER-ready per RUNBOOKS §0.3 — the correct order; the brief's "reset first" reflected the superseded #3132 flush-rule).
- Ported the 6 orphaned hw299 stamps from unmerged branch `uat/hw299-228` (commit `634e5e2df`, walked 2026-08-16 on THIS still-live machine; only the 3 stamp cells differ from main): rows **15, 19, 25, 241, 242** ⏳→✅ and **row 228 ❌→✅** (the hw298-wipe→hw299-refire IS the row-228 event, janitor sweep included).
- Header rewritten to the measured truth above. WBS §1 regenerated (`uat-partition.py --write`).
- Guards before push: `uat-drift-guard` OK (64 carried with scheduler's consent), tally 286 rows parsed, `test_uat_table_shape` / `test_uat_tally` / `test_uat_clause_identity` / `check-uat-fix-sha-citations` / `uat-partition --check` all exit 0.

**Tally after this commit: 239 ✅ / 17 ❌ / 25 ☐ / 5 ⏳ = 83.6%** (was 243/18/25 with a dead-env header; the delta is honest carry-forward minus the 6 restored same-machine stamps).

## Walk-readiness state handed to Lane C

- Doors 200 via public DNS; PIN-auth mail path NOT yet re-verified this session (mothership Stalwart was Running; OTP folder rule applies).
- 25 SSO rows (26–28, 30–45, 109–114): env carries `8423fa355` — walkable now.
- Row 115: guacamole 0.2.43 live, admin-enroll job completing.
- Row 218: bp-agenity card listed (0.5.28); per-Org installs in bssdoor/bssdoorm FAILED 2026-08-16 on `context deadline exceeded` (convergence-time contention) — retries kicked 2026-08-19 via suspend/resume; re-check before walking 218/219.
- mailwalk `bp-stalwart-tenant@0.1.15`: real defect — `no matches for kind "Certificate" in version cert-manager.io/v1` inside the vcluster (cert-manager CRD not replicated). Needs an issue + fix; blocks row 234's mail leg on that Org (a fresh funnel Org may hit the same).
- G8/G9/220/221: `catalyst-system/sovereign-anthropic-credentials` EXISTS on hw299 (auto-seeded at postback 2026-08-16) but the OAuth blob is 3 days stale (~4 h life) — PATH B re-seed with a FRESH founder blob immediately before the agentic walk.
- PR #6468 ("no live env") is refuted by the above and should be closed, not merged.
