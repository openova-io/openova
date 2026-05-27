# 2026-05-27 — hw30 prov failure modes RCA

## Surface
hw30.omani.works fresh Sovereign provision on Huawei Cloud Stack (HCS) Kom4DC me-east-215.

## Session shape
12 prov attempts (#4 → #15), 5 distinct failure modes RCA'd + fixed via Waves 5.135 / 5.136 / 5.137 / 5.138.

## Attempt-by-attempt failure ladder

| Wave fix | Bug RCA'd | Symptom | Permanent fix |
|---|---|---|---|
| 5.134 | HCS cell-affinity on numerical worker names | `Common.0021: CollectInfoTask-fail` recurring on same indices w3/w5/w6 across 9 attempts on hw30 #1-#9 | Worker name = `hash(deployment_id + region + index)` (non-numerical suffix, ~1.9e-6 collision prob) |
| 5.135 | wipe.go emit() send-on-closed-channel race | `panic: send on closed channel` killing catalyst-api mid-wipe → HCS resources stranded | deployments.go skip close when Status=="wiping"; wipe.go emit() defer recover() + snapshot under mu |
| 5.136 | runTofu error string lacks Common.0021 marker → retry-loop substring matcher never fires | hw30 #12 failed in 1 attempt despite the 6-attempt loop being present | streamLinesTee captures stderr to 32KB bytes.Buffer; appended to returned error on Wait failure |
| 5.137 | 30m Provision ctx truncates the 95-min retry envelope | hw30 #13 failed at 32m43s with "context deadline exceeded" between attempts 4 and 5 | Bump ctx 30m → 180m at deployments.go:1642 |
| 5.138 | EIPs untagged (Wave 5.4 disabled tags) → per-deployment Wipe can't see prior orphans → publicIp quota cap (10/10) | hw30 #14 failed at 50s with "error allocating EIP: conflict" | Janitor sweep by bandwidth_name prefix `catalyst-*` + status=DOWN + unbound |

## Deeper bug: HCS scheduler bad-cell affinity within-prov

Wave 5.134 fixed CROSS-prov affinity (different deployment_ids → different hash → fresh cells). But WITHIN a single prov, retries use the SAME hash because deployment_id is constant across retries. So HCS scheduler keeps picking the same broken cells.

Pattern observed on hw30 #15 (33175ce0eace614f):
- Attempt 1: tofu plans 8 workers, HCS creates some, Common.0021 fires on subset
- Attempts 2-5: tofu retries with SAME names for the failed ones → HCS scheduler picks SAME broken cells → Common.0021 fires AGAIN on the same indices
- Each attempt may incrementally succeed 1-2 workers (HCS cell health fluctuates)

After 5 attempts: 5 of 8 workers ACTIVE on HCS, 3 unhealthy slots remain. CP + EIPs all bound. Tofu STILL thinks the failed workers don't exist (correct — they were rolled back).

## Wave 5.139 candidate: per-retry salt in worker name hash

If hw30 #15 fails after attempt 6, ship:

1. Add `retry_attempt` tfvar (default 0, incremented by catalyst-api before each retry's plan).
2. Worker name formula: `hash(deployment_id + region + index + retry_attempt)`.
3. Worker resource `lifecycle.ignore_changes=[name]` so EXISTING ACTIVE workers don't get destroyed when their formula-computed name changes.
4. catalyst-api Provision: before each retry's `tofu plan`, rewrite tfvars with `retry_attempt = N+1`.

Effect: on retry, tofu sees ONLY the failed (missing-from-state) workers needing creation, with NEW names → HCS scheduler picks fresh cells. Successful workers stay put because of `ignore_changes`.

## Open follow-ups

- `#2492`: helmwatch.Bridge leaks mothership-flux events into hw30's dep stream (cross-deployment pollution).
- TBD: 8-worker minimum (`minWorkers := 8` for huawei in deployments.go:1011) drives HCS scheduler load; could investigate whether HCS cell-pool size warrants reducing to 4 + cluster-autoscaler. Architecturally needed for bp-catalyst-platform CPU budget so this is a trade-off study.

## Refs

- PRs: #2488 (5.135), #2489 (5.136), #2490 (5.137), #2491 (5.138)
- Issues: #2454 (master Hetzner→Huawei migration), #2492 (helmwatch leak)
- Memory: feedback_runtofu_stderr_tee_for_retry_match, feedback_retry_loop_envelope_must_fit_outer_ctx, feedback_hcs_tags_disabled_means_orphan_resources
