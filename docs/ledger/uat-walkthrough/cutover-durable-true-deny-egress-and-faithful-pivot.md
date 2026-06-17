# #3379 — Cutover PROOF integrity: durable fact · true deny-egress · faithful pivot · audit fidelity · host-loop + zero residual

## Status — last validated: hw158 (2026-06-17T08:54Z, RE-VALIDATION #2 — keycloak-cascade CLEARED + real resume observed; NEW catalyst-platform 1.4.665 roll-regression holds bootstrap-kit) — ⏳ KEYCLOAK BLOCKER CLOSED, cutover end-state handover-gated

- **Verdict: ⏳ The keycloak-1.4.30 cascade is CLEARED on hw158 and the cutover engine even RE-FIRED a real resume this session — but the cutover END-STATE is still correctly handover-gated, and a SEPARATE, NEW `bp-catalyst-platform@1.4.665` post-upgrade-hook regression is now (transiently) holding `bootstrap-kit` / `bp-continuum` / `bp-sandbox` non-Ready (NOT the keycloak cascade, NOT in #3379 scope).** `bp-keycloak:1.4.30` is **PUBLISHED + mirror-able on ghcr**: the in-cluster `ghcr-pull` robot `HEAD ghcr.io/v2/openova-io/bp-keycloak/manifests/1.4.30` → **`HTTP 200`** (parity with `1.4.29` → `200`), Flux source-controller `pulled 'bp-keycloak' chart with version '1.4.30'` (`Artifact=1.4.30`, `Ready=True`), and the `bp-keycloak` HR is **`Ready=True`** (`Helm upgrade succeeded … chart bp-keycloak@1.4.30`; `1.4.30 deployed 2026-06-17T07:29:39Z`).
- **Keycloak-cascade spine: ALL 8 Ready=True.** `bp-keycloak / bp-gitea / bp-grafana / bp-guacamole / bp-sso-bridge / bp-oidc-gate / bp-powerdns-admin / bp-newapi` are **all `Ready=True`** (each verified `{.status.conditions[Ready].status}=True` this session). The earlier `manifest unknown` blocker is **CLOSED**.
- **🆕 NEW (distinct) blocker — `bp-catalyst-platform@1.4.665` post-upgrade hook crashloops.** A deploy-bot bump to `1.4.665` (landed after the keycloak recovery) fails its post-upgrade hook: `job catalyst-gitea-flux-auth-sync failed: BackoffLimitExceeded` — the pod logs `FATAL: catalyst-system/catalyst-gitea-token.token empty (mint hook did not populate the PAT)` (a Gitea-PAT mint/sync ordering race in 1.4.665). The HR cycles `upgrade 1.4.665 → post-hook fail → RollbackSucceeded to 1.4.664 → re-attempt` (history: `1.4.664 deployed 08:44:16Z`, `1.4.665 failed 08:28:07Z`), so `bp-catalyst-platform` reads `Ready=Unknown :: Progressing`. Downstream: `bootstrap-kit` kustomization is `Ready=Unknown :: Progressing` (health-check times out waiting on `bp-catalyst-platform/bp-continuum/bp-sandbox InProgress`); `bp-continuum` + `bp-sandbox` are `Ready=False :: DependencyNotReady` on catalyst-platform; `sovereign-tls` is `Ready=False :: DependencyNotReady` on bootstrap-kit. **This is a NEW catalyst-platform-roll regression, orthogonal to the keycloak cascade and to the cutover (#3379).** Effective HR tally this session: **58/65 Ready** (3 of the 7 non-Ready = the `spec.suspend=true` Hetzner charts `bp-cluster-autoscaler-hcloud / bp-hcloud-ccm / bp-velero`; the other 4 = the 1.4.665 cascade + a tenant `vcluster` blocked by a kyverno harbor-proxy-pull policy on `walk-stranger-co`).
- **🆕 The cutover engine DID re-fire a REAL resume this session (proves #3681 + #4.1 live).** On the `08:46` catalyst-api restart the logs show `cutover-resume: in-flight cutover detected on startup` → `cutover-resume: spawning runCutover to resume interrupted cutover` (totalSteps 11). It then failed fast: `cutover: step failed step=harbor-prewarm err="Job catalyst/cutover-harbor-prewarm-1781677444 reported Failed condition (carried over from prior cutover attempt)"`. The resume **re-used the stale exhausted prewarm Job** (created `06:27:26Z`, `BackoffLimitExceeded` at `06:55:56Z` — BEFORE the `07:29` 1.4.30 publish; **NO new cutover Job was created post-1.4.30**), so it could not exercise the now-mirror-able chart. Crucially, the resume advanced `cutoverLastAttemptStartedAt` to **`2026-06-17T08:46:14Z`** while leaving `cutoverStartedAt` at the true T0 **`2026-06-17T06:24:04Z`** — the **#3681 audit-fidelity fix is now proven with an actual resume**, not just a structurally-wired field.
- **Cutover END-STATE — still correctly handover-gated, not chart-blocked.** `self-sovereign-cutover-status` CM: `cutoverComplete=false`, `failedStep=harbor-prewarm`, `currentStepIndex=2`, `progressPercent=18`, `registriesYamlActive=v1` (all 4 `node.*.registriesYaml=v1`). `secret/catalyst/tofu-phase0-archive` = **NotFound** (pre-handover → the in-cluster `POST /api/v1/internal/cutover/trigger` returns `425 Too Early`), `secret/catalyst/cutover-complete` = **NotFound**, `ccnp/cutover-egress-block` = **NotFound**, `ghcr-pull` secret still keys `["ghcr.io"]`, **149 live mothership-tethered images** in-cluster. A CLEAN re-fire (one that actually mirrors 1.4.30 via a fresh prewarm Job) needs the **post-handover** path (`ReceiveTofuArchive` seals the archive → engine spawns a fresh run) — the restart-resume alone wedges on the carried-over failed Job.
- **🆕 Improvement: `sme-tenants` kustomization flipped `False → Ready=True`** (`ReconciliationSucceeded`, `Applied sme-tenants@sha1:0046e246…`) — was `False :: Source artifact not found` in the prior re-walk; the tenant source now reconciles (row 6.5 precondition improved).
- **Handover front-door is HEALTHY:** `https://api.hw158.omani.works/healthz` → `HTTP 200`; `https://console.hw158.omani.works/` → `HTTP 200`; operator handover sessions establishing (`auth_handover: operator session established email=emrah.baysal@openova.io`).
- **Tally: 9 ✅ · 16 ❌ · 15 N/A · 4 ⏳.** Newly ✅ this session: **`4.1`** (resume re-enters mid-run — observed live in catalyst-api logs). **`0.6` REVERTED to ❌** vs the prior re-walk: `bootstrap-kit` is NOT currently `Ready=True` — it is `Unknown/Progressing` because of the NEW 1.4.665 catalyst-platform post-hook crashloop (honest live re-check, not carried). `5.1`/`5.2` stay ❌ (bootstrap-kit not at a Ready post-cutover rev). Cutover-END-STATE rows stay ❌ (handover-gated): `0.2`, durable-seal (Sec 1), registry-pivot (Sec 2), egress-block (Sec 3), end-to-end (Sec 6).
- **Maps to:** [`../UAT.md`](../UAT.md) — keycloak-1.4.30 chart recovered + spine-8 green + real resume proven; cutover end-state pending post-handover re-fire; NEW 1.4.665 catalyst-platform roll-regression noted (separate ticket).
- **Evidence:** inline `kubectl` + authenticated-ghcr-HEAD + catalyst-api-log captures below (this session, hw158, kubeconfig `/tmp/hw158-kc.yaml`, T `2026-06-17T08:54Z`).
- **What's needed to reach `cutoverComplete=true`:** (a) the NEW `1.4.665` `catalyst-gitea-flux-auth-sync` PAT-mint race must clear (so the spine fully re-settles), AND (b) complete handover (seal `secret/catalyst/tofu-phase0-archive` via `ReceiveTofuArchive`) → engine auto-re-fires with a FRESH prewarm Job → step-02 now passes (1.4.30 mirror-able, HEAD 200) → walk Sections 1–6 to green. **No code/chart fix remains for the keycloak blocker; it is closed.**
- **Index:** [`README.md`](README.md). Prior-env (hw150-train) evidence is void per the each-new-env-flushes-evidence rule.

**Issue:** #3379 · **Slug:** `cutover-durable-true-deny-egress-and-faithful-pivot` · **Pillar:** 5 (Sovereign independence) · ADR-0002 · Principle #11 · DoD §7
**Env under test:** hw158 (dep `ab2135d4cf2d01e4`), kubeconfig `/tmp/hw158-kc.yaml`; cutover chart `bp-self-sovereign-cutover@0.1.75` (live).

> **Scope:** this walkthrough proves the INTEGRITY, DURABILITY, and BREADTH of the cutover sovereignty
> proof — NOT merely that the 11 steps run. Every row is one operator action with an OBSERVED column.
>
> **What changed (the five faces):**
> 1. **#3667 durable fact** — `cutoverComplete=true` is sealed in OpenBao (`secret/catalyst/cutover-complete`),
>    a chart upgrade can no longer revert the flag or re-fire the 600s hold (status ConfigMap is init-only).
> 2. **#3671 faithful pivot** — the engine flips `registriesYamlActive=v2` after `harbor-prewarm`; the
>    registry-pivot DaemonSet writes a per-node v2 ack which step-04 COUNTS; the run REFUSES
>    `cutoverComplete=true` while the key is still `v1`.
> 3. **#3678 true deny-egress** — the 600s-hold CCNP is a TRUE default-deny (`enableDefaultDeny.egress:true`
>    + intra-cluster allow-list) instead of a subtractive 4-CIDR block; an active call-home assertion FAILS
>    the proof if `console.openova.io` (or any mothership host) is reachable from a pod under the hold.
> 4. **#3681 audit fidelity** — `cutoverStartedAt` is written ONCE (true first run); resume/re-fire advance a
>    separate `cutoverLastAttemptStartedAt`.
> 5. **#3695 host loop + zero residual** — step-10's sso-bridge override MERGES into the existing
>    `values:` (no duplicate-key BuildFailed); step-08 HARD-gates on bootstrap-kit/sovereign-tls/sme-tenants
>    Ready and rolls EVERY external-registry workload under deny-egress.

---

## LIVE STATE SNAPSHOT (hw158, RE-VALIDATION #2 session, T `2026-06-17T08:54Z`)

```
$ kubectl get hr -A --no-headers | awk '{c++; if($0 ~ /True/) r++} END {print r"/"c}'
  58/65 Ready
  Non-Ready (7): bp-cluster-autoscaler-hcloud / bp-hcloud-ccm / bp-velero  (spec.suspend=true Hetzner charts — not failures)
                 bp-catalyst-platform (Unknown/Progressing — NEW 1.4.665 post-hook crashloop)
                 bp-continuum + bp-sandbox (False/DependencyNotReady on catalyst-platform)
                 vcluster@walk-stranger-co (kyverno harbor-proxy-pull denied — tenant image-tag issue, unrelated)

$ # === KEYCLOAK CASCADE: CLEARED ===
$ kubectl get hr bp-keycloak -n flux-system
  Ready=True :: Helm upgrade succeeded … chart bp-keycloak@1.4.30   (1.4.30 deployed 2026-06-17T07:29:39Z)
$ kubectl get helmcharts … flux-system-bp-keycloak
  spec.version=1.4.30   Ready=True :: pulled 'bp-keycloak' chart with version '1.4.30'   Artifact=1.4.30
$ # authenticated ghcr HEAD via the in-cluster ghcr-pull robot (the same creds harbor-prewarm uses):
  HEAD ghcr.io/v2/openova-io/bp-keycloak/manifests/1.4.30  → HTTP 200   (1.4.29 → HTTP 200 for parity)
$ 8 cascade spine HRs each {Ready}=True: bp-keycloak / bp-gitea / bp-grafana / bp-guacamole /
                                         bp-sso-bridge / bp-oidc-gate / bp-powerdns-admin / bp-newapi

$ # === NEW BLOCKER (distinct from keycloak, distinct from #3379): bp-catalyst-platform 1.4.665 ===
$ kubectl get hr bp-catalyst-platform -n flux-system
  Ready=Unknown :: Progressing :: Running 'upgrade' action with timeout of 30m0s
  Released=False :: UpgradeFailed :: post-upgrade hooks failed: job catalyst-gitea-flux-auth-sync failed: BackoffLimitExceeded
  Remediated=True :: RollbackSucceeded :: rollback to catalyst-platform.v3 chart bp-catalyst-platform@1.4.664
  history: 1.4.664 deployed 08:44:16Z / 1.4.665 failed 08:28:07Z / 1.4.664 superseded 07:55:42Z
$ kubectl logs catalyst-gitea-flux-auth-sync-lrgq6 -n catalyst-system
  FATAL: catalyst-system/catalyst-gitea-token.token empty (mint hook did not populate the PAT)   (CrashLoopBackOff x6)
$ kubectl get kustomization bootstrap-kit -n flux-system
  Ready=Unknown :: Progressing   (health-check timeout waiting on bp-catalyst-platform/bp-continuum/bp-sandbox InProgress)
  applied=main@sha1:d436ad4a…   attempted=main@sha1:ff63a8e8…   (NOT converged — reverted from the prior re-walk's Ready=True)
$ kubectl get kustomization sovereign-tls  → Ready=False :: DependencyNotReady (on bootstrap-kit)
$ kubectl get kustomization sme-tenants    → Ready=True  :: ReconciliationSucceeded :: Applied sme-tenants@sha1:0046e246…  (IMPROVED — was 'Source artifact not found')

$ # === CUTOVER ENGINE: a REAL resume fired this session, then wedged on the carried-over failed Job ===
$ kubectl logs catalyst-api-6995b95f6c-9h86n -n catalyst-system | grep cutover-resume
  08:46:13 cutover-resume: in-flight cutover detected on startup  currentStepIndex=2 failedStep=harbor-prewarm
  08:46:14 cutover-resume: spawning runCutover to resume interrupted cutover  totalSteps=11
  08:46:17 cutover: step failed  step=harbor-prewarm  err="Job …cutover-harbor-prewarm-1781677444 reported Failed condition (carried over from prior cutover attempt)"

$ kubectl get cm self-sovereign-cutover-status -n catalyst   (key fields — re-fired-but-wedged, end-state unreached)
  cutoverComplete:                "false"     failedStep: harbor-prewarm   currentStepIndex: "2"  progressPercent: "18"
  registriesYamlActive:           v1          node.<4 nodes>.registriesYaml: v1   (all 4 — pivot never ran)
  cutoverStartedAt:               2026-06-17T06:24:04Z       <-- true T0 PRESERVED (#3681)
  cutoverLastAttemptStartedAt:    2026-06-17T08:46:14Z       <-- ADVANCED on the 08:46 restart-resume (#3681 proven LIVE)
  step.gitea-mirror.result:       success    step.harbor-projects.result: success    step.harbor-prewarm.result: failed

$ kubectl get jobs -n catalyst | grep cutover    # NO cutover Job created after the 07:29 1.4.30 publish:
  cutover-gitea-mirror-1781677444    created 06:24:05Z
  cutover-harbor-projects-1781677444 created 06:27:11Z
  cutover-harbor-prewarm-1781677444  created 06:27:26Z  failed=4  Failed=True BackoffLimitExceeded @ 06:55:56Z

$ kubectl get secret tofu-phase0-archive -n catalyst   → NotFound   (pre-handover → /internal/cutover/trigger returns 425)
$ kubectl get secret cutover-complete -n catalyst      → NotFound
$ kubectl get ccnp cutover-egress-block                → NotFound
$ kubectl get secret ghcr-pull -n flux-system          → auth keys ["ghcr.io"]   (still mothership — pivot did not happen)
$ pod-image grep harbor.openova.io|ghcr.io/openova-io|github.com/openova  → 149 live mothership-tethered images
$ curl -sk https://api.hw158.omani.works/healthz       → HTTP 200      (handover healthy)
$ curl -sk https://console.hw158.omani.works/          → HTTP 200
$ kubectl get pods … catalyst-api  → catalyst-api-6995b95f6c-9h86n  age ~8m  (ROLLED at 08:46 → drove the resume above)
```

---

## Section 0 — Pre-state: reproduce the false proof, confirm the chart-recovery delta

The cutover never reached `cutoverComplete=true` on hw158 (it FAILED at step-02 on the then-missing `bp-keycloak:1.4.30`). Post-recovery, the **chart-blocked** sub-state is gone (0.6 flips), while the registry-pivot-pivoted-nothing + no-durable-seal symptoms still reproduce (correct: the cutover hasn't re-run).

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 0.1 | `export KUBECONFIG=/tmp/hw158-kc.yaml` | prompt ready | prompt ready | ✅ |
| 0.2 | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | `true` | **`false`** — cutover has NOT re-run (parked on the stale `failedStep=harbor-prewarm`); now handover-gated, not chart-blocked | ❌ (cutover not re-run) |
| 0.3 | `…jsonpath='{.data.registriesYamlActive}'` | `v1` until pivot (#3671) | **`v1`** — all 4 `node.*.registriesYaml=v1`; registry-pivot never ran | ✅ (reproduced) |
| 0.4 | `kubectl get secret cutover-complete -n catalyst` | `NotFound` until sealed (#3667) | **`NotFound`** | ✅ (reproduced) |
| 0.5 | `…{.data.cutoverStartedAt}` vs `…{.data.step\.gitea-mirror\.startedAt}` | EQUAL (no #3681 corruption) | **EQUAL** — both `2026-06-17T06:24:04Z`. The #3681 24-min corruption is GONE | ✅ (fix present) |
| 0.6 | `kubectl get kustomization -n flux-system bootstrap-kit` | `Ready=True` at post-cutover rev (#3695) | **`Ready=Unknown :: Progressing`** — health-check timeout waiting on `bp-catalyst-platform/bp-continuum/bp-sandbox InProgress`; `applied=main@sha1:d436ad4a…` ≠ `attempted=main@sha1:ff63a8e8…`. **REVERTED from the prior re-walk's `Ready=True`** — held non-Ready by the NEW `bp-catalyst-platform@1.4.665` `catalyst-gitea-flux-auth-sync` post-hook crashloop (`PAT.token empty`), NOT the keycloak cascade. No #3695 duplicate-`values:` BuildFailed (a different failure mode) | ❌ (NEW catalyst-platform 1.4.665 roll-regression, not keycloak) |
| 0.7 | `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | local Harbor post-cutover (#3695) | **`harbor.openova.io/proxy-dockerhub/alpine/k8s:1.30.0`** — live MOTHERSHIP tether (cutover never re-keyed; correct pre-cutover) | ✅ (reproduced) |
| 0.8 | `kubectl get ccnp cutover-egress-block` (if a hold is live) | default-deny-with-allow (#3678) | **`NotFound`** — no hold; cutover never reached step-08. Cannot inspect a non-existent CCNP | N/A (step-08 never reached) |

---

## Section 1 — DURABILITY: a `helm upgrade` cannot revert the flag or re-fire the cutover (#3667)

**Goal:** `cutoverComplete=true` is backed by a sealed OpenBao secret the chart's upgrade CANNOT revert.

**STILL BLOCKED (now handover-gated, not chart-blocked):** every row presupposes a COMPLETED cutover (a sealed `cutover-complete` secret to test durability against). On hw158 the cutover has not re-run — the chart blocker is cleared but the engine awaits the post-handover trigger. Re-walk after handover seals `tofu-phase0-archive` and the cutover completes.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 1.1 | `kubectl get secret cutover-complete -n catalyst` | sealed secret EXISTS | `NotFound` — cutover never reached its success tail | ❌ |
| 1.2 | snapshot `…-l cutover.openova.io/run-epoch -o name` | a list captured | EMPTY — current cutover Jobs (`cutover-gitea-mirror/harbor-projects/harbor-prewarm-1781677444`) are NOT `run-epoch`-labelled on chart 0.1.75 | ❌ (label absent) |
| 1.3 | `flux reconcile hr bp-self-sovereign-cutover … --with-source` | reconciliation finished | the cutover HR is itself `Ready=True` now (`Helm install succeeded … chart bp-self-sovereign-cutover@0.1.75`), but reconciling it does NOT re-fire the engine (the in-cluster trigger 425s pre-handover); durability-vs-upgrade is un-exercisable without a completed run | N/A |
| 1.4 | `…{.data.cutoverComplete}` STILL `true` | re-derived `true` from seal | currently `false`; no seal to re-derive from | ❌ |
| 1.5 | diff jobs before/after — NO new cutover Jobs | empty diff | un-testable (no completed baseline) | N/A |
| 1.6 | `kubectl get ccnp cutover-egress-block` | `NotFound` | `NotFound` — vacuously (step-08 never ran) | N/A (vacuous) |
| 1.7 | restart catalyst-api → log "already complete (sealed); nothing to resume" | resume hook no-ops | catalyst-api pod is 3h59m old (NOT rolled) → no resume-on-restart exercised; with no sealed-complete state, a restart would attempt to RESUME the failed step-02, not no-op | N/A |
| 1.8 | console → Settings → Sovereignty — steady "Sovereign — tethers severed", CTA hidden | sovereign state | curl-only this session; and the card would NOT show "tethers severed" — cutover is incomplete | ⏳ (visual + not-applicable-state) |

---

## Section 2 — FAITHFUL REGISTRY PIVOT: `registriesYamlActive=v2` flips node containerd to LOCAL Harbor (#3671)

**Goal:** the engine sets `registriesYamlActive=v2` after `harbor-prewarm` succeeds.

**STILL BLOCKED at the precondition (now handover-gated):** step-02 `harbor-prewarm` FAILED on the old run, so step-03/04 (registry-pivot) never ran; `registriesYamlActive=v1` on all 4 nodes. The chart blocker is cleared — when the cutover re-fires post-handover, prewarm will pass (1.4.30 mirror-able, HTTP 200) and the pivot can run.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 2.1 | `…{.data.registriesYamlActive}` | `v2` | **`v1`** — pivot never fired (cutover not re-run) | ❌ |
| 2.2 | `…{.data.step\.registry-pivot\.result}` == `success` + per-node ack | success | **empty** (`step.registry-pivot.result=""`, `startedAt=""`) — never ran | ❌ |
| 2.3 | exec registry-pivot DS pod → `cat /host/etc/rancher/k3s/registries.yaml` rewrites to `harbor.<fqdn>` | local Harbor | the `registry-pivot` DS EXISTS (install-time, 4/4, age 5h31m) but never executed the v2 rewrite — all 4 nodes report `node.*.registriesYaml=v1`; step-04 never fired, so containerd still points at the mothership | ❌ (DS present but at v1) |
| 2.4 | with egress denied, pull a non-openova image via local Harbor | pull SUCCEEDS | un-testable — no pivot, no deny-egress hold | N/A |
| 2.5 | console /jobs step-04 row "N/N nodes at v2" | green row | curl-only; step-04 never ran | ⏳ (visual) |
| 2.6 | NEGATIVE on a scratch prov — hold one node, step-04 stays red | real verification | scratch-prov negative test, not runnable on this live env | ⏳ (scratch-prov) |

---

## Section 3 — TRUE DENY-EGRESS: the hold blocks the mothership broadly, not 4 CIDRs (#3678)

**Goal:** the 600s hold is a default-deny-egress with explicit intra-cluster allow-CIDRs.

**STILL BLOCKED (now handover-gated):** step-08 (`egress-block-test`) is step index 8 of 11; the old run died at index 2. The `cutover-egress-block` CCNP does not exist; no hold ever held. Becomes walkable once the cutover re-fires and reaches step-08.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 3.1 | during step-08 hold → `curl https://console.openova.io/healthz` FAILS | connection blocked | no hold ever fired — cannot probe a non-existent window | ❌ (step never reached) |
| 3.2 | `curl https://kubernetes.default.svc` SUCCEEDS (no #3640 strand) | apiserver reachable | un-testable (no hold); apiserver reachable generally (cluster up) | N/A |
| 3.3 | `kubectl get ccnp cutover-egress-block -o yaml` → `enableDefaultDeny.egress:true` + intra-cluster allow | default-deny-with-allow | **`NotFound`** — CCNP absent | ❌ |
| 3.4 | step-08 log `grep call-home` → "call-home denied + verified" | active assertion | no step-08 Job exists | ❌ |
| 3.5 | `kubectl get secret ghcr-pull -n flux-system … base64 -d` keys `harbor.<fqdn>`, NOT `ghcr.io` | tether #5 re-keyed | **keys `["ghcr.io"]`** — still the pre-cutover mothership key (correct pre-cutover; proves the pivot did NOT happen) | ❌ (still ghcr.io) |
| 3.6 | scratch-prov generality — arbitrary mothership host caught | proof catches it | scratch-prov negative, not runnable here | ⏳ (scratch-prov) |
| 3.7 | console /jobs step-08 row reflects mothership-wide block | UI row | curl-only; step-08 never ran | ⏳ (visual) |

---

## Section 4 — AUDIT FIDELITY: `cutoverStartedAt` is the true T0, never re-stamped on resume (#3681)

**Goal:** `cutoverStartedAt` written ONCE; `cutoverLastAttemptStartedAt` carries each resume. **The #3681 structure IS PRESENT and now PROVEN with a REAL resume on hw158 (this is the section with the strongest PASS).** This RE-VALIDATION caught an actual restart-driven resume (catalyst-api rolled at `08:46` on the 1.4.665 churn): the engine re-entered (`cutover-resume: spawning runCutover to resume interrupted cutover`) and advanced `cutoverLastAttemptStartedAt` to the restart moment WHILE preserving the true T0 — exactly the #3681 contract, observed live (no longer "field merely wired").

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 4.1 | fire cutover; restart catalyst-api MID-RUN during harbor-prewarm; resume continues from last step | resume re-enters | **OBSERVED LIVE** — catalyst-api rolled at `08:46` (1.4.665 churn); logs: `08:46:13 cutover-resume: in-flight cutover detected on startup (currentStepIndex=2, failedStep=harbor-prewarm)` → `08:46:14 cutover-resume: spawning runCutover to resume interrupted cutover (totalSteps=11)`. The resume RE-ENTERED from the last step. (It then correctly re-reported the carried-over failed prewarm Job — the resume re-uses the stale Job rather than recreating it, so it can't yet exercise the now-mirror-able 1.4.30; a clean fresh-Job re-fire needs the post-handover path) | ✅ (resume re-enters — observed) |
| 4.2 | `cutoverStartedAt` == EARLIEST `step.*.startedAt` | equal within seconds | **EQUAL** — `cutoverStartedAt = step.gitea-mirror.startedAt = 2026-06-17T06:24:04Z`. The #3681 corruption is FIXED | ✅ |
| 4.3 | `cutoverLastAttemptStartedAt` == restart moment, recorded separately | separate field | **PROVEN with a real resume** — `cutoverLastAttemptStartedAt=2026-06-17T08:46:14Z` (the `08:46` restart moment) while `cutoverStartedAt` stayed at the true T0 `06:24:04Z`. The two diverged exactly as #3681 requires (prior re-walk had them equal because no resume had yet occurred) | ✅ (proven live — fields diverge correctly) |
| 4.4 | console Sovereignty "Started <true T0>" + real elapsed | true window | curl-only this session | ⏳ (visual) |

---

## Section 5 — HOST GITOPS LOOP SURVIVES + zero residual mothership tether (#3695)

**Goal:** step-10 image-host injection merges into the existing `values:` mapping; `bootstrap-kit` reconciles Ready post-cutover; NO live pod references a mothership registry.

**MIXED — improved + one regression:** `bp-sso-bridge` HR is `Ready=True` (recovered from the missing-bp-keycloak dep) and `sme-tenants` improved `False → Ready=True`. But `bootstrap-kit` **REVERTED** to `Ready=Unknown :: Progressing` — held by the NEW `bp-catalyst-platform@1.4.665` post-hook crashloop (a deploy-bot roll, NOT keycloak); `sovereign-tls` is `False :: DependencyNotReady` on bootstrap-kit. The **post-cutover END-STATE** (registry pivot, zero residual tether) still can't be asserted because the cutover hasn't completed; mothership tethers are STILL LIVE (correct pre-cutover).

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 5.1 | `kubectl get kustomization -n flux-system bootstrap-kit sovereign-tls sme-tenants` all READY at post-cutover rev | all Ready | **`bootstrap-kit Ready=Unknown :: Progressing`** (`applied=d436ad4a…` ≠ `attempted=ff63a8e8…`; health-check timeout on `bp-catalyst-platform/bp-continuum/bp-sandbox InProgress`) — **REVERTED** from the prior `Ready=True`, held by the NEW 1.4.665 catalyst-platform post-hook crashloop. `sovereign-tls False :: DependencyNotReady (on bootstrap-kit)`. **`sme-tenants` IMPROVED → `Ready=True`** (`ReconciliationSucceeded`, `Applied sme-tenants@sha1:0046e246…`; was `Source artifact not found`). Not a post-cutover revision | ❌ (bootstrap-kit reverted on the 1.4.665 roll; sme-tenants improved ✅; none at post-cutover state) |
| 5.2 | `bootstrap-kit … lastAppliedRevision` == latest cutover commit on local Gitea | pivot commits applied | `lastApplied=main@sha1:d436ad4a…` ≠ `lastAttempted=main@sha1:ff63a8e8…` — NOT converged (mid-reconcile, blocked on the 1.4.665 spine churn). Neither is a post-cutover pivot commit (cutover hasn't run) | ❌ (not converged; not yet post-cutover) |
| 5.3 | `bp-sso-bridge … spec.values` `global.registryMirror=harbor.<fqdn>`; exactly ONE `values:` key | one values key | **`bp-sso-bridge` HR `Ready=True`** (`Helm install succeeded … chart bp-sso-bridge@0.2.20`) — recovered from the missing-bp-keycloak dep; but it is NOT pivoted to the local mirror (cutover step-10 hasn't run) | ❌ (HR Ready; not pivoted) |
| 5.4 | `kubectl get pods -n sso-bridge -o jsonpath='…image'` → `harbor.<fqdn>/…`, NOT `harbor.openova.io` | local Harbor | **`harbor.openova.io/proxy-dockerhub/alpine/k8s:1.30.0`** — MOTHERSHIP tether live (cutover never re-keyed) | ❌ |
| 5.5 | cluster-wide pod-image grep for `harbor.openova.io\|ghcr.io/openova-io\|github.com/openova` is EMPTY | no mothership pulls | **NON-EMPTY** — `ghcr.io/openova-io/openova/catalyst-api`, `…/console`, `…/continuum-controller`, plus sso-bridge on `harbor.openova.io`. Many live mothership tethers (expected pre-cutover) | ❌ |
| 5.6 | push trivial commit to local Gitea → bootstrap-kit reconciles green | loop alive sans mothership | not exercised; bootstrap-kit IS now at a stable `Ready=True` (precondition met), but independence-from-mothership is only provable post-cutover | ❌ (precondition recovered; end-state pending) |
| 5.7 | re-fire cutover (idempotency) → `13b-bp-sso-bridge.yaml` still ONE `values:` key | no BuildFailed | re-fire is gated pre-handover (425); idempotency of step-10 injection not reachable until the run advances past step-02 | N/A |
| 5.8 | scratch-prov generality — deliberate residual HR makes proof FAIL | generic gate | scratch-prov negative, not runnable here | ⏳ (scratch-prov) |
| 5.9 | console Sovereignty step-08 per-workload pass/fail panel | UI panel | curl-only; step-08 never ran | ⏳ (visual) |

---

## Section 6 — END-TO-END: a cut-over Sovereign onboards a customer with ZERO mothership dependency

**Goal:** the corrected cutover leaves a Sovereign that is independent AND still works.

**STILL BLOCKED (now handover-gated):** 6.1 holds (console up, 200), but 6.2 onward requires running the cutover to completion, which awaits the post-handover re-fire (the chart blocker is cleared).

| # | Do | Expected | OBSERVED on hw158 (RE-VALIDATION #2) | Verdict |
|---|---|---|---|---|
| 6.1 | bare `https://console.<fqdn>` → land `/dashboard` signed-in | dashboard, signed-in admin | `console.hw158.omani.works/` → HTTP 200 (reachable); signed-in landing is a browser check not done curl-only | ⏳ (visual; endpoint up) |
| 6.2 | console Sovereignty → "Achieve True Sovereignty" → steps 01-11 | progress card | cutover parked at the stale step-02; re-fire gated on handover. Path not yet exercisable to green | ❌ |
| 6.3 | wait → `cutoverComplete=true` only after all 11 steps + hold + Ready gate | true completion | `cutoverComplete=false`, parked at step-02 | ❌ |
| 6.4 | push to local Gitea → bootstrap-kit reconciles green | loop alive | bootstrap-kit IS now `Ready=True`; but a post-cutover independence demonstration needs the run to complete | ❌ |
| 6.5 | BSS Vouchers → redeem → Organization materializes | tenant home | `sme-tenants` kustomization IMPROVED to `Ready=True` (`Applied sme-tenants@sha1:0046e246…`; was `Source artifact not found`) — the tenant source now reconciles. But a ZERO-mothership-dependency onboard still requires the cutover to complete (post-handover); not demonstrable pre-cutover | ❌ (source recovered; end-to-end onboard still needs the completed cutover) |
| 6.6 | black-hole github/ghcr/harbor.openova.io → Sovereign keeps serving | independence | live pods still pull from `ghcr.io/openova-io` + `harbor.openova.io` (5.5) → black-holing would break pulls. Not independent pre-cutover | ❌ |

---

## Appendix A — Automated guards (NOT acceptance; source-level pre-stage only)

These `go test` / render-test rows protect the fix at source; they are evidence the guard exists, **not** the founder walk. Not executed in this live walk (acceptance is the live rows above).

| # | Command | Pass condition |
|---|---|---|
| A.1 | `cd products/catalyst/bootstrap/api && go test -race ./internal/handler/ -run Cutover` | green; the new tests pass |
| A.2 | two-call `runCutover` test | `cutoverStartedAt` byte-identical across calls; `cutoverLastAttemptStartedAt` advances (#3681) |
| A.3 | sealed-fact gate test | `spawnCutoverEngine` + `ResumeInterruptedCutover` no-op when `secret/catalyst/cutover-complete` is sealed, even with the CM reverted to `false` (#3667) |
| A.4 | render test on `09-cutover-status-configmap.yaml` | a `helm upgrade` does NOT emit `cutoverComplete:"false"` / `cutoverStartedAt:""` / `registriesYamlActive:"v1"` over a live state (#3667) |
| A.5 | injector unit test (step-10) | feed `13b-bp-sso-bridge.yaml` ALREADY carrying a `values:` block → output parses as valid single-`values:` YAML (#3695) |
| A.6 | cutover invariant test | `runCutover` cannot reach `cutoverComplete=true` while `registriesYamlActive != "v2"` (#3671) |
| A.7 | render test on `08-egress-block-test-job.yaml` | emitted CCNP is default-deny-with-allow-list, not a 4-CIDR subtractive block (#3678) |

---

## Evidence ledger

On a fresh prov, every row above gets a screenshot or `kubectl` capture committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`. `cutoverComplete=true` is admissible ONLY when Sections 1-6 are all green on the SAME env (per memory: each new env flushes all prior evidence — re-prove live).

### hw158 RE-VALIDATION #2 (2026-06-17T08:54Z, keycloak-1.4.30 CLEARED + real resume observed + NEW 1.4.665 catalyst-platform roll-regression) — Tally

- **9 ✅** — `0.1` (shell); `0.3`/`0.4`/`0.7` reproduced the live pre-cutover symptoms; `0.5`/`4.2`/`4.3` confirm the #3681 audit-fidelity fix (`cutoverStartedAt == step.gitea-mirror.startedAt == 06:24:04Z`; `cutoverLastAttemptStartedAt` ADVANCED to the `08:46` restart while T0 held — proven with a real resume, not just wired); **`4.1` newly ✅ — the resume RE-ENTERED mid-run, observed live in catalyst-api logs (`cutover-resume: spawning runCutover…`)**.
- **16 ❌** — cutover-END-STATE rows still unmet because the cutover has NOT completed (handover-gated, no longer chart-blocked): `0.2`, `1.1`, `1.2`, `1.4`, `2.1`, `2.2`, `2.3`, `3.1`, `3.3`, `3.4`, `3.5`, `5.3`, `5.4`, `5.5`, `5.6`, `6.2`, `6.3`, `6.4`, `6.5`, `6.6` — PLUS `0.6`/`5.1`/`5.2` which **REVERTED to ❌** vs the prior re-walk: `bootstrap-kit` is currently `Unknown/Progressing`, held by the NEW `bp-catalyst-platform@1.4.665` `catalyst-gitea-flux-auth-sync` post-hook crashloop (`PAT.token empty`) — a deploy-bot roll regression, NOT the keycloak cascade and NOT #3379. (`0.6` was ✅ in the prior re-walk; this is an honest live re-check, not a carried verdict.)
- **15 N/A** — rows un-testable without a completed cutover baseline (`0.8`, `1.3`, `1.5`, `1.6`, `1.7`, `2.4`, `3.2`, `5.7`).
- **4 ⏳** — handover-gated/visual/scratch-prov rows (`1.8`, `2.5`/`2.6`, `3.6`/`3.7`, `4.4`, `5.8`/`5.9`, `6.1`) — browser-only or negative-on-scratch-prov, not runnable curl-only this session.

**Root-cause status:**
1. **KEYCLOAK CASCADE — CLOSED.** `bp-keycloak:1.4.30` is **PUBLISHED + mirror-able** on `ghcr.io/openova-io/bp-keycloak` (in-cluster robot HEAD → `HTTP 200` at parity with `1.4.29`; Flux `pulled … 1.4.30`; HR `Ready=True`). All 8 cascade spine HRs (`bp-keycloak / bp-gitea / bp-grafana / bp-guacamole / bp-sso-bridge / bp-oidc-gate / bp-powerdns-admin / bp-newapi`) are `Ready=True`. The `manifest unknown` blocker is gone.
2. **🆕 NEW catalyst-platform 1.4.665 roll-regression (separate ticket; NOT #3379).** A deploy-bot bump to `bp-catalyst-platform@1.4.665` fails its post-upgrade hook (`job catalyst-gitea-flux-auth-sync … BackoffLimitExceeded`; pod `FATAL: catalyst-gitea-token.token empty (mint hook did not populate the PAT)`), cycling `upgrade→fail→rollback-to-1.4.664→retry`. This holds `bootstrap-kit` (Unknown/Progressing), `bp-continuum` + `bp-sandbox` (DependencyNotReady), and `sovereign-tls` (DependencyNotReady) non-Ready → effective tally `58/65`. Orthogonal to the cutover.
3. **CUTOVER END-STATE — handover-gated.** The engine re-fired a real resume this session (proving #3681/#4.1 live) but wedged on the carried-over stale-failed prewarm Job; a CLEAN re-fire (fresh prewarm Job that mirrors 1.4.30) needs the post-handover path (`ReceiveTofuArchive` seals `tofu-phase0-archive` → engine spawns a fresh run; the in-cluster `POST /internal/cutover/trigger` returns `425 Too Early` pre-handover). Handover front-door healthy (`api/healthz=200`, `console/=200`).

**Verdict: ⏳ KEYCLOAK-1.4.30 BLOCKER CLOSED + audit-fidelity resume PROVEN live (`4.1` flipped ✅); cutover end-state still handover-gated (NOT a chart/code defect). A NEW, separate `bp-catalyst-platform@1.4.665` post-hook regression currently holds `bootstrap-kit`/`continuum`/`sandbox` non-Ready — reverting `0.6`/`5.1`/`5.2` to ❌ honestly (8/64→58/65 tally; orthogonal to #3379).**
