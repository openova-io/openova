# Train manifest — hw306.omani.works (plan v1, 2026-09-02T07:20Z; fire pending gates)

Tracking issue: **#6778**. Founder decision 2026-09-02 (verbatim intent): *"proceed with the new hw306 environment to implement all the fixes piled up in the main branch … make the 306 be the best environment setup so far … follow all the principles … reach the ultimate perfect state with no going in circles."*

This file is the single plan for hw306. Every later section is appended with evidence, never rewritten; the ultracode understanding workflow (`wf_6dd712c8-553`, 10 read-only readers + synthesizer + 3 adversarial critics) enriches §3–§8 when it returns. Nothing below is a walk stamp.

---

## 1. Why this fire exists, and the rules it obeys

**Why.** hw305 (`hw305.omantel.biz`, dep `b2b00ce4c833badf`) was fired 2026-08-23 from main of that day. Since then **1263 commits** landed on main (delta by area: `products/catalyst` 402, `products/chargeback` 158 (new Blueprint, slot 13f), `clusters/_template` 94 pin bumps, `platform/self-sovereign-cutover` 24, plus postgres/external-secrets/newapi/mimir/guacamole/gitea/vpa/cnpg/kyverno/seaweedfs chart fixes). hw305 is mid-cutover and by canon *cannot receive fixes* (memory `reference_mid_cutover_sovereign_cannot_receive_fixes`): its cutover has looped on step 06 for 10 days, region-B is degraded (58/66 HRs; `bp-external-secrets` + `bp-mimir` helm-rollback, whose fix `ca1546627` #6760 is on main but unreachable), and the shape-level defects below need a fresh 2-region fire to be proven. A fresh prov is the only path that delivers the train.

**Rules in force (each is a gate, not a preference).**

| # | Rule | Source | How it is enforced in this train |
|---|---|---|---|
| R1 | **One environment at a time** — hw305 must be `wiped` in the record AND zero-remnant in the live cloud before hw306 fires | founder 2026-07-15, `feedback_one_environment_at_a_time_wipe_before_fire`, `prov-preflight.sh` check 2 | wipe → poll `wiped` → Huawei inventory diff (ECS/VPC/EIP/EVS/ELB/NAT) → preflight exit 0 |
| R2 | **Protect-list promotion** — RUNBOOKS §0.1 names hw305 as production "as of 2026-08-26"; the founder's hw306 decision promotes production to hw306 and updating that table is part of the promotion | RUNBOOKS §0.1 maintenance rule | PR updating RUNBOOKS §0.1 + PROTOCOL §5.0 lands with the FIRED line |
| R3 | **Debug-before-wipe / walk-everything-before-wipe** | RUNBOOKS §0.6, `feedback_walk_everything_before_any_wipe` | hw305 is *converged*, not failed: its evidence is already banked (250 green stamped hw305/hw302/hw301) and carry-forward keeps it verbatim. Scheduler `--due --env hw305` = 177 decay-due rows, all re-walkable on hw306; re-walking them on a dying env is the treadmill the CI gate refuses. **Decision: bank nothing new on hw305; fetch its cloud-init log + cutover status ConfigMap into `docs/sessions/2026-09-02/hw305-forensics/` before the wipe.** |
| R4 | **Max 2 fires/day**; never fire for one passenger | RUNBOOKS §0.4 | this is fire 1 of 2 today |
| R5 | **No catalyst-api / catalyst-ui merges while the prov is in flight** — the tofu apply runs inside the mothership catalyst-api Pod (`strategy=Recreate`); a roll abandons the apply | `feedback_never_merge_catalyst_api_prs_right_before_firing_a_prov`, `reference_deploybot_catalyst_api_roll_mid_fire_abandons_prov` | pre-fire: `kubectl -n catalyst rollout status deploy/catalyst-api` settled + zero in-progress build runs; merge freeze on `products/catalyst/bootstrap/**` and `core/**` from FIRE until `ready` (chart-only PRs are safe) |
| R6 | **NodePorts absolutely forbidden**, including for tests | founder 2026-07-03 | `scripts/check-no-nodeports.sh` on every PR; `scripts/check-live-nodeports.sh` against hw306 once its kubeconfig lands |
| R7 | **Carry-forward UAT runs AFTER `ready`**, never before the fire | RUNBOOKS §0.3 step 2, `uat-drift-guard.py` | `reset-uat.py hw306` only after the record holds `ready` |
| R8 | **Canonical endpoints only** for lifecycle: `POST /sovereign/api/v1/deployments`, `POST …/{id}/wipe` (body `{}`) | `feedback_canonical_wipe_endpoints` | the lifecycle helper gates on the response body, never on the echo (502 roll windows) |
| R9 | **Agent reports are claims** — live state is re-queried by the orchestrator before anything is called done | PRINCIPLES Part III | every workflow output in this file carries the command that re-verified it |
| R10 | **Walkers are read-only and each drives its own headless browser**; the Playwright MCP browser is one shared session and must not be fanned out | `reference_parallel_uat_walkers_own_browser`, `feedback_parallel_playwright_agents_share_one_browser` | walker briefs forbid live writes and MCP browser use |
| R11 | **File-editing agents run in worktree isolation**; sub-agent ceiling on this host is **6 concurrent** (8 CPUs − 2), queueing beyond that | `feedback_always_dispatch_agents_with_worktree_isolation` | workflow `isolation:'worktree'` for fix lanes |
| R12 | **Never `AskUserQuestion`**; auto-pick the highest-ICE option and file the rest as backlog | CLAUDE.md autonomy mandate | — |
| R13 | `Refs #N` in PR bodies, never `Closes #N`; issues close only after the operator-walk evidence lands | CLAUDE.md | — |
| R14 | Banned terms (GLOSSARY): Organization, sovereign-admin, Blueprint, Environment, Application | docs/GLOSSARY.md | — |
| R15 | **A denied tool call is one command, never a blocked goal**; no security-shaped blockers on our own pre-live infra | founder 2026-08-06 | — |

---

## 2. Live state at plan time (all re-verified by the orchestrator, 2026-09-02T06:40–07:10Z)

| Surface | Measured | Command |
|---|---|---|
| Mothership deployments | exactly one: `b2b00ce4c833badf` `ready` `hw305.omantel.biz`, started 2026-08-23T10:03:17Z, phase-1 `ready` 11:48:07Z (**105 min**) | `GET /sovereign/api/v1/deployments` with owner cookie |
| hw305 regions | `me-east-215-a` primary hrReady 65/65; `me-east-215-b-1` secondary **58/66, degraded** | same record, `regions[]` |
| hw305 region-B non-ready HRs | bp-external-secrets (helm rollback to v8), bp-mimir (rollback to v7), bp-external-secrets-stores / bp-harbor / bp-oidc-gate / bp-grafana (dependency chain) | `kubectl --kubeconfig ~/.kube/sovereigns/hw305-b.yaml get hr -A` |
| hw305 cutover | started 2026-08-23T11:48:29Z, `cutoverComplete=false`, `progressPercent=45`; steps 01–05 `success` (2026-09-01 10:03–11:53Z, harbor-prewarm 106 min); **step 06 `helmrepository-patches` FATAL loop every ~16 min** | `kubectl -n catalyst get cm self-sovereign-cutover-status` (region a) |
| step-06 root cause on hw305 | region-B pivot writes 65 HRs OK, then the poked region-B Kustomizations re-apply and restore **63 HRs to `oci://ghcr.io/openova-io`**; the git-side rewrite never landed: `Phase-2.5 WARN: sed rewrite of existing block failed` → `git diff empty after sed — nothing to commit`; at pivot time region-B `GitRepository flux-system/openova` was `Ready=False … authentication required` against `gitea-http.gitea.svc:3000` (now Ready=True). Live now: region-B 59 HRs on ghcr, 6 on `registry.hw305.omantel.biz` | `kubectl logs cutover-helmrepository-patches-1788331396-kl9mx -c helmrepository-patches` |
| Mothership catalyst-api | image `32ef8cc` (= commit `32ef8cc94`, 2026-08-21), `Recreate`, 1 replica, rollout settled; `CATALYST_HUAWEI_ACCESS_KEY/SECRET_KEY/PROJECT_ID/REGION` present (server-side stamping) | `kubectl -n catalyst get deploy catalyst-api -o json` |
| Fire path parity | `infra/providers/**` and `bootstrap/api/internal/{provisioner,providers}` **unchanged** between `32ef8cc94` and main (only handler/console files changed) → the running mothership fires exactly main's tofu module + cloud-init; **no mothership roll needed before the fire** | `git log 32ef8cc94..origin/main -- infra/providers/ …/internal/provisioner/ …/internal/providers/` |
| Mothership Flux | `catalyst-platform`, `apps`, `flux-system`, `openova-harbor` Kustomizations **suspended** → the deploy-bot cannot roll the mothership mid-fire by itself; R5 still applies to manual rolls | `kubectl get kustomization -A` |
| Mothership health | node `vmi3116389` Ready, DiskPressure=False; stalwart + sogo Running; Harbor `/api/v2.0/health` all components healthy; unrelated `iogrid/*` ImagePullBackOff (20h) | kubectl / curl |
| Chart pins vs ghcr | `bp-catalyst-platform 1.4.1623` ✅, `bp-kyverno 1.3.9` ✅, `bp-gitea 1.2.50` ✅, `bp-chargeback 0.1.2` ✅ (70 slot files; full audit in §4) ; Blueprint Release green since 2026-09-01T17:08Z | `gh api /orgs/openova-io/packages/container/<pkg>/versions` |
| DNS | `omani.works`, `omantel.biz`, `omani.homes`, `omani.rest` all delegated to `ns1-3.openova.io` (mothership PowerDNS, 45.151.123.50) → no registrar change on fire; `hw306.*` resolves nowhere yet | `dig @1.1.1.1 NS <zone>` |
| LE rotation | hw302 used `omani.works` 2026-08-20; hw305 `omantel.biz` 2026-08-23 → both windows clear; **hw306 → `omani.works`** (rotation per RUNBOOKS §0.3) | — |
| CI on main | last 15 runs green; 0 in-progress runs | `gh api actions/runs` |
| UAT ledger (main) | 286 rows: ✅250 ❌12 ⚠️7 ⏳17 (header hw305, walked from 2026-08-23) | `docs/ledger/UAT.md` |
| Anthropic input | mothership `catalyst/sovereign-anthropic-credentials` created 2026-08-13T23:53Z (memory: expired 2026-08-14, refresh spent); hw305 copy created 2026-08-23 → **validity by USE pending (§4)** | kubectl metadata only |
| Local checkout | `work/hw306` == `origin/main bb2c17e36`; 59 orphan hw305 screenshots from un-stamped walks parked in the session scratchpad; 3 dirty worktrees carry in-flight fixes (cutover-6645 secondary-mirror PAT; 5358 guacamole SSO gate; m2 region-b newapi admin-token) | `git worktree list` |

---

## 3. Passengers — the fixes on main since hw305 (RT-1..RT-5)

Grouped by pillar; each maps to the UAT rows it should flip on hw306. *(v1 = orchestrator's read of `git log --since=2026-08-23`; the delta-classifier report replaces this table with the full list.)*

| Pillar | Passenger (sha · issue) | Reaches hw306 via | Rows to walk |
|---|---|---|---|
| 5 Sovereignty | `5501fed08` step-01 secondary mirror on the shared PAT + batched push (#6754); `6dce604d5` region-local PAT mint (#6645); `587c2440e` activeDeadline 2700s (#6511); `464e9bba3` step-03 secondary-region Harbor prewarm (#6764) | `bp-self-sovereign-cutover` pin (version + ghcr presence verified in §4) | G11, 166, 165, 159, 227 |
| 3 DR / region-B | `79d1d213b` consumer `-mesh/-mesh-rw` aliases follow a DR promotion (#6753); `81df2e36b` singleton shared-pg admits region-B keycloak over ClusterMesh (#6627); `ca1546627` chart bumps upgrade-clean for mimir/vpa/external-secrets (#6760); `35ca70338` seaweedfs maxVolumes (#6766) | chart pins | 235, R21, 14, 189, G12 |
| 1 Marketplace / funnel | chargeback Blueprint + slot 13f + DNS subdomain (#6723 lanes A–D: `92faa4ea5`, `346da5a99`, `21ca53641`, `3de212baa`); `20f094538` repin bitnamilegacy/kubectl → alpine/k8s (#6755, 16 charts); newapi `a475a5578`/`7d8e023b5` (TBD-A6) + `:16` CNPG repin (#6759) | chart pins + catalog seed | 89, 93, 225, 3, 8, 91, 94 |
| 4 Agenity / MCP | `fbe899159` agenity sidebar via consoleUI 0.5.33; `f2105df68` per-Sovereign sidebar overrides store; `6a700f5ca` Org-scoped sidebar; (#6365 validate-credential-by-use and #6322 renew-before-expiry are **open PRs**) | `bp-catalyst-platform 1.4.1623` + `bp-agenity` pin | G8, G9, 219–223 **(INPUT-gated on the Anthropic credential)** |
| Operator console | `7d25c2908` jobs served from the durable store first (#6749); `44005b070` suspended HRs render Dormant (#6767); `0bc54baf5` k8scache watches CronJob/Job (#6705) | `bp-catalyst-platform 1.4.1623` | 161–174, 193–197, 212, 213 |

---

## 4. MUST-FIX-BEFORE-FIRE (blocks_fire) and SHOULD-FIX-DURING-PROV

*(v1 — populated from the orchestrator's live reads; the pin-auditor, cutover-2region-analyst and agentic-input-checker reports finalise it.)*

| # | Item | blocks_fire | Why | Smallest fix |
|---|---|---|---|---|
| F1 | **Step-06 region-B pivot durability** (#5359/#5596): the git-side bootstrap-kit rewrite in the local Gitea does not land (`sed rewrite of existing block failed`), so region-B's Flux re-apply restores ghcr URLs | **yes, for the cutover claim** (not for Phase-1) | hw306 will loop at step 06 exactly like hw305 unless the pinned cutover chart carries the fix; a mid-cutover env cannot take it later | fix the sed/rewrite path in `platform/self-sovereign-cutover/chart` step-06 script against the current bootstrap-kit YAML shape, bump chart, publish, bump the kit pin — analyst report names file:line |
| F2 | Every bootstrap-kit + catalog-seed pin must exist on ghcr (step-03 prewarm FATALs on a real unpublished chart) | yes | #6004 payload gate froze ghcr until 2026-09-01 | `gh workflow run blueprint-release.yaml -f blueprint=<name>` per missing pin |
| F3 | Region-B image path: secondary region must not run an empty Harbor (#6764 merged `464e9bba3`) — verify the pinned cutover chart version includes it | yes for cutover | — | pin check |
| F4 | Anthropic credential validity by USE | no (INPUT-gated rows only) | expired 2026-08-14 per memory | founder re-issues `credentialsJson`; the platform seeds it on fire or rotates it in live |
| F5 | Protect-list promotion PR (R2) + this manifest | no | protocol | docs PR with the FIRED line |

---

## 5. The fire — exact body and the pre-flight gate

**Body** (`provisioner.Request`, camelCase; derived field-for-field from hw305's `tofu.auto.tfvars.json`, changes: TLD → `omani.works`, FQDN → `hw306.omani.works`, bucket → `catalyst-hw306-omani-works`; Huawei AK/SK are server-stamped from the mothership env; `sshPublicKey` = the operator key from hw305's tfvars, 104 bytes):

```json
{"orgName":"Omantel","orgEmail":"emrah.baysal@openova.io","provider":"huawei",
 "sovereignFQDN":"hw306.omani.works","sovereignDomainMode":"byo","sovereignPoolDomain":"","sovereignSubdomain":"",
 "parentDomains":[{"name":"omani.works","role":"primary","registrarKind":"dynadot"},{"name":"omani.homes","role":"org-pool"},{"name":"omani.rest","role":"org-pool"}],
 "region":"me-east-215-a","controlPlaneSize":"m7n.xlarge.8","workerSize":"m7n.2xlarge.8","workerCount":5,"storageClass":"evs-ssd",
 "haEnabled":false,"bcpTopology":"active-hotstandby","marketplaceEnabled":true,"consoleIsolationEnabled":true,
 "fireCutoverOnHandover":false,"qaTestEnabled":false,"openovaFlowEnabled":true,
 "regions":[{"provider":"huawei","cloudRegion":"me-east-215-a","controlPlaneSize":"m7n.xlarge.8","workerSize":"m7n.2xlarge.8","workerCount":5,"storageClass":"evs-ssd"},
            {"provider":"huawei","cloudRegion":"me-east-215-b","controlPlaneSize":"m7n.xlarge.8","workerSize":"m7n.2xlarge.8","workerCount":5,"storageClass":"evs-ssd"}],
 "sshPublicKey":"<ssh-ed25519 … from hw305 tfvars>"}
```

**Gate table** (all must pass, in order; the lifecycle helper in the session scratchpad wraps each call and gates on the HTTP body):

| Gate | Command | Pass criterion |
|---|---|---|
| G0 must-fix list §4 blocks_fire items landed | per item | merged + published + pin bumped, re-queried on ghcr |
| G1 hw305 forensics captured | `GET …/deployments/b2b00ce4c833badf/cloudinit-log`; cutover-status CM; region-B HR list | files under `docs/sessions/2026-09-02/hw305-forensics/` |
| G2 hw305 wiped | `POST …/deployments/b2b00ce4c833badf/wipe -d '{}'` → poll `GET` | record `status=wiped` |
| G3 zero-remnant in the cloud | Huawei GET inventory diff vs the §2 baseline (ECS/VPC/EIP/EVS pvc-*/ELB/NAT) | only the bastion remains; CSI-EVS + CCM-ELB orphans purged (memory `reference_wipe_tofu_destroy_leaks_runtime_csi_evs_drain_before_destroy`) |
| G4 quota headroom after the ~15-min release lag | VPC ≥2 free of 5, EVS ≥100 free of 400, EIP headroom | measured, not assumed |
| G5 mothership stable | `kubectl -n catalyst rollout status deploy/catalyst-api`; `gh api actions/runs?status=in_progress` | settled >5 min; zero in-progress builds |
| G6 preflight | `BEARER=<owner JWT> HW_TFVARS=<hw305 tfvars> scripts/prov-preflight.sh hw306.omani.works catalyst-hw306-omani-works false true` | exit 0; **PRE-FLIGHT PASS vpc=X/5 evs=Y/400 eip=Z** appended to §9 |
| G7 fire | `POST …/deployments @body` | HTTP 201, `{id,status:"provisioning"}` recorded in §9 |

---

## 6. Phase-1 watch-list (timeline: hw305 105 min, hw300 51 min; secondary lags and reads `degraded:true` until it catches up — not stuck)

*(v1 from memory; the freshprov-trap-historian report replaces this with symptom → detection → fix-status-on-main.)* Watch `GET /deployments/{id}` every 2 min: `status` (`provisioning` → `phase1-watching` → `ready`), `phase1Substate`, `regions[].hrReady/hrTotal/degraded`, `error`. Known traps: tofu-init GitHub 504 (mothership-network-specific — curl from bastion AND mothership before blaming GitHub); cloud-init 0-hour wedge (GitHub IPv6 / CoreDNS); huawei-evs-csi missing; region bootstrap failure = registry routing not quota; cyclic re-bootstrap on the same VMs (#6485, hw300); handover auto-fires then cutover 425 race; catalyst-api restart abandons the apply (R5). **On any failure: fetch the cloud-init log FIRST, never unwedge by hand, ship the PR, and only then wipe + re-fire a NEW number (fire 2 of 2).**

---

## 7. Post-ready walk plan

1. `python3 scripts/reset-uat.py hw306` (carry-forward) → `python3 scripts/uat-confidence.py --due --env hw306` = the work-list (expected ~180 due, ~100 skipped at high confidence — never re-walk all 286).
2. Stage kubeconfigs from `/var/lib/catalyst/kubeconfigs/<id>*.yaml` on the mothership PVC to `~/.kube/sovereigns/hw306-{a,b}.yaml`; run `scripts/check-live-nodeports.sh`.
3. Walk auth: mothership-signed owner handover token → `302 /dashboard` when the injected verify-pubkey fingerprint matches the mothership key (`reference_handover_url_walkable_when_injected_pubkey_matches_mothership`); PIN mail is in the `OTP` IMAP folder, never INBOX.
4. Partition of the 36 non-green rows (final per-row procedures from the ledger-partitioner report): **cutover-gated** G11 166 165 159 227; **agentic INPUT-gated** G8 G9 219 220 221 222 223; **region-B / DR** 235 R21 14 189; **funnel / per-Org** 89 93 225 3 8 91 94 16 232 241; **operator console** 212 213 W2 52 56 60 62 109 133 234 242 67 G3.
5. Walkers: up to 6 concurrent read-only agents, each with its own headless Chromium (helper drafted by the walk-tooling report), curl/kubectl rows first, browser rows second; every stamp updates the three cells (status, env column, evidence) + appends `uat-observations.csv`; guards before push: `uat-drift-guard.py`, `check-walk-respects-scheduler.sh`, `check-uat-fix-sha-citations.py`, `uat-tally.py`.

---

## 8. Parallel lanes for the next 3–4 hours

| Lane | Owner | Work | Depends on |
|---|---|---|---|
| A — pre-fire fixes | fix agents (worktree-isolated, chart/docs only) + orchestrator merges | §4 blocks_fire items: F1 step-06 durability, F2 publishes, F3 pin check | analyst + pin reports |
| B — lifecycle | orchestrator only (never delegated) | G1 forensics → G2 wipe → G3/G4 inventory + quota → G5/G6 preflight → G7 fire → §6 watch → `ready` | lane A blocks_fire done |
| C — non-prov fixes | fix agents (worktree) for NO-FIX-YET rows that touch charts/scripts/docs only (no `core/**`, no `bootstrap/api/**` while the prov is in flight) | partitioner report |
| D — walkers | up to 6 read-only walker agents after `ready` | §7 | lane B `ready` |
| E — heartbeat | cron `13 */6 * * *` (session job `c70240c3`) | 6-hourly convergence report, delivery separated from reclassification | — |

---

## 9. Evidence appendix (appended at execution — commands, HTTP codes, timestamps)

- *(PRE-FLIGHT PASS line, FIRED line, ready line, protect-list promotion PR, cutover step lines, walk PRs go here.)*

### 9.1 Pre-wipe execution evidence (2026-09-02, UTC)

- **LANE-B-EXTRACTED: 05f983d8b** — last origin/main commit touching `docs/ledger/UAT.md`; hw305 evidence is on main (250 ✅ carried verbatim under the carry-forward policy). Scheduler `--due --env hw305` = 177 decay-due rows + 6 explicit (166 219 220 221 222 G11, none walkable on hw305) → nothing new banked; forensics committed under `hw305-forensics/` (cloud-init log 433 lines, record, cutover CM, step-06 FATAL run, region A/B HR + Flux + node state).
- **Report-driven corrections to v1** (ten read-only readers, all verified by the orchestrator before acting):
  - **Mothership roll WAS required** (v1 §2 said not): `prov-preflight.sh` check 4b fail-closes on deployed≠pinned (`scripts/prov-preflight.sh:260-283`), and `handler/sovereign_dns_records.go` on main adds `chargeback` to the parent-zone A-record allowlist (absent from 32ef8cc → hw306 would have fired without `chargeback.<fqdn>`). **Rolled by hand 07:26:04Z–07:26:56Z** (`kubectl -n catalyst set image deploy/catalyst-api …:b27d54a` + `deploy/catalyst-ui …:b27d54a`; Flux `catalyst-platform` Kustomization stays suspended); post-roll `rollout status` settled, pod `catalyst-api-74d776bb94-dc2r6` 1/1, owner `whoami` 200, `GET /deployments` lists hw305 `ready`, zero in-flight operations at roll time. Fire not before 07:32Z (5-min settle).
  - **Cutover step 06 is structurally FAIL-EXPECTED on a 2-region hw306 with chart 0.1.201** (analyst + delta lens, both citing `06-helmrepository-patches-job.yaml` `git push origin main` → region-A Gitea only, while region-B Flux consumes region-B's own Gitea mirror of GitHub that never receives the rewrite; #5359/#5596). Fixer dispatched (worktree) for **bp-self-sovereign-cutover 0.1.202** — durable in-region rewrite for every secondary region + Phase-2.5 sed audit. **Decision: fire with `fireCutoverOnHandover:false`** (product default #4061; also keeps row 159's pre-cutover state observable and the GitHub-delivery rows walkable); the 0.1.202 pin lands on hw306 pre-cutover (the kit follows main until step-05 pivots the source); the cutover is then started from the sovereign-admin CTA / `POST /api/v1/sovereign/cutover/start`, with the runner-SA trigger (RUNBOOKS §0.5) as fallback.
  - **Pins: zero publishes needed** — 68/68 bootstrap-kit pins, 76/76 resolvable catalog-seed pins, umbrella 1.4.1623, cutover 0.1.201 and all 12 umbrella image tags present on ghcr; `check-bootstrap-kit-pin-sync.sh --check-ghcr` 68/68; `check-train-coherent.sh --ref origin/main` COHERENT 4/4; `check-no-nodeports.sh` 0 across 72 charts; payload gate: umbrella 88.77 %, cutover 85.60 % of the #6004 ceiling (one prose bump from the 90 % gate — **no umbrella bumps in the fire window**).
  - **`uat-confidence.py --observe` cannot parse the HTML ledger** (`ledger_envs()`/`parse_ledger()` match legacy pipe rows only → "no row attributable to hw305"); without hw305 observations the scheduler will not mark the hw305 reds due on hw306 and the walk guard would refuse their flips. Fixer dispatched (worktree, scripts-only; records the hw305 verdicts in the same PR).
  - **newapi funnel-pin drift**: `core/services/provisioning/gitops/helmrelease_apps.go:730` newapi=1.4.154 vs catalog-seed 1.4.155; `TestDefaultHRAppPins_MatchCatalogSeed` fails on main. Fix is one line but rebuilds org-services → umbrella bump → **merge only after hw306 `ready` and before the cutover starts** (freeze rule).
  - **Anthropic credential is EXPIRED by use** (hw305 agenity init container CrashLoopBackOff ×2704, `#6163 OAuth token EXPIRED`); mothership secret dated 2026-08-13. hw306 seeds the same blob. Rows G8 G9 219 220 221 222 stay INPUT-gated until the founder re-issues `credentialsJson`; the per-Sovereign rotation path after ready is `kubectl -n catalyst-system create secret generic sovereign-anthropic-credentials …` → seed reconciler ≤10 min → ExternalSecret → agenity pod restart (no mothership roll).
- **Pre-wipe Huawei baseline** (GET-only, 07:14:48Z, project `f27698137bdc4b00ad509cf27f1e5547`): quotas **vpc 3/5, publicIp 10/10 (FULL)**, subnet 5/100, loadbalancer 2/50; ECS 13 (12 hw305 + bastion), VPC 3 (hw305 a/b + bastion), EIP 10 = 6 hw305 in service + bastion + **3 ORPHAN DOWN `catalyst-b2b00ce4-nat-preflight-{1,2,3}` = 212.72.24.25 / 5.37.79.43 / 212.72.24.35** (created at hw305 apply), NAT 2, ELB 2, EVS 112 (12 IaC system disks + 96 CSI pvc-* attached + 2 bastion + **2 detached hw305-era pvc-***), OBS 49 (1 hw305 + 48 archive buckets from hw139…hw270), 2 vpc-less orphan subnets/networks + 1 orphan SG + 2 orphan keypairs from kv030704/kv184704/hw284. **Expected post-wipe: ECS 1, VPC 1, EIP 1 (212.72.24.20 only), NAT 0, ELB 0, EVS 2, OBS 48; quotas vpc 1/5, publicIp 1/10.** If the 3 DOWN EIPs survive the wipe they are deleted by id (never an ACTIVE/ELB EIP, never the bastion).
- **Merge freeze declared**: from the wipe POST until `cutoverComplete=true` on hw306, no merges touching `core/**`, `products/catalyst/bootstrap/**`, `products/catalyst/chart/**`, `infra/providers/**` (catalyst-api/ui re-tag + umbrella bump), and no bootstrap-kit pin bumps after cutover step-01 freezes the mirror. Chart-only fixes that must reach hw306 (0.1.202) merge **after `ready`, before the cutover start**. Open PRs on the hold list: #6758 #6689 #6480 #6474 #6473 #6470 #6365 #6322 #6320 #6178 #6176 #6174 #6168.


---

## 10. DoD — what "best environment so far" means, in numbers

| Criterion | Target | Measured by |
|---|---|---|
| Phase-1 | `ready` with both regions `hrReady == hrTotal`, `degraded:false` on the secondary within 60 min of primary ready | deployment record |
| Sovereignty | `cutoverComplete=true` with step-08 deny-egress proof, region-B HelmRepositories 0 on ghcr after two re-applies | cutover-status CM + `kubectl get helmrepository -A` on region-B |
| Ledger | ≥ 270 green of 286 on hw306 stamps (hw305 peak was 270 on 2026-08-24; main is 250 today), 0 rows lost green without a named regression | `uat-tally.py` + observations CSV |
| Agentic | G8/G9/219–223 green **if** the credential is valid; else recorded INPUT-gated with the founder action named | agenity init container exit 0 + MCP tool call |
| Hygiene | 0 NodePorts live; 0 orphan Huawei resources after the hw305 wipe; no live patch without a merged PR | `check-live-nodeports.sh`, inventory diff, PR list |
| Process | 1 fire (≤2), zero `AskUserQuestion`, every agent claim re-verified, every stamp guard-green before push | this file |

## 11. Risks and the decision each forces

| Risk | Decision |
|---|---|
| F1 fix needs more than one cutover-chart cycle | fire Phase-1 with `fireCutoverOnHandover:true` anyway only if F1 is published; otherwise fire with it **false** and trigger the cutover via the runner-SA endpoint once the fixed chart pin is live (RUNBOOKS §0.5) |
| Wipe leaves CSI EVS / CCM ELB orphans | G3 diff decides; purge through the wipe handler's own path, never raw deletes of anything not tagged hw305 |
| Quota lag > 15 min | poll until headroom; never fire on assumed headroom |
| Phase-1 fails | cloud-init log first; fix as PR; second fire only if the fix is delivered; that is the last fire today |
| Anthropic credential expired | walk everything else; report the agentic rows INPUT-gated with the exact secret/field |
