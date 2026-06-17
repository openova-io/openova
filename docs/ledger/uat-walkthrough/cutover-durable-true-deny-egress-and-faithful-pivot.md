# UAT Walkthrough — SOVEREIGNTY: cutover earns `cutoverComplete=true` only via a durable, reconcile-immune fact and a TRUE deny-egress proof

## Status — last validated: hw158 (2026-06-17) — ❌ BLOCKED (spine wedged at cutover step-02)

- **Verdict: ❌ BLOCKED on hw158.** The cutover **DID auto-fire** at bootstrap (T0 `2026-06-17T06:24:04Z`) and **WEDGED + FAILED at step-02 `harbor-prewarm`**: the prewarm Job fatal'd with `initializing source docker://ghcr.io/openova-io/bp-keycloak:1.4.30: reading manifest 1.4.30 … manifest unknown` → `chart_ok=38 chart_fail=1` → `FATAL: 1 openova-io chart(s) failed to mirror — step-06 cannot strip ghcr.io safely`. Steps 03–11 never ran. The same missing `bp-keycloak:1.4.30` chart also keeps `bp-keycloak` HR `Ready=False` (`oci://ghcr.io/openova-io/bp-keycloak:1.4.30: not found`), which cascades `Ready=False` down `bp-gitea → bp-catalyst-platform → bp-continuum` and to `grafana/guacamole/sso-bridge/oidc-gate/powerdns-admin/newapi/sandbox`. **The spine is STILL WEDGED, not recovering** — as of this walk `bp-keycloak:1.4.30` is **not published on ghcr** (Flux source-controller HelmChart pins `spec.version=1.4.30` but `manifest unknown`), so PR #3750's test-fix merge has **not yet produced a 1.4.30 chart artifact**. HR currently serves the prior `bp-keycloak@1.4.29` install.
- **Handover is HEALTHY and is NOT the blocker:** `https://api.hw158.omani.works/healthz` → `HTTP 200`; `https://console.hw158.omani.works/` → `HTTP 200`. The blocker is purely the unpublished keycloak chart wedging the prewarm gate.
- **#3681 audit-fidelity structure IS present:** `cutoverStartedAt == step.gitea-mirror.startedAt == 2026-06-17T06:24:04Z` (top-level T0 equals the first step — no 24-min corruption).
- **Tally: 3 ✅ (Section 0 reproduced the live wedge) · 22 ❌ (POST-FIX rows unreachable — cutover wedged at step-02) · 15 N/A · 4 ⏳ (handover-gated / visual / negative-on-scratch-prov).**
- **Maps to:** [`../UAT.md`](../UAT.md) "What is NOT green on hw158" — cutover wedged at step-02 (`bp-keycloak:1.4.30` unpublished).
- **Evidence:** inline `kubectl` captures below (this session, hw158, kubeconfig `/tmp/hw158-kc.yaml`).
- **What's needed to unblock:** publish `bp-keycloak:1.4.30` to `ghcr.io/openova-io/bp-keycloak` (the CI release for PR #3750), then let Flux re-pull → keycloak HR Ready → re-fire cutover → prewarm passes → walk Sections 1–6. Until 1.4.30 lands, NO cutover step past step-02 can run.
- **Index:** [`README.md`](README.md). Prior-env (hw150-train) evidence is void per the each-new-env-flushes-evidence rule.

**Issue:** #3379
**Slug:** `cutover-durable-true-deny-egress-and-faithful-pivot`
**Pillar:** 5 (Sovereign independence) · ADR-0002 · Principle #11 · DoD §7
**Format:** every row is ONE operator action. `☐` is flipped by the founder doing the click and seeing the result. Below, ☑ rows carry this session's `kubectl` capture.
**Env under test:** the current converged Sovereign (`console.<fqdn>` — current = `console.hw158.omani.works`), kubeconfig at `/tmp/hw158-kc.yaml`.

> **Acceptance rule (founder, 2026-06-03):** this is a click-by-click UI/CLI walk, not a unit test. The automated `go test` rows live in Appendix A and are explicitly **NOT acceptance** — they only pre-stage the source-level guards. Acceptance = the founder walking the numbered rows below on a FRESH prov and seeing each "You should see" outcome.

> **Why this doc exists (the false-proof thesis):** on `hw150` the 11 cutover steps ran green and `cutoverComplete=true` was set, yet the cluster was (a) re-armable to re-fire the whole 600s hold by a routine `helm upgrade` (#3667), (b) still pointing node containerd at the MOTHERSHIP Harbor because `registriesYamlActive` was never flipped to `v2` (#3671), (c) certified by a "deny-egress" that only blocked 4 CIDRs and left `console.openova.io` + ~18 tethers reachable (#3678), (d) carrying a corrupted audit start-time stamped 24 min after the first step (#3681), and (e) running a `bp-sso-bridge` reconciler pod on `harbor.openova.io` while its own host GitOps loop (`bootstrap-kit`) was `BuildFailed` from a duplicate `values:` key the cutover itself injected (#3695). Every section below proves one of those five faces is now closed.

---

## LIVE STATE SNAPSHOT (hw158, this session)

```
$ kubectl get cm self-sovereign-cutover-status -n catalyst -o yaml   (key fields)
  cutoverComplete:                "false"
  failedStep:                     harbor-prewarm
  lastError:                      Job catalyst/cutover-harbor-prewarm-1781677444 reported Failed condition
  currentStepIndex:               "2"      progressPercent: "18"      totalSteps: "11"
  registriesYamlActive:           v1
  cutoverStartedAt:               2026-06-17T06:24:04Z
  cutoverLastAttemptStartedAt:    2026-06-17T06:24:04Z
  step.gitea-mirror.result:       success    (startedAt 06:24:04Z, finishedAt 06:27:11Z)
  step.harbor-projects.result:    success    (06:27:11Z → 06:27:25Z)
  step.harbor-prewarm.result:     failed     (06:27:25Z → 06:55:56Z)
  step.registry-pivot.result:     ""         (never ran)
  step.egress-block-test.result:  ""         (never ran)
  node.<4 nodes>.registriesYaml:  v1   (all 4 nodes — pivot never happened)

$ kubectl logs job/cutover-harbor-prewarm-1781677444 (tail)
  [harbor-prewarm] OK chart bp-velero:1.2.2 / bp-vpa:1.0.3 ...
  time="…06:54:05Z" level=fatal msg="initializing source docker://ghcr.io/openova-io/bp-keycloak:1.4.30:
                       reading manifest 1.4.30 in ghcr.io/openova-io/bp-keycloak: manifest unknown"
  [harbor-prewarm] FAIL chart bp-keycloak:1.4.30 exit=1
  [harbor-prewarm] Phase A2 complete: chart_ok=38 chart_fail=1
  [harbor-prewarm] FATAL: 1 openova-io chart(s) failed to mirror — step-06 cannot strip ghcr.io safely

$ kubectl get helmcharts.source.toolkit.fluxcd.io -n flux-system flux-system-bp-keycloak
  spec.version=1.4.30
  Ready=False :: chart pull error … 'oci://ghcr.io/openova-io/bp-keycloak:1.4.30': … not found
  ArtifactInStorage=True :: pulled 'bp-keycloak' chart with version '1.4.29'   ← still serving 1.4.29

$ kubectl get secret cutover-complete -n catalyst                 → NotFound
$ kubectl get ccnp cutover-egress-block                           → NotFound
$ curl -sk https://api.hw158.omani.works/healthz                  → HTTP 200   (handover healthy)
$ curl -sk https://console.hw158.omani.works/                     → HTTP 200
```

---

## Section 0 — Pre-state: reproduce the false proof on the CURRENT env

These rows DOCUMENT the gap. On hw158 the cutover never reached `cutoverComplete=true` at all (wedged at step-02), so 0.2 differs from the hw150 "false proof" baseline — but 0.3/0.4 (the registry-pivot-pivoted-nothing + no-durable-seal symptoms) reproduce exactly, and 0.5 (audit fidelity) shows the #3681 fix is in place.

| # | Do | Expected (hw150 baseline) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 0.1 | `export KUBECONFIG=/tmp/hw158-kc.yaml` | prompt ready | prompt ready | ✅ |
| 0.2 | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | `true` | **`false`** — cutover never completed; it FAILED at `failedStep=harbor-prewarm` (worse than hw150's false-green) | ❌ (failed-state seen) |
| 0.3 | `…jsonpath='{.data.registriesYamlActive}'` | `v1` (#3671) | **`v1`** — all 4 `node.*.registriesYaml=v1`; registry-pivot never ran | ✅ (reproduced) |
| 0.4 | `kubectl get secret cutover-complete -n catalyst` | `NotFound` (#3667) | **`NotFound`** (`Error … secrets "cutover-complete" not found`) | ✅ (reproduced) |
| 0.5 | `…{.data.cutoverStartedAt}…{.data.step\.gitea-mirror\.startedAt}` | LATER than first step (#3681 corruption) | **EQUAL** — both `2026-06-17T06:24:04Z`. The #3681 corruption is GONE; audit start == first-step start | ✅ (fix present — better than baseline) |
| 0.6 | `kubectl get kustomization -n flux-system bootstrap-kit` | `READY=False post build failed … "values" already defined` (#3695) | `Ready=Unknown :: Reconciliation in progress` — running health checks for rev `f3a1c2f…`, blocked on the wedged HRs (NOT the #3695 duplicate-`values:` BuildFailed; that BuildFailed is not present) | ❌ (different failure — spine-wedge, not the #3695 shape) |
| 0.7 | `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | `harbor.openova.io/proxy-dockerhub/alpine/k8s:…` (#3695) | **`harbor.openova.io/proxy-dockerhub/alpine/k8s:1.30.0`** — live MOTHERSHIP tether (cutover never re-keyed it) | ✅ (reproduced) |
| 0.8 | `kubectl get ccnp cutover-egress-block` (if a hold is live) | 4-CIDR subtractive (#3678) | **`NotFound`** — no hold is live; cutover wedged at step-02, step-08 egress-block never ran. Cannot inspect a non-existent CCNP | N/A (step-08 never reached) |

---

## Section 1 — DURABILITY: a `helm upgrade` cannot revert the flag or re-fire the cutover (#3667)

**Goal:** `cutoverComplete=true` is backed by a sealed OpenBao secret the chart's upgrade CANNOT revert.

**BLOCKED:** every row here presupposes a COMPLETED cutover (a sealed `cutover-complete` secret to test durability against). On hw158 the cutover FAILED at step-02, so there is nothing durable to perturb. Re-walk after `bp-keycloak:1.4.30` publishes and the cutover completes.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 1.1 | `kubectl get secret cutover-complete -n catalyst` | sealed secret EXISTS | `NotFound` — cutover never reached its success tail (failed at step-02) | ❌ |
| 1.2 | snapshot `…-l cutover.openova.io/run-epoch -o name` | a list captured | `kubectl get jobs … -l cutover.openova.io/run-epoch` returns EMPTY — current cutover Jobs are NOT labelled with `run-epoch` (they are `cutover-gitea-mirror/harbor-projects/harbor-prewarm-1781677444`); the durability-epoch labelling is not on this chart rev (0.1.75) | ❌ (label absent) |
| 1.3 | `flux reconcile hr bp-self-sovereign-cutover … --with-source` | reconciliation finished | not run — HR is `Ready=False` (`dependency 'flux-system/bp-gitea' is not ready`); forcing the upgrade path is meaningless while the dep chain is wedged | N/A |
| 1.4 | `…{.data.cutoverComplete}` STILL `true` | re-derived `true` from seal | currently `false`; no seal to re-derive from | ❌ |
| 1.5 | diff jobs before/after — NO new cutover Jobs | empty diff | un-testable (no completed baseline) | N/A |
| 1.6 | `kubectl get ccnp cutover-egress-block` | `NotFound` | `NotFound` — but vacuously (step-08 never ran), not because an upgrade declined to re-apply it | N/A (vacuous) |
| 1.7 | restart catalyst-api → log "already complete (sealed); nothing to resume" | resume hook no-ops | not run — no sealed-complete state exists; restarting would attempt to RESUME the failed step-02, not no-op | N/A |
| 1.8 | console → Settings → Sovereignty — steady "Sovereign — tethers severed", CTA hidden | sovereign state | curl-only this session (no browser per constraints); and the card would NOT show "tethers severed" — cutover is incomplete | ⏳ (visual + not-applicable-state) |

---

## Section 2 — FAITHFUL REGISTRY PIVOT: `registriesYamlActive=v2` flips node containerd to LOCAL Harbor (#3671)

**Goal:** the engine sets `registriesYamlActive=v2` after `harbor-prewarm` succeeds.

**BLOCKED at the precondition:** `harbor-prewarm` (step-02) **FAILED**, so step-03/04 (registry-pivot) never ran. `registriesYamlActive` is `v1` on all 4 nodes — exactly the unflipped state.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 2.1 | `…{.data.registriesYamlActive}` | `v2` | **`v1`** — prewarm failed, pivot never fired | ❌ |
| 2.2 | `…{.data.step\.registry-pivot\.result}` == `success` + per-node ack count | success | **empty** (`step.registry-pivot.result=""`, `startedAt=""`) — never ran | ❌ |
| 2.3 | exec registry-pivot DS pod → `cat /host/etc/rancher/k3s/registries.yaml` rewrites to `harbor.<fqdn>` | local Harbor | no registry-pivot DS exists (step never ran) | ❌ |
| 2.4 | with egress denied, pull a non-openova image via local Harbor | pull SUCCEEDS | un-testable — no pivot, no deny-egress hold | N/A |
| 2.5 | console /jobs step-04 row "N/N nodes at v2" | green row | curl-only; and step-04 never ran | ⏳ (visual) |
| 2.6 | NEGATIVE on a scratch prov — hold one node, step-04 stays red | real verification | scratch-prov negative test, not runnable on this live env | ⏳ (scratch-prov) |

---

## Section 3 — TRUE DENY-EGRESS: the hold blocks the mothership broadly, not 4 CIDRs (#3678)

**Goal:** the 600s hold is a default-deny-egress with explicit intra-cluster allow-CIDRs.

**BLOCKED:** step-08 (`egress-block-test`) is step index 8 of 11; the run died at index 2. The `cutover-egress-block` CCNP does not exist; no hold ever held.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 3.1 | during step-08 hold → `curl https://console.openova.io/healthz` FAILS | connection blocked | no hold ever fired (run died at step-02) — cannot probe a non-existent window | ❌ (step never reached) |
| 3.2 | `curl https://kubernetes.default.svc` SUCCEEDS (no #3640 strand) | apiserver reachable | un-testable (no hold); apiserver is reachable generally (cluster is up) | N/A |
| 3.3 | `kubectl get ccnp cutover-egress-block -o yaml` → `enableDefaultDeny.egress: true` + intra-cluster allow set | default-deny-with-allow | **`NotFound`** — CCNP absent | ❌ |
| 3.4 | step-08 log `grep call-home` → "call-home denied + verified" | active assertion | no step-08 Job exists | ❌ |
| 3.5 | `kubectl get secret ghcr-pull -n flux-system … base64 -d` keys `harbor.<fqdn>`, NOT `ghcr.io` | tether #5 re-keyed | **keys `"ghcr.io"`** — still the pre-cutover mothership key (cutover never re-keyed it; correct for a pre-cutover state but proves the pivot did NOT happen) | ❌ (still ghcr.io) |
| 3.6 | scratch-prov generality — arbitrary mothership host caught | proof catches it | scratch-prov negative, not runnable here | ⏳ (scratch-prov) |
| 3.7 | console /jobs step-08 row reflects mothership-wide block | UI row | curl-only; step-08 never ran | ⏳ (visual) |

---

## Section 4 — AUDIT FIDELITY: `cutoverStartedAt` is the true T0 and is never re-stamped on resume (#3681)

**Goal:** `cutoverStartedAt` written ONCE; `cutoverLastAttemptStartedAt` carries each resume. The #3681 structure IS PRESENT and correct on hw158 (this is the one section with a real PASS), but the full resume-mid-run walk (4.1) requires a running cutover that progresses, which the step-02 wedge prevents.

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 4.1 | fire cutover; restart catalyst-api MID-RUN during harbor-prewarm; resume continues from last step | resume re-enters | not actively performed (cutover is already failed/parked at step-02; a restart would re-attempt the same doomed prewarm vs 1.4.30) | ❌ (cannot exercise resume — wedged) |
| 4.2 | `cutoverStartedAt` == EARLIEST `step.*.startedAt` | equal within seconds | **EQUAL** — `cutoverStartedAt=2026-06-17T06:24:04Z` == `step.gitea-mirror.startedAt=2026-06-17T06:24:04Z`. The #3681 corruption (24-min-later stamp) is FIXED | ✅ |
| 4.3 | `cutoverLastAttemptStartedAt` == restart moment, recorded separately | separate field | field EXISTS and is populated: `cutoverLastAttemptStartedAt=2026-06-17T06:24:04Z` (equals T0 because there has been no resume yet — the field is wired and present) | ✅ (field present, structurally correct) |
| 4.4 | console Sovereignty "Started <true T0>" + real elapsed | true window | curl-only this session | ⏳ (visual) |

---

## Section 5 — HOST GITOPS LOOP SURVIVES + zero residual mothership tether (#3695)

**Goal:** step-10 image-host injection merges into the existing `values:` mapping; `bootstrap-kit` reconciles Ready post-cutover; NO live pod references a mothership registry.

**BLOCKED:** step-10 is far past the step-02 wedge. The #3695 duplicate-`values:` BuildFailed is NOT reproduced (bootstrap-kit is `Unknown`/health-checking, not BuildFailed), but post-cutover Readiness and zero-residual-tether can't be asserted because the cutover didn't run. Critically, **mothership tethers are STILL LIVE** (correct for a pre-cutover env, but proves Section 5's end-state is not met).

| # | Do | Expected (POST-FIX) | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 5.1 | `kubectl get kustomization -n flux-system` all READY at post-cutover rev | all Ready | `bootstrap-kit Unknown` (reconciling rev `f3a1c2f`), `sovereign-tls False` (dep bootstrap-kit not ready), `sme-tenants False` (Source artifact not found), `catalog-sovereign True`. NOT at a post-cutover revision | ❌ |
| 5.2 | `bootstrap-kit … lastAppliedRevision` == latest cutover commit on local Gitea | pivot commits applied | `lastApplied=main@sha1:27e99a1f…`, `lastAttempted=main@sha1:f3a1c2f6…` — applied ≠ attempted (mid-reconcile); NOT a cutover commit | ❌ |
| 5.3 | `bp-sso-bridge … spec.values` `global.registryMirror=harbor.<fqdn>`; exactly ONE `values:` key | one values key | bp-sso-bridge HR is `Ready=False` (`dependency 'flux-system/bp-keycloak' is not ready`); not pivoted | ❌ |
| 5.4 | `kubectl get pods -n sso-bridge -o jsonpath='…image'` → `harbor.<fqdn>/…`, NOT `harbor.openova.io` | local Harbor | **`harbor.openova.io/proxy-dockerhub/alpine/k8s:1.30.0`** — MOTHERSHIP tether live | ❌ |
| 5.5 | cluster-wide pod-image grep for `harbor.openova.io\|ghcr.io/openova-io\|github.com/openova` is EMPTY | no mothership pulls | **NON-EMPTY** — e.g. `ghcr.io/openova-io/openova/catalyst-api:4b0a9c6`, `…/console:3c2f7e4`, `…/continuum-controller:aaa6006`, plus sso-bridge on `harbor.openova.io`. Many live mothership tethers (expected pre-cutover) | ❌ |
| 5.6 | push trivial commit to local Gitea → bootstrap-kit reconciles green | loop alive sans mothership | not exercised — bootstrap-kit is not at a stable Ready state to demonstrate independence | ❌ |
| 5.7 | re-fire cutover (idempotency) → `13b-bp-sso-bridge.yaml` still ONE `values:` key | no BuildFailed | re-fire would re-hit the step-02 prewarm wall vs 1.4.30; idempotency of step-10 injection not reachable | N/A |
| 5.8 | scratch-prov generality — deliberate residual HR makes proof FAIL | generic gate | scratch-prov negative, not runnable here | ⏳ (scratch-prov) |
| 5.9 | console Sovereignty step-08 per-workload pass/fail panel | UI panel | curl-only; step-08 never ran | ⏳ (visual) |

---

## Section 6 — END-TO-END: a cut-over Sovereign onboards a customer with ZERO mothership dependency

**Goal:** the corrected cutover leaves a Sovereign that is independent AND still works.

**BLOCKED:** 6.1 partially holds (console is up, 200), but 6.2 onward requires running the cutover to completion, which is wedged at step-02.

| # | Do | Expected | OBSERVED on hw158 | Verdict |
|---|---|---|---|---|
| 6.1 | bare `https://console.<fqdn>` → land `/dashboard` signed-in | dashboard, signed-in admin | `console.hw158.omani.works/` → HTTP 200 (reachable); signed-in landing is a browser/visual check not done curl-only | ⏳ (visual; endpoint up) |
| 6.2 | console Sovereignty → "Achieve True Sovereignty" → steps 01-11 | progress card | cutover ALREADY auto-fired and is parked FAILED at step-02; CTA path not exercisable to green | ❌ |
| 6.3 | wait → `cutoverComplete=true` only after all 11 steps + hold + Ready gate | true completion | `cutoverComplete=false`, parked at step-02 | ❌ |
| 6.4 | push to local Gitea → bootstrap-kit reconciles green | loop alive | bootstrap-kit not at stable Ready (see 5.1) | ❌ |
| 6.5 | BSS Vouchers → redeem → Organization materializes | tenant home | depends on a healthy host Flux loop; sme-tenants is `False` (Source artifact not found) — not demonstrable | ❌ |
| 6.6 | black-hole github/ghcr/harbor.openova.io → Sovereign keeps serving | independence | live pods still pull from `ghcr.io/openova-io` + `harbor.openova.io` (5.5) → black-holing would break pulls. Not independent pre-cutover | ❌ |

---

## Appendix A — Automated guards (NOT acceptance; source-level pre-stage only)

These `go test` / render-test rows protect the fix at source; they are evidence the guard exists, **not** the founder walk. Not executed in this live walk (acceptance is the live rows above).

| # | Command | Pass condition |
|---|---|---|
| A.1 | `go test -race ./products/catalyst/bootstrap/api/internal/handler/ -run Cutover` | green; the new tests pass |
| A.2 | two-call `runCutover` test | `cutoverStartedAt` byte-identical across calls; `cutoverLastAttemptStartedAt` advances (#3681) |
| A.3 | sealed-fact gate test | `spawnCutoverEngine` + `ResumeInterruptedCutover` no-op when `secret/catalyst/cutover-complete` is sealed, even with the CM reverted to `false` (#3667) |
| A.4 | render test on `09-cutover-status-configmap.yaml` | a `helm upgrade` does NOT emit `cutoverComplete:"false"` / `cutoverStartedAt:""` / `registriesYamlActive:"v1"` over a live state (#3667) |
| A.5 | injector unit test (step-10) | feed `13b-bp-sso-bridge.yaml` ALREADY carrying a `values:` block → output parses as valid single-`values:` YAML (#3695) |
| A.6 | cutover invariant test | `runCutover` cannot reach `cutoverComplete=true` while `registriesYamlActive != "v2"` (#3671) |
| A.7 | render test on `08-egress-block-test-job.yaml` | emitted CCNP is default-deny-with-allow-list, not a 4-CIDR subtractive block (#3678) |

---

## Evidence ledger

On a fresh prov, every `☐` above gets a screenshot or `kubectl` capture committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`. `cutoverComplete=true` is admissible ONLY when Sections 1-6 are all green on the SAME env (per memory: each new env flushes all prior evidence — re-prove live).

### hw158 walk (2026-06-17) — Tally

- **3 ✅** — `0.1` (shell), `0.3`/`0.4`/`0.7` reproduced the live wedge symptoms, `0.5`/`4.2`/`4.3` confirm the #3681 audit-fidelity fix IS present (`cutoverStartedAt == step.gitea-mirror.startedAt == 06:24:04Z`; `cutoverLastAttemptStartedAt` field wired). (Counted as 3 distinct verified-PASS facts: 0.1, 0.5/4.2, 4.3; the 0.3/0.4/0.7 are "✅-reproduced-gap" markers on Section-0 documentation rows.)
- **22 ❌** — every POST-FIX row in Sections 1-6 that is reachable-but-unmet because the cutover is parked FAILED at step-02 (`0.2`, `1.1`, `1.2`, `1.4`, `2.1`, `2.2`, `2.3`, `3.1`, `3.3`, `3.4`, `3.5`, `4.1`, `5.1`, `5.2`, `5.3`, `5.4`, `5.5`, `5.6`, `6.2`, `6.3`, `6.4`, `6.5`, `6.6` — and `0.6` differs from baseline). 
- **15 N/A** — rows un-testable without a completed cutover baseline (`0.8`, `1.3`, `1.5`, `1.6`, `1.7`, `2.4`, `3.2`, `5.7`) or scratch-prov negatives folded below.
- **4 ⏳** — handover-gated/visual/scratch-prov rows (`1.8`, `2.5`/`2.6`, `3.6`/`3.7`, `4.4`, `5.8`/`5.9`, `6.1` — browser-only or negative-on-scratch-prov, not runnable curl-only this session).

**Root cause (single):** `bp-keycloak:1.4.30` is pinned by the chart/HelmRepository but is **not published on `ghcr.io/openova-io/bp-keycloak`** (`manifest unknown`). This (a) fails the cutover step-02 `harbor-prewarm` mirror (`chart_fail=1`), and (b) keeps `bp-keycloak` HR `Ready=False`, cascading the whole spine. **Handover (`api.hw158/healthz=200`, `console.hw158/=200`) is healthy and is NOT the blocker.** Unblock = ship the `bp-keycloak:1.4.30` chart artifact (PR #3750's release), then re-fire cutover.

**Verdict: ❌ BLOCKED — cutover wedged at step-02 (`bp-keycloak:1.4.30` unpublished). Spine STILL wedged, not recovering, as of this walk.**
