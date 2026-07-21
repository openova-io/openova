# hw283 — phase-1 failure forensics (2026-07-21)

**Env**: hw283.omani.works, dep `101cd122d394af22`, 2-region Huawei me-east-215-a/b, fired on the 5-PR train baked into mothership catalyst-api `22cc9ad` (#5303/#5307/#5308/#5309/#5310), FIRECUT=true, PROD certs.

**Outcome**: fired clean (preflight all-green), converged region-a, **failed in phase-1 on region-b** (~90min). The fresh-prov cycle surfaced a genuine P0 2-region defect — filed **#5317**.

## Root cause chain (live-diagnosed, debug-before-wipe)

1. **region-b `keycloak-0` stuck `Init:0/1` for 78min.** Pod event ×45: `MountVolume.SetUp failed for volume "keycloak-secrets": secret "keycloak-database-secret" not found`.
2. **The consumer-hub secret sync no-op'd.** `keycloak-database-secret` exists on region-A keycloak ns (110m) but is **absent from region-B entirely** (no namespace; region-b `shared-data` has zero consumer secrets). Yet ClusterMesh logged `synced 13 shared-pg consumer-hub Secret(s) (harbor-database-secret, gitea-database-secret, keycloak-database-secret, grafana-database-env…)` — logs success, delivers nothing.
3. **keycloak flux `RetriesExceeded`** (installFailures=2): config-cli post-install hook failed 6× because keycloak-0 never started.
4. **Cascade wedge**: `bp-gitea`/`bp-sso-bridge`/app-tier all `dependency 'bp-keycloak' is not ready` → region-b stuck **52/67 HR** (region-a fine **62/67**) → phase-1 watch context-cancelled → deployment `failed`.

DBs were healthy throughout (region-b `shared-pg` "Cluster in healthy state", all cnpg clusters ready) — the defect is purely the secret sync, not the database.

## Why #5263 (the #5261 fix, in the train) didn't cover it

#5263 (`mesh cascade force-reconciles replica-region stalled dependents`) force-reconciles the stalled keycloak HR **once the secret is delivered** — it fixed keycloak exhausting retries before a *late* secret (validated on hw281, secret synced 12:39Z). Here the secret is **never delivered** (the sync no-op's), so #5263's force-reconcile never fires. The defect is **upstream** of #5263: a flaky/racy cross-region secret sync (worked hw281, no-op hw283). Suspected mechanism: the cascade logs 'synced' optimistically before/without confirming the write landed, and/or runs once before region-b's consumer namespaces exist and never retries. Fix direction (#5317): verify-and-retry — read-back each secret on region-b, re-run until all present, log the honest confirmed count.

## Partial train validation salvaged (region-a, converged, NO hand-heal)

- **#5303 harbor** — region-a `harbor-registry` CM renders `redirect: disable: true` ✅
- **#5307 catalog-seed** — region-a Blueprint CRs: bp-guacamole `spec.version=0.2.29` (was 0.2.3), bp-continuum `0.1.12` (was 0.1.4) ✅

The other three train fixes (#5308 funnel, #5309 timeline, #5310 cutover-prewarm) need region-b + cutover, which #5317 blocked — deferred to the re-fire.

## Recovery sequence

1. #5317 fix (agent in flight: verify-and-retry the cross-region secret sync).
2. Merge the full train: #5317 + #5312 (#5285 test) + #5313 (#5193 wipe-strand) + #5314 (#5086 Hetzner) + #5316 (#5311 Continuum) + #5301 (Day-2 harbor).
3. **Wait for catalyst-api rollout to settle** — the deploy-bot rolls catalyst-api on every merge (even chart-only), opening a roll window that abandons in-flight wipes/fires (this session's hw282-wipe-abandon lesson).
4. Wipe hw283 (diagnostics captured; cloud-init log saved).
5. Re-fire hw284 → full zero-touch validation → cutover cc=true → region-kill G12 → UAT sweep.

**Not done (deliberately):** did not hand-heal region-b to force a green — the zero-touch metric requires the clean re-fire on the fixed train. hw283 stays until re-fire-ready (one-env gate) so it isn't wiped mid-roll.
