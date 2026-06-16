# One honest /jobs canvas — ingestion + faithful read model + remediation — UAT walk

> **Ticket:** [#3646](https://github.com/openova-io/openova/issues/3646) · **Folds:** #3665 (ingestion breadth) · #3674 (faithful read model / no fabrication) · #3670 (remediation) · **Builds on:** PR #3652 (cutover projection) · **Train:** `train/hw150`
>
> **What this proves (founder #5):** the `/jobs` canvas is **ONE honest reconciliation view** — it ingests *every* reconciler (HelmReleases + Kustomizations + CronJobs + child Jobs + reconciler Deployments + the 11-step cutover + DR switchover), renders them from **one backend list with a typed `kind`** (no client-side 4-feed mashup, no fabricated statuses), shows status **never ahead of reality**, and lets the operator **remediate** any Failed row from the UI (retry / run-now / re-submit) without dropping to `kubectl`.
>
> **Format law (memory `feedback_uat_doc_must_be_ui_walk.md`):** every row is ONE UI action — `Go to <URL>` · `Do <click/type>` · `See <screen>`. `kubectl`/`curl`/`grep` rows are ground-truth cross-checks, marked **[xcheck]**, NOT the acceptance gate — acceptance is the founder walking the clickable rows. Replace `<fqdn>` = `hw150.omantel.biz` and `<JWT>` with the live handover token. Tick **☑** pass / **☒** fail.

**Sign-in (once).** Go to `https://console.<fqdn>/auth/handover?token=<JWT>` → you land on `/dashboard` **signed in as the sovereign-admin, zero password fields, no login form**. ☐

---

## Section 1 — Ingestion breadth: recurring/child/reconciler activities are VISIBLE (folds #3665)

*The install row is green; the real work behind it is failing. The canvas must show the failure.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 1.1 | `/jobs` | scroll/search to the `install-openbao` row | it is **green / Succeeded** (the chart installed once) | ☐ |
| 1.2 | `/jobs` | in the Status filter pick **failed** (or sort by status so red rows surface) | a **RED `cron-openbao-snapshot-save`** row appears **in the same table** as the green `install-openbao` — the DR-backup CronJob's failure is no longer invisible | ☐ |
| 1.3 | `/jobs` | confirm both rows are in one viewport | green install + red cron side by side — the canvas tells the truth (install ≠ healthy) | ☐ |
| 1.4 | `/jobs` | click the `cron-openbao-snapshot-save` row | its **detail page** opens with an **Executions list**, one Execution per `*/10` scheduled run | ☐ |
| 1.5 | the cron detail page | read the latest Execution's log | the line `FATAL: bao login returned no client_token — the openbao-snapshot role may not yet be bound by auth-bootstrap` | ☐ |
| 1.6 | `/jobs` | filter / sort to find `task-cnpg-pair-...-primary-3-join` | the **stuck HA-join child Job** appears as a `task-*` row in **running** (not invisible) — the degraded 2/3 HA is surfaced | ☐ |
| 1.7 | `/jobs` | set the **Kind** filter to `cron` then `task` | every recurring + child-job activity is listed (`openbao-snapshot-save`, `guacamole-admin-enroll`, `newapi-admin-promote`, `bp-flux-stuck-hr-recovery`, `syft-grype`, the cnpg joins), each with its own status | ☐ |
| 1.8 | `/jobs` | find `reconciler-sso-bridge` (Kind=`reconciler`) | it shows a **health** status (`healthy`), **NOT** a one-shot "Succeeded" — a continuously-running reconciler is modelled distinctly from an install | ☐ |
| 1.9 | terminal | `kubectl --kubeconfig /tmp/hw150.kubeconfig scale deploy/sso-bridge-reconciler -n sso-bridge --replicas=0` | (perturb the reconciler) | ☐ |
| 1.10 | `/jobs` | refresh, re-read `reconciler-sso-bridge` | health flips **healthy → degraded** | ☐ |
| 1.11 | terminal | `kubectl --kubeconfig /tmp/hw150.kubeconfig scale deploy/sso-bridge-reconciler -n sso-bridge --replicas=1`; refresh `/jobs` | health flips back **degraded → healthy** | ☐ |
| 1.12 | **[xcheck]** terminal | `kubectl --kubeconfig /tmp/hw150.kubeconfig get cronjobs,jobs,kustomizations -A \| grep -i fail` | every failed reconciler in this output has a matching RED row on `/jobs` — one-to-one, **no silent green** | ☐ |

---

## Section 2 — Faithful read model: one typed list, ZERO fabrication (folds #3674)

*The "unified" view must be unified in the MODEL, not stitched + lied-about on the client.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 2.1 | `/jobs` | observe the whole table | **no visual regression** vs before the refactor — the same rows render | ☐ |
| 2.2 | `/jobs` | open DevTools → Network → the `/jobs` (or `/api/v1/sovereign/jobs`) XHR → Response | **every row carries a `kind` field** (`install`/`reconcile`/`cron`/`task`/`reconciler`/`step`/`mutation`/`lifecycle`) — no more prefix-string guessing | ☐ |
| 2.3 | same Response JSON | find the `Apps` / `Handover` / `Cutover` lifecycle groups | each carries a **real backend-derived status**, NOT a placeholder `pending` the client rewrites — and **not** a fake `succeeded` either (honest, derived from the deployment lifecycle) | ☐ |
| 2.4 | DevTools → Sources/bundle | search the page bundle for `applyHandoverStageOverride` and `mergeJobs` and `synthesizeJobFromFlowNode` | **none present** — the client-side merge + status-coercion code is deleted; the page renders the single list (+ an SSE tail) | ☐ |
| 2.5 | **[xcheck]** terminal | `curl -s https://console.<fqdn>/api/v1/sovereign/jobs \| python3 -c 'import json,sys; [print(j["jobName"], j.get("kind"), j["status"]) for j in json.load(sys.stdin)["jobs"]]'` | every row has a populated `kind`; lifecycle groups show an honest status; `openova-flow-server`/`openova-flow-emitter` rows are present **in the raw REST payload** (no longer only in the flow SSE) | ☐ |
| 2.6 | `/jobs` | perturb a HelmRelease (`kubectl --kubeconfig /tmp/hw150.kubeconfig annotate hr bp-grafana -n flux-system reconcile.fluxcd.io/requestedAt="$(date -u +%FT%TZ)" --overwrite`) and watch the row | the row updates via the **SSE tail of the same model** — not a separate flow-snapshot reconciliation racing the REST list | ☐ |

---

## Section 3 — Remediation: every Failed row is ACTIONABLE from the UI (folds #3670)

*A view of reconciliations that cannot trigger a reconciliation is half a control plane. One generic Flux-native primitive, no shell-out.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 3.1 | `/jobs` | Status filter = **failed** | the failed / not-Ready rows render (`install-velero`, `install-hcloud-ccm`, `install-cluster-autoscaler-hcloud`, `cron-openbao-snapshot-save`, the trivy scans) | ☐ |
| 3.2 | `/jobs` | hover the Failed `install-velero` row | a **Retry** control appears at the row end (visible only for failed/degraded rows + operator RBAC) | ☐ |
| 3.3 | `/jobs` | click **Retry** on `install-velero` | a toast "reconcile requested"; a **new Execution** badge appears on the row (attempt N+1) | ☐ |
| 3.4 | **[xcheck]** terminal | `kubectl --kubeconfig /tmp/hw150.kubeconfig get hr bp-velero -n flux-system -o jsonpath='{.metadata.annotations.reconcile\.fluxcd\.io/requestedAt}'` | the annotation = the **just-now timestamp** — the UI wrote a Flux-native reconcile trigger, not a shell-out | ☐ |
| 3.5 | `/jobs` | click into the `cron-openbao-snapshot-save` row → press **Run now** (confirm if prompted) | a one-off manual run is created | ☐ |
| 3.6 | **[xcheck]** terminal | `kubectl --kubeconfig /tmp/hw150.kubeconfig get jobs -n openbao` | a **new manually-triggered Job** (from the CronJob `jobTemplate`) is present, distinct from the scheduled `...-296xxxxx` runs | ☐ |
| 3.7 | the activity detail page | open the Executions list for the row you retried | **attempt N+1** is present; its **first log line** reads `[retry] requested by <operator-sub> at <ts>` — operator identity captured in the audit trail | ☐ |
| 3.8 | **[xcheck]** terminal (negative) | `curl -s -o /dev/null -w "%{http_code}" -X POST https://console.<fqdn>/api/v1/deployments/<depId>/jobs/install-velero/retry -H "Authorization: Bearer <NON-OPERATOR-TOKEN>"` | **403** — the retry endpoint is RBAC-gated (read-only viewers + cross-tenant deployment ids denied) | ☐ |
| 3.9 | **[xcheck]** terminal | `grep -rn "exec.Command" products/catalyst/bootstrap/api/internal/handler` | the retry handler does **NOT** add a shell-out — remediation is annotation-via-dynamic-client only (PRINCIPLES.md #61) | ☐ |

---

## Section 4 — Cutover EXECUTION honest projection (survivor #3646 / PR #3652 build-on)

*The dormant-install-vs-execution confusion (the founder catch on hw149) must stay fixed.*

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 4.1 | `/jobs` (during a live cutover) | open the activity view | a **`Cutover` group with its 11 `cutover-step-*` rows** + `dependsOn` edges — not a single opaque "install" row | ☐ |
| 4.2 | `/jobs` (mid-flight, `cutoverComplete=false`) | read the group state | the group / steps show **running**, **NEVER** a premature "Succeeded" (the `install-self-sovereign-cutover` dormant-install confusion is gone) | ☐ |
| 4.3 | `/jobs` (after completion) | re-read | the group flips to Succeeded **only** when `cutoverComplete=true` | ☐ |

---

## Section 5 — Generality (founder #4): ONE mechanism, not N hacks

| # | Go to (URL) | Do | See (expect) | ☐ |
|---|---|---|---|---|
| 5.1 | `/jobs` | confirm a Kustomization (`reconcile-*`), a CronJob (`cron-*`), and a batch Job (`task-*`) all render | all three appear via the SAME ingestion — one generic `OnReconcilerObservation`, kind switch only | ☐ |
| 5.2 | `/jobs` | use the SAME **Retry** control on an `install-*`, a `reconcile-*` (Kustomization), and a `cron-*` | all three remediate via the SAME `POST …/jobs/{jobId}/retry` endpoint (one handler + a kind switch) | ☐ |
| 5.3 | `/jobs` (after triggering a T5 DR switchover) | watch | the switchover appears as an activity group with steps + edges — the **SAME `ActivityBridge`** as the cutover, no bespoke view | ☐ |
| 5.4 | **[xcheck]** `git diff` | inspect the diff for §5.1/§5.2/§5.3 | **ZERO per-app/per-cloud branching**; adding a brand-new blueprint produces its row + becomes actionable with `JobsPage`/`JobsTable`/the Activity model **untouched** | ☐ |

---

## Result

**Headline question — is `/jobs` ONE honest, complete, actionable canvas?**

- **Ingestion (§1):** every recurring/child/reconciler activity is visible; a failing CronJob/child-Job/reconciler renders red while its green install stays green; `kubectl … | grep fail` is one-to-one with the red rows. ☐
- **Faithful model (§2):** every row carries a typed `kind`; the client 4-feed mashup + `applyHandoverStageOverride` fabrication are gone; lifecycle groups carry honest backend-derived status; openova-flow rows are in the raw REST list. ☐
- **Remediation (§3):** every Failed row is actionable (retry / run-now / re-submit) via one Flux-native primitive; RBAC-gated (403 for non-operator); no shell-out; operator identity audited. ☐
- **Cutover honesty (§4):** the 11-step execution shows running-until-complete, never a premature Succeeded. ☐
- **Generality (§5):** one ingestion bridge, one remediation endpoint, one ActivityBridge — zero per-app code; a new blueprint surfaces + is actionable automatically. ☐

Every ticked box gets a screenshot committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`.

## Appendix — automated checks (NOT acceptance)

- `go test -race ./products/catalyst/bootstrap/api/internal/jobs ./products/catalyst/bootstrap/api/internal/helmwatch ./products/catalyst/bootstrap/api/internal/handler` green.
- `activity_bridge_test.go` (group+steps+edges, failed-step→group-failed) + `cutover_activity_bridge_test.go` (durable replay = running, never Succeeded).
- New: ingestion test for `OnReconcilerObservation` across CronJob/Job/Kustomization/Deployment kinds (zero per-source branching); FE unit tests for the de-merged `JobsPage` (deleted helpers' tests removed, not skipped) + the Kind column + the Retry control; a regression test proving no Apps/Handover/Cutover phantom row is coerced; a handler test proving **403** on an unauthorized / cross-tenant retry caller.
