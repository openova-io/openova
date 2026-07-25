# status/uat backlog — live-evidence walk log (2026-07-25, hw288 `027f07559af1f9f7`)

Each open status/uat issue walked live this session (curl / Playwright / kubectl on the real surface). Infra issues (#5285/#5328/#5265/#5339) have no numbered UAT row — their evidence lives here + on the issue.

| Issue | Live walk + evidence | Verdict |
|---|---|---|
| **#5352** OOM | catalyst-api `/healthz` 200, **0 restarts / 7h**, 625Mi stable (was 28 restarts→4Gi); fix image `e4b36a5` | **CLOSED** (runtime-verified) |
| **#5341** gateway flake | `mcp.hw288/mcp` ×12 → 7/12 `404`(envoy) / 5/12 `405`(app); openbao 404 hdr `server: envoy`; console 0/6 clean; browser openbao → `ERR_HTTP_RESPONSE_CODE_FAILURE` | fix #5354 merged, **delivery-gated #5265** |
| **#5345** catalog chartless | console `/catalog/bp-redis` renders installable (v1.0.0, "+ New Instance" ×2, topologies) but ghcr = NO-PACKAGE; screenshot captured | gap real; publish-or-hide the 5 |
| **#5265** delivery gap | gitea-mirror + harbor-prewarm are ONE-SHOT cutover Jobs (ran once 2026-07-23 14:40, no recurring CronJob) → local Gitea/Harbor frozen; region-A bp-cilium stuck 1.4.16 vs ghcr 1.4.17 | **root-caused** — needs Day-2 recurring refresh |
| **#5339** region drift | region-A `vpc-podcidr-route-reconciler` = CronJob (`*/5`, 45h, pre-fix) vs region-B = Deployment (1/1, 33h, #5340) — stable split | root-caused → **#5359** |
| **#4901** Continuum | `continuums.dr.openova.io` shows 8 CRs incl cnpg-pair (Healthy); status surfaces `hotStandbyAbsent`/`standbyAvailable`/`replicationLagSeconds` + `StandbyAvailable=True` | gap **appears addressed** (self-corrected a short-name mis-read) |
| **#5285** watch-informer flood | catalyst-api `/healthz` 200, 0 restarts, 4 deps served, not starved; + 3 dedicated tests | code+test+runtime ✓, 1 failed-dep scenario left |
| **#5328** break-glass | script is in **open review-gated PR #5356** (not on main) — self-test can't run from main | review-gated |

**Filed this session:** #5358 (guacamole SSO nonce-race), #5359 (Pillar-5: cutover pivots only region-A; region-B Flux still on github/ghcr).

**Common blocker:** #5265 (frozen post-cutover catalog) gates #5341/#5358/#5339 delivery to hw288. Highest-leverage fix = a recurring Day-2 mirror/prewarm (+ region-B per #5359).
