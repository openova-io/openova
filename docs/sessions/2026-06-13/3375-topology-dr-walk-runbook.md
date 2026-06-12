# #3375 topology/DR walk runbook — DoD-6/7/8/9/10/11

> **Executable the moment hw130 is walk-ready** (console=200 + 0 non-Ready bp-*
> sustained 15 min). Every walk is **continuum/Flux-driven, zero-touch** — no
> kubectl surgery, never wipe, never touch the bastion. Grounded in the proven
> hw128 region-kill PASS (`docs/sessions/2026-06-11/hw128-region-kill-walk-PASS.md`,
> 3s promote, zero data loss) + the agreement rows (`docs/topology-matrix.md`).
>
> Env: `console.hw130.omantel.biz`, `deployment_id 526540f27f4acfbc`,
> kubeconfigs `/tmp/hw130-a.kubeconfig` (region A / primary) + `/tmp/hw130-b.kubeconfig`
> (region B / secondary). Console session: mint a 5-min RS256 JWT with
> `/tmp/hw-priv.pem` → `/auth/handover?token=…` → Playwright.

## Pre-walk gate (HARD — do not start until ALL true)
1. `curl -s -o /dev/null -w "%{http_code}" https://console.hw130.omantel.biz/` → **200**.
2. `KUBECONFIG=/tmp/hw130-a.kubeconfig kubectl get hr -A` → **0 non-Ready bp-***, sustained 15 min.
3. Same on `/tmp/hw130-b.kubeconfig` (region B).
4. `bp-openbao` rolled to **≥1.2.30** (the proxy-glob fix; `kubectl get hr bp-openbao -n flux-system -o jsonpath='{.status.history[0].chartVersion}'`).

## DoD-6 — cnpg-pair region-kill, CONTINUUM-DRIVEN (the row, re-walked)
Agreement row (`bp-cnpg-pair`): sync `remote_apply` over ClusterMesh; continuum
lease + `cnpg promote` + PowerDNS lua-record flip `db.<org>.<sov>`; ~5s write
disruption. hw128 proved it HAND-DRIVEN (3s promote); this re-walks it via the
Continuum CR so promotion is NOT a manual patch (the hw128 finding #1 fix).

| Step | Action | Expect |
|---|---|---|
| 6.1 | Confirm `Continuum.dr.openova.io` CR exists for the pair (`kubectl get continuum -A`) + `continuum.enabled=true` on the cnpg-pair HR | a CR named `*-continuum`, status reconciled by the continuum-controller |
| 6.2 | Confirm WAL streaming: on region-b replica `SELECT * FROM pg_stat_replication` | `streaming` |
| 6.3 | `INSERT INTO walk_proof` on region-a primary; `SELECT` on region-b replica | row present cross-region (data-level replication) |
| 6.4 | **HARD-kill region A** (batch-stop the 4 region-a ECS via Huawei AK/SK — the hw128 mechanism; NOT a wipe) | region-a kube API dead |
| 6.5 | Poll region-b replica every 15s through the kill | proof row served on EVERY tick — zero unavailability |
| 6.6 | Observe the **continuum-controller** drive the promote (lease expiry → `cnpg promote` → PowerDNS lua flip) — NOT a manual patch | `pg_is_in_recovery()` flips t→f; RTO measured vs the row's ≤60s / hw128's 3s |
| 6.7 | `INSERT` on the promoted survivor | accepted — survivor read-write |
| 6.8 | Record RTO + RPO (RPO=0 for sync) with timestamps + screenshots | evidence/ |

## DoD-7 — gitea region-kill (PG flip + SeaweedFS blob-mirror)
Agreement row (`bp-gitea`): PG via cnpg-pair sync + Git blobs on SeaweedFS S3
mirrored cross-region; switchover = continuum PG flip + PowerDNS flip
`gitea.<sov>` (30s TTL), push paused ~5s. Requires `objectStorage.enabled=true`
+ `objectStorage.mirror.enabled=true` on the gitea HR (default-OFF; the
per-Sovereign overlay enables it for the DR walk).

| Step | Action | Expect |
|---|---|---|
| 7.1 | `git push` a commit with an LFS blob to `gitea.hw130.omantel.biz` BEFORE the kill | push succeeds; blob object present in region-A SeaweedFS bucket `gitea-blobs` |
| 7.2 | Verify the blob is mirrored to region-B SeaweedFS (the `weed filer.remote.sync` Deployment) | object present in BOTH regions before the kill |
| 7.3 | HARD-kill region A | region-a dead |
| 7.4 | Continuum flips PG + PowerDNS `gitea.<sov>` to region B | within the row's RTO (push paused ~5s) |
| 7.5 | `git clone` + `git push` (incl. the LFS blob) against the survivor | succeeds within the row's RTO — repo reachable + pushable, blob fetchable from region-B mirror |
| 7.6 | Screenshots + the clone/push transcript | evidence/ |

## DoD-8 — openbao region-kill (snapshot-replication, KV reads uninterrupted)
Agreement row (`bp-openbao`): Raft in-cluster; cross-cluster async perf-replication
(OSS snapshot+restore); switchover = continuum runs `vault operator raft
transition-to-primary` on standby; **KV reads continue uninterrupted on replica
throughout**. Requires `snapshotReplication.enabled=true` (primary CronJob saving
to S3; secondary fetching to the restore-staging PVC).

| Step | Action | Expect |
|---|---|---|
| 8.1 | Write a KV secret on region-A openbao; confirm the primary snapshot CronJob shipped a snapshot to `s3://openbao-snapshots/latest.snap` | snapshot object present in SeaweedFS |
| 8.2 | Confirm region-B secondary CronJob fetched `latest.snap` to the restore-staging PVC | snapshot present on region B |
| 8.3 | Start a KV-read loop against region B (the replica) | reads succeed |
| 8.4 | HARD-kill region A | region-a dead |
| 8.5 | KV-read loop on region B continues through the kill | **uninterrupted** — the row's core assertion |
| 8.6 | Promotion: `bao operator raft snapshot restore` of the staged snapshot + (continuum follow-on) `transition-to-primary` | region B serves the secret read-write |
| 8.7 | Read the secret written in 8.1 on the promoted region B | value matches (RPO within the async snapshot interval) |

## DoD-9 — rejoin (no split-brain) — once per walked row
hw128 finding #3: region-a re-join is a Day-2 gap (CR-level split-brain). Walk the
documented rejoin per each walked row's procedure.

| Step | Action | Expect |
|---|---|---|
| 9.1 | Batch-START region A (Huawei AK/SK) | region-a recovers |
| 9.2 | Per the row's rejoin: region A's pair-half DEMOTES and rejoins as replica to the promoted region B (continuum switchover sequencing) | no split-brain; single primary |
| 9.3 | Verify console + the walked apps healthy AFTER rejoin BEFORE the next kill | all green |

## DoD-10 — console truth (instance page + catalog card show the converged declaration)
| Step | Action | Expect |
|---|---|---|
| 10.1 | Console → catalog card for a walked app (e.g. cnpg-pair / gitea) | shows exactly the converged `spec.topology` declaration (one card per multi-region instance, #3370) |
| 10.2 | Instance detail page | the per-cluster roles (active/passive) + endpoint match the agreement row |
| 10.3 | Screenshots | evidence/ |

## DoD-11 — evidence + UAT
- All screenshots + transcripts under `docs/sessions/2026-06-13/evidence/`.
- Flip the relevant rows in `docs/ledger/UAT.md` with evidence links + the measured RTO/RPO.
- Comment the DoD-box table on #3375; close the issue once walked.

## Zero-touch guardrails (NEVER violate)
- Walks are continuum/Flux-driven. The ONLY allowed imperative action is the
  region-kill itself (batch-stop ECS via Huawei AK/SK — reversible, the hw128
  mechanism) and the documented operator failover patch where a row's continuum
  dispatch is a CLASS-B follow-on (openbao `transition-to-primary`).
- NEVER wipe. NEVER touch the bastion (`bastion-openova` / EIP 212.72.24.20).
- After each rejoin, verify health BEFORE the next kill.
