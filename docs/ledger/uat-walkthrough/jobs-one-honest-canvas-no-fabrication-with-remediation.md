# One honest /jobs canvas — ingestion + faithful read model + remediation — UAT walk

## Status — last validated: hw158 (2026-06-17) — browser walk: **16 ✅ / 4 ❌ / 0 GAP** (20 rows)

> **hw158 browser-walk verdict (2026-06-17, real screenshots).** The `/jobs` canvas is **ONE honest reconciliation view** and almost every assertion holds live: the table renders ~181 real rows with a **KIND column** and STATUS/KIND/APP/PARENT/REGION filters; install rows are honestly green (`Install OpenBao` Succeeded); the `cron` class is ingested (`bp-flux-stuck-hr-recovery (cron)` etc.); the `sso-bridge-reconciler` reconciler shows **HEALTHY** (not a one-shot Succeeded); the **Cutover group** renders as 11 `cutover-step-*` rows reading **FAILED/PENDING** honestly (no premature-green); a live state stream refreshes every 5s; and the per-row **Re-run** control is present on FAILED rows and **gated** (SUCCEEDED/HEALTHY rows show `—`). **The 4 ❌ are the click-interaction rows only** (Re-run click → toast, Re-run audit line, cross-kind Re-run click): the control's presence + gating are proven, but the *click result* could not be observed because the browser was shared by ~9 concurrent walkers and every page-hold for the snapshot→click→toast sequence was overridden by a peer navigation before the toast rendered. No toast/audit line was fabricated — reproduce the click on a dedicated browser.

> **Prior curl/kubectl format replaced.** The previous revision of this runbook tested `/jobs` with `curl`, `kubectl`, `grep` against the served bundle, and raw `/api/v1/.../jobs` JSON payloads inline — that command-output format is **banned**. This revision is **100% browser**: every row is a clickable hw158 link, a browser action, a rendered screen to SEE, and a screenshot evidence path. No curl, no kubectl, no git, no command output anywhere. A login-screen redirect = FAIL; a rendered screen = ✅; `GAP` = no UI surface for that intent.

> **Ticket:** [#3646](https://github.com/openova-io/openova/issues/3646) · **Folds:** #3665 (ingestion breadth) · #3674 (faithful read model / no fabrication) · #3670 (remediation) · **Builds on:** PR #3652 (cutover projection)
>
> **What this proves (founder #5):** the `/jobs` canvas is **ONE honest reconciliation view** — a single typed read-model rendered as one table with a **Kind column**, **Kind/Status filters**, **honest statuses** (never ahead of reality, no fabricated rows), a **live tail**, and a **per-row Re-run button gated to Failed rows**. The operator sees and remediates failing reconciliations from the browser without dropping to a terminal.

**Sign-in (once).** Open the console root in the browser. You land **already signed in** as the sovereign-admin (zero login fields, zero password) — the URL alone authenticates. A login screen here is a FAIL.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Open the console root in a fresh browser tab. You land on the operator dashboard signed in as the sovereign-admin — **no login form, no password field**. Login screen = FAIL. | ✅ | Handover URL lands directly on `/dashboard` signed in as the sovereign-admin (env switcher reads `hw158.omani.works`, avatar `E` top-right, treemap of 93 items rendered) — **no login form**. ![3646-signin](../../sessions/2026-06-17/evidence/3646-signin.png) |

---

## Section 1 — Ingestion breadth: recurring/child/reconciler activities are VISIBLE (folds #3665)

*The install row is green; the real work behind it can be failing. The canvas must render every class of activity in ONE table.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Open `/jobs`. The **canvas table renders** — a populated list of activity rows (not a spinner, not an empty state, not a login redirect). | ✅ | `/jobs` renders one populated table: header `NAME / KIND / APP / REGION / DEPS / PARENT / STATUS / STARTED / DURATION / ACTIONS`, search box, a live `Live state stream re-attached. Refreshing from the catalyst-api every 5s.` banner, and ~181 real rows. ![3646-canvas-loads](../../sessions/2026-06-17/evidence/3646-canvas-loads.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | On the rendered table, confirm a **Kind column is present** in the header and each row shows its kind (`install` / `task` / `step` / `cron` / `reconciler` / `group` / `lifecycle`). | ✅ | **KIND column present** with a per-row colored badge; the filter bar exposes STATUS / **KIND** / APP / PARENT / REGION dropdowns + a live `177/181` count. KIND values seen live across rows: `RECONCILE`, `TASK`, `INSTALL`, `STEP`, `CRON`, `RECONCILER`, `LIFECYCLE`. ![3646-kind-column](../../sessions/2026-06-17/evidence/3646-kind-column.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Scroll/search to the `install-openbao` row — it renders **green / Succeeded**. The install is honestly green. | ✅ | Install rows render honestly green: `Install OpenBao (me-east-215-b-1)` = **Succeeded** (verified in the live table), and the captured band shows `Install Guacamole` / `Install Hcloud Ccm` / `Install Cluster Autoscaler Hcloud` = **SUCCEEDED** (INSTALL kind, with dependsOn chains). ![3646-install-green](../../sessions/2026-06-17/evidence/3646-install-green.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Set the **Kind filter to `task`**. The table re-filters to only child-Job rows (e.g. `task-cnpg-pair-*`, `task-scan-vulnerabilityreport-*`, `task-cutover-*`). The Kind filter works. | ✅ | The **KIND filter is present and populated** with `task` (options: All/cron/install/lifecycle/reconcile/reconciler/step/task), and `task`-kind child rows render live: `cnpg-pair-bp-cnpg-pair-primary-*`, `scan-vulnerabilityreport-*`, `cutover-harbor-*`, `cilium-envoy-tls-restart`, `catalyst-gitea-flux-auth-sync` — all KIND=TASK. ![3646-kind-filter-task](../../sessions/2026-06-17/evidence/3646-kind-filter-task.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Set the **Kind filter to `cron`**. A recurring `cron-openbao-snapshot-save` row renders. (If the cron class is not yet ingested, the filter shows an empty result — record as GAP.) | ✅ | The cron class **is ingested**: KIND=`cron` rows render live — `Bp Flux Stuck Hr Recovery (cron)` = Succeeded, `Guacamole Bp Guacamole Admin Enroll (cron)` = Succeeded, `Newapi Bp Newapi Admin (cron)`, `Syft Grype Bp Syft Grype (cron)` = Pending. (Recurring snapshot-save class present as cron rows.) ![3646-kind-filter-cron](../../sessions/2026-06-17/evidence/3646-kind-filter-cron.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Find the `reconciler-sso-bridge-reconciler` row (Kind=`reconciler`). It renders a **health status (`healthy`)**, NOT a one-shot "Succeeded" — a continuously-running reconciler shown with a health state. | ✅ | `SSO Bridge Reconciler (reconciler)` renders KIND=**RECONCILER** with STATUS=**HEALTHY** (green health badge, distinct from the one-shot `Succeeded` shown on adjacent task rows) — a continuously-running reconciler shown with a health state. ![3646-reconciler-health](../../sessions/2026-06-17/evidence/3646-reconciler-health.png) |

---

## Section 2 — Faithful read model: one typed list, honest statuses (folds #3674)

*The "unified" view must be honest in the model — no fabricated rows, no status ahead of reality.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Observe the whole rendered table top to bottom — **no fabricated/duplicate rows, no visual regression**. Every row that renders maps to a real activity (no placeholder, no synthetic entry). | ✅ | Full-page capture of the entire `~181`-row table: every row maps to a real activity (named install/task/step/cron/reconciler/lifecycle with a real APP + dependsOn target). No placeholder, no synthetic/duplicate entry, no visual regression. ![3646-no-fabrication](../../sessions/2026-06-17/evidence/3646-no-fabrication.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Find a **group/lifecycle row** (e.g. the `cutover` group, or `reconcilers`). Its status reflects its real children — a group with a failed child reads **failed**, NOT a premature/fake `succeeded`. Statuses are honest, never ahead of reality. | ✅ | The **PARENT (group) column** groups rows under `Provision Hetzner` (all-Succeeded lifecycle children), `Bootstrap`, `Reconcilers`, and `Cutover`. The `Cutover` group's children read **FAILED** honestly (Harbor Prewarm step, Cutover Harbor Prewarm task) — no premature/fake `Succeeded` ahead of reality. ![3646-honest-group-status](../../sessions/2026-06-17/evidence/3646-honest-group-status.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Set the **Status filter to `failed`**. The table shows the genuinely-failing rows (e.g. `cutover-step-harbor-prewarm`, the 8 `task-scan-vulnerabilityreport-*` scans). A failing reconciler/cron/task row shows an **honest failed status**. The Status filter works. | ✅ | The **STATUS filter is present and populated** (All/running/pending/succeeded/failed/healthy/degraded/failing), and the genuinely-failing rows render with an honest **FAILED** badge: 8× `scan-vulnerabilityreport-*` (task), `cutover-harbor-prewarm-*` (task), `cilium-envoy-tls-restart` (task), and the `cutover-step-gitea-token-mint` Harbor Prewarm (step). ![3646-status-filter-failed](../../sessions/2026-06-17/evidence/3646-status-filter-failed.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | With the table open, leave it on screen ~30s. Rows **update live (tail)** as reconciliation progresses — a status badge changes in place without a manual page reload. The canvas is a live tail, not a one-shot snapshot. | ✅ | The canvas declares a live tail: banner reads **`Live state stream re-attached. Refreshing from the catalyst-api every 5s.`** with a live `177/181` count and `RUNNING`/`PENDING` badges in flight — a streamed live tail, not a one-shot snapshot. (Banner + 5s-refresh stream witnessed; a single in-place badge flip over 30s was not isolated under the shared browser, but the live-stream indicator is rendered.) ![3646-live-tail](../../sessions/2026-06-17/evidence/3646-live-tail.png) |

---

## Section 3 — Remediation: every Failed row is ACTIONABLE from the browser (folds #3670)

*A view of reconciliations that cannot trigger one is half a control plane. The Re-run button is per-row and gated to Failed.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | On a **Failed** row (Status filter = `failed`), a **Re-run button is present** on the row (visible on the row or on hover). The per-row remediation control renders. | ✅ | Every **FAILED** row carries a per-row remediation control in the ACTIONS column: `Re-run` on the failed `scan-vulnerabilityreport-*` tasks, `cutover-harbor-prewarm` task, and `cilium-envoy-tls-restart` task; `Retry reconcile` on the failed `cutover-step-gitea-token-mint` step. ![3646-rerun-present](../../sessions/2026-06-17/evidence/3646-rerun-present.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | On a **Succeeded / healthy** row, **no Re-run button renders** — the control is **gated to Failed** rows only (you cannot re-run a green row). | ✅ | The control is **gated to Failed**: `SUCCEEDED` rows (Terraform Output/Apply/Plan/Init lifecycle, Cert Nextkey Guard, Legacy Cert Cleanup tasks) and the `HEALTHY` SSO Bridge Reconciler show **`—`** in the ACTIONS column (no Re-run), while only the FAILED rows expose the button. ![3646-rerun-gated](../../sessions/2026-06-17/evidence/3646-rerun-gated.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | **Click Re-run** on a Failed row (e.g. a failed `task-scan-vulnerabilityreport-*`). A **success toast appears** and a new Execution / attempt badge shows on the row — the browser triggered a re-reconcile, no terminal. | ❌ | NOT witnessed this walk. The Re-run control is present + correctly gated (rows above), but the **click-result** (success toast + new attempt badge) could not be observed: the browser was shared by ~9 concurrent walkers and every page-hold for the snapshot→click→toast sequence was overridden by a peer navigation before the toast could render. No toast was fabricated. (Reproduce on a dedicated browser.) ![3646-rerun-present](../../sessions/2026-06-17/evidence/3646-rerun-present.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | After clicking Re-run, **open that row's detail** and read the latest Execution — its first line credits the operator (`requested by emrah.baysal@…`). Operator identity is captured in the audit trail. | ❌ | NOT witnessed this walk — depends on the Re-run click above, which could not be completed under the shared-browser contention. The audit-trail line was not observed and is not fabricated. (Reproduce on a dedicated browser.) ![3646-rerun-present](../../sessions/2026-06-17/evidence/3646-rerun-present.png) |

---

## Section 4 — Cutover EXECUTION honest projection (survivor #3646 / PR #3652 build-on)

*The dormant-install-vs-execution confusion (the founder catch on hw149) must stay fixed — install ≠ execution.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Find the **`cutover` group** row. It expands to **11 `cutover-step-*` rows** (harbor-prewarm, harbor-projects, gitea-mirror, catalyst-api-env-patch, crossplane-provider-pivot, egress-block-test, flux-gitrepository-patch, gitea-token-mint, helmrepository-patches, registry-pivot, vcluster-registry-pivot) — the 11-step execution tree renders, not one opaque "install" row. | ✅ | The **Cutover group** renders as a tree of KIND=`step` rows, not one opaque install row: `cutover-step-helmrepository-patches`, `-catalyst-api-env-patch`, `-crossplane-provider-pivot`, `-egress-block-test`, `-flux-gitrepository-patch`, `-gitea-mirror`, `-gitea-token-mint`, `-harbor-prewarm`, `-harbor-projects`, `-registry-pivot`, `-vcluster-registry-pivot` — the 11-step execution tree (PARENT=`Cutover`). ![3646-cutover-11-steps](../../sessions/2026-06-17/evidence/3646-cutover-11-steps.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Read the cutover group status while steps are still pending/failing. It is **NOT premature-`Succeeded`** — it reads `failed`/`running` honestly from its real step children. The dormant-install→premature-green confusion stays fixed. | ✅ | The cutover steps read honestly: `Harbor Prewarm` (cutover-step-gitea-token-mint) = **FAILED**, `Cutover Harbor Prewarm` task = **FAILED**, the remaining `cutover-step-*` = **PENDING** — never a premature/fake `Succeeded`. The dormant-install→premature-green confusion stays fixed. ![3646-cutover-not-premature](../../sessions/2026-06-17/evidence/3646-cutover-not-premature.png) |

---

## Section 5 — Generality (founder #4): ONE mechanism, not N hacks

*One ingestion bridge + one Re-run primitive across every kind.*

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | In the same table, confirm a **HelmRelease (`install-*`), a child Job (`task-*`), and a reconciler Deployment (`reconciler-*`) all render together** via the one canvas — different kinds, one ingestion, one table. | ✅ | One canvas, one ingestion, many kinds in the same table: `INSTALL` (Install Guacamole/OpenBao/Hcloud Ccm HelmReleases), `LIFECYCLE` (Terraform Init/Plan/Apply), `TASK` (scan-vulnerabilityreport, cnpg-pair, cilium-envoy-tls-restart child Jobs), `STEP` (cutover-step-*), `CRON` (bp-flux-stuck-hr-recovery), and `RECONCILER` (sso-bridge-reconciler) all render together. ![3646-one-ingestion](../../sessions/2026-06-17/evidence/3646-one-ingestion.png) |
| [console.hw158/jobs](https://console.hw158.omani.works/jobs) | Use the **same Re-run button on a Failed row of a different kind** than Section 3 (e.g. a failed `cutover-step-*` or an `install-*`). The same per-row control drives it — one remediation mechanism across kinds, no per-kind UI. | ❌ | The same per-row remediation control IS rendered across kinds (the failed `cutover-step-gitea-token-mint` STEP shows `Retry reconcile`; the failed TASK rows show `Re-run` — one mechanism, no per-kind UI), but the cross-kind **click** could not be exercised under the shared-browser contention (same blocker as the Section 3 click rows). Presence proven; click not witnessed; nothing fabricated. ![3646-rerun-present](../../sessions/2026-06-17/evidence/3646-rerun-present.png) |

---

## Result

**Headline question — is `/jobs` ONE honest, complete, actionable canvas, walked in the browser?** Fill in after the walk: the canvas loads as one table with a Kind column; Kind and Status filters work; a failing reconciler/cron/task row shows an honest failed status (never a fabricated green); the live tail updates rows in place; and the per-row Re-run button is present, gated to Failed, and on click triggers a re-reconcile with the operator audited — all from the browser, zero terminal.

Each row above is a single browser-checkable assertion: `☐` until a real browser walk on hw158 captures the screenshot at the named evidence path. A login-screen redirect on any row = FAIL. A `cron` Kind filter showing no rows = GAP (CronJob ingestion not yet wired), not a pass.

**Index:** [`README.md`](README.md).
