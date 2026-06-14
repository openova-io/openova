# #3379 SOVEREIGNTY — user acceptance walk (web UI)

**Honest framing:** the cutover (cutting the cord to the mothership) is a behind-the-scenes process the user never drives, so there is **almost no end-user UI**. The user-facing acceptance is the **negative** one — after the Sovereign goes independent, **nothing they touch breaks**. The cutover engine itself is verified from the operator side (handover finalise → 11-step engine → `cutoverComplete`).

## 2026-06-15 — hw139 (`c89aa7059556b342`, kom4dc 2-VPC, chart 0.1.60) — cutover wedged EARLIER, at step-03 harbor-prewarm (NEW keystone catch; NOT the hw136 step-08 wedge)

**Verdict: cutover did NOT complete zero-touch. Wedged at step-03 `harbor-prewarm` (18%).** This is an *earlier* wedge than the hw136 step-08 egress-block one — and the #3535 (skopeo `--insecure-policy`) + #3536 (step-05 `secretRef`) fixes in 0.1.60 are **both present and correct in the live ConfigMaps**, but the engine never reaches step-05/step-08 to exercise them.

The cutover was already fired on hw139 (handover done — operator session established as emrah.baysal@openova.io; tofu-phase0-archive sealed; `cutoverStartedAt 2026-06-14T19:08:49Z`). It ran step-01 → step-02 cleanly, then **harbor-prewarm timed out** and the engine has been permanently stuck there since. ZERO-TOUCH discipline observed — hw139 was **not** hand-patched to force the step.

| Step | Job | Result | One line |
|---|---|---|---|
| 01 gitea-mirror | `cutover-gitea-mirror-1781464129` | success | catalog mirrored to local Gitea (19:08:49 → 19:15:18) |
| 02 harbor-projects | `cutover-harbor-projects-1781464129` | success | openova-io/proxy-* projects created in local Harbor |
| 03 harbor-prewarm | `cutover-harbor-prewarm-1781464129` | **FAILED** | **DeadlineExceeded** (`activeDeadlineSeconds:1800`) — pushed only **6 of ~29** openova-io images via skopeo before the 30-min deadline killed it; slow github/ghcr egress on kom4dc |
| 04 registry-pivot | — | not started | engine halted at 03 |
| 05 flux-gitrepository-patch | — | not started | (#3536 secretRef fix present in the live CM but never executed) |
| 06 helmrepository-patches | — | not started | — |
| 07 catalyst-api-env-patch | — | not started | — |
| 08 egress-block-test | — | not started | **the 600s deny-egress hold never ran** |
| 09 gitea-token-mint | — | not started | — |
| 10 vcluster-registry-pivot | — | not started | — |
| 11 crossplane-provider-pivot | — | not started | — |

**The 4 completion proofs — all in their honest INCOMPLETE state (live kubectl):**
1. `cutoverComplete = false` (`self-sovereign-cutover-status` CM, `progressPercent=18 failedStep=harbor-prewarm`).
2. deny-egress CCNP `cutover-egress-block`: **absent** (`NotFound` — step-08 never reached).
3. `ghcr-pull` dockerconfigjson `auths` still keys **`ghcr.io`** in every ns (flux-system/catalyst-system/catalyst/dmz) — the registry-pivot Secret rewrite never ran.
4. Flux GitRepository `openova`: still `url=https://github.com/openova-io/openova` (not local Gitea), `secretRef=` empty — step-05 never reached.

**Root cause — two stacked defects (code-cited in `docs/sessions/2026-06-15/evidence/3379-cutover/06-ROOT-CAUSE-code-citations.txt`):**
- **CAUSE 1 (trigger):** `platform/self-sovereign-cutover/chart/templates/03-harbor-prewarm-job.yaml:52` `activeDeadlineSeconds: {{ .Values.stepTimeouts.harborPrewarmSeconds }}` = `1800` (values.yaml:513), `restartPolicy: Never`. Phase A enumerates every `ghcr.io/openova-io/*` image cluster-wide, downloads a 38MB skopeo binary from github (~120-360KB/s on kom4dc), then skopeo-copies ~29 multi-layer images ghcr.io→local Harbor. That does not fit in 30 min on this link → `reason: DeadlineExceeded`. (6 of 29 repos landed — proof it timed out mid-push, not a logical failure.)
- **CAUSE 2 (makes it unrecoverable zero-touch):** the resume-on-startup logic (`products/catalyst/bootstrap/api/internal/handler/cutover.go:1355-1388`) *intends* to re-run a failed step ("the failed step re-runs; if it passes this time, the cutover proceeds") but only resets `.result=="running"` rows — a `.result=="failed"` row is left as-is. `runCutover` (`:1053`) then doesn't skip it (not "success") and calls `runCutoverStep`, which via `findExistingTerminalJobForStep` (`:1121-1169`) **finds the stale Failed Job and refuses to re-create it** ("We DO NOT re-create on Failed … operator intervention is required"). The two functions contradict each other: resume wants to re-run, the idempotency check forbids it. The step is **never re-executed**, so EVERY zero-touch re-fire path (engine resume, handover re-POST `source="handover"`, operator CTA `source="operator"`) re-fails at step-03 identically. Witnessed live in the hw139 catalyst-api startup log: `cutover-resume: in-flight cutover detected` → `spawning runCutover to resume` → `cutover: step failed … carried over from prior cutover attempt`.

**Fix direction (follow-up issue, NOT a live patch):** (a) raise `harborPrewarmSeconds` for slow-egress provs and/or chunk the skopeo push; AND (b) make the resume/idempotency path treat a `DeadlineExceeded` Job as transient — delete it and re-run — while still halting on a genuine non-zero logical exit. Both 0.1.60 fixes (#3535/#3536) are correct; this is a distinct, earlier defect they do not cover.

Evidence: `docs/sessions/2026-06-15/evidence/3379-cutover/01..08`.

## 2026-06-14 — hw136 (`3a2ee904b1d0366a`, kom4dc 2-VPC) — cutover FIRED for the first time on a real Sovereign; halted at the egress-block fail-safe (NO half-pivot)

This is the first execution of the sovereignty cutover on a handed-over Sovereign. PR #3521 (catalyst-api `9de0b65`, in chart `bp-catalyst-platform` 1.4.629) fixed the root cause that previously made it **impossible** — the handover `tofu-archive` receiver was behind `RequireSession`, so on a real Sovereign the mother→child archive POST was 401'd before the handler and the cutover never auto-fired. #3519 flipped `egressTest.enforceCIDRBlock` default→true (bp-self-sovereign-cutover 0.1.57).

| Step | What was checked | Status | Evidence |
|---|---|---|---|
| Handover receiver reachable (#3521) | `POST api.hw136.omani.works/api/v1/handover/tofu-archive` (cookieless) → **400 "deploymentId and sovereignFqdn are required"** — handler reached (content validation). Before #3521 this was **401 "unauthenticated"** from the middleware, before the handler. | ✅ | `00-handover-receiver-reachable-400.json` |
| Handover finalise fires the cutover (#3521) | `POST console.openova.io/sovereign/api/v1/handover/finalise/3a2ee904b1d0366a` (RS256 owner JWT) → 200 with **`"tofuArchiveSubmitted": true`**. Before #3521: `false` + `errors:["submit tofu archive: status 401: unauthenticated"]`. Archive sealed → cutover auto-fired (`cutoverStartedAt 2026-06-14T10:05:13Z`). | ✅ | `01-handover-finalise-response.json` |
| Zero-click SSO into the console | `console.hw136.omani.works` → lands on `/dashboard` signed in as **emrah.baysal@openova.io** (`role: sovereign-admin`, roles `[catalyst-owner, catalyst-admin, sovereign-admins]`), no login form. Live treemap renders the primary region (`hw-me-east-215-a-rtz-prod`). | ✅ | `dashboard-zero-click-sso.png` |
| Cutover engine — steps 1–8 of 11 | gitea-mirror, harbor-projects, harbor-prewarm, registry-pivot, flux-gitrepository-patch (GitRepository `openova` → `http://gitea-http.gitea.svc.cluster.local:3000/openova/openova`), helmrepository-patches (kubectl-patched 39 live HelmRepos → local + git-pushed 60 rewritten files to local Gitea), catalyst-api-env-patch (rolled catalyst-api twice from the **local Harbor**, pivoted console/api URLs to local) — all **success**. | ✅ | `02-cutover-events-stream.txt`, `03-cutover-status-FAILED.json` |
| Cutover engine — egress-block-test (the deny-egress hold) | **FAILED at 10:17:08 (~74s, NOT the 600s survival window).** Its pre-flight URL assertion found **59 HelmRepositories reverted to `oci://ghcr.io/openova-io`** → `FAIL — at least one HelmRepository did not pivot`. The `cutover-egress-block` CiliumClusterwideNetworkPolicy was therefore **never applied** and the 600s hold never ran. (Root cause below.) | ❌ | `job-logs-raw.json` |
| `cutoverComplete` | **false.** Engine halted at `egress-block-test`; the post-egress steps (crossplane-provider-pivot, gitea-token-mint, vcluster-registry-pivot) never ran. | ❌ | `03-cutover-status-FAILED.json` |
| Safety — NOT half-pivoted | `state=tethered`, `registriesYamlActive=v1`, and console/api/registry/gitea/bao all 200/303 throughout and after the halt. The fail-safe assertion refused to enter the deny-egress hold while tethers remained, so hw136 stayed fully intact. | ✅ | `03-cutover-status-FAILED.json` |

**Root cause of the egress-block-test failure (a real cutover ordering defect, distinct from #3521):**
The `helmrepository-patches` step (succeeded 10:13:02) kubectl-patched the 39 live HelmRepositories to local AND git-pushed 60 rewritten files (ghcr→local) to local Gitea — but its own log notes "commit will reconcile via bootstrap-kit Kustomization", i.e. the durable Git-source rewrite is **asynchronous**. `egress-block-test` ran ~3 minutes later (10:15:54) and asserted **once**, with no wait/retry — by then a Flux reconcile had re-applied the OLD `oci://ghcr.io/openova-io` HelmRepository manifests (reverting the kubectl patches) and the bootstrap-kit Kustomization had not yet pulled the new local-pivot commit. So the assertion fails on the transient Flux-revert window. This is the documented kubectl-patch-then-Flux-reverts race (`feedback_cutover_bootstrap_halfpivot`). Fix direction: egress-block-test (08) should force-reconcile the bootstrap-kit Kustomization + GitRepository and **poll the HelmRepository pivot to convergence with a bounded timeout** before failing; or helmrepository-patches (06) should block on that convergence before returning success. Tracked in the follow-up issue.

**Verdict (hw136):** #3521 is **validated** — the cutover now fires on a real Sovereign for the first time, and 8/11 steps execute cleanly (including the registry pivot of catalyst-api itself from local Harbor). It then halts at the egress-block fail-safe on a real, separate ordering defect (HelmRepository pivot not yet reconciled when the assertion runs), with **no half-pivot**. `cutoverComplete` and the witnessed 600s deny-egress hold remain pending that fix + a re-run.

## 2026-06-14 (earlier) — hw133 baseline (cutover never run there)

Baseline only — at the time, the cutover had never been run on any handed-over environment. The user-facing surfaces all worked (console signed-in, Apps full catalog, grafana SSO, gitea from local services), but the post-cutover rows were unexecuted and there is no console UI/badge surfacing "this Sovereign is now independent".

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw133.omani.works](https://console.hw133.omani.works/) | After cutover, the console still loads and you are signed in, as before. **Baseline:** console loads signed-in as emrah.baysal (sovereign-admin), dashboard treemap renders 97 items. | ✅ | [![](../../sessions/2026-06-14/evidence/3379/r1.png)](../../sessions/2026-06-14/evidence/3379/r1.png) |
| [Apps](https://console.hw133.omani.works/apps) | Navigate Dashboard / Apps / Organizations → all render normally. Apps page renders Deployments (49) / Catalog (63). | ✅ | [![](../../sessions/2026-06-14/evidence/3379/r2.png)](../../sessions/2026-06-14/evidence/3379/r2.png) |
| [grafana.hw133.omani.works](https://grafana.hw133.omani.works/) | App still loads signed-in, unchanged. Grafana Home renders, SSO-authenticated (no login form). | ✅ | [![](../../sessions/2026-06-14/evidence/3379/r3.png)](../../sessions/2026-06-14/evidence/3379/r3.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | App works (served from the Sovereign's own local services). Gitea dashboard signed-in as `emrah.baysal`; Gitea v1.22.3. | ✅ | [![](../../sessions/2026-06-14/evidence/3379/r4.png)](../../sessions/2026-06-14/evidence/3379/r4.png) |
| [console.hw133.omani.works](https://console.hw133.omani.works/) | ❌ **GAP** — no UI page/badge shows "this Sovereign is now independent / cutover complete". | ❌ | [![](../../sessions/2026-06-14/evidence/3379/r5.png)](../../sessions/2026-06-14/evidence/3379/r5.png) |
| [Apps](https://console.hw133.omani.works/apps) | Install/update an app **after** cutover → pulls from the Sovereign's own local registry. **N/A on hw133** (no cutover run there). | ❌ | [![](../../sessions/2026-06-14/evidence/3379/r6.png)](../../sessions/2026-06-14/evidence/3379/r6.png) |

**Verdict (overall):** sovereignty cutover is an **ops process with almost no end-user UI** — the only positive UI acceptance is the negative one (nothing the user touches breaks), which holds. The operator-side engine now **fires** (#3521) and runs 8/11 steps on a real Sovereign; `cutoverComplete` + the 600s deny-egress hold are pending the egress-block ordering fix.
