# One honest /jobs canvas — ingestion + faithful read model + remediation — UAT walk

## Status — last validated: hw158 (2026-06-17) — 🟡 WALKED, PARTIAL (curl/kubectl honest re-walk)

- **Tally (this honest re-walk, curl+kubectl only, NO browser):** **18 ✅ · 6 ❌ · 6 N/A · 5 ⏳ VISUAL-PENDING** (41 numbered steps + sign-in).
- **POST-FIX RE-CHECK (2026-06-17, #3756): NOT yet propagated to hw158 — cron rows STAY ❌.** #3756 adds get/list/watch on `cronjobs` + `kustomizations` to the `catalyst-api-cutover-driver` ClusterRole (the RBAC the canvas needs to ingest CronJobs as `cron-*` rows). **Live on hw158 the grant is ABSENT:** `kubectl get clusterrole catalyst-api-cutover-driver -o jsonpath='{.rules}'` shows `batch/cronjobs` with only `[update,patch,delete,create]` — **no `get/list/watch`** — and **no `kustomizations` resource at all**. So the canvas still cannot list CronJobs; the failing `openbao-snapshot-save` CronJob (last run `openbao-snapshot-save-29694740 Failed 0/1`, pod log `FATAL: bao login returned no client_token`) remains an invisible (no `cron-*` row). Rows **1.2 / 1.3 / 1.7-cron / 5.1-cron stay ❌**, **1.4 / 1.5 / 3.5 / 3.6 stay N/A**, until the bp-catalyst-platform release carrying #3756 reconciles onto the env. (The separate FE `jobName` retry fix is #3646, not in this propagation window.)
- **Headline verdict: 🟡 PARTIAL.** The **typed read-model core of #3646 is PROVEN LIVE** on hw158 (image `4b0a9c6`): the canvas backing model `GET /api/v1/deployments/ab2135d4cf2d01e4/jobs` returns **170 rows, ALL typed (`missingKind:0`)** across 5 server-typed kinds + `group` parents, with **honest statuses never-ahead-of-reality** (cutover NOT premature-Succeeded; `reconcilers` group `failed` from real failed children; the `reconciler` health round-trips `healthy→degraded→healthy` under live perturbation), and a **working Flux-native remediation backend** (bare-name retry → **200**, writes the `reconcile.fluxcd.io/requestedAt` annotation; the FE composite-id form → **404** — the documented defect, reproduced live). **But two contract claims in the prior writeup do NOT hold on this env and are now marked ❌:**
  - **❌ The CronJob ingestion is ABSENT.** The model has **zero `cron`-kind rows**. The `openbao-snapshot-save` CronJob exists in-cluster and its last two runs **Failed (0/1)**, yet there is **no `cron-openbao-snapshot-save` row** on the canvas — the exact "invisible failing CronJob" the §1 narrative claimed was fixed is still invisible here. (§1.2–1.5, §1.7-cron → ❌/N-A.)
  - **❌ The prior writeup said the `cutover` group is `pending`.** Live, the `cutover` **group is `failed`** (step `cutover-step-harbor-prewarm` failed; 2 steps succeeded, 8 pending). This is still HONEST (no premature green — the core anti-pattern stays fixed) but it is **not** the "all-pending" state the prior doc asserted, so the literal claim is corrected.
- **Env caveat (load-bearing):** **hw158 is NOT fully converged.** `bp-keycloak` HR is wedged on a chart-pull 404 (`oci://ghcr.io/openova-io/bp-keycloak:1.4.30: not found`), which cascades `bp-gitea`/`bp-grafana`/`bp-guacamole`/`bp-sso-bridge`/`bp-self-sovereign-cutover` to `dependency not ready`. That non-convergence is *why* the cutover steps + harbor-prewarm are red and several installs are mid-flight — the canvas faithfully reflects this degraded reality, which is exactly the point of the honest read-model, but it means this is a degraded-env walk, not a green-env walk.
- **THE DEFECT (FE button wiring) — confirmed at source + live:** `JobsTable.tsx:799` passes `jobId={job.id}` (the composite `{depId}:{jobName}`) into `RetryJobButton.tsx:62`, which `encodeURIComponent`s it (`:` → `%3A`) → the proxy 404s `job-not-found`. Live proof: bare `…/jobs/task-scan-vulnerabilityreport-5946b4b779/retry` → **200**; composite `…/jobs/ab2135d4cf2d01e4%3Atask-scan-…/retry` → **404**. The backend store dual-accepts composite-or-bare (`store.go` comment cites the proxy-colon issue), so the bare call works. **Fix:** send `job.jobName`, not `job.id`. Tracked under **#3646**.
- **Maps to:** no direct [`../UAT.md`](../UAT.md) row.
- **Evidence:** this walk is curl+kubectl inline below (no screenshots captured — browser-rendered rows are ⏳ VISUAL-PENDING).
- **What's needed to close clean:** (1) the one-line FE fix (send `jobName`); (2) wire CronJob (`cron-*`) ingestion so a failing `openbao-snapshot-save` surfaces as a red row; (3) re-walk on a CONVERGED env (keycloak chart pin fixed) so the cutover/handover steps run rather than wedge.
- **Index:** [`README.md`](README.md). Prior-env (hw150) evidence is void.

> **Ticket:** [#3646](https://github.com/openova-io/openova/issues/3646) · **Folds:** #3665 (ingestion breadth) · #3674 (faithful read model / no fabrication) · #3670 (remediation) · **Builds on:** PR #3652 (cutover projection)
>
> **What this proves (founder #5):** the `/jobs` canvas is **ONE honest reconciliation view** — it ingests HelmReleases + child Jobs + reconciler Deployments + the 11-step cutover, renders them from **one backend list with a typed `kind`** (no client-side 4-feed mashup, no fabricated statuses), shows status **never ahead of reality**, and lets the operator **remediate** Failed rows from the UI without dropping to `kubectl`. **Gap on this env:** CronJob ingestion is not yet wired (failing CronJobs invisible).
>
> **Walk method (this honest re-walk):** curl/kubectl/grep ONLY, NO browser. Auth: minted a fresh handover JWT (signed with the mothership `/tmp/handover-jwt-private.pem`, whose public half matches hw158's trusted `catalyst-handover-jwt-public` JWK), exchanged at `/auth/handover` for the `catalyst_session` cookie (302 → `/dashboard`, no password). Live image = `ghcr.io/openova-io/openova/catalyst-api:4b0a9c6`, which registers `POST .../jobs/{jobId}/retry → h.RetryJob` and ships `jobs_retry.go`. `kubectl`/`curl`/`grep` are ground-truth; rendered-canvas rows a headless curl cannot prove are marked **⏳ VISUAL-PENDING**.

**Sign-in (once).** Go to `https://console.<fqdn>/auth/handover?token=<JWT>` → you land on `/dashboard` signed in as the sovereign-admin, zero password fields. ✅
`curl -sSk -c cookies "https://console.hw158.omani.works/auth/handover?token=<fresh-JWT>"` → **HTTP 302  location: /dashboard**, `set-cookie: catalyst_session=…`, `catalyst_refresh` cleared (Max-Age=0). No login form. **✅** (token minted with mothership signer, validated by hw158's mounted public JWK).

---

## Section 1 — Ingestion breadth: recurring/child/reconciler activities are VISIBLE (folds #3665)

*The install row is green; the real work behind it is failing. The canvas must show the failure.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 1.1 | `/jobs` | scroll/search to the `install-openbao` row | it is **green / Succeeded** | ✅ |
| 1.2 | `/jobs` | Status filter **failed** | a **RED `cron-openbao-snapshot-save`** row appears in the same table | ❌ |
| 1.3 | `/jobs` | confirm both rows in one viewport | green install + red cron side by side | ❌ |
| 1.4 | `/jobs` | click the `cron-openbao-snapshot-save` row | detail page with an Executions list | N/A |
| 1.5 | the cron detail page | read the latest Execution's log | the `FATAL: bao login returned no client_token …` line | N/A |
| 1.6 | `/jobs` | find `task-cnpg-pair-...-primary-3-join` | the stuck HA-join child Job appears as a `task-*` row in **running** | ❌ |
| 1.7 | `/jobs` | set the **Kind** filter to `cron` then `task` | every recurring + child activity is listed | ❌ (cron) / ✅ (task) |
| 1.8 | `/jobs` | find `reconciler-sso-bridge` (Kind=`reconciler`) | shows **health** (`healthy`), NOT a one-shot "Succeeded" | ✅ |
| 1.9 | terminal | `kubectl scale deploy/sso-bridge-reconciler -n sso-bridge --replicas=0` | (perturb) | ✅ |
| 1.10 | `/jobs` | re-read `reconciler-sso-bridge` | health flips **healthy → degraded** | ✅ |
| 1.11 | terminal | scale back `--replicas=1`; refresh | health flips back **degraded → healthy** | ✅ |
| 1.12 | **[xcheck]** terminal | `kubectl get cronjobs,jobs,kustomizations -A | grep -i fail` ↔ red rows | one-to-one, no silent green | ⚠️ PARTIAL |

**1.1 — ✅.** `GET /api/v1/deployments/ab2135d4cf2d01e4/jobs` (HTTP 200, 170 rows). The `install-openbao` row carries `status=succeeded` (also `install-me-east-215-b-1:openbao` = succeeded). The chart-install IS green. **SEEN.**

**1.2 — ❌.** The model has **zero rows with `kind:"cron"`** and **no `cron-openbao-snapshot-save` row** at all. Filtering the 170-row payload for `cron` / `openbao-snapshot`:
```
$ python3 -c '…d["jobs"]… kind=="cron"' → cron-kind rows: 0  []
openbao rows present: install-openbao (install/succeeded), install-me-east-215-b-1:openbao (install/succeeded)  # NO cron row
```
The failing DR-backup CronJob is **NOT** surfaced as a row. The "invisible failing CronJob" the §1 narrative says is fixed is **still invisible on this build/env**. **❌.**

**1.3 — ❌.** Cannot show "green install + red cron side by side" — the red cron row does not exist (see 1.2). **❌.**

**1.4 / 1.5 — N/A.** No `cron-openbao-snapshot-save` row exists to click; the detail page + the `FATAL: bao login …` log line are unreachable via this canvas. (Ground-truth: the CronJob's failure DOES exist — `kubectl get jobs -n openbao` shows `openbao-snapshot-save-29694660 Failed 0/1` — it is just not projected as a canvas row.) **N/A (row absent).**

**1.6 — ❌ (different live reality).** No `…-primary-3-join` row; the cnpg join child IS modelled as a `task-*` row, but it has **converged**, not stuck-running:
```
model: task-cnpg-pair-bp-cnpg-pair-primary-2-join → kind=task status=succeeded
kubectl: cnpg-pair-bp-cnpg-pair-primary-2-join  Complete 1/1  (also primary-1-initdb Complete)
```
The child-Job ingestion works (the row exists), but the specific "stuck primary-3-join in running" state the step asserts is not present (HA join completed). Marking ❌ against the literal expectation; the underlying capability (child Jobs as `task` rows) is ✅ and covered by 1.7.

**1.7 — ❌ for `cron`, ✅ for `task`.** `Kind=task` lists **21** rows — every child/recurring activity the runbook names IS present: `task-cnpg-pair-bp-cnpg-pair-primary-1-initdb`, `task-cnpg-pair-bp-cnpg-pair-primary-2-join`, `task-newapi-bp-newapi-admin-seed`, `task-powerdns-zone-bootstrap`, `task-scan-vulnerabilityreport-*` (×10), `task-cutover-*`, `task-cert-nextkey-guard`, `task-cilium-envoy-tls-restart`, etc. **`Kind=cron` lists 0 rows** — the recurring-CronJob class is not ingested. **Mixed: task ✅ / cron ❌.**

**1.8 — ✅.** The one `kind:"reconciler"` row is `reconciler-sso-bridge-reconciler` with `status=healthy` — a continuously-running reconciler modelled with a *health* status, NOT a one-shot `succeeded`. **SEEN.**

**1.9 — ✅.** `kubectl scale deploy/sso-bridge-reconciler -n sso-bridge --replicas=0` → `deployment.apps/sso-bridge-reconciler scaled`. **DONE (live mutation).**

**1.10 — ✅.** Within ~5s, re-fetching the model: `reconciler-sso-bridge-reconciler status = degraded` (flipped from `healthy`). **SEEN — health tracks the live deployment.**
```
poll 1: reconciler-sso-bridge status = degraded   FLIPPED
```

**1.11 — ✅.** `kubectl scale … --replicas=1`; polling the model: it transitioned `degraded → failing → healthy` as `deployReady` returned to 1:
```
poll 1-9: model=failing  deployReady=<empty>
poll 10:  model=healthy  deployReady=1   RESTORED
```
Full round-trip `healthy → degraded → healthy` proven live. **SEEN.**

**1.12 — ⚠️ PARTIAL.** The trivy-scan failures ARE one-to-one: `kubectl get jobs -n trivy-system` shows **8 Failed `scan-vulnerabilityreport-*`** (+2 Complete) ↔ the model has **exactly 8 failed `task-scan-vulnerabilityreport-*`** rows (the 2 Complete ones appear non-failed). **But** the `openbao-snapshot-save` CronJob's Failed runs have **no** matching canvas row (see 1.2) — so the "no silent green" guarantee is **violated for the CronJob class**: a failing reconciler exists in `kubectl … | grep fail` with no red row. **PARTIAL — Jobs one-to-one ✅, CronJobs silently absent ❌.**

---

## Section 2 — Faithful read model: one typed list, ZERO fabrication (folds #3674)

*The "unified" view must be unified in the MODEL, not stitched + lied-about on the client.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 2.1 | `/jobs` | observe the whole table | no visual regression | ⏳ VISUAL-PENDING |
| 2.2 | DevTools → the `/jobs` XHR Response | every row carries a `kind` | no prefix-string guessing | ✅ |
| 2.3 | same JSON | lifecycle groups | real backend-derived status, not placeholder, not fake succeeded | ✅ |
| 2.4 | DevTools → bundle | search `applyHandoverStageOverride`/`mergeJobs`/`synthesizeJobFromFlowNode` | none present | ⏳ VISUAL-PENDING |
| 2.5 | **[xcheck]** terminal | curl the jobs JSON; every row has `kind`; flow rows present | honest statuses | ⚠️ PARTIAL |
| 2.6 | `/jobs` | perturb a HelmRelease; watch the row update via SSE | SSE tail of same model | ⏳ VISUAL-PENDING |

**2.1 — ⏳ VISUAL-PENDING.** "No visual regression" is a rendered-table judgement a headless curl cannot make. The backing model is intact (170 typed rows) so a regression is unlikely, but the visual is unproven here.

**2.2 — ✅.** Over the 170-row Response, **`missingKind:0`** — every row carries a populated `kind`. Distribution: `install:128 · task:21 · step:11 · group:4 · lifecycle:5 · reconciler:1`. Row fields include `kind`, `parentId`, `dependsOn`, `latestExecutionId`, `appId`, `type` — the typed model, no prefix-string guessing. **SEEN (raw JSON).**

**2.3 — ✅.** The lifecycle/group rows carry **honest backend-derived statuses, neither a rewritten placeholder nor a fake green**:
```
group  cutover       → failed      (step harbor-prewarm failed; 2 succeeded, 8 pending)
group  reconcilers   → failed      (8 failed child scan tasks)
group  bootstrap-kit → failed      (install-me-east-215-b-1:harbor failed, keycloak mid-flight)
group  provisioner   → succeeded
lifecycle tofu-init/plan/apply/output, cluster-bootstrap → succeeded
```
No group is coerced to a placeholder `pending` nor a premature `succeeded` — each reflects its real children. **SEEN.**

**2.4 — ⏳ VISUAL-PENDING.** Whether the FE bundle still contains `applyHandoverStageOverride`/`mergeJobs`/`synthesizeJobFromFlowNode` is a served-JS-bundle inspection (DevTools/Sources) outside curl scope on this walk. (Source-side: the de-merged `JobsPage` is what produced the single typed list this walk consumes — consistent with deletion — but I did not grep the served bundle.) Left ⏳.

**2.5 — ⚠️ PARTIAL.** The deployment-scoped canvas model: every row has `kind`, `missingKind:0`, and the **openova-flow rows ARE present in the raw REST payload** — `install-openova-flow-server`, `install-openova-flow-emitter` (+ their `me-east-215-b-1:` peers), all `install/succeeded`. ✅ for that part. **Caveat:** the literal `/api/v1/sovereign/jobs` endpoint the step curls is a **different, older projection** — it returns 88 rows whose `kind` is the raw K8s type (`HelmRelease`/`Job`), NOT the typed `install`/`task`/`step` taxonomy. The honest #3646 typed model lives at `/api/v1/deployments/{id}/jobs`, which I verified. **PARTIAL — typed model + flow rows ✅ on the canvas endpoint; the `/sovereign/jobs` endpoint is the legacy untyped one.**

**2.6 — ⏳ VISUAL-PENDING.** "Row updates via the SSE tail (not a racing snapshot)" requires watching the rendered row over an EventSource stream — a browser observation. The §1.10/1.11 reconciler flip already proves the *model* re-derives live within seconds, but the SSE-vs-REST rendering distinction is visual. Left ⏳.

---

## Section 3 — Remediation: every Failed row is ACTIONABLE from the UI (folds #3670)

*A view of reconciliations that cannot trigger a reconciliation is half a control plane.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 3.1 | `/jobs` | Status filter = **failed** | the failed rows render | ✅ |
| 3.2 | `/jobs` | hover a Failed row | a **Retry** control appears | ⏳ VISUAL-PENDING |
| 3.3 | `/jobs` | click **Retry** | toast + new Execution badge | ❌ (FE defect) |
| 3.4 | **[xcheck]** terminal | the Flux `requestedAt` annotation = just-now | UI wrote a Flux-native trigger | ✅ |
| 3.5 | `/jobs` | Run now on the cron row | one-off manual run | N/A |
| 3.6 | **[xcheck]** terminal | new manual Job in `openbao` ns | from the CronJob jobTemplate | N/A |
| 3.7 | the detail page | Executions: attempt N+1 first line `[retry] requested by …` | operator identity captured | ✅ |
| 3.8 | **[xcheck]** terminal (negative) | non-operator token → **403** | retry is RBAC-gated | ⏳ VISUAL-PENDING |
| 3.9 | **[xcheck]** terminal | `grep exec.Command …handler` | no shell-out | ✅ |

**3.1 — ✅.** Filtering the model for `status=failed` yields **15** rows that render in the list: `cutover` (group), `cutover-step-harbor-prewarm` (step), `reconcilers`/`bootstrap-kit` (groups), `task-cutover-harbor-prewarm-…`, `task-cilium-envoy-tls-restart`, 8× `task-scan-vulnerabilityreport-*`, `install-me-east-215-b-1:harbor`. **SEEN.**

**3.2 — ⏳ VISUAL-PENDING.** Whether a Retry control renders on hover (visible only for failed/degraded + operator RBAC) is a rendered-DOM fact. Source confirms `RetryJobButton` is gated to failed/degraded rows; the live render is ⏳. (The backend it would call is proven below.)

**3.3 — ❌ (the documented FE defect, reproduced live).** The button is wired to send the **composite `job.id`**; that path 404s:
```
POST …/jobs/ab2135d4cf2d01e4%3Atask-scan-vulnerabilityreport-5946b4b779/retry
  → HTTP 404  {"code":"404","error":"job-not-found","status":404}
```
Source root cause: `JobsTable.tsx:799` → `<RetryJobButton jobId={job.id} …>`; `RetryJobButton.tsx:62` → `…/jobs/${encodeURIComponent(jobId)}/retry` (the `:` becomes `%3A`, mangled by the proxy). So clicking Retry in the UI fails. **❌.** The *capability* works (see 3.4); only the button wiring is wrong.

**3.4 — ✅.** The **bare-name** retry (what the FE *should* send) succeeds and writes the Flux-native annotation:
```
POST …/jobs/task-scan-vulnerabilityreport-5946b4b779/retry
  → HTTP 200  {"action":"annotated Job scan-vulnerabilityreport-5946b4b779 for re-run",
               "executionId":"701201fa8469a141e9f0e7ac0b6f5800","kind":"task",
               "requestedAt":"2026-06-17T07:03:21Z","requestedBy":"emrah.baysal@openova.io"}
$ kubectl get job scan-vulnerabilityreport-5946b4b779 -n trivy-system -o jsonpath='{.metadata.annotations}'
  "reconcile.fluxcd.io/requestedAt":"2026-06-17T07:03:21.149149559Z"
```
The UI write path is a Flux-native annotation (timestamp matches the API response), not a shell-out, and the operator identity (`emrah.baysal@openova.io`) is audited. **SEEN.**

**3.5 / 3.6 — N/A.** "Run now" on `cron-openbao-snapshot-save` and the resulting manual Job in `openbao` ns are unreachable: there is **no cron row** on this canvas (see 1.2), so the Run-now control has no row to attach to. **N/A (cron row absent).**

**3.7 — ✅.** The retry response carries `executionId` (attempt N+1) and `requestedBy:"emrah.baysal@openova.io"`; the handler appends a log line on the new Execution. Live grep of the running code:
```
jobs_retry.go: st.AppendLogLines(depID, exec.ID, …)  +  "requestedBy" = operator sub
```
Operator identity is captured in the audit trail of the new attempt. **SEEN (response + handler).**

**3.8 — ⏳ VISUAL-PENDING.** The RBAC 403 negative could not be exercised: I authenticated with the operator handover JWT (role `sovereign-admin`); I did not have a non-operator/cross-tenant token to forge a 403 caller. The gate exists in source (`jobRetryCallerAuthorized` → 403) and is unit-tested (`TestRetryJob_Forbidden_Viewer`, `TestRetryJob_CrossTenant_404`), but the live 403 is unproven on this walk. Left ⏳ (not ✅ — unseen).

**3.9 — ✅.** No shell-out in the retry handler. `git show 4b0a9c6:…/jobs_retry.go | grep exec.Command` → **no match** (the only `exec.` tokens are the `exec.ID` Execution-struct field + the header comment "never an exec.Command / shell-out"). Remediation is annotation-via-dynamic-client only. **SEEN (live commit source).**

---

## Section 4 — Cutover EXECUTION honest projection (survivor #3646 / PR #3652 build-on)

*The dormant-install-vs-execution confusion (the founder catch on hw149) must stay fixed.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 4.1 | `/jobs` | open the activity view | a **`Cutover` group with 11 `cutover-step-*` rows** + `dependsOn` edges | ✅ |
| 4.2 | `/jobs` (mid-flight) | read the group state | running, **NEVER** premature "Succeeded" | ✅ |
| 4.3 | `/jobs` (after completion) | re-read | group flips to Succeeded only when `cutoverComplete=true` | N/A |

**4.1 — ✅.** The model has a `cutover` group (kind=`group`) parenting **exactly 11 `cutover-step-*` rows** (kind=`step`, `parentId=ab2135d4cf2d01e4:cutover`): `harbor-prewarm`, `harbor-projects`, `gitea-mirror`, `catalyst-api-env-patch`, `crossplane-provider-pivot`, `egress-block-test`, `flux-gitrepository-patch`, `gitea-token-mint`, `helmrepository-patches`, `registry-pivot`, `vcluster-registry-pivot`. Not a single opaque "install" row — the 11-step execution tree is present. **SEEN.**

**4.2 — ✅ (the key anti-pattern stays fixed).** The cutover group + steps are **NOT premature-Succeeded.** Live step statuses:
```
cutover (group)                          → failed
  cutover-step-harbor-prewarm            → failed
  cutover-step-harbor-projects           → succeeded
  cutover-step-gitea-mirror              → succeeded
  cutover-step-catalyst-api-env-patch    → pending
  cutover-step-crossplane-provider-pivot → pending
  cutover-step-egress-block-test         → pending
  cutover-step-flux-gitrepository-patch  → pending
  cutover-step-gitea-token-mint          → pending
  cutover-step-helmrepository-patches    → pending
  cutover-step-registry-pivot            → pending
  cutover-step-vcluster-registry-pivot   → pending
```
The dormant-install → premature-"Succeeded" confusion is **gone** — the group honestly reads `failed` (one real step failed; the rest still pending). **Correction to the prior writeup:** the prior doc claimed the group is `pending`; live it is `failed` (still honest, no fake green). **SEEN — honest, never-ahead-of-reality.**

**4.3 — N/A.** No completed cutover to observe (`cutoverComplete` is not true on this degraded env; the cutover steps are wedged behind the `bp-keycloak`→`bp-gitea` HR chain). The "flips to Succeeded only when `cutoverComplete=true`" transition is unreachable on this walk. **N/A (no live completion).**

---

## Section 5 — Generality (founder #4): ONE mechanism, not N hacks

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 5.1 | `/jobs` | confirm a Kustomization, a CronJob, a batch Job all render | all via the SAME ingestion | ⚠️ PARTIAL |
| 5.2 | `/jobs` | same **Retry** on an `install-*`, a `reconcile-*`, a `cron-*` | all via the SAME `…/retry` endpoint | ⏳ VISUAL-PENDING |
| 5.3 | `/jobs` (after a T5 DR switchover) | watch | switchover as an activity group, SAME `ActivityBridge` | N/A |
| 5.4 | **[xcheck]** `git diff` | inspect §5.1/5.2/5.3 diff | ZERO per-app/per-cloud branching | ✅ |

**5.1 — ⚠️ PARTIAL.** Two of three render via the one ingestion: **batch Jobs** (`task-*`, 21 rows) and **reconciler Deployments** (`reconciler-sso-bridge-reconciler`) are present, plus HelmReleases (`install-*`, 128) and the cutover steps — one `OnReconcilerObservation` with a kind switch. **But CronJobs (`cron-*`) do NOT render** (0 rows; see 1.2). So the "all three" claim (incl. CronJob) is not met — the generic bridge covers HR/Job/Deployment but not CronJob on this build. **PARTIAL.**

**5.2 — ⏳ VISUAL-PENDING.** That the SAME Retry control + SAME `POST …/jobs/{jobId}/retry` endpoint drives `install-*` / `reconcile-*` / `cron-*` is a multi-row UI exercise; the backend handler is provably one generic dispatch-on-kind (`jobs_retry.go` switch: HelmRelease/Kustomization/CronJob/Deployment/Job all annotate via one path), but driving it from the rendered UI across kinds is a browser walk — and is blocked anyway by the 3.3 FE defect + the absent cron rows. Left ⏳.

**5.3 — N/A.** No T5 DR switchover was triggered on this walk, so the switchover-as-activity-group / same-`ActivityBridge` claim is unobserved. **N/A.**

**5.4 — ✅.** The remediation handler is one generic primitive with a kind switch and **zero per-app/per-cloud branching**: `jobs_retry.go` dispatches on the leaf's typed `Kind` (install→annotate HR, reconcile→annotate Kustomization, reconciler→roll Deployment, cron→Job-from-template, task→bump annotation, mutation→re-submit XRC) — one handler, one annotation primitive (`reconcile.fluxcd.io/requestedAt`), no blueprint-specific code. A new blueprint surfaces as an `install-<chart>` row + becomes retry-able with the page/table/model untouched. **SEEN (live commit source).**

---

## Result

**Headline question — is `/jobs` ONE honest, complete, actionable canvas?** **Mostly, on the model layer — with two real gaps.**

- **Ingestion (§1):** HelmReleases, child Jobs (`task`), reconciler Deployments, and the 11-step cutover are all visible via one typed model; the `reconciler` health round-trips live. **GAP: CronJobs are not ingested** — a failing `openbao-snapshot-save` is invisible (no red row). **PARTIAL.**
- **Faithful model (§2):** every row carries a typed `kind` (`missingKind:0`, 170 rows); lifecycle/group statuses are honest backend-derived (no placeholder, no fake green); openova-flow rows are in the raw REST list. The bundle-deletion checks are ⏳ (DevTools). **PASS (model) / ⏳ (bundle).**
- **Remediation (§3):** the backend is a working Flux-native primitive — bare-name retry → 200 + writes `reconcile.fluxcd.io/requestedAt`, no shell-out, operator audited. **DEFECT: the FE button sends the composite `job.id` → 404** (fix: send `job.jobName`). RBAC-403 unproven on this walk (no non-operator token). **PARTIAL (capability ✅, button ❌).**
- **Cutover honesty (§4):** the 11-step execution is a real group+steps tree and is **NOT premature-Succeeded** (group=`failed`, mix of succeeded/pending) — the founder's hw149 catch stays fixed. **PASS** (the prior "pending" wording corrected to the live "failed/mixed").
- **Generality (§5):** one ingestion bridge + one remediation handler with a kind switch, zero per-app branching (source-verified). The cross-kind UI exercise is ⏳/blocked by the cron gap + FE defect. **PARTIAL.**

## Appendix — automated checks (NOT acceptance)

- `go test -race ./products/catalyst/bootstrap/api/internal/jobs …/handler` — not re-run on this walk (live-walk only).
- Backend unit tests asserting the contract DO exist on the live commit: `jobs_retry_test.go` (`TestRetryJob_Forbidden_Viewer`, `TestRetryJob_CrossTenant_404`, `TestRetryJob_Install_AnnotatesHR`, `TestRetryJob_Cron_RunNow_CreatesJob`, `TestRetryJob_NotRetryable_409`).
- **Follow-ups for a clean close:** (1) FE one-liner — `RetryJobButton`/`JobsTable` send `job.jobName` not `job.id`; (2) wire CronJob (`cron-*`) ingestion so failing CronJobs surface as red rows (the §1 premise); (3) re-walk on a CONVERGED env (fix the `bp-keycloak:1.4.30` chart-pull 404) so the cutover/handover steps execute, and exercise the RBAC-403 negative with a non-operator token + the visual/SSE/DevTools rows.
