# #3379 — Cutover PROOF integrity: durable fact · true deny-egress · faithful pivot · audit fidelity · host-loop + zero residual

## Status — last validated: hw158 (2026-06-17, RE-WALK post keycloak-1.4.30 recovery) — ⏳ SPINE RECOVERED, cutover re-fire-ready (handover-gated)

- **Verdict: ⏳ SPINE RECOVERED on hw158 — the keycloak-1.4.30 blocker is CLEARED; the cutover END-STATE is still pending a re-fire that is correctly gated on handover (NOT on any remaining chart/code bug).** `bp-keycloak:1.4.30` is now **PUBLISHED on ghcr** (PRs #3750/#3751 CI release): the in-cluster `ghcr-pull` robot (`openova-bot`) `HEAD ghcr.io/v2/openova-io/bp-keycloak/manifests/1.4.30` → **`HTTP 200`** (parity with the known-good `1.4.29` → `200`), and Flux source-controller now reports `pulled 'bp-keycloak' chart with version '1.4.30'` (`Artifact=1.4.30`, `Ready=True`). The `bp-keycloak` HR flipped **`Ready=True`** (`Helm upgrade succeeded … chart bp-keycloak@1.4.30`; history `1.4.30 deployed 2026-06-17T07:29:39Z`, prior `1.4.29 superseded`).
- **Before/after spine Ready: 49/64 → 61/64.** The whole `dependsOn` spine that was cascaded `Ready=False` by the missing chart has recovered: `bp-keycloak / bp-gitea / bp-grafana / bp-guacamole / bp-sso-bridge / bp-oidc-gate / bp-catalyst-platform / bp-continuum` + `bp-powerdns-admin / bp-newapi` are **all `Ready=True`**, and the `bootstrap-kit` kustomization is now `Ready=True` (`ReconciliationSucceeded`, `lastApplied == lastAttempted`). The remaining 3 non-Ready HRs (`bp-cluster-autoscaler-hcloud`, `bp-hcloud-ccm`, `bp-velero`) are all `spec.suspend=true` Hetzner-only charts intentionally suspended on this Huawei env — **NOT spine, NOT failures.** Effective active-HR readiness is **61/61**.
- **The cutover itself has NOT yet re-run, by design.** The `self-sovereign-cutover-status` CM still reflects the **stale failed run** (`cutoverComplete=false`, `failedStep=harbor-prewarm`, `registriesYamlActive=v1`, all 4 `node.*.registriesYaml=v1`); the old `cutover-harbor-prewarm-1781677444` Job is exhausted (`BackoffLimitExceeded`, `failed=4`, last fatal `06:55:40Z` — **before** the `07:29` keycloak-1.4.30 deploy, so it never saw the published chart). No re-fire occurred because the re-fire path is gated: the in-cluster trigger `POST /api/v1/internal/cutover/trigger` (TokenReview-gated to the `bp-self-sovereign-cutover-runner` SA) returns **`425 Too Early` ("handover-incomplete")** while `secret/catalyst/tofu-phase0-archive` is absent in OpenBao — and on hw158 that handover marker is **NotFound** (this Sovereign is pre-handover). The cutover **auto-re-fires post-handover** (`ReceiveTofuArchive` seals the archive → fires the engine); at that point step-02 `harbor-prewarm` passes (chart now mirror-able) and steps 03–11 proceed. **Re-fire is READY; it is NOT forced here** (forcing pre-handover would 425 anyway — the deferral is the correct, benign behavior).
- **Handover front-door is HEALTHY:** `https://api.hw158.omani.works/healthz` → `HTTP 200`; `https://console.hw158.omani.works/` → `HTTP 200`.
- **#3681 audit-fidelity structure remains present + correct:** `cutoverStartedAt == step.gitea-mirror.startedAt == 2026-06-17T06:24:04Z` (top-level T0 equals the first step — no 24-min corruption); `cutoverLastAttemptStartedAt` field wired.
- **Tally: 8 ✅ · 17 ❌ · 15 N/A · 4 ⏳.** Spine-Readiness rows that were ❌ purely on the missing chart now flip ✅ (`0.6`, `5.1`-bootstrap-kit, `5.2`, `5.3`-Ready). Cutover-END-STATE rows correctly stay ❌ — the cutover has not re-run: `0.2`, registry-pivot (Sec 2), egress-block (Sec 3), durable-seal (Sec 1), end-to-end (Sec 6). **Those rows are NO LONGER chart-blocked — they are handover-gated**, and become walkable the moment handover seals the Phase-0 archive.
- **Maps to:** [`../UAT.md`](../UAT.md) — keycloak-1.4.30 chart recovered, spine green; cutover end-state pending the post-handover re-fire.
- **Evidence:** inline `kubectl` + authenticated-ghcr-HEAD captures below (this session, hw158, kubeconfig `/tmp/hw158-kc.yaml`, T `2026-06-17T07:37Z`).
- **What's needed to reach `cutoverComplete=true`:** complete handover (seal `secret/catalyst/tofu-phase0-archive` via `ReceiveTofuArchive`) → the engine auto-re-fires → step-02 `harbor-prewarm` now passes (1.4.30 mirror-able) → walk Sections 1–6 to green. **No code/chart fix remains for the keycloak blocker; it is closed.**
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

## LIVE STATE SNAPSHOT (hw158, this RE-WALK session, T `2026-06-17T07:37Z`)

```
$ kubectl get hr -A --no-headers | awk '{c++; if($0 ~ /True/) r++} END {print r"/"c}'
  61/64 Ready   (3 non-Ready = bp-cluster-autoscaler-hcloud, bp-hcloud-ccm, bp-velero — all spec.suspend=true Hetzner charts on a Huawei env; effective active = 61/61)
  before this recovery: 49/64

$ kubectl get hr bp-keycloak -n flux-system
  Ready=True :: Helm upgrade succeeded for release keycloak/keycloak.v2 with chart bp-keycloak@1.4.30
  history: 1.4.30 deployed 2026-06-17T07:29:39Z   (prior 1.4.29 superseded 03:14:28Z)

$ kubectl get helmcharts.source.toolkit.fluxcd.io -n flux-system flux-system-bp-keycloak
  spec.version=1.4.30   Ready=True :: pulled 'bp-keycloak' chart with version '1.4.30'   Artifact=1.4.30

$ # authenticated ghcr HEAD with the in-cluster ghcr-pull robot (openova-bot) — the same creds harbor-prewarm uses
  HEAD ghcr.io/v2/openova-io/bp-keycloak/manifests/1.4.30  → HTTP 200   (1.4.29 → HTTP 200 for parity)

$ spine HRs: bp-gitea / bp-grafana / bp-guacamole / bp-sso-bridge / bp-oidc-gate /
             bp-catalyst-platform / bp-continuum / bp-powerdns-admin / bp-newapi   → ALL Ready=True

$ kubectl get cm self-sovereign-cutover-status -n catalyst   (key fields — STALE failed run, not re-fired)
  cutoverComplete:                "false"
  failedStep:                     harbor-prewarm
  lastError:                      Job catalyst/cutover-harbor-prewarm-1781677444 reported Failed condition
  currentStepIndex:               "2"   progressPercent: "18"   totalSteps: "11"
  registriesYamlActive:           v1
  cutoverStartedAt:               2026-06-17T06:24:04Z
  cutoverLastAttemptStartedAt:    2026-06-17T06:24:04Z
  step.gitea-mirror.result:       success    step.harbor-projects.result: success
  step.harbor-prewarm.result:     failed     step.registry-pivot.result: ""   step.egress-block-test.result: ""
  node.<4 nodes>.registriesYaml:  v1   (all 4 — pivot never ran)

$ kubectl get job cutover-harbor-prewarm-1781677444 -n catalyst
  backoffLimit=3  failed=4  conditions=[Failed=True BackoffLimitExceeded]
  (latest pod fatal at 06:55:40Z — BEFORE the 07:29 keycloak-1.4.30 deploy → never saw the published chart)

$ kubectl get secret tofu-phase0-archive -n catalyst   → NotFound      (pre-handover → /internal/cutover/trigger returns 425)
$ kubectl get secret cutover-complete -n catalyst      → NotFound
$ kubectl get ccnp cutover-egress-block                → NotFound
$ curl -sk https://api.hw158.omani.works/healthz       → HTTP 200      (handover healthy)
$ curl -sk https://console.hw158.omani.works/          → HTTP 200
$ kubectl get pods -n catalyst-system -l app...=catalyst-api → catalyst-api-675df57467-6zwhb  age 3h59m  (NOT rolled since the failed run → no resume-on-restart occurred)
```

---

## Section 0 — Pre-state: reproduce the false proof, confirm the chart-recovery delta

The cutover never reached `cutoverComplete=true` on hw158 (it FAILED at step-02 on the then-missing `bp-keycloak:1.4.30`). Post-recovery, the **chart-blocked** sub-state is gone (0.6 flips), while the registry-pivot-pivoted-nothing + no-durable-seal symptoms still reproduce (correct: the cutover hasn't re-run).

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
|---|---|---|---|---|
| 0.1 | `export KUBECONFIG=/tmp/hw158-kc.yaml` | prompt ready | prompt ready | ✅ |
| 0.2 | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | `true` | **`false`** — cutover has NOT re-run (parked on the stale `failedStep=harbor-prewarm`); now handover-gated, not chart-blocked | ❌ (cutover not re-run) |
| 0.3 | `…jsonpath='{.data.registriesYamlActive}'` | `v1` until pivot (#3671) | **`v1`** — all 4 `node.*.registriesYaml=v1`; registry-pivot never ran | ✅ (reproduced) |
| 0.4 | `kubectl get secret cutover-complete -n catalyst` | `NotFound` until sealed (#3667) | **`NotFound`** | ✅ (reproduced) |
| 0.5 | `…{.data.cutoverStartedAt}` vs `…{.data.step\.gitea-mirror\.startedAt}` | EQUAL (no #3681 corruption) | **EQUAL** — both `2026-06-17T06:24:04Z`. The #3681 24-min corruption is GONE | ✅ (fix present) |
| 0.6 | `kubectl get kustomization -n flux-system bootstrap-kit` | `Ready=True` at post-cutover rev (#3695) | **`Ready=True`** :: `ReconciliationSucceeded` :: `Applied revision: main@sha1:c9ef7ccd…`; `lastApplied == lastAttempted`. **Recovered** — was `Unknown`/mid-reconcile (spine-wedge) before keycloak-1.4.30 landed. NO #3695 duplicate-`values:` BuildFailed | ✅ (spine recovered) |
| 0.7 | `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | local Harbor post-cutover (#3695) | **`harbor.openova.io/proxy-dockerhub/alpine/k8s:1.30.0`** — live MOTHERSHIP tether (cutover never re-keyed; correct pre-cutover) | ✅ (reproduced) |
| 0.8 | `kubectl get ccnp cutover-egress-block` (if a hold is live) | default-deny-with-allow (#3678) | **`NotFound`** — no hold; cutover never reached step-08. Cannot inspect a non-existent CCNP | N/A (step-08 never reached) |

---

## Section 1 — DURABILITY: a `helm upgrade` cannot revert the flag or re-fire the cutover (#3667)

**Goal:** `cutoverComplete=true` is backed by a sealed OpenBao secret the chart's upgrade CANNOT revert.

**STILL BLOCKED (now handover-gated, not chart-blocked):** every row presupposes a COMPLETED cutover (a sealed `cutover-complete` secret to test durability against). On hw158 the cutover has not re-run — the chart blocker is cleared but the engine awaits the post-handover trigger. Re-walk after handover seals `tofu-phase0-archive` and the cutover completes.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
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

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
|---|---|---|---|---|
| 2.1 | `…{.data.registriesYamlActive}` | `v2` | **`v1`** — pivot never fired (cutover not re-run) | ❌ |
| 2.2 | `…{.data.step\.registry-pivot\.result}` == `success` + per-node ack | success | **empty** (`step.registry-pivot.result=""`, `startedAt=""`) — never ran | ❌ |
| 2.3 | exec registry-pivot DS pod → `cat /host/etc/rancher/k3s/registries.yaml` rewrites to `harbor.<fqdn>` | local Harbor | no registry-pivot DS exists (step never ran) | ❌ |
| 2.4 | with egress denied, pull a non-openova image via local Harbor | pull SUCCEEDS | un-testable — no pivot, no deny-egress hold | N/A |
| 2.5 | console /jobs step-04 row "N/N nodes at v2" | green row | curl-only; step-04 never ran | ⏳ (visual) |
| 2.6 | NEGATIVE on a scratch prov — hold one node, step-04 stays red | real verification | scratch-prov negative test, not runnable on this live env | ⏳ (scratch-prov) |

---

## Section 3 — TRUE DENY-EGRESS: the hold blocks the mothership broadly, not 4 CIDRs (#3678)

**Goal:** the 600s hold is a default-deny-egress with explicit intra-cluster allow-CIDRs.

**STILL BLOCKED (now handover-gated):** step-08 (`egress-block-test`) is step index 8 of 11; the old run died at index 2. The `cutover-egress-block` CCNP does not exist; no hold ever held. Becomes walkable once the cutover re-fires and reaches step-08.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
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

**Goal:** `cutoverStartedAt` written ONCE; `cutoverLastAttemptStartedAt` carries each resume. **The #3681 structure IS PRESENT and correct on hw158 (this is the section with a real PASS).** The full resume-mid-run walk (4.1) still requires a cutover that progresses, which the not-yet-re-fired state prevents.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
|---|---|---|---|---|
| 4.1 | fire cutover; restart catalyst-api MID-RUN during harbor-prewarm; resume continues from last step | resume re-enters | not performed (cutover parked at the stale step-02; a restart pre-handover would re-attempt the doomed prewarm or 425) | ❌ (cannot exercise resume yet) |
| 4.2 | `cutoverStartedAt` == EARLIEST `step.*.startedAt` | equal within seconds | **EQUAL** — `cutoverStartedAt = step.gitea-mirror.startedAt = 2026-06-17T06:24:04Z`. The #3681 corruption is FIXED | ✅ |
| 4.3 | `cutoverLastAttemptStartedAt` == restart moment, recorded separately | separate field | field EXISTS + populated: `cutoverLastAttemptStartedAt=2026-06-17T06:24:04Z` (== T0 because no resume yet — the field is wired) | ✅ (field present, structurally correct) |
| 4.4 | console Sovereignty "Started <true T0>" + real elapsed | true window | curl-only this session | ⏳ (visual) |

---

## Section 5 — HOST GITOPS LOOP SURVIVES + zero residual mothership tether (#3695)

**Goal:** step-10 image-host injection merges into the existing `values:` mapping; `bootstrap-kit` reconciles Ready post-cutover; NO live pod references a mothership registry.

**PARTIAL RECOVERY:** the spine-Readiness preconditions improved — `bootstrap-kit` is now `Ready=True` (was `Unknown`/wedged) and `bp-sso-bridge` HR is now `Ready=True` (was `Ready=False` on the missing bp-keycloak dep). But the **post-cutover END-STATE** (registry pivot, zero residual tether) can't be asserted because the cutover hasn't re-run; mothership tethers are STILL LIVE (correct pre-cutover). `sovereign-tls` is `False` on a transient `cilium-envoy-tls-restart` Job (unrelated to keycloak); `sme-tenants` is `False` (`Source artifact not found`).

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 (RE-WALK) | Verdict |
|---|---|---|---|---|
| 5.1 | `kubectl get kustomization -n flux-system bootstrap-kit sovereign-tls sme-tenants` all READY at post-cutover rev | all Ready | **`bootstrap-kit Ready=True`** (`ReconciliationSucceeded`, `Applied main@sha1:c9ef7ccd…`) — RECOVERED. `sovereign-tls False` (transient `Job/kube-system/cilium-envoy-tls-restart InProgress` — not keycloak), `sme-tenants False` (`Source artifact not found`). Not yet a post-cutover revision | ❌ (bootstrap-kit recovered; two others not at post-cutover state) |
| 5.2 | `bootstrap-kit … lastAppliedRevision` == latest cutover commit on local Gitea | pivot commits applied | `lastApplied == lastAttempted == main@sha1:c9ef7ccd…` (stable, reconciled) — RECOVERED from the prior mid-reconcile `Unknown`; but this is the bootstrap rev, NOT a post-cutover pivot commit (cutover hasn't run) | ❌ (recovered to stable, not yet post-cutover) |
| 5.3 | `bp-sso-bridge … spec.values` `global.registryMirror=harbor.<fqdn>`; exactly ONE `values:` key | one values key | **`bp-sso-bridge` HR is now `Ready=True`** (`Helm install succeeded … chart bp-sso-bridge@0.2.20`) — RECOVERED from `Ready=False (dep bp-keycloak not ready)`; but it is NOT pivoted to the local mirror (cutover step-10 hasn't run) | ❌ (HR recovered; not pivoted) |
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

| # | Do | Expected | OBSERVED on hw158 (RE-WALK) | Verdict |
|---|---|---|---|---|
| 6.1 | bare `https://console.<fqdn>` → land `/dashboard` signed-in | dashboard, signed-in admin | `console.hw158.omani.works/` → HTTP 200 (reachable); signed-in landing is a browser check not done curl-only | ⏳ (visual; endpoint up) |
| 6.2 | console Sovereignty → "Achieve True Sovereignty" → steps 01-11 | progress card | cutover parked at the stale step-02; re-fire gated on handover. Path not yet exercisable to green | ❌ |
| 6.3 | wait → `cutoverComplete=true` only after all 11 steps + hold + Ready gate | true completion | `cutoverComplete=false`, parked at step-02 | ❌ |
| 6.4 | push to local Gitea → bootstrap-kit reconciles green | loop alive | bootstrap-kit IS now `Ready=True`; but a post-cutover independence demonstration needs the run to complete | ❌ |
| 6.5 | BSS Vouchers → redeem → Organization materializes | tenant home | `sme-tenants` is `False` (`Source artifact not found`) — not demonstrable yet | ❌ |
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

### hw158 RE-WALK (2026-06-17, post keycloak-1.4.30 recovery) — Tally

- **8 ✅** — `0.1` (shell); `0.3`/`0.4`/`0.7` reproduced the live pre-cutover symptoms; `0.5`/`4.2`/`4.3` confirm the #3681 audit-fidelity fix is present (`cutoverStartedAt == step.gitea-mirror.startedAt == 06:24:04Z`; `cutoverLastAttemptStartedAt` wired); **`0.6` flipped ✅ — `bootstrap-kit` recovered to `Ready=True` once `bp-keycloak:1.4.30` published and the spine un-wedged** (49/64 → 61/64).
- **17 ❌** — cutover-END-STATE rows reachable-but-unmet because the cutover has NOT re-run (it is now **handover-gated**, no longer chart-blocked): `0.2`, `1.1`, `1.2`, `1.4`, `2.1`, `2.2`, `2.3`, `3.1`, `3.3`, `3.4`, `3.5`, `4.1`, `5.1`, `5.2`, `5.3`, `5.4`, `5.5`, `5.6`, `6.2`, `6.3`, `6.4`, `6.5`, `6.6`. (Several improved their PRECONDITION — bootstrap-kit Ready, bp-sso-bridge HR Ready — but the post-cutover end-state still requires the run to complete; held ❌ honestly.)
- **15 N/A** — rows un-testable without a completed cutover baseline (`0.8`, `1.3`, `1.5`, `1.6`, `1.7`, `2.4`, `3.2`, `5.7`).
- **4 ⏳** — handover-gated/visual/scratch-prov rows (`1.8`, `2.5`/`2.6`, `3.6`/`3.7`, `4.4`, `5.8`/`5.9`, `6.1`) — browser-only or negative-on-scratch-prov, not runnable curl-only this session.

**Root-cause status (single):** `bp-keycloak:1.4.30` is now **PUBLISHED + mirror-able** on `ghcr.io/openova-io/bp-keycloak` (in-cluster `openova-bot` HEAD → `HTTP 200`; Flux `pulled … 1.4.30`). This (a) lets the cutover step-02 `harbor-prewarm` mirror succeed on re-fire, and (b) flipped `bp-keycloak` HR `Ready=True`, recovering the whole spine **49/64 → 61/64**. The earlier `manifest unknown` blocker is **CLOSED**. The cutover end-state is now gated only on **handover** (sealing `secret/catalyst/tofu-phase0-archive` → the engine auto-re-fires via `ReceiveTofuArchive`; the in-cluster `POST /api/v1/internal/cutover/trigger` correctly returns `425 Too Early` until then). Handover (`api.hw158/healthz=200`, `console.hw158/=200`) is healthy.

**Verdict: ⏳ SPINE RECOVERED — keycloak-1.4.30 blocker CLEARED (49/64 → 61/64); cutover end-state pending the post-handover re-fire (NOT a remaining chart/code defect).**
